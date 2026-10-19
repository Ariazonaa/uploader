package api

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/blobcrypt"
	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// handleChangePassword updates the current user's password after verifying the
// current one, then revokes every other session. Changing a password is the
// standard response to "someone else may be in my account", so leaving the
// attacker's session alive would defeat the point. API tokens are deliberately
// left alone: they are separate credentials the user manages explicitly, and
// silently invalidating them would break ShareX and every other integration.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !s.verifyAccountPassword(w, r, u, req.CurrentPassword) {
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		writeErr(w, http.StatusBadRequest, "new "+err.Error())
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.store.UpdatePassword(r.Context(), u.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update password")
		return
	}
	// Keep the caller signed in, drop everyone else. A failure here must not make
	// the (already applied) password change look unsuccessful, so it is logged
	// and reported alongside the result rather than turned into a 500.
	revoked, err := s.revokeOtherSessions(r, u.ID)
	if err != nil {
		log.Printf("account: revoke other sessions for user %s: %v", u.ID, err)
	}
	s.audit(r, store.AuditPasswordChange, u.Username, map[string]any{"sessions_revoked": revoked})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "sessions_revoked": revoked})
}

// verifyAccountPassword re-checks the signed-in user's password before a
// sensitive account action (password change, 2FA setup/enable/disable,
// account deletion) and writes the error response when the check fails.
//
// Attempts are charged to the same per-account buckets as failed logins. A
// stolen session must not turn these endpoints into an unthrottled password
// oracle, and guesses made here must not come on top of
// what the login form already allows. As with a login, the correct password
// is refunded.
func (s *Server) verifyAccountPassword(w http.ResponseWriter, r *http.Request, u *store.User, password string) bool {
	attempt, ok, retry := s.chargePasswordAttempt(r, u.Username)
	if !ok {
		writeRateLimited(w, retry, "too many failed password attempts for this account, try again later")
		return false
	}
	if !auth.VerifyPassword(u.PasswordHash, password) {
		writeErr(w, http.StatusUnauthorized, "password is incorrect")
		return false
	}
	attempt.succeeded()
	return true
}

// revokeOtherSessions deletes every session of the user except the one making
// this request, and returns how many were removed. The routes calling it are
// session-only, so a missing session means it vanished mid-request; then
// there is nothing to keep and every session is revoked. Any other lookup
// error revokes nothing: falling back to "revoke all" on a database hiccup
// would sign the caller out too.
func (s *Server) revokeOtherSessions(r *http.Request, userID uuid.UUID) (int64, error) {
	cur, err := s.sessions.Resolve(r.Context(), r)
	if err == nil {
		return s.store.DeleteUserSessionsExcept(r.Context(), userID, cur.ID)
	}
	if !errors.Is(err, auth.ErrNoSession) {
		return 0, err
	}
	n, err := s.store.DeleteUserSessions(r.Context(), userID)
	if err == nil {
		log.Printf("account: request without a current session; revoked all %d session(s) of user %s", n, userID)
	}
	return n, err
}

// sessionLabel shortens a session id for the audit log (see handleRevokeSession).
func sessionLabel(id []byte) string {
	h := hex.EncodeToString(id)
	if len(h) > 8 {
		h = h[:8]
	}
	return h
}

