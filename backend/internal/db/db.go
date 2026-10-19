// Package db owns the PostgreSQL connection pool and schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PoolOptions tunes the connection pool. Zero values fall back to sane defaults,
// so callers can set only the fields they care about.
type PoolOptions struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

func (o PoolOptions) withDefaults() PoolOptions {
	if o.MaxConns <= 0 {
		o.MaxConns = 12
	}
	if o.MinConns <= 0 {
		o.MinConns = 2
	}
	// pgxpool rejects MinConns > MaxConns; clamp so a small explicit MaxConns
	// (e.g. UPLOADER_DB_MAX_CONNS=1) with a defaulted MinConns can't break boot.
	if o.MinConns > o.MaxConns {
		o.MinConns = o.MaxConns
	}
	if o.MaxConnLifetime <= 0 {
		o.MaxConnLifetime = time.Hour
	}
	if o.MaxConnIdleTime <= 0 {
		o.MaxConnIdleTime = 30 * time.Minute
	}
	return o
}

// Connect opens a pgx connection pool with default tuning and verifies it.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return ConnectWith(ctx, databaseURL, PoolOptions{})
}

// ConnectWith opens a pgx connection pool with the given tuning and verifies
// connectivity. Used for both the primary pool and an optional read-replica.
func ConnectWith(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	opts = opts.withDefaults()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = opts.MaxConns
	cfg.MinConns = opts.MinConns
	cfg.MaxConnLifetime = opts.MaxConnLifetime
	cfg.MaxConnIdleTime = opts.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate runs all pending goose migrations against the database. It opens its
// own *sql.DB (goose works on database/sql) via the pgx stdlib adapter.
func Migrate(ctx context.Context, databaseURL, direction string) error {
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open sql db: %w", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	var run func(context.Context, *sql.DB, string, ...goose.OptionsFunc) error
	switch direction {
	case "up", "":
		run = goose.UpContext
	case "down":
		run = goose.DownContext
	case "reset":
		run = goose.ResetContext
	case "status":
		return goose.StatusContext(ctx, sqlDB, "migrations")
	default:
		return fmt.Errorf("unknown migration direction %q (use up|down|reset|status)", direction)
	}

	unlock, err := lockMigrations(ctx, sqlDB)
	if err != nil {
		return err
	}
	defer unlock()
	return run(ctx, sqlDB, "migrations")
}

// lockMigrations holds a session-level advisory lock on a dedicated connection
// until the returned func is called. goose's package-level API takes no lock of
// its own, so instances booting at the same time (every one runs "up") applied
// the same pending migration concurrently: one of them failed boot on an
// object that already existed, and a migration that is not one transaction
// could be left half-applied. Now the others wait and then find nothing left
// to do. Postgres also releases the lock when the session ends, so a process
// that dies mid-migration does not wedge the next boot.
func lockMigrations(ctx context.Context, sqlDB *sql.DB) (func(), error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lock.DefaultLockID); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", lock.DefaultLockID)
		_ = conn.Close()
	}, nil
}

// ensure the pgx stdlib driver is registered for goose's database/sql usage.
var _ = stdlib.GetDefaultDriver
