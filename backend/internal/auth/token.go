package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// TokenPrefixLen is the length of the non-secret prefix stored for fast lookup.
const TokenPrefixLen = 12

const tokenScheme = "upl_"

// tokenRandomBytes is the token's entropy. The first 8 encoded characters end
// up in the stored, non-secret prefix; the remaining ~208 bits are the secret.
const tokenRandomBytes = 32

// tokenHashScheme tags a stored token hash as SHA-256. Older rows hold a
// bcrypt hash ("$2a$...") instead.
const tokenHashScheme = "sha256:"

// GeneratedToken is the result of minting a new API token. The plaintext is
// shown to the user exactly once and never stored.
type GeneratedToken struct {
	Plaintext string
	Prefix    string
	Hash      string
}

// GenerateToken mints a new random API token, returning the plaintext (to show
// the user once), the lookup prefix and the hash (to persist).
func GenerateToken() (GeneratedToken, error) {
	buf := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return GeneratedToken{}, err
	}
	plaintext := tokenScheme + base64.RawURLEncoding.EncodeToString(buf)
	return GeneratedToken{
		Plaintext: plaintext,
		Prefix:    TokenPrefix(plaintext),
		Hash:      HashToken(plaintext),
	}, nil
}

// HashToken returns the stored form of a token: its SHA-256. A fast hash is
// enough because the token is random rather than chosen by a person, so there
// is nothing to guess; and it has to be fast, because every request that
// presents a known (non-secret) prefix pays for a comparison before any rate
// limit applies. With bcrypt that was a CPU-exhaustion lever.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return tokenHashScheme + hex.EncodeToString(sum[:])
}

// IsLegacyTokenHash reports whether a stored hash is a bcrypt digest from
// before tokens were stored as SHA-256.
func IsLegacyTokenHash(hash string) bool {
	return strings.HasPrefix(hash, "$2")
}

// TokenPrefix returns the indexed lookup prefix for a token.
func TokenPrefix(token string) string {
	if len(token) < TokenPrefixLen {
		return token
	}
	return token[:TokenPrefixLen]
}

// LooksLikeToken reports whether a credential string is shaped like an API token
// (lets the middleware skip session handling for obvious tokens).
func LooksLikeToken(s string) bool {
	return strings.HasPrefix(s, tokenScheme)
}

// VerifyToken reports whether the plaintext matches the stored hash: SHA-256
// in constant time, or bcrypt for tokens minted before the switch.
func VerifyToken(hash, plaintext string) bool {
	if strings.HasPrefix(hash, tokenHashScheme) {
		return subtle.ConstantTimeCompare([]byte(hash), []byte(HashToken(plaintext))) == 1
	}
	if IsLegacyTokenHash(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
	}
	return false
}
