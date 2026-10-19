// Package config loads and validates the bootstrap configuration from the
// environment. Everything here is read once at startup; runtime-editable values
// (app name, embed template, ...) live in the database and are handled by the
// settings package.
package config

import (
	"encoding/hex"
	"fmt"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

const envPrefix = "UPLOADER_"

// Config holds the immutable, environment-derived configuration.
type Config struct {
	HTTPAddr    string
	DatabaseURL string

	// DatabaseReplicaURL, when set, is used as a read-only replica for read-heavy
	// public queries; writes always go to DatabaseURL. Empty = no replica.
	DatabaseReplicaURL string

	// Connection-pool tuning (0 = built-in default).
	DBMaxConns        int
	DBMinConns        int
	DBMaxConnLifetime time.Duration
	DBMaxConnIdleTime time.Duration

	// SessionHashKey/SessionBlockKey secure the encrypted session cookie.
	// Both must be 32 bytes (64 hex chars).
	SessionHashKey  []byte
	SessionBlockKey []byte
	SessionTTL      time.Duration
	SecureCookies   bool

	StorageFileRoot string

	// TrustProxy enables honoring X-Forwarded-* headers (proto/host/for). Only
	// turn this on when the app sits behind a reverse proxy you control, since
	// these headers are otherwise client-spoofable.
	TrustProxy bool

	// ClientIPHeader, when set, is the only header the client address is read
	// from with TrustProxy (its rightmost entry), in canonical form. Empty reads
	// X-Forwarded-For and falls back to X-Real-IP. Set it when the proxy writes
	// just one of them: a header the proxy does not overwrite carries whatever
	// the client put there.
	ClientIPHeader string

	// DirectUploadLimit is the hard per-request upload size cap in bytes.
	DirectUploadLimit int64
	SlugLength        int
	StripEXIF         bool

	// EncryptDefault seeds the runtime "encrypt_default" setting: whether uploads
	// are stored AES-256-GCM encrypted at rest by default (per-upload override via
	// ?encrypt=true/false).
	EncryptDefault bool

	// EncryptionKey is the 32-byte at-rest blob-encryption key, supplied as 64 hex
	// chars (UPLOADER_ENCRYPTION_KEY). Optional: when empty, the key is derived
	// from SessionBlockKey. Parsed leniently — a malformed value is treated as
	// unset rather than failing startup.
	EncryptionKey []byte

	// RetentionDays seeds the runtime "retention_days" setting: when > 0, the
	// janitor auto-deletes files older than this many days (from storage + DB).
	// This is a one-time seed of the built-in default; the live value is editable
	// only in the admin dashboard (not env-configurable).
	RetentionDays int

	// TrashRetentionDays controls how long soft-deleted files stay recoverable in
	// the trash before the janitor purges them permanently (default 30).
	TrashRetentionDays int

	// AuditRetentionDays seeds the runtime "audit_retention_days" setting: the
	// janitor deletes audit-log entries older than this (0 = keep forever).
	AuditRetentionDays int

	// DefaultBlockedMime seeds the runtime "blocked_mime" setting (comma/space/
	// newline separated list of exact MIME types or "prefix/" patterns).
	DefaultBlockedMime string

	// RegistrationMode controls self-service signup: "off" (default, single-user)
	// or "invite" (public registration requires a valid single-use invite code).
	RegistrationMode string

	// Web Push (VAPID). When both keys are set, browser push is enabled.
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string // mailto: or https URL contact for push services

	// Defaults seeded into the settings table on first run.
	DefaultAppName          string
	DefaultPublicURL        string
	DefaultEmbedSiteName    string
	DefaultEmbedDescription string
	DefaultEmbedColor       string
	DefaultEmbedTitle       string
	DefaultEmbedAuthorName  string
	DefaultEmbedAuthorURL   string
	DefaultAppLogoURL       string
	DefaultLoginHeading     string
	DefaultLoginSubtext     string
	DefaultLoginBgURL       string
	DefaultHidePoweredBy    string
	DefaultLegalNotice      string

	// DefaultSlugStrategy seeds the runtime "slug_strategy" setting: how
	// auto-generated share slugs are named (random|gfycat|date). Hardcoded and
	// dashboard-only — intentionally NOT env-configurable, like the other runtime
	// UI defaults.
	DefaultSlugStrategy string
}

// InviteRegistration reports whether invite-based self-registration is enabled.
func (c *Config) InviteRegistration() bool {
	return c.RegistrationMode == "invite"
}

// PushEnabled reports whether Web Push is configured (both VAPID keys present).
func (c *Config) PushEnabled() bool {
	return c.VAPIDPublicKey != "" && c.VAPIDPrivateKey != ""
}

// Load reads configuration from the environment, applies defaults and
// validates required values. It returns an error describing every problem
// found rather than failing on the first one.
func Load() (*Config, error) {
	var problems []string
	c := &Config{
		HTTPAddr:           env("HTTP_ADDR", ":8080"),
		DatabaseURL:        env("DATABASE_URL", ""),
		DatabaseReplicaURL: env("DATABASE_REPLICA_URL", ""),
		DBMaxConns:         int(envInt64("DB_MAX_CONNS", 0, &problems)),
		DBMinConns:         int(envInt64("DB_MIN_CONNS", 0, &problems)),
		DBMaxConnLifetime:  envDuration("DB_MAX_CONN_LIFETIME", 0, &problems),
		DBMaxConnIdleTime:  envDuration("DB_MAX_CONN_IDLE_TIME", 0, &problems),
		SessionTTL:         envDuration("SESSION_TTL", 720*time.Hour, &problems),
		SecureCookies:      envBool("SECURE_COOKIES", true, &problems),
		TrustProxy:         envBool("TRUST_PROXY", false, &problems),
		StorageFileRoot:    env("STORAGE_FILE_ROOT", "./data/files"),
		SlugLength:         int(envInt64("SLUG_LENGTH", 6, &problems)),
		RegistrationMode:   strings.ToLower(strings.TrimSpace(env("REGISTRATION", "off"))),
		VAPIDPublicKey:     env("VAPID_PUBLIC_KEY", ""),
		VAPIDPrivateKey:    env("VAPID_PRIVATE_KEY", ""),
		VAPIDSubject:       env("VAPID_SUBJECT", "mailto:admin@example.com"),
		// Runtime, dashboard-editable settings: hardcoded built-in defaults seeded
		// once into the DB. These are intentionally NOT env-configurable — after the
		// first boot they are edited only in the admin dashboard (Settings).
		DirectUploadLimit:       100 << 20, // 100 MiB
		StripEXIF:               true,
		EncryptDefault:          false,
		RetentionDays:           0,
		TrashRetentionDays:      30,
		AuditRetentionDays:      365,
		DefaultBlockedMime:      "",
		DefaultAppName:          "Uploader",
		DefaultPublicURL:        "",
		DefaultEmbedSiteName:    "",
		DefaultEmbedDescription: "",
		DefaultEmbedColor:       "#5865F2",
		DefaultEmbedTitle:       "",
		DefaultEmbedAuthorName:  "",
		DefaultEmbedAuthorURL:   "",
		DefaultAppLogoURL:       "",
		DefaultLoginHeading:     "",
		DefaultLoginSubtext:     "",
		DefaultLoginBgURL:       "",
		DefaultHidePoweredBy:    "",
		DefaultLegalNotice:      "",
		DefaultSlugStrategy:     "random",
	}

	if c.DatabaseURL == "" {
		problems = append(problems, envPrefix+"DATABASE_URL is required (e.g. postgres://user:pass@host:5432/uploader?sslmode=disable)")
	}

	hashKey, err := hexKey("SESSION_HASH_KEY")
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.SessionHashKey = hashKey

	blockKey, err := hexKey("SESSION_BLOCK_KEY")
	if err != nil {
		problems = append(problems, err.Error())
	}
	c.SessionBlockKey = blockKey

	// Optional at-rest encryption key: 64 hex chars → 32 bytes. Parsed leniently
	// (empty or malformed → unset), so the key falls back to one derived from the
	// session block key at startup rather than failing to boot.
	if raw := strings.TrimSpace(env("ENCRYPTION_KEY", "")); raw != "" {
		if key, decErr := hex.DecodeString(raw); decErr == nil && len(key) == 32 {
			c.EncryptionKey = key
		}
	}

	if h := strings.TrimSpace(env("CLIENT_IP_HEADER", "")); h != "" {
		if validHeaderName(h) {
			c.ClientIPHeader = textproto.CanonicalMIMEHeaderKey(h)
		} else {
			problems = append(problems, envPrefix+"CLIENT_IP_HEADER must be an HTTP header name, e.g. X-Real-IP")
		}
	}

	if c.SlugLength < 4 {
		problems = append(problems, envPrefix+"SLUG_LENGTH must be at least 4")
	}
	if c.DirectUploadLimit <= 0 {
		problems = append(problems, "DirectUploadLimit default must be positive")
	}
	if c.RegistrationMode == "" {
		c.RegistrationMode = "off"
	}
	if c.RegistrationMode != "off" && c.RegistrationMode != "invite" {
		problems = append(problems, envPrefix+`REGISTRATION must be "off" or "invite"`)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return c, nil
}

// hexKey decodes a 32-byte key supplied as 64 hex characters.
func hexKey(name string) ([]byte, error) {
	raw := env(name, "")
	if raw == "" {
		return nil, fmt.Errorf("%s%s is required (64 hex chars = 32 bytes; generate with: openssl rand -hex 32)", envPrefix, name)
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s%s must be valid hex: %v", envPrefix, name, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s%s must decode to 32 bytes, got %d", envPrefix, name, len(key))
	}
	return key, nil
}

// validHeaderName reports whether s is an HTTP field name (an RFC 9110 token).
func validHeaderName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return s != ""
}

func env(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return v
	}
	return def
}

// The typed readers below return def when the variable is unset or empty (the
// compose file passes optional variables through even when .env leaves them
// blank). A value that is set but does not parse is appended to problems and
// fails Load: falling back to the default silently turned TRUST_PROXY=yes into
// false, which put every client behind the proxy into one rate-limit bucket.

func envBool(name string, def bool, problems *[]string) bool {
	v := strings.TrimSpace(env(name, ""))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s%s=%q is not a boolean (use true or false)", envPrefix, name, v))
		return def
	}
	return b
}

func envInt64(name string, def int64, problems *[]string) int64 {
	v := strings.TrimSpace(env(name, ""))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s%s=%q is not an integer", envPrefix, name, v))
		return def
	}
	return n
}

func envDuration(name string, def time.Duration, problems *[]string) time.Duration {
	v := strings.TrimSpace(env(name, ""))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s%s=%q is not a duration (e.g. 720h, 30m)", envPrefix, name, v))
		return def
	}
	return d
}
