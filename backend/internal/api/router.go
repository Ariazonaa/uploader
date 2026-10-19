package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"
)

// Routes builds the HTTP handler with all routes and middleware.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders(s.cfg.TrustProxy))
	r.Use(crossOriginProtection().Handler)

	// Per-IP rate limiters: strict on login (brute force), looser on uploads.
	loginLimiter := newKeyedRateLimiter(rate.Every(12*time.Second), 5)
	uploadLimiter := newKeyedRateLimiter(rate.Limit(2), 20)
	uploadUserLimiter := newKeyedRateLimiter(rate.Limit(4), 40) // per-account cap
	// tus PATCH carries one chunk of a single upload, so it fires far more often
	// than a direct upload; it gets its own generous per-account budget rather
	// than the upload limiter, which would stall a legitimate large transfer.
	tusChunkLimiter := newKeyedRateLimiter(rate.Limit(30), 120)
	// /ready pings the database and writes a probe blob, so it is not free —
	// unauthenticated callers get a modest budget.
	readyLimiter := newKeyedRateLimiter(rate.Limit(1), 5)
	// Creating API tokens and registering push subscriptions are rare,
	// deliberate acts; per account.
	tokenCreateLimiter := newKeyedRateLimiter(rate.Every(time.Minute), 10)
	pushSubscribeLimiter := newKeyedRateLimiter(rate.Every(time.Minute), 10)

	r.Route("/api/v1", func(r chi.Router) {
		// Unknown API paths and methods answer in JSON. Set here, before the SPA
		// fallback below: chi copies a parent's NotFound handler into every
		// subrouter that has none of its own, which made a mistyped endpoint (or a
		// POST to one) come back as "200 OK" with the SPA's index.html.
		r.NotFound(apiNotFound)
		r.MethodNotAllowed(apiMethodNotAllowed(r))

		// Public
		r.Get("/health", s.handleHealth)
		r.Get("/ready", s.limit(readyLimiter, s.handleReady))
		r.Get("/public/settings", s.handlePublicSettings)
		r.Get("/public/files/{slug}", s.handlePublicFile)
		r.Post("/auth/setup", s.limit(loginLimiter, s.handleSetup))
		r.Post("/auth/login", s.limit(loginLimiter, s.handleLogin))
		r.Post("/auth/login/totp", s.limit(loginLimiter, s.handleLoginTOTP))
		r.Post("/auth/register", s.limit(loginLimiter, s.handleRegister))

		// Authenticated (any logged-in user)
		r.Group(func(r chi.Router) {
			r.Use(s.authn.Require)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Get("/tokens", s.handleListTokens)

			// Account security and token management: browser session only, an
			// API token gets 403 (see sessionOnly).
			r.Group(func(r chi.Router) {
				r.Use(sessionOnly)

				// Everything that re-checks the password shares the login's per-IP
				// limiter (and, inside verifyAccountPassword, its per-account one).
				r.Post("/auth/password", s.limit(loginLimiter, s.handleChangePassword))
				r.Post("/auth/totp/setup", s.limit(loginLimiter, s.handleTOTPSetup))
				r.Post("/auth/totp/enable", s.limit(loginLimiter, s.handleTOTPEnable))
				r.Post("/auth/totp/disable", s.limit(loginLimiter, s.handleTOTPDisable))
				r.Get("/auth/sessions", s.handleListSessions)
				r.Delete("/auth/sessions/{id}", s.handleRevokeSession)
				r.Post("/auth/sessions/revoke-others", s.handleRevokeOtherSessions)
				r.Delete("/auth/account", s.limit(loginLimiter, s.handleDeleteAccount))
				r.Get("/auth/export", s.handleExport)

				r.Post("/tokens", s.limitUser(tokenCreateLimiter, s.handleCreateToken))
				r.Delete("/tokens/{id}", s.handleDeleteToken)
			})

			r.Post("/upload", s.limit(uploadLimiter, s.limitUser(uploadUserLimiter, s.handleUpload)))
			r.Get("/upload/defaults", s.handleUploadDefaults)

			// Resumable uploads (tus 1.0). Creation shares the direct-upload
			// limiters — without them tus was a complete bypass of the upload rate
			// limit — while chunk PATCHes get their own higher-frequency budget.
			r.Options("/tus", s.handleTusOptions)
			r.Post("/tus", s.limit(uploadLimiter, s.limitUser(uploadUserLimiter, s.handleTusCreate)))
			r.Head("/tus/{id}", s.handleTusHead)
			r.Patch("/tus/{id}", s.limitUser(tusChunkLimiter, s.handleTusPatch))
			r.Delete("/tus/{id}", s.limitUser(tusChunkLimiter, s.handleTusDelete))

			r.Get("/files", s.handleListFiles)
			r.Get("/files/trash", s.handleListTrash)
			r.Post("/files/trash/empty", s.handleEmptyTrash)
			r.Patch("/files/{id}", s.handleRenameFile)
			r.Delete("/files/{id}", s.handleDeleteFile)
			r.Post("/files/{id}/restore", s.handleRestoreFile)
			r.Delete("/files/{id}/purge", s.handlePurgeFile)
			r.Post("/files/bulk-delete", s.handleBulkDeleteFiles)

			r.Get("/stats", s.handleStats)
			r.Get("/stats/activity", s.handleActivity)
			r.Get("/stats/export", s.handleStatsExport)

			r.Post("/push/subscribe", s.limitUser(pushSubscribeLimiter, s.handlePushSubscribe))
			r.Post("/push/unsubscribe", s.handlePushUnsubscribe)
			r.Post("/push/test", s.handlePushTest)

			// OpenAPI spec: any logged-in user (cookie or API token) may read it;
			// logged-out requests get 401. Powers the in-app Swagger UI docs page.
			r.Get("/openapi.yaml", s.handleOpenAPISpec)

			// Admin-only: global config, view stats, invite management, audit log.
			r.Group(func(r chi.Router) {
				r.Use(adminOnly)

				// Reading the settings is admin-only too: the snapshot is operator
				// configuration (blocked MIME types, retention, public URL, embed
				// templates), and the SPA only fetches it from the admin-gated
				// settings page.
				r.Get("/settings", s.handleGetSettings)
				r.Patch("/settings", s.handlePatchSettings)

				r.Get("/stats/views", s.handleViewStats)

				r.Get("/invites", s.handleListInvites)
				r.Post("/invites", s.handleCreateInvite)
				r.Delete("/invites/{code}", s.handleRevokeInvite)

				r.Get("/audit", s.handleListAudit)
			})
		})
	})

	// Public blob + share routes. chi does not answer HEAD for a GET route, and
	// link checkers, unfurlers and download managers probe with HEAD, so it is
	// registered alongside; the handlers record views for GET only.
	getHead := func(pattern string, h http.HandlerFunc) {
		r.Get(pattern, h)
		r.Head(pattern, h)
	}
	getHead("/raw/{slug}", s.handleRaw)
	getHead("/thumb/{slug}", s.handleThumb)
	getHead("/hls/{slug}/{file}", s.handleHLS)
	getHead("/s/{slug}", s.handleSharePage)
	getHead("/p/{slug}", s.handlePlayer)
	getHead("/oembed", s.handleOEmbed)
	r.Get("/d/{key}", s.handleDeleteConfirm)
	r.Post("/d/{key}", s.handleDeleteByKey)

	// SPA: everything else falls through to the embedded frontend (if present).
	if s.spa != nil {
		r.NotFound(s.spa.ServeHTTP)
	}

	return r
}

