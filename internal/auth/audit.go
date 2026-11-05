// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"time"
)

// Actor identifies who performs an action, for the audit log.
type Actor struct {
	UserID int64  // 0 for the system / command line
	Name   string // username or "cli"
	IP     string
}

type actorKey struct{}

// WithActor returns a context carrying the acting user for audit entries.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor stored in ctx (zero Actor if none).
func ActorFrom(ctx context.Context) Actor {
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	ActorID int64     `json:"actorId,omitempty"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	IP      string    `json:"ip,omitempty"`
	Success bool      `json:"success"`
	Details string    `json:"details,omitempty"`
}

// Audit writes an audit log entry; the actor is taken from ctx. Failures to
// write are ignored by callers on purpose (they are returned for tests).
func (s *Service) Audit(ctx context.Context, action, target string, success bool, details string) error {
	a := ActorFrom(ctx)
	var actorID any
	if a.UserID != 0 {
		actorID = a.UserID
	}
	_, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO audit_log(at, actor_id, actor, action, target, ip, success, details) VALUES (?,?,?,?,?,?,?,?)`,
		s.now().Unix(), actorID, truncate(a.Name, 100), action, truncate(target, 320), a.IP, success, truncate(details, 1000))
	return err
}

// ListAudit returns the newest entries first; beforeID > 0 pages backwards.
func (s *Service) ListAudit(ctx context.Context, limit int, beforeID int64) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, at, COALESCE(actor_id,0), actor, action, target, ip, success, details FROM audit_log`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.ActorID, &e.Actor, &e.Action, &e.Target, &e.IP, &e.Success, &e.Details); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
