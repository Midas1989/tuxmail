// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Relation kinds (KONZEPT 6.3).
const (
	RelReply        = "reply"
	RelForward      = "forward"
	RelReference    = "reference"
	RelDraftOf      = "draft_of"
	RelRedirect     = "redirect"
	RelManual       = "manual"
	RelInviteUpdate = "invite_update"
)

// Relation origins (KONZEPT 6.3).
const (
	OriginHeader       = "header"
	OriginThreadIndex  = "thread_index"
	OriginServerThread = "server_thread"
	OriginCompose      = "compose"
	OriginHeuristic    = "heuristic"
	OriginUser         = "user"
)

// Relation links two messages by their stable keys (see Message.Key).
type Relation struct {
	ID         int64
	SourceKey  string // the newer message
	TargetKey  string // the message it refers to
	Kind       string
	Origin     string
	Confidence float64
	CreatedAt  time.Time
}

// InsertRelation stores a relation unless one with the same (source, target,
// kind) exists already. It reports whether a new row was inserted. A zero
// CreatedAt means now.
func (s *Store) InsertRelation(ctx context.Context, r Relation) (bool, error) {
	if r.SourceKey == "" || r.TargetKey == "" || r.Kind == "" || r.Origin == "" {
		return false, errors.New("store: insert relation: source, target, kind and origin are required")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	res, err := s.q(ctx).ExecContext(ctx, `INSERT OR IGNORE INTO message_relations(source_key, target_key, kind, origin, confidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, r.SourceKey, r.TargetKey, r.Kind, r.Origin, r.Confidence, r.CreatedAt.Unix())
	if err != nil {
		return false, fmt.Errorf("store: insert relation: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RelationsByKey returns all relations in which key is source or target and
// whose confidence is at least minConfidence.
func (s *Store) RelationsByKey(ctx context.Context, key string, minConfidence float64) ([]Relation, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, source_key, target_key, kind, origin, confidence, created_at
		FROM message_relations WHERE (source_key = ?1 OR target_key = ?1) AND confidence >= ?2 ORDER BY id`,
		key, minConfidence)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (Relation, error) {
		var (
			r  Relation
			ts int64
		)
		err := sc.Scan(&r.ID, &r.SourceKey, &r.TargetKey, &r.Kind, &r.Origin, &r.Confidence, &ts)
		r.CreatedAt = time.Unix(ts, 0).UTC()
		return r, err
	})
}

// DeleteRelation deletes a relation by id (e.g. when the user splits a thread).
func (s *Store) DeleteRelation(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM message_relations WHERE id = ?`, id))
}

// ==== Conversations ===========================================================

// Conversation kinds.
const (
	ConvEmailThread = "email_thread"
	ConvChat        = "chat"
	ConvPersonView  = "person_view"
)

// Conversation is a thread of mails, a chat or a person view.
type Conversation struct {
	ID           int64
	Kind         string
	Title        string
	LastActivity time.Time
	UnreadCount  int
	Pinned       bool
	Muted        bool
	Archived     bool
}

const conversationCols = `id, kind, title, last_activity, unread_count, pinned, muted, archived`

func scanConversation(sc scanner) (Conversation, error) {
	var (
		c     Conversation
		title sql.NullString
		last  sql.NullInt64
	)
	err := sc.Scan(&c.ID, &c.Kind, &title, &last, &c.UnreadCount, &c.Pinned, &c.Muted, &c.Archived)
	c.Title, c.LastActivity = title.String, timeFromNull(last)
	return c, err
}

// CreateConversation inserts a conversation and returns its id.
func (s *Store) CreateConversation(ctx context.Context, c Conversation) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO conversations(kind, title, last_activity, unread_count, pinned, muted, archived)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.Kind, nullString(c.Title), unixOrNull(c.LastActivity), c.UnreadCount,
		boolInt(c.Pinned), boolInt(c.Muted), boolInt(c.Archived))
	if err != nil {
		return 0, fmt.Errorf("store: create conversation: %w", err)
	}
	return res.LastInsertId()
}

// UpdateConversation overwrites the conversation c.ID.
func (s *Store) UpdateConversation(ctx context.Context, c Conversation) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE conversations SET kind = ?, title = ?, last_activity = ?,
		unread_count = ?, pinned = ?, muted = ?, archived = ? WHERE id = ?`, c.Kind, nullString(c.Title),
		unixOrNull(c.LastActivity), c.UnreadCount, boolInt(c.Pinned), boolInt(c.Muted), boolInt(c.Archived), c.ID))
}

