// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Read models of the application layer (internal/app): filtered and paged
// message lists, batch lookups for list rows and conversation aggregates for
// the chat/hybrid views.

// MessageFilter selects live messages. All set fields must match.
type MessageFilter struct {
	AccountIDs []int64
	// FolderIDs: the message has a location in one of these folders.
	FolderIDs []int64
	// Roles: the message has a location in a folder with one of these roles.
	Roles []domain.FolderRole
	// FlagsSet: all these flag bits are set; FlagsClear: all are clear.
	FlagsSet   Flags
	FlagsClear Flags
	// IDs restricts the result to these message ids (nil = no restriction,
	// empty non-nil = nothing matches).
	IDs []int64
	// ConversationIDs restricts to messages of these (thread) conversations.
	ConversationIDs []int64
}

func int64List(ids []int64) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// where builds the SQL condition for alias m (messages).
func (f MessageFilter) where() (string, []any) {
	// +m.state: without ANALYZE statistics SQLite would pick ix_msg_live_sent
	// (state is not selective) and sort all live messages; this way the
	// newest-first page walks ix_msg_time and stops after the page.
	conds := []string{`+m.state = 'live'`}
	var args []any
	if f.IDs != nil {
		if len(f.IDs) == 0 {
			return "0", nil
		}
		conds = append(conds, `m.id IN (`+int64List(f.IDs)+`)`)
	}
	if f.ConversationIDs != nil {
		if len(f.ConversationIDs) == 0 {
			return "0", nil
		}
		conds = append(conds, `m.conversation_id IN (`+int64List(f.ConversationIDs)+`)`)
	}
	if len(f.AccountIDs) > 0 {
		conds = append(conds, `m.account_id IN (`+int64List(f.AccountIDs)+`)`)
	}
	if len(f.FolderIDs) > 0 {
		conds = append(conds, `EXISTS (SELECT 1 FROM message_locations l WHERE l.message_id = m.id AND l.folder_id IN (`+int64List(f.FolderIDs)+`))`)
	}
	if len(f.Roles) > 0 {
		ph := make([]string, len(f.Roles))
		for i, r := range f.Roles {
			ph[i] = "?"
			args = append(args, string(r))
		}
		conds = append(conds, `EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
			WHERE l.message_id = m.id AND f.role IN (`+strings.Join(ph, ",")+`))`)
	}
	if f.FlagsSet != 0 {
		conds = append(conds, `m.flags & ? = ?`)
		args = append(args, f.FlagsSet, f.FlagsSet)
	}
	if f.FlagsClear != 0 {
		conds = append(conds, `m.flags & ? = 0`)
		args = append(args, f.FlagsClear)
	}
	return strings.Join(conds, " AND "), args
}

