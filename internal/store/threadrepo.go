// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
)

// ThreadRepo implements threading.Repository, threading.Transactor and
// threading.HeuristicIndex on top of the SQLite schema (KONZEPT 6).
//
// Message keys are the keys of package threading: a normalized Message-ID,
// "~obj:<account id>:<server object id>" for messages without Message-ID but
// with a server object id, and "~fp:<fingerprint>" otherwise (see
// MessageKey). A key may match rows in several accounts (the same mail
// delivered to two accounts); assignments always apply to all of them, so a
// conversation is a set of keys, not of rows. A row whose conversation_id is
// NULL is "not present" for the engine.
//
// After every assignment change the statistics (last_activity,
// unread_count) of the affected conversations are recomputed in the same
// transaction.
type ThreadRepo struct {
	s *Store
	q querier
	// tx is set when the repository is bound to a transaction (InTx).
	tx *sql.Tx
}

var (
	_ threading.Repository     = (*ThreadRepo)(nil)
	_ threading.Transactor     = (*ThreadRepo)(nil)
	_ threading.HeuristicIndex = (*ThreadRepo)(nil)
)

// ThreadRepository returns the SQLite threading repository of the store.
func (s *Store) ThreadRepository() *ThreadRepo { return &ThreadRepo{s: s, q: s.db} }

// keyExpr computes the threading key of a messages row (alias-free; use
// keyExprFor for joins).
const keyExpr = `CASE WHEN message_id_hdr IS NOT NULL THEN message_id_hdr
	WHEN server_obj_id IS NOT NULL THEN '~obj:' || account_id || ':' || server_obj_id
	ELSE '~fp:' || fingerprint END`

// MessageKey returns the threading key of a stored message (see ThreadRepo).
// It equals threading.MessageKey for the header the message was stored from
// when the namespace is the decimal account id.
func MessageKey(m Message) string {
	switch {
	case m.MessageIDHdr != "":
		return m.MessageIDHdr
	case m.ServerObjectID != "":
		return threading.PrefixObjectID + strconv.FormatInt(m.AccountID, 10) + ":" + m.ServerObjectID
	default:
		return threading.PrefixFingerprint + m.Fingerprint
	}
}

// keyWhere returns a WHERE condition selecting the rows of a key. ok is false
// for virtual or malformed keys, which never match a row. The conditions
// are written so that the existing per-account indexes are usable
// ("account_id IN (SELECT id FROM accounts)" lets SQLite probe the
// (account_id, ...) indexes once per account).
func keyWhere(key string) (where string, args []any, ok bool) {
	switch {
	case key == "" || threading.IsVirtualKey(key):
		return "", nil, false
	case strings.HasPrefix(key, threading.PrefixObjectID):
		rest := strings.TrimPrefix(key, threading.PrefixObjectID)
		ns, obj, found := strings.Cut(rest, ":")
		acc, err := strconv.ParseInt(ns, 10, 64)
		if !found || err != nil || obj == "" {
			return "", nil, false
		}
		return `account_id = ? AND server_obj_id = ? AND message_id_hdr IS NULL`, []any{acc, obj}, true
	case strings.HasPrefix(key, threading.PrefixFingerprint):
		fp := strings.TrimPrefix(key, threading.PrefixFingerprint)
		if fp == "" {
			return "", nil, false
		}
		return `account_id IN (SELECT id FROM accounts) AND fingerprint = ? AND message_id_hdr IS NULL AND server_obj_id IS NULL`,
			[]any{fp}, true
	case strings.HasPrefix(key, "~"):
		return "", nil, false
	default:
		return `account_id IN (SELECT id FROM accounts) AND message_id_hdr = ?`, []any{key}, true
	}
}

