// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// ErrHandleInUse is returned when a handle is added to a person although it
// already belongs to another person. Merge the persons instead.
var ErrHandleInUse = errors.New("store: handle belongs to another person")

// Handle kinds (KONZEPT 10.2).
const (
	HandleEmail     = "email"
	HandlePhone     = "phone"
	HandleWhatsApp  = "whatsapp"
	HandleSignal    = "signal"
	HandleTelegram  = "telegram"
	HandleMatrix    = "matrix"
	HandleInstagram = "instagram"
	HandleFacebook  = "facebook"
)

// Person is one human being across all channels (KONZEPT 10.2).
type Person struct {
	ID          int64
	DisplayName string
	AvatarBlob  string // blob hash
	IsMe        bool
}

// Handle is an address of a person on one channel. Value is normalized (see
// NormalizeHandle).
type Handle struct {
	PersonID int64
	Kind     string
	Value    string
	Verified bool
}

// PersonWithHandles is a person together with all its handles.
type PersonWithHandles struct {
	Person
	Handles []Handle
}

// NormalizeHandle returns the canonical form of a handle value: e-mail
// addresses and user names are trimmed and lowercased; phone numbers (and the
// phone-number based WhatsApp/Signal handles) are reduced to digits with a
// leading "+" ("00" is treated as international prefix). Full E.164
// normalization with country defaults is left to the person resolver.
func NormalizeHandle(kind, value string) string {
	value = strings.TrimSpace(value)
	switch kind {
	case HandlePhone, HandleWhatsApp, HandleSignal:
		if looksLikePhone(value) {
			return normalizePhone(value)
		}
	}
	return strings.ToLower(value)
}

func looksLikePhone(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !unicode.IsDigit(r) && !strings.ContainsRune("+-/ ().", r) {
			return false
		}
	}
	return true
}

func normalizePhone(v string) string {
	var b strings.Builder
	plus := strings.HasPrefix(v, "+")
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if !plus && strings.HasPrefix(digits, "00") {
		digits, plus = digits[2:], true
	}
	if plus {
		return "+" + digits
	}
	return digits
}

const personCols = `id, display_name, avatar_blob, is_me`

func scanPerson(sc scanner) (Person, error) {
	var (
		p            Person
		name, avatar sql.NullString
	)
	err := sc.Scan(&p.ID, &name, &avatar, &p.IsMe)
	p.DisplayName, p.AvatarBlob = name.String, avatar.String
	return p, err
}

// CreatePerson inserts a person and returns its id.
func (s *Store) CreatePerson(ctx context.Context, p Person) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO persons(display_name, avatar_blob, is_me) VALUES (?, ?, ?)`,
		nullString(p.DisplayName), nullString(p.AvatarBlob), boolInt(p.IsMe))
	if err != nil {
		return 0, fmt.Errorf("store: create person: %w", err)
	}
	return res.LastInsertId()
}

// UpdatePerson overwrites the person p.ID.
func (s *Store) UpdatePerson(ctx context.Context, p Person) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE persons SET display_name = ?, avatar_blob = ?, is_me = ? WHERE id = ?`,
		nullString(p.DisplayName), nullString(p.AvatarBlob), boolInt(p.IsMe), p.ID))
}

// GetPerson returns the person with the given id.
func (s *Store) GetPerson(ctx context.Context, id int64) (Person, error) {
	p, err := scanPerson(s.q(ctx).QueryRowContext(ctx, `SELECT `+personCols+` FROM persons WHERE id = ?`, id))
	return p, notFound(err)
}

// ListPersons returns all persons ordered by display name.
func (s *Store) ListPersons(ctx context.Context) ([]Person, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+personCols+` FROM persons ORDER BY display_name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanPerson)
}

// DeletePerson deletes a person. Its handles are deleted; contacts, addresses
// and chat messages referencing it lose the reference.
func (s *Store) DeletePerson(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM persons WHERE id = ?`, id))
}

// AddHandle attaches a handle to a person; the value is normalized. Adding a
// handle the person already has updates its verified flag (a verified handle
// stays verified). ErrHandleInUse is returned if another person owns it.
func (s *Store) AddHandle(ctx context.Context, h Handle) error {
	if h.Kind == "" || h.Value == "" {
		return errors.New("store: add handle: kind and value are required")
	}
	v := NormalizeHandle(h.Kind, h.Value)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT person_id FROM person_handles WHERE kind = ? AND value = ?`, h.Kind, v).Scan(&owner)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO person_handles(person_id, kind, value, verified) VALUES (?, ?, ?, ?)`,
				h.PersonID, h.Kind, v, boolInt(h.Verified))
			return err
		case err != nil:
			return err
		case owner != h.PersonID:
			return ErrHandleInUse
		}
		_, err = tx.ExecContext(ctx, `UPDATE person_handles SET verified = MAX(verified, ?) WHERE kind = ? AND value = ?`,
			boolInt(h.Verified), h.Kind, v)
		return err
	})
}

// RemoveHandle removes a handle (value is normalized).
func (s *Store) RemoveHandle(ctx context.Context, kind, value string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM person_handles WHERE kind = ? AND value = ?`,
		kind, NormalizeHandle(kind, value)))
}

// Handles returns the handles of a person ordered by kind and value.
func (s *Store) Handles(ctx context.Context, personID int64) ([]Handle, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT person_id, kind, value, verified FROM person_handles
		WHERE person_id = ? ORDER BY kind, value`, personID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanHandle)
}

