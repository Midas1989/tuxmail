// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Mails kept with their header only ("Nur Kopf"): older mails are stored
// with header, preview and attachment names; the body is loaded on opening
// (internal/sync, mail.headersOnlyOlderThanDays). A message without blob is
// such a mail.

// KeyHeadersOnlyDays is the app setting of this device with the global
// default: mails older than this many days are kept with their header only
// (0: all mails completely; missing: as before the setting existed). It is
// never synced to a server.
const KeyHeadersOnlyDays = "mail.headersOnlyOlderThanDays"

// HeadersOnlyKinds are the account kinds whose bodies can be loaded again
// from the server at any time. Only their bodies are ever released. POP3 is
// not among them: with "delete from server" the local copy is the only one,
// and even without it a POP3 server may drop a mail at any time.
var HeadersOnlyKinds = []string{"imap", "jmap", "eas", "graph", "ews", "gmail"}

// PreviewTask is a mail kept with its header only that has no preview yet.
type PreviewTask struct {
	MessageID      int64
	FolderRemoteID string
	RemoteRef      string
}

// MessagesNeedingPreview returns header-only mails of an account dated
// before `before` whose preview was not requested yet, with one server
// location each (refs with skipRefPrefix, e.g. placeholders, are ignored).
func (s *Store) MessagesNeedingPreview(ctx context.Context, accountID int64, before time.Time, skipRefPrefix string, limit int) ([]PreviewTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, f.remote_id, MIN(l.remote_ref)
		FROM messages m
		JOIN message_locations l ON l.message_id = m.id
		JOIN folders f ON f.id = l.folder_id
		WHERE m.account_id = ? AND m.state = 'live' AND m.blob_hash IS NULL AND m.preview_tried = 0
		  AND COALESCE(m.snippet, '') = ''
		  AND COALESCE(m.sent_at, m.received_at, 0) < ?
		  AND substr(f.remote_id, 1, 6) <> 'local/'
		  AND (? = '' OR substr(l.remote_ref, 1, length(?)) <> ?)
		GROUP BY m.id
		ORDER BY COALESCE(m.sent_at, m.received_at, 0) DESC, m.id DESC LIMIT ?`,
		accountID, before.Unix(), skipRefPrefix, skipRefPrefix, skipRefPrefix, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (PreviewTask, error) {
		var t PreviewTask
		return t, sc.Scan(&t.MessageID, &t.FolderRemoteID, &t.RemoteRef)
	})
}

// SetMessagePreview stores the preview of a header-only mail and marks it
// as requested (also when the preview is empty). A mail whose body arrived
// in the meantime keeps the snippet derived from the body.
func (s *Store) SetMessagePreview(ctx context.Context, id int64, preview string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET preview_tried = 1,
		snippet = CASE WHEN blob_hash IS NULL AND ? <> '' THEN ? ELSE snippet END WHERE id = ?`, preview, preview, id)
	return err
}

