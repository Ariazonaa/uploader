package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// purgeFileBlobs removes the blob, thumbnail and HLS rendition of a file whose
// row was just deleted, skipping keys a new file with the same slug has taken
// over in the meantime (see upload.PurgeBlobs).
func (s *Server) purgeFileBlobs(ctx context.Context, f store.File) {
	upload.PurgeBlobs(ctx, s.store, s.storage, f)
}

// rawURLFor builds the public raw URL for a file, ending in the file's real
// extension (e.g. /raw/aB3kdq.mp4). The extension makes chat clients like
// Discord auto-embed the media; handleRaw strips it back to the slug.
func rawURLFor(base string, f store.File) string {
	return base + "/raw/" + f.Slug + upload.PreferredExt(f.OriginalName, f.MimeType)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

type fileView struct {
	ID         uuid.UUID  `json:"id"`
	Slug       string     `json:"slug"`
	Name       string     `json:"name"`
	MimeType   string     `json:"mime_type"`
	SizeBytes  int64      `json:"size_bytes"`
	Width      *int       `json:"width,omitempty"`
	Height     *int       `json:"height,omitempty"`
	DurationMs *int64     `json:"duration_ms,omitempty"`
	Views      int64      `json:"views"`
	CreatedAt  time.Time  `json:"created_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	URL        string     `json:"url"`
	Raw        string     `json:"raw"`
	Thumb      string     `json:"thumb,omitempty"`
	Encrypted  bool       `json:"encrypted"`
}

func toFileView(f store.File, base string) fileView {
	v := fileView{
		ID: f.ID, Slug: f.Slug, Name: f.OriginalName, MimeType: f.MimeType,
		SizeBytes: f.SizeBytes, Width: f.Width, Height: f.Height, DurationMs: f.DurationMs,
		Views: f.Views, CreatedAt: f.CreatedAt, DeletedAt: f.DeletedAt,
		URL: base + "/s/" + f.Slug, Raw: rawURLFor(base, f), Encrypted: f.Encrypted,
	}
	if f.ThumbKey != nil {
		v.Thumb = base + "/thumb/" + f.Slug
	}
	return v
}

// maxSearchRunes caps the file search term. The search compares trigrams of
// the term with every one of the user's file names, so its cost grows with the
// term's length, and the request line alone allows a term of almost 1 MB: a
// dozen such searches tied up the whole connection pool. Longer terms are cut
// (store.ListFiles also bounds the query time).
const maxSearchRunes = 200

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	q := r.URL.Query()
	limit := clampInt(atoiDefault(q.Get("limit"), 60), 1, 200)
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	// order defaults to descending unless explicitly "asc".
	desc := q.Get("order") != "asc"
	files, err := s.store.ListFiles(r.Context(), u.ID, store.FileQuery{
		Search:     truncate(q.Get("q"), maxSearchRunes),
		MimePrefix: q.Get("type"),
		Sort:       q.Get("sort"),
		Desc:       desc,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list files")
		return
	}
	base := s.publicBaseURL(r)
	views := make([]fileView, 0, len(files))
	for _, f := range files {
		views = append(views, toFileView(f, base))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleRenameFile changes a file's display name.
func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := sanitizeFilename(req.Name)
	if name == "" || len([]rune(name)) > 255 {
		writeErr(w, http.StatusBadRequest, "name must be 1–255 characters")
		return
	}
	if err := s.store.RenameFile(r.Context(), id, u.ID, name); err != nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "renamed", "name": name})
}

// handleBulkDeleteFiles deletes several of the user's files in one request.
func (s *Server) handleBulkDeleteFiles(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 500 {
		writeErr(w, http.StatusBadRequest, "provide 1–500 ids")
		return
	}
	// Malformed ids are skipped, as are (in the store) missing, foreign and
	// already trashed ones; the whole batch is a single UPDATE.
	ids := make([]uuid.UUID, 0, len(req.IDs))
	for _, raw := range req.IDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, id)
		}
	}
	deleted := 0
	if len(ids) > 0 {
		n, err := s.store.DeleteFiles(r.Context(), ids, u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not delete files")
			return
		}
		deleted = int(n)
	}
	// One aggregate audit row for the whole batch (not per file).
	if deleted > 0 {
		s.audit(r, store.AuditFileDelete, "bulk", map[string]any{"count": deleted})
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

// handleDeleteFile moves a file to the trash (soft delete). Blobs are kept so it
// can be restored; the janitor purges them after the trash-retention window.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	f, err := s.store.DeleteFile(r.Context(), id, u.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	s.audit(r, store.AuditFileDelete, f.Slug, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "trashed"})
}

// handleListTrash returns a page of the user's soft-deleted files, paged like
// handleListFiles: the trash can hold any number of files, and all of them
// used to be loaded and encoded in one response.
func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	q := r.URL.Query()
	limit := clampInt(atoiDefault(q.Get("limit"), 60), 1, 200)
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	files, err := s.store.ListTrash(r.Context(), u.ID, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list trash")
		return
	}
	base := s.publicBaseURL(r)
	views := make([]fileView, 0, len(files))
	for _, f := range files {
		views = append(views, toFileView(f, base))
	}
	writeJSON(w, http.StatusOK, views)
}

// handleRestoreFile restores a soft-deleted file from the trash.
func (s *Server) handleRestoreFile(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	f, err := s.store.RestoreFile(r.Context(), id, u.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not in trash")
		return
	}
	s.audit(r, store.AuditFileRestore, f.Slug, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

// handlePurgeFile permanently deletes a single trashed file (and its blobs).
func (s *Server) handlePurgeFile(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file id")
		return
	}
	f, err := s.store.HardDeleteFile(r.Context(), id, u.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not in trash")
		return
	}
	s.purgeFileBlobs(r.Context(), f)
	s.audit(r, store.AuditFilePurge, f.Slug, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleEmptyTrash permanently deletes all of the user's trashed files.
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	files, err := s.store.EmptyTrash(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not empty trash")
		return
	}
	for _, f := range files {
		s.purgeFileBlobs(r.Context(), f)
	}
	s.audit(r, store.AuditFilePurge, "empty-trash", map[string]any{"count": len(files)})
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(files)})
}
