package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
)

// testServer builds a Server with just the dependencies the pure request-scoped
// helpers need (no database, no storage).
func testServer(cfg *config.Config) *Server {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Server{
		cfg:      cfg,
		settings: settings.NewStatic(settings.Snapshot{}),
	}
}

func requestFrom(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientIPIgnoresForwardingHeadersWithoutTrustProxy(t *testing.T) {
	s := testServer(&config.Config{TrustProxy: false})
	r := requestFrom("203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	})
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the socket peer 203.0.113.9", got)
	}
}

func TestClientIPUsesRightmostForwardedForEntry(t *testing.T) {
	s := testServer(&config.Config{TrustProxy: true})
	// nginx sends "$http_x_forwarded_for, $remote_addr": everything but the last
	// entry is whatever the client chose to send. Reading the leftmost value
	// would let anyone mint a fresh rate-limit bucket per request.
	r := requestFrom("10.89.0.41:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 198.51.100.7",
	})
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the proxy-appended 198.51.100.7", got)
	}
}

func TestClientIPRejectsUnparseableForwardedValues(t *testing.T) {
	s := testServer(&config.Config{TrustProxy: true})
	// A garbage last hop must not become a rate-limit key of its own.
	r := requestFrom("203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4, not-an-ip",
	})
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want fallback to 203.0.113.9", got)
	}
}

func TestClientIPFallsBackToRealIP(t *testing.T) {
	s := testServer(&config.Config{TrustProxy: true})
	r := requestFrom("10.89.0.41:5555", map[string]string{"X-Real-IP": "198.51.100.7"})
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want 198.51.100.7", got)
	}
}

// With a configured client-IP header only that header counts: behind a proxy
// that sets X-Real-IP alone, X-Forwarded-For is whatever the client sent.
func TestClientIPUsesOnlyTheConfiguredHeader(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		headers      map[string]string
		want         string
	}{
		{"X-Real-IP beats a forged XFF", "X-Real-Ip", map[string]string{
			"X-Forwarded-For": "1.2.3.4", "X-Real-IP": "198.51.100.7"}, "198.51.100.7"},
		{"missing header falls back to the peer, not XFF", "X-Real-Ip", map[string]string{
			"X-Forwarded-For": "1.2.3.4"}, "10.89.0.41"},
		{"XFF only: no X-Real-IP fallback", "X-Forwarded-For", map[string]string{
			"X-Real-IP": "1.2.3.4"}, "10.89.0.41"},
		{"XFF only: rightmost entry", "X-Forwarded-For", map[string]string{
			"X-Forwarded-For": "1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"any header", "Cf-Connecting-Ip", map[string]string{
			"CF-Connecting-IP": "2001:db8::1", "X-Forwarded-For": "1.2.3.4"}, "2001:db8::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(&config.Config{TrustProxy: true, ClientIPHeader: tc.header})
			if got := s.clientIP(requestFrom("10.89.0.41:5555", tc.headers)); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
	// Without TrustProxy the setting changes nothing.
	s := testServer(&config.Config{ClientIPHeader: "X-Real-Ip"})
	if got := s.clientIP(requestFrom("203.0.113.9:1", map[string]string{"X-Real-IP": "1.2.3.4"})); got != "203.0.113.9" {
		t.Fatalf("clientIP without TrustProxy = %q, want the peer", got)
	}
}

func TestAdminOnly(t *testing.T) {
	handler := adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinctive: proves we reached the handler
	}))

	cases := []struct {
		name string
		user *store.User
		want int
	}{
		{"no user in context", nil, http.StatusForbidden},
		{"non-admin", &store.User{ID: uuid.New()}, http.StatusForbidden},
		{"admin", &store.User{ID: uuid.New(), IsAdmin: true}, http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.user != nil {
				r = r.WithContext(auth.WithUser(r.Context(), tc.user))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want == http.StatusForbidden {
				if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type = %q, want JSON", ct)
				}
				if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", cc)
				}
			}
		})
	}
}

func TestSessionOnly(t *testing.T) {
	handler := sessionOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, tc := range []struct {
		name   string
		method auth.Method
		want   int
	}{
		{"unknown method", 0, http.StatusForbidden},
		{"API token", auth.MethodToken, http.StatusForbidden},
		{"session cookie", auth.MethodSession, http.StatusTeapot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			ctx := auth.WithUser(r.Context(), &store.User{ID: uuid.New()})
			if tc.method != 0 {
				ctx = auth.WithMethod(ctx, tc.method)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r.WithContext(ctx))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.want == http.StatusForbidden && !strings.Contains(w.Body.String(), "API tokens are not accepted") {
				t.Fatalf("body %q does not explain the refusal", w.Body)
			}
		})
	}
}

