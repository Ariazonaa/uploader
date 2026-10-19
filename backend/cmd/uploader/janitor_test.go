package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepStaleTusJudgesUploadByLastActivity(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)

	// Created two days ago, but its data file received a chunk a minute ago:
	// still active, both files must survive.
	touch(t, filepath.Join(dir, "active.json"), old)
	touch(t, filepath.Join(dir, "active.bin"), now.Add(-time.Minute))
	// Nothing for two days: abandoned, both files go.
	touch(t, filepath.Join(dir, "idle.json"), old)
	touch(t, filepath.Join(dir, "idle.bin"), old)
	// Fresh upload.
	touch(t, filepath.Join(dir, "new.json"), now)

	sweepStaleTus(dir, now)

	for _, keep := range []string{"active.json", "active.bin", "new.json"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("%s was swept", keep)
		}
	}
	for _, gone := range []string{"idle.json", "idle.bin"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s was not swept", gone)
		}
	}
}

func TestSweepStaleTempOnlyTouchesOwnOldFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-staleTempAfter - time.Hour)

	touch(t, filepath.Join(dir, "uploader-123"), old)       // crashed spool: gone
	touch(t, filepath.Join(dir, "uploader-media-456"), old) // crashed ffmpeg input: gone
	touch(t, filepath.Join(dir, "uploader-789"), now)       // in use: kept
	touch(t, filepath.Join(dir, "someone-else"), old)       // not ours: kept

	hls := filepath.Join(dir, "uploader-hls-abc")
	if err := os.Mkdir(hls, 0o750); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(hls, "seg000.ts"), old)
	if err := os.Chtimes(hls, old, old); err != nil {
		t.Fatal(err)
	}
	// A directory that merely shares the generic prefix is not an HLS scratch
	// directory; it is left alone.
	other := filepath.Join(dir, "uploader-other")
	if err := os.Mkdir(other, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}

	sweepStaleTemp(dir, now)

	for _, gone := range []string{"uploader-123", "uploader-media-456", "uploader-hls-abc"} {
		if exists(filepath.Join(dir, gone)) {
			t.Errorf("%s was not swept", gone)
		}
	}
	for _, keep := range []string{"uploader-789", "someone-else", "uploader-other"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("%s was swept", keep)
		}
	}
}
