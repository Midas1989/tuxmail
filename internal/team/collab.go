// SPDX-License-Identifier: AGPL-3.0-or-later

package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ==== Assignments =============================================================

const assignmentCols = `a.mailbox_id, a.conversation_id, COALESCE(a.assignee_id,0), a.status, a.priority, a.due_at, a.opened_at,
	a.first_response_at, a.done_at, a.updated_at, COALESCE(a.updated_by,0),
	(SELECT COUNT(*) FROM team_comments c WHERE c.mailbox_id = a.mailbox_id AND c.conversation_id = a.conversation_id)`

func ptime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

func scanAssignment(sc interface{ Scan(...any) error }) (Assignment, error) {
	var a Assignment
	var due, first, done sql.NullInt64
	var opened, updated int64
	err := sc.Scan(&a.MailboxID, &a.ConversationID, &a.AssigneeID, &a.Status, &a.Priority, &due, &opened, &first, &done,
		&updated, &a.UpdatedBy, &a.Comments)
	if err != nil {
		return a, err
	}
	a.DueAt, a.FirstResponseAt, a.DoneAt = ptime(due), ptime(first), ptime(done)
	a.OpenedAt, a.UpdatedAt = time.Unix(opened, 0).UTC(), time.Unix(updated, 0).UTC()
	return a, nil
}

func (s *Service) defaultAssignment(mailboxID, convID int64) Assignment {
	now := s.now().UTC().Truncate(time.Second)
	return Assignment{MailboxID: mailboxID, ConversationID: convID, Status: StatusOpen, Priority: PriorityNormal, OpenedAt: now, UpdatedAt: now}
}

func (s *Service) resolveNames(ctx context.Context, as []Assignment) {
	cache := map[int64]string{}
	for i := range as {
		id := as[i].AssigneeID
		if id == 0 {
			continue
		}
		n, ok := cache[id]
		if !ok {
			if u, err := s.dir.User(ctx, id); err == nil {
				n = u.Name()
			}
			cache[id] = n
		}
		as[i].AssigneeName = n
	}
}

// Assignment returns the state of a conversation (defaults if none is stored).
func (s *Service) Assignment(ctx context.Context, mailboxID, convID int64) (Assignment, error) {
	a, err := scanAssignment(s.db.QueryRowContext(ctx, `SELECT `+assignmentCols+` FROM team_conversations a
		WHERE a.mailbox_id = ? AND a.conversation_id = ?`, mailboxID, convID))
	if errors.Is(err, sql.ErrNoRows) {
		a = s.defaultAssignment(mailboxID, convID)
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_comments WHERE mailbox_id = ? AND conversation_id = ?`,
			mailboxID, convID).Scan(&a.Comments)
		return a, nil
	}
	if err != nil {
		return a, err
	}
	l := []Assignment{a}
	s.resolveNames(ctx, l)
	return l[0], nil
}

// Assignments returns the stored states of the given conversations (missing
// ones are omitted; callers treat them as open/unassigned).
func (s *Service) Assignments(ctx context.Context, mailboxID int64, convIDs []int64) (map[int64]Assignment, error) {
	out := map[int64]Assignment{}
	for len(convIDs) > 0 {
		n := min(len(convIDs), 500)
		chunk := convIDs[:n]
		convIDs = convIDs[n:]
		args := []any{mailboxID}
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+assignmentCols+` FROM team_conversations a WHERE a.mailbox_id = ?
			AND a.conversation_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		var list []Assignment
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			list = append(list, a)
		}
		rows.Close()
		s.resolveNames(ctx, list)
		for _, a := range list {
			out[a.ConversationID] = a
		}
	}
	return out, nil
}

// ParseDue parses a due date (RFC 3339, "2006-01-02T15:04" or a date, which
// means the end of that day in UTC).
func ParseDue(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04"} {
		if t, err := time.Parse(l, v); err == nil {
			return t.UTC(), nil
		}
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Add(24*time.Hour - time.Second).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: invalid due date %q", ErrInvalidInput, v)
}

