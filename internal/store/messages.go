// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// MessageState is the lifecycle state of a logical message (KONZEPT 6.5).
type MessageState string

const (
	StateLive     MessageState = "live"
	StateOrphaned MessageState = "orphaned" // no known location; may reappear
	StateDeleted  MessageState = "deleted"  // orphaned for too long
)

// Flags is the bit set stored in messages.flags.
type Flags uint32

const (
	FlagSeen Flags = 1 << iota
	FlagAnswered
	FlagFlagged
	FlagDeleted
	FlagDraft
	FlagForwarded
)

var flagBits = []struct {
	bit  Flags
	flag domain.Flag
}{
	{FlagSeen, domain.FlagSeen},
	{FlagAnswered, domain.FlagAnswered},
	{FlagFlagged, domain.FlagFlagged},
	{FlagDeleted, domain.FlagDeleted},
	{FlagDraft, domain.FlagDraft},
	{FlagForwarded, domain.FlagForwarded},
}

// FlagsFromDomain converts protocol flags to the stored bit set. Unknown
// keywords are ignored.
func FlagsFromDomain(fs []domain.Flag) Flags {
	var out Flags
	for _, f := range fs {
		for _, b := range flagBits {
			if b.flag == f {
				out |= b.bit
			}
		}
	}
	return out
}

// Domain converts the bit set back to protocol flags.
func (f Flags) Domain() []domain.Flag {
	var out []domain.Flag
	for _, b := range flagBits {
		if f&b.bit != 0 {
			out = append(out, b.flag)
		}
	}
	return out
}

// Address roles in message_addresses.
const (
	RoleFrom    = "from"
	RoleTo      = "to"
	RoleCc      = "cc"
	RoleBcc     = "bcc"
	RoleReplyTo = "reply_to"
)

// MessageAddress is one row of message_addresses.
type MessageAddress struct {
	Role     string
	Name     string
	Email    string
	PersonID int64
}

// Message is a logical message (one row in messages), independent of where it is stored.
type Message struct {
	ID             int64
	AccountID      int64
	MessageIDHdr   string // normalized Message-ID; "" if the message has none
	Fingerprint    string // required fallback key (KONZEPT 6.2)
	ServerObjectID string
	ServerThreadID string
	Subject        string
	From           string
	SentAt         time.Time
	ReceivedAt     time.Time
	Size           int64
	BlobHash       string
	Snippet        string
	HasAttachments bool
	Flags          Flags
	ConversationID int64
	State          MessageState
	OrphanedSince  time.Time
	CreatedAt      time.Time
}

// Key returns the stable key used in message_relations and conversation_overrides:
// the Message-ID if present, otherwise "fp:" + fingerprint.
func (m Message) Key() string {
	if m.MessageIDHdr != "" {
		return m.MessageIDHdr
	}
	return "fp:" + m.Fingerprint
}

// Location is a place where a message is stored on the server.
type Location struct {
	MessageID int64
	FolderID  int64
	RemoteRef string // IMAP UID, EAS ServerId, Graph id ...
}

const messageCols = `id, account_id, message_id_hdr, fingerprint, server_obj_id, server_thread_id, subject, from_addr,
	sent_at, received_at, size, blob_hash, snippet, has_attachments, flags, conversation_id, state, orphaned_since, created_at`

func scanMessage(sc scanner) (Message, error) {
	var (
		m                                         Message
		mid, objID, thrID, subj, from, blob, snip sql.NullString
		sent, recv, conv, orphaned                sql.NullInt64
		created                                   int64
	)
	err := sc.Scan(&m.ID, &m.AccountID, &mid, &m.Fingerprint, &objID, &thrID, &subj, &from,
		&sent, &recv, &m.Size, &blob, &snip, &m.HasAttachments, &m.Flags, &conv, &m.State, &orphaned, &created)
	m.MessageIDHdr, m.ServerObjectID, m.ServerThreadID = mid.String, objID.String, thrID.String
	m.Subject, m.From, m.BlobHash, m.Snippet = subj.String, from.String, blob.String, snip.String
	m.SentAt, m.ReceivedAt, m.OrphanedSince = timeFromNull(sent), timeFromNull(recv), timeFromNull(orphaned)
	m.ConversationID = conv.Int64
	m.CreatedAt = time.Unix(created, 0).UTC()
	return m, err
}