// ListFilteredMessages returns one page of matching messages (newest first)
// and the total number of matches.
func (s *Store) ListFilteredMessages(ctx context.Context, f MessageFilter, limit, offset int) ([]Message, int, error) {
	where, args := f.where()
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixed("m.", messageCols)+` FROM messages m WHERE `+where+`
		ORDER BY COALESCE(m.sent_at, m.received_at, 0) DESC, m.id DESC LIMIT ? OFFSET ?`, append(args, limit, max(offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collect(rows, scanMessage)
	return list, total, err
}

// MatchingMessageIDs returns the ids of all messages matching f.
func (s *Store) MatchingMessageIDs(ctx context.Context, f MessageFilter) ([]int64, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT m.id FROM messages m WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanID)
}

func scanID(sc scanner) (int64, error) {
	var id int64
	return id, sc.Scan(&id)
}

// GetMessages returns the messages with the given ids (in no particular order).
func (s *Store) GetMessages(ctx context.Context, ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id IN (`+int64List(ids)+`)`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// AddressesFor returns the addresses of many messages.
func (s *Store) AddressesFor(ctx context.Context, ids []int64) (map[int64][]MessageAddress, error) {
	out := map[int64][]MessageAddress{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, role, name, email, person_id FROM message_addresses
		WHERE message_id IN (`+int64List(ids)+`) ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			a    MessageAddress
			name sql.NullString
			pid  sql.NullInt64
		)
		if err := rows.Scan(&id, &a.Role, &name, &a.Email, &pid); err != nil {
			return nil, err
		}
		a.Name, a.PersonID = name.String, pid.Int64
		out[id] = append(out[id], a)
	}
	return out, rows.Err()
}

// FoldersFor returns the folder ids of many messages.
func (s *Store) FoldersFor(ctx context.Context, ids []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, folder_id FROM message_locations WHERE message_id IN (`+int64List(ids)+`)
		ORDER BY message_id, folder_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m, f int64
		if err := rows.Scan(&m, &f); err != nil {
			return nil, err
		}
		if l := out[m]; len(l) == 0 || l[len(l)-1] != f {
			out[m] = append(out[m], f)
		}
	}
	return out, rows.Err()
}

// ProjectsForItems returns the project ids linked to many items of one kind.
func (s *Store) ProjectsForItems(ctx context.Context, kind ItemKind, ids []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, project_id FROM project_items WHERE item_kind = ? AND item_id IN (`+int64List(ids)+`)
		ORDER BY item_id, project_id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i, p int64
		if err := rows.Scan(&i, &p); err != nil {
			return nil, err
		}
		out[i] = append(out[i], p)
	}
	return out, rows.Err()
}

// TaggedItemIDs returns the ids of items of a kind tagged with one of the
// tags (including descendants when the tag ids already contain them).
func (s *Store) TaggedItemIDs(ctx context.Context, kind ItemKind, tagIDs []int64) ([]int64, error) {
	if len(tagIDs) == 0 {
		return []int64{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT item_id FROM item_tags WHERE item_kind = ? AND tag_id IN (`+int64List(tagIDs)+`)`, kind)
	if err != nil {
		return nil, err
	}
	ids, err := collect(rows, scanID)
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

// TagSubtreeIDs returns the id of a tag and all its descendants.
func (s *Store) TagSubtreeIDs(ctx context.Context, tagID int64) ([]int64, error) {
	t, err := s.GetTag(ctx, tagID)
	if err != nil {
		return nil, err
	}
	sub, err := tagSubtree(ctx, s.db, t)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sub)+1)
	ids = append(ids, t.ID)
	for _, x := range sub {
		if x.ID != t.ID {
			ids = append(ids, x.ID)
		}
	}
	return ids, nil
}

// ==== Conversations ============================================================

// ChatMessageFilter selects chat messages for conversation aggregates.
type ChatMessageFilter struct {
	AccountIDs []int64
	// IDs restricts to these chat message ids (nil = all).
	IDs []int64
}

func (f ChatMessageFilter) where() string {
	conds := []string{`cm.deleted = 0`}
	if f.IDs != nil {
		if len(f.IDs) == 0 {
			return "0"
		}
		conds = append(conds, `cm.id IN (`+int64List(f.IDs)+`)`)
	}
	if len(f.AccountIDs) > 0 {
		conds = append(conds, `c.account_id IN (`+int64List(f.AccountIDs)+`)`)
	}
	return strings.Join(conds, " AND ")
}

// ConvQuery selects the conversations for a list. Mails and chat messages
// matching the filters are grouped by thread conversation (ByPerson false)
// or by counterpart person (ByPerson true; group chats keep their own
// conversation). A nil filter excludes that item type.
type ConvQuery struct {
	ByPerson bool
	Mail     *MessageFilter
	Chat     *ChatMessageFilter
}

// ConvAggregate summarizes the matching items of one conversation.
type ConvAggregate struct {
	ConversationID int64
	Last           time.Time
	Unread         int
	Mails          int
	ChatMessages   int
	AccountIDs     []int64
	Channels       []string // chat channels (mail is implied by Mails > 0)
	Group          bool     // contains a group chat
}

