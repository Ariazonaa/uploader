//go:build integration

package store_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// Keyset paging visits each of the user's live files once, newest first,
// also across rows that share a created_at.
func TestListFilesAfter(t *testing.T) {
	e := newEnv(t)
	u := e.user("exporter", false)
	other := e.user("other", false)
	const n = 8
	for i := 0; i < n; i++ {
		e.file(u.ID, fmt.Sprintf("f%d", i))
	}
	e.file(other.ID, "foreign")
	trashed := e.file(u.ID, "trashed")
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	newest := e.file(u.ID, "newest")
	e.exec(`UPDATE files SET created_at = '2026-01-01T12:00:00Z' WHERE user_id = $1 AND slug <> 'newest'`, u.ID)

	for _, pageSize := range []int{1, 3, n + 1, 50} {
		var all []store.File
		var after *store.File
		for pages := 0; ; pages++ {
			page, err := e.st.ListFilesAfter(e.ctx, u.ID, after, pageSize)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
			if len(page) < pageSize {
				break
			}
			after = &page[len(page)-1]
			if pages > 2*n {
				t.Fatalf("page size %d: paging does not end", pageSize)
			}
		}
		seen := map[uuid.UUID]bool{}
		for _, f := range all {
			if seen[f.ID] || f.UserID != u.ID || f.DeletedAt != nil {
				t.Fatalf("page size %d: %s listed twice, foreign or trashed", pageSize, f.Slug)
			}
			seen[f.ID] = true
		}
		if len(all) != n+1 || all[0].ID != newest.ID {
			t.Fatalf("page size %d: %d files, first %s; want %d starting with the newest", pageSize, len(all), all[0].Slug, n+1)
		}
	}
}

// The export variant pages through the trash too, and nothing foreign.
func TestListAllFilesAfterIncludesTrash(t *testing.T) {
	e := newEnv(t)
	u := e.user("exporter", false)
	other := e.user("other", false)
	e.file(u.ID, "live1")
	e.file(u.ID, "live2")
	trashed := e.file(u.ID, "trashed")
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	e.file(other.ID, "foreign")

	var slugs []string
	var after *store.File
	for {
		page, err := e.st.ListAllFilesAfter(e.ctx, u.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range page {
			slugs = append(slugs, f.Slug)
		}
		if len(page) < 2 {
			break
		}
		after = &page[len(page)-1]
	}
	if len(slugs) != 3 {
		t.Fatalf("export listed %v, want live1, live2 and trashed", slugs)
	}
	for _, s := range slugs {
		if s == "foreign" {
			t.Fatal("export listed another user's file")
		}
	}
}
