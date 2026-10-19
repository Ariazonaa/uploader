//go:build integration

package store_test

import (
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// Slugs are unique regardless of case (storage keys are built from them, and
// the filesystem may not tell "ABCD" from "abcd"), trashed files included.
func TestSlugsAreUniqueIgnoringCase(t *testing.T) {
	e := newEnv(t)
	u := e.user("slugs", false)
	live := e.file(u.ID, "MixedCase")
	trashed := e.file(u.ID, "Trashed")
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"mixedcase", "MIXEDCASE", "trashed"} {
		f := store.File{ID: uuid.New(), UserID: u.ID, Slug: slug, OriginalName: "x", MimeType: "text/plain", StorageKey: "k/" + slug}
		if err := e.st.CreateFile(e.ctx, f); sqlState(err) != "23505" {
			t.Errorf("slug %q next to %q/%q: err %v, want a unique violation", slug, live.Slug, trashed.Slug, err)
		}
	}
	// Lookups stay exact.
	if _, err := e.st.GetFileBySlug(e.ctx, "mixedcase"); err == nil {
		t.Error("GetFileBySlug matched a slug of different case")
	}
}

// GetFileByHash (upload dedup) returns the newest live copy, never one that is
// trashed or carries its own expiry or view limit.
func TestGetFileByHashPicksNewestLiveCopy(t *testing.T) {
	e := newEnv(t)
	u := e.user("dedup", false)
	old := e.file(u.ID, "old")
	newer := e.file(u.ID, "newer")
	trashed := e.file(u.ID, "trashed")
	expiring := e.file(u.ID, "expiring")
	limited := e.file(u.ID, "limited")
	other := e.user("other", false)
	e.file(other.ID, "foreign")
	e.exec(`UPDATE files SET content_hash = 'h'`)
	e.exec(`UPDATE files SET created_at = now() - interval '2 days' WHERE id = $1`, old.ID)
	e.exec(`UPDATE files SET created_at = now() - interval '1 day' WHERE id = $1`, newer.ID)
	e.exec(`UPDATE files SET deleted_at = now() WHERE id = $1`, trashed.ID)
	e.exec(`UPDATE files SET expires_at = now() + interval '1 hour' WHERE id = $1`, expiring.ID)
	e.exec(`UPDATE files SET max_views = 5 WHERE id = $1`, limited.ID)

	got, err := e.st.GetFileByHash(e.ctx, u.ID, "h")
	if err != nil || got.ID != newer.ID {
		t.Fatalf("GetFileByHash = %q, %v; want the newest live copy %q", got.Slug, err, newer.Slug)
	}
}
