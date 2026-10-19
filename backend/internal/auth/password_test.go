package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		want     error
	}{
		{"empty", "", ErrPasswordTooShort},
		{"7 bytes", strings.Repeat("a", 7), ErrPasswordTooShort},
		{"8 bytes", strings.Repeat("a", 8), nil},
		{"72 bytes", strings.Repeat("a", 72), nil},
		{"73 bytes", strings.Repeat("a", 73), ErrPasswordTooLong},
		// The limit is bcrypt's, in bytes: 25 euro signs are 75 bytes.
		{"25 multi-byte characters", strings.Repeat("€", 25), ErrPasswordTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePassword(tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("ValidatePassword = %v, want %v", err, tc.want)
			}
		})
	}
}

// Everything ValidatePassword accepts must be hashable; bcrypt refuses more
// than 72 bytes.
func TestHashPasswordAcceptsTheLongestValidPassword(t *testing.T) {
	pw := strings.Repeat("a", MaxPasswordLen)
	if ValidatePassword(pw) != nil {
		t.Fatal("precondition: the longest password is not valid")
	}
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, pw) {
		t.Fatal("hash does not verify")
	}
	if _, err := HashPassword(pw + "a"); err == nil {
		t.Fatal("bcrypt accepted 73 bytes; MaxPasswordLen may be out of date")
	}
}
