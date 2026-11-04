// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Addressbook is an address book of an account; AccountID 0 is a local one.
type Addressbook struct {
	ID        int64
	AccountID int64
	RemoteID  string
	Name      string
	SyncToken string
}

// Contact is one vCard. VCard holds the original data (unknown properties are
// preserved); DisplayName and Emails are extracted for lookup and search.
type Contact struct {
	ID            int64
	AddressbookID int64
	UID           string
	Href          string
	ETag          string
	VCard         string
	PersonID      int64
	DisplayName   string
	Emails        []string // normalized to lowercase, sorted, unique
}

const addressbookCols = `id, account_id, remote_id, name, sync_token`

func scanAddressbook(sc scanner) (Addressbook, error) {
	var (
		a             Addressbook
		acc           sql.NullInt64
		remote, token sql.NullString
	)
	err := sc.Scan(&a.ID, &acc, &remote, &a.Name, &token)
	a.AccountID, a.RemoteID, a.SyncToken = acc.Int64, remote.String, token.String
	return a, err
}

// CreateAddressbook inserts an address book and returns its id.
func (s *Store) CreateAddressbook(ctx context.Context, a Addressbook) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO addressbooks(account_id, remote_id, name, sync_token) VALUES (?, ?, ?, ?)`,
		nullID(a.AccountID), nullString(a.RemoteID), a.Name, nullString(a.SyncToken))
	if err != nil {
		return 0, fmt.Errorf("store: create addressbook: %w", err)
	}
	return res.LastInsertId()
}

// UpsertAddressbook inserts or updates the remote address book identified by
// (AccountID, RemoteID) and returns its id. The sync token is left untouched.
func (s *Store) UpsertAddressbook(ctx context.Context, a Addressbook) (int64, error) {
	if a.AccountID == 0 || a.RemoteID == "" {
		return 0, errors.New("store: upsert addressbook: account and remote id are required")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO addressbooks(account_id, remote_id, name) VALUES (?, ?, ?)
		ON CONFLICT(account_id, remote_id) DO UPDATE SET name = excluded.name RETURNING id`,
		a.AccountID, a.RemoteID, a.Name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert addressbook %q: %w", a.RemoteID, err)
	}
	return id, nil
}

// UpdateAddressbook overwrites the address book a.ID (including the sync token).
func (s *Store) UpdateAddressbook(ctx context.Context, a Addressbook) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE addressbooks SET account_id = ?, remote_id = ?, name = ?, sync_token = ?
		WHERE id = ?`, nullID(a.AccountID), nullString(a.RemoteID), a.Name, nullString(a.SyncToken), a.ID))
}

// GetAddressbook returns the address book with the given id.
func (s *Store) GetAddressbook(ctx context.Context, id int64) (Addressbook, error) {
	a, err := scanAddressbook(s.db.QueryRowContext(ctx, `SELECT `+addressbookCols+` FROM addressbooks WHERE id = ?`, id))
	return a, notFound(err)
}

// ListAddressbooks returns all address books ordered by account and name.
func (s *Store) ListAddressbooks(ctx context.Context) ([]Addressbook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+addressbookCols+` FROM addressbooks ORDER BY account_id NULLS FIRST, name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAddressbook)
}

// ListAccountAddressbooks returns the address books of an account (0 = local ones).
func (s *Store) ListAccountAddressbooks(ctx context.Context, accountID int64) ([]Addressbook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+addressbookCols+` FROM addressbooks WHERE account_id IS ? ORDER BY name, id`,
		nullID(accountID))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAddressbook)
}

// SetAddressbookSyncToken stores the sync token of an address book.
func (s *Store) SetAddressbookSyncToken(ctx context.Context, id int64, token string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE addressbooks SET sync_token = ? WHERE id = ?`, nullString(token), id))
}

// AddressbookSyncToken returns the sync token of an address book ("" if none).
func (s *Store) AddressbookSyncToken(ctx context.Context, id int64) (string, error) {
	var t sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT sync_token FROM addressbooks WHERE id = ?`, id).Scan(&t)
	return t.String, notFound(err)
}

