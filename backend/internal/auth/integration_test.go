//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"uploader/internal/db/dbtest"
	"uploader/internal/db/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func integrationAuthenticator(t *testing.T) (*Authenticator, *store.Store, store.User) {
	t.Helper()
	pool, _ := dbtest.Open(t)
	st := store.New(pool)
	u := store.User{ID: uuid.New(), Username: "alice", Email: "alice@example.com", PasswordHash: "x"}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	return NewAuthenticator(NewSessionManager(st, key, key, time.Hour, false), st), st, u
}

func bearerRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// A token stored as bcrypt (before tokens were hashed with SHA-256) still
// authenticates, and is re-stored as SHA-256 on that first use.
func TestLegacyBcryptTokenIsUpgradedOnUse(t *testing.T) {
	a, st, u := integrationAuthenticator(t)
	ctx := context.Background()
	const plain = "upl_bcryptEraToken0123456789abcdef" // the old 24-byte shape
	legacy, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(ctx, store.APIToken{
		UserID: u.ID, Name: "sharex", TokenPrefix: TokenPrefix(plain), TokenHash: string(legacy),
	}, 10); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.Identify(bearerRequest(plain[:TokenPrefixLen] + "wrongwrongwrong")); !errors.Is(err, ErrNoSession) {
		t.Fatalf("wrong token with a known prefix: %v, want ErrNoSession", err)
	}
	for i := range 2 {
		got, method, err := a.Identify(bearerRequest(plain))
		if err != nil || got.ID != u.ID || method != MethodToken {
			t.Fatalf("use %d: Identify = %v, %v, %v", i+1, got, method, err)
		}
		tokens, err := st.ListTokens(ctx, u.ID)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("ListTokens = %v, %v", tokens, err)
		}
		if tokens[0].TokenHash != HashToken(plain) {
			t.Fatalf("use %d: stored hash %q, want the SHA-256 form", i+1, tokens[0].TokenHash)
		}
	}
}

// An unknown session id is a missing credential (401), not a lookup failure.
func TestUnknownSessionIsNoSession(t *testing.T) {
	a, _, _ := integrationAuthenticator(t)
	cookie, err := a.sessions.sc.Encode(CookieName, bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	if _, _, err := a.Identify(r); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown session: %v, want ErrNoSession", err)
	}
}
