//go:build integration

package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"uploader/internal/db/dbtest"
	"uploader/internal/db/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// env is one test's private, fully migrated database.
type env struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	st   *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool, _ := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return &env{t: t, ctx: ctx, pool: pool, st: store.New(pool)}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// user creates a user with the given (unique) name.
func (e *env) user(name string, admin bool) store.User {
	e.t.Helper()
	u := store.User{ID: uuid.New(), Username: name, Email: name + "@example.com", PasswordHash: "x", IsAdmin: admin}
	if err := e.st.CreateUser(e.ctx, u); err != nil {
		e.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// file creates a file row for the user.
func (e *env) file(userID uuid.UUID, slug string) store.File {
	e.t.Helper()
	f := store.File{
		ID: uuid.New(), UserID: userID, Slug: slug, OriginalName: slug + ".png",
		MimeType: "image/png", SizeBytes: 100, StorageKey: "k/" + slug,
	}
	if err := e.st.CreateFile(e.ctx, f); err != nil {
		e.t.Fatalf("create file %s: %v", slug, err)
	}
	return f
}

// parallel runs fn(i) for i in [0,n) on n goroutines released at the same
// moment, and returns the per-goroutine errors.
func parallel(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	return errs
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
