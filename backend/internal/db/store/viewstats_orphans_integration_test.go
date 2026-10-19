//go:build integration

package store_test

import (
	"testing"

	"uploader/internal/db/store"
)

// Rows a view wrote after its file's purge trigger ran are swept up later;
// stats of live and trashed files stay.
func TestDeleteOrphanViewStats(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	live := e.file(u.ID, "live")
	trashed := e.file(u.ID, "trashed")
	for _, f := range []store.File{live, trashed} {
		if err := e.st.RecordView(e.ctx, f.Slug, "example.com", "desktop"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	// What the race leaves behind: rows for a slug whose file is gone.
	e.exec(`INSERT INTO file_view_stats (slug, referrer_host, ua_category, count)
		VALUES ('gone', 'example.com', 'desktop', 3), ('gone', '(direct)', 'mobile', 1)`)

	n, err := e.st.DeleteOrphanViewStats(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d rows, want 2", n)
	}
	if got := e.count(`SELECT count(*) FROM file_view_stats WHERE slug IN ('live', 'trashed')`); got != 2 {
		t.Fatalf("%d stats rows left for live/trashed files, want 2", got)
	}
}
