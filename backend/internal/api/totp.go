package api

import (
	"encoding/json"
	"net/http"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// handleTOTPSetup begins 2FA enrollment: generates a secret, stores it encrypted
// (not yet enabled) and returns the otpauth URL + secret to show once.
//
// Setup and enable both require the account password, like disable does.
// Without it, anyone holding a session (or, before these routes became
// session-only, a leaked ShareX token) could bind 2FA to their own
// authenticator, revoke every session and lock the owner out for good.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if u.TOTPEnabled {
		writeErr(w, http.StatusConflict, "2FA is already enabled")
		return
	}
	if !s.verifyAccountPassword(w, r, u, req.Password) {
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not generate secret")
		return
	}
	enc, err := auth.EncryptString(s.cfg.SessionBlockKey, secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store secret")
		return
	}
	set, err := s.store.SetTOTPSecret(r.Context(), u.ID, enc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store secret")
		return
	}
	if !set {
		writeErr(w, http.StatusConflict, "2FA is already enabled")
		return
	}
	issuer := s.settings.Current().AppName
	if issuer == "" {
		issuer = "Uploader"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":      secret,
		"otpauth_url": auth.OTPAuthURL(issuer, u.Username, secret),
	})
}

// handleTOTPEnable verifies the password and the first code against the
// pending secret and, on success, enables 2FA and returns one-time recovery
// codes (shown once).
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !s.verifyAccountPassword(w, r, u, req.Password) {
		return
	}
	// Re-fetch to get the freshly stored (pending) secret.
	fresh, err := s.store.GetUserByID(r.Context(), u.ID)
	if err != nil || fresh.TOTPSecret == nil {
		writeErr(w, http.StatusBadRequest, "start setup first")
		return
	}
	if fresh.TOTPEnabled {
		writeErr(w, http.StatusConflict, "2FA is already enabled")
		return
	}
	secret, err := auth.DecryptString(s.cfg.SessionBlockKey, *fresh.TOTPSecret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not read secret")
		return
	}
	step, valid := auth.ValidateTOTPStep(secret, req.Code)
	if !valid {
		writeErr(w, http.StatusBadRequest, "incorrect code")
		return
	}
	// Spend the code like a login does. Otherwise the code that enrolled the
	// authenticator stayed good for a login for the rest of its window, for
	// anyone who saw it typed.
	// #nosec G115 -- step is a Unix timestamp divided by 30; see handleLoginTOTP.
	claimed, err := s.store.ClaimTOTPStep(r.Context(), u.ID, int64(step))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not verify code")
		return
	}
	if !claimed {
		writeErr(w, http.StatusBadRequest, "code already used, wait for the next one")
		return
	}
	plain, hashed, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not generate recovery codes")
		return
	}
	enabled, err := s.store.EnableTOTP(r.Context(), u.ID, *fresh.TOTPSecret, hashed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not enable 2FA")
		return
	}
	if !enabled {
		writeErr(w, http.StatusConflict, "2FA setup changed in the meantime, please start again")
		return
	}
	s.audit(r, store.AuditTOTPEnable, u.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "enabled", "recovery_codes": plain})
}

// handleTOTPDisable turns 2FA off after re-verifying the password.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !s.verifyAccountPassword(w, r, u, req.Password) {
		return
	}
	if err := s.store.DisableTOTP(r.Context(), u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not disable 2FA")
		return
	}
	s.audit(r, store.AuditTOTPDisable, u.Username, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled"})
}
