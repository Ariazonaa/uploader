//go:build integration

package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"uploader/internal/db"
)

// EnvURL names the environment variable holding the server DSN.
const EnvURL = "UPLOADER_TEST_DATABASE_URL"

var (
	// template is the per-run database the app's migrations were applied to;
	// every test database is cloned from it (CREATE DATABASE ... TEMPLATE),
	// which is far cheaper than migrating each one from scratch.
	template string
	// createMu serializes clones: Postgres refuses to copy a template while
	// another session is connected to it, and a concurrent clone counts.
	createMu sync.Mutex
)

// Main is the body of a package's TestMain. It creates and migrates the
// template database (using the app's own db.Migrate), runs the tests and drops
// the template again. With UPLOADER_TEST_DATABASE_URL unset it just runs the
// tests, which then skip themselves in Open.
func Main(m *testing.M) int {
	base := os.Getenv(EnvURL)
	if base == "" {
		return m.Run()
	}
	goose.SetLogger(goose.NopLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	name := uniqueName("tmpl")
	if err := exec(ctx, base, "CREATE DATABASE "+quote(name)); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "dbtest: create template database: %v\n", err)
		return 1
	}
	if err := db.Migrate(ctx, WithDatabase(base, name), "up"); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "dbtest: migrate template database: %v\n", err)
		dropDatabase(base, name)
		return 1
	}
	cancel()
	template = name

	code := m.Run()
	dropDatabase(base, name)
	return code
}

// Create clones a fresh, fully migrated database for this test and returns its
// DSN. The database is dropped when the test finishes. Skips the test when
// UPLOADER_TEST_DATABASE_URL is unset.
func Create(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skip(EnvURL + " not set; skipping database integration test")
	}
	if template == "" {
		t.Fatal("dbtest: template database missing; call dbtest.Main from TestMain")
	}
	name := uniqueName("t")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	createMu.Lock()
	err := exec(ctx, base, "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(template))
	createMu.Unlock()
	if err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}
	t.Cleanup(func() { dropDatabase(base, name) })
	return WithDatabase(base, name)
}

// Open is Create plus a connection pool on the new database (closed before
// the database is dropped). The pool is sized for the concurrency tests.
func Open(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := Create(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.ConnectWith(ctx, dsn, db.PoolOptions{MaxConns: 32, MinConns: 1})
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// WithDatabase returns dsn pointed at database name. Both URL and
// keyword/value DSNs are supported.
func WithDatabase(dsn, name string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err == nil {
			u.Path = "/" + name
			u.RawPath = ""
			return u.String()
		}
	}
	// In keyword/value form a later key overrides an earlier one.
	return dsn + " dbname=" + name
}

func exec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

// dropDatabase removes a database this package created. WITH (FORCE) (PG 13+)
// terminates connections a failed test may have leaked.
func dropDatabase(base, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec(ctx, base, "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)"); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: drop database %s: %v\n", name, err)
	}
}

func uniqueName(kind string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("uploader_it_%s_%d_%s", kind, os.Getpid(), hex.EncodeToString(b[:]))
}

func quote(name string) string { return pgx.Identifier{name}.Sanitize() }
