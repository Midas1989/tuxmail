// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SearchDoc is the text indexed for one item in fts_items.
type SearchDoc struct {
	Subject     string
	Body        string
	Addresses   string
	Attachments string // attachment file names
}

// IndexMessage (re)indexes a message for full-text search. The index entry is
// removed automatically when the message is deleted.
func (s *Store) IndexMessage(ctx context.Context, messageID int64, d SearchDoc) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Deleting the mapping fires the trigger that removes the old FTS row.
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts_docs WHERE message_id = ?`, messageID); err != nil {
			return err
		}
		var docID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO fts_docs(message_id) VALUES (?) RETURNING id`, messageID).
			Scan(&docID); err != nil {
			return fmt.Errorf("store: index message %d: %w", messageID, err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO fts_items(rowid, subject, body, addresses, attachments) VALUES (?, ?, ?, ?, ?)`,
			docID, d.Subject, d.Body, d.Addresses, d.Attachments)
		return err
	})
}

// UnindexMessage removes a message from the full-text index.
func (s *Store) UnindexMessage(ctx context.Context, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM fts_docs WHERE message_id = ?`, messageID)
	return err
}

// SearchMessages runs an FTS5 MATCH query and returns the ids of matching live
// messages, best match first.
func (s *Store) SearchMessages(ctx context.Context, query string, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.message_id FROM fts_items f
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
