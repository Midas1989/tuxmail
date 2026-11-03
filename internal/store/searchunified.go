// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Highlight markers that Search puts around matched terms in snippets. They
// are control characters that never occur in indexed text, so the UI can
// escape the snippet (e.g. for HTML) first and then replace the markers.
const (
	HighlightStart = "\x02"
	HighlightEnd   = "\x03"
)

// DefaultSearchLimit is used when SearchQuery.Limit is not positive.
const DefaultSearchLimit = 50

// SearchHit is one result of the unified search.
type SearchHit struct {
	Kind      SearchScope
	ID        int64 // id in the table of Kind (messages, chat_messages, cal_objects, contacts, cloud_links)
	Title     string
	Snippet   string // FTS5 snippet with HighlightStart/HighlightEnd markers (a stored excerpt without text query)
	Time      time.Time
	AccountID int64 // 0 for local items
	Score     float64
}

// searchArm is one SELECT of the UNION ALL in Search.
type searchArm struct {
	sql  string
	args []any
}

// armBuilder assembles one arm. Its select list is
// (kind, id, title, snippet, time, account_id, score).
type armBuilder struct {
	kind     SearchScope
	from     string // FROM clause without FTS
	ftsJoin  string // joins from fts_items/fts_docs to the item table alias
	expr     string // FTS MATCH expression ("" = no full-text match)
	id       string
	title    string
	fallback string // snippet expression without full-text match
	time     string
	account  string
	where    []string
	args     []any
}

func (b *armBuilder) cond(c string, args ...any) {
	b.where = append(b.where, c)
	b.args = append(b.args, args...)
}

