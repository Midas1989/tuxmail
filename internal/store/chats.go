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

// Chat kinds (KONZEPT 5.2).
const (
	ChatDirect  = "direct"
	ChatGroup   = "group"
	ChatChannel = "channel"
)

// Chat message delivery states.
const (
	ChatStatusSent      = "sent"
	ChatStatusDelivered = "delivered"
	ChatStatusRead      = "read"
	ChatStatusFailed    = "failed"
)

// Chat is a messenger chat (direct chat, group or channel).
type Chat struct {
	ID             int64
	AccountID      int64
	RemoteID       string
	Kind           string
	Title          string
	ConversationID int64
	// Channel is the network the chat belongs to (whatsapp, signal, telegram,
	// matrix, ...). Bridged chats keep the channel of the original network.
	// If empty on upsert, the account kind is used.
	Channel string
	// LastReadAt is the read marker: incoming messages sent after it are unread.
	LastReadAt time.Time
}

// Reaction is one emoji reaction on a chat message.
type Reaction struct {
	Emoji  string `json:"emoji"`
	Sender string `json:"sender"` // remote sender id or handle
}

// ChatMessage is one message in a chat.
type ChatMessage struct {
	ID              int64
	ChatID          int64
	RemoteID        string
	SenderPersonID  int64
	FromMe          bool
	SentAt          time.Time
	Body            string
	ReplyToRemoteID string
	MediaBlob       string // blob hash
	MediaMime       string
	MediaName       string // file name or caption
	Status          string // ChatStatus*
	Reactions       []Reaction
	EditedAt        time.Time
	Deleted         bool
}

// ChatCursor addresses a position in a chat for keyset paging. The zero value
// means "newest".
type ChatCursor struct {
	SentAt time.Time
	ID     int64
}

// Cursor returns the cursor that continues paging after m.
func (m ChatMessage) Cursor() ChatCursor { return ChatCursor{SentAt: m.SentAt, ID: m.ID} }

const chatCols = `id, account_id, remote_id, kind, title, conversation_id, channel, last_read_at`

func scanChat(sc scanner) (Chat, error) {
	var (
		c              Chat
		title, channel sql.NullString
		conv, lastRead sql.NullInt64
	)
	err := sc.Scan(&c.ID, &c.AccountID, &c.RemoteID, &c.Kind, &title, &conv, &channel, &lastRead)
	c.Title, c.Channel, c.ConversationID, c.LastReadAt = title.String, channel.String, conv.Int64, timeFromNull(lastRead)
	return c, err
}

// UpsertChat inserts or updates the chat identified by (AccountID, RemoteID)
// and returns its id. A zero ConversationID and an empty Channel do not
// overwrite stored values; the read marker is never changed (see MarkChatRead).
func (s *Store) UpsertChat(ctx context.Context, c Chat) (int64, error) {
	if c.RemoteID == "" || c.Kind == "" {
		return 0, errors.New("store: upsert chat: remote id and kind are required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO chats(account_id, remote_id, kind, title, conversation_id, channel)
		VALUES (?1, ?2, ?3, ?4, ?5, COALESCE(?6, (SELECT kind FROM accounts WHERE id = ?1)))
		ON CONFLICT(account_id, remote_id) DO UPDATE SET
			kind = excluded.kind, title = excluded.title,
			conversation_id = COALESCE(?5, chats.conversation_id),
			channel = COALESCE(?6, chats.channel)
		RETURNING id`,
		c.AccountID, c.RemoteID, c.Kind, nullString(c.Title), nullID(c.ConversationID), nullString(c.Channel)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert chat %q: %w", c.RemoteID, err)
	}
	return id, nil
}

// GetChat returns the chat with the given id.
func (s *Store) GetChat(ctx context.Context, id int64) (Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = ?`, id))
	return c, notFound(err)
}

// ChatByRemoteID returns the chat of an account with the given remote id.
func (s *Store) ChatByRemoteID(ctx context.Context, accountID int64, remoteID string) (Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE account_id = ? AND remote_id = ?`,
		accountID, remoteID))
	return c, notFound(err)
}

// ListChats returns the chats of an account, most recently active first.
func (s *Store) ListChats(ctx context.Context, accountID int64) ([]Chat, error) {
	return s.listChats(ctx, `c.account_id = ?`, accountID)
}

// ListChatsByConversation returns the chats belonging to a conversation, most
// recently active first.
func (s *Store) ListChatsByConversation(ctx context.Context, conversationID int64) ([]Chat, error) {
	return s.listChats(ctx, `c.conversation_id = ?`, conversationID)
}

func (s *Store) listChats(ctx context.Context, where string, args ...any) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("c.", chatCols)+` FROM chats c
		WHERE `+where+`
		ORDER BY (SELECT MAX(sent_at) FROM chat_messages WHERE chat_id = c.id) DESC NULLS LAST, c.id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanChat)
}

