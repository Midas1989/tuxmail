// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SearchDoc is the text indexed for one item in fts_items. For items other
// than mails the fields are used as: Subject = title (chat title, event or
// task summary, contact name, file name), Body = text (message text,
// description, notes), Addresses = participants/sender/e-mail addresses and
// handles, Attachments = file names (media, attachments).
type SearchDoc struct {
	Subject           string
	Body              string
	Addresses         string
	Attachments       string // attachment file names
	AttachmentContent string // text extracted from attachments (PDF, DOCX, ...)
	// Encrypted marks Body/AttachmentContent as coming from decrypted (e.g.
	// PGP) content; they are only indexed if the account's policy allows it.
	Encrypted bool
}

// IndexMessage (re)indexes a message for full-text search, honoring the index
// policy of its account (see IndexPolicy). The index entry is removed
// automatically when the message is deleted.
func (s *Store) IndexMessage(ctx context.Context, messageID int64, d SearchDoc) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var acc int64
		if err := tx.QueryRowContext(ctx, `SELECT account_id FROM messages WHERE id = ?`, messageID).Scan(&acc); err != nil {
			return fmt.Errorf("store: index message %d: %w", messageID, notFound(err))
		}
		return indexDoc(ctx, tx, docMail, messageID, acc, d)
	})
}

// UnindexMessage removes a message from the full-text index.
func (s *Store) UnindexMessage(ctx context.Context, messageID int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM fts_docs WHERE message_id = ?`, messageID)
	return err
}

// SearchMessages runs an FTS5 MATCH query and returns the ids of matching live
// messages, best match first. The query is passed to FTS5 verbatim; use Search
// for user input.
func (s *Store) SearchMessages(ctx context.Context, query string, limit int) ([]int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT d.message_id FROM fts_items f
		JOIN fts_docs d ON d.id = f.rowid
		JOIN messages m ON m.id = d.message_id
		WHERE fts_items MATCH ? AND m.state = 'live'
		ORDER BY f.rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}

// MessagesToIndex returns up to limit live messages that have no full-text
// index entry yet, oldest first (e.g. after migration 0003 rebuilt the index).
func (s *Store) MessagesToIndex(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT m.id FROM messages m
		WHERE m.state = 'live' AND NOT EXISTS (SELECT 1 FROM fts_docs d WHERE d.message_id = m.id)
		ORDER BY m.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}