// GetConversation returns the conversation with the given id.
func (s *Store) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := scanConversation(s.q(ctx).QueryRowContext(ctx, `SELECT `+conversationCols+` FROM conversations WHERE id = ?`, id))
	return c, notFound(err)
}

// AssignConversation sets the conversation of a message (0 detaches it).
func (s *Store) AssignConversation(ctx context.Context, messageID, conversationID int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE messages SET conversation_id = ? WHERE id = ?`,
		nullID(conversationID), messageID))
}

// RefreshConversationStats recomputes last_activity and unread_count of an
// email conversation from its live messages.
func (s *Store) RefreshConversationStats(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE conversations SET
			last_activity = (SELECT MAX(COALESCE(sent_at, received_at)) FROM messages WHERE conversation_id = ?1 AND +state = 'live'),
			unread_count  = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?1 AND +state = 'live' AND flags & ?2 = 0)
		WHERE id = ?1`, id, FlagSeen))
}

// MergeConversations merges the given conversations into the one with the
// lowest (oldest) id, which is returned (KONZEPT 6.4 step 3). Messages, chats
// and overrides are moved; the others are deleted. Flags are combined: pinned
// and muted if any was, archived only if all were.
func (s *Store) MergeConversations(ctx context.Context, ids ...int64) (int64, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return 0, errors.New("store: merge conversations: no ids")
	}
	keep, others := ids[0], ids[1:]
	if len(others) == 0 {
		return keep, nil
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		ph := placeholders(len(others))
		phAll := placeholders(len(ids))
		all := make([]any, len(ids))
		for i, id := range ids {
			all[i] = id
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id IN (`+phAll+`)`, all...).Scan(&n); err != nil {
			return err
		}
		if n != len(ids) {
			return ErrNotFound
		}
		args := append([]any{keep}, all[1:]...)
		for _, q := range []string{
			`UPDATE messages SET conversation_id = ? WHERE conversation_id IN (` + ph + `)`,
			`UPDATE chats SET conversation_id = ? WHERE conversation_id IN (` + ph + `)`,
			`UPDATE conversation_overrides SET conversation_id = ? WHERE conversation_id IN (` + ph + `)`,
		} {
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET
				last_activity = (SELECT MAX(last_activity) FROM conversations WHERE id IN (`+phAll+`)),
				unread_count  = (SELECT SUM(unread_count) FROM conversations WHERE id IN (`+phAll+`)),
				pinned        = (SELECT MAX(pinned) FROM conversations WHERE id IN (`+phAll+`)),
				muted         = (SELECT MAX(muted) FROM conversations WHERE id IN (`+phAll+`)),
				archived      = (SELECT MIN(archived) FROM conversations WHERE id IN (`+phAll+`))
			WHERE id = ?`, repeatArgs(all, 5, keep)...); err != nil {
			return err
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		_, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id IN (`+ph+`)`, all[1:]...)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: merge conversations: %w", err)
	}
	return keep, nil
}

// placeholders returns "?,?,...,?" with n placeholders (n >= 1).
func placeholders(n int) string {
	return "?" + strings.Repeat(",?", n-1)
}

// repeatArgs returns args repeated n times followed by tail.
func repeatArgs(args []any, n int, tail ...any) []any {
	out := make([]any, 0, len(args)*n+len(tail))
	for range n {
		out = append(out, args...)
	}
	return append(out, tail...)
}

// ==== Overrides ===============================================================

// Override actions.
const (
	OverrideSplit = "split"
	OverrideMerge = "merge"
	OverridePinTo = "pin_to"
)

// Override is a manual user decision about a message's conversation.
type Override struct {
	MessageKey     string
	ConversationID int64
	Action         string
}

// SetOverride stores (or replaces) the override for a message key.
func (s *Store) SetOverride(ctx context.Context, o Override) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO conversation_overrides(message_key, conversation_id, action) VALUES (?, ?, ?)
		ON CONFLICT(message_key) DO UPDATE SET conversation_id = excluded.conversation_id, action = excluded.action`,
		o.MessageKey, nullID(o.ConversationID), o.Action)
	return err
}

// GetOverride returns the override for a message key.
func (s *Store) GetOverride(ctx context.Context, key string) (Override, error) {
	var (
		o    Override
		conv sql.NullInt64
	)
	err := s.q(ctx).QueryRowContext(ctx, `SELECT message_key, conversation_id, action FROM conversation_overrides
		WHERE message_key = ?`, key).Scan(&o.MessageKey, &conv, &o.Action)
	o.ConversationID = conv.Int64
	return o, notFound(err)
}

// DeleteOverride removes the override for a message key.
func (s *Store) DeleteOverride(ctx context.Context, key string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM conversation_overrides WHERE message_key = ?`, key))
}