// AssignChatConversation sets the conversation of a chat (0 detaches it).
func (s *Store) AssignChatConversation(ctx context.Context, chatID, conversationID int64) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE chats SET conversation_id = ? WHERE id = ?`,
		nullID(conversationID), chatID))
}

// MarkChatRead moves the read marker of a chat to upTo (it never moves back).
func (s *Store) MarkChatRead(ctx context.Context, chatID int64, upTo time.Time) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE chats SET last_read_at = MAX(COALESCE(last_read_at, 0), ?) WHERE id = ?`,
		upTo.Unix(), chatID))
}

// DeleteChat deletes a chat and its messages.
func (s *Store) DeleteChat(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id))
}

// unreadChatCond selects unread messages of chat c (alias m for chat_messages).
const unreadChatCond = `m.from_me = 0 AND m.deleted = 0 AND COALESCE(m.sent_at, 0) > COALESCE(c.last_read_at, 0)`

// ChatUnreadCounts returns the number of unread incoming messages per chat of
// an account (0 = all accounts). Chats without unread messages are omitted.
func (s *Store) ChatUnreadCounts(ctx context.Context, accountID int64) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, COUNT(*) FROM chats c JOIN chat_messages m ON m.chat_id = c.id
		WHERE (?1 = 0 OR c.account_id = ?1) AND `+unreadChatCond+` GROUP BY c.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int)
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ==== Chat messages ===========================================================

const chatMessageCols = `id, chat_id, remote_id, sender_person_id, from_me, sent_at, body, reply_to_remote_id,
	media_blob, media_mime, media_name, status, reactions_json, edited_at, deleted`

func scanChatMessage(sc scanner) (ChatMessage, error) {
	var (
		m                                              ChatMessage
		sender, sent, edited                           sql.NullInt64
		body, replyTo, blob, mime, name, st, reactJSON sql.NullString
	)
	err := sc.Scan(&m.ID, &m.ChatID, &m.RemoteID, &sender, &m.FromMe, &sent, &body, &replyTo,
		&blob, &mime, &name, &st, &reactJSON, &edited, &m.Deleted)
	if err != nil {
		return m, err
	}
	m.SenderPersonID, m.SentAt, m.EditedAt = sender.Int64, timeFromNull(sent), timeFromNull(edited)
	m.Body, m.ReplyToRemoteID, m.MediaBlob, m.MediaMime = body.String, replyTo.String, blob.String, mime.String
	m.MediaName, m.Status = name.String, st.String
	if reactJSON.String != "" {
		if err := json.Unmarshal([]byte(reactJSON.String), &m.Reactions); err != nil {
			return m, fmt.Errorf("store: chat message %d: bad reactions_json: %w", m.ID, err)
		}
	}
	return m, nil
}

func reactionsJSON(rs []Reaction) (any, error) {
	if len(rs) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(rs)
	return string(b), err
}

