package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrPushEndpointTaken is returned when a push endpoint is already registered
// to a different account.
var ErrPushEndpointTaken = errors.New("push endpoint belongs to another user")

// PushSubscription is a browser Web Push subscription belonging to a user.
type PushSubscription struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Endpoint string
	P256dh   string
	Auth     string
}

// ErrPushLimit is returned when the user already has the maximum number of
// push subscriptions.
var ErrPushLimit = errors.New("push subscription limit reached")

// SavePushSubscription stores (or refreshes) a subscription, keyed by endpoint.
// An existing row is only refreshed when it already belongs to the same user;
// if the endpoint is registered to someone else it returns ErrPushEndpointTaken
// and changes nothing. Reassigning ownership on conflict let any account that
// learned an endpoint take over (and silence) another user's device.
//
// A new endpoint is refused with ErrPushLimit once the user has maxPerUser
// others; refreshing one they already have always works. The count and the
// write run under a lock on the user's row, so parallel calls cannot overshoot.
func (s *Store) SavePushSubscription(ctx context.Context, sub PushSubscription, maxPerUser int) error {
	if sub.ID == uuid.Nil {
		sub.ID = uuid.New()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockUserRow(ctx, tx, sub.UserID); err != nil {
		return err
	}
	var others int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM push_subscriptions WHERE user_id = $1 AND endpoint <> $2`,
		sub.UserID, sub.Endpoint).Scan(&others); err != nil {
		return err
	}
	if others >= maxPerUser {
		return ErrPushLimit
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (endpoint) DO UPDATE SET
		   p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth
		 WHERE push_subscriptions.user_id = EXCLUDED.user_id`,
		sub.ID, sub.UserID, sub.Endpoint, sub.P256dh, sub.Auth)
	if err != nil {
		return err
	}
	// ON CONFLICT ... DO UPDATE ... WHERE reports 0 rows when the WHERE excluded
	// the conflicting row, i.e. it is owned by another user.
	if tag.RowsAffected() == 0 {
		return ErrPushEndpointTaken
	}
	return tx.Commit(ctx)
}

// ListPushSubscriptions returns all subscriptions for a user.
func (s *Store) ListPushSubscriptions(ctx context.Context, userID uuid.UUID) ([]PushSubscription, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		if err := rows.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.P256dh, &p.Auth); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription removes a subscription by endpoint. This is the
// unscoped variant, reserved for server-side pruning after a push service
// reports the endpoint gone (404/410). User-initiated removal must go through
// DeleteUserPushSubscription so one account cannot unsubscribe another's device.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, endpoint)
	return err
}

// DeleteUserPushSubscription removes a subscription only if it belongs to the
// given user.
func (s *Store) DeleteUserPushSubscription(ctx context.Context, endpoint string, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM push_subscriptions WHERE endpoint = $1 AND user_id = $2`, endpoint, userID)
	return err
}