// findExisting looks up a message by its stable keys in priority order
// (KONZEPT 6.2): Message-ID, server object id, fingerprint.
func findExisting(ctx context.Context, q querier, m Message) (int64, error) {
	try := func(query string, args ...any) (int64, error) {
		var id int64
		err := q.QueryRowContext(ctx, query, args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	if m.MessageIDHdr != "" {
		return try(`SELECT id FROM messages WHERE account_id = ? AND message_id_hdr = ?`, m.AccountID, m.MessageIDHdr)
	}
	if m.ServerObjectID != "" {
		id, err := try(`SELECT id FROM messages WHERE account_id = ? AND server_obj_id = ? ORDER BY id LIMIT 1`,
			m.AccountID, m.ServerObjectID)
		if id != 0 || err != nil {
			return id, err
		}
	}
	return try(`SELECT id FROM messages WHERE account_id = ? AND message_id_hdr IS NULL AND fingerprint = ?`,
		m.AccountID, m.Fingerprint)
}

// UpsertMessage inserts a message or updates the existing one with the same
// stable key (Message-ID, else server object id, else fingerprint) and returns
// its id and whether it was newly created.
//
// On update, the state, conversation and creation time are preserved; empty
// BlobHash/ServerObjectID/ServerThreadID and a zero ConversationID do not
// overwrite stored values. Use AddLocation to (re)attach a location.
func (s *Store) UpsertMessage(ctx context.Context, m Message) (id int64, created bool, err error) {
	if m.Fingerprint == "" {
		return 0, false, errors.New("store: upsert message: fingerprint is required")
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		id, err = findExisting(ctx, tx, m)
		if err != nil {
			return err
		}
		if id == 0 {
			created = true
			return tx.QueryRowContext(ctx, `INSERT INTO messages(account_id, message_id_hdr, fingerprint, server_obj_id,
				server_thread_id, subject, from_addr, sent_at, received_at, size, blob_hash, snippet, has_attachments,
				flags, conversation_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
				m.AccountID, nullString(m.MessageIDHdr), m.Fingerprint, nullString(m.ServerObjectID),
				nullString(m.ServerThreadID), nullString(m.Subject), nullString(m.From), unixOrNull(m.SentAt),
				unixOrNull(m.ReceivedAt), m.Size, nullString(m.BlobHash), nullString(m.Snippet),
				boolInt(m.HasAttachments), m.Flags, nullID(m.ConversationID), time.Now().Unix()).Scan(&id)
		}
		_, err = tx.ExecContext(ctx, `UPDATE messages SET
				server_obj_id = COALESCE(?, server_obj_id), server_thread_id = COALESCE(?, server_thread_id),
				subject = ?, from_addr = ?, sent_at = ?, received_at = COALESCE(?, received_at), size = ?,
				blob_hash = COALESCE(?, blob_hash), snippet = COALESCE(?, snippet), has_attachments = ?, flags = ?,
				conversation_id = COALESCE(?, conversation_id)
			WHERE id = ?`,
			nullString(m.ServerObjectID), nullString(m.ServerThreadID), nullString(m.Subject), nullString(m.From),
			unixOrNull(m.SentAt), unixOrNull(m.ReceivedAt), m.Size, nullString(m.BlobHash), nullString(m.Snippet),
			boolInt(m.HasAttachments), m.Flags, nullID(m.ConversationID), id)
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("store: upsert message: %w", err)
	}
	return id, created, nil
}

// GetMessage returns the message with the given id.
func (s *Store) GetMessage(ctx context.Context, id int64) (Message, error) {
	m, err := scanMessage(s.q(ctx).QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
	return m, notFound(err)
}

func (s *Store) findOne(ctx context.Context, where string, args ...any) (Message, error) {
	m, err := scanMessage(s.q(ctx).QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE `+where+` ORDER BY id LIMIT 1`, args...))
	return m, notFound(err)
}

// FindMessageByMessageID finds a message by its normalized Message-ID header.
func (s *Store) FindMessageByMessageID(ctx context.Context, accountID int64, messageID string) (Message, error) {
	return s.findOne(ctx, `account_id = ? AND message_id_hdr = ?`, accountID, messageID)
}

// FindMessageByServerObjectID finds a message by its server object id (EMAILID, X-GM-MSGID ...).
func (s *Store) FindMessageByServerObjectID(ctx context.Context, accountID int64, objID string) (Message, error) {
	return s.findOne(ctx, `account_id = ? AND server_obj_id = ?`, accountID, objID)
}

// FindMessageByFingerprint finds a message by its fingerprint.
func (s *Store) FindMessageByFingerprint(ctx context.Context, accountID int64, fingerprint string) (Message, error) {
	return s.findOne(ctx, `account_id = ? AND fingerprint = ?`, accountID, fingerprint)
}

// FindMessageByLocation finds the message stored at (folder, remote ref).
func (s *Store) FindMessageByLocation(ctx context.Context, folderID int64, remoteRef string) (Message, error) {
	return s.findOne(ctx, `id = (SELECT message_id FROM message_locations WHERE folder_id = ? AND remote_ref = ?)`,
		folderID, remoteRef)
}

// ListMessagesByConversation returns the live messages of a conversation in chronological order.
func (s *Store) ListMessagesByConversation(ctx context.Context, conversationID int64) ([]Message, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE conversation_id = ? AND +state = 'live' ORDER BY sent_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// ListMessagesInFolder returns the messages located in a folder, newest first.
func (s *Store) ListMessagesInFolder(ctx context.Context, folderID int64, limit, offset int) ([]Message, error) {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+prefixed("m.", messageCols)+` FROM messages m
		JOIN message_locations l ON l.message_id = m.id
		WHERE l.folder_id = ? ORDER BY m.sent_at DESC, m.id DESC LIMIT ? OFFSET ?`, folderID, limit, offset)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// SetMessageFlags replaces the flags of a message.
func (s *Store) SetMessageFlags(ctx context.Context, id int64, f Flags) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE messages SET flags = ? WHERE id = ?`, f, id))
}

// SetMessageBlob sets the blob hash of the raw message.
func (s *Store) SetMessageBlob(ctx context.Context, id int64, hash string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE messages SET blob_hash = ? WHERE id = ?`, nullString(hash), id))
}

// DeleteMessage removes a message row completely (relations are kept, they hang on keys).
func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id))
}

