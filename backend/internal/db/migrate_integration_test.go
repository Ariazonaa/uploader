//go:build integration

package db_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"uploader/internal/db"
	"uploader/internal/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// newestVersion is the highest migration shipped in migrations/. Bump it with
// every new migration so the down/up round trip below keeps covering the tip.
const newestVersion = 16

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func version(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctxT(t), `SELECT coalesce(max(version_id), 0) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	return v
}

func migrate(t *testing.T, dsn, dir string) {
	t.Helper()
	if err := db.Migrate(ctxT(t), dsn, dir); err != nil {
		t.Fatalf("migrate %s: %v", dir, err)
	}
}

func exists(t *testing.T, pool *pgxpool.Pool, query string, args ...any) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(ctxT(t), `SELECT EXISTS (`+query+`)`, args...).Scan(&ok); err != nil {
		t.Fatalf("exists %q: %v", query, err)
	}
	return ok
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctxT(t), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func hasColumn(t *testing.T, pool *pgxpool.Pool, table, column string) bool {
	return exists(t, pool, `SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, table, column)
}

func hasTrigger(t *testing.T, pool *pgxpool.Pool) bool {
	return exists(t, pool, `SELECT 1 FROM pg_trigger WHERE tgname = 'files_purge_view_stats'`)
}

func hasIndex(t *testing.T, pool *pgxpool.Pool, name string) bool {
	return exists(t, pool, `SELECT 1 FROM pg_indexes WHERE indexname = $1`, name)
}

type statKey struct{ slug, host, ua string }

func viewStats(t *testing.T, pool *pgxpool.Pool) map[statKey]int64 {
	t.Helper()
	rows, err := pool.Query(ctxT(t), `SELECT slug, referrer_host, ua_category, count FROM file_view_stats`)
	if err != nil {
		t.Fatalf("query view stats: %v", err)
	}
	defer rows.Close()
	out := map[statKey]int64{}
	for rows.Next() {
		var k statKey
		var n int64
		if err := rows.Scan(&k.slug, &k.host, &k.ua, &n); err != nil {
			t.Fatalf("scan view stats: %v", err)
		}
		out[k] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("view stats rows: %v", err)
	}
	return out
}

// The fully migrated schema is at the newest version.
func TestMigrateUpReachesNewest(t *testing.T) {
	pool, _ := dbtest.Open(t)
	if v := version(t, pool); v != newestVersion {
		t.Fatalf("version = %d, want %d (bump newestVersion when adding a migration)", v, newestVersion)
	}
	if !hasTrigger(t, pool) || !hasColumn(t, pool, "users", "totp_last_step") {
		t.Fatal("0012/0013 objects missing after up")
	}
	if exists(t, pool, `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'abuse_reports'`) {
		t.Fatal("0014 objects: abuse_reports still present after up")
	}
	if !hasIndex(t, pool, "files_slug_lower_uniq") {
		t.Fatal("0015 objects: files_slug_lower_uniq missing after up")
	}
	for _, name := range files0016Indexes {
		if !hasIndex(t, pool, name) {
			t.Fatalf("0016 objects: %s missing after up", name)
		}
	}
}

// files0016Indexes are the indexes migration 0016 adds.
var files0016Indexes = []string{
	"files_created_at_idx", "files_expires_at_idx", "files_max_views_idx",
	"files_storage_key_idx", "files_thumb_key_idx",
}

// downTo15 rolls back 0016 and checks that its indexes are gone.
func downTo15(t *testing.T, pool *pgxpool.Pool, dsn string) {
	t.Helper()
	migrate(t, dsn, "down")
	if v := version(t, pool); v != 15 {
		t.Fatalf("after rolling back 0016: version = %d, want 15", v)
	}
	for _, name := range files0016Indexes {
		if hasIndex(t, pool, name) {
			t.Fatalf("0016 down left %s behind", name)
		}
	}
}

