//go:build integration

package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// routedServer is an integration server with real authentication, reachable
// through the full router.
func routedServer(t *testing.T) (*Server, *store.Store, http.Handler) {
	t.Helper()
	s, st := integrationServer(t)
	key := bytes.Repeat([]byte{5}, 32)
	s.sessions = auth.NewSessionManager(st, key, key, time.Hour, false)
	s.authn = auth.NewAuthenticator(s.sessions, st)
	return s, st, s.Routes()
}

// apiToken creates a token for userID and returns its plaintext.
func apiToken(t *testing.T, st *store.Store, userID uuid.UUID) string {
	t.Helper()
	gen, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToken(context.Background(), store.APIToken{
		UserID: userID, Name: "test", TokenPrefix: gen.Prefix, TokenHash: gen.Hash,
	}, maxTokensPerUser); err != nil {
		t.Fatal(err)
	}
	return gen.Plaintext
}

// Token management and account security accept only a browser session: a
// leaked API token must not mint tokens, revoke sessions or take the account.
// Everything else, admin endpoints included, still works with a token.
func TestAccountSecurityRoutesRefuseAPITokens(t *testing.T) {
	s, st, h := routedServer(t)
	adminID := mustAdmin(t, st)
	token := apiToken(t, st, adminID)

	do := func(method, path string, auth func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.RemoteAddr = "192.0.2.1:1234"
		auth(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }

	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodDelete, "/api/v1/tokens/" + uuid.NewString()},
		{http.MethodPost, "/api/v1/auth/password"},
		{http.MethodPost, "/api/v1/auth/totp/setup"},
		{http.MethodPost, "/api/v1/auth/totp/enable"},
		{http.MethodPost, "/api/v1/auth/totp/disable"},
		{http.MethodGet, "/api/v1/auth/sessions"},
		{http.MethodDelete, "/api/v1/auth/sessions/00"},
		{http.MethodPost, "/api/v1/auth/sessions/revoke-others"},
		{http.MethodDelete, "/api/v1/auth/account"},
		{http.MethodGet, "/api/v1/auth/export"},
	} {
		w := do(rt.method, rt.path, bearer)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "API tokens are not accepted") {
			t.Errorf("%s %s with an API token: status %d (%s), want 403", rt.method, rt.path, w.Code, w.Body)
		}
	}

	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/invites"},
	} {
		if w := do(rt.method, rt.path, bearer); w.Code != http.StatusOK {
			t.Errorf("%s %s with an API token: status %d (%s), want 200", rt.method, rt.path, w.Code, w.Body)
		}
	}

	// The same session-only routes work for a browser login.
	login := httptest.NewRecorder()
	if err := s.sessions.Create(context.Background(), login, adminID, "192.0.2.1", "test"); err != nil {
		t.Fatal(err)
	}
	cookies := login.Result().Cookies()
	withCookie := func(r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
	}
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/sessions"},
		{http.MethodPost, "/api/v1/auth/sessions/revoke-others"},
	} {
		if w := do(rt.method, rt.path, withCookie); w.Code != http.StatusOK {
			t.Errorf("%s %s with a session: status %d (%s), want 200", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// At the per-account cap, creating a token is a 409 that says why.
func TestCreateTokenAtCapIsConflict(t *testing.T) {
	s, st, _ := routedServer(t)
	userID := mustAdmin(t, st)
	for range maxTokensPerUser {
		apiToken(t, st, userID)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"name":"one too many"}`))
	r = r.WithContext(auth.WithMethod(auth.WithUser(r.Context(), &store.User{ID: userID}), auth.MethodSession))
	w := httptest.NewRecorder()
	s.handleCreateToken(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "token limit reached") {
		t.Fatalf("status %d (%s), want 409 explaining the limit", w.Code, w.Body)
	}
}
