// Package auth implements password hashing, cookie sessions, API tokens and the
// authentication middleware.
package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLen is the shortest password (in bytes) an account may set.
	MinPasswordLen = 8
	// MaxPasswordLen is bcrypt's input limit in bytes. bcrypt refuses longer
	// passwords instead of ignoring the excess, which surfaced as a 500 from
	// every endpoint that sets one.
	MaxPasswordLen = 72
)

// Errors returned by ValidatePassword; their text is meant for the user.
var (
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 72 bytes")
)

// ValidatePassword checks a new password against the length limits. Every
// path that sets a password -- setup, registration, password change and the
// CLI -- goes through it.
func ValidatePassword(password string) error {
	switch {
	case len(password) < MinPasswordLen:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLen:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns a bcrypt hash of the password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports whether the password matches the stored hash. It runs
// in constant time relative to the hash (bcrypt property).
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is a valid bcrypt hash (of a random string) used to spend the same
// work as a real comparison when the user does not exist, so login latency does
// not reveal whether a username is valid. Generated once at init.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("uploader-dummy-password"), bcrypt.DefaultCost)

// DummyVerify performs a throwaway bcrypt comparison to equalize the response
// time of the user-not-found path with the password-mismatch path.
func DummyVerify(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}
