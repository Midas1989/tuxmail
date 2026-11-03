// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Store functions used by the sync engine (internal/sync).

// MessageAttachment is one row of the attachments table.
type MessageAttachment struct {
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
	BlobHash  string // "" = not stored separately (part of the raw message)
}

// SetMessageAttachments replaces the attachment rows of a message.
func (s *Store) SetMessageAttachments(ctx context.Context, messageID int64, atts []MessageAttachment) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM attachments WHERE message_id = ?`, messageID); err != nil {
			return err
		}
		for _, a := range atts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO attachments(message_id, filename, mime_type, size, content_id, blob_hash)
				VALUES (?, ?, ?, ?, ?, ?)`, messageID, nullString(a.Filename), nullString(a.MIMEType), a.Size,
				nullString(a.ContentID), nullString(a.BlobHash)); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetMessageContent stores the results of downloading and parsing the raw
// message: blob hash, snippet and the attachment flag.
func (s *Store) SetMessageContent(ctx context.Context, id int64, blobHash, snippet string, hasAttachments bool) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE messages SET blob_hash = COALESCE(?, blob_hash), snippet = ?,
		has_attachments = ? WHERE id = ?`, nullString(blobHash), nullString(snippet), boolInt(hasAttachments), id))
}

// RefreshFolderCounts recomputes the total and unread counters of a folder
// from the local locations of live messages and returns them.
func (s *Store) RefreshFolderCounts(ctx context.Context, folderID int64) (total, unread int, err error) {
	err = s.db.QueryRowContext(ctx, `UPDATE folders SET
			total  = (SELECT COUNT(DISTINCT l.message_id) FROM message_locations l JOIN messages m ON m.id = l.message_id
			          WHERE l.folder_id = ?1 AND m.state = 'live'),
			unread = (SELECT COUNT(DISTINCT l.message_id) FROM message_locations l JOIN messages m ON m.id = l.message_id
			          WHERE l.folder_id = ?1 AND m.state = 'live' AND m.flags & ?2 = 0)
		WHERE id = ?1 RETURNING total, unread`, folderID, FlagSeen).Scan(&total, &unread)
	return total, unread, notFound(err)
}

// GCOrphanedMessages works like GCOrphaned but returns the messages that
// were marked deleted, so that callers can update dependent state (e.g.
// remove them from their conversation).
func (s *Store) GCOrphanedMessages(ctx context.Context, maxAge time.Duration, now time.Time) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `UPDATE messages SET state = 'deleted'
		WHERE state = 'orphaned' AND orphaned_since <= ? RETURNING `+messageCols, now.Add(-maxAge).Unix())
	if err != nil {
		return nil, err
	}
	return collect(rows, scanMessage)
}

// UpdateFolderPath changes remote id, name and parent of a folder in place
// (keeping its id, sync state and message locations) and rewrites the
// remote ids of its subfolders that start with the old path followed by the
// delimiter.
func (s *Store) UpdateFolderPath(ctx context.Context, folderID int64, remoteID, name string, parentID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var oldRemote string
		var delim sql.NullString
		var acc int64
		if err := tx.QueryRowContext(ctx, `SELECT account_id, remote_id, delimiter FROM folders WHERE id = ?`, folderID).
			Scan(&acc, &oldRemote, &delim); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE folders SET remote_id = ?, name = ?, parent_id = ? WHERE id = ?`,
			remoteID, name, nullID(parentID), folderID); err != nil {
			return fmt.Errorf("store: update folder path: %w", err)
		}
		if delim.String == "" || oldRemote == remoteID {
			return nil
		}
		prefix := oldRemote + delim.String
		_, err := tx.ExecContext(ctx, `UPDATE folders SET remote_id = ? || substr(remote_id, ?)
			WHERE account_id = ? AND substr(remote_id, 1, ?) = ?`,
			remoteID+delim.String, len(prefix)+1, acc, len(prefix), prefix)
		return err
	})
}

// ClaimNextAccountOp works like ClaimNextOp but only considers operations of
// one account whose kind is one of kinds (all kinds if empty).
func (s *Store) ClaimNextAccountOp(ctx context.Context, now time.Time, accountID int64, kinds ...string) (op Op, ok bool, err error) {
	args := []any{now.Unix(), accountID}
	kindCond := ""
	if len(kinds) > 0 {
		kindCond = ` AND o.kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	op, err = scanOp(s.db.QueryRowContext(ctx, `UPDATE ops SET state = 'running', attempts = attempts + 1
		WHERE id = (
			SELECT o.id FROM ops o
			WHERE o.state = 'pending' AND o.next_try_at <= ? AND o.account_id = ?`+kindCond+`
			  AND NOT EXISTS (SELECT 1 FROM ops p WHERE p.account_id = o.account_id AND p.id <> o.id
			                  AND (p.state = 'running' OR (p.state = 'pending' AND p.id < o.id)))
			ORDER BY o.next_try_at, o.id LIMIT 1)
		RETURNING `+opCols, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Op{}, false, nil
	}
	if err != nil {
		return Op{}, false, fmt.Errorf("store: claim op: %w", err)
	}
	return op, true, nil
}

// NextAccountOpDue returns when the next pending operation of an account
// (restricted to kinds if given) becomes due. ok is false if none is pending.
func (s *Store) NextAccountOpDue(ctx context.Context, accountID int64, kinds ...string) (due time.Time, ok bool, err error) {
	args := []any{accountID}
	kindCond := ""
	if len(kinds) > 0 {
		kindCond = ` AND kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	var v sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT MIN(next_try_at) FROM ops WHERE state = 'pending' AND account_id = ?`+kindCond,
		args...).Scan(&v)
	if err != nil || !v.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(v.Int64, 0), true, nil
}

// ReleaseOp puts a running operation back into the pending state without
// counting the attempt (e.g. when the server was unreachable), due at
// nextTry.
func (s *Store) ReleaseOp(ctx context.Context, id int64, cause error, nextTry time.Time) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE ops SET state = 'pending', attempts = MAX(attempts - 1, 0),
		last_error = ?, next_try_at = ? WHERE id = ? AND state = 'running'`, nullString(msg), nextTry.Unix(), id))
}

