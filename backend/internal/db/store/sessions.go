package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateSession stores a new login session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, expires_at, ip, user_agent)
		 VALUES ($1, $2, $3, $4, $5)`,
		sess.ID, sess.UserID, sess.ExpiresAt, sess.IP, sess.UserAgent)
	return err
}

// GetSession returns a non-expired session by id, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, id []byte) (Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, expires_at, ip, user_agent, created_at
		 FROM sessions WHERE id = $1 AND expires_at > now()`, id).
		Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.IP, &sess.UserAgent, &sess.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, id []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// DeleteExpiredSessions purges sessions past their expiry. Returns rows removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, time.Now())
	return tag.RowsAffected(), err
}

// ListSessions returns a user's active (non-expired) sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, expires_at, ip, user_agent, created_at
		 FROM sessions WHERE user_id = $1 AND expires_at > now() ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.IP, &sess.UserAgent, &sess.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteUserSession removes one session if it belongs to the user. Returns
// ErrNotFound if no such session exists.
func (s *Store) DeleteUserSession(ctx context.Context, id []byte, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserSessionsExcept removes all of a user's sessions except keepID
// ("log out everywhere else"). Returns the number removed.
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID uuid.UUID, keepID []byte) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, keepID)
	return tag.RowsAffected(), err
}

// DeleteUserSessions removes every session of a user and returns the number
// removed. Used when the caller authenticated without a session (an API token)
// and so has no "current" session to keep: DeleteUserSessionsExcept with a nil
// keep id would compare `id <> NULL`, which is never true, and delete nothing.
func (s *Store) DeleteUserSessions(ctx context.Context, userID uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return tag.RowsAffected(), err
}
