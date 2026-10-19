package config

import (
	"strings"
	"testing"
	"time"
)

// setRequired sets the variables Load cannot do without.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv(envPrefix+"DATABASE_URL", "postgres://u:p@localhost/db")
	t.Setenv(envPrefix+"SESSION_HASH_KEY", strings.Repeat("ab", 32))
	t.Setenv(envPrefix+"SESSION_BLOCK_KEY", strings.Repeat("cd", 32))
}

// A typed variable that is set but does not parse fails Load instead of
// silently falling back to the default.
func TestLoadRejectsUnparseableTypedValues(t *testing.T) {
	for name, value := range map[string]string{
		"TRUST_PROXY":          "yes",
		"SECURE_COOKIES":       "on",
		"SLUG_LENGTH":          "six",
		"DB_MAX_CONNS":         "1e3",
		"SESSION_TTL":          "30",
		"DB_MAX_CONN_LIFETIME": "1 hour",
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(envPrefix+name, value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), envPrefix+name) {
				t.Fatalf("Load with %s=%q: err = %v, want a problem naming the variable", name, value, err)
			}
		})
	}
}

func TestLoadClientIPHeader(t *testing.T) {
	setRequired(t)
	t.Setenv(envPrefix+"CLIENT_IP_HEADER", " x-real-ip ")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientIPHeader != "X-Real-Ip" {
		t.Fatalf("ClientIPHeader = %q, want the canonical X-Real-Ip", c.ClientIPHeader)
	}
	for _, bad := range []string{"X Real IP", "X-Real-IP:", "X-Réal-IP"} {
		t.Setenv(envPrefix+"CLIENT_IP_HEADER", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CLIENT_IP_HEADER") {
			t.Errorf("CLIENT_IP_HEADER=%q: err = %v, want a problem", bad, err)
		}
	}
	t.Setenv(envPrefix+"CLIENT_IP_HEADER", "")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientIPHeader != "" {
		t.Fatalf("empty CLIENT_IP_HEADER gave %q, want the default", c.ClientIPHeader)
	}
}

// Unset and empty both mean "use the default"; valid values are applied.
func TestLoadTypedValues(t *testing.T) {
	setRequired(t)
	t.Setenv(envPrefix+"SECURE_COOKIES", "")
	t.Setenv(envPrefix+"DB_MAX_CONN_LIFETIME", "  ")
	t.Setenv(envPrefix+"TRUST_PROXY", " TRUE ")
	t.Setenv(envPrefix+"SESSION_TTL", "48h")
	t.Setenv(envPrefix+"SLUG_LENGTH", "8")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookies || c.DBMaxConnLifetime != 0 {
		t.Errorf("empty values did not keep the defaults: SecureCookies=%v DBMaxConnLifetime=%v", c.SecureCookies, c.DBMaxConnLifetime)
	}
	if !c.TrustProxy || c.SessionTTL != 48*time.Hour || c.SlugLength != 8 {
		t.Errorf("values not applied: TrustProxy=%v SessionTTL=%v SlugLength=%d", c.TrustProxy, c.SessionTTL, c.SlugLength)
	}
}
