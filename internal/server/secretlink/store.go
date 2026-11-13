// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secretlink stores one-time secret links: short texts (e.g. a password) that the
// sender's browser encrypted with AES-256-GCM under a random key. The key is only part of the
// link fragment and never reaches the server; the server holds the ciphertext until it has
// been opened as often as permitted or has expired.
//
// This is a convenience for ordinary secrets. A very secret password belongs in a password
// manager's own sharing function (e.g. Bitwarden Send): there the sender and recipient
// control the sharing end to end, here the operator of the TuxMail server delivers the page
// that decrypts the text.
package secretlink

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

const (
	// MaxCiphertext is the largest accepted ciphertext (nonce + AES-GCM output) in bytes.
	MaxCiphertext = 64 << 10
	// MinCiphertext is nonce (12) + tag (16) + at least one byte.
	MinCiphertext = 29
	MaxTTL        = 7 * 24 * time.Hour
	MinTTL        = time.Minute
	MaxViews      = 10
	// MaxActivePerUser limits what one user keeps on the server at the same time.
	MaxActivePerUser = 50
)

var (
	// ErrGone: unknown, expired or already used up. One error for all three, so the answer
	// does not tell whether an id ever existed.
	ErrGone = errors.New("secretlink: gone")
	// ErrInvalid: a parameter is outside its limits.
	ErrInvalid = errors.New("secretlink: invalid")
	// ErrQuota: the user has too many active links.
	ErrQuota = errors.New("secretlink: too many active links")
)

// Link is the metadata of a stored link (never the content).
type Link struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Views     int       `json:"views"`
	MaxViews  int       `json:"maxViews"`
}

// Store keeps the links in the server database.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidID reports whether id has the shape of an id this package creates.
func ValidID(id string) bool {
	if len(id) != 22 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Create stores a ciphertext for owner and returns the link metadata.
func (s *Store) Create(ctx context.Context, owner int64, ciphertext []byte, ttl time.Duration, maxViews int) (Link, error) {
	if len(ciphertext) < MinCiphertext || len(ciphertext) > MaxCiphertext || ttl < MinTTL || ttl > MaxTTL || maxViews < 1 || maxViews > MaxViews {
		return Link{}, ErrInvalid
	}
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_links WHERE expires_at <= ?`, now.Unix()); err != nil {
		return Link{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_links WHERE owner_id = ?`, owner).Scan(&active); err != nil {
		return Link{}, err
	}
	if active >= MaxActivePerUser {
		return Link{}, ErrQuota
	}
	id, err := newID()
	if err != nil {
		return Link{}, err
	}
	l := Link{ID: id, CreatedAt: now.UTC().Truncate(time.Second), ExpiresAt: now.Add(ttl).UTC().Truncate(time.Second), MaxViews: maxViews}
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_links(id, owner_id, ciphertext, created_at, expires_at, max_views) VALUES (?, ?, ?, ?, ?, ?)`,
		l.ID, owner, ciphertext, l.CreatedAt.Unix(), l.ExpiresAt.Unix(), maxViews); err != nil {
		return Link{}, err
	}
	return l, tx.Commit()
}

// Open returns the ciphertext and counts the view. Links of a disabled owner
// are gone for everybody until the account is enabled again (L-22). The check and the count are one statement,
// so parallel requests cannot exceed max_views; the row is deleted with the last view.
func (s *Store) Open(ctx context.Context, id string) ([]byte, error) {
	if !ValidID(id) {
		return nil, ErrGone
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ct []byte
	var views, maxViews int
	err = tx.QueryRowContext(ctx, `UPDATE secret_links SET views = views + 1
		WHERE id = ? AND expires_at > ? AND views < max_views
		AND owner_id NOT IN (SELECT id FROM users WHERE disabled <> 0)
		RETURNING ciphertext, views, max_views`, id, s.now().Unix()).Scan(&ct, &views, &maxViews)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGone
	}
	if err != nil {
		return nil, err
	}
	if views >= maxViews {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secret_links WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ct, tx.Commit()
}

// List returns the active links of owner, newest first.
func (s *Store) List(ctx context.Context, owner int64) ([]Link, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, created_at, expires_at, views, max_views FROM secret_links
		WHERE owner_id = ? AND expires_at > ? ORDER BY created_at DESC, id`, owner, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var c, e int64
		if err := rows.Scan(&l.ID, &c, &e, &l.Views, &l.MaxViews); err != nil {
			return nil, err
		}
		l.CreatedAt, l.ExpiresAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

// Delete removes a link of owner (revoking it). Another user's id counts as gone.
func (s *Store) Delete(ctx context.Context, owner int64, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM secret_links WHERE id = ? AND owner_id = ?`, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGone
	}
	return nil
}

// Prune deletes expired links (called by the housekeeping loop).
func (s *Store) Prune(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM secret_links WHERE expires_at <= ?`, s.now().Unix())
	return err
}
