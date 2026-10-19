//go:build integration

package upload

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"uploader/internal/db/dbtest"
	"uploader/internal/db/store"
	"uploader/internal/storage"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// hookStorage runs onPut before every blob write.
type hookStorage struct {
	storage.Storage
	onPut func() error
}

func (h *hookStorage) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	if h.onPut != nil {
		if err := h.onPut(); err != nil {
			return 0, err
		}
	}
	return h.Storage.Put(ctx, key, r)
}

type svcEnv struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	st    *store.Store
	blobs *hookStorage
	svc   *Service
	user  uuid.UUID
}

func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	pool, _ := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	st := store.New(pool)
	fs, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs := &hookStorage{Storage: fs}
	u := store.User{ID: uuid.New(), Username: "up", Email: "up@example.com", PasswordHash: "x"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	return &svcEnv{t: t, ctx: ctx, pool: pool, st: st, blobs: blobs, svc: NewService(st, blobs, 6), user: u.ID}
}

func (e *svcEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *svcEnv) save(content string, opts SaveOptions) (store.File, error) {
	e.t.Helper()
	return e.svc.SaveWith(e.ctx, e.user, strings.NewReader(content), "a.txt", opts)
}

func (e *svcEnv) fileCount() int {
	e.t.Helper()
	files, err := e.st.ListAllFiles(e.ctx, e.user)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(files)
}

// A client that hangs up once its upload is committed still gets (and keeps)
// the stored file.
func TestSaveSurvivesClientHangUpAfterCommit(t *testing.T) {
	e := newSvcEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.blobs.onPut = func() error { cancel(); return nil }

	f, err := e.svc.SaveWith(ctx, e.user, strings.NewReader("hello world"), "a.txt", SaveOptions{})
	if err != nil {
		t.Fatalf("SaveWith after the client hung up mid-write: %v", err)
	}
	if _, err := e.st.GetFileBySlugPrimary(e.ctx, f.Slug); err != nil {
		t.Fatalf("row missing: %v", err)
	}
	if _, err := e.blobs.Stat(e.ctx, f.StorageKey); err != nil {
		t.Fatalf("blob missing: %v", err)
	}
}

// A failed blob write rolls its row back even when the client's context is
// already gone; otherwise a row would point at a blob that never existed.
func TestSaveRollsBackAfterClientHangUp(t *testing.T) {
	e := newSvcEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.blobs.onPut = func() error { cancel(); return errors.New("disk full") }

	if _, err := e.svc.SaveWith(ctx, e.user, strings.NewReader("hello world"), "a.txt", SaveOptions{}); err == nil {
		t.Fatal("SaveWith succeeded although the blob write failed")
	}
	if n := e.fileCount(); n != 0 {
		t.Fatalf("%d row(s) left behind by a failed blob write", n)
	}
}

// Dedup hands out the newest copy of the content, and only if its blob is in
// place and it is not about to fall out of the retention window.
func TestDedupOnlyReusesSafeCopy(t *testing.T) {
	e := newSvcEnv(t)
	mustSave := func() store.File {
		t.Helper()
		f, err := e.save("same bytes", SaveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	first := mustSave()
	if again := mustSave(); again.ID != first.ID {
		t.Fatal("identical upload was not deduplicated")
	}

	// A copy whose blob is missing (still being written, or lost) is not reused.
	if err := e.blobs.Delete(e.ctx, first.StorageKey); err != nil {
		t.Fatal(err)
	}
	second := mustSave()
	if second.ID == first.ID {
		t.Fatal("deduplicated against a copy without a blob")
	}
	e.exec(`DELETE FROM files WHERE id = $1`, first.ID)

	// With 30-day retention, a 20-day-old copy is not reused …
	days := 30
	e.svc.SetRetention(func() int { return days })
	e.exec(`UPDATE files SET created_at = now() - interval '20 days' WHERE id = $1`, second.ID)
	third := mustSave()
	if third.ID == second.ID {
		t.Fatal("deduplicated against a copy close to the retention cutoff")
	}
	// … a younger one is, and the newest copy wins over older ones.
	e.exec(`UPDATE files SET created_at = now() - interval '2 days' WHERE id = $1`, second.ID)
	e.exec(`UPDATE files SET created_at = now() - interval '1 day' WHERE id = $1`, third.ID)
	if got := mustSave(); got.ID != third.ID {
		t.Fatalf("deduplicated against %s, want the newest copy %s", got.Slug, third.Slug)
	}
	// Without retention, age does not matter.
	days = 0
	e.exec(`UPDATE files SET created_at = now() - interval '1000 days' WHERE id IN ($1, $2)`, second.ID, third.ID)
	if got := mustSave(); got.ID != second.ID && got.ID != third.ID {
		t.Fatal("old copy not reused without retention")
	}
	if n := e.fileCount(); n != 2 {
		t.Fatalf("%d files stored, want 2", n)
	}
}

// A slug that differs from a taken one only in case collides: a custom slug is
// refused with ErrSlugTaken, a random one is drawn again.
func TestSlugCollisionIgnoresCase(t *testing.T) {
	e := newSvcEnv(t)
	taken, err := e.save("one", SaveOptions{CustomSlug: "MyLink"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.save("two", SaveOptions{CustomSlug: "mylink"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("custom slug differing only in case: err %v, want ErrSlugTaken", err)
	}

	draws := []string{"MYLINK", "fresh1"}
	generateSlug = func(string, int) (string, error) {
		s := draws[0]
		draws = draws[1:]
		return s, nil
	}
	t.Cleanup(func() { generateSlug = GenerateSlug })
	f, err := e.save("three", SaveOptions{})
	if err != nil || f.Slug != "fresh1" {
		t.Fatalf("random slug colliding with %q in case only: got %q, %v; want a re-roll to fresh1", taken.Slug, f.Slug, err)
	}
}
