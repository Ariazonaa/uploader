package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// isUniqueViolation reports whether err is a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// handleRegister creates a new (non-admin) account from a single-use invite
// code. Only active when UPLOADER_REGISTRATION=invite. All failure paths return
// a generic error so invite codes / existing usernames can't be probed.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.InviteRegistration() {
		writeErr(w, http.StatusNotFound, "registration is disabled")
		return
	}
	var req struct {
		Username   string `json:"username"`
		Email      string `json:"email"`
		Password   string `json:"password"`
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	req.InviteCode = strings.TrimSpace(req.InviteCode)

	if req.Username == "" || req.Email == "" || req.InviteCode == "" {
		writeErr(w, http.StatusBadRequest, "username, email and invite code are required")
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

	_, err = s.store.RegisterWithInvite(r.Context(), store.User{
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
	}, req.InviteCode)
	switch {
	case errors.Is(err, store.ErrInvalidInvite):
		writeErr(w, http.StatusForbidden, "invalid or already used invite code")
		return
	case isUniqueViolation(err):
		// Username or email already taken (standard registration feedback).
		writeErr(w, http.StatusConflict, "username or email already in use")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not create account")
		return
	}
	// Public endpoint: no user in context yet, record the new account's username.
	s.auditAnon(r, store.AuditUserRegister, req.Username, req.Username, nil)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "registered"})
}

// --- invite management (admin only) ---

type inviteView struct {
	Code      string     `json:"code"`
	Used      bool       `json:"used"`
	ExpiresAt *time.Time `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// maxInviteExpiryDays bounds expires_in_days; anything longer is what 0
// (never expires) is for.
const maxInviteExpiryDays = 3650

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		ExpiresInDays int `json:"expires_in_days"`
	}
	// The body is optional, but one that is there must decode: ignoring the
	// error turned "expires_in_days": 0.5 (or "7") into an invite that never
	// expires -- the opposite of what was asked for.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid request body: expires_in_days must be a whole number")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxInviteExpiryDays {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("expires_in_days must be between 0 (never) and %d", maxInviteExpiryDays))
		return
	}

	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, req.ExpiresInDays)
		expires = &t
	}

	code, err := randomInviteCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not generate code")
		return
	}
	if err := s.store.CreateInvite(r.Context(), code, u.ID, expires); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store invite")
		return
	}
	s.audit(r, store.AuditInviteCreate, code, nil)
	writeJSON(w, http.StatusCreated, map[string]string{"code": code})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := s.store.ListInvites(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list invites")
		return
	}
	views := make([]inviteView, 0, len(invites))
	for _, c := range invites {
		views = append(views, inviteView{
			Code:      c.Code,
			Used:      c.UsedAt != nil, // used_by is cleared when the redeeming account is deleted
			ExpiresAt: c.ExpiresAt,
			UsedAt:    c.UsedAt,
			CreatedAt: c.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(chi.URLParam(r, "code"))
	if code == "" {
		writeErr(w, http.StatusBadRequest, "missing code")
		return
	}
	if err := s.store.RevokeInvite(r.Context(), code); err != nil {
		writeErr(w, http.StatusNotFound, "invite not found or already used")
		return
	}
	s.audit(r, store.AuditInviteRevoke, code, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// randomInviteCode returns a high-entropy URL-safe invite code (~192 bits).
func randomInviteCode() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