func TestKeyedRateLimiterBurstAndIsolation(t *testing.T) {
	// Zero refill rate: exactly `burst` requests get through, ever.
	k := newKeyedRateLimiter(rate.Limit(0), 2)

	for i := range 2 {
		if !k.allow("a") {
			t.Fatalf("request %d for key a was denied inside the burst", i+1)
		}
	}
	if k.allow("a") {
		t.Fatal("key a was allowed past its burst")
	}
	// A different key must have its own bucket, or one noisy client would
	// throttle everyone else.
	if !k.allow("b") {
		t.Fatal("key b was denied because of key a's usage")
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	securityHeaders(false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Resource-Policy": "same-origin",
	}
	for h, v := range want {
		if got := w.Header().Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header is missing")
	}
	// No page uses inline script; allowing it would let injected markup run.
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src ") && strings.Contains(d, "'unsafe-inline'") {
			t.Errorf("CSP %q allows inline script", d)
		}
	}
}

// HSTS goes out only on requests that arrived over HTTPS: directly, or via a
// trusted proxy that says so in X-Forwarded-Proto.
func TestSecurityHeadersHSTSOnlyOverHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tls        bool
		proto      string
		trustProxy bool
		want       bool
	}{
		{"plain HTTP", false, "", false, false},
		{"direct TLS", true, "", false, true},
		{"trusted proxy, https", false, "https", true, true},
		{"trusted proxy, list", false, "HTTPS, http", true, true},
		{"trusted proxy, http", false, "http", true, false},
		{"untrusted forwarded https", false, "https", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := "http://example.com/"
			if tc.tls {
				url = "https://example.com/"
			}
			r := httptest.NewRequest(http.MethodGet, url, nil)
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			w := httptest.NewRecorder()
			securityHeaders(tc.trustProxy)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)
			got := w.Header().Get("Strict-Transport-Security")
			if tc.want && got != "max-age=31536000" {
				t.Fatalf("Strict-Transport-Security = %q, want max-age=31536000", got)
			}
			if !tc.want && got != "" {
				t.Fatalf("Strict-Transport-Security = %q on a non-HTTPS request", got)
			}
		})
	}
}

func TestAllowCrossOriginEmbedOverridesCORP(t *testing.T) {
	// The blob routes exist so other sites can embed the media; the global
	// same-origin policy would have the browser block a cross-origin <img>.
	w := httptest.NewRecorder()
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	allowCrossOriginEmbed(w)
	if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("CORP = %q, want cross-origin", got)
	}
}

func TestClientIPUsesLastForwardedForHeaderLine(t *testing.T) {
	s := testServer(&config.Config{TrustProxy: true})
	// A client can send its own X-Forwarded-For line; the proxy's line comes
	// last. Header.Get would read the client's.
	r := requestFrom("10.89.0.41:5555", nil)
	r.Header.Add("X-Forwarded-For", "1.2.3.4")
	r.Header.Add("X-Forwarded-For", "5.6.7.8, 198.51.100.7")
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the last entry of the last line 198.51.100.7", got)
	}
}

func TestRateKeyGroupsIPv6By64(t *testing.T) {
	a := rateKey("2001:db8:1:2:aaaa::1")
	b := rateKey("2001:db8:1:2:ffff:ffff:ffff:ffff")
	c := rateKey("2001:db8:1:3::1")
	if a != b {
		t.Fatalf("addresses in one /64 got different keys: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different /64s share a key: %q", a)
	}
	if a != "2001:db8:1:2::/64" {
		t.Fatalf("rateKey = %q, want 2001:db8:1:2::/64", a)
	}
	if got := rateKey("203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("IPv4 key = %q, want the address itself", got)
	}
	if got := rateKey("::ffff:203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("IPv4-mapped key = %q, want 203.0.113.9", got)
	}
}

func TestKeyedRateLimiterCapsEntriesLRU(t *testing.T) {
	k := newKeyedRateLimiter(rate.Limit(0), 1)
	k.maxEntries = 2
	k.allow("a")
	k.allow("b")
	k.allow("a") // "a" is now the most recently used; "b" is the oldest
	k.allow("c") // over the cap: evicts "b"
	if len(k.entries) != 2 || k.lru.Len() != 2 {
		t.Fatalf("limiter holds %d/%d entries, want 2", len(k.entries), k.lru.Len())
	}
	if _, ok := k.entries["b"]; ok {
		t.Fatal("least recently used key was not evicted")
	}
	if _, ok := k.entries["a"]; !ok {
		t.Fatal("recently used key was evicted")
	}
}

