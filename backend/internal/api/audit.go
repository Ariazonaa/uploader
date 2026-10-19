package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// auditEventSet is the whitelist of known audit event names. It validates the
// `event` query filter (an unknown value is ignored rather than erroring).
var auditEventSet = map[string]struct{}{
	store.AuditLoginSuccess:    {},
	store.AuditLoginFailure:    {},
	store.AuditLogout:          {},
	store.AuditTokenCreate:     {},
	store.AuditTokenRevoke:     {},
	store.AuditFileDelete:      {},
	store.AuditFilePurge:       {},
	store.AuditFileRestore:     {},
	store.AuditSettingsUpdate:  {},
	store.AuditInviteCreate:    {},
	store.AuditInviteRevoke:    {},
	store.AuditUserRegister:    {},
	store.AuditPasswordChange:  {},
	store.AuditTOTPEnable:      {},
	store.AuditTOTPDisable:     {},
	store.AuditSessionRevoke:   {},
	store.AuditAccountDelete:   {},
	store.AuditAccountExport:   {},
	store.AuditFileDeleteByKey: {},
}

const (
	// maxAuditFieldLen bounds the free-text columns of an audit row.
	maxAuditFieldLen = 256
	// maxUserAgentLen bounds the stored User-Agent (audit rows and sessions).
	maxUserAgentLen = 512
)

// clientUA returns the request's User-Agent, made storable (see cleanText).
func clientUA(r *http.Request) string {
	return cleanText(r.UserAgent(), maxUserAgentLen)
}

// cleanText makes a client-supplied string safe to store in a Postgres TEXT
// column: invalid UTF-8 is replaced with U+FFFD and NUL bytes are dropped --
// Postgres rejects both, which made the audit INSERT fail and the event vanish
// (recordAudit is best-effort) -- and the result is truncated to n runes.
func cleanText(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	return truncate(s, n)
}

// cleanMetadata applies cleanText to every string inside audit metadata. JSONB
// rejects the \u0000 escape json.Marshal emits for a NUL, and metadata can
// carry user input (a token name, for example).
func cleanMetadata(v any) any {
	switch t := v.(type) {
	case string:
		return cleanText(t, maxAuditFieldLen)
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = cleanText(e, maxAuditFieldLen)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cleanMetadata(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[cleanText(k, maxAuditFieldLen)] = cleanMetadata(e)
		}
		return out
	default:
		return v
	}
}

// truncate shortens s to at most n runes (never splitting one in half).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// recordAudit writes a single audit-log row. It is strictly best-effort: it
// never propagates an error and never blocks the request beyond a short bound,
// so recording an action can never break the action itself. Secrets (passwords,
// tokens, TOTP codes) must never be passed in — only key names / prefixes.
func (s *Server) recordAudit(r *http.Request, userID *uuid.UUID, actorLabel, event, target string, metadata map[string]any) {
	var raw json.RawMessage
	if len(metadata) > 0 {
		if b, err := json.Marshal(cleanMetadata(metadata)); err != nil {
			log.Printf("audit: marshal metadata for %s: %v", event, err)
		} else {
			raw = b
		}
	}
	// Detach from the request's cancellation so the row is still written if the
	// client disconnects right after the mutation, but cap the work at 3s.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	// Actor label and target can come straight from a request body (the username
	// on a failed login, for instance) and the User-Agent is capped only by Go's
	// 1 MB header limit. Truncate before persisting so an unauthenticated caller
	// cannot inflate the audit table a megabyte per attempt, and sanitise them so
	// a non-UTF-8 or NUL-carrying value cannot make the INSERT fail.
	if err := s.store.InsertAuditEvent(ctx, store.AuditEvent{
		UserID:     userID,
		ActorLabel: cleanText(actorLabel, maxAuditFieldLen),
		Event:      event,
		Target:     cleanText(target, maxAuditFieldLen),
		IP:         s.clientIP(r),
		UserAgent:  clientUA(r),
		Metadata:   raw,
	}); err != nil {
		log.Printf("audit: record %s: %v", event, err)
	}
}

// audit records an event attributed to the authenticated user in context (if
// any). Use it from handlers that run behind auth.Require.
func (s *Server) audit(r *http.Request, event, target string, metadata map[string]any) {
	var userID *uuid.UUID
	actorLabel := ""
	if u, ok := auth.UserFrom(r.Context()); ok {
		id := u.ID
		userID = &id
		actorLabel = u.Username
	}
	s.recordAudit(r, userID, actorLabel, event, target, metadata)
}

// auditAnon records an event with no attributed user (login failures, invite
// registration) but keeps a human-readable actor label (e.g. the username that
// was attempted).
func (s *Server) auditAnon(r *http.Request, event, actorLabel, target string, metadata map[string]any) {
	s.recordAudit(r, nil, actorLabel, event, target, metadata)
}

type auditView struct {
	ID         uuid.UUID       `json:"id"`
	CreatedAt  time.Time       `json:"created_at"`
	UserID     *uuid.UUID      `json:"user_id"`
	ActorLabel string          `json:"actor_label"`
	Event      string          `json:"event"`
	Target     string          `json:"target"`
	IP         string          `json:"ip"`
	UserAgent  string          `json:"user_agent"`
	Metadata   json.RawMessage `json:"metadata"`
}

// handleListAudit returns a paginated, optionally filtered view of the audit
// log (admin only).
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := clampInt(atoiDefault(q.Get("limit"), 50), 1, 200)
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	f := store.AuditFilter{Limit: limit, Offset: offset}
	if ev := q.Get("event"); ev != "" {
		if _, ok := auditEventSet[ev]; ok {
			f.Event = ev
		}
	}
	if uid := q.Get("user"); uid != "" {
		if id, err := uuid.Parse(uid); err == nil {
			f.UserID = &id
		}
	}

	events, total, err := s.store.ListAuditEvents(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list audit log")
		return
	}
	views := make([]auditView, 0, len(events))
	for _, e := range events {
		md := e.Metadata
		if len(md) == 0 {
			md = json.RawMessage("{}")
		}
		views = append(views, auditView{
			ID: e.ID, CreatedAt: e.CreatedAt, UserID: e.UserID, ActorLabel: e.ActorLabel,
			Event: e.Event, Target: e.Target, IP: e.IP, UserAgent: e.UserAgent, Metadata: md,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": views,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
