//go:build integration

package store_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

func newToken(userID uuid.UUID) store.APIToken {
	id := uuid.New()
	return store.APIToken{UserID: userID, Name: "t", TokenPrefix: "upl_" + id.String()[:8], TokenHash: "sha256:" + id.String()}
}

// CreateToken refuses past the per-user cap, counts only the caller's tokens,
// and makes room again once a token is revoked.
func TestCreateTokenCap(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	other := e.user("other", false)
	const limit = 3

	for i := range limit {
		if err := e.st.CreateToken(e.ctx, newToken(u.ID), limit); err != nil {
			t.Fatalf("token %d: %v", i+1, err)
		}
	}
	if err := e.st.CreateToken(e.ctx, newToken(u.ID), limit); !errors.Is(err, store.ErrTokenLimit) {
		t.Fatalf("token past the cap: %v, want ErrTokenLimit", err)
	}
	if err := e.st.CreateToken(e.ctx, newToken(other.ID), limit); err != nil {
		t.Fatalf("another user's token: %v", err)
	}
	tokens, err := e.st.ListTokens(e.ctx, u.ID)
	if err != nil || len(tokens) != limit {
		t.Fatalf("ListTokens = %d, %v; want %d", len(tokens), err, limit)
	}
	if err := e.st.DeleteToken(e.ctx, tokens[0].ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateToken(e.ctx, newToken(u.ID), limit); err != nil {
		t.Fatalf("token after a revocation: %v", err)
	}
	if err := e.st.CreateToken(e.ctx, newToken(uuid.New()), limit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token for a missing user: %v, want ErrNotFound", err)
	}
}

// Parallel requests cannot overshoot the cap: the count and the insert are
// serialised per user.
func TestCreateTokenCapUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	const limit, callers = 5, 20

	errs := parallel(callers, func(int) error { return e.st.CreateToken(e.ctx, newToken(u.ID), limit) })
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, store.ErrTokenLimit):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != limit {
		t.Fatalf("%d tokens created, want exactly %d", ok, limit)
	}
	if n := e.count(`SELECT count(*) FROM api_tokens WHERE user_id = $1`, u.ID); n != limit {
		t.Fatalf("%d tokens stored, want %d", n, limit)
	}
}

// UpgradeTokenHash only replaces the hash it was asked to replace.
func TestUpgradeTokenHash(t *testing.T) {
	e := newEnv(t)
	u := e.user("me", false)
	tok := newToken(u.ID)
	tok.TokenHash = "$2a$10$legacy"
	if err := e.st.CreateToken(e.ctx, tok, 10); err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.ListTokens(e.ctx, u.ID)
	id := stored[0].ID
	if err := e.st.UpgradeTokenHash(e.ctx, id, "$2a$10$somethingelse", "sha256:wrong"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpgradeTokenHash(e.ctx, id, "$2a$10$legacy", "sha256:new"); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.ListTokensByPrefix(e.ctx, tok.TokenPrefix)
	if err != nil || len(got) != 1 || got[0].TokenHash != "sha256:new" {
		t.Fatalf("after upgrade: %+v, %v", got, err)
	}
}