// SetContentTime records when the body of a message was stored locally.
func (s *Store) SetContentTime(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET content_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// ReleaseCandidate is a message whose body may be released, with the server
// location it can be loaded again from (in a folder synced without error).
type ReleaseCandidate struct {
	MessageID      int64
	BlobHash       string
	FolderID       int64
	FolderRemoteID string
	RemoteRef      string
}

// Locations that can never be loaded again from the server: local folders
// (POP3 "local/…", the local account), copies and imports ("local:",
// "local-copy:") and placeholders of pending operations ("~pending:").
const notRefetchableLocation = `(substr(f.remote_id, 1, 6) = 'local/' OR COALESCE(f.role, '') IN ('drafts', 'templates', 'local')
	OR substr(l.remote_ref, 1, 6) = 'local:' OR substr(l.remote_ref, 1, 11) = 'local-copy:' OR substr(l.remote_ref, 1, 1) = '~'
	OR f.account_id <> m.account_id)`

// ReleasableBodies returns messages of an account whose body may be
// released: dated before `before`, body stored before `loadedBefore` (a mail
// just opened keeps its body for a while) and loadable again from the
// server. Never released are mails of other account kinds than
// HeadersOnlyKinds (POP3, local folders, ...), drafts, mails with any
// location in a local folder, a drafts or templates folder, as a local copy
// or import or with a pending operation, mails whose attachments are stored
// separately and mails that are or were encrypted (PGP/S-MIME: decrypted
// index entries, encrypted parts, an S/MIME status). The caller also checks
// the body itself (size, encryption) and confirms on the server that the
// message still exists. Only mails larger than maxSize (they could not be
// loaded again) and only locations in okFolders (synced without error in
// the current cycle) count.
func (s *Store) ReleasableBodies(ctx context.Context, accountID int64, before, loadedBefore time.Time, maxSize int64, okFolders []int64, limit int) ([]ReleaseCandidate, error) {
	if len(okFolders) == 0 {
		return nil, nil
	}
	args := []any{accountID}
	kinds := ""
	for i, k := range HeadersOnlyKinds {
		if i > 0 {
			kinds += ","
		}
		kinds += "?"
		args = append(args, k)
	}
	args = append(args, before.Unix(), loadedBefore.Unix(), int64(FlagDraft), maxSize, maxSize)
	for _, f := range okFolders {
		args = append(args, f)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.blob_hash, f0.id, f0.remote_id, MIN(l0.remote_ref) FROM messages m
		JOIN accounts a ON a.id = m.account_id
		JOIN message_locations l0 ON l0.message_id = m.id
		JOIN folders f0 ON f0.id = l0.folder_id
		WHERE m.account_id = ? AND a.kind IN (`+kinds+`) AND m.state = 'live' AND m.blob_hash IS NOT NULL
		  AND COALESCE(m.sent_at, m.received_at, 0) > 0 AND COALESCE(m.sent_at, m.received_at, 0) < ?
		  AND m.keep_body = 0 AND (m.content_at IS NULL OR m.content_at <= ?)
		  AND (m.flags & ?) = 0
		  AND (? <= 0 OR m.size <= ?)
		  AND f0.id IN (`+placeholders(len(okFolders))+`)
		  AND NOT EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
		                  WHERE l.message_id = m.id AND `+notRefetchableLocation+`)
		  AND NOT EXISTS (SELECT 1 FROM attachments t WHERE t.message_id = m.id AND (t.blob_hash IS NOT NULL
		                  OR lower(COALESCE(t.mime_type, '')) IN ('application/pgp-encrypted', 'application/pkcs7-mime', 'application/x-pkcs7-mime')))
		  AND NOT EXISTS (SELECT 1 FROM fts_docs d WHERE d.message_id = m.id AND d.encrypted = 1)
		  AND NOT EXISTS (SELECT 1 FROM message_insights i WHERE i.message_id = m.id AND COALESCE(i.smime, '') NOT IN ('', 'null', '{}'))
		  AND COALESCE(m.snippet, '') NOT LIKE '%BEGIN PGP%'
		GROUP BY m.id
		ORDER BY COALESCE(m.sent_at, m.received_at, 0), m.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ReleaseCandidate, error) {
		var c ReleaseCandidate
		return c, sc.Scan(&c.MessageID, &c.BlobHash, &c.FolderID, &c.FolderRemoteID, &c.RemoteRef)
	})
}

// ReleaseBody forgets the stored body of a message (the blob itself is
// deleted by the caller once no row references it). Header, preview and the
// attachment names stay. It reports false if the body changed meanwhile.
func (s *Store) ReleaseBody(ctx context.Context, id int64, blobHash string) (bool, error) {
	var ok bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE messages SET blob_hash = NULL, content_at = NULL, preview_tried = 1
			WHERE id = ? AND blob_hash = ?`, id, blobHash)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		ok = n == 1
		return err
	})
	return ok, err
}

// KeepBody marks a message whose body must never be released (found to be
// encrypted when checked).
func (s *Store) KeepBody(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET keep_body = 1 WHERE id = ?`, id)
	return err
}

