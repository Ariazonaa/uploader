package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"uploader/internal/db/store"
)

// testAuthenticator has no store: only requests that fail before any lookup
// (no credential at all) may be sent through it.
func testAuthenticator() *Authenticator {
	key := bytes.Repeat([]byte{7}, 32)
	return NewAuthenticator(NewSessionManager(nil, key, key, time.Hour, false), nil)
}

// assertJSONError checks the response is a non-cacheable JSON API error.
func assertJSONError(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d", w.Code, status)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("body %q is not a JSON error", w.Body)
	}
}

// A credential that cannot be checked because the database is unreachable is
// a 503, not a 401 that would sign the SPA out.
func TestRequireReportsLookupFailureAs503(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key := bytes.Repeat([]byte{7}, 32)
	sessions := NewSessionManager(store.New(pool), key, key, time.Hour, false)
	a := NewAuthenticator(sessions, store.New(pool))
	h := a.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached although the credential could not be checked")
	}))

	cookie, err := sessions.sc.Encode(CookieName, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for name, set := range map[string]func(*http.Request){
		"session": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie}) },
		"token":   func(r *http.Request) { r.Header.Set("Authorization", "Bearer upl_0123456789abcdef") },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil).WithContext(ctx)
			set(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			assertJSONError(t, w, http.StatusServiceUnavailable)
		})
	}
}

func TestRequireRejectsAnonymousWithJSON(t *testing.T) {
	h := testAuthenticator().Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler reached without credentials")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	assertJSONError(t, w, http.StatusUnauthorized)
}
