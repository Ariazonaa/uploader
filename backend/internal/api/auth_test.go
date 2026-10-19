package api

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
)

func TestKeyedRateLimiterRefund(t *testing.T) {
	k := newKeyedRateLimiter(rate.Limit(0), 2)
	k.allow("a")
	k.allow("a")
	k.refund("a")
	if !k.allow("a") {
		t.Fatal("refunded token not available")
	}
	if k.allow("a") {
		t.Fatal("refund granted more than one token")
	}
	// A refund never lifts a bucket above its burst.
	k.allow("b")
	k.refund("b")
	k.refund("b")
	k.allow("b")
	k.allow("b")
	if k.allow("b") {
		t.Fatal("refunds overfilled the bucket")
	}
	// Refunding an unknown key is a no-op, not a new entry.
	k.refund("c")
	if _, ok := k.entries["c"]; ok {
		t.Fatal("refund created an entry")
	}
}

func TestUnknownUserLabel(t *testing.T) {
	s := testServer(&config.Config{SessionHashKey: bytes.Repeat([]byte{7}, 32)})
	typed := "Hunter2-Secret"
	label := s.unknownUserLabel(typed)
	if !regexp.MustCompile(`^unknown:[0-9a-f]{12}$`).MatchString(label) {
		t.Fatalf("label = %q, want unknown: plus 12 hex digits", label)
	}
	if strings.Contains(strings.ToLower(label), "hunter") {
		t.Fatalf("label %q leaks the typed name", label)
	}
	// The same name, however it was cased or padded, correlates.
	if got := s.unknownUserLabel("  hunter2-secret\t"); got != label {
		t.Fatalf("variant of the same name: %q, want %q", got, label)
	}
	if s.unknownUserLabel("someone-else") == label {
		t.Fatal("different names share a label")
	}
	// Keyed: without the server secret the label cannot be checked against a
	// guessed password list.
	other := testServer(&config.Config{SessionHashKey: bytes.Repeat([]byte{8}, 32)})
	if other.unknownUserLabel(typed) == label {
		t.Fatal("label does not depend on the server key")
	}
}

// Passwords past bcrypt's 72 bytes are refused as a 400 before anything is
// hashed or stored (the store is nil); they used to end in a 500.
func TestNewPasswordsAreLengthChecked(t *testing.T) {
	long := strings.Repeat("ä", 37) // 74 bytes
	for _, tc := range []struct {
		name    string
		handler func(*Server) http.HandlerFunc
		body    string
	}{
		{"register", func(s *Server) http.HandlerFunc { return s.handleRegister },
			`{"username":"bob","email":"bob@example.com","invite_code":"x","password":"` + long + `"}`},
		{"register, short", func(s *Server) http.HandlerFunc { return s.handleRegister },
			`{"username":"bob","email":"bob@example.com","invite_code":"x","password":"short"}`},
		{"password change", func(s *Server) http.HandlerFunc { return s.handleChangePassword },
			`{"current_password":"` + testPassword + `","new_password":"` + long + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(&config.Config{RegistrationMode: "invite"})
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r = r.WithContext(auth.WithUser(r.Context(), testUser(t)))
			w := httptest.NewRecorder()
			tc.handler(s)(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "password must be") {
				t.Fatalf("status %d (%s), want 400 naming the password limit", w.Code, w.Body)
			}
		})
	}
}

// An expiry that is not a whole number of days in range is refused before
// anything is stored (the store is nil); 0.5 used to create an invite that
// never expires.
func TestCreateInviteRejectsInvalidExpiry(t *testing.T) {
	for _, body := range []string{
		`{"expires_in_days":0.5}`,
		`{"expires_in_days":"7"}`,
		`{"expires_in_days":-1}`,
		`{"expires_in_days":3651}`,
		`{"expires_in_days":1e3}`,
		`not json`,
	} {
		s := testServer(nil)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/invites", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), testUser(t)))
		w := httptest.NewRecorder()
		s.handleCreateInvite(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, w.Code)
		}
	}
}

// unreachableStore returns a store whose every query fails (its pool is
// closed), standing in for a database outage.
func unreachableStore(t *testing.T) *store.Store {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	return store.New(pool)
}

// A failing user lookup is not a wrong password: it answers 503 and costs the
// account's throttles nothing, however often it is retried.
func TestLoginLookupErrorIsNotAFailedAttempt(t *testing.T) {
	s := testServer(nil)
	s.store = unreachableStore(t)
	for i := range loginAccountBurst + 1 {
		w := httptest.NewRecorder()
		s.handleLogin(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(`{"username":"alice","password":"longenough"}`)))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status %d (%s), want 503", i, w.Code, w.Body)
		}
	}
	if _, ok, _ := s.chargePasswordAttempt(httptest.NewRequest(http.MethodPost, "/", nil), "alice"); !ok {
		t.Fatal("lookup errors were charged to the throttle")
	}
}