// DeleteAddressbook deletes an address book and its contacts.
func (s *Store) DeleteAddressbook(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM addressbooks WHERE id = ?`, id))
}

// ==== Contacts ================================================================

// contactCols selects a contact including its e-mail addresses (as a
// newline-separated list, sorted).
const contactCols = `c.id, c.addressbook_id, c.uid, c.href, c.etag, c.vcard, c.person_id, c.display_name,
	(SELECT group_concat(email, char(10)) FROM (SELECT email FROM contact_emails WHERE contact_id = c.id ORDER BY email))`

func scanContact(sc scanner) (Contact, error) {
	var (
		c                        Contact
		href, etag, name, emails sql.NullString
		person                   sql.NullInt64
	)
	err := sc.Scan(&c.ID, &c.AddressbookID, &c.UID, &href, &etag, &c.VCard, &person, &name, &emails)
	c.Href, c.ETag, c.DisplayName, c.PersonID = href.String, etag.String, name.String, person.Int64
	if emails.String != "" {
		c.Emails = strings.Split(emails.String, "\n")
	}
	return c, err
}

func normalizeEmails(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if e = NormalizeHandle(HandleEmail, e); e != "" {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// UpsertContact inserts or replaces the contact identified by (AddressbookID,
// UID) and returns its id and whether it was newly created. The vCard data is
// authoritative, so all fields including the e-mail list are overwritten; a
// zero PersonID keeps an existing person link (see LinkContactToPerson).
func (s *Store) UpsertContact(ctx context.Context, c Contact) (id int64, created bool, err error) {
	if c.UID == "" || c.VCard == "" {
		return 0, false, errors.New("store: upsert contact: uid and vcard are required")
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM contacts WHERE addressbook_id = ? AND uid = ?`, c.AddressbookID, c.UID).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		created = id == 0
		err = tx.QueryRowContext(ctx, `INSERT INTO contacts(addressbook_id, uid, href, etag, vcard, person_id, display_name)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(addressbook_id, uid) DO UPDATE SET href = excluded.href, etag = excluded.etag, vcard = excluded.vcard,
				person_id = COALESCE(excluded.person_id, contacts.person_id), display_name = excluded.display_name
			RETURNING id`, c.AddressbookID, c.UID, nullString(c.Href), nullString(c.ETag), c.VCard, nullID(c.PersonID),
			nullString(c.DisplayName)).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM contact_emails WHERE contact_id = ?`, id); err != nil {
			return err
		}
		for _, e := range normalizeEmails(c.Emails) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO contact_emails(contact_id, email) VALUES (?, ?)`, id, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("store: upsert contact %q: %w", c.UID, err)
	}
	return id, created, nil
}

// GetContact returns the contact with the given id.
func (s *Store) GetContact(ctx context.Context, id int64) (Contact, error) {
	c, err := scanContact(s.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE c.id = ?`, id))
	return c, notFound(err)
}

// ContactByUID returns the contact of an address book with the given UID.
func (s *Store) ContactByUID(ctx context.Context, addressbookID int64, uid string) (Contact, error) {
	c, err := scanContact(s.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts c
		WHERE c.addressbook_id = ? AND c.uid = ?`, addressbookID, uid))
	return c, notFound(err)
}

// ListContacts returns the contacts of an address book ordered by name.
func (s *Store) ListContacts(ctx context.Context, addressbookID int64) ([]Contact, error) {
	return s.listContacts(ctx, `c.addressbook_id = ?`, addressbookID)
}

// ContactsByPerson returns the contacts linked to a person.
func (s *Store) ContactsByPerson(ctx context.Context, personID int64) ([]Contact, error) {
	return s.listContacts(ctx, `c.person_id = ?`, personID)
}

// FindContactsByEmail returns the contacts having the given e-mail address
// (case-insensitive, exact).
func (s *Store) FindContactsByEmail(ctx context.Context, email string) ([]Contact, error) {
	return s.listContacts(ctx, `c.id IN (SELECT contact_id FROM contact_emails WHERE email = ?)`,
		NormalizeHandle(HandleEmail, email))
}

// SearchContacts returns up to limit contacts whose display name or one of
// whose e-mail addresses contains q (case-insensitive substring match, e.g.
// for address autocompletion). Name prefix matches are ranked first.
func (s *Store) SearchContacts(ctx context.Context, q string, limit int) ([]Contact, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	pat := "%" + likeEscape(strings.ToLower(q)) + "%"
	prefix := likeEscape(strings.ToLower(q)) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactCols+` FROM contacts c
		WHERE lower(c.display_name) LIKE ?1 ESCAPE '\'
		   OR c.id IN (SELECT contact_id FROM contact_emails WHERE email LIKE ?1 ESCAPE '\')
		ORDER BY (lower(c.display_name) LIKE ?2 ESCAPE '\') DESC, c.display_name COLLATE NOCASE, c.id
		LIMIT ?3`, pat, prefix, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanContact)
}

func (s *Store) listContacts(ctx context.Context, where string, args ...any) ([]Contact, error) {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE `+where+`
		ORDER BY c.display_name COLLATE NOCASE, c.id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanContact)
}

// LinkContactToPerson links a contact to a person (0 removes the link).
func (s *Store) LinkContactToPerson(ctx context.Context, contactID, personID int64) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE contacts SET person_id = ? WHERE id = ?`, nullID(personID), contactID))
}

// DeleteContact deletes a contact.
func (s *Store) DeleteContact(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM contacts WHERE id = ?`, id))
}

// DeleteContactByUID deletes the contact of an address book with the given UID.
func (s *Store) DeleteContactByUID(ctx context.Context, addressbookID int64, uid string) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM contacts WHERE addressbook_id = ? AND uid = ?`, addressbookID, uid))
}

// likeEscape escapes the LIKE wildcards % and _ (and the escape character \).
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
