// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Persistence of the application layer (internal/app, migration 0005): small
// settings documents, chat-mode flags and marks, chat folders, drafts,
// scheduled messages, snippets, workflow metadata, uploads, meeting
// proposals, contact cards and chat signature settings.

// ==== Settings documents =======================================================

// GetAppSetting decodes the JSON document stored under key into v. It reports
// false (and leaves v untouched) when nothing is stored.
func (s *Store) GetAppSetting(ctx context.Context, key string, v any) (bool, error) {
	var js string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM app_settings WHERE key = ?`, key).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(js), v); err != nil {
		return false, fmt.Errorf("store: app setting %q: %w", key, err)
	}
	return true, nil
}

// SetAppSetting stores v as JSON under key.
func (s *Store) SetAppSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_settings(key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		key, string(b), time.Now().Unix())
	return err
}

// DeleteAppSetting removes the document stored under key.
func (s *Store) DeleteAppSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, key)
	return err
}

// ==== Conversation state =======================================================

// ConversationState holds the chat-mode flags of a conversation.
type ConversationState struct {
	ConversationID     int64
	Favorite           bool
	MarkedUnread       bool
	Blocked            bool
	SnoozedUntil       time.Time
	ReturnedFromSnooze bool
}

// GetConversationState returns the state of a conversation (zero value if none is stored).
func (s *Store) GetConversationState(ctx context.Context, id int64) (ConversationState, error) {
	st := ConversationState{ConversationID: id}
	var snooze sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT favorite, marked_unread, blocked, snoozed_until, returned_from_snooze
		FROM conversation_state WHERE conversation_id = ?`, id).
		Scan(&st.Favorite, &st.MarkedUnread, &st.Blocked, &snooze, &st.ReturnedFromSnooze)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	st.SnoozedUntil = timeFromNull(snooze)
	return st, err
}

// ConversationStates returns the stored states of all conversations that have one.
func (s *Store) ConversationStates(ctx context.Context) (map[int64]ConversationState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_id, favorite, marked_unread, blocked, snoozed_until, returned_from_snooze
		FROM conversation_state`)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows, func(sc scanner) (ConversationState, error) {
		var st ConversationState
		var snooze sql.NullInt64
		err := sc.Scan(&st.ConversationID, &st.Favorite, &st.MarkedUnread, &st.Blocked, &snooze, &st.ReturnedFromSnooze)
		st.SnoozedUntil = timeFromNull(snooze)
		return st, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ConversationState, len(list))
	for _, st := range list {
		out[st.ConversationID] = st
	}
	return out, nil
}

// PutConversationState stores the state of a conversation.
func (s *Store) PutConversationState(ctx context.Context, st ConversationState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_state(conversation_id, favorite, marked_unread, blocked,
			snoozed_until, returned_from_snooze, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET favorite = excluded.favorite, marked_unread = excluded.marked_unread,
			blocked = excluded.blocked, snoozed_until = excluded.snoozed_until,
			returned_from_snooze = excluded.returned_from_snooze, updated_at = excluded.updated_at`,
		st.ConversationID, boolInt(st.Favorite), boolInt(st.MarkedUnread), boolInt(st.Blocked),
		unixOrNull(st.SnoozedUntil), boolInt(st.ReturnedFromSnooze), time.Now().Unix())
	return err
}