// 0016's partial indexes must be usable by the janitor's expiry query: with
// sequential scans off, the planner has to reach the rows through them.
func TestMigrateExpiryIndexesUsable(t *testing.T) {
	pool, _ := dbtest.Open(t)
	ctx := ctxT(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN SELECT id FROM files
		WHERE (expires_at IS NOT NULL AND expires_at <= now())
		   OR (max_views IS NOT NULL AND views >= max_views)`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	for _, name := range []string{"files_expires_at_idx", "files_max_views_idx"} {
		if !strings.Contains(plan.String(), name) {
			t.Errorf("expiry query plan does not use %s:\n%s", name, plan.String())
		}
	}
}

// 0015 adds the case-insensitive slug index. Rows that already differ only in
// case must not fail the migration (and with it every boot): the index is
// skipped instead.
func TestMigrateSlugIndexSkipsCaseDuplicates(t *testing.T) {
	pool, dsn := dbtest.Open(t)
	downTo15(t, pool, dsn)
	migrate(t, dsn, "down")
	if v := version(t, pool); v != 14 || hasIndex(t, pool, "files_slug_lower_uniq") {
		t.Fatalf("after one down: version %d, index still present: %v", v, hasIndex(t, pool, "files_slug_lower_uniq"))
	}
	userID := uuid.New()
	mustExec(t, pool, `INSERT INTO users (id, email, username, password_hash) VALUES ($1, 'c@example.com', 'c', 'x')`, userID)
	for _, slug := range []string{"abcd", "ABCD"} {
		mustExec(t, pool, `INSERT INTO files (id, user_id, slug, original_name, mime_type, size_bytes, storage_key)
			VALUES ($1, $2, $3, 'a.png', 'image/png', 1, 'k/' || $3)`, uuid.New(), userID, slug)
	}
	migrate(t, dsn, "up")
	if v := version(t, pool); v != newestVersion {
		t.Fatalf("after up: version = %d, want %d", v, newestVersion)
	}
	if hasIndex(t, pool, "files_slug_lower_uniq") {
		t.Fatal("0015 created the index although case duplicates exist")
	}
}

// Roll back 0016 to 0012, then seed data shaped like a pre-0013 deployment
// (orphaned and junk view stats), migrate up again and check 0013's backfill.
// On the way down, 0013's down must still rebuild the old one-report-per-slug
// index from data only the 0013 schema allows.
func TestMigrateDownUpNewest(t *testing.T) {
	pool, dsn := dbtest.Open(t)

	downTo15(t, pool, dsn)
	migrate(t, dsn, "down")
	if v := version(t, pool); v != 14 || hasIndex(t, pool, "files_slug_lower_uniq") {
		t.Fatalf("after one down: version = %d, want 14 without files_slug_lower_uniq", v)
	}
	migrate(t, dsn, "down")
	if v := version(t, pool); v != 13 {
		t.Fatalf("after two downs: version = %d, want 13", v)
	}
	if !hasIndex(t, pool, "abuse_reports_open_reporter_uniq") || !hasIndex(t, pool, "abuse_reports_slug_idx") ||
		!hasIndex(t, pool, "abuse_reports_status_idx") {
		t.Fatal("0014 down did not restore the 0013 abuse_reports table")
	}
	// The file FK must come back too, still nulling the reference on purge.
	if !exists(t, pool, `SELECT 1 FROM information_schema.referential_constraints rc
		JOIN information_schema.table_constraints tc ON tc.constraint_name = rc.constraint_name
		WHERE tc.table_name = 'abuse_reports' AND rc.delete_rule = 'SET NULL'`) {
		t.Fatal("0014 down did not restore the abuse_reports.file_id ON DELETE SET NULL foreign key")
	}

	// Several reports per slug, as 0013 allows; 0013's down keeps the oldest.
	mustExec(t, pool, `INSERT INTO abuse_reports (id, slug, reporter_email, reason, created_at)
		VALUES ($1, 'live', 'a@example.com', 'r1', now() - interval '1 hour')`, uuid.New())
	mustExec(t, pool, `INSERT INTO abuse_reports (id, slug, reporter_email, reason) VALUES ($1, 'live', 'b@example.com', 'r2')`, uuid.New())
	mustExec(t, pool, `INSERT INTO abuse_reports (id, slug, reporter_email, reason) VALUES ($1, 'live', '', 'r3')`, uuid.New())

	migrate(t, dsn, "down")
	if v := version(t, pool); v != 12 {
		t.Fatalf("after three downs: version = %d, want 12", v)
	}
	if hasTrigger(t, pool) || hasIndex(t, pool, "abuse_reports_open_reporter_uniq") || !hasIndex(t, pool, "abuse_reports_slug_uniq") {
		t.Fatal("0013 down did not restore the pre-0013 schema")
	}
	var n int
	var reason string
	if err := pool.QueryRow(ctxT(t), `SELECT count(*), min(reason) FROM abuse_reports WHERE slug = 'live'`).Scan(&n, &reason); err != nil {
		t.Fatal(err)
	}
	if n != 1 || reason != "r1" {
		t.Fatalf("after 0013 down: %d report(s), kept %q; want 1, r1", n, reason)
	}
	migrate(t, dsn, "down")
	if v := version(t, pool); v != 11 {
		t.Fatalf("after four downs: version = %d, want 11", v)
	}
	if hasColumn(t, pool, "users", "totp_last_step") {
		t.Fatal("0012 down left users.totp_last_step behind")
	}

	// Seed at schema version 11.
	userID := uuid.New()
	mustExec(t, pool, `INSERT INTO users (id, email, username, password_hash) VALUES ($1, 'm@example.com', 'm', 'x')`, userID)
	fileID := uuid.New()
	mustExec(t, pool, `INSERT INTO files (id, user_id, slug, original_name, mime_type, size_bytes, storage_key)
		VALUES ($1, $2, 'live', 'a.png', 'image/png', 1, 'k/live')`, fileID, userID)
	long := strings.Repeat("a", 300)
	for _, r := range []struct {
		slug, host, ua string
		n              int64
	}{
		{"live", "example.com", "desktop", 5},
		{"live", "Example.ORG", "desktop", 2},  // valid host, lowercased by the backfill
		{"live", "EXAMPLE.com.", "desktop", 4}, // merges into example.com
		{"live", "example.org.", "mobile", 6},  // trailing dot only
		{"live", "Bad Host!!", "desktop", 3},
		{"live", long, "desktop", 2},
		{"live", "(other)", "desktop", 1},
		{"live", "(direct)", "mobile", 4},
		{"live", "bad_host", "mobile", 7},
		{"live", "1.2.3.4", "bot", 1},
		{"gone", "example.com", "desktop", 9}, // orphan: no such file
		{"gone", "(direct)", "desktop", 1},
	} {
		mustExec(t, pool, `INSERT INTO file_view_stats (slug, referrer_host, ua_category, count) VALUES ($1, $2, $3, $4)`,
			r.slug, r.host, r.ua, r.n)
	}
	migrate(t, dsn, "up")
	if v := version(t, pool); v != newestVersion {
		t.Fatalf("after up: version = %d, want %d", v, newestVersion)
	}

	got := viewStats(t, pool)
	want := map[statKey]int64{
		{"live", "example.com", "desktop"}: 5 + 4,
		{"live", "example.org", "desktop"}: 2,
		{"live", "example.org", "mobile"}:  6,
		{"live", "(other)", "desktop"}:     1 + 3 + 2,
		{"live", "(direct)", "mobile"}:     4,
		{"live", "(other)", "mobile"}:      7,
		{"live", "1.2.3.4", "bot"}:         1,
	}
	if len(got) != len(want) {
		t.Errorf("view stats after backfill = %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("view stats %v = %d, want %d (all: %v)", k, got[k], n, got)
		}
	}

	if !hasTrigger(t, pool) {
		t.Fatal("up did not recreate the 0013 view-stats trigger")
	}
	// 0014 drops the reports table, rows and all.
	if exists(t, pool, `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'abuse_reports'`) {
		t.Fatal("0014 up left the abuse_reports table behind")
	}
}

// Instances booting together all run "up" against an empty database; the
// advisory lock makes them take turns instead of applying the same migrations
// at once.
func TestMigrateUpConcurrent(t *testing.T) {
	pool, dsn := dbtest.Open(t)
	migrate(t, dsn, "reset")

	const n = 4
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			errs <- db.Migrate(ctxT(t), dsn, "up")
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent up: %v", err)
		}
	}
	if v := version(t, pool); v != newestVersion {
		t.Fatalf("after concurrent up: version = %d, want %d", v, newestVersion)
	}
	var applied int
	if err := pool.QueryRow(ctxT(t), `SELECT count(*) FROM goose_db_version WHERE version_id = $1 AND is_applied`, newestVersion).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("newest migration recorded %d times, want once", applied)
	}
}

// Every down migration must run: reset to zero, then back up.
func TestMigrateResetAndUp(t *testing.T) {
	pool, dsn := dbtest.Open(t)
	migrate(t, dsn, "reset")
	if v := version(t, pool); v != 0 {
		t.Fatalf("after reset: version = %d, want 0", v)
	}
	if exists(t, pool, `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'files'`) {
		t.Fatal("reset left the files table behind")
	}
	migrate(t, dsn, "up")
	if v := version(t, pool); v != newestVersion {
		t.Fatalf("after reset+up: version = %d, want %d", v, newestVersion)
	}
}