func TestKeyedRateLimiterEvictsIdleKeys(t *testing.T) {
	k := newKeyedRateLimiter(rate.Limit(0), 1)
	k.idleTTL = time.Millisecond
	k.allow("a")
	time.Sleep(5 * time.Millisecond)
	k.allow("b")
	if _, ok := k.entries["a"]; ok {
		t.Fatal("idle key survived past idleTTL")
	}
}

func TestKeyedRateLimiterResetAndExhaust(t *testing.T) {
	k := newKeyedRateLimiter(rate.Every(time.Hour), 2)
	k.allow("x")
	k.allow("x")
	ok, retry := k.take("x")
	if ok {
		t.Fatal("allowed past the burst")
	}
	if retry <= 0 || retry > time.Hour {
		t.Fatalf("retry = %v, want within (0, 1h]", retry)
	}
	k.reset("x")
	if !k.allow("x") {
		t.Fatal("still limited after reset")
	}
	k.exhaust("y")
	if k.allow("y") {
		t.Fatal("exhausted key was allowed")
	}
}

func TestLimitSendsRetryAfter(t *testing.T) {
	s := testServer(nil)
	k := newKeyedRateLimiter(rate.Every(time.Minute), 1)
	h := s.limit(k, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	w := httptest.NewRecorder()
	h(w, requestFrom("203.0.113.9:1", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("first request: status %d, want 204", w.Code)
	}
	w = httptest.NewRecorder()
	h(w, requestFrom("203.0.113.9:2", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: status %d, want 429", w.Code)
	}
	ra, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || ra < 1 || ra > 60 {
		t.Fatalf("Retry-After = %q, want 1..60 seconds", w.Header().Get("Retry-After"))
	}
}

func TestLimitSharesBucketAcrossIPv6Slash64(t *testing.T) {
	s := testServer(nil)
	k := newKeyedRateLimiter(rate.Limit(0), 1)
	h := s.limit(k, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	w := httptest.NewRecorder()
	h(w, requestFrom("[2001:db8::1]:1", nil))
	w = httptest.NewRecorder()
	h(w, requestFrom("[2001:db8::2]:1", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("a second address in the same /64 got status %d, want 429", w.Code)
	}
}

func TestLoginRefusesThrottledAccountBeforeTouchingStore(t *testing.T) {
	// The server has no store: reaching the lookup would panic, so a 429 proves
	// the per-account throttle runs first.
	s := testServer(nil)
	key := loginSourceKey("Alice", "192.0.2.1") // httptest.NewRequest's RemoteAddr
	for range loginAccountBurst {
		s.limiters().loginAccount.allow(key)
	}
	body := strings.NewReader(`{"username":"alice","password":"x"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}

func TestPublicBaseURL(t *testing.T) {
	proxied := testServer(&config.Config{TrustProxy: true})

	r := requestFrom("10.0.0.1:1", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "i.example.com"})
	r.Host = "backend:8080"
	if got := proxied.publicBaseURL(r); got != "https://i.example.com" {
		t.Fatalf("derived base = %q", got)
	}

	// A forwarded host that is not a bare host[:port] is ignored.
	for _, bad := range []string{`evil.com/"><script>`, "user@evil.com", "a b", "evil.com:99999"} {
		r = requestFrom("10.0.0.1:1", map[string]string{"X-Forwarded-Host": bad})
		r.Host = "backend:8080"
		if got := proxied.publicBaseURL(r); got != "http://backend:8080" {
			t.Fatalf("X-Forwarded-Host %q produced %q, want fallback to r.Host", bad, got)
		}
	}

	// The configured public_url always wins over any header.
	configured := &Server{
		cfg:      &config.Config{TrustProxy: true},
		settings: settings.NewStatic(settings.Snapshot{PublicURL: "https://files.example.org/"}),
	}
	r = requestFrom("10.0.0.1:1", map[string]string{"X-Forwarded-Host": "attacker.example"})
	if got := configured.publicBaseURL(r); got != "https://files.example.org" {
		t.Fatalf("configured base = %q, want https://files.example.org", got)
	}
}

func TestValidHostPort(t *testing.T) {
	good := []string{"example.com", "example.com:8443", "127.0.0.1", "127.0.0.1:80", "[::1]", "[::1]:8080", "xn--bcher-kva.de"}
	bad := []string{"", "::1", "exa mple.com", "example.com/path", "-bad.com", "example..com", "host:0", "host:abc", "ex_ample.com"}
	for _, v := range good {
		if !validHostPort(v) {
			t.Errorf("validHostPort(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if validHostPort(v) {
			t.Errorf("validHostPort(%q) = true, want false", v)
		}
	}
}