// ==== Addresses ===============================================================

// SetMessageAddresses replaces all addresses of a message.
func (s *Store) SetMessageAddresses(ctx context.Context, messageID int64, addrs []MessageAddress) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_addresses WHERE message_id = ?`, messageID); err != nil {
			return err
		}
		for _, a := range addrs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO message_addresses(message_id, role, name, email, person_id)
				VALUES (?, ?, ?, ?, ?)`, messageID, a.Role, nullString(a.Name), a.Email, nullID(a.PersonID)); err != nil {
				return err
			}
		}
		return nil
	})
}

// MessageAddresses returns the addresses of a message in insertion order.
func (s *Store) MessageAddresses(ctx context.Context, messageID int64) ([]MessageAddress, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT role, name, email, person_id FROM message_addresses
		WHERE message_id = ? ORDER BY rowid`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (MessageAddress, error) {
		var (
			a    MessageAddress
			name sql.NullString
			pid  sql.NullInt64
		)
		err := sc.Scan(&a.Role, &name, &a.Email, &pid)
		a.Name, a.PersonID = name.String, pid.Int64
		return a, err
	})
}

// AddressesFromHeader converts parsed header addresses into message_addresses rows.
func AddressesFromHeader(h domain.MessageHeaderInfo) []MessageAddress {
	var out []MessageAddress
	add := func(role string, as []domain.Address) {
		for _, a := range as {
			out = append(out, MessageAddress{Role: role, Name: a.Name, Email: a.Email})
		}
	}
	add(RoleFrom, h.From)
	add(RoleTo, h.To)
	add(RoleCc, h.Cc)
	add(RoleBcc, h.Bcc)
	add(RoleReplyTo, h.ReplyTo)
	return out
}

// ==== Locations, orphaning and reattaching (KONZEPT 6.5) ======================

// AddLocation records that a message is stored at (folder, remote ref). If the
// message was orphaned or deleted it becomes live again (reattach). An existing
// location with the same (folder, remote ref) is reassigned to this message.
func (s *Store) AddLocation(ctx context.Context, loc Location) error {
	return s.withTx(ctx, func(tx *sql.Tx) error { return addLocation(ctx, tx, loc) })
}

func addLocation(ctx context.Context, tx *sql.Tx, loc Location) error {
	if loc.RemoteRef == "" {
		return errors.New("store: add location: empty remote ref")
	}
	// A previous occupant of this slot (e.g. after UIDVALIDITY reuse) loses it.
	var prev int64
	err := tx.QueryRowContext(ctx, `SELECT message_id FROM message_locations WHERE folder_id = ? AND remote_ref = ?`,
		loc.FolderID, loc.RemoteRef).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if prev == loc.MessageID {
		return reattach(ctx, tx, loc.MessageID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_locations(message_id, folder_id, remote_ref) VALUES (?, ?, ?)
		ON CONFLICT(folder_id, remote_ref) DO UPDATE SET message_id = excluded.message_id`,
		loc.MessageID, loc.FolderID, loc.RemoteRef); err != nil {
		return err
	}
	if prev != 0 {
		if err := orphanIfUnlocated(ctx, tx, prev, time.Now()); err != nil {
			return err
		}
	}
	return reattach(ctx, tx, loc.MessageID)
}

