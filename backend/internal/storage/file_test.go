package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStorageRoundTrip(t *testing.T) {
	st, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	payload := []byte("hello world")

	n, err := st.Put(ctx, "ab/cd/abcd.txt", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("Put returned %d, want %d", n, len(payload))
	}

	rc, err := st.Open(ctx, "ab/cd/abcd.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close() // close before Delete (Windows can't remove open files)
	if !bytes.Equal(got, payload) {
		t.Errorf("Open returned %q, want %q", got, payload)
	}

	if err := st.Delete(ctx, "ab/cd/abcd.txt"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	// Deleting a missing key is not an error.
	if err := st.Delete(ctx, "ab/cd/abcd.txt"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("client went away") }

// A failed write leaves neither the key nor a staging file behind; a good one
// leaves exactly the blob.
func TestFileStoragePutCleansUpStaging(t *testing.T) {
	root := t.TempDir()
	st, err := NewFileStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.Put(ctx, "ab/cd/bad.txt", io.MultiReader(strings.NewReader("partial"), failingReader{})); err == nil {
		t.Fatal("Put with a failing reader: want error")
	}
	if _, err := st.Put(ctx, "ab/cd/good.txt", strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "ab", "cd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "good.txt" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("shard dir holds %v, want only good.txt", names)
	}
}

func TestSweepStagingRemovesOnlyStaleStagingFiles(t *testing.T) {
	root := t.TempDir()
	st, err := NewFileStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	write := func(rel string, mtime time.Time) string {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	crashed := write("ab/cd/.upload-123", old)    // abandoned by a killed Put
	inRoot := write(".upload-456", old)           // the /ready probe's staging file
	writing := write("ab/cd/.upload-789", now)    // a Put still in progress
	blob := write("ab/cd/abcd.png", old)          // a real blob
	tus := write(".tus/.upload-ignored", old)     // not ours to touch
	probe := write(".healthcheck", old)           // other dotfiles stay
	thumb := write("thumbs/ab/cd/.upload-1", old) // nested shard

	if n := st.SweepStaging(now.Add(-time.Hour)); n != 3 {
		t.Errorf("SweepStaging removed %d files, want 3", n)
	}
	for _, gone := range []string{crashed, inRoot, thumb} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s was not swept", gone)
		}
	}
	for _, kept := range []string{writing, blob, tus, probe} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was swept", kept)
		}
	}
}

func TestFileStorageRejectsTraversal(t *testing.T) {
	st, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	badKeys := []string{
		"../escape.txt",
		"../../etc/passwd",
		"a/../../b.txt",
		"..\\windows\\system32",
	}
	for _, k := range badKeys {
		if _, err := st.Put(ctx, k, bytes.NewReader([]byte("x"))); err == nil {
			t.Errorf("Put(%q) should have been rejected", k)
		}
	}
}
