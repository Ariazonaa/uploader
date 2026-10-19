//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP is defined over HMAC-SHA1
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/dbtest"
	"uploader/internal/db/store"
	"uploader/internal/settings"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func integrationServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	pool, _ := dbtest.Open(t)
	st := store.New(pool)
	return &Server{cfg: &config.Config{RegistrationMode: "invite"}, store: st}, st
}

// recordAudit must get client-controlled text containing NUL bytes and
// invalid UTF-8 into the audit table (cleaned), not lose the event.
func TestRecordAuditStoresDirtyInput(t *testing.T) {
	s, st := integrationServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.Header.Set("User-Agent", "evil\x00agent \xff\xfe"+strings.Repeat("u", 2000))
	s.recordAudit(req, nil, "adm\x00in\xc3", store.AuditLoginFailure, "tar\xffget",
		map[string]any{"name\x00": "tok\x00en\xff", "list": []string{"a\x00"}, "n": 3})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, total, err := st.ListAuditEvents(ctx, store.AuditFilter{Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("audit rows = %d, %v; want the event recorded", total, err)
	}
	e := events[0]
	for field, v := range map[string]string{"actor": e.ActorLabel, "target": e.Target, "ua": e.UserAgent, "meta": string(e.Metadata)} {
		if strings.Contains(v, "\x00") || strings.Contains(v, `\u0000`) || !utf8.ValidString(v) {
			t.Errorf("%s stored uncleaned: %q", field, v)
		}
	}
	if e.ActorLabel != "admin�" || e.Target != "tar�get" {
		t.Errorf("actor/target = %q/%q", e.ActorLabel, e.Target)
	}
	if n := utf8.RuneCountInString(e.UserAgent); n != maxUserAgentLen {
		t.Errorf("user agent is %d runes, want truncation to %d", n, maxUserAgentLen)
	}
	var md map[string]any
	if err := json.Unmarshal(e.Metadata, &md); err != nil || md["name"] != "token�" || md["n"] != float64(3) {
		t.Errorf("metadata = %s, %v", e.Metadata, err)
	}
}

// The register handler maps the store's outcomes onto the documented statuses,
// and a duplicate name leaves the invite usable.
func TestHandleRegisterAgainstDatabase(t *testing.T) {
	s, st := integrationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.CreateUser(ctx, store.User{Username: "taken", Email: "taken@example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, "inv", mustAdmin(t, st), nil); err != nil {
		t.Fatal(err)
	}
	register := func(user, code string) int {
		t.Helper()
		body := `{"username":"` + user + `","email":"` + user + `@example.org","password":"longenough","invite_code":"` + code + `"}`
		rec := httptest.NewRecorder()
		s.handleRegister(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body)))
		return rec.Code
	}
	if c := register("newbie", "wrong"); c != http.StatusForbidden {
		t.Fatalf("unknown code: status %d, want 403", c)
	}
	if c := register("Taken", "inv"); c != http.StatusConflict {
		t.Fatalf("duplicate username: status %d, want 409", c)
	}
	if c := register("newbie", "inv"); c != http.StatusCreated {
		t.Fatalf("valid registration: status %d, want 201", c)
	}
	if c := register("again", "inv"); c != http.StatusForbidden {
		t.Fatalf("reused code: status %d, want 403", c)
	}
	if _, total, _ := st.ListAuditEvents(ctx, store.AuditFilter{Event: store.AuditUserRegister, Limit: 10}); total != 1 {
		t.Fatalf("%d user.register audit events, want 1", total)
	}
}

