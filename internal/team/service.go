// SPDX-License-Identifier: AGPL-3.0-or-later

package team

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DirUser is a user of the server as seen by the team service.
type DirUser struct {
	ID          int64
	Username    string
	DisplayName string
	Admin       bool
	Disabled    bool
}

// Name returns the display name or the username.
func (u DirUser) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Directory resolves users and groups (implemented by the server on top of
// internal/auth).
type Directory interface {
	User(ctx context.Context, id int64) (DirUser, error)
	Users(ctx context.Context) ([]DirUser, error)
	UserGroupIDs(ctx context.Context, userID int64) ([]int64, error)
	GroupName(ctx context.Context, groupID int64) (string, error)
	GroupMembers(ctx context.Context, groupID int64) ([]int64, error)
}

// Auditor writes an audit log entry (auth.Service.Audit with the actor).
type Auditor func(ctx context.Context, actor Actor, action, target, details string)

// Service is the team mailbox service. It is safe for concurrent use.
type Service struct {
	db    *sql.DB
	dir   Directory
	audit Auditor
	now   func() time.Time

	presence *PresenceTracker

	mu        sync.RWMutex
	listeners []func(Event)
	rrMu      sync.Mutex
	reads     sync.Map // [user, mailbox, conversation] -> time of the last "team.read" audit entry
}

// Options configure a Service.
type Options struct {
	Audit       Auditor
	Now         func() time.Time
	PresenceTTL time.Duration
}

// New creates the service on the server database (tables of migration
// 0003_team.sql).
func New(db *sql.DB, dir Directory, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Service{db: db, dir: dir, audit: opts.Audit, now: opts.Now}
	s.presence = NewPresenceTracker(opts.PresenceTTL, opts.Now)
	return s
}

// Subscribe registers a listener for collaboration events. Listeners are
// called synchronously and must not block.
func (s *Service) Subscribe(fn func(Event)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Service) emit(ev Event) {
	s.mu.RLock()
	ls := append([]func(Event){}, s.listeners...)
	s.mu.RUnlock()
	for _, fn := range ls {
		fn(ev)
	}
}

func (s *Service) auditf(ctx context.Context, a Actor, action string, mailboxID, convID int64, details string) {
	if s.audit == nil {
		return
	}
	target := fmt.Sprintf("team=%d", mailboxID)
	if convID != 0 {
		target += fmt.Sprintf(" conversation=%d", convID)
	}
	s.audit(ctx, a, action, target, details)
}

func (s *Service) unix() int64 { return s.now().Unix() }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ==== Mailboxes ===============================================================

func (in *MailboxInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	if in.Name == "" || len(in.Name) > 100 {
		return fmt.Errorf("%w: name must be 1-100 characters", ErrInvalidInput)
	}
	if len(in.Address) > 320 || len(in.Description) > 1000 {
		return fmt.Errorf("%w: address or description too long", ErrInvalidInput)
	}
	if in.Kind == "" {
		in.Kind = KindMail
	}
	if in.Kind != KindMail && in.Kind != KindMessenger {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidInput, in.Kind)
	}
	return nil
}

const mailboxCols = `id, name, address, description, kind, shared_drafts, disabled, created_at, updated_at`

