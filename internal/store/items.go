// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ItemKind is the kind of a stored item that tags, projects and search folder
// hits can refer to (polymorphic references: kind + row id).
type ItemKind string

const (
	ItemMessage      ItemKind = "message"      // messages
	ItemConversation ItemKind = "conversation" // conversations
	ItemChat         ItemKind = "chat"         // chats
	ItemChatMessage  ItemKind = "chat_message" // chat_messages
	ItemEvent        ItemKind = "event"        // cal_objects (kind event)
	ItemTask         ItemKind = "task"         // cal_objects (kind todo)
	ItemContact      ItemKind = "contact"      // contacts
	ItemPerson       ItemKind = "person"       // persons
	ItemFile         ItemKind = "file"         // cloud_links
	ItemSearchFolder ItemKind = "search_folder"
	ItemProject      ItemKind = "project" // only as a tagged item
)

// AllItemKinds lists every item kind.
var AllItemKinds = []ItemKind{ItemMessage, ItemConversation, ItemChat, ItemChatMessage, ItemEvent, ItemTask,
	ItemContact, ItemPerson, ItemFile, ItemSearchFolder, ItemProject}

// Valid reports whether k is a known item kind.
func (k ItemKind) Valid() bool {
	for _, x := range AllItemKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Scope returns the search scope of items of this kind (ok=false for kinds
// the unified search does not return, e.g. conversations).
func (k ItemKind) Scope() (SearchScope, bool) {
	switch k {
	case ItemMessage:
		return ScopeMail, true
	case ItemChatMessage:
		return ScopeChat, true
	case ItemEvent:
		return ScopeEvent, true
	case ItemTask:
		return ScopeTask, true
	case ItemContact:
		return ScopeContact, true
	case ItemFile:
		return ScopeFile, true
	}
	return "", false
}

// ItemKindForScope returns the item kind of search hits of a scope.
func ItemKindForScope(sc SearchScope) ItemKind {
	switch sc {
	case ScopeMail:
		return ItemMessage
	case ScopeChat:
		return ItemChatMessage
	case ScopeEvent:
		return ItemEvent
	case ScopeTask:
		return ItemTask
	case ScopeContact:
		return ItemContact
	case ScopeFile:
		return ItemFile
	}
	return ""
}

// Item references a stored item.
type Item struct {
	Kind ItemKind `json:"kind"`
	ID   int64    `json:"id"`
}

func (it Item) String() string { return string(it.Kind) + ":" + fmt.Sprint(it.ID) }

// ParseItem parses "kind:id" (as produced by Item.String).
func ParseItem(s string) (Item, error) {
	k, id, ok := strings.Cut(s, ":")
	var it Item
	if !ok {
		return it, fmt.Errorf("store: invalid item %q (want kind:id)", s)
	}
	it.Kind = ItemKind(k)
	if _, err := fmt.Sscan(id, &it.ID); err != nil || it.ID <= 0 || !it.Kind.Valid() {
		return Item{}, fmt.Errorf("store: invalid item %q (want kind:id)", s)
	}
	return it, nil
}

// HitItem returns the item a search hit refers to.
func HitItem(h SearchHit) Item { return Item{Kind: ItemKindForScope(h.Kind), ID: h.ID} }

// itemTable returns the table and extra condition holding items of kind k.
func itemTable(k ItemKind) (table, extra string, ok bool) {
	switch k {
	case ItemMessage:
		return "messages", "", true
	case ItemConversation:
		return "conversations", "", true
	case ItemChat:
		return "chats", "", true
	case ItemChatMessage:
		return "chat_messages", "", true
	case ItemEvent:
		return "cal_objects", " AND kind = 'event'", true
	case ItemTask:
		return "cal_objects", " AND kind = 'todo'", true
	case ItemContact:
		return "contacts", "", true
	case ItemPerson:
		return "persons", "", true
	case ItemFile:
		return "cloud_links", "", true
	case ItemSearchFolder:
		return "search_folders", "", true
	case ItemProject:
		return "projects", "", true
	}
	return "", "", false
}

// checkItems verifies that all items exist.
func checkItems(ctx context.Context, q querier, items []Item) error {
	for _, it := range items {
		table, extra, ok := itemTable(it.Kind)
		if !ok {
			return fmt.Errorf("store: unknown item kind %q", it.Kind)
		}
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`+extra, it.ID).Scan(&one)
		if err != nil {
			return fmt.Errorf("store: item %s: %w", it, notFound(err))
		}
	}
	return nil
}

// ItemExists reports whether the item exists.
func (s *Store) ItemExists(ctx context.Context, it Item) (bool, error) {
	err := checkItems(ctx, s.db, []Item{it})
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), ErrNotFound.Error()) {
		return false, nil
	}
	return false, err
}

// Message key prefixes understood by FindMessageByKey (see Message.Key and
// internal/core/threading).
const (
	keyPrefixFP       = "fp:"
	keyPrefixFPThread = "~fp:"
	keyPrefixObj      = "~obj:"
)

// FindMessageByKey looks up a message of an account by its stable key: a
// normalized Message-ID, "fp:<fingerprint>" / "~fp:<fingerprint>" or
// "~obj:<server object id>". accountID 0 searches all accounts (first match).
func (s *Store) FindMessageByKey(ctx context.Context, accountID int64, key string) (Message, error) {
	acc := ""
	args := []any{}
	if accountID != 0 {
		acc = " AND account_id = ?"
		args = append(args, accountID)
	}
	switch {
	case strings.HasPrefix(key, keyPrefixFP):
		return s.findOne(ctx, `fingerprint = ?`+acc, append([]any{strings.TrimPrefix(key, keyPrefixFP)}, args...)...)
	case strings.HasPrefix(key, keyPrefixFPThread):
		return s.findOne(ctx, `fingerprint = ?`+acc, append([]any{strings.TrimPrefix(key, keyPrefixFPThread)}, args...)...)
	case strings.HasPrefix(key, keyPrefixObj):
		return s.findOne(ctx, `server_obj_id = ?`+acc, append([]any{strings.TrimPrefix(key, keyPrefixObj)}, args...)...)
	}
	return s.findOne(ctx, `message_id_hdr = ?`+acc, append([]any{key}, args...)...)
}

// FindChatMessageByRemoteID looks up a chat message by its remote id within
// an account (0 = any account).
func (s *Store) FindChatMessageByRemoteID(ctx context.Context, accountID int64, remoteID string) (int64, error) {
	q := `SELECT cm.id FROM chat_messages cm JOIN chats c ON c.id = cm.chat_id WHERE cm.remote_id = ?`
	args := []any{remoteID}
	if accountID != 0 {
		q += ` AND c.account_id = ?`
		args = append(args, accountID)
	}
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, q+` ORDER BY cm.id LIMIT 1`, args...).Scan(&id)
	return id, notFound(err)
}

// FindCloudLinkByPath looks up a cloud link by its remote path within an
// account (0 = any account).
func (s *Store) FindCloudLinkByPath(ctx context.Context, accountID int64, path string) (int64, error) {
	q := `SELECT id FROM cloud_links WHERE remote_path = ?`
	args := []any{path}
	if accountID != 0 {
		q += ` AND account_id = ?`
		args = append(args, accountID)
	}
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, q+` ORDER BY id LIMIT 1`, args...).Scan(&id)
	return id, notFound(err)
}

// AttachmentInfo is the metadata of a mail attachment.
type AttachmentInfo struct {
	Filename string
	MIMEType string
	Size     int64
}

// MessageAttachments returns the attachment metadata of a message.
func (s *Store) MessageAttachments(ctx context.Context, messageID int64) ([]AttachmentInfo, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT COALESCE(filename, ''), COALESCE(mime_type, ''), COALESCE(size, 0)
		FROM attachments WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (AttachmentInfo, error) {
		var a AttachmentInfo
		return a, sc.Scan(&a.Filename, &a.MIMEType, &a.Size)
	})
}

// ItemSummary is a compact description of an item (for timelines, events
// and lists).
type ItemSummary struct {
	Item
	Title     string
	Snippet   string
	Sender    string
	Time      time.Time
	AccountID int64
	Unread    bool
}

// summarySQL returns a SELECT of (title, snippet, sender, time, account,
// unread) for one item of kind k with id = ?; the alias l must provide
// added_at as a fallback time.
var summarySQL = map[ItemKind]string{
	ItemMessage: `SELECT COALESCE(m.subject, ''), COALESCE(m.snippet, ''), COALESCE(m.from_addr, ''),
		COALESCE(m.sent_at, m.received_at, 0), m.account_id, (m.flags & 1) = 0 FROM messages m WHERE m.id = ?`,
	ItemConversation: `SELECT COALESCE(c.title, ''), '', '', COALESCE(c.last_activity, 0), 0, c.unread_count > 0
		FROM conversations c WHERE c.id = ?`,
	ItemChat: `SELECT COALESCE(c.title, ''), '', '', COALESCE((SELECT MAX(sent_at) FROM chat_messages WHERE chat_id = c.id), 0),
		c.account_id, 0 FROM chats c WHERE c.id = ?`,
	ItemChatMessage: `SELECT COALESCE(c.title, ''), COALESCE(substr(cm.body, 1, 200), cm.media_name, ''),
		COALESCE((SELECT display_name FROM persons WHERE id = cm.sender_person_id), ''), COALESCE(cm.sent_at, 0), c.account_id,
		cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0)
		FROM chat_messages cm JOIN chats c ON c.id = cm.chat_id WHERE cm.id = ?`,
	ItemEvent: `SELECT COALESCE(o.summary, ''), '', '', COALESCE(o.dtstart, 0), COALESCE(cal.account_id, 0), 0
		FROM cal_objects o JOIN calendars cal ON cal.id = o.calendar_id WHERE o.id = ? AND o.kind = 'event'`,
	ItemTask: `SELECT COALESCE(o.summary, ''), COALESCE(o.status, ''), '', COALESCE(o.due_at, o.dtstart, 0), COALESCE(cal.account_id, 0),
		0 FROM cal_objects o JOIN calendars cal ON cal.id = o.calendar_id WHERE o.id = ? AND o.kind = 'todo'`,
	ItemContact: `SELECT COALESCE(ct.display_name, ''), COALESCE((SELECT group_concat(email, ', ') FROM contact_emails WHERE contact_id = ct.id), ''),
		'', 0, COALESCE(ab.account_id, 0), 0 FROM contacts ct JOIN addressbooks ab ON ab.id = ct.addressbook_id WHERE ct.id = ?`,
	ItemPerson:       `SELECT COALESCE(p.display_name, ''), '', '', 0, 0, 0 FROM persons p WHERE p.id = ?`,
	ItemFile:         `SELECT COALESCE(cl.file_name, cl.remote_path, cl.url, ''), COALESCE(cl.url, ''), '', COALESCE(cl.created_at, 0), COALESCE(cl.account_id, 0), 0 FROM cloud_links cl WHERE cl.id = ?`,
	ItemSearchFolder: `SELECT sf.name, sf.query, '', sf.created_at, COALESCE(sf.account_id, 0), 0 FROM search_folders sf WHERE sf.id = ?`,
	ItemProject:      `SELECT p.name, COALESCE(p.key, ''), '', p.created_at, 0, 0 FROM projects p WHERE p.id = ?`,
}

// ItemSummary returns a compact description of an item.
func (s *Store) ItemSummary(ctx context.Context, it Item) (ItemSummary, error) {
	q, ok := summarySQL[it.Kind]
	if !ok {
		return ItemSummary{}, fmt.Errorf("store: unknown item kind %q", it.Kind)
	}
	sum := ItemSummary{Item: it}
	var ts int64
	var acc sql.NullInt64
	err := s.q(ctx).QueryRowContext(ctx, q, it.ID).Scan(&sum.Title, &sum.Snippet, &sum.Sender, &ts, &acc, &sum.Unread)
	if err != nil {
		return ItemSummary{}, fmt.Errorf("store: item %s: %w", it, notFound(err))
	}
	sum.AccountID = acc.Int64
	if ts != 0 {
		sum.Time = time.Unix(ts, 0).UTC()
	}
	return sum, nil
}