// Update changes the assignment of a conversation. role is the actor's
// role on the mailbox: with RoleReply a user may only take unassigned
// conversations (or his own) and change their status; reassigning others
// needs RoleAssign.
func (s *Service) Update(ctx context.Context, a Actor, role Role, mailboxID, convID int64, p AssignmentPatch) (Assignment, error) {
	if !role.Allows(RoleReply) {
		return Assignment{}, ErrForbidden
	}
	cur, err := s.Assignment(ctx, mailboxID, convID)
	if err != nil {
		return Assignment{}, err
	}
	next := cur
	if p.AssigneeID != nil {
		next.AssigneeID = *p.AssigneeID
	}
	if p.Status != nil {
		if !p.Status.Valid() {
			return Assignment{}, fmt.Errorf("%w: unknown status %q", ErrInvalidInput, *p.Status)
		}
		next.Status = *p.Status
	}
	if p.Priority != nil {
		if !validPriority(*p.Priority) {
			return Assignment{}, fmt.Errorf("%w: unknown priority %q", ErrInvalidInput, *p.Priority)
		}
		next.Priority = *p.Priority
	}
	if p.DueAt != nil {
		if *p.DueAt == "" {
			next.DueAt = nil
		} else {
			t, err := ParseDue(*p.DueAt)
			if err != nil {
				return Assignment{}, err
			}
			next.DueAt = &t
		}
	}
	if !role.Allows(RoleAssign) {
		mine := cur.AssigneeID == 0 || cur.AssigneeID == a.UserID
		if !mine || (next.AssigneeID != cur.AssigneeID && next.AssigneeID != a.UserID && next.AssigneeID != 0) {
			return Assignment{}, ErrForbidden
		}
	}
	if next.AssigneeID != 0 && next.AssigneeID != cur.AssigneeID {
		r, err := s.RoleOf(ctx, next.AssigneeID, mailboxID)
		if err != nil {
			return Assignment{}, err
		}
		if !r.Allows(RoleReply) {
			return Assignment{}, fmt.Errorf("%w: user %d cannot work on this mailbox", ErrInvalidInput, next.AssigneeID)
		}
	}
	return s.store(ctx, a, cur, next)
}