// WakeSnoozed ends all snoozes that are due at now and marks the
// conversations as returned; it returns the woken conversation ids.
func (s *Store) WakeSnoozed(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE conversation_state SET snoozed_until = NULL, returned_from_snooze = 1, updated_at = ?1
		WHERE snoozed_until IS NOT NULL AND snoozed_until <= ?1 RETURNING conversation_id`, now.Unix())
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}

// ==== Person conversations and counterparts ===================================

// PersonConversation returns the person_view conversation of a person,
// creating it (with the given title) if needed.
func (s *Store) PersonConversation(ctx context.Context, personID int64, title string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM person_conversations WHERE person_id = ?`, personID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM person_conversations WHERE person_id = ?`, personID).Scan(&id); err == nil {
			return nil
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO conversations(kind, title) VALUES (?, ?) RETURNING id`,
			ConvPersonView, nullString(title)).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO person_conversations(person_id, conversation_id) VALUES (?, ?)`, personID, id)
		return err
	})
	return id, err
}

// PersonOfConversation returns the person of a person_view conversation (0 if it is none).
func (s *Store) PersonOfConversation(ctx context.Context, conversationID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT person_id FROM person_conversations WHERE conversation_id = ?`, conversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// PersonConversations returns all person -> conversation mappings.
func (s *Store) PersonConversations(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT person_id, conversation_id FROM person_conversations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var p, c int64
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

// MessagesWithoutCounterpart returns ids of live messages that have no
// counterpart row yet (at most limit, oldest first).
func (s *Store) MessagesWithoutCounterpart(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id FROM messages m
		LEFT JOIN message_counterparts mc ON mc.message_id = m.id
		WHERE mc.message_id IS NULL AND m.state = 'live' ORDER BY m.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}

// SetMessageCounterpart records the person a mail is "with".
func (s *Store) SetMessageCounterpart(ctx context.Context, messageID, personID int64, outgoing bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO message_counterparts(message_id, person_id, outgoing) VALUES (?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET person_id = excluded.person_id, outgoing = excluded.outgoing`,
		messageID, nullID(personID), boolInt(outgoing))
	return err
}

// MessageCounterpart returns the counterpart person of a mail (0 if unknown).
func (s *Store) MessageCounterpart(ctx context.Context, messageID int64) (personID int64, outgoing bool, err error) {
	var p sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT person_id, outgoing FROM message_counterparts WHERE message_id = ?`, messageID).Scan(&p, &outgoing)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return p.Int64, outgoing, err
}

// ChatsWithoutCounterpart returns ids of chats that have no counterpart row yet.
func (s *Store) ChatsWithoutCounterpart(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id FROM chats c LEFT JOIN chat_counterparts cc ON cc.chat_id = c.id
		WHERE cc.chat_id IS NULL ORDER BY c.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}

// SetChatCounterpart records the person of a direct chat (0 for groups).
func (s *Store) SetChatCounterpart(ctx context.Context, chatID, personID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_counterparts(chat_id, person_id) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET person_id = excluded.person_id`, chatID, nullID(personID))
	return err
}

// ResetCounterparts drops all counterpart rows (they are recomputed lazily),
// e.g. after persons were merged or identities changed.
func (s *Store) ResetCounterparts(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_counterparts`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM chat_counterparts`)
		return err
	})
}

// ==== Item marks ===============================================================

// ItemStar is a starred item.
type ItemStar struct {
	ItemID         string
	ConversationID int64
	StarredAt      time.Time
}

// SetItemStar stars (or unstars) an item.
func (s *Store) SetItemStar(ctx context.Context, itemID string, conversationID int64, starred bool) error {
	if !starred {
		_, err := s.db.ExecContext(ctx, `DELETE FROM item_stars WHERE item_id = ?`, itemID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO item_stars(item_id, conversation_id, starred_at) VALUES (?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET conversation_id = excluded.conversation_id`, itemID, conversationID, time.Now().Unix())
	return err
}

// ListItemStars returns starred items, newest first.
func (s *Store) ListItemStars(ctx context.Context) ([]ItemStar, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, conversation_id, starred_at FROM item_stars ORDER BY starred_at DESC, item_id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ItemStar, error) {
		var st ItemStar
		var at int64
		err := sc.Scan(&st.ItemID, &st.ConversationID, &at)
		st.StarredAt = time.Unix(at, 0).UTC()
		return st, err
	})
}

// StarredItemSet returns the ids of all starred items.
func (s *Store) StarredItemSet(ctx context.Context) (map[string]bool, error) {
	stars, err := s.ListItemStars(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(stars))
	for _, st := range stars {
		out[st.ItemID] = true
	}
	return out, nil
}

// ErrPinLimit is returned when a conversation already has the maximum number of pinned items.
var ErrPinLimit = errors.New("store: pin limit reached")

// MaxPinnedItems is the number of items that may be pinned per conversation.
const MaxPinnedItems = 3

