package api

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"uploader/internal/auth"
	"uploader/internal/upload"
)

// errInvalidSlug rejects a malformed ?slug=.
var errInvalidSlug = errors.New("slug must be 3–64 chars of letters, digits, '-' or '_'")

// uploadOptions reads the per-upload query options that direct and tus uploads
// share, on top of the server defaults. The only error is errInvalidSlug.
func (s *Server) uploadOptions(r *http.Request) (upload.SaveOptions, error) {
	q := r.URL.Query()
	set := s.settings.Current()
	opts := upload.SaveOptions{
		StripEXIF:    set.StripEXIFBool(),
		Encrypt:      set.EncryptDefaultBool(),
		SlugStrategy: set.SlugStrategyValue(),
	}

	// Optional custom slug (?slug=my-link). Validated before reading the body so
	// a bad slug fails fast. An empty value keeps the random-slug behavior.
	opts.CustomSlug = strings.TrimSpace(q.Get("slug"))
	if opts.CustomSlug != "" && !upload.ValidCustomSlug(opts.CustomSlug) {
		return opts, errInvalidSlug
	}

	// EXIF stripping defaults to the server setting; a client may opt out per
	// upload via ?strip_exif=false. ShareX sends no such param, so its
	// uploads keep the default (automatic) behavior.
	if v := q.Get("strip_exif"); v == "false" || v == "0" {
		opts.StripEXIF = false
	}

	// At-rest encryption defaults to the server setting; a client may override
	// per upload via ?encrypt=true/false (so ShareX can request it).
	switch q.Get("encrypt") {
	case "true", "1":
		opts.Encrypt = true
	case "false", "0":
		opts.Encrypt = false
	}

	// Auto-slug naming strategy defaults to the server setting; a client may
	// override per upload via ?slug_strategy=random|gfycat|date. A custom
	// ?slug= still wins over any strategy.
	if v := strings.ToLower(strings.TrimSpace(q.Get("slug_strategy"))); v != "" && upload.ValidSlugStrategy(v) {
		opts.SlugStrategy = v
	}
	return opts, nil
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())

	opts, err := s.uploadOptions(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Hard cap the request body. A little slack above the configured limit
	// accounts for multipart framing overhead; the file part itself is held to
	// the exact limit below. MaxBytesReader bounds size, not time, so the read
	// deadline is extended (not removed) for the transfer.
	extendUploadDeadline(w)
	limit := s.maxUploadBytes()
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
				return
			}
			writeErr(w, http.StatusBadRequest, "malformed upload")
			return
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue // skip non-file form fields
		}

		name := upload.CleanFilename(part.FileName())
		body := &fileLimitReader{r: part, left: limit, limit: limit}
		f, err := s.uploads.SaveWith(r.Context(), u.ID, body, name, opts)
		_ = part.Close()
		if err != nil {
			var maxErr *http.MaxBytesError
			switch {
			case errors.As(err, &maxErr):
				writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
			case errors.Is(err, upload.ErrSlugTaken):
				writeErr(w, http.StatusConflict, "that slug is already taken")
			case errors.Is(err, upload.ErrBlockedType):
				writeErr(w, http.StatusUnsupportedMediaType, "this file type is not allowed")
			case errors.Is(err, upload.ErrEncryptionUnavailable):
				writeErr(w, http.StatusServiceUnavailable, "encryption is not configured")
			default:
				// Surface the underlying cause server-side; the client only ever
				// sees a generic 500, so without this an upload failure (e.g. an
				// unwritable storage directory) is impossible to diagnose.
				log.Printf("upload: save failed for user %s (%q): %v", u.ID, name, err)
				writeErr(w, http.StatusInternalServerError, "upload failed")
			}
			return
		}

		base := s.publicBaseURL(r)
		resp := map[string]any{
			"id":        f.ID,
			"slug":      f.Slug,
			"url":       base + "/s/" + f.Slug,
			"raw":       rawURLFor(base, f),
			"name":      f.OriginalName,
			"size":      f.SizeBytes,
			"encrypted": f.Encrypted,
		}
		if f.ThumbKey != nil {
			resp["thumb"] = base + "/thumb/" + f.Slug
		}
		if f.DeleteKey != nil {
			resp["delete"] = base + "/d/" + *f.DeleteKey
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	writeErr(w, http.StatusBadRequest, "no file part in upload")
}

// fileLimitReader fails a file part that grows past the per-file limit with
// the error http.MaxBytesReader uses, so it is answered 413 like an oversized
// body. The body cap alone leaves the multipart slack to the file as well.
type fileLimitReader struct {
	r     io.Reader
	left  int64 // bytes still allowed
	limit int64
}

func (l *fileLimitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, &http.MaxBytesError{Limit: l.limit}
	}
	// Read one byte past the limit so an exact-size file still ends in EOF.
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	if int64(n) > l.left {
		n = int(l.left)
		l.left = -1
		return n, &http.MaxBytesError{Limit: l.limit}
	}
	l.left -= int64(n)
	return n, err
}

// handleUploadDefaults tells an uploading client which per-upload defaults the
// server applies when it sends no override, so the SPA can pre-set its toggles
// to match. The full settings snapshot is admin-only; this exposes just what a
// regular uploader needs.
func (s *Server) handleUploadDefaults(w http.ResponseWriter, r *http.Request) {
	set := s.settings.Current()
	writeJSON(w, http.StatusOK, map[string]any{
		"encrypt_default":      set.EncryptDefaultBool(),
		"encryption_available": len(s.encKey()) > 0,
		"strip_exif_default":   set.StripEXIFBool(),
		"max_upload_bytes":     s.maxUploadBytes(),
	})
}
