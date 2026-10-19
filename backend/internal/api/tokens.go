package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// maxTokensPerUser caps the API tokens one account can hold. GET /tokens
// returns them all, so the cap also bounds that list.
const maxTokensPerUser = 50

type tokenView struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toTokenView(t store.APIToken) tokenView {
	return tokenView{ID: t.ID, Name: t.Name, Prefix: t.TokenPrefix, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	tokens, err := s.store.ListTokens(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	views := make([]tokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, toTokenView(t))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		req.Name = "token"
	}

	gen, err := auth.GenerateToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not generate token")
		return
	}
	if err := s.store.CreateToken(r.Context(), store.APIToken{
		UserID:      u.ID,
		Name:        req.Name,
		TokenPrefix: gen.Prefix,
		TokenHash:   gen.Hash,
	}, maxTokensPerUser); err != nil {
		if errors.Is(err, store.ErrTokenLimit) {
			writeErr(w, http.StatusConflict, fmt.Sprintf(
				"token limit reached: an account can hold at most %d API tokens, revoke unused ones first",
				maxTokensPerUser))
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not store token")
		return
	}
	// Record only the non-secret prefix + name — never the plaintext token.
	s.audit(r, store.AuditTokenCreate, gen.Prefix, map[string]any{"name": req.Name})
	// The plaintext token is returned exactly once.
	writeJSON(w, http.StatusCreated, map[string]string{
		"name":  req.Name,
		"token": gen.Plaintext,
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid token id")
		return
	}
	if err := s.store.DeleteToken(r.Context(), id, u.ID); err != nil {
		log.Printf("tokens: delete %s for user %s failed: %v", id, u.ID, err)
		writeErr(w, http.StatusNotFound, "token not found")
		return
	}
	s.audit(r, store.AuditTokenRevoke, id.String(), nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