// A TOTP secret that no longer decrypts (the session block key was rotated)
// must not lock the account out: recovery codes do not depend on that key.
func TestLoginTOTPAcceptsRecoveryCodeWhenSecretDoesNotDecrypt(t *testing.T) {
	s, st := integrationServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	s.cfg.SessionBlockKey = newKey
	s.sessions = auth.NewSessionManager(st, bytes.Repeat([]byte{3}, 32), newKey, time.Hour, false)

	u := store.User{ID: uuid.New(), Username: "twofa", Email: "twofa@example.com", PasswordHash: "x"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := auth.EncryptString(oldKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	plain, hashed, err := auth.GenerateRecoveryCodes(2)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetTOTPSecret(ctx, u.ID, enc); err != nil || !ok {
		t.Fatalf("set secret: %v, %v", ok, err)
	}
	if ok, err := st.EnableTOTP(ctx, u.ID, enc, hashed); err != nil || !ok {
		t.Fatalf("enable: %v, %v", ok, err)
	}

	login := func(code string) *httptest.ResponseRecorder {
		t.Helper()
		pending, err := s.sessions.IssuePending(u.ID, u.PasswordHash)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"pending":"` + pending + `","code":"` + code + `"}`
		rec := httptest.NewRecorder()
		s.handleLoginTOTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", strings.NewReader(body)))
		return rec
	}
	if rec := login("123456"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("TOTP code with an undecryptable secret: status %d, want 401", rec.Code)
	}
	rec := login(plain[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery code: status %d (%s), want 200", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("recovery login set no session cookie")
	}
	if rec := login(plain[0]); rec.Code != http.StatusUnauthorized {
		t.Fatalf("spent recovery code: status %d, want 401", rec.Code)
	}
}

// loginServer is integrationServer with a session manager, plus one account
// ("alice") whose password is "longenough".
func loginServer(t *testing.T) (*Server, *store.Store, store.User) {
	t.Helper()
	s, st := integrationServer(t)
	s.settings = settings.NewStatic(settings.Snapshot{})
	s.cfg.SessionHashKey = bytes.Repeat([]byte{5}, 32)
	s.cfg.SessionBlockKey = bytes.Repeat([]byte{6}, 32)
	s.sessions = auth.NewSessionManager(st, s.cfg.SessionHashKey, s.cfg.SessionBlockKey, time.Hour, false)
	hash, err := auth.HashPassword("longenough")
	if err != nil {
		t.Fatal(err)
	}
	u := store.User{ID: uuid.New(), Username: "alice", Email: "alice@example.com", PasswordHash: hash}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return s, st, u
}

func postLogin(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body)))
	return rec
}

// Setup, registration and the CLI store usernames trimmed, so login trims too.
func TestLoginTrimsUsername(t *testing.T) {
	s, _, _ := loginServer(t)
	if rec := postLogin(s, `{"username":"  Alice\t","password":"longenough"}`); rec.Code != http.StatusOK {
		t.Fatalf("padded username: status %d (%s), want 200", rec.Code, rec.Body)
	}
}

// A failed login with an unknown name must not store what was typed (often a
// password); one with a known name keeps the name.
func TestLoginFailureAuditLabels(t *testing.T) {
	s, st, _ := loginServer(t)
	if rec := postLogin(s, `{"username":"MyS3cretPass","password":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: status %d, want 401", rec.Code)
	}
	if rec := postLogin(s, `{"username":"alice","password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401", rec.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, total, err := st.ListAuditEvents(ctx, store.AuditFilter{Event: store.AuditLoginFailure, Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("login.failure events = %d, %v; want 2", total, err)
	}
	labels := map[string]bool{}
	for _, e := range events {
		if strings.Contains(strings.ToLower(e.ActorLabel+e.Target), "s3cret") {
			t.Fatalf("typed name stored: actor %q, target %q", e.ActorLabel, e.Target)
		}
		labels[e.ActorLabel] = true
	}
	if want := s.unknownUserLabel("MyS3cretPass"); !labels[want] || !labels["alice"] {
		t.Fatalf("actor labels = %v, want %q and alice", labels, want)
	}
}

// The invite body stays optional; a whole number of days sets the expiry.
func TestCreateInviteExpiry(t *testing.T) {
	s, st := integrationServer(t)
	admin, err := st.GetUserByID(context.Background(), mustAdmin(t, st))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{}`, `{"expires_in_days":0}`, `{"expires_in_days":7}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/invites", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), &admin))
		rec := httptest.NewRecorder()
		s.handleCreateInvite(rec, r)
		if rec.Code != http.StatusCreated {
			t.Fatalf("body %q: status %d (%s), want 201", body, rec.Code, rec.Body)
		}
	}
	invites, err := st.ListInvites(context.Background())
	if err != nil || len(invites) != 4 {
		t.Fatalf("invites = %d, %v; want 4", len(invites), err)
	}
	expiring := 0
	for _, inv := range invites {
		if inv.ExpiresAt != nil {
			expiring++
			if d := time.Until(*inv.ExpiresAt); d < 6*24*time.Hour || d > 8*24*time.Hour {
				t.Errorf("expiry in %v, want about 7 days", d)
			}
		}
	}
	if expiring != 1 {
		t.Fatalf("%d invites expire, want 1", expiring)
	}
}

// The first-run setup refuses a password bcrypt cannot hash with a 400, and
// creates no account.
func TestSetupRejectsOverlongPassword(t *testing.T) {
	s, st := integrationServer(t)
	body := `{"username":"root","email":"root@example.com","password":"` + strings.Repeat("x", 73) + `"}`
	rec := httptest.NewRecorder()
	s.handleSetup(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d (%s), want 400", rec.Code, rec.Body)
	}
	if n, err := st.CountUsers(context.Background()); err != nil || n != 0 {
		t.Fatalf("users = %d, %v; want none", n, err)
	}
}

// enableTOTP turns 2FA on for u and returns its recovery codes.
func enableTOTP(t *testing.T, s *Server, st *store.Store, u store.User, codes int) []string {
	t.Helper()
	ctx := context.Background()
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := auth.EncryptString(s.cfg.SessionBlockKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	plain, hashed, err := auth.GenerateRecoveryCodes(codes)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetTOTPSecret(ctx, u.ID, enc); err != nil || !ok {
		t.Fatalf("set secret: %v, %v", ok, err)
	}
	if ok, err := st.EnableTOTP(ctx, u.ID, enc, hashed); err != nil || !ok {
		t.Fatalf("enable: %v, %v", ok, err)
	}
	return plain
}

// pendingFromLogin logs alice in with password and returns the 2FA token.
func pendingFromLogin(t *testing.T, s *Server, password string) string {
	t.Helper()
	rec := postLogin(s, `{"username":"alice","password":"`+password+`"}`)
	var res struct {
		Pending string `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Pending == "" {
		t.Fatalf("login: status %d (%s), want a 2FA challenge", rec.Code, rec.Body)
	}
	return res.Pending
}

func postLoginTOTP(s *Server, pending, code string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := `{"pending":"` + pending + `","code":"` + code + `"}`
	s.handleLoginTOTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/totp", strings.NewReader(body)))
	return rec
}

// A pending token earned with the old password must not finish the login
// once the password has been changed.
func TestPendingTokenDiesWithPasswordChange(t *testing.T) {
	s, st, u := loginServer(t)
	codes := enableTOTP(t, s, st, u, 2)
	stale := pendingFromLogin(t, s, "longenough")

	hash, err := auth.HashPassword("evenlonger")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePassword(context.Background(), u.ID, hash); err != nil {
		t.Fatal(err)
	}
	if rec := postLoginTOTP(s, stale, codes[0]); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token from before the password change: status %d (%s), want 401", rec.Code, rec.Body)
	}
	// The code was not spent on the refused attempt.
	if rec := postLoginTOTP(s, pendingFromLogin(t, s, "evenlonger"), codes[0]); rec.Code != http.StatusOK {
		t.Fatalf("fresh token: status %d (%s), want 200", rec.Code, rec.Body)
	}
}

// totpNow computes the current RFC 6238 code for a base32 secret, as an
// authenticator app would.
func totpNow(t *testing.T, secret string) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(time.Now().Unix()/30)) // #nosec G115 -- positive Unix time
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// postAs calls an authenticated handler as u with a JSON body.
func postAs(u store.User, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/totp", strings.NewReader(body))
	r = r.WithContext(auth.WithUser(r.Context(), &u))
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

// The code that enrolled the authenticator is spent: someone who saw it
// typed cannot reuse it for a login within its validity window.
func TestTOTPEnrollmentCodeCannotLogIn(t *testing.T) {
	s, _, u := loginServer(t)
	rec := postAs(u, s.handleTOTPSetup, `{"password":"longenough"}`)
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil || setup.Secret == "" {
		t.Fatalf("setup: status %d (%s)", rec.Code, rec.Body)
	}
	code := totpNow(t, setup.Secret)
	if rec := postAs(u, s.handleTOTPEnable, `{"password":"longenough","code":"`+code+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: status %d (%s), want 200", rec.Code, rec.Body)
	}
	if rec := postLoginTOTP(s, pendingFromLogin(t, s, "longenough"), code); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with the enrolment code: status %d (%s), want 401", rec.Code, rec.Body)
	}
}

func mustAdmin(t *testing.T, st *store.Store) uuid.UUID {
	t.Helper()
	u := store.User{ID: uuid.New(), Username: "root", Email: "root@example.com", PasswordHash: "x", IsAdmin: true}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}
