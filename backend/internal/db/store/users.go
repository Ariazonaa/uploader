package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateUser inserts a new account.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, email, username, password_hash, is_admin)
		 VALUES ($1, $2, $3, $4, $5)`,
		u.ID, u.Email, u.Username, u.PasswordHash, u.IsAdmin)
	return err
}

// CountUsers returns the number of accounts; used to enforce single-user mode.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// firstAdminLockKey is an arbitrary constant key for the advisory lock that
// serializes first-run bootstrap attempts ("uploader-setup").
const firstAdminLockKey int64 = 0x75706c64727374 // "upldrst"

// CreateFirstAdmin creates the bootstrap admin account, but only if no users
// exist yet. It returns true when the account was created and false when one
// already existed. A transaction-scoped advisory lock serializes concurrent
// bootstrap attempts so two simultaneous requests can't both create an admin
// (a plain INSERT ... WHERE NOT EXISTS would not, under READ COMMITTED, since
// each statement reads its own snapshot of the still-empty table).
func (s *Store) CreateFirstAdmin(ctx context.Context, u User) (bool, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Block until any concurrent bootstrap transaction has committed/rolled back.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, firstAdminLockKey); err != nil {
		return false, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, email, username, password_hash, is_admin)
		 VALUES ($1, $2, $3, $4, true)`,
		u.ID, u.Email, u.Username, u.PasswordHash); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// GetUserByID loads a user by id.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, userSelect+` WHERE id = $1`, id))
}

// UpdatePassword sets a new bcrypt password hash for a user.
func (s *Store) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash)
	return err
}

// ErrLastAdmin is returned by DeleteUserUnlessLastAdmin when removing the
// account would leave the instance without any administrator.
var ErrLastAdmin = errors.New("last admin account")

// adminSetLockKey is the advisory-lock key that serializes changes which could
// remove the last administrator ("upldadm").
const adminSetLockKey int64 = 0x75706c6461646d

// DeleteUserUnlessLastAdmin removes the account (files, tokens and sessions
// cascade via FK) unless it is the only remaining admin, in which case it
// returns ErrLastAdmin and deletes nothing. The admin count and the delete run
// in one transaction under an advisory lock: as a separate check-then-act, two
// admins deleting themselves at the same moment could both see "2 admins" and
// both succeed, leaving no admin at all. ErrNotFound if the user is gone.
func (s *Store) DeleteUserUnlessLastAdmin(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminSetLockKey); err != nil {
		return err
	}
	// Re-read the role inside the lock rather than trusting the caller's copy.
	var isAdmin bool
	err = tx.QueryRow(ctx, `SELECT is_admin FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&isAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isAdmin {
		var others int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM users WHERE is_admin AND id <> $1`, id).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetUserByUsername loads a user by username (case-insensitive).
func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, userSelect+` WHERE lower(username) = lower($1)`, username))
}

const userSelect = `SELECT id, email, username, password_hash, is_admin, totp_secret, totp_enabled, totp_recovery, totp_last_step, created_at FROM users`

func (s *Store) scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.PasswordHash, &u.IsAdmin,
		&u.TOTPSecret, &u.TOTPEnabled, &u.TOTPRecovery, &u.TOTPLastStep, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// SetTOTPSecret stores the (encrypted) secret of a new enrolment without
// enabling 2FA yet. It reports false, changing nothing, when 2FA is already
// enabled: unconditionally, a setup racing an enable (a second tab) replaced
// the secret just enabled and switched 2FA off again.
func (s *Store) SetTOTPSecret(ctx context.Context, id uuid.UUID, encSecret string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_secret = $2 WHERE id = $1 AND NOT totp_enabled`, id, encSecret)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// EnableTOTP marks 2FA active and stores the recovery-code hashes, but only
// while 2FA is still off and the pending secret is still encSecret -- the one
// the caller checked the first code against. It reports false otherwise: a
// setup in another tab replaced the secret in between, and enabling would
// bind 2FA to an authenticator the user never scanned, or a double click
// already enabled it, and the recovery codes just shown would be replaced.
func (s *Store) EnableTOTP(ctx context.Context, id uuid.UUID, encSecret, recoveryHashes string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_enabled = true, totp_recovery = $3
		 WHERE id = $1 AND NOT totp_enabled AND totp_secret = $2`, id, encSecret, recoveryHashes)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DisableTOTP clears all 2FA state for a user, including the replay guard (a
// re-enrolled authenticator starts from a fresh secret, so old steps are moot).
func (s *Store) DisableTOTP(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_secret = NULL, totp_enabled = false, totp_recovery = '', totp_last_step = 0 WHERE id = $1`, id)
	return err
}

// ClaimTOTPStep records that a TOTP time step has been used for a login, but
// only if it is newer than the last one accepted. It reports whether the claim
// succeeded; false means the code was already spent, i.e. a replay. The
// conditional UPDATE row-locks the user, so two simultaneous attempts with the
// same code cannot both win — this is what makes the guard safe across
// concurrent requests and across app instances.
func (s *Store) ClaimTOTPStep(ctx context.Context, id uuid.UUID, step int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, id, step)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReplaceTOTPRecovery swaps the stored recovery hashes from old to updated (one
// code removed after it was redeemed), but only if they are still exactly old.
// It reports whether the swap happened; false means another request changed
// the list first (typically: redeemed the same code concurrently), so the
// caller must treat the code as NOT consumed. A blind overwrite would let one
// code log in twice in parallel, and could resurrect a code spent in between.
func (s *Store) ReplaceTOTPRecovery(ctx context.Context, id uuid.UUID, old, updated string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET totp_recovery = $3 WHERE id = $1 AND totp_recovery = $2`, id, old, updated)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RegisterWithInvite atomically claims a single-use invite code and creates a
// (non-admin) user in one transaction. Returns ErrInvalidInvite for a
// missing/used/expired code.
//
// "Used" means used_at is set. used_by is no proof: it is ON DELETE SET NULL,
// so deleting the account that redeemed a code cleared it, and the code
// became redeemable a second time.
//
// Order matters: invite_codes.used_by is a (non-deferrable) foreign key to
// users(id), so the user row must exist before the invite can point at it —
// claiming first failed every registration with an FK violation. The invite is
// still checked (and row-locked with FOR UPDATE) before the user is inserted,
// so a caller without a valid code cannot use this endpoint to probe which
// usernames/emails are taken. Two concurrent claims of the same code serialize
// on that row lock; the loser re-evaluates the WHERE clause after the winner
// commits, sees used_at set and gets ErrInvalidInvite. If user creation fails
// (duplicate username/email) the whole tx rolls back and the code stays unused.
func (s *Store) RegisterWithInvite(ctx context.Context, u User, code string) (User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var one int
	err = tx.QueryRow(ctx,
		`SELECT 1 FROM invite_codes
		 WHERE code = $1 AND used_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		 FOR UPDATE`, code).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInvalidInvite
	}
	if err != nil {
		return User{}, err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, email, username, password_hash, is_admin) VALUES ($1, $2, $3, $4, false)`,
		u.ID, u.Email, u.Username, u.PasswordHash); err != nil {
		return User{}, err
	}

	// The row is locked by this tx, but keep the full condition anyway so the
	// claim can never succeed against a code that is no longer claimable.
	tag, err := tx.Exec(ctx,
		`UPDATE invite_codes SET used_by = $1, used_at = now()
		 WHERE code = $2 AND used_at IS NULL AND (expires_at IS NULL OR expires_at > now())`,
		u.ID, code)
	if err != nil {
		return User{}, err
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrInvalidInvite // rollback also removes the user row
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return u, nil
}