// crossOriginProtection refuses state-changing browser requests from another
// origin (Sec-Fetch-Site, or Origin vs Host where that header is missing).
// SameSite=Lax alone leaves login CSRF open -- signing a victim into the
// attacker's account needs no cookie -- and the JSON handlers accept any
// Content-Type, so a cross-site form post reached them. Requests carrying
// neither header are not from a browser (ShareX, curl, scripts) and pass, as
// does the same-origin delete form on /d/{key}.
func crossOriginProtection() *http.CrossOriginProtection {
	c := http.NewCrossOriginProtection()
	c.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const msg = "cross-origin request refused"
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusForbidden, msg)
			return
		}
		writeErrorPage(w, http.StatusForbidden, msg)
	}))
	return c
}

// apiNotFound is the JSON 404 for unknown /api/v1 paths.
func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotFound, "not found")
}

// apiMethodNotAllowed is the JSON 405 for known /api/v1 paths hit with the
// wrong method. A custom chi handler loses the Allow header chi's default
// computes, so it is rebuilt by probing the router for each method.
func apiMethodNotAllowed(routes chi.Routes) http.HandlerFunc {
	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
			path = rctx.RoutePath
		}
		for _, m := range methods {
			if routes.Match(chi.NewRouteContext(), m, path) {
				w.Header().Add("Allow", m)
			}
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
