package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	// #nosec G505 -- RFC 6238 defines TOTP over HMAC-SHA1 and every
	// authenticator app implements that; the construction does not rely on
	// SHA-1 collision resistance. A "stronger" hash here would just produce
	// codes no authenticator can generate.
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	totpPeriod = 30
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new base32-encoded TOTP shared secret (160-bit).
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// OTPAuthURL builds the otpauth:// URI that authenticator apps scan/import.
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// currentCounter is the current TOTP time step.
func currentCounter() uint64 {
	// #nosec G115 -- Unix() is positive for any clock this service can run on.
	return uint64(time.Now().Unix() / totpPeriod)
}

// totpAt computes the TOTP code for a given counter (RFC 6238 / HOTP).
func totpAt(secret string, counter uint64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", totpDigits, code%1_000_000), nil
}

// ValidateTOTP reports whether code is valid for secret now, allowing ±1 step of
// clock skew. Comparison is constant-time.
func ValidateTOTP(secret, code string) bool {
	_, ok := ValidateTOTPStep(secret, code)
	return ok
}

// ValidateTOTPStep is ValidateTOTP but also returns the time step the code
// matched. Callers that authenticate a login persist that step and refuse any
// code from a step they have already accepted, so an observed code cannot be
// replayed inside its (±1 step, i.e. up to 90 second) validity window.
func ValidateTOTPStep(secret, code string) (uint64, bool) {
	code = stripCodeSeparators(code)
	if len(code) != totpDigits {
		return 0, false
	}
	counter := currentCounter()
	for _, c := range []uint64{counter - 1, counter, counter + 1} {
		want, err := totpAt(secret, c)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return c, true
		}
	}
	return 0, false
}

// stripCodeSeparators drops the spaces and hyphens that authenticator apps and
// the recovery-code list show for readability and people type along. Refusing
// "123 456" was a wrong code -- charged to the 2FA throttle like any other.
func stripCodeSeparators(code string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code)
}

// --- secret encryption at rest (AES-GCM with the session block key) ---

// EncryptString seals plaintext with AES-GCM under key (32 bytes), returning a
// base64 string (nonce || ciphertext).
func EncryptString(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}

// DecryptString reverses EncryptString.
func DecryptString(key []byte, enc string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// --- recovery codes ---

// GenerateRecoveryCodes returns n human-friendly one-time codes (plaintext, to
// show once) and their bcrypt hashes joined by newlines (to persist).
func GenerateRecoveryCodes(n int) (plain []string, hashed string, err error) {
	var hashes []string
	for i := 0; i < n; i++ {
		buf := make([]byte, 5)
		if _, err = rand.Read(buf); err != nil {
			return nil, "", err
		}
		code := strings.ToLower(b32.EncodeToString(buf)) // 8 chars
		code = code[:4] + "-" + code[4:]
		h, herr := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if herr != nil {
			return nil, "", herr
		}
		plain = append(plain, code)
		hashes = append(hashes, string(h))
	}
	return plain, strings.Join(hashes, "\n"), nil
}

// LooksLikeRecoveryCode reports whether s has the shape GenerateRecoveryCodes
// produces ("abcd-efgh": eight base32 characters split by a hyphen), ignoring
// case, spaces and hyphens. It is a cheap pre-filter, not a validity check.
func LooksLikeRecoveryCode(s string) bool {
	_, ok := normalizeRecoveryCode(s)
	return ok
}

// normalizeRecoveryCode returns s in the form the codes are hashed in
// ("abcd-efgh"), and whether s has the shape of a recovery code at all.
func normalizeRecoveryCode(s string) (string, bool) {
	s = strings.ToLower(stripCodeSeparators(s))
	if len(s) != 8 {
		return "", false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '2' && r <= '7') {
			return "", false
		}
	}
	return s[:4] + "-" + s[4:], true
}

// ConsumeRecoveryCode checks code against the stored newline-separated bcrypt
// hashes. On match it returns the remaining hashes (with the used one removed)
// and true.
//
// Input that cannot be a recovery code is rejected before any hashing: matching
// walks up to ten bcrypt comparisons, so running it on every mistyped six-digit
// TOTP code turned each failed login into ~600ms of CPU — a cheap amplifier for
// anyone willing to send wrong codes.
func ConsumeRecoveryCode(stored, code string) (remaining string, ok bool) {
	code, shaped := normalizeRecoveryCode(code)
	if !shaped || stored == "" {
		return stored, false
	}
	lines := strings.Split(stored, "\n")
	for i, h := range lines {
		if h == "" {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(code)) == nil {
			rest := append(append([]string{}, lines[:i]...), lines[i+1:]...)
			return strings.Join(rest, "\n"), true
		}
	}
	return stored, false
}
