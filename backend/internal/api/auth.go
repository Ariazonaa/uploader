package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

type userView struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	Email       string    `json:"email"`
	IsAdmin     bool      `json:"is_admin"`
	TOTPEnabled bool      `json:"totp_enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

func toUserView(u *store.User) userView {
	return userView{
		ID: u.ID, Username: u.Username, Email: u.Email,
		IsAdmin: u.IsAdmin, TOTPEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt,
	}
}

// handleSetup is the first-run bootstrap: it creates the admin account through
// the web UI when no users exist yet, then logs them in. It is a no-op (409)
// once any account exists, and the underlying insert is race-safe.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if n, err := s.store.CountUsers(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not check setup state")
		return
	} else if n > 0 {
		writeErr(w, http.StatusConflict, "setup already completed")
		return
	}

	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	if req.Username == "" || req.Email == "" {
		writeErr(w, http.StatusBadRequest, "username and email are required")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u := store.User{Username: req.Username, Email: req.Email, PasswordHash: hash, IsAdmin: true}
	created, err := s.store.CreateFirstAdmin(r.Context(), u)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "setup already completed")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not create account")
		return
	}
	if !created {
		writeErr(w, http.StatusConflict, "setup already completed")
		return
	}
	// Fetch the freshly created admin (CreateFirstAdmin doesn't return the row).
	admin, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "account created; please log in")
		return
	}
	if err := s.sessions.Create(r.Context(), w, admin.ID, s.clientIP(r), clientUA(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "account created; please log in")
		return
	}
	writeJSON(w, http.StatusCreated, toUserView(&admin))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Setup, registration and the CLI store the name trimmed; a stray space
	// from autofill or copy-paste must not turn into "invalid credentials".
	req.Username = strings.TrimSpace(req.Username)

	// Per-account failure throttle, keyed by the attempted username (not the
	// user id) so that existing and unknown usernames behave identically and
	// the throttle leaks nothing. The attempt is charged up front -- so
	// parallel guesses cannot all slip past a check -- and refunded when the
	// password turns out to be right.
	attempt, ok, retry := s.chargePasswordAttempt(r, req.Username)
	if !ok {
		writeRateLimited(w, retry, "too many failed login attempts for this account, try again later")
		return
	}

	u, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if errors.Is(err, store.ErrNotFound) {
		// Spend equivalent bcrypt work on the user-not-found path so login
		// latency does not reveal whether the username exists.
		auth.DummyVerify(req.Password)
		label := s.unknownUserLabel(req.Username)
		s.auditAnon(r, store.AuditLoginFailure, label, label, nil)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		// A failed lookup says nothing about the password. Reporting it as
		// wrong credentials sent users hunting for a typo while every retry
		// during a database outage drained their throttle.
		attempt.void()
		log.Printf("auth: login lookup: %v", err)
		writeErr(w, http.StatusServiceUnavailable, "login is temporarily unavailable, try again later")
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		s.auditAnon(r, store.AuditLoginFailure, req.Username, req.Username, nil)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	attempt.succeeded()

	// Second factor: don't create a session yet — issue a short-lived pending
	// token the client exchanges (with a code) at /auth/login/totp.
	if u.TOTPEnabled {
		pending, err := s.sessions.IssuePending(u.ID, u.PasswordHash)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not start 2FA")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"totp_required": true, "pending": pending})
		return
	}

	if err := s.sessions.Create(r.Context(), w, u.ID, s.clientIP(r), clientUA(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}
	// The user is not yet in the request context (this handler runs before the
	// auth middleware), so attribute the event via the known local user.
	s.recordAudit(r, &u.ID, u.Username, store.AuditLoginSuccess, "", nil)
	writeJSON(w, http.StatusOK, toUserView(&u))
}

// handleLoginTOTP completes a 2FA login: exchanges the pending token + a TOTP or
// recovery code for a real session.
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pending string `json:"pending"`
		Code    string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pending, err := s.sessions.VerifyPending(req.Pending)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "2FA session expired, please log in again")
		return
	}
	// The pending token is stateless and valid for five minutes, so bound what
	// one token can do: a few code attempts, and single use once it succeeds.
	lims := s.limiters()
	tokenKey := pending.Key
	if !lims.pendingToken.allow(tokenKey) {
		writeErr(w, http.StatusUnauthorized, "too many attempts, please log in again")
		return
	}
	// Per-account second-factor throttle, across all pending tokens and IPs:
	// logging in again for a fresh token must not reset the budget. Charged up
	// front like the password throttle, refunded on success.
	totpKey := "totp:" + pending.UserID.String()
	if ok, retry := lims.totpAccount.take(totpKey); !ok {
		writeRateLimited(w, retry, "too many failed 2FA attempts, try again later")
		return
	}
	u, err := s.store.GetUserByID(r.Context(), pending.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		lims.pendingToken.refund(tokenKey)
		lims.totpAccount.refund(totpKey)
		log.Printf("auth: 2FA login lookup: %v", err)
		writeErr(w, http.StatusServiceUnavailable, "login is temporarily unavailable, try again later")
		return
	}
	if err != nil || !u.TOTPEnabled || u.TOTPSecret == nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// The token vouches for the password as it was at issue time. After a
	// password change it must not finish the login it started.
	if !pending.MatchesPassword(u.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "2FA session expired, please log in again")
		return
	}

	ok := false
	// The secret is encrypted with the session block key. Once it no longer
	// decrypts -- typically after UPLOADER_SESSION_BLOCK_KEY was rotated --
	// TOTP codes cannot be checked, but that must not lock the account out:
	// the recovery codes below are bcrypt hashes, independent of the key.
	// Answering 500 here, before they were tried, locked out every 2FA user,
	// the admin included.
	if secret, err := auth.DecryptString(s.cfg.SessionBlockKey, *u.TOTPSecret); err != nil {
		log.Printf("auth: TOTP secret of user %s does not decrypt (was UPLOADER_SESSION_BLOCK_KEY rotated?): %v; "+
			"only a recovery code can complete this login", u.ID, err)
	} else if step, valid := auth.ValidateTOTPStep(secret, req.Code); valid {
		// Codes are single-use: claiming the time step fails if it has already been
		// spent, so a code observed in transit (or over a shoulder) cannot be
		// replayed for the remainder of its ±1-step, up to 90 second, window.
		// #nosec G115 -- step is a Unix timestamp divided by 30; it is far below
		// math.MaxInt64 and will stay so for the rest of the epoch.
		claimed, err := s.store.ClaimTOTPStep(r.Context(), u.ID, int64(step))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not verify code")
			return
		}
		ok = claimed
	}
	if !ok {
		// Fall back to a one-time recovery code. The removal is a compare-and-swap
		// against the list we checked: if a parallel request redeemed the same code
		// (or any other) first, the swap fails and this attempt is rejected, so a
		// code cannot log in twice. A write error fails closed.
		if remaining, used := auth.ConsumeRecoveryCode(u.TOTPRecovery, req.Code); used {
			swapped, err := s.store.ReplaceTOTPRecovery(r.Context(), u.ID, u.TOTPRecovery, remaining)
			if err != nil {
				log.Printf("auth: consume recovery code for user %s: %v", u.ID, err)
				writeErr(w, http.StatusInternalServerError, "could not verify code")
				return
			}
			ok = swapped
		}
	}
	if !ok {
		s.recordAudit(r, &u.ID, u.Username, store.AuditLoginFailure, u.Username, nil)
		writeErr(w, http.StatusUnauthorized, "invalid 2FA code")
		return
	}
	lims.pendingToken.exhaust(tokenKey)
	lims.totpAccount.reset(totpKey)

	if err := s.sessions.Create(r.Context(), w, u.ID, s.clientIP(r), clientUA(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create session")
		return
	}
	s.recordAudit(r, &u.ID, u.Username, store.AuditLoginSuccess, "", nil)
	writeJSON(w, http.StatusOK, toUserView(&u))
}

// loginThrottleKey is the per-account failure-throttle key for a login attempt:
// the attempted username, case-folded like the lookup, bounded in length.
func loginThrottleKey(username string) string {
	return "login:" + truncate(strings.ToLower(strings.TrimSpace(username)), maxAuditFieldLen)
}

// unknownUserLabel stands in for a login name that matched no account in the
// audit log. What lands in the username field is often a password typed one
// field too early, and the audit log is readable by every admin and kept for
// a long time, so the name itself is never stored. A keyed hash of it still
// lets repeated attempts with the same name be told apart and correlated.
func (s *Server) unknownUserLabel(username string) string {
	mac := hmac.New(sha256.New, s.cfg.SessionHashKey)
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(username))))
	return "unknown:" + hex.EncodeToString(mac.Sum(nil))[:12]
}

// loginSourceKey narrows loginThrottleKey to one source network.
func loginSourceKey(username, ip string) string {
	return loginThrottleKey(username) + "|" + rateKey(ip)
}

// passwordAttempt is one password check's claim on the per-account throttles.
type passwordAttempt struct {
	lims            *serverLimiters
	source, account string
}

// chargePasswordAttempt takes one attempt for username from the caller's
// per-source bucket and from the account-wide one. Charging the account alone
// let anyone who knew a username keep it locked out from a single address;
// now that source only exhausts its own bucket, and the larger account-wide
// budget still bounds guessing spread over many addresses.
func (s *Server) chargePasswordAttempt(r *http.Request, username string) (passwordAttempt, bool, time.Duration) {
	a := passwordAttempt{
		lims:    s.limiters(),
		source:  loginSourceKey(username, s.clientIP(r)),
		account: loginThrottleKey(username),
	}
	if ok, retry := a.lims.loginAccount.take(a.source); !ok {
		return a, false, retry
	}
	if ok, retry := a.lims.loginAccountAll.take(a.account); !ok {
		a.lims.loginAccount.refund(a.source)
		return a, false, retry
	}
	return a, true, 0
}

// succeeded records that the password was right. The source starts over, but
// the account-wide bucket only gets this attempt back: resetting it would hand
// a guesser spread over many addresses a fresh budget whenever the owner
// signs in.
func (a passwordAttempt) succeeded() {
	a.lims.loginAccount.reset(a.source)
	a.lims.loginAccountAll.refund(a.account)
}

// void takes back the charge for an attempt that could not be evaluated.
func (a passwordAttempt) void() {
	a.lims.loginAccount.refund(a.source)
	a.lims.loginAccountAll.refund(a.account)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Attribute before Destroy runs; the actor comes from the request context
	// (injected by auth.Require), not from the session being torn down.
	s.audit(r, store.AuditLogout, "", nil)
	s.sessions.Destroy(r.Context(), w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	writeJSON(w, http.StatusOK, toUserView(u))
}