func splitIDs(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// AggregateConversations returns one aggregate per conversation with
// matching items (unordered).
func (s *Store) AggregateConversations(ctx context.Context, q ConvQuery) ([]ConvAggregate, error) {
	byID := map[int64]*ConvAggregate{}
	var order []int64
	get := func(id int64) *ConvAggregate {
		a := byID[id]
		if a == nil {
			a = &ConvAggregate{ConversationID: id}
			byID[id] = a
			order = append(order, id)
		}
		return a
	}
	if q.Mail != nil {
		where, args := q.Mail.where()
		conv, join := `m.conversation_id`, ``
		if q.ByPerson {
			conv = `pc.conversation_id`
			join = `JOIN message_counterparts mc ON mc.message_id = m.id JOIN person_conversations pc ON pc.person_id = mc.person_id`
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+conv+`, MAX(COALESCE(m.sent_at, m.received_at, 0)),
				SUM(m.flags & ? = 0), COUNT(*), GROUP_CONCAT(DISTINCT m.account_id)
			FROM messages m `+join+` WHERE `+conv+` IS NOT NULL AND `+where+` GROUP BY 1`, append([]any{FlagSeen}, args...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, last int64
			var unread, n int
			var accs sql.NullString
			if err := rows.Scan(&id, &last, &unread, &n, &accs); err != nil {
				rows.Close()
				return nil, err
			}
			a := get(id)
			a.Last = time.Unix(last, 0).UTC()
			a.Unread += unread
			a.Mails += n
			a.AccountIDs = append(a.AccountIDs, splitIDs(accs.String)...)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if q.Chat != nil {
		conv, join := `c.conversation_id`, ``
		if q.ByPerson {
			conv = `COALESCE(CASE WHEN c.kind = 'direct' THEN pc.conversation_id END, c.conversation_id)`
			join = `LEFT JOIN chat_counterparts cc ON cc.chat_id = c.id LEFT JOIN person_conversations pc ON pc.person_id = cc.person_id`
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+conv+` AS cid, MAX(COALESCE(cm.sent_at, 0)),
				SUM(cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0)), COUNT(*),
				GROUP_CONCAT(DISTINCT c.account_id), GROUP_CONCAT(DISTINCT COALESCE(c.channel, '')), MAX(c.kind <> 'direct')
			FROM chats c JOIN chat_messages cm ON cm.chat_id = c.id `+join+`
			WHERE cid IS NOT NULL AND `+q.Chat.where()+` GROUP BY 1`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, last int64
			var unread, n int
			var accs, chans sql.NullString
			var group bool
			if err := rows.Scan(&id, &last, &unread, &n, &accs, &chans, &group); err != nil {
				rows.Close()
				return nil, err
			}
			a := get(id)
			if t := time.Unix(last, 0).UTC(); t.After(a.Last) {
				a.Last = t
			}
			a.Unread += unread
			a.ChatMessages += n
			a.Group = a.Group || group
			a.AccountIDs = append(a.AccountIDs, splitIDs(accs.String)...)
			for _, ch := range strings.Split(chans.String, ",") {
				if ch != "" {
					a.Channels = append(a.Channels, ch)
				}
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	out := make([]ConvAggregate, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// GetConversations returns the conversations with the given ids by id.
func (s *Store) GetConversations(ctx context.Context, ids []int64) (map[int64]Conversation, error) {
	out := map[int64]Conversation{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+conversationCols+` FROM conversations WHERE id IN (`+int64List(ids)+`)`)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows, scanConversation)
	for _, c := range list {
		out[c.ID] = c
	}
	return out, err
}

// SetConversationFlags updates pinned, muted and archived of a conversation.
func (s *Store) SetConversationFlags(ctx context.Context, id int64, pinned, muted, archived bool) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE conversations SET pinned = ?, muted = ?, archived = ? WHERE id = ?`,
		boolInt(pinned), boolInt(muted), boolInt(archived), id))
}

// MessagesWithoutConversation returns live messages that are not assigned to
// a conversation yet (at most limit).
func (s *Store) MessagesWithoutConversation(ctx context.Context, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE conversation_id IS NULL AND +state = 'live'
		ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// MessageIDsOfPerson returns the live messages whose counterpart is the person.
func (s *Store) MessageIDsOfPerson(ctx context.Context, personID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id FROM messages m JOIN message_counterparts mc ON mc.message_id = m.id
		WHERE mc.person_id = ? AND m.state = 'live'`, personID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanID)
}

// MessageIDsOfConversation returns the live messages of a thread conversation.
func (s *Store) MessageIDsOfConversation(ctx context.Context, conversationID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE conversation_id = ? AND +state = 'live'`, conversationID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanID)
}

// DirectChatsOfPerson returns the direct chats with a person.
func (s *Store) DirectChatsOfPerson(ctx context.Context, personID int64) ([]Chat, error) {
	return s.listChats(ctx, `c.kind = 'direct' AND c.id IN (SELECT chat_id FROM chat_counterparts WHERE person_id = ?)`, personID)
}

// ChatCounterpart returns the counterpart person of a chat (0 if none).
func (s *Store) ChatCounterpart(ctx context.Context, chatID int64) (int64, error) {
	var p sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT person_id FROM chat_counterparts WHERE chat_id = ?`, chatID).Scan(&p)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return p.Int64, err
}

// ChatMessagesAfter returns chat messages of a chat sent after t (for marking read).
func (s *Store) LatestChatMessageTime(ctx context.Context, chatID int64) (time.Time, error) {
	var t sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(sent_at) FROM chat_messages WHERE chat_id = ?`, chatID).Scan(&t)
	return timeFromNull(t), err
}

// ChatOfMessage returns the chat id of a chat message.
func (s *Store) ChatOfMessage(ctx context.Context, chatMessageID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT chat_id FROM chat_messages WHERE id = ?`, chatMessageID).Scan(&id)
	return id, notFound(err)
}

// ChatMessagesByIDs returns chat messages by id.
func (s *Store) ChatMessagesByIDs(ctx context.Context, ids []int64) ([]ChatMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatMessageCols+` FROM chat_messages WHERE id IN (`+int64List(ids)+`)`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanChatMessage)
}

// MaxMessageID returns the highest message row id (0 if there are none);
// used to detect new mail cheaply.
func (s *Store) MaxMessageID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM messages`).Scan(&id)
	return id.Int64, err
}

// MaxChatID returns the highest chat row id (0 if there are none).
func (s *Store) MaxChatID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM chats`).Scan(&id)
	return id.Int64, err
}

// ParticipantsOfConversations returns the persons appearing in the mails
// and chat messages of thread conversations (conversation id -> person ids).
func (s *Store) ParticipantsOfConversations(ctx context.Context, ids []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	list := int64List(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT m.conversation_id, a.person_id FROM messages m
			JOIN message_addresses a ON a.message_id = m.id
			WHERE m.conversation_id IN (`+list+`) AND a.person_id IS NOT NULL AND +m.state = 'live'
		UNION
		SELECT DISTINCT c.conversation_id, cm.sender_person_id FROM chats c JOIN chat_messages cm ON cm.chat_id = c.id
			WHERE c.conversation_id IN (`+list+`) AND cm.sender_person_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return nil, err
		}
		out[c] = append(out[c], p)
	}
	return out, rows.Err()
}

