package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
	"uploader/internal/upload"
)

func TestFileLimitReader(t *testing.T) {
	for _, tc := range []struct {
		size    int
		wantErr bool
	}{{0, false}, {999, false}, {1000, false}, {1001, true}, {5000, true}} {
		lr := &fileLimitReader{r: bytes.NewReader(make([]byte, tc.size)), left: 1000, limit: 1000}
		n, err := io.Copy(io.Discard, lr)
		var maxErr *http.MaxBytesError
		if tc.wantErr && !errors.As(err, &maxErr) || !tc.wantErr && err != nil {
			t.Errorf("size %d: err = %v, want MaxBytesError: %v", tc.size, err, tc.wantErr)
		}
		if n > 1000 {
			t.Errorf("size %d: passed %d bytes through a 1000-byte limit", tc.size, n)
		}
	}
}

// The request body may exceed the limit by the multipart slack, the file part
// may not: an oversized part is refused with 413 before anything is stored.
func TestUploadRejectsFilePartOverLimit(t *testing.T) {
	s := &Server{
		cfg:      &config.Config{DirectUploadLimit: 1000},
		settings: settings.NewStatic(settings.Snapshot{}),
		// No store: the part must be refused while it is spooled, before any
		// database access.
		uploads: upload.NewService(nil, nil, 6),
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(bytes.Repeat([]byte("x"), 2000))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{ID: uuid.New()}))
	rec := httptest.NewRecorder()
	s.handleUpload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("file part over the limit: status %d (%s), want 413", rec.Code, rec.Body)
	}
}

func TestUploadDefaults(t *testing.T) {
	for _, tc := range []struct {
		encryptDefault string
		key            []byte
		wantDefault    bool
		wantAvailable  bool
	}{
		{"true", nil, true, false},
		{"false", make([]byte, 32), false, true},
		{"", make([]byte, 32), false, true},
	} {
		s := &Server{
			cfg:      &config.Config{DirectUploadLimit: 1 << 20},
			settings: settings.NewStatic(settings.Snapshot{EncryptDefault: tc.encryptDefault}),
		}
		s.SetEncryptionKey(tc.key)
		rec := httptest.NewRecorder()
		s.handleUploadDefaults(rec, httptest.NewRequest(http.MethodGet, "/api/v1/upload/defaults", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var got struct {
			EncryptDefault      bool  `json:"encrypt_default"`
			EncryptionAvailable bool  `json:"encryption_available"`
			MaxUploadBytes      int64 `json:"max_upload_bytes"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.EncryptDefault != tc.wantDefault || got.EncryptionAvailable != tc.wantAvailable || got.MaxUploadBytes != 1<<20 {
			t.Errorf("encrypt_default=%q key=%d: got %+v", tc.encryptDefault, len(tc.key), got)
		}
	}
}