func reattach(ctx context.Context, tx *sql.Tx, messageID int64) error {
	return checkAffected(tx.ExecContext(ctx, `UPDATE messages SET state = 'live', orphaned_since = NULL WHERE id = ?`, messageID))
}

// RemoveLocation removes the location (folder, remote ref). If the message has
// no location left it becomes orphaned. It returns the affected message id, or
// ErrNotFound if the location was unknown.
func (s *Store) RemoveLocation(ctx context.Context, folderID int64, remoteRef string) (int64, error) {
	var mid int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		mid, err = removeLocation(ctx, tx, folderID, remoteRef)
		if err != nil {
			return err
		}
		return orphanIfUnlocated(ctx, tx, mid, time.Now())
	})
	return mid, err
}

func removeLocation(ctx context.Context, tx *sql.Tx, folderID int64, remoteRef string) (int64, error) {
	var mid int64
	err := tx.QueryRowContext(ctx, `DELETE FROM message_locations WHERE folder_id = ? AND remote_ref = ? RETURNING message_id`,
		folderID, remoteRef).Scan(&mid)
	return mid, notFound(err)
}

// MoveLocation moves a message from one location to another in a single
// transaction (client-side move with known COPYUID/MOVE result). If newRef is
// empty (destination id unknown) only the old location is removed and the
// message is orphaned until the next sync of the destination reattaches it.
func (s *Store) MoveLocation(ctx context.Context, fromFolderID int64, fromRef string, toFolderID int64, newRef string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		mid, err := removeLocation(ctx, tx, fromFolderID, fromRef)
		if err != nil {
			return err
		}
		if newRef == "" {
			return orphanIfUnlocated(ctx, tx, mid, time.Now())
		}
		return addLocation(ctx, tx, Location{MessageID: mid, FolderID: toFolderID, RemoteRef: newRef})
	})
}

// RemoveAllLocations removes every location of a folder (e.g. after a full
// resync because UIDVALIDITY changed) and orphans messages left without a
// location. It returns the number of removed locations.
func (s *Store) RemoveAllLocations(ctx context.Context, folderID int64) (int64, error) {
	var n int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `DELETE FROM message_locations WHERE folder_id = ? RETURNING message_id`, folderID)
		if err != nil {
			return err
		}
		ids, err := collect(rows, func(sc scanner) (int64, error) {
			var v int64
			return v, sc.Scan(&v)
		})
		if err != nil {
			return err
		}
		n = int64(len(ids))
		now := time.Now()
		for _, id := range ids {
			if err := orphanIfUnlocated(ctx, tx, id, now); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// Locations returns all locations of a message.
func (s *Store) Locations(ctx context.Context, messageID int64) ([]Location, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT message_id, folder_id, remote_ref FROM message_locations
		WHERE message_id = ? ORDER BY folder_id, remote_ref`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (Location, error) {
		var l Location
		return l, sc.Scan(&l.MessageID, &l.FolderID, &l.RemoteRef)
	})
}

// orphanIfUnlocated marks a live message as orphaned if it has no location left.
func orphanIfUnlocated(ctx context.Context, q querier, messageID int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE messages SET state = 'orphaned', orphaned_since = ?
		WHERE id = ? AND state = 'live'
		  AND NOT EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id)`, now.Unix(), messageID)
	return err
}

// MarkOrphaned marks a message as orphaned regardless of its locations, which
// are removed. Reattach it with AddLocation.
func (s *Store) MarkOrphaned(ctx context.Context, messageID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_locations WHERE message_id = ?`, messageID); err != nil {
			return err
		}
		return checkAffected(tx.ExecContext(ctx, `UPDATE messages SET state = 'orphaned',
			orphaned_since = COALESCE(orphaned_since, ?) WHERE id = ? AND state <> 'deleted'`, time.Now().Unix(), messageID))
	})
}

// GCOrphaned marks messages that have been orphaned since before now-maxAge as
// deleted and returns how many were affected. Relations are kept because they
// are keyed by message keys (KONZEPT 6.5).
func (s *Store) GCOrphaned(ctx context.Context, maxAge time.Duration, now time.Time) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE messages SET state = 'deleted'
		WHERE state = 'orphaned' AND orphaned_since <= ?`, now.Add(-maxAge).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func prefixed(prefix, cols string) string {
	out := make([]byte, 0, len(cols)*2)
	start := true
	for i := 0; i < len(cols); i++ {
		c := cols[i]
		if start && c != ' ' && c != '\t' && c != '\n' {
			out = append(out, prefix...)
			start = false
		}
		if c == ',' {
			start = true
		}
		out = append(out, c)
	}
	return string(out)
}
