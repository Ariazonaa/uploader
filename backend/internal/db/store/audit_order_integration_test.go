//go:build integration

package store_test

import (
	"bytes"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// Events recorded in the same instant must page deterministically: ties are
// broken by id, and every event shows up exactly once across the pages.
func TestListAuditEventsPagesEventsWithEqualTimestamps(t *testing.T) {
	e := newEnv(t)
	const n, pageSize = 12, 3
	for range n {
		if err := e.st.InsertAuditEvent(e.ctx, store.AuditEvent{Event: store.AuditLoginFailure}); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`UPDATE audit_log SET created_at = '2026-01-01T00:00:00Z'`)

	var all []uuid.UUID
	seen := make(map[uuid.UUID]bool)
	for offset := 0; offset < n; offset += pageSize {
		page, total, err := e.st.ListAuditEvents(e.ctx, store.AuditFilter{Limit: pageSize, Offset: offset})
		if err != nil || total != n || len(page) != pageSize {
			t.Fatalf("page at %d: %d events, total %d, %v", offset, len(page), total, err)
		}
		for _, ev := range page {
			if seen[ev.ID] {
				t.Fatalf("event %s appears on more than one page", ev.ID)
			}
			seen[ev.ID] = true
			all = append(all, ev.ID)
		}
	}
	if len(seen) != n {
		t.Fatalf("pages covered %d of %d events", len(seen), n)
	}
	// Postgres orders uuid bytewise, like bytes.Compare.
	for i := 1; i < len(all); i++ {
		if bytes.Compare(all[i-1][:], all[i][:]) <= 0 {
			t.Fatalf("events with equal timestamps not in id DESC order at %d: %s then %s", i, all[i-1], all[i])
		}
	}
}
