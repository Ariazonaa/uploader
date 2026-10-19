package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Audit event names — the security-relevant actions recorded in audit_log.
// token.use is deliberately NOT recorded (far too high-volume).
const (
	AuditLoginSuccess   = "login.success"
	AuditLoginFailure   = "login.failure"
	AuditLogout         = "logout"
	AuditTokenCreate    = "token.create"
	AuditTokenRevoke    = "token.revoke"
	AuditFileDelete     = "file.delete"
	AuditFilePurge      = "file.purge"
	AuditFileRestore    = "file.restore"
	AuditSettingsUpdate = "settings.update"
	AuditInviteCreate   = "invite.create"
	AuditInviteRevoke   = "invite.revoke"
	AuditUserRegister   = "user.register"
	// Account security. A deleted account's own deletion event is recorded
	// without a user id (the row is gone; see handleDeleteAccount).
	AuditPasswordChange = "password.change"
	AuditTOTPEnable     = "totp.enable"
	AuditTOTPDisable    = "totp.disable"
	AuditSessionRevoke  = "session.revoke"
	AuditAccountDelete  = "account.delete"
	AuditAccountExport  = "account.export"
	// AuditFileDeleteByKey is a file trashed through its public ShareX
	// deletion link, by whoever holds the key (no signed-in actor).
	AuditFileDeleteByKey = "file.delete_by_key"
)

// AuditEvent is a single recorded security-relevant action. UserID is nil for
// anonymous events (a failed login, an invite-based registration).
type AuditEvent struct {
	ID         uuid.UUID
	CreatedAt  time.Time
	UserID     *uuid.UUID
	ActorLabel string
	Event      string
	Target     string
	IP         string
	UserAgent  string
	Metadata   json.RawMessage
}

// AuditFilter narrows a ListAuditEvents query. A zero value returns the newest
// events across all users and event types.
type AuditFilter struct {
	Event  string
	UserID *uuid.UUID
	Limit  int
	Offset int
}

// Keep this column list in sync with scanAuditRow's Scan order.
const auditColumns = "id, created_at, user_id, actor_label, event, target, ip, user_agent, metadata"
const auditSelect = "SELECT " + auditColumns + " FROM audit_log"

func scanAuditRow(row pgx.Row) (AuditEvent, error) {
	var e AuditEvent
	err := row.Scan(&e.ID, &e.CreatedAt, &e.UserID, &e.ActorLabel, &e.Event, &e.Target, &e.IP, &e.UserAgent, &e.Metadata)
	return e, err
}

// InsertAuditEvent records a single audit event. Callers treat this as
// best-effort (see api.recordAudit): this method only performs the write.
func (s *Store) InsertAuditEvent(ctx context.Context, e AuditEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if len(e.Metadata) == 0 {
		e.Metadata = []byte("{}")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (id, user_id, actor_label, event, target, ip, user_agent, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.UserID, e.ActorLabel, e.Event, e.Target, e.IP, e.UserAgent, e.Metadata)
	return err
}

// ListAuditEvents returns a filtered page of audit events (newest first) plus
// the total number of matching rows (for pagination). Reads go to the replica.
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, int, error) {
	var args []any
	where := ""
	// add appends a filter clause and its bound value, wiring the placeholder
	// index to the argument position (mirrors ListFiles).
	add := func(clause string, val any) {
		args = append(args, val)
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += fmt.Sprintf(clause, len(args))
	}
	if f.Event != "" {
		add("event = $%d", f.Event)
	}
	if f.UserID != nil {
		add("user_id = $%d", *f.UserID)
	}

	var total int
	if err := s.reader().QueryRow(ctx, "SELECT count(*) FROM audit_log"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit)
	limitIdx := len(args)
	args = append(args, f.Offset)
	offsetIdx := len(args)
	// id breaks ties between events of the same instant; without it LIMIT/OFFSET
	// pages repeat some of them and skip others.
	query := fmt.Sprintf("%s%s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", auditSelect, where, limitIdx, offsetIdx)

	rows, err := s.reader().Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		e, err := scanAuditRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, err
	}
	return out, total, nil
}

// DeleteAuditEventsOlderThan removes audit events created before cutoff (the
// janitor's audit_retention_days window) and returns how many were deleted.
func (s *Store) DeleteAuditEventsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM audit_log WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