// store persists next and emits the events; it is shared by Update and
// the workflow action.
func (s *Service) store(ctx context.Context, a Actor, cur, next Assignment) (Assignment, error) {
	now := s.now().UTC().Truncate(time.Second)
	switch {
	case next.Status == StatusDone && cur.Status != StatusDone:
		next.DoneAt = &now
	case next.Status != StatusDone && cur.Status == StatusDone:
		next.DoneAt = nil
		next.OpenedAt = now // reopened: response time counts again
		next.FirstResponseAt = nil
	}
	if next.Status == StatusOpen && next.AssigneeID != 0 && cur.AssigneeID == 0 && cur.Status == StatusOpen {
		next.Status = StatusInProgress
	}
	var assignee, due, first, done, by any
	if next.AssigneeID != 0 {
		assignee = next.AssigneeID
	}
	if next.DueAt != nil {
		due = next.DueAt.Unix()
	}
	if next.FirstResponseAt != nil {
		first = next.FirstResponseAt.Unix()
	}
	if next.DoneAt != nil {
		done = next.DoneAt.Unix()
	}
	if a.UserID != 0 {
		by = a.UserID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_conversations(mailbox_id, conversation_id, assignee_id, status, priority,
		due_at, opened_at, first_response_at, done_at, updated_by, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(mailbox_id, conversation_id) DO UPDATE SET assignee_id = excluded.assignee_id, status = excluded.status,
		priority = excluded.priority, due_at = excluded.due_at, opened_at = excluded.opened_at,
		first_response_at = excluded.first_response_at, done_at = excluded.done_at, updated_by = excluded.updated_by,
		updated_at = excluded.updated_at`,
		next.MailboxID, next.ConversationID, assignee, string(next.Status), next.Priority, due, next.OpenedAt.Unix(), first, done, by, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return Assignment{}, ErrNotFound
		}
		return Assignment{}, err
	}
	out, err := s.Assignment(ctx, next.MailboxID, next.ConversationID)
	if err != nil {
		return Assignment{}, err
	}
	if out.AssigneeID != cur.AssigneeID {
		s.auditf(ctx, a, "team.assigned", out.MailboxID, out.ConversationID, fmt.Sprintf("assignee=%d previous=%d", out.AssigneeID, cur.AssigneeID))
		var to []int64
		if out.AssigneeID != 0 {
			to = []int64{out.AssigneeID}
		}
		s.emit(Event{Kind: EventAssigned, MailboxID: out.MailboxID, ConversationID: out.ConversationID, ActorID: a.UserID,
			ActorName: a.Name, UserIDs: to, Assignment: &out, PreviousAssigneeID: cur.AssigneeID})
	}
	if out.Status != cur.Status || out.Priority != cur.Priority || !sameTime(out.DueAt, cur.DueAt) {
		s.auditf(ctx, a, "team.status", out.MailboxID, out.ConversationID, fmt.Sprintf("status=%s priority=%s", out.Status, out.Priority))
		s.emit(Event{Kind: EventUpdated, MailboxID: out.MailboxID, ConversationID: out.ConversationID, ActorID: a.UserID,
			ActorName: a.Name, Assignment: &out})
	}
	return out, nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// Touch makes sure a conversation has a row (called when new mail arrives,
// so that "opened_at" is the arrival time). A conversation that was done is
// reopened when the customer writes again.
func (s *Service) Touch(ctx context.Context, mailboxID, convID int64, reopen bool) error {
	cur, err := s.Assignment(ctx, mailboxID, convID)
	if err != nil {
		return err
	}
	var exists int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_conversations WHERE mailbox_id = ? AND conversation_id = ?`,
		mailboxID, convID).Scan(&exists)
	if exists > 0 && !(reopen && cur.Status == StatusDone) {
		return nil
	}
	next := cur
	if reopen && cur.Status == StatusDone {
		next.Status = StatusOpen
	}
	_, err = s.store(ctx, Actor{Name: "system"}, cur, next)
	return err
}

// RecordReply marks the first response of the team and writes the audit
// entry "team.replied".
func (s *Service) RecordReply(ctx context.Context, a Actor, mailboxID, convID int64) error {
	cur, err := s.Assignment(ctx, mailboxID, convID)
	if err != nil {
		return err
	}
	s.auditf(ctx, a, "team.replied", mailboxID, convID, "")
	if cur.FirstResponseAt != nil {
		return nil
	}
	next := cur
	now := s.now().UTC().Truncate(time.Second)
	next.FirstResponseAt = &now
	if next.Status == StatusOpen {
		next.Status = StatusWaiting
	}
	_, err = s.store(ctx, a, cur, next)
	return err
}

// readAuditInterval suppresses repeated "team.read" entries of the same
// user and conversation (the UI reloads conversations often).
const readAuditInterval = 10 * time.Minute

// RecordRead writes the audit entry "team.read" (KONZEPT 8.14: who read
// what), at most once per readAuditInterval and user/conversation.
func (s *Service) RecordRead(ctx context.Context, a Actor, mailboxID, convID int64) {
	k := [3]int64{a.UserID, mailboxID, convID}
	now := s.now()
	if v, ok := s.reads.Load(k); ok && now.Sub(v.(time.Time)) < readAuditInterval {
		return
	}
	s.reads.Store(k, now)
	s.auditf(ctx, a, "team.read", mailboxID, convID, "")
}

// Actor returns the actor for a user (display name resolved).
func (s *Service) Actor(ctx context.Context, userID int64) Actor {
	a := Actor{UserID: userID}
	if u, err := s.dir.User(ctx, userID); err == nil {
		a.Name = u.Name()
	}
	return a
}

// Username returns the login name of a user ("" if unknown).
func (s *Service) Username(ctx context.Context, userID int64) string {
	if u, err := s.dir.User(ctx, userID); err == nil {
		return u.Username
	}
	return ""
}

// ViewIDs returns the conversations matching a view that is defined by the
// assignment state (mine, done). ok=false for views that need the team
// store (all, open, unassigned include conversations without a row).
func (s *Service) ViewIDs(ctx context.Context, userID, mailboxID int64, view string) (ids []int64, ok bool, err error) {
	var q string
	args := []any{mailboxID}
	switch view {
	case ViewMine:
		q, args = `assignee_id = ? AND status <> 'done'`, append(args, userID)
	case ViewDone:
		q = `status = 'done'`
	default:
		return nil, false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_id FROM team_conversations WHERE mailbox_id = ? AND `+q+
		` ORDER BY updated_at DESC`, args...)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	ids = []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, true, err
		}
		ids = append(ids, id)
	}
	return ids, true, rows.Err()
}

// MatchesView reports whether a conversation with the given state (nil =
// no row) belongs to a view.
func MatchesView(view string, userID int64, a *Assignment) bool {
	st, assignee := StatusOpen, int64(0)
	if a != nil {
		st, assignee = a.Status, a.AssigneeID
	}
	switch view {
	case ViewMine:
		return assignee == userID && st != StatusDone
	case ViewUnassigned:
		return assignee == 0 && st != StatusDone
	case ViewDone:
		return st == StatusDone
	case ViewOpen:
		return st != StatusDone
	}
	return true
}

// ==== Comments and mentions ===================================================

var mentionRe = regexp.MustCompile(`(?:^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]{0,63})`)

// ParseMentions returns the usernames mentioned as @name in body.
func ParseMentions(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		n := strings.ToLower(strings.TrimRight(m[1], "._-"))
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

const maxCommentLen = 10000

// AddComment adds an internal comment. Mentioned users (@username) that
// have access to the mailbox get a mention (EventMention).
func (s *Service) AddComment(ctx context.Context, a Actor, role Role, mailboxID, convID int64, body string) (Comment, error) {
	if !role.Allows(RoleReply) {
		return Comment{}, ErrForbidden
	}
	body = strings.TrimSpace(body)
	if body == "" || len(body) > maxCommentLen {
		return Comment{}, fmt.Errorf("%w: comment must be 1-%d characters", ErrInvalidInput, maxCommentLen)
	}
	mentions, err := s.resolveMentions(ctx, mailboxID, body, a.UserID)
	if err != nil {
		return Comment{}, err
	}
	var author any
	if a.UserID != 0 {
		author = a.UserID
	}
	now := s.now().UTC().Truncate(time.Second)
	var id int64
	err = withTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO team_comments(mailbox_id, conversation_id, author_id, author_name, body, created_at)
			VALUES (?,?,?,?,?,?)`, mailboxID, convID, author, a.Name, body, now.Unix())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		for _, uid := range mentions {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO team_mentions(comment_id, user_id) VALUES (?,?)`, id, uid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Comment{}, err
	}
	c := Comment{ID: id, MailboxID: mailboxID, ConversationID: convID, AuthorID: a.UserID, AuthorName: a.Name, Body: body,
		Mentions: nonNil(mentions), CreatedAt: now}
	s.auditf(ctx, a, "team.comment", mailboxID, convID, fmt.Sprintf("comment=%d mentions=%v", id, mentions))
	s.emit(Event{Kind: EventComment, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name, Comment: &c})
	if len(mentions) > 0 {
		s.emit(Event{Kind: EventMention, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name,
			UserIDs: mentions, Comment: &c})
	}
	return c, nil
}

func nonNil(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// resolveMentions maps @usernames to members (excluding the author).
func (s *Service) resolveMentions(ctx context.Context, mailboxID int64, body string, authorID int64) ([]int64, error) {
	names := ParseMentions(body)
	if len(names) == 0 {
		return nil, nil
	}
	members, err := s.Members(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	by := map[string]int64{}
	for _, m := range members {
		by[strings.ToLower(m.Username)] = m.UserID
	}
	var out []int64
	for _, n := range names {
		if id, ok := by[n]; ok && id != authorID {
			out = append(out, id)
		}
	}
	return unionIDs(out), nil
}

// Comments returns the comments of a conversation, oldest first.
func (s *Service) Comments(ctx context.Context, mailboxID, convID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(author_id,0), author_name, body, created_at, edited_at
		FROM team_comments WHERE mailbox_id = ? AND conversation_id = ? ORDER BY id`, mailboxID, convID)
	if err != nil {
		return nil, err
	}
	out := []Comment{}
	for rows.Next() {
		c := Comment{MailboxID: mailboxID, ConversationID: convID, Mentions: []int64{}}
		var at int64
		var ed sql.NullInt64
		if err := rows.Scan(&c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &at, &ed); err != nil {
			rows.Close()
			return nil, err
		}
		c.CreatedAt, c.EditedAt = time.Unix(at, 0).UTC(), ptime(ed)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		r, err := s.db.QueryContext(ctx, `SELECT user_id FROM team_mentions WHERE comment_id = ? ORDER BY user_id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for r.Next() {
			var uid int64
			if err := r.Scan(&uid); err == nil {
				out[i].Mentions = append(out[i].Mentions, uid)
			}
		}
		r.Close()
	}
	return out, nil
}

// DeleteComment removes a comment; authors may delete their own, managers
// every comment.
func (s *Service) DeleteComment(ctx context.Context, a Actor, role Role, mailboxID, commentID int64) error {
	var author, convID int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(author_id,0), conversation_id FROM team_comments WHERE id = ? AND mailbox_id = ?`,
		commentID, mailboxID).Scan(&author, &convID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !(role.Allows(RoleManage) || (author == a.UserID && role.Allows(RoleReply))) {
		return ErrForbidden
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM team_comments WHERE id = ?`, commentID); err != nil {
		return err
	}
	s.auditf(ctx, a, "team.comment.delete", mailboxID, convID, fmt.Sprintf("comment=%d", commentID))
	s.emit(Event{Kind: EventComment, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name})
	return nil
}

// Mentions returns the newest mentions of a user (unreadOnly filters read ones).
func (s *Service) Mentions(ctx context.Context, userID int64, unreadOnly bool, limit int) ([]Mention, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT c.id, c.mailbox_id, c.conversation_id, COALESCE(c.author_id,0), c.author_name, c.body, c.created_at, n.read_at, m.name
		FROM team_mentions n JOIN team_comments c ON c.id = n.comment_id JOIN team_mailboxes m ON m.id = c.mailbox_id
		WHERE n.user_id = ?`
	if unreadOnly {
		q += ` AND n.read_at IS NULL`
	}
	q += ` ORDER BY c.id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mention{}
	for rows.Next() {
		var m Mention
		var at int64
		var rd sql.NullInt64
		if err := rows.Scan(&m.ID, &m.MailboxID, &m.ConversationID, &m.AuthorID, &m.AuthorName, &m.Body, &at, &rd, &m.MailboxName); err != nil {
			return nil, err
		}
		m.CreatedAt, m.ReadAt, m.Mentions = time.Unix(at, 0).UTC(), ptime(rd), []int64{userID}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkMentionsRead marks the user's mentions in a conversation as read
// (convID 0: all of the mailbox).
func (s *Service) MarkMentionsRead(ctx context.Context, userID, mailboxID, convID int64) error {
	q := `UPDATE team_mentions SET read_at = ? WHERE user_id = ? AND read_at IS NULL AND comment_id IN
		(SELECT id FROM team_comments WHERE mailbox_id = ?`
	args := []any{s.unix(), userID, mailboxID}
	if convID != 0 {
		q += ` AND conversation_id = ?`
		args = append(args, convID)
	}
	_, err := s.db.ExecContext(ctx, q+`)`, args...)
	return err
}

// ==== Drafts ==================================================================

// SaveDraft stores (body != nil) or deletes the user's reply draft.
func (s *Service) SaveDraft(ctx context.Context, a Actor, mailboxID, convID int64, body json.RawMessage) error {
	if len(body) == 0 || string(body) == "null" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM team_drafts WHERE mailbox_id = ? AND conversation_id = ? AND user_id = ?`,
			mailboxID, convID, a.UserID)
		if err == nil {
			s.emit(Event{Kind: EventDraft, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name})
		}
		return err
	}
	if len(body) > 1<<20 || !json.Valid(body) {
		return fmt.Errorf("%w: draft must be a JSON document up to 1 MiB", ErrInvalidInput)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_drafts(mailbox_id, conversation_id, user_id, body_json, updated_at) VALUES (?,?,?,?,?)
		ON CONFLICT(mailbox_id, conversation_id, user_id) DO UPDATE SET body_json = excluded.body_json, updated_at = excluded.updated_at`,
		mailboxID, convID, a.UserID, string(body), s.unix())
	if err == nil {
		s.emit(Event{Kind: EventDraft, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name})
	}
	return err
}

// Draft returns the user's own draft (nil if none).
func (s *Service) Draft(ctx context.Context, userID, mailboxID, convID int64) (*Draft, error) {
	var d Draft
	var body string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT body_json, updated_at FROM team_drafts WHERE mailbox_id = ? AND conversation_id = ?
		AND user_id = ?`, mailboxID, convID, userID).Scan(&body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d = Draft{MailboxID: mailboxID, ConversationID: convID, UserID: userID, Body: json.RawMessage(body), UpdatedAt: time.Unix(at, 0).UTC()}
	return &d, nil
}

// Drafts returns the drafts of other team members for a conversation. The
// content is only included when the mailbox shares drafts; otherwise the
// entries only tell who is working on a reply (draft lock hint).
func (s *Service) Drafts(ctx context.Context, userID, mailboxID, convID int64) ([]Draft, error) {
	m, err := s.GetMailbox(ctx, mailboxID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, body_json, updated_at FROM team_drafts WHERE mailbox_id = ?
		AND conversation_id = ? AND user_id <> ? ORDER BY updated_at DESC`, mailboxID, convID, userID)
	if err != nil {
		return nil, err
	}
	out := []Draft{}
	for rows.Next() {
		d := Draft{MailboxID: mailboxID, ConversationID: convID}
		var body string
		var at int64
		if err := rows.Scan(&d.UserID, &body, &at); err != nil {
			rows.Close()
			return nil, err
		}
		d.UpdatedAt = time.Unix(at, 0).UTC()
		if m.SharedDrafts {
			d.Body = json.RawMessage(body)
		}
		out = append(out, d)
	}
	rows.Close()
	for i := range out {
		if u, err := s.dir.User(ctx, out[i].UserID); err == nil {
			out[i].UserName = u.Name()
		}
	}
	return out, rows.Err()
}

// ==== Statistics ==============================================================

// Stats computes the team statistics of a mailbox. total is the number of
// conversations in the team store (rows only exist for touched ones).
func (s *Service) Stats(ctx context.Context, mailboxID int64, total int) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(assignee_id,0), status, due_at, opened_at, first_response_at
		FROM team_conversations WHERE mailbox_id = ?`, mailboxID)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	st := Stats{ByAssignee: []AssigneeStats{}}
	per := map[int64]*AssigneeStats{}
	var resp []int64
	rowsN := 0
	now := s.unix()
	for rows.Next() {
		var assignee, opened int64
		var status Status
		var due, first sql.NullInt64
		if err := rows.Scan(&assignee, &status, &due, &opened, &first); err != nil {
			return Stats{}, err
		}
		rowsN++
		switch status {
		case StatusOpen:
			st.Open++
		case StatusInProgress:
			st.InProgress++
		case StatusWaiting:
			st.Waiting++
		case StatusDone:
			st.Done++
		}
		if status != StatusDone {
			if assignee == 0 {
				st.Unassigned++
			}
			if due.Valid && due.Int64 < now {
				st.Overdue++
			}
		}
		if first.Valid && first.Int64 >= opened {
			resp = append(resp, first.Int64-opened)
		}
		if assignee != 0 {
			p := per[assignee]
			if p == nil {
				p = &AssigneeStats{UserID: assignee}
				per[assignee] = p
			}
			if status == StatusDone {
				p.Done++
			} else {
				p.Open++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	// Conversations without a row are open and unassigned.
	if total > rowsN {
		st.Open += total - rowsN
		st.Unassigned += total - rowsN
	}
	st.Total = max(total, rowsN)
	st.Responded = len(resp)
	st.MedianFirstResponseSeconds = median(resp)
	for _, p := range per {
		if u, err := s.dir.User(ctx, p.UserID); err == nil {
			p.Name = u.Name()
		}
		st.ByAssignee = append(st.ByAssignee, *p)
	}
	sort.Slice(st.ByAssignee, func(i, j int) bool { return st.ByAssignee[i].Name < st.ByAssignee[j].Name })
	return st, nil
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
