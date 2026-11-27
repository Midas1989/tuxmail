// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// docKind describes one kind of indexed item.
type docKind struct {
	kind  string // fts_docs.kind (for cal objects: decided per object)
	idCol string // fts_docs column referencing the item
}

var (
	docMail    = docKind{"mail", "message_id"}
	docChat    = docKind{"chat", "chat_message_id"}
	docEvent   = docKind{"event", "cal_object_id"}
	docTask    = docKind{"task", "cal_object_id"}
	docContact = docKind{"contact", "contact_id"}
	docFile    = docKind{"file", "cloud_link_id"}
)

// indexDoc replaces the index entry of an item, applying the index policy of
// accountID (0 = local item, default policy).
func indexDoc(ctx context.Context, tx *sql.Tx, k docKind, itemID, accountID int64, d SearchDoc) error {
	pol, err := indexPolicy(ctx, tx, accountID)
	if err != nil {
		return err
	}
	d = pol.apply(d)
	// Deleting the old row fires the trigger that removes the old FTS entry.
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_docs WHERE `+k.idCol+` = ?`, itemID); err != nil {
		return err
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	_, err = tx.ExecContext(ctx, `INSERT INTO fts_docs(kind, `+k.idCol+`, account_id, encrypted, subject, body, addresses,
		attachments, attachment_content) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.kind, itemID, nullID(accountID), boolInt(d.Encrypted), d.Subject, d.Body, d.Addresses, d.Attachments,
		d.AttachmentContent)
	if err != nil {
		return fmt.Errorf("store: index %s %d: %w", k.kind, itemID, err)
	}
	return nil
}

// indexItem looks up the account of an item with accountQuery and indexes it.
func (s *Store) indexItem(ctx context.Context, k docKind, itemID int64, accountQuery string, d SearchDoc) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var acc sql.NullInt64
		if err := tx.QueryRowContext(ctx, accountQuery, itemID).Scan(&acc); err != nil {
			return fmt.Errorf("store: index %s %d: %w", k.kind, itemID, notFound(err))
		}
		return indexDoc(ctx, tx, k, itemID, acc.Int64, d)
	})
}

