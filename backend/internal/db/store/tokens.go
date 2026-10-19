package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrTokenLimit is returned by CreateToken when the user already holds the
// maximum number of API tokens.
var ErrTokenLimit = errors.New("api token limit reached")

// CreateToken stores a new API token (hash + prefix) unless the user already
// holds maxPerUser tokens, in which case it returns ErrTokenLimit. The count
// and the insert run under a lock on the user's row, so parallel requests
// cannot overshoot the cap.
func (s *Store) CreateToken(ctx context.Context, t APIToken, maxPerUser int) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockUserRow(ctx, tx, t.UserID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = $1`, t.UserID).Scan(&n); err != nil {
		return err
	}
	if n >= maxPerUser {
		return ErrTokenLimit
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash)
		 VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.UserID, t.Name, t.TokenPrefix, t.TokenHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockUserRow locks the user's row until tx ends, serialising a per-account
// count-then-insert cap. FOR NO KEY UPDATE does not conflict with the FOR KEY
// SHARE lock a foreign-key check takes, so uploads and other inserts that
// reference the user are not held up. ErrNotFound if the user is gone.
func lockUserRow(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ListTokens returns all tokens for a user, newest first.
func (s *Store) ListTokens(ctx context.Context, userID uuid.UUID) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, tokenSelect+` WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTokens(rows)
}

// ListTokensByPrefix returns candidate tokens for a given prefix; the caller
// compares the hashes to find the match.
func (s *Store) ListTokensByPrefix(ctx context.Context, prefix string) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, tokenSelect+` WHERE token_prefix = $1`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTokens(rows)
}

// DeleteToken removes a token owned by the user. Returns ErrNotFound if absent.
func (s *Store) DeleteToken(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpgradeTokenHash replaces a token's stored hash, but only while it still
// holds oldHash, so a concurrent upgrade or revocation is never overwritten.
func (s *Store) UpgradeTokenHash(ctx context.Context, id uuid.UUID, oldHash, newHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE api_tokens SET token_hash = $3 WHERE id = $1 AND token_hash = $2`, id, oldHash, newHash)
	return err
}

// TouchToken records the last-used timestamp (best-effort).
func (s *Store) TouchToken(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $1 WHERE id = $2`, time.Now(), id)
	return err
}

// #nosec G101 -- a SQL column list, not a credential. token_hash names the
// column holding the token digest; no secret is embedded here.
const tokenSelect = `SELECT id, user_id, name, token_prefix, token_hash, last_used_at, created_at FROM api_tokens`

func scanTokens(rows pgx.Rows) ([]APIToken, error) {
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenPrefix, &t.TokenHash, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return out, nil
}
