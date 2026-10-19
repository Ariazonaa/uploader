// Package store contains the domain models and all database access. Queries are
// hand-written against pgx — explicit SQL, no ORM, easy to audit.
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the connection pool and exposes typed query methods. An optional
// read pool (a read-replica) serves read-heavy public queries; writes always go
// to the primary pool.
type Store struct {
	pool *pgxpool.Pool
	read *pgxpool.Pool
}

// New returns a Store backed by a single pool (read == write).
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, read: pool}
}

// NewWithReplica returns a Store that routes read-heavy queries to read and all
// writes to write. Pass the same pool for both to disable replica routing.
func NewWithReplica(write, read *pgxpool.Pool) *Store {
	if read == nil {
		read = write
	}
	return &Store{pool: write, read: read}
}

// reader returns the pool to use for read-only queries (the replica if set).
func (s *Store) reader() *pgxpool.Pool { return s.read }

// Ping verifies database connectivity (readiness check).
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// User is an account. In single-user mode there is exactly one row.
type User struct {
	ID           uuid.UUID
	Email        string
	Username     string
	PasswordHash string
	IsAdmin      bool
	TOTPSecret   *string // AES-GCM encrypted; nil when 2FA not set up
	TOTPEnabled  bool
	TOTPRecovery string // newline-separated bcrypt hashes
	TOTPLastStep int64  // highest TOTP time step already spent (replay guard)
	CreatedAt    time.Time
}

// InviteCode is a single-use registration invite.
type InviteCode struct {
	Code      string
	CreatedBy *uuid.UUID
	UsedBy    *uuid.UUID
	UsedAt    *time.Time
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Session is a server-side login session referenced by an encrypted cookie.
type Session struct {
	ID        []byte
	UserID    uuid.UUID
	ExpiresAt time.Time
	IP        string
	UserAgent string
	CreatedAt time.Time
}

// APIToken authenticates programmatic clients (ShareX, scripts). Only the
// bcrypt hash is stored; TokenPrefix is a non-secret lookup hint.
type APIToken struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Name        string
	TokenPrefix string
	TokenHash   string
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

// File is an uploaded blob plus its metadata.
type File struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Slug          string
	OriginalName  string
	MimeType      string
	SizeBytes     int64
	StorageKey    string
	ThumbKey      *string
	Width         *int
	Height        *int
	DurationMs    *int64
	Views         int64
	DeleteKey     *string
	ContentHash   *string
	DominantColor *string
	HasHLS        bool
	CreatedAt     time.Time
	DeletedAt     *time.Time // non-nil = in trash (soft-deleted)
	Encrypted     bool       // blob is stored AES-256-GCM encrypted at rest
}

// Stats is an aggregate usage summary for a user.
type Stats struct {
	FileCount  int64
	TotalBytes int64
	TotalViews int64
}

// DayCount is the number of uploads on a given day (for activity/streak/wrapped).
type DayCount struct {
	Day   time.Time
	Count int64
}