// SetItemPin pins (or unpins) an item in a conversation.
func (s *Store) SetItemPin(ctx context.Context, conversationID int64, itemID string, pinned bool) error {
	if !pinned {
		_, err := s.db.ExecContext(ctx, `DELETE FROM item_pins WHERE conversation_id = ? AND item_id = ?`, conversationID, itemID)
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(item_id = ?), 0) > 0 FROM item_pins WHERE conversation_id = ?`,
			itemID, conversationID).Scan(&n, &exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if n >= MaxPinnedItems {
			return ErrPinLimit
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO item_pins(conversation_id, item_id, pinned_at) VALUES (?, ?, ?)`,
			conversationID, itemID, time.Now().Unix())
		return err
	})
}

// PinnedItems returns the pinned item ids of a conversation, oldest pin first.
func (s *Store) PinnedItems(ctx context.Context, conversationID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id FROM item_pins WHERE conversation_id = ? ORDER BY pinned_at, item_id`, conversationID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (string, error) {
		var id string
		return id, sc.Scan(&id)
	})
}

// ToggleItemReaction toggles a reaction of the current user on an item and
// returns whether it is now set.
func (s *Store) ToggleItemReaction(ctx context.Context, itemID, emoji string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM item_reactions WHERE item_id = ? AND emoji = ?`, itemID, emoji)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO item_reactions(item_id, emoji, created_at) VALUES (?, ?, ?)`, itemID, emoji, time.Now().Unix())
	return err == nil, err
}

// ItemReactions returns the reactions of the current user per item id.
func (s *Store) ItemReactions(ctx context.Context, itemIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for len(itemIDs) > 0 {
		chunk := itemIDs[:min(len(itemIDs), 500)]
		itemIDs = itemIDs[len(chunk):]
		args := make([]any, len(chunk))
		for i, v := range chunk {
			args[i] = v
		}
		rows, err := s.db.QueryContext(ctx, `SELECT item_id, emoji FROM item_reactions WHERE item_id IN (`+placeholders(len(chunk))+`)
			ORDER BY created_at, emoji`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, e string
			if err := rows.Scan(&id, &e); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], e)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HideItem hides an item locally (e.g. a mail removed from the chat view).
func (s *Store) HideItem(ctx context.Context, itemID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO item_hidden(item_id, hidden_at) VALUES (?, ?)`, itemID, time.Now().Unix())
	return err
}

// HiddenItemSet returns all hidden item ids.
func (s *Store) HiddenItemSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id FROM item_hidden`)
	if err != nil {
		return nil, err
	}
	ids, err := collect(rows, func(sc scanner) (string, error) {
		var id string
		return id, sc.Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// ==== Chat folders =============================================================

// ChatFolderRecord is a stored chat folder; Rules is opaque JSON owned by internal/app.
type ChatFolderRecord struct {
	ID        string
	Name      string
	Emoji     string
	RulesJSON string
	SortOrder int
}

// ListChatFolders returns the chat folders in display order.
func (s *Store) ListChatFolders(ctx context.Context) ([]ChatFolderRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, emoji, rules_json, sort_order FROM chat_folders ORDER BY sort_order, name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ChatFolderRecord, error) {
		var f ChatFolderRecord
		var emoji sql.NullString
		err := sc.Scan(&f.ID, &f.Name, &emoji, &f.RulesJSON, &f.SortOrder)
		f.Emoji = emoji.String
		return f, err
	})
}

// PutChatFolder inserts or replaces a chat folder.
func (s *Store) PutChatFolder(ctx context.Context, f ChatFolderRecord) error {
	if f.ID == "" || f.Name == "" {
		return errors.New("store: chat folder needs id and name")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_folders(id, name, emoji, rules_json, sort_order, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, emoji = excluded.emoji, rules_json = excluded.rules_json,
			sort_order = excluded.sort_order, updated_at = excluded.updated_at`,
		f.ID, f.Name, nullString(f.Emoji), f.RulesJSON, f.SortOrder, time.Now().Unix())
	return err
}

