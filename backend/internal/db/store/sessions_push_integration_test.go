//go:build integration

package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

func (e *env) session(userID uuid.UUID, id byte, ttl time.Duration) []byte {
	e.t.Helper()
	sid := bytes.Repeat([]byte{id}, 32)
	if err := e.st.CreateSession(e.ctx, store.Session{ID: sid, UserID: userID, ExpiresAt: time.Now().Add(ttl), IP: "127.0.0.1", UserAgent: "test"}); err != nil {
		e.t.Fatalf("create session: %v", err)
	}
	return sid
}

func TestDeleteUserSessionsExcept(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	other := e.user("other", false)
	keep := e.session(u.ID, 1, time.Hour)
	e.session(u.ID, 2, time.Hour)
	e.session(u.ID, 3, time.Hour)
	e.session(u.ID, 4, -time.Hour) // expired, still a row
	otherSess := e.session(other.ID, 5, time.Hour)

	n, err := e.st.DeleteUserSessionsExcept(e.ctx, u.ID, keep)
	if err != nil || n != 3 {
		t.Fatalf("DeleteUserSessionsExcept = %d, %v; want 3", n, err)
	}
	if _, err := e.st.GetSession(e.ctx, keep); err != nil {
		t.Fatalf("kept session gone: %v", err)
	}
	if _, err := e.st.GetSession(e.ctx, otherSess); err != nil {
		t.Fatalf("another user's session was removed: %v", err)
	}
	// A nil keep id compares id <> NULL, which is never true: nothing is
	// deleted. This is why the API uses DeleteUserSessions for token callers.
	if n, err := e.st.DeleteUserSessionsExcept(e.ctx, u.ID, nil); err != nil || n != 0 {
		t.Fatalf("DeleteUserSessionsExcept(nil) = %d, %v; want 0 (documented SQL NULL semantics)", n, err)
	}
}

func TestDeleteUserSessions(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	other := e.user("other", false)
	e.session(u.ID, 1, time.Hour)
	e.session(u.ID, 2, -time.Hour)
	otherSess := e.session(other.ID, 3, time.Hour)

	n, err := e.st.DeleteUserSessions(e.ctx, u.ID)
	if err != nil || n != 2 {
		t.Fatalf("DeleteUserSessions = %d, %v; want 2", n, err)
	}
	if left, _ := e.st.ListSessions(e.ctx, u.ID); len(left) != 0 {
		t.Fatalf("%d sessions left", len(left))
	}
	if _, err := e.st.GetSession(e.ctx, otherSess); err != nil {
		t.Fatalf("another user's session was removed: %v", err)
	}
	if n, err := e.st.DeleteUserSessions(e.ctx, u.ID); err != nil || n != 0 {
		t.Fatalf("second DeleteUserSessions = %d, %v; want 0", n, err)
	}
}

func TestSessionExpiryAndScopedDelete(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	other := e.user("other", false)
	live := e.session(u.ID, 1, time.Hour)
	dead := e.session(u.ID, 2, -time.Minute)

	if _, err := e.st.GetSession(e.ctx, dead); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session lookup = %v, want ErrNotFound", err)
	}
	list, err := e.st.ListSessions(e.ctx, u.ID)
	if err != nil || len(list) != 1 || !bytes.Equal(list[0].ID, live) {
		t.Fatalf("ListSessions = %v, %v; want only the live session", list, err)
	}
	if err := e.st.DeleteUserSession(e.ctx, live, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting someone else's session = %v, want ErrNotFound", err)
	}
	if n, err := e.st.DeleteExpiredSessions(e.ctx); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v; want 1", n, err)
	}
	if err := e.st.DeleteUserSession(e.ctx, live, u.ID); err != nil {
		t.Fatal(err)
	}
}