type sessionView struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	sessions, err := s.store.ListSessions(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	var currentID []byte
	if cur, err := s.sessions.Resolve(r.Context(), r); err == nil {
		currentID = cur.ID
	}
	views := make([]sessionView, 0, len(sessions))
	for _, sess := range sessions {
		views = append(views, sessionView{
			ID:        hex.EncodeToString(sess.ID),
			IP:        sess.IP,
			UserAgent: sess.UserAgent,
			Current:   string(sess.ID) == string(currentID),
			CreatedAt: sess.CreatedAt,
			ExpiresAt: sess.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := hex.DecodeString(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if err := s.store.DeleteUserSession(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	// The session id is a bearer secret (the cookie resolves to it), so only
	// a short prefix goes into the log, enough to match it with the list.
	s.audit(r, store.AuditSessionRevoke, u.Username, map[string]any{"session": sessionLabel(id)})
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	n, err := s.revokeOtherSessions(r, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}
	s.audit(r, store.AuditSessionRevoke, u.Username, map[string]any{"others": true, "count": n})
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

// handleDeleteAccount removes the account and all of its data (GDPR erasure).
// Requires the current password as confirmation.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !s.verifyAccountPassword(w, r, u, req.Password) {
		return
	}
	// Collect the blobs to purge before the rows cascade away with the user.
	files, err := s.store.ListAllFiles(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not enumerate files")
		return
	}
	// Refuse to remove the last administrator. Other accounts may still exist, so
	// the first-run wizard would stay closed (it keys off "no users at all") and
	// the instance would be left with nobody able to reach settings, invites or
	// the audit log. The admin count and the delete happen atomically in the
	// store, so two admins deleting themselves at once cannot both succeed.
	if err := s.store.DeleteUserUnlessLastAdmin(r.Context(), u.ID); err != nil {
		if errors.Is(err, store.ErrLastAdmin) {
			writeErr(w, http.StatusConflict,
				"this is the only admin account on this instance and cannot be deleted")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not delete account")
		return
	}
	// Blobs go only after the account is really gone: purging first, as before,
	// destroyed the files of an account whose deletion was then refused or
	// failed. Detached from the request so a client disconnect cannot leave the
	// purge half-done; anything missed is an orphan the janitor's scan removes.
	purgeCtx := context.WithoutCancel(r.Context())
	for _, f := range files {
		s.purgeFileBlobs(purgeCtx, f)
	}
	// Recorded without a user id: audit_log.user_id references users, and the
	// row is gone. The username and former id stay in the label and metadata.
	s.auditAnon(r, store.AuditAccountDelete, u.Username, u.Username,
		map[string]any{"user_id": u.ID.String(), "files": len(files)})
	s.sessions.Destroy(r.Context(), w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// exportPageSize is how many file rows the data export reads at a time.
const exportPageSize = 500

// maxConcurrentExports caps the data exports one instance streams at once.
// Each one reads (and, for encrypted blobs, decrypts) every file of an
// account, so a handful running in parallel is enough to saturate the disk.
const maxConcurrentExports = 2

// exportSlots is the semaphore behind maxConcurrentExports.
var exportSlots = make(chan struct{}, maxConcurrentExports)

// exportRetryAfter is what a client turned away by the export cap is told to
// wait before trying again.
const exportRetryAfter = 30 * time.Second

// handleExport streams a ZIP with the user's files plus a JSON manifest (GDPR
// data portability). Rows are read a page at a time, once for the manifest and
// once for the files, so the export of a large account never holds all of its
// rows in memory.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	select {
	case exportSlots <- struct{}{}:
		defer func() { <-exportSlots }()
	default:
		writeRateLimited(w, exportRetryAfter, "too many data exports are running, try again shortly")
		return
	}
	// The first page is read before anything is written, so a database error
	// can still become a proper HTTP error.
	first, err := s.store.ListAllFilesAfter(r.Context(), u.ID, nil, exportPageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not enumerate files")
		return
	}
	s.audit(r, store.AuditAccountExport, u.Username, nil)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="uploader-export.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()

	base := s.publicBaseURL(r)
	if iw, err := zw.Create("DSGVO-Auskunft.txt"); err == nil {
		_, _ = iw.Write([]byte(dsgvoNotice(u.Username, u.Email)))
	}
	// The response is already streaming, so from here on a failure cannot
	// become an HTTP error any more. Log it and list it in the archive instead
	// of silently leaving data out.
	var failed []string
	if mw, err := zw.Create("manifest.json"); err == nil {
		if err := s.writeExportManifest(r.Context(), mw, u.ID, first, base); err != nil {
			log.Printf("export: user %s manifest: %v", u.ID, err)
			failed = append(failed, "manifest.json")
		}
	}

	err = s.eachExportFile(r.Context(), u.ID, first, func(f store.File) {
		if err := s.exportFile(r.Context(), zw, f); err != nil {
			log.Printf("export: user %s file %s: %v", u.ID, f.Slug, err)
			failed = append(failed, f.Slug+" ("+f.OriginalName+")")
		}
	})
	if err != nil {
		log.Printf("export: user %s file list: %v", u.ID, err)
		failed = append(failed, "(weitere Dateien: Liste konnte nicht vollständig gelesen werden)")
	}
	if len(failed) > 0 {
		if ew, err := zw.Create("EXPORT-FEHLER.txt"); err == nil {
			_, _ = ew.Write([]byte("Diese Dateien konnten nicht (vollständig) exportiert werden:\n\n" +
				strings.Join(failed, "\n") + "\n"))
		}
	}
}

// eachExportFile calls fn for every file of the user, trashed ones included,
// newest first, starting with the already loaded first page. It stops early
// once ctx is cancelled (the client went away).
func (s *Server) eachExportFile(ctx context.Context, userID uuid.UUID, first []store.File, fn func(store.File)) error {
	page := first
	for {
		for _, f := range page {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fn(f)
		}
		if len(page) < exportPageSize {
			return nil
		}
		var err error
		page, err = s.store.ListAllFilesAfter(ctx, userID, &page[len(page)-1], exportPageSize)
		if err != nil {
			return err
		}
	}
}

// writeExportManifest writes the JSON array of every exported file's metadata,
// encoding it element by element as the pages come in.
func (s *Server) writeExportManifest(ctx context.Context, w io.Writer, userID uuid.UUID, first []store.File, base string) error {
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return err
	}
	n := 0
	var werr error
	err := s.eachExportFile(ctx, userID, first, func(f store.File) {
		if werr != nil {
			return
		}
		b, err := json.MarshalIndent(toFileView(f, base), "  ", "  ")
		if err != nil {
			werr = err
			return
		}
		sep := "  "
		if n > 0 {
			sep = ",\n  "
		}
		n++
		if _, err := io.WriteString(w, sep); err != nil {
			werr = err
			return
		}
		_, werr = w.Write(b)
	})
	if err == nil {
		err = werr
	}
	if _, werr := io.WriteString(w, "\n]\n"); err == nil {
		err = werr
	}
	return err
}

// exportFile writes one file's content into the export archive. Encrypted blobs
// are decrypted exactly as handleRaw does: the export must hand the user their
// file, not the at-rest ciphertext. The entry is named like the raw URL (slug
// plus upload.PreferredExt), and formats that are compressed already are
// stored rather than deflated a second time for nothing.
func (s *Server) exportFile(ctx context.Context, zw *zip.Writer, f store.File) error {
	rc, err := s.storage.Open(ctx, f.StorageKey)
	if err != nil {
		return fmt.Errorf("open blob: %w", err)
	}
	defer rc.Close()
	var src io.Reader = rc
	if f.Encrypted {
		dr, err := blobcrypt.NewReader(rc, s.encKey(), f.SizeBytes)
		if err != nil {
			return fmt.Errorf("decrypt blob: %w", err)
		}
		src = dr
	}
	hdr := &zip.FileHeader{
		Name:     "files/" + f.Slug + upload.PreferredExt(f.OriginalName, f.MimeType),
		Method:   zip.Deflate,
		Modified: f.CreatedAt,
	}
	if alreadyCompressed(f.MimeType) {
		hdr.Method = zip.Store
	}
	fw, err := zw.CreateHeader(hdr)
	if err != nil {
		return fmt.Errorf("create zip entry: %w", err)
	}
	if _, err := io.Copy(fw, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

// alreadyCompressed reports whether a sniffed MIME type is a compressed format
// that Deflate would only spend CPU on: images other than the uncompressed
// ones and SVG (text), audio, video and archives.
func alreadyCompressed(mime string) bool {
	m := strings.ToLower(mime)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	switch m {
	case "image/svg+xml", "image/bmp", "image/x-icon", "image/vnd.microsoft.icon", "image/tiff",
		"audio/wave", "audio/wav", "audio/x-wav", "audio/aiff", "audio/x-aiff":
		return false
	case "application/zip", "application/x-gzip", "application/gzip", "application/x-rar-compressed",
		"application/x-7z-compressed", "application/zstd", "application/x-bzip2", "application/x-xz",
		"application/pdf":
		return true
	}
	return strings.HasPrefix(m, "image/") || strings.HasPrefix(m, "audio/") || strings.HasPrefix(m, "video/")
}

// dsgvoNotice returns the German GDPR (DSGVO) Art. 15 data-subject information
// that accompanies a data export.
func dsgvoNotice(username, email string) string {
	return strings.Join([]string{
		"Datenauskunft gemäß Art. 15 DSGVO (Recht auf Auskunft)",
		"========================================================",
		"",
		"Dieses Archiv enthält die zu deinem Konto gespeicherten personenbezogenen",
		"Daten sowie alle von dir hochgeladenen Dateien.",
		"",
		"Konto:",
		"  Benutzername: " + username,
		"  E-Mail:       " + email,
		"",
		"Inhalt dieses Exports:",
		"  - manifest.json : Metadaten aller Dateien (Name, Größe, Typ, Datum, Aufrufe, Links)",
		"  - files/        : die Originaldateien",
		"",
		"Verarbeitete Datenkategorien:",
		"  - Kontodaten (Benutzername, E-Mail, Erstellungsdatum)",
		"  - Hochgeladene Dateien und deren Metadaten",
		"  - Technische Sitzungs-/Login-Daten (IP, User-Agent) zur Absicherung",
		"",
		"Deine weiteren Rechte nach DSGVO:",
		"  - Art. 16  Berichtigung",
		"  - Art. 17  Löschung („Recht auf Vergessenwerden\") — im Konto unter „Account",
		"             löschen\" jederzeit selbst durchführbar",
		"  - Art. 18  Einschränkung der Verarbeitung",
		"  - Art. 20  Datenübertragbarkeit (dieser Export)",
		"  - Art. 21  Widerspruch",
		"  - Art. 77  Beschwerderecht bei einer Aufsichtsbehörde",
		"",
		"Erstellt automatisch beim Datenexport.",
		"",
	}, "\n")
}