// DeleteChatFolder deletes a chat folder.
func (s *Store) DeleteChatFolder(ctx context.Context, id string) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM chat_folders WHERE id = ?`, id))
}

// ==== Drafts ===================================================================

// ChatDraft is the unsent composer text of a conversation.
type ChatDraft struct {
	ConversationID int64
	Text           string
	ReplyToItemID  string
	ChannelKey     string
	UpdatedAt      time.Time
}

func scanDraft(sc scanner) (ChatDraft, error) {
	var d ChatDraft
	var reply, ch sql.NullString
	var at int64
	err := sc.Scan(&d.ConversationID, &d.Text, &reply, &ch, &at)
	d.ReplyToItemID, d.ChannelKey, d.UpdatedAt = reply.String, ch.String, time.Unix(at, 0).UTC()
	return d, err
}

// GetChatDraft returns the draft of a conversation.
func (s *Store) GetChatDraft(ctx context.Context, conversationID int64) (ChatDraft, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT conversation_id, text, reply_to_item_id, channel_key, updated_at
		FROM chat_drafts WHERE conversation_id = ?`, conversationID))
	return d, notFound(err)
}

// ChatDrafts returns all drafts by conversation id.
func (s *Store) ChatDrafts(ctx context.Context) (map[int64]ChatDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_id, text, reply_to_item_id, channel_key, updated_at FROM chat_drafts`)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows, scanDraft)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ChatDraft, len(list))
	for _, d := range list {
		out[d.ConversationID] = d
	}
	return out, nil
}

// PutChatDraft stores a draft; an empty text deletes it.
func (s *Store) PutChatDraft(ctx context.Context, d ChatDraft) error {
	if d.Text == "" && d.ReplyToItemID == "" {
		return s.DeleteChatDraft(ctx, d.ConversationID)
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_drafts(conversation_id, text, reply_to_item_id, channel_key, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET text = excluded.text,
		reply_to_item_id = excluded.reply_to_item_id, channel_key = excluded.channel_key, updated_at = excluded.updated_at`,
		d.ConversationID, d.Text, nullString(d.ReplyToItemID), nullString(d.ChannelKey), d.UpdatedAt.Unix())
	return err
}

// DeleteChatDraft deletes the draft of a conversation (no error if none exists).
func (s *Store) DeleteChatDraft(ctx context.Context, conversationID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_drafts WHERE conversation_id = ?`, conversationID)
	return err
}

// ==== Scheduled messages =======================================================

// Scheduled message states.
const (
	ScheduledPending   = "pending"
	ScheduledSending   = "sending"
	ScheduledSent      = "sent"
	ScheduledFailed    = "failed"
	ScheduledCancelled = "cancelled"
)

// ScheduledMessage is a message to be sent later. MessageJSON is the
// complete outgoing message (owned by internal/app).
type ScheduledMessage struct {
	ID             string
	ConversationID int64
	Channel        string
	SendAt         time.Time
	Text           string
	MessageJSON    string
	State          string
	LastError      string
	CreatedAt      time.Time
}

const scheduledCols = `id, conversation_id, channel, send_at, text, message_json, state, last_error, created_at`

func scanScheduled(sc scanner) (ScheduledMessage, error) {
	var m ScheduledMessage
	var conv sql.NullInt64
	var lastErr sql.NullString
	var at, created int64
	err := sc.Scan(&m.ID, &conv, &m.Channel, &at, &m.Text, &m.MessageJSON, &m.State, &lastErr, &created)
	m.ConversationID, m.LastError = conv.Int64, lastErr.String
	m.SendAt, m.CreatedAt = time.Unix(at, 0).UTC(), time.Unix(created, 0).UTC()
	return m, err
}

// CreateScheduledMessage stores a pending scheduled message.
func (s *Store) CreateScheduledMessage(ctx context.Context, m ScheduledMessage) error {
	if m.ID == "" {
		return errors.New("store: scheduled message needs an id")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduled_messages(id, conversation_id, channel, send_at, text, message_json, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`, m.ID, nullID(m.ConversationID), m.Channel, m.SendAt.Unix(), m.Text, m.MessageJSON, time.Now().Unix())
	return err
}

// GetScheduledMessage returns a scheduled message.
func (s *Store) GetScheduledMessage(ctx context.Context, id string) (ScheduledMessage, error) {
	m, err := scanScheduled(s.db.QueryRowContext(ctx, `SELECT `+scheduledCols+` FROM scheduled_messages WHERE id = ?`, id))
	return m, notFound(err)
}

