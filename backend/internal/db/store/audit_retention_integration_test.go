//go:build integration

package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

func TestDeleteAuditEventsOlderThan(t *testing.T) {
	e := newEnv(t)
	old, recent := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{old, recent} {
		if err := e.st.InsertAuditEvent(e.ctx, store.AuditEvent{ID: id, Event: store.AuditLoginSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	e.exec(`UPDATE audit_log SET created_at = now() - interval '400 days' WHERE id = $1`, old)

	n, err := e.st.DeleteAuditEventsOlderThan(e.ctx, time.Now().AddDate(0, 0, -365))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d events, want 1", n)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE id = $1`, old) != 0 {
		t.Error("event past the window survived")
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE id = $1`, recent) != 1 {
		t.Error("recent event was deleted")
	}
}
