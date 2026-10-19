package api

import (
	"container/list"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"uploader/internal/auth"
)

// adminOnly rejects non-admin users with 403. It must run after auth.Require
// (which puts the user in the context).
func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := auth.UserFrom(r.Context()); !ok || !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionOnly rejects requests that did not authenticate with a session cookie
// with 403. It guards token management and account security: an API token
// sits in ShareX configs and scripts, and one that leaks must not be able to
// mint further tokens, sign the owner out, change the password, enrol 2FA or
// export and delete the account. It must run after auth.Require.
func sessionOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.MethodFrom(r.Context()) != auth.MethodSession {
			writeErr(w, http.StatusForbidden,
				"this endpoint requires a browser login; API tokens are not accepted here")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// uploadDeadline is how long a single request may spend receiving an upload
// body. The server's global ReadTimeout is deliberately short as a slow-loris
// defence; the upload paths push it out to something a large transfer over a
// slow mobile link can actually meet, without going back to "no limit at all".
const uploadDeadline = 30 * time.Minute

// extendUploadDeadline grants this request the longer upload read deadline. It
// is best-effort: if the connection does not support deadline control the call
// simply does nothing and the global ReadTimeout still applies.
func extendUploadDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadDeadline))
}

// securityHeaders applies conservative security headers to every response.
// trustProxy decides whether X-Forwarded-Proto may vouch for HTTPS.
func securityHeaders(trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			// CSP: lock down origins (no external or inline scripts, no framing, no
			// <base> injection). No page ships an inline <script> or on* handler, so
			// script-src stays 'self' only: injected markup cannot run script. Styles
			// keep 'unsafe-inline' because the server-rendered share/error pages use
			// small inline <style> blocks. img/media allow https so operator branding
			// + http.cat work.
			h.Set("Content-Security-Policy",
				"default-src 'self'; "+
					"img-src 'self' https: data:; "+
					"media-src 'self' https:; "+
					"script-src 'self'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"connect-src 'self'; "+
					"object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
			// HSTS, one year. The reverse proxy leaves security headers to the app,
			// so nobody else sends it. Browsers ignore it over plain HTTP, hence
			// only on HTTPS requests. No includeSubDomains: other hosts under the
			// parent domain are none of this app's business.
			if r.TLS != nil || (trustProxy && forwardedProto(r) == "https") {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// maxLimiterEntries caps the number of keys a single keyedRateLimiter tracks.
// Idle keys are evicted after idleTTL, but a flood of distinct keys (rotating
// source addresses) would otherwise grow the map without bound before any of
// them goes idle. At the cap the least recently used key is evicted.
const maxLimiterEntries = 100_000

// keyedRateLimiter holds a token-bucket limiter per key (e.g. client IP). Keys
// live in an LRU list: idle keys are dropped after idleTTL (by then their
// bucket has fully refilled, so eviction never resets an active limit) and the
// total is capped at maxEntries.
type keyedRateLimiter struct {
	mu         sync.Mutex
	entries    map[string]*list.Element // value: *rateEntry
	lru        *list.List               // front = most recently used
	r          rate.Limit
	burst      int
	idleTTL    time.Duration
	maxEntries int
}

type rateEntry struct {
	key      string
	lim      *rate.Limiter
	lastSeen time.Time
}

func newKeyedRateLimiter(r rate.Limit, burst int) *keyedRateLimiter {
	return &keyedRateLimiter{
		entries:    make(map[string]*list.Element),
		lru:        list.New(),
		r:          r,
		burst:      burst,
		idleTTL:    time.Hour,
		maxEntries: maxLimiterEntries,
	}
}

// entry returns the bucket for key, creating it if create is set (else nil when
// absent). The caller must hold k.mu.
func (k *keyedRateLimiter) entry(key string, now time.Time, create bool) *rateEntry {
	// Drop idle keys from the cold end of the list; this is O(evicted).
	for back := k.lru.Back(); back != nil; back = k.lru.Back() {
		e := back.Value.(*rateEntry)
		if now.Sub(e.lastSeen) <= k.idleTTL {
			break
		}
		k.lru.Remove(back)
		delete(k.entries, e.key)
	}
	if el, ok := k.entries[key]; ok {
		k.lru.MoveToFront(el)
		e := el.Value.(*rateEntry)
		e.lastSeen = now
		return e
	}
	if !create {
		return nil
	}
	for len(k.entries) >= k.maxEntries && k.lru.Len() > 0 {
		back := k.lru.Back()
		k.lru.Remove(back)
		delete(k.entries, back.Value.(*rateEntry).key)
	}
	e := &rateEntry{key: key, lim: rate.NewLimiter(k.r, k.burst), lastSeen: now}
	k.entries[key] = k.lru.PushFront(e)
	return e
}

// allow consumes one token for key and reports whether the request may pass.
func (k *keyedRateLimiter) allow(key string) bool {
	ok, _ := k.take(key)
	return ok
}

// take consumes one token for key. When the bucket is empty it reports false
// and how long until a token is available (for Retry-After).
func (k *keyedRateLimiter) take(key string) (bool, time.Duration) {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.entry(key, now, true)
	if e.lim.AllowN(now, 1) {
		return true, 0
	}
	return false, retryDelay(e.lim, now, k.idleTTL)
}

// reset forgets key, so its next request starts with a full bucket.
func (k *keyedRateLimiter) reset(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if el, ok := k.entries[key]; ok {
		k.lru.Remove(el)
		delete(k.entries, key)
	}
}

// refund gives back one token taken from key's bucket, for an attempt that
// turned out not to count. Unlike reset it never grants more than that.
func (k *keyedRateLimiter) refund(key string) {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if e := k.entry(key, now, false); e != nil {
		// rate.Limiter has no call to add a token, but a negative count does
		// that; the limiter caps the result at the burst on its next read.
		e.lim.AllowN(now, -1)
	}
}

// exhaust empties key's bucket for good (until the key is evicted as idle),
// e.g. to make a pending-2FA token single-use once it has succeeded.
func (k *keyedRateLimiter) exhaust(key string) {
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.entry(key, now, true).lim = rate.NewLimiter(0, 0)
}

// retryDelay is how long until lim holds one token again. A bucket that never
// refills (zero rate) reports fallback.
func retryDelay(lim *rate.Limiter, now time.Time, fallback time.Duration) time.Duration {
	r := lim.Limit()
	if r <= 0 {
		return fallback
	}
	need := 1 - lim.TokensAt(now)
	if need <= 0 {
		return 0
	}
	return time.Duration(need / float64(r) * float64(time.Second))
}

// writeRateLimited sends a 429 with a Retry-After header (whole seconds,
// rounded up, at least 1) so well-behaved clients know when to come back.
func writeRateLimited(w http.ResponseWriter, retry time.Duration, msg string) {
	secs := int64(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	writeErr(w, http.StatusTooManyRequests, msg)
}

// rateKey turns a client IP into a rate-limit key. IPv4 addresses are used as
// is; IPv6 addresses are truncated to their /64, the smallest prefix a single
// subscriber is normally assigned. Keyed per full address, one host could
// rotate through 2^64 source addresses and get a fresh bucket for each.
func rateKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// limit wraps a handler, rejecting requests from a key that exceeds the rate
// with 429. The key is the client IP (IPv6: its /64).
func (s *Server) limit(k *keyedRateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := k.take(rateKey(s.clientIP(r))); !ok {
			writeRateLimited(w, retry, "rate limit exceeded")
			return
		}
		next(w, r)
	}
}

// limitUser rate-limits per authenticated user (in addition to per-IP), so a
// single account can't hammer an endpoint from many IPs. Must run behind
// auth.Require.
func (s *Server) limitUser(k *keyedRateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := rateKey(s.clientIP(r))
		if u, ok := auth.UserFrom(r.Context()); ok {
			key = "u:" + u.ID.String()
		}
		if ok, retry := k.take(key); !ok {
			writeRateLimited(w, retry, "rate limit exceeded")
			return
		}
		next(w, r)
	}
}

// serverLimiters are the throttles that live on the Server rather than in
// Routes(): they are keyed by something other than the client address (an
// account, a pending-2FA token) and are applied from inside handlers. All are
// in-memory and per instance; with several app instances each keeps its own
// buckets, which weakens but never breaks them (per-instance limiter state is
// allowed to degrade gracefully -- see CLAUDE.md).
type serverLimiters struct {
	// loginAccount throttles failed password attempts per attempted username
	// and source network (IPv6: its /64). Each attempt takes a token; a
	// successful one resets the bucket, so in effect only failures count.
	loginAccount *keyedRateLimiter
	// loginAccountAll caps failed password attempts per attempted username
	// whatever the source: the per-IP limits alone are defeated by a botnet or
	// a large IPv6 allocation. Its budget is several sources' worth, so that
	// one source can lock an account out for itself, but not for everyone.
	loginAccountAll *keyedRateLimiter
	// totpAccount throttles failed second-factor attempts per account.
	totpAccount *keyedRateLimiter
	// pendingToken caps attempts per pending-2FA token and makes the token
	// single-use after success; the token itself is stateless and valid for 5
	// minutes, so without this it could be replayed freely within that window.
	pendingToken *keyedRateLimiter
	// pushTest bounds how often one user can trigger outbound push requests.
	pushTest *keyedRateLimiter
}

const (
	// Failed password logins per username and source: 10 at once, then one
	// per minute.
	loginAccountBurst = 10
	loginAccountEvery = time.Minute
	// ... and per username across all sources: 30 at once, then one per 30
	// seconds, so emptying it takes three sources and keeping it empty two.
	loginAccountAllBurst = 30
	loginAccountAllEvery = 30 * time.Second
	// Failed 2FA codes per account: 5 at once, then one per two minutes.
	totpAccountBurst = 5
	totpAccountEvery = 2 * time.Minute
	// Code attempts per pending-2FA token before the user must log in again.
	pendingTokenAttempts = 5
)

func newServerLimiters() *serverLimiters {
	pending := newKeyedRateLimiter(0, pendingTokenAttempts)
	// Pending tokens expire after 5 minutes; keep their counters a bit longer.
	pending.idleTTL = 10 * time.Minute
	return &serverLimiters{
		loginAccount:    newKeyedRateLimiter(rate.Every(loginAccountEvery), loginAccountBurst),
		loginAccountAll: newKeyedRateLimiter(rate.Every(loginAccountAllEvery), loginAccountAllBurst),
		totpAccount:     newKeyedRateLimiter(rate.Every(totpAccountEvery), totpAccountBurst),
		pendingToken:    pending,
		pushTest:        newKeyedRateLimiter(rate.Every(20*time.Second), 3),
	}
}

// limiters returns the server's handler-level throttles, creating them on first
// use (so a zero Server, as built in tests, works too).
func (s *Server) limiters() *serverLimiters {
	s.limitersOnce.Do(func() { s.lims = newServerLimiters() })
	return s.lims
}