// ListScheduledMessages returns pending messages (of one conversation, 0 = all) ordered by send time.
func (s *Store) ListScheduledMessages(ctx context.Context, conversationID int64) ([]ScheduledMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+scheduledCols+` FROM scheduled_messages
		WHERE state IN ('pending', 'failed') AND (?1 = 0 OR conversation_id = ?1) ORDER BY send_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanScheduled)
}

// UpdateScheduledMessage updates text, send time and message of a pending message.
func (s *Store) UpdateScheduledMessage(ctx context.Context, m ScheduledMessage) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE scheduled_messages SET send_at = ?, text = ?, message_json = ?
		WHERE id = ? AND state IN ('pending', 'failed')`, m.SendAt.Unix(), m.Text, m.MessageJSON, m.ID))
}

// SetScheduledState sets the state (and error) of a scheduled message.
func (s *Store) SetScheduledState(ctx context.Context, id, state, lastErr string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE scheduled_messages SET state = ?, last_error = ? WHERE id = ?`,
		state, nullString(lastErr), id))
}

// ClaimDueScheduled marks pending messages due at now as sending and returns them.
func (s *Store) ClaimDueScheduled(ctx context.Context, now time.Time, limit int) ([]ScheduledMessage, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE scheduled_messages SET state = 'sending'
		WHERE id IN (SELECT id FROM scheduled_messages WHERE state = 'pending' AND send_at <= ? ORDER BY send_at LIMIT ?)
		RETURNING `+scheduledCols, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanScheduled)
}

// ==== Snippets =================================================================

// Snippet is a quick reply ("/trigger").
type Snippet struct {
	ID        string
	Trigger   string
	Title     string
	Text      string
	SortOrder int
}

// ListSnippets returns all snippets ordered by trigger.
func (s *Store) ListSnippets(ctx context.Context) ([]Snippet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, trigger, title, text, sort_order FROM snippets ORDER BY sort_order, trigger, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (Snippet, error) {
		var sn Snippet
		return sn, sc.Scan(&sn.ID, &sn.Trigger, &sn.Title, &sn.Text, &sn.SortOrder)
	})
}

// PutSnippet inserts or replaces a snippet.
func (s *Store) PutSnippet(ctx context.Context, sn Snippet) error {
	if sn.ID == "" || sn.Trigger == "" {
		return errors.New("store: snippet needs id and trigger")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO snippets(id, trigger, title, text, sort_order, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET trigger = excluded.trigger, title = excluded.title, text = excluded.text,
			sort_order = excluded.sort_order, updated_at = excluded.updated_at`,
		sn.ID, sn.Trigger, sn.Title, sn.Text, sn.SortOrder, time.Now().Unix())
	return err
}

// DeleteSnippet deletes a snippet.
func (s *Store) DeleteSnippet(ctx context.Context, id string) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM snippets WHERE id = ?`, id))
}

// ==== Workflow metadata ========================================================

// WorkflowMeta binds a workflow to a project and records its template.
type WorkflowMeta struct {
	WorkflowID string
	ProjectID  int64
	TemplateID string
}

// PutWorkflowMeta stores the metadata of an existing workflow.
func (s *Store) PutWorkflowMeta(ctx context.Context, m WorkflowMeta) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_meta(workflow_id, project_id, template_id) VALUES (?, ?, ?)
		ON CONFLICT(workflow_id) DO UPDATE SET project_id = excluded.project_id, template_id = excluded.template_id`,
		m.WorkflowID, nullID(m.ProjectID), nullString(m.TemplateID))
	return err
}

// WorkflowMetas returns the metadata of all workflows by id.
func (s *Store) WorkflowMetas(ctx context.Context) (map[string]WorkflowMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_id, project_id, template_id FROM workflow_meta`)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows, func(sc scanner) (WorkflowMeta, error) {
		var m WorkflowMeta
		var p sql.NullInt64
		var t sql.NullString
		err := sc.Scan(&m.WorkflowID, &p, &t)
		m.ProjectID, m.TemplateID = p.Int64, t.String
		return m, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]WorkflowMeta, len(list))
	for _, m := range list {
		out[m.WorkflowID] = m
	}
	return out, nil
}

// ==== Uploads ==================================================================

// Upload is an attachment uploaded before sending.
type Upload struct {
	Token     string
	Filename  string
	MIMEType  string
	Size      int64
	BlobHash  string
	CreatedAt time.Time
}

