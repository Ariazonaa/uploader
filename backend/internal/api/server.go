// Package api wires the HTTP server: routing, handlers and middleware.
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
	"uploader/internal/storage"
	"uploader/internal/upload"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	storage  storage.Storage
	sessions *auth.SessionManager
	authn    *auth.Authenticator
	uploads  *upload.Service
	settings *settings.Service

	// encryptionKey is the 32-byte at-rest blob-encryption key, used by handleRaw
	// to decrypt encrypted blobs on the fly. Same key the upload service seals with.
	encryptionKey []byte

	// views, when set, batches view analytics in memory and writes them
	// periodically (see viewBatcher). nil = record each view inline.
	views *viewBatcher

	// spa serves the embedded single-page app (may be nil in dev).
	spa http.Handler

	// Handler-level throttles (see serverLimiters), created lazily.
	limitersOnce sync.Once
	lims         *serverLimiters
}

// SetEncryptionKey installs the at-rest blob-encryption key used to decrypt
// encrypted blobs when serving them (mirrors upload.Service.SetEncryptionKey).
func (s *Server) SetEncryptionKey(key []byte) { s.encryptionKey = key }

// encKey returns the at-rest blob-encryption key (32 bytes) for decryption.
func (s *Server) encKey() []byte { return s.encryptionKey }

// etagFor derives the public ETag for a file from its content hash. The raw
// SHA-256 is deliberately never exposed: it would let anyone confirm that a share
// link holds a specific known file without downloading it. An HMAC under a
// server-side key keeps the validator strong and stable across restarts while
// revealing nothing about the content.
func (s *Server) etagFor(contentHash string) string {
	mac := hmac.New(sha256.New, s.encryptionKey)
	mac.Write([]byte("uploader-etag-v1"))
	mac.Write([]byte(contentHash))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// NewServer constructs a Server.
func NewServer(
	cfg *config.Config,
	st *store.Store,
	blobs storage.Storage,
	sessions *auth.SessionManager,
	authn *auth.Authenticator,
	uploads *upload.Service,
	set *settings.Service,
	spa http.Handler,
) *Server {
	return &Server{
		cfg: cfg, store: st, storage: blobs, sessions: sessions,
		authn: authn, uploads: uploads, settings: set, spa: spa,
	}
}

// maxUploadBytes resolves the per-file upload cap. The runtime "max_upload_mib"
// setting wins when set; otherwise we fall back to the boot-time env default
// (cfg.DirectUploadLimit, validated > 0), so a cleared setting never yields 0.
func (s *Server) maxUploadBytes() int64 {
	if b := s.settings.Current().MaxUploadBytes(); b > 0 {
		return b
	}
	return s.cfg.DirectUploadLimit
}

// --- response helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// API responses are dynamic and per-user; never let the browser or any
	// intermediary serve a stale copy (e.g. a file list after a delete).
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// clientIP extracts the best-guess client IP.
//
// Forwarding headers are only honored when TrustProxy is enabled, and then only
// the RIGHTMOST X-Forwarded-For entry. The chain is client-controlled up to the
// hop our own proxy appends (nginx sends "$http_x_forwarded_for, $remote_addr"),
// so reading the leftmost value would let any client forge a fresh rate-limit
// bucket per request — defeating the login brute-force limiter and letting them
// write arbitrary IPs into the audit log. The last entry is the one the proxy we
// trust observed, and it is the only one a client cannot influence.
//
// A request can carry several X-Forwarded-For header lines (a client-sent one
// plus the proxy's). Header.Get would return the FIRST line -- the client's --
// so the rightmost entry is taken from the LAST line, i.e. the last entry
// overall.
//
// Values that don't parse as an IP are discarded (a misconfigured proxy must not
// turn into an unbounded set of rate-limit keys).
//
// With ClientIPHeader (UPLOADER_CLIENT_IP_HEADER) set, that header alone is
// read, the same way, with no fallback. Behind a proxy that writes only
// X-Real-IP, the X-Forwarded-For preferred by default is passed through
// untouched from the client.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if h := s.cfg.ClientIPHeader; h != "" {
			if ip := lastForwardedIP(r, h); ip != "" {
				return ip
			}
		} else {
			if ip := lastForwardedIP(r, "X-Forwarded-For"); ip != "" {
				return ip
			}
			// Proxies that set a single-valued X-Real-IP instead of a chain.
			if ip := validIP(r.Header.Get("X-Real-IP")); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lastForwardedIP returns the rightmost entry of the last line of the named
// header if it is an IP address, else "".
func lastForwardedIP(r *http.Request, name string) string {
	vals := r.Header.Values(name)
	if len(vals) == 0 {
		return ""
	}
	parts := strings.Split(vals[len(vals)-1], ",")
	return validIP(parts[len(parts)-1])
}

// validIP returns the trimmed value if it parses as an IP address, else "".
func validIP(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || net.ParseIP(v) == nil {
		return ""
	}
	return v
}

// publicBaseURL returns the canonical external base URL (no trailing slash).
// The configured public_url setting always wins; only when it is empty is the
// URL derived from the request. Operators behind a proxy should set public_url:
// the derived fallback trusts X-Forwarded-Host (with TrustProxy), which is only
// safe if the proxy overwrites it (the shipped nginx config does). A forwarded
// host that is not a plain host[:port] is ignored in favour of r.Host.
func (s *Server) publicBaseURL(r *http.Request) string {
	if u := strings.TrimRight(s.settings.Current().PublicURL, "/"); u != "" {
		return u
	}
	scheme := "http"
	host := r.Host
	if s.cfg.TrustProxy {
		if proto := forwardedProto(r); proto != "" {
			scheme = proto
		}
		if vals := r.Header.Values("X-Forwarded-Host"); len(vals) > 0 {
			// As with X-Forwarded-For, the proxy's value is the last one.
			parts := strings.Split(vals[len(vals)-1], ",")
			if fwd := strings.TrimSpace(parts[len(parts)-1]); validHostPort(fwd) {
				host = fwd
			}
		}
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}

// forwardedProto returns the scheme X-Forwarded-Proto reports, "http" or
// "https", or "" when the header is absent or says anything else. A proxy may
// send a comma-separated list ("https, http"); the first entry is the
// client-facing hop. Only meaningful with TrustProxy.
func forwardedProto(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if first, _, ok := strings.Cut(proto, ","); ok {
		proto = first
	}
	switch proto = strings.ToLower(strings.TrimSpace(proto)); proto {
	case "http", "https":
		return proto
	}
	return ""
}

// validHostPort reports whether v is exactly a "host" or "host:port" (DNS name,
// IPv4, or bracketed IPv6) -- no scheme, path, userinfo or whitespace that could
// smuggle markup or a different origin into generated links.
func validHostPort(v string) bool {
	if v == "" || len(v) > 260 {
		return false
	}
	host, port := v, ""
	if h, p, err := net.SplitHostPort(v); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		host = v[1 : len(v)-1]
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		// A bare IPv6 literal must be bracketed to be a valid URL host.
		return ip.To4() != nil || strings.HasPrefix(v, "[")
	}
	return validHostname(strings.ToLower(host))
}

// validHostname reports whether h (already lowercase) is a syntactically valid
// DNS name: at most 253 characters, dot-separated labels of 1-63 characters
// from [a-z0-9-] that neither start nor end with a hyphen.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
