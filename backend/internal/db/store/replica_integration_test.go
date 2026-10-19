//go:build integration

package store_test

import (
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/dbtest"
	"uploader/internal/db/store"
)

// A replica that lags behind the primary, modelled as a separate database:
// rows written through e.st exist only on the "primary".
func TestReadsWithALaggingReplica(t *testing.T) {
	e := newEnv(t)
	replicaPool, _ := dbtest.Open(t)
	st := store.NewWithReplica(e.pool, replicaPool)
	u := e.user("owner", false)
	fresh := e.file(u.ID, "fresh")
	gone := e.file(u.ID, "gone")
	if _, err := e.st.DeleteFile(e.ctx, gone.ID, u.ID); err != nil {
		t.Fatal(err)
	}

	// A public lookup that misses on the replica asks the primary.
	if got, err := st.GetFileBySlug(e.ctx, fresh.Slug); err != nil || got.ID != fresh.ID {
		t.Fatalf("GetFileBySlug(fresh) = %v, %v; want it from the primary", got.ID, err)
	}
	if _, err := st.GetFileBySlug(e.ctx, gone.Slug); err == nil {
		t.Fatal("GetFileBySlug returned a trashed file")
	}
	// A hit on the replica is served from there.
	replicaOnly := store.New(replicaPool)
	ru := store.User{ID: uuid.New(), Username: "r", Email: "r@example.com", PasswordHash: "x"}
	if err := replicaOnly.CreateUser(e.ctx, ru); err != nil {
		t.Fatal(err)
	}
	rf := store.File{ID: uuid.New(), UserID: ru.ID, Slug: "replicated", OriginalName: "r.png", MimeType: "image/png", StorageKey: "k/r"}
	if err := replicaOnly.CreateFile(e.ctx, rf); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetFileBySlug(e.ctx, rf.Slug); err != nil || got.ID != rf.ID {
		t.Fatalf("GetFileBySlug(replicated) = %v, %v; want the replica's row", got.ID, err)
	}

	// The owner's own views read the primary.
	if list, err := st.ListFiles(e.ctx, u.ID, store.FileQuery{Limit: 10}); err != nil || len(list) != 1 || list[0].ID != fresh.ID {
		t.Fatalf("ListFiles = %d files, %v; want the fresh one", len(list), err)
	}
	if trash, err := st.ListTrash(e.ctx, u.ID, 10, 0); err != nil || len(trash) != 1 || trash[0].ID != gone.ID {
		t.Fatalf("ListTrash = %d files, %v; want the trashed one", len(trash), err)
	}
	if stats, err := st.UserStats(e.ctx, u.ID); err != nil || stats.FileCount != 1 {
		t.Fatalf("UserStats = %+v, %v; want one file", stats, err)
	}
}
