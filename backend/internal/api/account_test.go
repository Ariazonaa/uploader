package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

const testPassword = "correct horse"

// testUser returns a user whose password is testPassword (cheap bcrypt cost).
func testUser(t *testing.T) *store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return &store.User{ID: uuid.New(), Username: "Alice", PasswordHash: string(hash)}
}

// Password checks behind a session draw from the login's per-account
// buckets, so a stolen session cannot use them to guess the password without
// limit.
func TestVerifyAccountPasswordSharesTheLoginThrottle(t *testing.T) {
	s := testServer(nil)
	u := testUser(t)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	for i := range loginAccountBurst {
		w := httptest.NewRecorder()
		if s.verifyAccountPassword(w, r, u, "wrong") || w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: status %d, want 401", i, w.Code)
		}
	}
	w := httptest.NewRecorder()
	if s.verifyAccountPassword(w, r, u, testPassword) {
		t.Fatal("the correct password passed an exhausted bucket")
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d (Retry-After %q), want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	// The login form for the same, case-folded username is throttled too.
	if _, ok, _ := s.chargePasswordAttempt(r, "alice"); ok {
		t.Fatal("login bucket untouched by failed account-password checks")
	}
}

func TestVerifyAccountPasswordRefundsOnSuccess(t *testing.T) {
	s := testServer(nil)
	u := testUser(t)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	for range loginAccountBurst - 1 {
		s.verifyAccountPassword(httptest.NewRecorder(), r, u, "wrong")
	}
	if !s.verifyAccountPassword(httptest.NewRecorder(), r, u, testPassword) {
		t.Fatal("correct password refused")
	}
	for i := range loginAccountBurst {
		w := httptest.NewRecorder()
		s.verifyAccountPassword(w, r, u, "wrong")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("after a success, guess %d got %d, want a full bucket (401)", i, w.Code)
		}
	}
}

// 2FA setup and enable refuse without the account password, before the store
// is touched (the test server has none, so reaching it would panic).
func TestTOTPEnrollmentRequiresPassword(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(*Server) http.HandlerFunc
		body    string
		want    int
	}{
		{"setup without password", func(s *Server) http.HandlerFunc { return s.handleTOTPSetup }, `{}`, http.StatusUnauthorized},
		{"setup with wrong password", func(s *Server) http.HandlerFunc { return s.handleTOTPSetup }, `{"password":"x"}`, http.StatusUnauthorized},
		{"setup without body", func(s *Server) http.HandlerFunc { return s.handleTOTPSetup }, ``, http.StatusBadRequest},
		{"enable without password", func(s *Server) http.HandlerFunc { return s.handleTOTPEnable }, `{"code":"123456"}`, http.StatusUnauthorized},
		{"enable with wrong password", func(s *Server) http.HandlerFunc { return s.handleTOTPEnable }, `{"code":"123456","password":"x"}`, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(nil)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp", strings.NewReader(tc.body))
			r = r.WithContext(auth.WithUser(r.Context(), testUser(t)))
			w := httptest.NewRecorder()
			tc.handler(s)(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, tc.want, w.Body)
			}
		})
	}
}
