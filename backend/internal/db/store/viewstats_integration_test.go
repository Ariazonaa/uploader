//go:build integration

package store_test

import (
	"fmt"
	"testing"
	"time"

	"uploader/internal/db/store"
)

func (e *env) stats(slug string) map[string]int64 {
	e.t.Helper()
	rows, err := e.pool.Query(e.ctx, `SELECT referrer_host, sum(count) FROM file_view_stats WHERE slug = $1 GROUP BY 1`, slug)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var h string
		var n int64
		if err := rows.Scan(&h, &n); err != nil {
			e.t.Fatal(err)
		}
		out[h] = n
	}
	return out
}

func TestRecordViewReferrerCap(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	f := e.file(u.ID, "capped")
	other := e.file(u.ID, "uncapped")
	view := func(slug, host, ua string) {
		t.Helper()
		if err := e.st.RecordView(e.ctx, slug, host, ua); err != nil {
			t.Fatalf("RecordView(%s, %s): %v", slug, host, err)
		}
	}

	for i := 0; i < store.MaxReferrersPerSlug; i++ {
		view(f.Slug, fmt.Sprintf("host%d.example", i), "desktop")
	}
	// Over the cap: new hosts fold into "(other)" ...
	for i := 0; i < 20; i++ {
		view(f.Slug, fmt.Sprintf("late%d.example", i), "desktop")
	}
	// ... while already tracked hosts and the reserved buckets keep counting.
	view(f.Slug, "host0.example", "mobile")
	view(f.Slug, "host0.example", "desktop")
	view(f.Slug, "", "")
	view(f.Slug, store.ReferrerOther, "bot")

	got := e.stats(f.Slug)
	tracked := 0
	for h := range got {
		if h != store.ReferrerDirect && h != store.ReferrerOther {
			tracked++
		}
	}
	if tracked != store.MaxReferrersPerSlug {
		t.Fatalf("%d distinct referrer hosts tracked, want %d", tracked, store.MaxReferrersPerSlug)
	}
	if got[store.ReferrerOther] != 21 || got[store.ReferrerDirect] != 1 || got["host0.example"] != 3 {
		t.Fatalf("(other)=%d (direct)=%d host0=%d, want 21/1/3", got[store.ReferrerOther], got[store.ReferrerDirect], got["host0.example"])
	}
	for h := range got {
		if len(h) > 4 && h[:4] == "late" {
			t.Fatalf("host %q tracked past the cap", h)
		}
	}
	// The empty UA category is stored as "other".
	if n := e.count(`SELECT count(*) FROM file_view_stats WHERE slug = $1 AND referrer_host = '(direct)' AND ua_category = 'other'`, f.Slug); n != 1 {
		t.Fatal("empty UA category not normalised to 'other'")
	}
	// The reserved buckets are not hosts and do not use up the cap, even when
	// they exist before any real referrer.
	first := e.file(u.ID, "reserved-first")
	view(first.Slug, "", "desktop")
	view(first.Slug, store.ReferrerOther, "desktop")
	for i := 0; i < store.MaxReferrersPerSlug+5; i++ {
		view(first.Slug, fmt.Sprintf("host%d.example", i), "desktop")
	}
	tracked = 0
	for h := range e.stats(first.Slug) {
		if h != store.ReferrerDirect && h != store.ReferrerOther {
			tracked++
		}
	}
	if tracked != store.MaxReferrersPerSlug {
		t.Fatalf("with reserved buckets recorded first: %d hosts tracked, want %d", tracked, store.MaxReferrersPerSlug)
	}
	// The cap is per slug: another file still tracks new hosts.
	view(other.Slug, "late0.example", "desktop")
	if e.stats(other.Slug)["late0.example"] != 1 {
		t.Fatal("cap leaked across slugs")
	}

	// A view for a slug with no file row is dropped, not stored as an orphan.
	view("no-such-slug", "example.com", "desktop")
	if n := e.count(`SELECT count(*) FROM file_view_stats WHERE slug = 'no-such-slug'`); n != 0 {
		t.Fatal("view for a missing file created a stats row")
	}

	top, err := e.st.TopReferrers(e.ctx, 1)
	if err != nil || len(top) != 1 || top[0].Name != store.ReferrerOther || top[0].Count != 21+1+5 {
		t.Fatalf("TopReferrers = %v, %v", top, err)
	}
	cats, err := e.st.TopUACategories(e.ctx, 10)
	if err != nil || len(cats) == 0 || cats[0].Name != "desktop" {
		t.Fatalf("TopUACategories = %v, %v", cats, err)
	}
}

// Concurrent first views of the same new host must not fail on the upsert.
func TestRecordViewConcurrent(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	f := e.file(u.ID, "busy")
	const n = 16
	errs := parallel(n, func(int) error { return e.st.RecordView(e.ctx, f.Slug, "example.com", "desktop") })
	for i, err := range errs {
		if err != nil {
			t.Fatalf("view %d: %v", i, err)
		}
	}
	if got := e.stats(f.Slug)["example.com"]; got != n {
		t.Fatalf("count = %d, want %d", got, n)
	}
}

// The AFTER DELETE trigger from 0013 removes a slug's stats on every
// hard-delete path, and only that slug's.
func TestViewStatsPurgedWithFile(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	keep := e.file(u.ID, "keep")
	byID := e.file(u.ID, "byid")
	old := e.file(u.ID, "oldtrash")
	recent := e.file(u.ID, "newtrash")
	hard := e.file(u.ID, "hard")
	for _, f := range []store.File{keep, byID, old, recent, hard} {
		if err := e.st.RecordView(e.ctx, f.Slug, "example.com", "desktop"); err != nil {
			t.Fatal(err)
		}
		if err := e.st.RecordView(e.ctx, f.Slug, "", "mobile"); err != nil {
			t.Fatal(err)
		}
	}
	has := func(slug string) bool {
		return e.count(`SELECT count(*) FROM file_view_stats WHERE slug = $1`, slug) > 0
	}

	if err := e.st.PurgeFileByID(e.ctx, byID.ID); err != nil {
		t.Fatal(err)
	}
	if has(byID.Slug) {
		t.Error("PurgeFileByID left view stats behind")
	}

	for _, f := range []store.File{old, recent, hard} {
		if _, err := e.st.DeleteFile(e.ctx, f.ID, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Soft delete keeps the stats (the file is restorable).
	if !has(old.Slug) {
		t.Fatal("soft delete removed view stats")
	}
	e.exec(`UPDATE files SET deleted_at = now() - interval '40 days' WHERE id = $1`, old.ID)
	purged, err := e.st.PurgeTrashedOlderThan(e.ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0].ID != old.ID {
		t.Fatalf("PurgeTrashedOlderThan purged %v, want only %s", purged, old.Slug)
	}
	if has(old.Slug) {
		t.Error("PurgeTrashedOlderThan left view stats behind")
	}
	if !has(recent.Slug) {
		t.Error("stats of a recently trashed file were removed")
	}

	if _, err := e.st.HardDeleteFile(e.ctx, hard.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if has(hard.Slug) {
		t.Error("HardDeleteFile left view stats behind")
	}
	if !has(keep.Slug) {
		t.Error("stats of an untouched file were removed")
	}
}
