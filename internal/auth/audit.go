// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"log/slog"
	"strings"
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

// Audit writes an audit log entry; the actor is taken from ctx. A failure to
// write is an error (Basis spec/BETRIEB.md): it is returned and always logged,
// since most callers go on with the action.
func (s *Service) Audit(ctx context.Context, action, target string, success bool, details string) error {
	a := ActorFrom(ctx)
	var actorID any
	if a.UserID != 0 {
		actorID = a.UserID
	}
	_, err := s.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO audit_log(at, actor_id, actor, action, target, ip, success, details) VALUES (?,?,?,?,?,?,?,?)`,
		s.now().Unix(), actorID, truncate(a.Name, 100), action, truncate(target, 320), a.IP, success, truncate(details, 1000))
	if err != nil {
		slog.Error("auth: audit entry not written", "action", action, "err", err)
	}
	return err
}

// ListAudit returns the newest entries first; beforeID > 0 pages backwards.
func (s *Service) ListAudit(ctx context.Context, limit int, beforeID int64) ([]AuditEntry, error) {
	return s.QueryAudit(ctx, AuditFilter{Limit: limit, BeforeID: beforeID})
}

// AuditFilter narrows QueryAudit. Empty fields match everything.
type AuditFilter struct {
	Limit    int   // 1..1000, default 100
	BeforeID int64 // > 0 pages backwards (entries with a smaller id)
	// Action and Actor match as case-insensitive prefixes ("user." lists all
	// user actions, "al" all actors starting with al).
	Action string
	Actor  string
	// OnlyFailed lists unsuccessful actions only.
	OnlyFailed bool
}

// likePrefix turns a literal prefix into a LIKE pattern (use with ESCAPE '\').
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p) + "%"
}

// QueryAudit returns the newest entries first that match the filter.
func (s *Service) QueryAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, at, COALESCE(actor_id,0), actor, action, target, ip, success, details FROM audit_log WHERE 1=1`
	args := []any{}
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	if a := strings.TrimSpace(f.Action); a != "" {
		q += ` AND action LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(truncate(a, 100)))
	}
	if a := strings.TrimSpace(f.Actor); a != "" {
		q += ` AND actor LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(truncate(a, 100)))
	}
	if f.OnlyFailed {
		q += ` AND success = 0`
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