// HeadersOnlyStats counts the mails of an account kept with header only
// and those with a stored body.
func (s *Store) HeadersOnlyStats(ctx context.Context, accountID int64) (headersOnly, complete int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(blob_hash IS NULL), 0), COALESCE(SUM(blob_hash IS NOT NULL), 0)
		FROM messages WHERE account_id = ? AND state = 'live'`, accountID).Scan(&headersOnly, &complete)
	return
}

// ==== Read cache of the mode "Über den Server" ===================================

// RemoteCachedSummary returns the summary (JSON) of a loaded message.
func (s *Store) RemoteCachedSummary(ctx context.Context, id int64) (string, bool, error) {
	var js string
	err := s.db.QueryRowContext(ctx, `SELECT summary_json FROM remote_cache_messages WHERE id = ?`, id).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return js, err == nil, err
}

// RemoteCachedDate returns the date of a loaded message ("" if unknown).
func (s *Store) RemoteCachedDate(ctx context.Context, id int64) (string, error) {
	var d string
	err := s.db.QueryRowContext(ctx, `SELECT msg_date FROM remote_cache_messages WHERE id = ?`, id).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return d, err
}

// HasRemoteCache reports whether an answer is kept (without touching it).
func (s *Store) HasRemoteCache(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_cache WHERE cache_key = ?`, key).Scan(&n)
	return n > 0, err
}

// remoteContentKeys are the kept answers with the content of message %d.
var remoteContentKeys = []string{"/api/messages/%d", "/api/messages/%d/remote-content"}

// remoteContentPrefixes are prefixes of kept answers with its parts.
var remoteContentPrefixes = []string{"/api/messages/%d/attachments/", "/api/messages/%d/cid/"}

// ReleaseRemoteCachedBodies removes the content of loaded messages from the
// read cache (detail, attachments, inline images, loaded images) for which
// release returns true, and replaces their search text with what release
// returns (header and preview). The list entries stay.
func (s *Store) ReleaseRemoteCachedBodies(ctx context.Context, release func(id int64, date, summaryJSON string) (string, bool)) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, msg_date, summary_json FROM remote_cache_messages`)
	if err != nil {
		return err
	}
	type rel struct {
		id   int64
		text string
	}
	all, err := collect(rows, func(sc scanner) (rel, error) {
		var (
			r        rel
			date, js string
		)
		if err := sc.Scan(&r.id, &date, &js); err != nil {
			return r, err
		}
		text, ok := release(r.id, date, js)
		if !ok {
			r.id = 0
		}
		r.text = text
		return r, nil
	})
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, r := range all {
			if r.id == 0 {
				continue
			}
			for _, k := range remoteContentKeys {
				if _, err := tx.ExecContext(ctx, `DELETE FROM remote_cache WHERE cache_key = ?`, fmt.Sprintf(k, r.id)); err != nil {
					return err
				}
			}
			for _, p := range remoteContentPrefixes {
				pre := fmt.Sprintf(p, r.id)
				if _, err := tx.ExecContext(ctx, `DELETE FROM remote_cache WHERE substr(cache_key, 1, ?) = ?`, len(pre), pre); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE remote_cache_messages SET search_text = ? WHERE id = ?`, r.text, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// PendingBodies counts the mails of an account (of HeadersOnlyKinds) without a
// stored body dated since notBefore (zero: all) that the sync would download
// (at most maxSize, with a server location), and their size.
func (s *Store) PendingBodies(ctx context.Context, accountID int64, notBefore time.Time, maxSize int64) (count int, bytes int64, err error) {
	args := []any{accountID}
	kinds := ""
	for i, k := range HeadersOnlyKinds {
		if i > 0 {
			kinds += ","
		}
		kinds += "?"
		args = append(args, k)
	}
	args = append(args, unixOrZero(notBefore), maxSize, maxSize)
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(m.size), 0) FROM messages m JOIN accounts a ON a.id = m.account_id
		WHERE m.account_id = ? AND a.kind IN (`+kinds+`) AND a.enabled = 1 AND m.state = 'live' AND m.blob_hash IS NULL
		  AND COALESCE(m.sent_at, m.received_at, 0) >= ? AND (? <= 0 OR m.size <= ?)
		  AND EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
		              WHERE l.message_id = m.id AND substr(l.remote_ref, 1, 1) <> '~' AND substr(f.remote_id, 1, 6) <> 'local/')`, args...).Scan(&count, &bytes)
	return
}