// MarkOpRetry records a failed attempt like MarkOpFailed, but with a
// caller-chosen retry time.
func (s *Store) MarkOpRetry(ctx context.Context, id int64, cause error, nextTry time.Time, maxAttempts int, permanent bool) (OpState, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	var state OpState
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var attempts int
		if err := tx.QueryRowContext(ctx, `SELECT attempts FROM ops WHERE id = ?`, id).Scan(&attempts); err != nil {
			return notFound(err)
		}
		state = OpPending
		if permanent || attempts >= maxAttempts {
			state = OpFailed
		}
		_, err := tx.ExecContext(ctx, `UPDATE ops SET state = ?, last_error = ?, next_try_at = ? WHERE id = ?`,
			state, msg, nextTry.Unix(), id)
		return err
	})
	return state, err
}

// BodyTask locates a message whose raw content has not been downloaded.
type BodyTask struct {
	MessageID      int64
	FolderID       int64
	FolderRemoteID string
	RemoteRef      string
}

// MessagesWithoutBody returns up to limit live messages of an account
// without a stored raw message, newest first, each with one of its
// locations. Messages sent before notBefore and messages larger than
// maxSize (0 = no limit) are skipped.
// Locations whose remote ref starts with skipRefPrefix (placeholders of
// pending operations) are ignored.
func (s *Store) MessagesWithoutBody(ctx context.Context, accountID int64, notBefore time.Time, maxSize int64, skipRefPrefix string, limit int) ([]BodyTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, f.id, f.remote_id, MIN(l.remote_ref)
		FROM messages m
		JOIN message_locations l ON l.message_id = m.id
		JOIN folders f ON f.id = l.folder_id
		WHERE m.account_id = ? AND m.state = 'live' AND m.blob_hash IS NULL
		  AND COALESCE(m.sent_at, m.received_at, 0) >= ? AND (? = 0 OR m.size <= ?)
		  AND (? = '' OR substr(l.remote_ref, 1, length(?)) <> ?)
		GROUP BY m.id
		ORDER BY COALESCE(m.sent_at, m.received_at, 0) DESC, m.id DESC LIMIT ?`,
		accountID, unixOrZero(notBefore), maxSize, maxSize, skipRefPrefix, skipRefPrefix, skipRefPrefix, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (BodyTask, error) {
		var t BodyTask
		return t, sc.Scan(&t.MessageID, &t.FolderID, &t.FolderRemoteID, &t.RemoteRef)
	})
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// LocationsByPrefix returns all locations of an account's folders whose
// remote ref starts with prefix (e.g. placeholders of pending operations).
func (s *Store) LocationsByPrefix(ctx context.Context, accountID int64, prefix string) ([]Location, error) {
	if prefix == "" {
		return nil, errors.New("store: empty location prefix")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.message_id, l.folder_id, l.remote_ref FROM message_locations l
		JOIN folders f ON f.id = l.folder_id
		WHERE f.account_id = ? AND substr(l.remote_ref, 1, ?) = ? ORDER BY l.folder_id, l.remote_ref`,
		accountID, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (Location, error) {
		var l Location
		return l, sc.Scan(&l.MessageID, &l.FolderID, &l.RemoteRef)
	})
}

// FolderRefs returns the remote refs of all locations in a folder mapped to
// their message ids.
func (s *Store) FolderRefs(ctx context.Context, folderID int64) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT remote_ref, message_id FROM message_locations WHERE folder_id = ?`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			ref string
			id  int64
		)
		if err := rows.Scan(&ref, &id); err != nil {
			return nil, err
		}
		out[ref] = id
	}
	return out, rows.Err()
}

// ConversationIDsOf returns the distinct conversations of the given messages.
func (s *Store) ConversationIDsOf(ctx context.Context, messageIDs ...int64) ([]int64, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(messageIDs))
	for i, id := range messageIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT conversation_id FROM messages
		WHERE conversation_id IS NOT NULL AND id IN (`+placeholders(len(args))+`) ORDER BY conversation_id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var v int64
		return v, sc.Scan(&v)
	})
}

// FindFolder resolves a folder reference of an account given as remote id,
// role name (inbox, sent, drafts, trash, junk, archive) or folder name
// (case-insensitive), in this order.
func (s *Store) FindFolder(ctx context.Context, accountID int64, ref string) (domain.Folder, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return domain.Folder{}, ErrNotFound
	}
	if f, err := s.FolderByRemoteID(ctx, accountID, ref); err == nil || !errors.Is(err, ErrNotFound) {
		return f, err
	}
	switch r := strings.ToLower(ref); r {
	case "inbox", "sent", "drafts", "trash", "junk", "archive":
		if f, err := s.FolderByRole(ctx, accountID, domain.FolderRole(r)); err == nil || !errors.Is(err, ErrNotFound) {
			return f, err
		}
	}
	f, err := scanFolder(s.db.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders
		WHERE account_id = ? AND (lower(name) = lower(?) OR lower(remote_id) = lower(?)) ORDER BY id LIMIT 1`,
		accountID, ref, ref))
	return f, notFound(err)
}
