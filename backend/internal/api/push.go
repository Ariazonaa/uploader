package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// pushServiceHosts are the push services browsers actually hand out endpoints
// for. The endpoint URL comes from the client, and the server POSTs to it, so
// accepting any URL made /push/subscribe + /push/test a blind SSRF primitive
// into the internal network. Exact hosts match as is; entries starting with "."
// match any subdomain.
var pushServiceHosts = []string{
	"fcm.googleapis.com",         // Chrome, Edge (Chromium), Opera, Brave
	".push.services.mozilla.com", // Firefox (updates.push.services.mozilla.com)
	".notify.windows.com",        // legacy Edge / WNS
	"web.push.apple.com",         // Safari
	".push.apple.com",            // Safari (regional hosts)
}

const (
	maxPushEndpointLen = 2048
	maxPushKeyLen      = 256
	// maxPushSubscriptionsPerUser caps the browsers one account can register.
	// Generous, because a dead subscription is only pruned once a push to it
	// fails.
	maxPushSubscriptionsPerUser = 20
	// pushRequestTimeout bounds one delivery; pushSendTimeout bounds a whole
	// fan-out, so a slow push service cannot pin the request (or a goroutine).
	pushRequestTimeout = 10 * time.Second
	pushSendTimeout    = 30 * time.Second
)

// pushHTTPClient delivers push messages. It has a hard timeout (the default
// client has none) and never follows redirects: a push service answers
// directly, and following a redirect would re-open the SSRF the endpoint
// allowlist closes.
var pushHTTPClient = &http.Client{
	Timeout: pushRequestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// validPushEndpoint reports whether raw is an https URL on a known push service
// (see pushServiceHosts) with no userinfo and the default port.
func validPushEndpoint(raw string) bool {
	if raw == "" || len(raw) > maxPushEndpointLen {
		return false
	}
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	if p := u.Port(); p != "" && p != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !validHostname(host) {
		return false
	}
	for _, allowed := range pushServiceHosts {
		if strings.HasPrefix(allowed, ".") {
			if strings.HasSuffix(host, allowed) {
				return true
			}
		} else if host == allowed {
			return true
		}
	}
	return false
}

// browserSubscription mirrors the JSON the browser's PushManager produces.
type browserSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.PushEnabled() {
		writeErr(w, http.StatusNotFound, "push is not enabled")
		return
	}
	u := auth.MustUser(r.Context())
	var req browserSubscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Endpoint == "" || req.Keys.P256dh == "" || req.Keys.Auth == "" {
		writeErr(w, http.StatusBadRequest, "incomplete subscription")
		return
	}
	if len(req.Keys.P256dh) > maxPushKeyLen || len(req.Keys.Auth) > maxPushKeyLen {
		writeErr(w, http.StatusBadRequest, "invalid subscription keys")
		return
	}
	if !validPushEndpoint(req.Endpoint) {
		writeErr(w, http.StatusBadRequest, "unsupported push service endpoint")
		return
	}
	if err := s.store.SavePushSubscription(r.Context(), store.PushSubscription{
		UserID: u.ID, Endpoint: req.Endpoint, P256dh: req.Keys.P256dh, Auth: req.Keys.Auth,
	}, maxPushSubscriptionsPerUser); err != nil {
		if errors.Is(err, store.ErrPushEndpointTaken) {
			writeErr(w, http.StatusConflict, "this push subscription is registered to another account")
			return
		}
		if errors.Is(err, store.ErrPushLimit) {
			writeErr(w, http.StatusConflict, fmt.Sprintf(
				"push subscription limit reached: an account can register at most %d browsers",
				maxPushSubscriptionsPerUser))
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not save subscription")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "subscribed"})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.PushEnabled() {
		writeErr(w, http.StatusNotFound, "push is not enabled")
		return
	}
	u := auth.MustUser(r.Context())
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Scoped to the caller: an endpoint is not a secret we can rely on, so an
	// unscoped delete would let any logged-in user unsubscribe another's device.
	_ = s.store.DeleteUserPushSubscription(r.Context(), req.Endpoint, u.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.PushEnabled() {
		writeErr(w, http.StatusNotFound, "push is not enabled")
		return
	}
	u := auth.MustUser(r.Context())
	// Every call fans out outbound HTTP requests, so bound it per user.
	if ok, retry := s.limiters().pushTest.take("u:" + u.ID.String()); !ok {
		writeRateLimited(w, retry, "rate limit exceeded")
		return
	}
	s.sendPush(r.Context(), u.ID, "Test notification", "Push notifications are working 🎉", "/")
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// sendPush delivers a notification to all of a user's subscriptions. Dead
// subscriptions (404/410) are pruned, and so are rows whose endpoint is not on
// the push-service allowlist (stored before it existed); those are never
// contacted. Best-effort: delivery errors are logged, not returned. The whole
// fan-out is bounded by pushSendTimeout and cancelled with ctx.
func (s *Server) sendPush(ctx context.Context, userID uuid.UUID, title, body, url string) {
	if !s.cfg.PushEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, pushSendTimeout)
	defer cancel()
	subs, err := s.store.ListPushSubscriptions(ctx, userID)
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": url})
	for _, sub := range subs {
		if !validPushEndpoint(sub.Endpoint) {
			_ = s.store.DeletePushSubscription(ctx, sub.Endpoint)
			continue
		}
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
		}, &webpush.Options{
			HTTPClient:      pushHTTPClient,
			Subscriber:      vapidSubscriber(s.cfg.VAPIDSubject),
			VAPIDPublicKey:  s.cfg.VAPIDPublicKey,
			VAPIDPrivateKey: s.cfg.VAPIDPrivateKey,
			TTL:             30,
		})
		if err != nil {
			log.Printf("push: deliver to user %s: %v", userID, err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			_ = s.store.DeletePushSubscription(ctx, sub.Endpoint)
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			// A rejected message is otherwise invisible: the push service says why
			// (e.g. Apple's {"reason":"BadJwtToken"}) only in the body.
			reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			log.Printf("push: deliver to user %s: %s answered %d: %q",
				userID, pushHost(sub.Endpoint), resp.StatusCode, strings.TrimSpace(string(reason)))
		}
		_ = resp.Body.Close()
	}
}

// vapidSubscriber turns UPLOADER_VAPID_SUBJECT into what webpush-go expects.
// The library prefixes everything that is not an https: URL with "mailto:",
// so the documented "mailto:admin@example.com" went out as
// "mailto:mailto:admin@example.com" -- which Apple's push service rejects, so
// Safari never received a notification.
func vapidSubscriber(subject string) string {
	subject = strings.TrimSpace(subject)
	if len(subject) >= len("mailto:") && strings.EqualFold(subject[:len("mailto:")], "mailto:") {
		return subject[len("mailto:"):]
	}
	return subject
}

// pushHost names the push service of an endpoint for logs; the full endpoint
// URL is a bearer capability for that subscription and stays out of them.
func pushHost(endpoint string) string {
	if u, err := neturl.Parse(endpoint); err == nil {
		return u.Hostname()
	}
	return "push service"
}