func scanHandle(sc scanner) (Handle, error) {
	var h Handle
	return h, sc.Scan(&h.PersonID, &h.Kind, &h.Value, &h.Verified)
}

// FindPersonByHandle returns the person owning the handle (value is normalized).
func (s *Store) FindPersonByHandle(ctx context.Context, kind, value string) (Person, error) {
	p, err := scanPerson(s.q(ctx).QueryRowContext(ctx, `SELECT `+prefixed("p.", personCols)+` FROM persons p
		JOIN person_handles h ON h.person_id = p.id WHERE h.kind = ? AND h.value = ?`, kind, NormalizeHandle(kind, value)))
	return p, notFound(err)
}

// ListPersonsWithHandles returns all persons with their handles, ordered by
// display name.
func (s *Store) ListPersonsWithHandles(ctx context.Context) ([]PersonWithHandles, error) {
	persons, err := s.ListPersons(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT person_id, kind, value, verified FROM person_handles ORDER BY person_id, kind, value`)
	if err != nil {
		return nil, err
	}
	handles, err := collect(rows, scanHandle)
	if err != nil {
		return nil, err
	}
	byPerson := make(map[int64][]Handle)
	for _, h := range handles {
		byPerson[h.PersonID] = append(byPerson[h.PersonID], h)
	}
	out := make([]PersonWithHandles, len(persons))
	for i, p := range persons {
		out[i] = PersonWithHandles{Person: p, Handles: byPerson[p.ID]}
	}
	return out, nil
}

// MergePersons merges the persons others into keep (KONZEPT 10.2): handles,
// contacts, mail addresses and chat message senders are moved to keep, and the
// others are deleted. keep keeps its display name and avatar unless they are
// empty, in which case the first non-empty value of the others is used; IsMe
// is set if any of the persons was "me".
func (s *Store) MergePersons(ctx context.Context, keep int64, others ...int64) error {
	others = slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(others))), func(id int64) bool { return id == keep })
	if len(others) == 0 {
		return nil
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		ids := append([]int64{keep}, others...)
		all := make([]any, len(ids))
		for i, id := range ids {
			all[i] = id
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		rows, err := tx.QueryContext(ctx, `SELECT `+personCols+` FROM persons WHERE id IN (`+placeholders(len(ids))+`)`, all...)
		if err != nil {
			return err
		}
		persons, err := collect(rows, scanPerson)
		if err != nil {
			return err
		}
		if len(persons) != len(ids) {
			return ErrNotFound
		}
		byID := make(map[int64]Person, len(persons))
		for _, p := range persons {
			byID[p.ID] = p
		}
		merged := byID[keep]
		for _, id := range others {
			p := byID[id]
			if merged.DisplayName == "" {
				merged.DisplayName = p.DisplayName
			}
			if merged.AvatarBlob == "" {
				merged.AvatarBlob = p.AvatarBlob
			}
			merged.IsMe = merged.IsMe || p.IsMe
		}
		ph := placeholders(len(others))
		args := append([]any{keep}, all[1:]...)
		for _, q := range []string{
			`UPDATE person_handles SET person_id = ? WHERE person_id IN (` + ph + `)`,
			`UPDATE contacts SET person_id = ? WHERE person_id IN (` + ph + `)`,
			`UPDATE message_addresses SET person_id = ? WHERE person_id IN (` + ph + `)`,
			`UPDATE chat_messages SET sender_person_id = ? WHERE sender_person_id IN (` + ph + `)`,
		} {
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE persons SET display_name = ?, avatar_blob = ?, is_me = ? WHERE id = ?`,
			nullString(merged.DisplayName), nullString(merged.AvatarBlob), boolInt(merged.IsMe), keep); err != nil {
			return err
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		_, err = tx.ExecContext(ctx, `DELETE FROM persons WHERE id IN (`+ph+`)`, all[1:]...)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: merge persons: %w", err)
	}
	return nil
}

// LinkAddressesToPerson sets the person of all mail addresses with the given
// e-mail address (case-insensitive) and returns the number of updated rows.
// LinkMessageAddressToPerson links the unlinked rows of one message with
// this address (index on message_id): for an address whose earlier rows are
// linked already, LinkAddressesToPerson would walk all rows of the address
// again for every further mail, which made indexing quadratic in the number
// of mails per sender.
func (s *Store) LinkMessageAddressToPerson(ctx context.Context, messageID int64, email string, personID int64) error {
	// INDEXED BY: without statistics SQLite picks the lower(email) index for
	// the equal-cost terms and walks every row of a frequent sender.
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE message_addresses INDEXED BY ix_addr_message SET person_id = ?1
		WHERE message_id = ?2 AND lower(email) = ?3 AND person_id IS NULL`, nullID(personID), messageID, NormalizeHandle(HandleEmail, email))
	return err
}

func (s *Store) LinkAddressesToPerson(ctx context.Context, email string, personID int64) (int64, error) {
	// Only rows that are not linked yet are written: the indexer calls this
	// for every address of every new mail, and rewriting all earlier rows of
	// a frequent sender each time made large initial syncs quadratic.
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE message_addresses SET person_id = ?1 WHERE lower(email) = ?2
		AND person_id IS NOT ?1`, nullID(personID), NormalizeHandle(HandleEmail, email))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
