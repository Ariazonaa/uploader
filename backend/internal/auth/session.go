package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/securecookie"

	"uploader/internal/db/store"
)

// CookieName is the name of the session cookie.
const CookieName = "uploader_session"

// ErrNoSession indicates the request carried no valid session.
var ErrNoSession = errors.New("no session")

// SessionManager issues and validates encrypted session cookies backed by
// server-side session rows.
type SessionManager struct {
	sc     *securecookie.SecureCookie
	store  *store.Store
	ttl    time.Duration
	secure bool
}

// NewSessionManager builds a session manager. hashKey and blockKey must each be
// 32 bytes.
func NewSessionManager(st *store.Store, hashKey, blockKey []byte, ttl time.Duration, secure bool) *SessionManager {
	sc := securecookie.New(hashKey, blockKey)
	sc.MaxAge(int(ttl.Seconds()))
	return &SessionManager{sc: sc, store: st, ttl: ttl, secure: secure}
}

// Create starts a new session for the user and writes the cookie.
func (m *SessionManager) Create(ctx context.Context, w http.ResponseWriter, userID uuid.UUID, ip, userAgent string) error {
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	expires := time.Now().Add(m.ttl)
	if err := m.store.CreateSession(ctx, store.Session{
		ID:        id,
		UserID:    userID,
		ExpiresAt: expires,
		IP:        ip,
		UserAgent: userAgent,
	}); err != nil {
		return err
	}

	encoded, err := m.sc.Encode(CookieName, id)
	if err != nil {
		return err
	}
	// #nosec G124 -- HttpOnly, Secure and SameSite are all set. Secure comes
	// from UPLOADER_SECURE_COOKIES (default true) rather than a literal,
	// because a plain-HTTP local run cannot use a Secure cookie at all.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    encoded,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(m.ttl.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Pending is a verified pending-2FA token.
type Pending struct {
	UserID uuid.UUID
	// Key identifies the token by its decoded content, for counting attempts
	// per token. The transport string is no identity: base64 decoding skips
	// CR/LF and ignores the unused bits of the last character, so one token
	// has many spellings, and each one started a fresh count -- even after
	// the token had been spent.
	Key string

	passwordTag string
}

// MatchesPassword reports whether the token was issued against passwordHash,
// i.e. the password has not changed since the user passed the password step.
func (p Pending) MatchesPassword(passwordHash string) bool {
	return subtle.ConstantTimeCompare([]byte(p.passwordTag), []byte(passwordTag(passwordHash))) == 1
}

// passwordTag fingerprints a password hash for a pending token. The token is
// encrypted, but there is no need to carry the hash itself.
func passwordTag(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:16])
}

// IssuePending mints a short-lived, signed token proving a user passed the
// password step and is awaiting TOTP. It is NOT a session. The random nonce
// keeps two tokens issued within the same second apart.
//
// The token is bound to the password hash it was earned with: someone who
// learnt the old password must not be able to finish a login started before
// the owner changed it.
func (m *SessionManager) IssuePending(userID uuid.UUID, passwordHash string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	val := strings.Join([]string{
		userID.String(),
		time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
		hex.EncodeToString(nonce),
		passwordTag(passwordHash),
	}, "|")
	return m.sc.Encode("pending_2fa", val)
}

// VerifyPending validates a pending-2FA token.
func (m *SessionManager) VerifyPending(token string) (Pending, error) {
	var val string
	if err := m.sc.Decode("pending_2fa", token, &val); err != nil {
		return Pending{}, ErrNoSession
	}
	parts := strings.Split(val, "|")
	if len(parts) != 4 {
		return Pending{}, ErrNoSession
	}
	t, err := time.Parse(time.RFC3339, parts[1])
	if err != nil || time.Now().After(t) {
		return Pending{}, ErrNoSession
	}
	uid, err := uuid.Parse(parts[0])
	if err != nil {
		return Pending{}, ErrNoSession
	}
	sum := sha256.Sum256([]byte(val))
	return Pending{UserID: uid, Key: hex.EncodeToString(sum[:]), passwordTag: parts[3]}, nil
}

// Resolve validates the session cookie and returns the session, ErrNoSession
// when there is no valid one, or the lookup error when the database failed.
func (m *SessionManager) Resolve(ctx context.Context, r *http.Request) (store.Session, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return store.Session{}, ErrNoSession
	}
	var id []byte
	if err := m.sc.Decode(CookieName, cookie.Value, &id); err != nil {
		return store.Session{}, ErrNoSession
	}
	sess, err := m.store.GetSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, ErrNoSession
	}
	if err != nil {
		return store.Session{}, err
	}
	return sess, nil
}

// Destroy deletes the current session and clears the cookie (logout).
func (m *SessionManager) Destroy(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(CookieName); err == nil {
		var id []byte
		if err := m.sc.Decode(CookieName, cookie.Value, &id); err == nil {
			_ = m.store.DeleteSession(ctx, id)
		}
	}
	// #nosec G124 -- same attributes as the cookie being cleared; see Create.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
}