// CreateUpload records an upload.
func (s *Store) CreateUpload(ctx context.Context, u Upload) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO uploads(token, filename, mime_type, size, blob_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.Token, u.Filename, u.MIMEType, u.Size, u.BlobHash, time.Now().Unix())
	return err
}

// GetUpload returns an upload by token.
func (s *Store) GetUpload(ctx context.Context, token string) (Upload, error) {
	var u Upload
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT token, filename, mime_type, size, blob_hash, created_at FROM uploads WHERE token = ?`, token).
		Scan(&u.Token, &u.Filename, &u.MIMEType, &u.Size, &u.BlobHash, &at)
	u.CreatedAt = time.Unix(at, 0).UTC()
	return u, notFound(err)
}

// PurgeUploads deletes uploads created before the given time and returns their blob hashes.
func (s *Store) PurgeUploads(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `DELETE FROM uploads WHERE created_at < ? RETURNING blob_hash`, before.Unix())
	if err != nil {
		return nil, err
	}
	hashes, err := collect(rows, func(sc scanner) (string, error) {
		var h string
		return h, sc.Scan(&h)
	})
	if err != nil {
		return nil, err
	}
	// Content-addressed: the same content may be a sent attachment or a
	// signature image by now.
	return s.UnreferencedBlobs(ctx, hashes)
}

// ==== Meeting proposals, contact cards, chat signatures ======================

// PutMeetingProposal stores a meeting proposal (JSON owned by internal/app).
func (s *Store) PutMeetingProposal(ctx context.Context, id string, conversationID int64, itemID, js string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_proposals(id, conversation_id, item_id, proposal_json, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET item_id = excluded.item_id, proposal_json = excluded.proposal_json`,
		id, conversationID, nullString(itemID), js, time.Now().Unix())
	return err
}

// GetMeetingProposal returns the JSON of a meeting proposal.
func (s *Store) GetMeetingProposal(ctx context.Context, id string) (conversationID int64, itemID, js string, err error) {
	var item sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT conversation_id, item_id, proposal_json FROM meeting_proposals WHERE id = ?`, id).
		Scan(&conversationID, &item, &js)
	return conversationID, item.String, js, notFound(err)
}

// MeetingProposalsByItem returns proposal JSON by item id for a conversation.
func (s *Store) MeetingProposalsByItem(ctx context.Context, conversationID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, proposal_json FROM meeting_proposals WHERE conversation_id = ? AND item_id IS NOT NULL`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, js string
		if err := rows.Scan(&id, &js); err != nil {
			return nil, err
		}
		out[id] = js
	}
	return out, rows.Err()
}

// PutContactCard stores the contact card JSON of a person.
func (s *Store) PutContactCard(ctx context.Context, personID int64, js string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO contact_cards(person_id, card_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET card_json = excluded.card_json, updated_at = excluded.updated_at`,
		personID, js, time.Now().Unix())
	return err
}

// IdentityChat holds the chat-mode signature settings of an identity.
type IdentityChat struct {
	IdentityID    int64
	SignatureMode string
	SignatureID   int64
}

// IdentityChatSettings returns the chat settings of all identities that have some.
func (s *Store) IdentityChatSettings(ctx context.Context) (map[int64]IdentityChat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT identity_id, signature_mode, signature_id FROM identity_chat`)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows, func(sc scanner) (IdentityChat, error) {
		var c IdentityChat
		var mode sql.NullString
		var sig sql.NullInt64
		err := sc.Scan(&c.IdentityID, &mode, &sig)
		c.SignatureMode, c.SignatureID = mode.String, sig.Int64
		return c, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]IdentityChat, len(list))
	for _, c := range list {
		out[c.IdentityID] = c
	}
	return out, nil
}

// PutIdentityChat stores the chat settings of an identity.
func (s *Store) PutIdentityChat(ctx context.Context, c IdentityChat) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO identity_chat(identity_id, signature_mode, signature_id) VALUES (?, ?, ?)
		ON CONFLICT(identity_id) DO UPDATE SET signature_mode = excluded.signature_mode, signature_id = excluded.signature_id`,
		c.IdentityID, nullString(c.SignatureMode), nullID(c.SignatureID))
	return err
}