func (b *armBuilder) build() searchArm {
	var sb strings.Builder
	snip, score := b.fallback, "0.0"
	if b.expr != "" {
		snip = `snippet(fts_items, -1, char(2), char(3), '…', 12)`
		score = `-bm25(fts_items)`
	}
	fmt.Fprintf(&sb, `SELECT '%s', %s, COALESCE(%s, ''), COALESCE(%s, ''), COALESCE(%s, 0), COALESCE(%s, 0), %s FROM `,
		b.kind, b.id, b.title, snip, b.time, b.account, score)
	var args []any
	where := slices.Clone(b.where)
	if b.expr != "" {
		sb.WriteString(`fts_items JOIN fts_docs d ON d.id = fts_items.rowid ` + b.ftsJoin)
		where = append([]string{`fts_items MATCH ?`}, where...)
		args = append(args, b.expr)
	} else {
		sb.WriteString(b.from)
	}
	args = append(args, b.args...)
	if len(where) > 0 {
		sb.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	return searchArm{sql: sb.String(), args: args}
}

func (b *armBuilder) accounts(ids []int64, restricted bool) {
	if restricted {
		b.cond(b.account+` IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	}
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func likePattern(v string) string { return "%" + likeEscape(strings.ToLower(v)) + "%" }

// Search runs a unified search over mails, chat messages, events, tasks,
// contacts and cloud links (KONZEPT 8.4). Results are ordered by relevance
// (with a text query) and then by time, newest first. An empty query returns
// no results. User input is always escaped; see ParseQuery and SearchQuery.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if q.IsEmpty() {
		return nil, nil
	}
	text, subj := ftsTerms(q.Text), subjectTerms(q.Subject)
	useFTS := len(text) > 0 || len(subj) > 0
	allExpr := ""
	if useFTS {
		allExpr = ftsExpr(text, subj, nil)
	}

	accounts, accRestricted, err := s.resolveSearchAccounts(ctx, q)
	if err != nil {
		return nil, err
	}
	if accRestricted && len(accounts) == 0 {
		return nil, nil
	}
	folders, folderFilter, err := s.resolveSearchFolders(ctx, q, accounts, accRestricted)
	if err != nil {
		return nil, err
	}

	var chatChannels []string
	for _, ch := range q.Channels {
		if ch != ChannelMail {
			chatChannels = append(chatChannels, ch)
		}
	}
	mailAllowed := len(q.Channels) == 0 || slices.Contains(q.Channels, ChannelMail)
	chatAllowed := len(q.Channels) == 0 || len(chatChannels) > 0
	messageOnly := len(q.FromAddrs) > 0 || len(q.ToAddrs) > 0 || q.HasAttachment || q.Unread || folderFilter
	otherAllowed := !messageOnly && len(q.Channels) == 0

	from, to := int64(math.MinInt64), int64(math.MaxInt64)
	timed := !q.From.IsZero() || !q.To.IsZero()
	if !q.From.IsZero() {
		from = q.From.Unix()
	}
	if !q.To.IsZero() {
		to = q.To.Unix()
	}

	var arms []searchArm

	// ---- Mail ----
	if q.hasScope(ScopeMail) && mailAllowed && !(folderFilter && len(folders) == 0) {
		b := &armBuilder{kind: ScopeMail, from: `messages m`, ftsJoin: `JOIN messages m ON m.id = d.message_id`,
			id: `m.id`, title: `m.subject`, fallback: `m.snippet`, time: `COALESCE(m.sent_at, m.received_at)`, account: `m.account_id`}
		if useFTS {
			var cols []string
			for _, f := range q.MailFields {
				if c := f.ftsColumn(); c != "" && !slices.Contains(cols, c) {
					cols = append(cols, c)
				}
			}
			if len(cols) == len(AllMailFields) {
				cols = nil
			}
			b.expr = ftsExpr(text, subj, cols)
		}
		b.cond(`m.state = 'live'`)
		b.accounts(accounts, accRestricted)
		if folderFilter {
			b.cond(`EXISTS (SELECT 1 FROM message_locations l WHERE l.message_id = m.id AND l.folder_id IN (`+
				placeholders(len(folders))+`))`, int64Args(folders)...)
		} else if q.ExcludeTrashSpam {
			b.cond(`(NOT EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
					WHERE l.message_id = m.id AND f.role IN ('trash', 'junk'))
				OR EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
					WHERE l.message_id = m.id AND COALESCE(f.role, '') NOT IN ('trash', 'junk')))`)
		}
		if timed {
			b.cond(`COALESCE(m.sent_at, m.received_at) >= ? AND COALESCE(m.sent_at, m.received_at) < ?`, from, to)
		}
		addrCond := func(roles string, v string) {
			p := likePattern(v)
			b.cond(`EXISTS (SELECT 1 FROM message_addresses a WHERE a.message_id = m.id AND a.role IN (`+roles+`)
				AND (lower(a.email) LIKE ? ESCAPE '\' OR lower(COALESCE(a.name, '')) LIKE ? ESCAPE '\'))`, p, p)
		}
		for _, v := range q.FromAddrs {
			p := likePattern(v)
			b.cond(`(lower(COALESCE(m.from_addr, '')) LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM message_addresses a
				WHERE a.message_id = m.id AND a.role = 'from'
				AND (lower(a.email) LIKE ? ESCAPE '\' OR lower(COALESCE(a.name, '')) LIKE ? ESCAPE '\')))`, p, p, p)
		}
		for _, v := range q.ToAddrs {
			addrCond(`'to', 'cc', 'bcc'`, v)
		}
		if q.HasAttachment {
			b.cond(`m.has_attachments = 1`)
		}
		if q.Unread {
			b.cond(`m.flags & ? = 0`, FlagSeen)
		}
		arms = append(arms, b.build())
	}

	// ---- Chat messages ----
	if q.hasScope(ScopeChat) && chatAllowed && !folderFilter && len(q.ToAddrs) == 0 {
		b := &armBuilder{kind: ScopeChat, from: `chat_messages cm JOIN chats c ON c.id = cm.chat_id`,
			ftsJoin: `JOIN chat_messages cm ON cm.id = d.chat_message_id JOIN chats c ON c.id = cm.chat_id`,
			expr:    allExpr, id: `cm.id`, title: `c.title`, fallback: `COALESCE(cm.body, cm.media_name)`,
			time: `cm.sent_at`, account: `c.account_id`}
		b.cond(`cm.deleted = 0`)
		b.accounts(accounts, accRestricted)
		if len(chatChannels) > 0 {
			b.cond(`c.channel IN (`+placeholders(len(chatChannels))+`)`, stringArgs(chatChannels)...)
		}
		if timed {
			b.cond(`COALESCE(cm.sent_at, 0) >= ? AND COALESCE(cm.sent_at, 0) < ?`, from, to)
		}
		for _, v := range q.FromAddrs {
			p := likePattern(v)
			b.cond(`EXISTS (SELECT 1 FROM persons p WHERE p.id = cm.sender_person_id
				AND (lower(COALESCE(p.display_name, '')) LIKE ? ESCAPE '\'
				  OR EXISTS (SELECT 1 FROM person_handles h WHERE h.person_id = p.id AND h.value LIKE ? ESCAPE '\')))`, p, p)
		}
		if q.HasAttachment {
			b.cond(`(cm.media_blob IS NOT NULL OR cm.media_name IS NOT NULL)`)
		}
		if q.Unread {
			b.cond(`cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0)`)
		}
		arms = append(arms, b.build())
	}

	// ---- Events and tasks ----
	for _, sc := range []SearchScope{ScopeEvent, ScopeTask} {
		if !q.hasScope(sc) || !otherAllowed {
			continue
		}
		kind, timeExpr := CalEvent, `o.dtstart`
		if sc == ScopeTask {
			kind, timeExpr = CalTodo, `COALESCE(o.due_at, o.dtstart)`
		}
		b := &armBuilder{kind: sc, from: `cal_objects o JOIN calendars cal ON cal.id = o.calendar_id`,
			ftsJoin: `JOIN cal_objects o ON o.id = d.cal_object_id JOIN calendars cal ON cal.id = o.calendar_id`,
			expr:    allExpr, id: `o.id`, title: `o.summary`, fallback: `o.summary`, time: timeExpr, account: `cal.account_id`}
		b.cond(`o.kind = ?`, kind)
		b.accounts(accounts, accRestricted)
		if timed && sc == ScopeEvent {
			b.cond(`(EXISTS (SELECT 1 FROM cal_instances i WHERE i.object_id = o.id
					AND i.start_at < ? AND (i.end_at > ? OR i.start_at >= ?))
				OR (NOT EXISTS (SELECT 1 FROM cal_instances i WHERE i.object_id = o.id)
					AND o.dtstart < ? AND (COALESCE(o.dtend, o.dtstart) > ? OR o.dtstart >= ?)))`,
				to, from, from, to, from, from)
		} else if timed {
			b.cond(`COALESCE(o.due_at, o.dtstart) >= ? AND COALESCE(o.due_at, o.dtstart) < ?`, from, to)
		}
		arms = append(arms, b.build())
	}

	// ---- Contacts (no time) ----
	if q.hasScope(ScopeContact) && otherAllowed && !timed {
		b := &armBuilder{kind: ScopeContact, from: `contacts ct JOIN addressbooks ab ON ab.id = ct.addressbook_id`,
			ftsJoin: `JOIN contacts ct ON ct.id = d.contact_id JOIN addressbooks ab ON ab.id = ct.addressbook_id`,
			expr:    allExpr, id: `ct.id`, title: `ct.display_name`,
			fallback: `(SELECT group_concat(email, ', ') FROM contact_emails WHERE contact_id = ct.id)`,
			time:     `NULL`, account: `ab.account_id`}
		b.accounts(accounts, accRestricted)
		arms = append(arms, b.build())
	}

	// ---- Cloud links ----
	if q.hasScope(ScopeFile) && otherAllowed {
		b := &armBuilder{kind: ScopeFile, from: `cloud_links cl`, ftsJoin: `JOIN cloud_links cl ON cl.id = d.cloud_link_id`,
			expr: allExpr, id: `cl.id`, title: `COALESCE(cl.file_name, cl.remote_path, cl.url)`, fallback: `cl.url`,
			time: `cl.created_at`, account: `cl.account_id`}
		b.accounts(accounts, accRestricted)
		if timed {
			b.cond(`COALESCE(cl.created_at, 0) >= ? AND COALESCE(cl.created_at, 0) < ?`, from, to)
		}
		arms = append(arms, b.build())
	}

	if len(arms) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	var (
		parts []string
		args  []any
	)
	for _, a := range arms {
		parts = append(parts, a.sql)
		args = append(args, a.args...)
	}
	query := strings.Join(parts, "\nUNION ALL\n") + "\nORDER BY 7 DESC, 5 DESC, 1, 2 LIMIT ? OFFSET ?"
	args = append(args, limit, max(q.Offset, 0))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	return collect(rows, func(sc scanner) (SearchHit, error) {
		var (
			h  SearchHit
			ts int64
		)
		err := sc.Scan(&h.Kind, &h.ID, &h.Title, &h.Snippet, &ts, &h.AccountID, &h.Score)
		if ts != 0 {
			h.Time = time.Unix(ts, 0).UTC()
		}
		return h, err
	})
}

func stringArgs(vs []string) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// resolveSearchAccounts returns the account ids the query is restricted to.
// account: values match an account id, display name or identity address
// (case-insensitive); with AccountIDs set as well, the intersection is used.
func (s *Store) resolveSearchAccounts(ctx context.Context, q SearchQuery) ([]int64, bool, error) {
	if len(q.AccountNames) == 0 {
		return q.AccountIDs, len(q.AccountIDs) > 0, nil
	}
	var ids []int64
	for _, name := range q.AccountNames {
		num, _ := strconv.ParseInt(name, 10, 64)
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM accounts WHERE id = ?1 OR lower(display_name) = ?2
			OR id IN (SELECT account_id FROM identities WHERE lower(email) = ?2)`, num, strings.ToLower(name))
		if err != nil {
			return nil, false, err
		}
		found, err := collect(rows, scanInt64)
		if err != nil {
			return nil, false, err
		}
		ids = append(ids, found...)
	}
	if len(q.AccountIDs) > 0 {
		ids = slices.DeleteFunc(ids, func(id int64) bool { return !slices.Contains(q.AccountIDs, id) })
	}
	slices.Sort(ids)
	return slices.Compact(ids), true, nil
}

var folderRoleAliases = map[string]string{
	"spam": "junk", "junk": "junk", "trash": "trash", "papierkorb": "trash", "deleted": "trash",
	"inbox": "inbox", "posteingang": "inbox", "sent": "sent", "gesendet": "sent",
	"drafts": "drafts", "entwürfe": "drafts", "archive": "archive", "archiv": "archive",
}

// resolveSearchFolders returns the folder ids the query is restricted to and
// whether a folder filter was requested at all. in: values match a folder
// name, remote id or role (with common aliases such as spam → junk).
func (s *Store) resolveSearchFolders(ctx context.Context, q SearchQuery, accounts []int64, accRestricted bool) ([]int64, bool, error) {
	if len(q.FolderIDs) == 0 && len(q.FolderNames) == 0 {
		return nil, false, nil
	}
	ids := slices.Clone(q.FolderIDs)
	for _, name := range q.FolderNames {
		ln := strings.ToLower(name)
		query := `SELECT id FROM folders WHERE (lower(name) = ? OR lower(remote_id) = ? OR role = ?)`
		args := []any{ln, ln, folderRoleAliases[ln]}
		if accRestricted {
			query += ` AND account_id IN (` + placeholders(len(accounts)) + `)`
			args = append(args, int64Args(accounts)...)
		}
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, err
		}
		found, err := collect(rows, scanInt64)
		if err != nil {
			return nil, false, err
		}
		ids = append(ids, found...)
	}
	if q.IncludeSubfolders && len(ids) > 0 {
		rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE sub(id) AS (
				SELECT id FROM folders WHERE id IN (`+placeholders(len(ids))+`)
				UNION SELECT f.id FROM folders f JOIN sub ON f.parent_id = sub.id)
			SELECT id FROM sub`, int64Args(ids)...)
		if err != nil {
			return nil, false, err
		}
		if ids, err = collect(rows, scanInt64); err != nil {
			return nil, false, err
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), true, nil
}

func scanInt64(sc scanner) (int64, error) {
	var v int64
	return v, sc.Scan(&v)
}