func (s *Store) unindex(ctx context.Context, k docKind, itemID int64) error {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM fts_docs WHERE `+k.idCol+` = ?`, itemID)
	return err
}

// IndexChatMessage (re)indexes a chat message (Subject = chat title, Body =
// text/caption, Addresses = sender, Attachments = media file name). Deleted
// messages are not indexed.
func (s *Store) IndexChatMessage(ctx context.Context, chatMessageID int64, d SearchDoc) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var (
			acc     int64
			deleted bool
		)
		err := tx.QueryRowContext(ctx, `SELECT c.account_id, m.deleted FROM chat_messages m JOIN chats c ON c.id = m.chat_id
			WHERE m.id = ?`, chatMessageID).Scan(&acc, &deleted)
		if err != nil {
			return fmt.Errorf("store: index chat message %d: %w", chatMessageID, notFound(err))
		}
		if deleted {
			_, err := tx.ExecContext(ctx, `DELETE FROM fts_docs WHERE chat_message_id = ?`, chatMessageID)
			return err
		}
		return indexDoc(ctx, tx, docChat, chatMessageID, acc, d)
	})
}

// UnindexChatMessage removes a chat message from the index.
func (s *Store) UnindexChatMessage(ctx context.Context, chatMessageID int64) error {
	return s.unindex(ctx, docChat, chatMessageID)
}

// IndexCalObject (re)indexes an event or task (Subject = summary, Body =
// description/location, Addresses = organizer/attendees). Tasks (kind todo)
// are indexed as scope "task", everything else as "event".
func (s *Store) IndexCalObject(ctx context.Context, objectID int64, d SearchDoc) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var (
			acc  sql.NullInt64
			kind string
		)
		err := tx.QueryRowContext(ctx, `SELECT c.account_id, o.kind FROM cal_objects o JOIN calendars c ON c.id = o.calendar_id
			WHERE o.id = ?`, objectID).Scan(&acc, &kind)
		if err != nil {
			return fmt.Errorf("store: index cal object %d: %w", objectID, notFound(err))
		}
		k := docEvent
		if kind == CalTodo {
			k = docTask
		}
		return indexDoc(ctx, tx, k, objectID, acc.Int64, d)
	})
}

// UnindexCalObject removes a calendar object from the index.
func (s *Store) UnindexCalObject(ctx context.Context, objectID int64) error {
	return s.unindex(ctx, docEvent, objectID)
}

// IndexContact (re)indexes a contact (Subject = name, Body = notes,
// organization etc., Addresses = e-mail addresses, phone numbers, handles).
func (s *Store) IndexContact(ctx context.Context, contactID int64, d SearchDoc) error {
	return s.indexItem(ctx, docContact, contactID, `SELECT a.account_id FROM contacts c
		JOIN addressbooks a ON a.id = c.addressbook_id WHERE c.id = ?`, d)
}

// UnindexContact removes a contact from the index.
func (s *Store) UnindexContact(ctx context.Context, contactID int64) error {
	return s.unindex(ctx, docContact, contactID)
}

// IndexCloudLink (re)indexes a cloud link (Subject = file name, Body = remote
// path/URL, Attachments = file name).
func (s *Store) IndexCloudLink(ctx context.Context, linkID int64, d SearchDoc) error {
	return s.indexItem(ctx, docFile, linkID, `SELECT account_id FROM cloud_links WHERE id = ?`, d)
}

// UnindexCloudLink removes a cloud link from the index.
func (s *Store) UnindexCloudLink(ctx context.Context, linkID int64) error {
	return s.unindex(ctx, docFile, linkID)
}

// ==== Index policy (KONZEPT 8.4) ==============================================

// IndexPolicy controls what of an account's content is indexed locally.
// Metadata (subjects/titles, addresses, attachment file names) is always
// indexed; it is stored in the database anyway.
type IndexPolicy struct {
	AccountID int64
	// IndexContent enables indexing of bodies/texts. Off keeps message texts
	// out of the index for privacy.
	IndexContent bool
	// IndexAttachmentContent enables indexing of text extracted from attachments.
	IndexAttachmentContent bool
	// IndexEncrypted enables indexing of decrypted (PGP) content.
	IndexEncrypted bool
}

// DefaultIndexPolicy is used for accounts without a stored policy and for
// local calendars/address books.
func DefaultIndexPolicy(accountID int64) IndexPolicy {
	return IndexPolicy{AccountID: accountID, IndexContent: true}
}

func (p IndexPolicy) apply(d SearchDoc) SearchDoc {
	if !p.IndexContent || (d.Encrypted && !p.IndexEncrypted) {
		d.Body, d.AttachmentContent = "", ""
	}
	if !p.IndexAttachmentContent {
		d.AttachmentContent = ""
	}
	return d
}

func indexPolicy(ctx context.Context, q querier, accountID int64) (IndexPolicy, error) {
	p := IndexPolicy{AccountID: accountID}
	if accountID == 0 {
		return DefaultIndexPolicy(0), nil
	}
	err := q.QueryRowContext(ctx, `SELECT index_content, index_attachment_content, index_encrypted
		FROM search_index_policy WHERE account_id = ?`, accountID).Scan(&p.IndexContent, &p.IndexAttachmentContent, &p.IndexEncrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultIndexPolicy(accountID), nil
	}
	return p, err
}

// GetIndexPolicy returns the index policy of an account (the default policy if
// none is stored).
func (s *Store) GetIndexPolicy(ctx context.Context, accountID int64) (IndexPolicy, error) {
	return indexPolicy(ctx, s.db, accountID)
}

// ListIndexPolicies returns all stored per-account policies.
func (s *Store) ListIndexPolicies(ctx context.Context) ([]IndexPolicy, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT account_id, index_content, index_attachment_content, index_encrypted
		FROM search_index_policy ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (IndexPolicy, error) {
		var p IndexPolicy
		return p, sc.Scan(&p.AccountID, &p.IndexContent, &p.IndexAttachmentContent, &p.IndexEncrypted)
	})
}

// SetIndexPolicy stores the policy of p.AccountID. Content that the new policy
// forbids is purged from the existing index immediately; content it newly
// allows only appears after the items are re-indexed.
func (s *Store) SetIndexPolicy(ctx context.Context, p IndexPolicy) error {
	if p.AccountID == 0 {
		return errors.New("store: set index policy: account id is required")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO search_index_policy(account_id, index_content, index_attachment_content,
				index_encrypted) VALUES (?, ?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET index_content = excluded.index_content,
				index_attachment_content = excluded.index_attachment_content, index_encrypted = excluded.index_encrypted`,
			p.AccountID, boolInt(p.IndexContent), boolInt(p.IndexAttachmentContent), boolInt(p.IndexEncrypted)); err != nil {
			return fmt.Errorf("store: set index policy: %w", err)
		}
		purge := func(set, where string) error {
			// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
			_, err := tx.ExecContext(ctx, `UPDATE fts_docs SET `+set+` WHERE account_id = ? AND `+where, p.AccountID)
			return err
		}
		if !p.IndexContent {
			if err := purge(`body = '', attachment_content = ''`, `(body <> '' OR attachment_content <> '')`); err != nil {
				return err
			}
		}
		if !p.IndexEncrypted {
			if err := purge(`body = '', attachment_content = ''`, `encrypted = 1 AND (body <> '' OR attachment_content <> '')`); err != nil {
				return err
			}
		}
		if !p.IndexAttachmentContent {
			if err := purge(`attachment_content = ''`, `attachment_content <> ''`); err != nil {
				return err
			}
		}
		return nil
	})
}