func TestLoginTOTPLookupErrorIsNotAFailedAttempt(t *testing.T) {
	s := testServer(nil)
	st := unreachableStore(t)
	s.store = st
	s.sessions = auth.NewSessionManager(st, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), time.Hour, false)
	userID := uuid.New()
	pending, err := s.sessions.IssuePending(userID, "x")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"pending":"` + pending + `","code":"123456"}`
	for i := range max(pendingTokenAttempts, totpAccountBurst) + 1 {
		w := httptest.NewRecorder()
		s.handleLoginTOTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", strings.NewReader(body)))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: status %d (%s), want 503", i, w.Code, w.Body)
		}
	}
}

// A spent pending token stays spent however it is re-encoded: base64 skips
// line breaks, so keying the counter by the transport string handed every
// spelling five fresh attempts. The store is nil, so reaching it would panic.
func TestLoginTOTPCountsAttemptsPerDecodedToken(t *testing.T) {
	s := testServer(nil)
	s.sessions = auth.NewSessionManager(nil, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), time.Hour, false)
	tok, err := s.sessions.IssuePending(uuid.New(), "x")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.sessions.VerifyPending(tok)
	if err != nil {
		t.Fatal(err)
	}
	s.limiters().pendingToken.exhaust(p.Key)
	body := `{"pending":"` + tok[:8] + `\r\n` + tok[8:] + `","code":"123456"}`
	w := httptest.NewRecorder()
	s.handleLoginTOTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", strings.NewReader(body)))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "too many attempts") {
		t.Fatalf("re-encoded spent token: status %d (%s), want 401 too many attempts", w.Code, w.Body)
	}
}

// checkPassword runs one account-password check from ip and returns the
// status: 200 when the password was verified (nothing was written).
func checkPassword(t *testing.T, s *Server, ip, password string) int {
	t.Helper()
	w := httptest.NewRecorder()
	s.verifyAccountPassword(w, requestFrom(net.JoinHostPort(ip, "1"), nil), testUser(t), password)
	return w.Code
}

// One source exhausting its bucket must not lock the account out for others:
// keyed by the username alone, anyone could keep a known account locked.
func TestPasswordThrottleIsPerSource(t *testing.T) {
	s := testServer(nil)
	for range loginAccountBurst {
		checkPassword(t, s, "203.0.113.1", "wrong")
	}
	if c := checkPassword(t, s, "203.0.113.1", testPassword); c != http.StatusTooManyRequests {
		t.Fatalf("exhausted source: status %d, want 429", c)
	}
	if c := checkPassword(t, s, "2001:db8::1", "wrong"); c != http.StatusUnauthorized {
		t.Fatalf("other source, wrong password: status %d, want 401", c)
	}
	if c := checkPassword(t, s, "198.51.100.7", testPassword); c != http.StatusOK {
		t.Fatalf("other source, right password: status %d, want it verified", c)
	}
}

// Guesses spread over many sources still meet the account-wide budget, and the
// owner signing in gives back only their own attempt.
func TestPasswordThrottleCapsAccountAcrossSources(t *testing.T) {
	s := testServer(nil)
	n := 0
	source := func() string { n++; return fmt.Sprintf("203.0.113.%d", n) }
	ip := ""
	for i := range loginAccountAllBurst - 1 {
		if i%loginAccountBurst == 0 {
			ip = source()
		}
		if c := checkPassword(t, s, ip, "wrong"); c != http.StatusUnauthorized {
			t.Fatalf("guess %d: status %d, want 401", i, c)
		}
	}
	if c := checkPassword(t, s, source(), testPassword); c != http.StatusOK {
		t.Fatalf("owner with one attempt left: status %d, want it verified", c)
	}
	if c := checkPassword(t, s, source(), "wrong"); c != http.StatusUnauthorized {
		t.Fatalf("last attempt of the budget: status %d, want 401", c)
	}
	fresh := source()
	if c := checkPassword(t, s, fresh, testPassword); c != http.StatusTooManyRequests {
		t.Fatalf("fresh source past the account budget: status %d, want 429", c)
	}
	// The refused attempt did not cost the fresh source its own bucket.
	el := s.limiters().loginAccount.entries[loginSourceKey(testUser(t).Username, fresh)]
	if got := el.Value.(*rateEntry).lim.Tokens(); got < loginAccountBurst-0.01 {
		t.Fatalf("fresh source holds %.2f tokens after a refused attempt, want %d", got, loginAccountBurst)
	}
}