// UpsertChatMessage inserts or updates the message identified by (ChatID,
// RemoteID) and returns its id and whether it was newly created. On update,
// empty Status/Reactions, a zero EditedAt and a zero SenderPersonID keep the
// stored values, and a message once marked deleted stays deleted.
func (s *Store) UpsertChatMessage(ctx context.Context, m ChatMessage) (id int64, created bool, err error) {
	if m.RemoteID == "" {
		return 0, false, errors.New("store: upsert chat message: remote id is required")
	}
	react, err := reactionsJSON(m.Reactions)
	if err != nil {
		return 0, false, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM chat_messages WHERE chat_id = ? AND remote_id = ?`, m.ChatID, m.RemoteID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			created = true
			return tx.QueryRowContext(ctx, `INSERT INTO chat_messages(chat_id, remote_id, sender_person_id, from_me, sent_at,
				body, reply_to_remote_id, media_blob, media_mime, media_name, status, reactions_json, edited_at, deleted)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				m.ChatID, m.RemoteID, nullID(m.SenderPersonID), boolInt(m.FromMe), unixOrNull(m.SentAt), nullString(m.Body),
				nullString(m.ReplyToRemoteID), nullString(m.MediaBlob), nullString(m.MediaMime), nullString(m.MediaName),
				nullString(m.Status), react, unixOrNull(m.EditedAt), boolInt(m.Deleted)).Scan(&id)
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_messages SET
				sender_person_id = COALESCE(?, sender_person_id), from_me = ?, sent_at = COALESCE(?, sent_at),
				body = ?, reply_to_remote_id = ?, media_blob = COALESCE(?, media_blob), media_mime = COALESCE(?, media_mime),
				media_name = COALESCE(?, media_name), status = COALESCE(?, status),
				reactions_json = COALESCE(?, reactions_json), edited_at = COALESCE(?, edited_at), deleted = MAX(deleted, ?)
			WHERE id = ?`,
			nullID(m.SenderPersonID), boolInt(m.FromMe), unixOrNull(m.SentAt), nullString(m.Body),
			nullString(m.ReplyToRemoteID), nullString(m.MediaBlob), nullString(m.MediaMime), nullString(m.MediaName),
			nullString(m.Status), react, unixOrNull(m.EditedAt), boolInt(m.Deleted), id)
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("store: upsert chat message: %w", err)
	}
	return id, created, nil
}

// GetChatMessage returns the chat message with the given id.
func (s *Store) GetChatMessage(ctx context.Context, id int64) (ChatMessage, error) {
	m, err := scanChatMessage(s.db.QueryRowContext(ctx, `SELECT `+chatMessageCols+` FROM chat_messages WHERE id = ?`, id))
	return m, notFound(err)
}

// ChatMessageByRemoteID returns the message of a chat with the given remote id.
func (s *Store) ChatMessageByRemoteID(ctx context.Context, chatID int64, remoteID string) (ChatMessage, error) {
	m, err := scanChatMessage(s.db.QueryRowContext(ctx, `SELECT `+chatMessageCols+` FROM chat_messages
		WHERE chat_id = ? AND remote_id = ?`, chatID, remoteID))
	return m, notFound(err)
}

// ListChatMessages returns up to limit messages of a chat older than before,
// newest first (keyset paging: pass the Cursor of the last returned message to
// get the next page; the zero cursor starts at the newest message). Messages
// without a send time sort as oldest.
func (s *Store) ListChatMessages(ctx context.Context, chatID int64, before ChatCursor, limit int) ([]ChatMessage, error) {
	q := `SELECT ` + chatMessageCols + ` FROM chat_messages WHERE chat_id = ?`
	args := []any{chatID}
	if before != (ChatCursor{}) {
		q += ` AND (COALESCE(sent_at, 0), id) < (?, ?)`
		args = append(args, before.SentAt.Unix(), before.ID)
	}
	q += ` ORDER BY COALESCE(sent_at, 0) DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanChatMessage)
}

// ListChatMessagesSince returns up to limit messages of a chat newer than
// after, oldest first (for catching up after a sync).
func (s *Store) ListChatMessagesSince(ctx context.Context, chatID int64, after ChatCursor, limit int) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatMessageCols+` FROM chat_messages
		WHERE chat_id = ? AND (COALESCE(sent_at, 0), id) > (?, ?)
		ORDER BY COALESCE(sent_at, 0), id LIMIT ?`, chatID, after.SentAt.Unix(), after.ID, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanChatMessage)
}

// SetChatMessageStatus sets the delivery status of a message.
func (s *Store) SetChatMessageStatus(ctx context.Context, id int64, status string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE chat_messages SET status = ? WHERE id = ?`, nullString(status), id))
}

// SetChatMessageReactions replaces the reactions of a message.
func (s *Store) SetChatMessageReactions(ctx context.Context, id int64, rs []Reaction) error {
	react, err := reactionsJSON(rs)
	if err != nil {
		return err
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE chat_messages SET reactions_json = ? WHERE id = ?`, react, id))
}

// EditChatMessage replaces the body of a message and records the edit time.
// The caller should re-index the message (IndexChatMessage).
func (s *Store) EditChatMessage(ctx context.Context, id int64, body string, editedAt time.Time) error {
	if editedAt.IsZero() {
		editedAt = time.Now()
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE chat_messages SET body = ?, edited_at = ? WHERE id = ?`,
		nullString(body), editedAt.Unix(), id))
}

// MarkChatMessageDeleted flags a message as deleted ("deleted for everyone"):
// its text and media reference are cleared and it is removed from the search
// index. The row stays so that replies keep their reference.
func (s *Store) MarkChatMessageDeleted(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := checkAffected(tx.ExecContext(ctx, `UPDATE chat_messages SET deleted = 1, body = NULL,
			media_blob = NULL, media_mime = NULL, media_name = NULL WHERE id = ?`, id)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM fts_docs WHERE chat_message_id = ?`, id)
		return err
	})
}

// DeleteChatMessage removes a message row completely.
func (s *Store) DeleteChatMessage(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM chat_messages WHERE id = ?`, id))
}
