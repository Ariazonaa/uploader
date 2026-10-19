package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// Authenticator resolves the user behind a request from either a session cookie
// or an API token, and provides HTTP middleware.
type Authenticator struct {
	sessions *SessionManager
	store    *store.Store
}

// NewAuthenticator wires the authenticator.
func NewAuthenticator(sessions *SessionManager, st *store.Store) *Authenticator {
	return &Authenticator{sessions: sessions, store: st}
}

// Identify attempts to authenticate the request. It returns the user and how
// they authenticated, ErrNoSession when the request carries no valid
// credential, or the lookup error when the credential could not be checked
// (database unavailable). It does not write a response.
func (a *Authenticator) Identify(r *http.Request) (*store.User, Method, error) {
	if tok := extractToken(r); tok != "" {
		// No fallback to the cookie: an invalid token must not silently succeed
		// as whatever session the client also happens to carry.
		u, err := a.userFromToken(r.Context(), tok)
		return u, MethodToken, err
	}
	u, err := a.userFromSession(r)
	return u, MethodSession, err
}

func (a *Authenticator) userFromSession(r *http.Request) (*store.User, error) {
	sess, err := a.sessions.Resolve(r.Context(), r)
	if err != nil {
		return nil, err
	}
	return a.user(r.Context(), sess.UserID)
}

func (a *Authenticator) userFromToken(ctx context.Context, token string) (*store.User, error) {
	prefix := TokenPrefix(token)
	candidates, err := a.store.ListTokensByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if VerifyToken(c.TokenHash, token) {
			if IsLegacyTokenHash(c.TokenHash) {
				// Re-store a bcrypt-era token as SHA-256 on first use, so its prefix
				// stops being a lever for expensive comparisons. Best-effort: on
				// failure the next use tries again.
				_ = a.store.UpgradeTokenHash(ctx, c.ID, c.TokenHash, HashToken(token))
			}
			_ = a.store.TouchToken(ctx, c.ID)
			return a.user(ctx, c.UserID)
		}
	}
	return nil, ErrNoSession
}

// user loads the account behind a valid credential. An account that no longer
// exists is a missing credential, not an outage.
func (a *Authenticator) user(ctx context.Context, id uuid.UUID) (*store.User, error) {
	u, err := a.store.GetUserByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Require is middleware that rejects unauthenticated requests with 401 and
// injects the user and the auth method into the context otherwise. A
// credential that could not be checked is a 503, not a 401: the SPA treats 401
// as "logged out", so a database hiccup used to sign every open tab out.
func (a *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, method, err := a.Identify(r)
		if err != nil && !errors.Is(err, ErrNoSession) {
			log.Printf("auth: cannot check credentials: %v", err)
			writeError(w, http.StatusServiceUnavailable, "authentication is temporarily unavailable")
			return
		}
		if err != nil || u == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := WithMethod(WithUser(r.Context(), u), method)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeError sends an error in the shape, content type and caching mode of
// every other API error (api.writeErr). http.Error answered text/plain and
// left the response cacheable.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// extractToken pulls an API token from the Authorization (Bearer) header or the
// X-Uploader-Token header. Returns "" if none present.
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		v := strings.TrimSpace(h)
		if after, ok := strings.CutPrefix(v, "Bearer "); ok {
			v = strings.TrimSpace(after)
		}
		if LooksLikeToken(v) {
			return v
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-Uploader-Token")); LooksLikeToken(h) {
		return h
	}
	return ""
}