func scanMailbox(sc interface{ Scan(...any) error }) (Mailbox, error) {
	var m Mailbox
	var c, u int64
	err := sc.Scan(&m.ID, &m.Name, &m.Address, &m.Description, &m.Kind, &m.SharedDrafts, &m.Disabled, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.CreatedAt, m.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
	return m, err
}

// CreateMailbox creates a team mailbox (metadata only; the server adds the
// account to the team store).
func (s *Service) CreateMailbox(ctx context.Context, a Actor, in MailboxInput) (Mailbox, error) {
	if err := in.normalize(); err != nil {
		return Mailbox{}, err
	}
	var by any
	if a.UserID != 0 {
		by = a.UserID
	}
	now := s.unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO team_mailboxes(name, address, description, kind, shared_drafts, disabled,
		created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		in.Name, in.Address, in.Description, in.Kind, in.SharedDrafts, in.Disabled, by, now, now)
	if isUnique(err) {
		return Mailbox{}, fmt.Errorf("%w: mailbox %q", ErrExists, in.Name)
	}
	if err != nil {
		return Mailbox{}, err
	}
	id, _ := res.LastInsertId()
	s.auditf(ctx, a, "team.mailbox.create", id, 0, in.Name)
	s.emit(Event{Kind: EventMailbox, MailboxID: id, ActorID: a.UserID})
	return s.GetMailbox(ctx, id)
}

// UpdateMailbox changes a mailbox.
func (s *Service) UpdateMailbox(ctx context.Context, a Actor, id int64, in MailboxInput) (Mailbox, error) {
	if err := in.normalize(); err != nil {
		return Mailbox{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE team_mailboxes SET name = ?, address = ?, description = ?, kind = ?,
		shared_drafts = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		in.Name, in.Address, in.Description, in.Kind, in.SharedDrafts, in.Disabled, s.unix(), id)
	if isUnique(err) {
		return Mailbox{}, fmt.Errorf("%w: mailbox %q", ErrExists, in.Name)
	}
	if err != nil {
		return Mailbox{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Mailbox{}, ErrNotFound
	}
	s.auditf(ctx, a, "team.mailbox.update", id, 0, in.Name)
	s.emit(Event{Kind: EventMailbox, MailboxID: id, ActorID: a.UserID})
	return s.GetMailbox(ctx, id)
}

// DeleteMailbox deletes a mailbox with all collaboration data. The server
// removes the team store.
func (s *Service) DeleteMailbox(ctx context.Context, a Actor, id int64) error {
	m, err := s.GetMailbox(ctx, id)
	if err != nil {
		return err
	}
	// Members must be computed before the grants are gone.
	members, _ := s.memberIDs(ctx, id)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM team_mailboxes WHERE id = ?`, id); err != nil {
		return err
	}
	s.auditf(ctx, a, "team.mailbox.delete", id, 0, m.Name)
	s.emit(Event{Kind: EventMailbox, MailboxID: id, ActorID: a.UserID, UserIDs: members})
	return nil
}

// GetMailbox returns a mailbox.
func (s *Service) GetMailbox(ctx context.Context, id int64) (Mailbox, error) {
	return scanMailbox(s.db.QueryRowContext(ctx, `SELECT `+mailboxCols+` FROM team_mailboxes WHERE id = ?`, id))
}

// ListMailboxes returns all mailboxes (administration).
func (s *Service) ListMailboxes(ctx context.Context) ([]Mailbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mailboxCols+` FROM team_mailboxes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mailbox{}
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ==== Grants and roles ========================================================

// SetGrant gives a user (userID) or a group (groupID) a role; RoleNone
// removes the grant.
func (s *Service) SetGrant(ctx context.Context, a Actor, mailboxID, userID, groupID int64, role Role) error {
	if (userID == 0) == (groupID == 0) {
		return fmt.Errorf("%w: exactly one of user and group is required", ErrInvalidInput)
	}
	if role != RoleNone && !role.Valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	if _, err := s.GetMailbox(ctx, mailboxID); err != nil {
		return err
	}
	col, id := "user_id", userID
	if groupID != 0 {
		col, id = "group_id", groupID
	}
	before, _ := s.memberIDs(ctx, mailboxID)
	if role == RoleNone {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM team_grants WHERE mailbox_id = ? AND `+col+` = ?`, mailboxID, id); err != nil {
			return err
		}
	} else {
		_, err := s.db.ExecContext(ctx, `INSERT INTO team_grants(mailbox_id, `+col+`, role, created_at) VALUES (?,?,?,?)
			ON CONFLICT DO UPDATE SET role = excluded.role`, mailboxID, id, string(role), s.unix())
		if err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return ErrNotFound
			}
			return err
		}
	}
	s.auditf(ctx, a, "team.grant", mailboxID, 0, fmt.Sprintf("%s=%d role=%s", strings.TrimSuffix(col, "_id"), id, role))
	after, _ := s.memberIDs(ctx, mailboxID)
	s.emit(Event{Kind: EventMailbox, MailboxID: mailboxID, ActorID: a.UserID, UserIDs: unionIDs(before, after)})
	return nil
}

// ListGrants returns the grants of a mailbox with resolved names.
func (s *Service) ListGrants(ctx context.Context, mailboxID int64) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(user_id,0), COALESCE(group_id,0), role FROM team_grants
		WHERE mailbox_id = ? ORDER BY group_id IS NULL, role DESC`, mailboxID)
	if err != nil {
		return nil, err
	}
	var out []Grant
	for rows.Next() {
		g := Grant{MailboxID: mailboxID}
		if err := rows.Scan(&g.UserID, &g.GroupID, &g.Role); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].UserID != 0 {
			if u, err := s.dir.User(ctx, out[i].UserID); err == nil {
				out[i].Name = u.Username
			}
		} else if n, err := s.dir.GroupName(ctx, out[i].GroupID); err == nil {
			out[i].Name = n
		}
	}
	if out == nil {
		out = []Grant{}
	}
	return out, nil
}

// RoleOf returns the effective role of a user on a mailbox: the highest of
// the direct grant and the grants of the user's groups. Server
// administrators manage every mailbox; disabled users and disabled
// mailboxes have no access (administrators still do, to repair them).
func (s *Service) RoleOf(ctx context.Context, userID, mailboxID int64) (Role, error) {
	u, err := s.dir.User(ctx, userID)
	if err != nil || u.Disabled {
		return RoleNone, nil
	}
	m, err := s.GetMailbox(ctx, mailboxID)
	if errors.Is(err, ErrNotFound) {
		return RoleNone, nil
	}
	if err != nil {
		return RoleNone, err
	}
	if u.Admin {
		return RoleManage, nil
	}
	if m.Disabled {
		return RoleNone, nil
	}
	roles, err := s.userRoles(ctx, u.ID)
	if err != nil {
		return RoleNone, err
	}
	return roles[mailboxID], nil
}

// userRoles returns the effective role per mailbox from grants (no admin rule).
func (s *Service) userRoles(ctx context.Context, userID int64) (map[int64]Role, error) {
	groups, err := s.dir.UserGroupIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	q := `SELECT mailbox_id, role FROM team_grants WHERE user_id = ?`
	args := []any{userID}
	if len(groups) > 0 {
		q += ` OR group_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(groups)), ",") + `)`
		for _, g := range groups {
			args = append(args, g)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Role{}
	for rows.Next() {
		var mid int64
		var r Role
		if err := rows.Scan(&mid, &r); err != nil {
			return nil, err
		}
		out[mid] = Max(out[mid], r)
	}
	return out, rows.Err()
}

// Require returns ErrForbidden unless the user has at least role need.
// Users without any access get ErrNotFound so mailbox ids do not leak.
func (s *Service) Require(ctx context.Context, userID, mailboxID int64, need Role) (Role, error) {
	r, err := s.RoleOf(ctx, userID, mailboxID)
	if err != nil {
		return RoleNone, err
	}
	if r == RoleNone {
		return RoleNone, ErrNotFound
	}
	if !r.Allows(need) {
		return r, ErrForbidden
	}
	return r, nil
}

// Accessible returns the mailboxes the user can see, with role and counters.
func (s *Service) Accessible(ctx context.Context, userID int64) ([]Access, error) {
	u, err := s.dir.User(ctx, userID)
	if err != nil || u.Disabled {
		return []Access{}, nil
	}
	all, err := s.ListMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := s.userRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []Access{}
	for _, m := range all {
		r := roles[m.ID]
		if u.Admin {
			r = RoleManage
		} else if m.Disabled {
			r = RoleNone
		}
		if r == RoleNone {
			continue
		}
		a := Access{Mailbox: m, Role: r}
		_ = s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(assignee_id = ? AND status <> 'done'), 0),
			COALESCE(SUM(assignee_id IS NULL AND status <> 'done'), 0),
			COALESCE(SUM(status <> 'done'), 0)
			FROM team_conversations WHERE mailbox_id = ?`, userID, m.ID).Scan(&a.Mine, &a.Unassigned, &a.Open)
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_mentions n JOIN team_comments c ON c.id = n.comment_id
			WHERE n.user_id = ? AND n.read_at IS NULL AND c.mailbox_id = ?`, userID, m.ID).Scan(&a.Mentions)
		out = append(out, a)
	}
	return out, nil
}

// memberIDs returns the ids of all users with a grant (directly or through
// a group), without administrators that have no grant.
func (s *Service) memberIDs(ctx context.Context, mailboxID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(user_id,0), COALESCE(group_id,0) FROM team_grants WHERE mailbox_id = ?`, mailboxID)
	if err != nil {
		return nil, err
	}
	var users, groups []int64
	for rows.Next() {
		var u, g int64
		if err := rows.Scan(&u, &g); err != nil {
			rows.Close()
			return nil, err
		}
		if u != 0 {
			users = append(users, u)
		} else {
			groups = append(groups, g)
		}
	}
	rows.Close()
	for _, g := range groups {
		ids, err := s.dir.GroupMembers(ctx, g)
		if err != nil {
			return nil, err
		}
		users = append(users, ids...)
	}
	return unionIDs(users), nil
}

// Members returns all users with access to a mailbox (including
// administrators), sorted by name.
func (s *Service) Members(ctx context.Context, mailboxID int64) ([]Member, error) {
	users, err := s.dir.Users(ctx)
	if err != nil {
		return nil, err
	}
	out := []Member{}
	for _, u := range users {
		r, err := s.RoleOf(ctx, u.ID, mailboxID)
		if err != nil {
			return nil, err
		}
		if r == RoleNone {
			continue
		}
		out = append(out, Member{UserID: u.ID, Username: u.Username, DisplayName: u.Name(), Role: r})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
	return out, nil
}

// AudienceIDs returns the ids of all users who can see the mailbox; used to
// fan out events.
func (s *Service) AudienceIDs(ctx context.Context, mailboxID int64) ([]int64, error) {
	ms, err := s.Members(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.UserID
	}
	return out, nil
}

func unionIDs(lists ...[]int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, l := range lists {
		for _, id := range l {
			if id != 0 && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