// MessagesByKey returns all rows (of any account and state) with the given
// threading key, ordered by id.
func (s *Store) MessagesByKey(ctx context.Context, key string) ([]Message, error) {
	where, args, ok := keyWhere(key)
	if !ok {
		return nil, nil
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// KeyHasRows reports whether rows of key exist that are not in state
// deleted (live or orphaned).
func (s *Store) KeyHasRows(ctx context.Context, key string) (bool, error) {
	where, args, ok := keyWhere(key)
	if !ok {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+where+` AND state <> 'deleted'`, args...).Scan(&n)
	return n > 0, err
}

// AlignKeyConversation assigns rows of key that have no conversation to the
// conversation of the other rows of the key (e.g. the same mail synced into
// a second account after the engine already threaded it) and returns that
// conversation (0 if the key has none).
func (s *Store) AlignKeyConversation(ctx context.Context, key string) (int64, error) {
	where, args, ok := keyWhere(key)
	if !ok {
		return 0, nil
	}
	var conv int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var id sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MIN(conversation_id) FROM messages WHERE `+where+
			` AND conversation_id IS NOT NULL`, args...).Scan(&id); err != nil {
			return err
		}
		if !id.Valid {
			return nil
		}
		conv = id.Int64
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		res, err := tx.ExecContext(ctx, `UPDATE messages SET conversation_id = ? WHERE `+where+` AND conversation_id IS NULL`,
			append([]any{conv}, args...)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return refreshConversationStats(ctx, tx, conv)
		}
		return nil
	})
	return conv, err
}

// SaveRelations implements threading.Repository.
func (r *ThreadRepo) SaveRelations(ctx context.Context, rels []threading.Relation, createdAt time.Time) error {
	for _, rel := range rels {
		if rel.SourceKey == "" || rel.TargetKey == "" || rel.Kind == "" {
			return fmt.Errorf("store: relation with empty field: %+v", rel)
		}
		origin := string(rel.Origin)
		if origin == "" {
			origin = OriginHeader
		}
		// SQLite evaluates all SET expressions against the old row.
		if _, err := r.q.ExecContext(ctx, `INSERT INTO message_relations(source_key, target_key, kind, origin, confidence, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_key, target_key, kind) DO UPDATE SET
				origin = CASE WHEN excluded.confidence > confidence THEN excluded.origin ELSE origin END,
				confidence = MAX(confidence, excluded.confidence)`,
			rel.SourceKey, rel.TargetKey, string(rel.Kind), origin, rel.Confidence, createdAt.Unix()); err != nil {
			return fmt.Errorf("store: save relation: %w", err)
		}
	}
	return nil
}

// RelationsOf implements threading.Repository.
func (r *ThreadRepo) RelationsOf(ctx context.Context, key string) ([]threading.Relation, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT source_key, target_key, kind, origin, confidence FROM message_relations
		WHERE source_key = ?1
		UNION ALL
		SELECT source_key, target_key, kind, origin, confidence FROM message_relations
		WHERE target_key = ?1 AND source_key <> ?1`, key)
	if err != nil {
		return nil, err
	}
	out, err := collect(rows, func(sc scanner) (threading.Relation, error) {
		var (
			rel          threading.Relation
			kind, origin string
		)
		err := sc.Scan(&rel.SourceKey, &rel.TargetKey, &kind, &origin, &rel.Confidence)
		rel.Kind, rel.Origin = threading.RelationKind(kind), threading.Origin(origin)
		return rel, err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.SourceKey != b.SourceKey {
			return a.SourceKey < b.SourceKey
		}
		if a.TargetKey != b.TargetKey {
			return a.TargetKey < b.TargetKey
		}
		return a.Kind < b.Kind
	})
	return out, nil
}

// ConversationOf implements threading.Repository. If the rows of a key
// disagree (which only happens transiently), the oldest conversation wins.
func (r *ThreadRepo) ConversationOf(ctx context.Context, key string) (int64, error) {
	where, args, ok := keyWhere(key)
	if !ok {
		return 0, nil
	}
	var id sql.NullInt64
	err := r.q.QueryRowContext(ctx, `SELECT MIN(conversation_id) FROM messages WHERE `+where+
		` AND conversation_id IS NOT NULL`, args...).Scan(&id)
	return id.Int64, err
}

// SetConversation implements threading.Repository. It assigns all rows of
// key and refreshes the statistics of the old and new conversations.
func (r *ThreadRepo) SetConversation(ctx context.Context, key string, conversationID int64) error {
	where, args, ok := keyWhere(key)
	if !ok {
		return nil
	}
	if conversationID != 0 {
		var one int
		err := r.q.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, conversationID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: conversation %d", threading.ErrNotFound, conversationID)
		}
		if err != nil {
			return err
		}
	}
	rows, err := r.q.QueryContext(ctx, `SELECT DISTINCT conversation_id FROM messages WHERE `+where+
		` AND conversation_id IS NOT NULL`, args...)
	if err != nil {
		return err
	}
	old, err := collect(rows, func(sc scanner) (int64, error) {
		var v int64
		return v, sc.Scan(&v)
	})
	if err != nil {
		return err
	}
	if _, err := r.q.ExecContext(ctx, `UPDATE messages SET conversation_id = ? WHERE `+where,
		append([]any{nullID(conversationID)}, args...)...); err != nil {
		return err
	}
	for _, id := range append(old, conversationID) {
		if id != 0 {
			if err := refreshConversationStats(ctx, r.q, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Members implements threading.Repository.
func (r *ThreadRepo) Members(ctx context.Context, conversationID int64) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT DISTINCT `+keyExpr+` FROM messages WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return nil, err
	}
	out, err := collect(rows, func(sc scanner) (string, error) {
		var k string
		return k, sc.Scan(&k)
	})
	sort.Strings(out)
	return out, err
}

// CreateConversation implements threading.Repository.
func (r *ThreadRepo) CreateConversation(ctx context.Context, c threading.Conversation) (int64, error) {
	kind := string(c.Kind)
	if kind == "" {
		kind = ConvEmailThread
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO conversations(kind, title) VALUES (?, ?)`, kind, nullString(c.Title))
	if err != nil {
		return 0, fmt.Errorf("store: create conversation: %w", err)
	}
	return res.LastInsertId()
}

// GetConversation implements threading.Repository.
func (r *ThreadRepo) GetConversation(ctx context.Context, id int64) (threading.Conversation, error) {
	var (
		c     threading.Conversation
		kind  string
		title sql.NullString
	)
	err := r.q.QueryRowContext(ctx, `SELECT id, kind, title FROM conversations WHERE id = ?`, id).Scan(&c.ID, &kind, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: conversation %d", threading.ErrNotFound, id)
	}
	c.Kind, c.Title = threading.ConversationKind(kind), title.String
	return c, err
}

// DeleteConversation implements threading.Repository. Overrides pointing at
// the conversation are kept (with conversation NULL) instead of being
// cascaded away; the engine re-creates a conversation for them when their
// message re-appears.
func (r *ThreadRepo) DeleteConversation(ctx context.Context, id int64) error {
	if _, err := r.q.ExecContext(ctx, `UPDATE conversation_overrides SET conversation_id = NULL WHERE conversation_id = ?`, id); err != nil {
		return err
	}
	err := checkAffected(r.q.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id))
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: conversation %d", threading.ErrNotFound, id)
	}
	return err
}

// Override implements threading.Repository.
func (r *ThreadRepo) Override(ctx context.Context, key string) (threading.Override, bool, error) {
	var (
		o      threading.Override
		conv   sql.NullInt64
		action string
	)
	err := r.q.QueryRowContext(ctx, `SELECT message_key, conversation_id, action FROM conversation_overrides
		WHERE message_key = ?`, key).Scan(&o.MessageKey, &conv, &action)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	o.ConversationID, o.Action = conv.Int64, threading.OverrideAction(action)
	return o, true, nil
}

// SetOverride implements threading.Repository.
func (r *ThreadRepo) SetOverride(ctx context.Context, o threading.Override) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO conversation_overrides(message_key, conversation_id, action) VALUES (?, ?, ?)
		ON CONFLICT(message_key) DO UPDATE SET conversation_id = excluded.conversation_id, action = excluded.action`,
		o.MessageKey, nullID(o.ConversationID), string(o.Action))
	return err
}

// DeleteOverride implements threading.Repository.
func (r *ThreadRepo) DeleteOverride(ctx context.Context, key string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM conversation_overrides WHERE message_key = ?`, key)
	return err
}

// InTx implements threading.Transactor. Nested calls reuse the transaction.
func (r *ThreadRepo) InTx(ctx context.Context, fn func(threading.Repository) error) error {
	if r.tx != nil {
		return fn(r)
	}
	return r.s.withTx(ctx, func(tx *sql.Tx) error {
		return fn(&ThreadRepo{s: r.s, q: tx, tx: tx})
	})
}

// IndexMessage implements threading.HeuristicIndex. It is a no-op: the
// candidates are read from messages and message_addresses.
func (r *ThreadRepo) IndexMessage(context.Context, threading.Candidate) error { return nil }

// maxHeuristicCandidates bounds the rows scanned by FindBySubject.
const maxHeuristicCandidates = 5000

// FindBySubject implements threading.HeuristicIndex: present messages whose
// subject key equals subjectKey and whose date lies in [from, to].
func (r *ThreadRepo) FindBySubject(ctx context.Context, subjectKey string, from, to time.Time) ([]threading.Candidate, error) {
	cands, err := r.candidates(ctx, `conversation_id IS NOT NULL AND subject IS NOT NULL
		AND COALESCE(sent_at, received_at) BETWEEN ? AND ?`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	out := cands[:0]
	for _, c := range cands {
		if threading.SubjectKey(c.Subject) == subjectKey {
			out = append(out, c)
		}
	}
	return out, nil
}

// ForwardCandidates returns live or orphaned messages sent by fromEmail
// (any account) with a date in [from, to], for matching inline-forwarded
// blocks (threading.MatchForward). An empty fromEmail matches any sender.
func (s *Store) ForwardCandidates(ctx context.Context, fromEmail string, from, to time.Time) ([]threading.Candidate, error) {
	r := s.ThreadRepository()
	where := `state <> 'deleted' AND COALESCE(sent_at, received_at) BETWEEN ? AND ?`
	args := []any{from.Unix(), to.Unix()}
	if e := strings.ToLower(strings.TrimSpace(fromEmail)); e != "" {
		where += ` AND id IN (SELECT message_id FROM message_addresses WHERE role = 'from' AND lower(email) = ?)`
		args = append(args, e)
	}
	return r.candidates(ctx, where, args...)
}

func (r *ThreadRepo) candidates(ctx context.Context, where string, args ...any) ([]threading.Candidate, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, `+keyExpr+`, COALESCE(subject, ''), COALESCE(from_addr, ''),
			COALESCE(sent_at, received_at, 0)
		FROM messages WHERE `+where+` ORDER BY id DESC LIMIT `+strconv.Itoa(maxHeuristicCandidates), args...)
	if err != nil {
		return nil, err
	}
	type row struct {
		id int64
		c  threading.Candidate
	}
	list, err := collect(rows, func(sc scanner) (row, error) {
		var (
			x  row
			ts int64
		)
		err := sc.Scan(&x.id, &x.c.Key, &x.c.Subject, &x.c.From, &ts)
		if ts != 0 {
			x.c.Date = time.Unix(ts, 0).UTC()
		}
		return x, err
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]threading.Candidate, 0, len(list))
	for _, x := range list {
		if seen[x.c.Key] {
			continue
		}
		seen[x.c.Key] = true
		arows, err := r.q.QueryContext(ctx, `SELECT role, lower(email) FROM message_addresses
			WHERE message_id = ? AND role IN ('from', 'to', 'cc') ORDER BY rowid`, x.id)
		if err != nil {
			return nil, err
		}
		addrs, err := collect(arows, func(sc scanner) ([2]string, error) {
			var a [2]string
			return a, sc.Scan(&a[0], &a[1])
		})
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if a[0] == RoleFrom && x.c.From == "" {
				x.c.From = a[1]
			}
			x.c.Participants = append(x.c.Participants, a[1])
		}
		x.c.From = strings.ToLower(x.c.From)
		out = append(out, x.c)
	}
	return out, nil
}

// refreshConversationStats recomputes last_activity and unread_count of a
// conversation from its live messages (no error if it does not exist).
func refreshConversationStats(ctx context.Context, q querier, id int64) error {
	_, err := q.ExecContext(ctx, `UPDATE conversations SET
			last_activity = (SELECT MAX(COALESCE(sent_at, received_at)) FROM messages WHERE conversation_id = ?1 AND +state = 'live'),
			unread_count  = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?1 AND +state = 'live' AND flags & ?2 = 0)
		WHERE id = ?1`, id, FlagSeen)
	return err
}

// RefreshConversations recomputes the statistics of the given
// conversations; unknown ids and 0 are ignored.
func (s *Store) RefreshConversations(ctx context.Context, ids ...int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		if err := refreshConversationStats(ctx, s.db, id); err != nil {
			return err
		}
	}
	return nil
}