// PersonCounts returns how many mails were sent to each person by the user
// (outgoing mails by counterpart), most frequent first.
func (s *Store) OutgoingCountsByPerson(ctx context.Context, since time.Time, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mc.person_id FROM message_counterparts mc JOIN messages m ON m.id = mc.message_id
		WHERE mc.outgoing = 1 AND mc.person_id IS NOT NULL AND COALESCE(m.sent_at, 0) >= ?
		GROUP BY mc.person_id HAVING COUNT(*) >= 2 ORDER BY COUNT(*) DESC LIMIT ?`, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanID)
}

// FolderCounts recomputes unread/total of the folders of an account from the
// local messages (used when no sync engine maintains the server counters).
func (s *Store) RecountFolders(ctx context.Context, accountID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE folders SET
		total  = (SELECT COUNT(*) FROM message_locations l JOIN messages m ON m.id = l.message_id WHERE l.folder_id = folders.id AND m.state = 'live'),
		unread = (SELECT COUNT(*) FROM message_locations l JOIN messages m ON m.id = l.message_id WHERE l.folder_id = folders.id AND m.state = 'live' AND m.flags & ? = 0)
		WHERE account_id = ?`, FlagSeen, accountID)
	return err
}

// AttachmentRow is a row of the attachments table.
type AttachmentRow struct {
	ID        int64
	MessageID int64
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
	BlobHash  string
}

// AttachmentsOf returns the attachment rows of a message in insertion order.
func (s *Store) AttachmentsOf(ctx context.Context, messageID int64) ([]AttachmentRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id, filename, mime_type, size, content_id, blob_hash FROM attachments
		WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (AttachmentRow, error) {
		var a AttachmentRow
		var name, mime, cid, blob sql.NullString
		var size sql.NullInt64
		err := sc.Scan(&a.ID, &a.MessageID, &name, &mime, &size, &cid, &blob)
		a.Filename, a.MIMEType, a.Size, a.ContentID, a.BlobHash = name.String, mime.String, size.Int64, cid.String, blob.String
		return a, err
	})
}
