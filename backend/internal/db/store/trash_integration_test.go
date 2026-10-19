//go:build integration

package store_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// The trash pages through every trashed file exactly once, newest-trashed
// first, even when files were trashed at the same instant (bulk delete).
func TestListTrashPagination(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	e.file(u.ID, "live")
	const n = 7
	for i := 0; i < n; i++ {
		f := e.file(u.ID, fmt.Sprintf("t%d", i))
		if _, err := e.st.DeleteFile(e.ctx, f.ID, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`UPDATE files SET deleted_at = '2026-01-01T12:00:00Z' WHERE user_id = $1 AND deleted_at IS NOT NULL AND slug <> 't6'`, u.ID)
	newest := e.count(`SELECT count(*) FROM files WHERE slug = 't6' AND deleted_at > '2026-01-01T12:00:00Z'`)
	if newest != 1 {
		t.Fatal("setup: t6 is not the newest trashed file")
	}

	seen := map[uuid.UUID]int{}
	var order []string
	for offset := 0; offset < n+3; offset += 3 {
		page, err := e.st.ListTrash(e.ctx, u.ID, 3, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range page {
			seen[f.ID]++
			order = append(order, f.Slug)
		}
	}
	if len(seen) != n || len(order) != n {
		t.Fatalf("paged through %d rows (%d distinct), want %d once each: %v", len(order), len(seen), n, order)
	}
	if order[0] != "t6" {
		t.Fatalf("first trashed file listed = %s, want the newest (t6)", order[0])
	}
}

// A trashed row still holds its slug (and its blobs); only a purge frees it.
func TestSlugStorageKey(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	f := e.file(u.ID, "held")
	check := func(wantKey string, wantTaken bool) {
		t.Helper()
		key, taken, err := e.st.SlugStorageKey(e.ctx, f.Slug)
		if err != nil || key != wantKey || taken != wantTaken {
			t.Fatalf("SlugStorageKey = %q, %v, %v; want %q, %v", key, taken, err, wantKey, wantTaken)
		}
	}
	check(f.StorageKey, true)
	if _, err := e.st.DeleteFile(e.ctx, f.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	check(f.StorageKey, true)
	if _, err := e.st.HardDeleteFile(e.ctx, f.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	check("", false)
}

// The traffic breakdown follows the soft-delete rule: a trashed file's views
// drop out, and come back when it is restored.
func TestTrafficStatsSkipTrashedFiles(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	live := e.file(u.ID, "live")
	trashed := e.file(u.ID, "trashed")
	for i := 0; i < 3; i++ {
		if err := e.st.RecordView(e.ctx, trashed.Slug, "trash.example", "bot"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.RecordView(e.ctx, live.Slug, "live.example", "desktop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}

	sum := func(ncs []store.NameCount, err error) map[string]int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, nc := range ncs {
			out[nc.Name] = nc.Count
		}
		return out
	}
	refs := sum(e.st.TopReferrers(e.ctx, 10))
	agents := sum(e.st.TopUACategories(e.ctx, 10))
	if len(refs) != 1 || refs["live.example"] != 1 || len(agents) != 1 || agents["desktop"] != 1 {
		t.Fatalf("with a trashed file: referrers %v, agents %v; want only the live file's view", refs, agents)
	}

	if _, err := e.st.RestoreFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if refs := sum(e.st.TopReferrers(e.ctx, 10)); refs["trash.example"] != 3 {
		t.Fatalf("after restore: referrers %v; want trash.example back with 3", refs)
	}
	if agents := sum(e.st.TopUACategories(e.ctx, 10)); agents["bot"] != 3 {
		t.Fatalf("after restore: agents %v; want bot back with 3", agents)
	}
}

// Bulk delete trashes the caller's live files among the ids in one statement
// and counts only those; foreign, missing and already trashed ids are skipped.
func TestDeleteFilesBulk(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	other := e.user("other", false)
	a, b := e.file(u.ID, "a"), e.file(u.ID, "b")
	keep := e.file(u.ID, "keep")
	foreign := e.file(other.ID, "foreign")
	gone := e.file(u.ID, "gone")
	if _, err := e.st.DeleteFile(e.ctx, gone.ID, u.ID); err != nil {
		t.Fatal(err)
	}

	n, err := e.st.DeleteFiles(e.ctx, []uuid.UUID{a.ID, b.ID, a.ID, foreign.ID, gone.ID, uuid.New()}, u.ID)
	if err != nil || n != 2 {
		t.Fatalf("DeleteFiles = %d, %v; want 2", n, err)
	}
	if got := e.count(`SELECT count(*) FROM files WHERE deleted_at IS NOT NULL AND user_id = $1`, u.ID); got != 3 {
		t.Fatalf("%d trashed files, want a, b and gone", got)
	}
	if got := e.count(`SELECT count(*) FROM files WHERE deleted_at IS NULL AND id = ANY($1)`, []uuid.UUID{keep.ID, foreign.ID}); got != 2 {
		t.Fatal("bulk delete touched a file it was not given or does not own")
	}
}
