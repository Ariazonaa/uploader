package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// Encrypting a data file chunk by chunk, at arbitrary offsets, yields the same
// bytes as encrypting it in one go, and decrypts back.
func TestTusStreamAtIsPositional(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	plain := make([]byte, 1000)
	_, _ = rand.Read(plain)

	whole := make([]byte, len(plain))
	s, err := tusStreamAt(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.XORKeyStream(whole, plain)

	pieces := make([]byte, 0, len(plain))
	for off := 0; off < len(plain); {
		n := min(1+off%37, len(plain)-off) // odd sizes, never block-aligned for long
		s, err := tusStreamAt(key, int64(off))
		if err != nil {
			t.Fatal(err)
		}
		out := make([]byte, n)
		s.XORKeyStream(out, plain[off:off+n])
		pieces = append(pieces, out...)
		off += n
	}
	if !bytes.Equal(whole, pieces) {
		t.Fatal("chunked encryption differs from encrypting in one go")
	}
	back := make([]byte, len(whole))
	s, _ = tusStreamAt(key, 0)
	s.XORKeyStream(back, whole)
	if !bytes.Equal(back, plain) {
		t.Fatal("decryption does not restore the plaintext")
	}
}

func TestTusScratchKeyWrap(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 32)
	key, wrapped, err := newTusScratchKey(master, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := unwrapTusScratchKey(master, "id-1", wrapped)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("unwrap = %x, %v", got, err)
	}
	if _, err := unwrapTusScratchKey(bytes.Repeat([]byte{8}, 32), "id-1", wrapped); err == nil {
		t.Fatal("unwrapped with another master key")
	}
	if _, err := unwrapTusScratchKey(master, "id-2", wrapped); err == nil {
		t.Fatal("unwrapped for another upload")
	}
}

// An upload that will be stored encrypted never sits in the scratch dir in
// plaintext, survives a restart (the key is in its record) and reaches the
// pipeline as plaintext.
func TestTusEncryptedUploadScratchIsCiphertext(t *testing.T) {
	th := newTusHarness(t)
	th.s.SetEncryptionKey(bytes.Repeat([]byte{1}, 32))
	user := uuid.New()
	plain := []byte(strings.Repeat("secret plaintext ", 20)) // 340 bytes
	path := th.createQ(user, len(plain), "?encrypt=true")
	id := strings.TrimPrefix(path, "/api/v1/tus/")

	off := 0
	for _, n := range []int{5, 17, 100} {
		if rec := th.patch(context.Background(), path, user, off, string(plain[off:off+n])); rec.Code != http.StatusNoContent {
			t.Fatalf("PATCH at %d: status %d", off, rec.Code)
		}
		off += n
	}
	onDisk, err := os.ReadFile(filepath.Join(th.dir, ".tus", id+".bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != off || bytes.Contains(onDisk, []byte("secret")) {
		t.Fatalf("scratch holds %d bytes (want %d), plaintext visible: %v", len(onDisk), off, bytes.Contains(onDisk, []byte("secret")))
	}
	if meta, _ := os.ReadFile(filepath.Join(th.dir, ".tus", id+".json")); !bytes.Contains(meta, []byte(`"scratch_key"`)) {
		t.Fatalf("upload record carries no wrapped key: %s", meta)
	}

	// "Restart": a fresh server with the same key and storage root resumes.
	th.s = &Server{cfg: th.s.cfg, settings: th.s.settings}
	th.s.SetEncryptionKey(bytes.Repeat([]byte{1}, 32))
	th.h = newTusHarnessRouter(th.s)

	var got []byte
	var gotOpts upload.SaveOptions
	tusSaveHook = func(_ context.Context, _ uuid.UUID, r io.Reader, _ string, opts upload.SaveOptions) (store.File, error) {
		got, _ = io.ReadAll(r)
		gotOpts = opts
		return store.File{Slug: "enc"}, nil
	}
	if rec := th.patch(context.Background(), path, user, off, string(plain[off:])); rec.Code != http.StatusNoContent ||
		rec.Header().Get("X-Uploader-Slug") != "enc" {
		t.Fatalf("final PATCH after restart: status %d", rec.Code)
	}
	if !bytes.Equal(got, plain) || !gotOpts.Encrypt {
		t.Fatalf("pipeline got %q (encrypt %v), want the plaintext, encrypted", got, gotOpts.Encrypt)
	}
}

// A plaintext upload's scratch stays as it was.
func TestTusPlainUploadScratchIsPlain(t *testing.T) {
	th := newTusHarness(t)
	th.s.SetEncryptionKey(bytes.Repeat([]byte{1}, 32))
	user := uuid.New()
	path := th.createQ(user, 10, "?encrypt=false")
	id := strings.TrimPrefix(path, "/api/v1/tus/")
	th.patch(context.Background(), path, user, 0, "plain")
	if onDisk, _ := os.ReadFile(filepath.Join(th.dir, ".tus", id+".bin")); string(onDisk) != "plain" {
		t.Fatalf("plaintext upload scratch = %q", onDisk)
	}
}

// When the key changed since the upload was created, its scratch cannot be
// read: the request fails, but the data is kept in case the key comes back.
func TestTusEncryptedUploadWithChangedKey(t *testing.T) {
	th := newTusHarness(t)
	th.s.SetEncryptionKey(bytes.Repeat([]byte{1}, 32))
	user := uuid.New()
	path := th.createQ(user, 10, "?encrypt=true")
	th.patch(context.Background(), path, user, 0, "abc")

	th.s.SetEncryptionKey(bytes.Repeat([]byte{2}, 32))
	if rec := th.patch(context.Background(), path, user, 3, "def"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH under another key: status %d, want 500", rec.Code)
	}
	if names := th.scratch(); len(names) != 2 {
		t.Fatalf("scratch after a key mismatch = %v, want the upload kept", names)
	}
	// Without any key the upload can never be stored: it is discarded.
	th.s.SetEncryptionKey(nil)
	if rec := th.patch(context.Background(), path, user, 3, "def"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH without a key: status %d, want 503", rec.Code)
	}
	if names := th.scratch(); len(names) != 0 {
		t.Fatalf("scratch after the key went away = %v", names)
	}
}