// A user cannot register more than the cap of endpoints, but refreshing one
// they already have still works at the cap, and parallel calls cannot
// overshoot it.
func TestSavePushSubscriptionCap(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	const limit = 3
	sub := func(i int) store.PushSubscription {
		return store.PushSubscription{UserID: u.ID, Endpoint: fmt.Sprintf("https://fcm.googleapis.com/fcm/send/%d", i), P256dh: "p", Auth: "a"}
	}

	errs := parallel(10, func(i int) error { return e.st.SavePushSubscription(e.ctx, sub(i), limit) })
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrPushLimit):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != limit {
		t.Fatalf("%d subscriptions saved in parallel, want exactly %d", ok, limit)
	}
	subs, err := e.st.ListPushSubscriptions(e.ctx, u.ID)
	if err != nil || len(subs) != limit {
		t.Fatalf("ListPushSubscriptions = %d, %v", len(subs), err)
	}
	refresh := subs[0]
	refresh.P256dh = "fresh"
	if err := e.st.SavePushSubscription(e.ctx, refresh, limit); err != nil {
		t.Fatalf("refreshing an existing subscription at the cap: %v", err)
	}
	if err := e.st.SavePushSubscription(e.ctx, sub(100), limit); !errors.Is(err, store.ErrPushLimit) {
		t.Fatalf("new subscription at the cap: %v, want ErrPushLimit", err)
	}
}

func TestSavePushSubscriptionOwnership(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice", false)
	mallory := e.user("mallory", false)
	const ep = "https://fcm.googleapis.com/fcm/send/abc"

	if err := e.st.SavePushSubscription(e.ctx, store.PushSubscription{UserID: alice.ID, Endpoint: ep, P256dh: "p1", Auth: "a1"}, 20); err != nil {
		t.Fatal(err)
	}
	// Same user re-subscribing refreshes the keys.
	if err := e.st.SavePushSubscription(e.ctx, store.PushSubscription{UserID: alice.ID, Endpoint: ep, P256dh: "p2", Auth: "a2"}, 20); err != nil {
		t.Fatalf("re-subscribe by owner: %v", err)
	}
	// Another user presenting the same endpoint is refused and changes nothing.
	err := e.st.SavePushSubscription(e.ctx, store.PushSubscription{UserID: mallory.ID, Endpoint: ep, P256dh: "evil", Auth: "evil"}, 20)
	if !errors.Is(err, store.ErrPushEndpointTaken) {
		t.Fatalf("takeover = %v, want ErrPushEndpointTaken", err)
	}
	subs, err := e.st.ListPushSubscriptions(e.ctx, alice.ID)
	if err != nil || len(subs) != 1 {
		t.Fatalf("alice subs = %v, %v", subs, err)
	}
	if s := subs[0]; s.UserID != alice.ID || s.P256dh != "p2" || s.Auth != "a2" {
		t.Fatalf("subscription after takeover attempt = %+v", s)
	}
	if subs, _ := e.st.ListPushSubscriptions(e.ctx, mallory.ID); len(subs) != 0 {
		t.Fatalf("mallory owns %d subscription(s)", len(subs))
	}
	// The scoped delete does not let mallory unsubscribe alice's device.
	if err := e.st.DeleteUserPushSubscription(e.ctx, ep, mallory.ID); err != nil {
		t.Fatal(err)
	}
	if subs, _ := e.st.ListPushSubscriptions(e.ctx, alice.ID); len(subs) != 1 {
		t.Fatal("scoped delete removed another user's subscription")
	}
	if err := e.st.DeleteUserPushSubscription(e.ctx, ep, alice.ID); err != nil {
		t.Fatal(err)
	}
	// Once freed, the endpoint can be registered by someone else.
	if err := e.st.SavePushSubscription(e.ctx, store.PushSubscription{UserID: mallory.ID, Endpoint: ep, P256dh: "m", Auth: "m"}, 20); err != nil {
		t.Fatalf("subscribe to a freed endpoint: %v", err)
	}
	if err := e.st.DeletePushSubscription(e.ctx, ep); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM push_subscriptions`); n != 0 {
		t.Fatalf("%d subscription(s) left after unscoped delete", n)
	}
}
