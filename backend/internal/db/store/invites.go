package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CreateInvite stores a new single-use invite code.
func (s *Store) CreateInvite(ctx context.Context, code string, createdBy uuid.UUID, expiresAt *time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO invite_codes (code, created_by, expires_at) VALUES ($1, $2, $3)`,
		code, createdBy, expiresAt)
	return err
}

// ListInvites returns all invite codes, newest first.
func (s *Store) ListInvites(ctx context.Context) ([]InviteCode, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT code, created_by, used_by, used_at, expires_at, created_at
		 FROM invite_codes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InviteCode
	for rows.Next() {
		var c InviteCode
		if err := rows.Scan(&c.Code, &c.CreatedBy, &c.UsedBy, &c.UsedAt, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeInvite deletes an unused invite code. Returns ErrNotFound if it does not
// exist or was already used (used codes are kept as an audit trail). Used means
// used_at is set; used_by is cleared when the account that redeemed the code
// is deleted.
func (s *Store) RevokeInvite(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM invite_codes WHERE code = $1 AND used_at IS NULL`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
