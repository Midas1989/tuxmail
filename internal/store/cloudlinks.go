// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Cloud link providers (KONZEPT 12).
const (
	CloudNextcloud   = "nextcloud"
	CloudOpenCloud   = "opencloud"
	CloudVaultwarden = "vaultwarden"
)

// CloudLink is a share link created for an attachment (KONZEPT 12.2).
type CloudLink struct {
	ID          int64
	AccountID   int64
	Provider    string
	RemotePath  string
	ShareID     string
	URL         string
	PasswordRef string // reference into the secret store
	FileName    string
	MessageID   int64 // mail the link was inserted into (0 = none/unknown)
	ExpiresAt   time.Time
	CreatedAt   time.Time
	RevokedAt   time.Time
}

// Active reports whether the link is neither revoked nor expired at now.
func (l CloudLink) Active(now time.Time) bool {
	return l.RevokedAt.IsZero() && (l.ExpiresAt.IsZero() || now.Before(l.ExpiresAt))
}

const cloudLinkCols = `id, account_id, provider, remote_path, share_id, url, password_ref, file_name, message_id,
	expires_at, created_at, revoked_at`

func scanCloudLink(sc scanner) (CloudLink, error) {
	var (
		l                                        CloudLink
		acc, msg, expires, created, revoked      sql.NullInt64
		provider, path, share, url, pwRef, fname sql.NullString
	)
	err := sc.Scan(&l.ID, &acc, &provider, &path, &share, &url, &pwRef, &fname, &msg, &expires, &created, &revoked)
	l.AccountID, l.MessageID = acc.Int64, msg.Int64
	l.Provider, l.RemotePath, l.ShareID, l.URL, l.PasswordRef, l.FileName =
		provider.String, path.String, share.String, url.String, pwRef.String, fname.String
	l.ExpiresAt, l.CreatedAt, l.RevokedAt = timeFromNull(expires), timeFromNull(created), timeFromNull(revoked)
	return l, err
}

// CreateCloudLink inserts a cloud link and returns its id. A zero CreatedAt
// means now.
func (s *Store) CreateCloudLink(ctx context.Context, l CloudLink) (int64, error) {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO cloud_links(account_id, provider, remote_path, share_id, url, password_ref,
		file_name, message_id, expires_at, created_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(l.AccountID), nullString(l.Provider), nullString(l.RemotePath), nullString(l.ShareID), nullString(l.URL),
		nullString(l.PasswordRef), nullString(l.FileName), nullID(l.MessageID), unixOrNull(l.ExpiresAt),
		l.CreatedAt.Unix(), unixOrNull(l.RevokedAt))
	if err != nil {
		return 0, fmt.Errorf("store: create cloud link: %w", err)
	}
	return res.LastInsertId()
}

// GetCloudLink returns the cloud link with the given id.
func (s *Store) GetCloudLink(ctx context.Context, id int64) (CloudLink, error) {
	l, err := scanCloudLink(s.q(ctx).QueryRowContext(ctx, `SELECT `+cloudLinkCols+` FROM cloud_links WHERE id = ?`, id))
	return l, notFound(err)
}

// ListCloudLinks returns the links of an account (0 = all), newest first.
func (s *Store) ListCloudLinks(ctx context.Context, accountID int64) ([]CloudLink, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+cloudLinkCols+` FROM cloud_links WHERE ?1 = 0 OR account_id = ?1
		ORDER BY created_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCloudLink)
}

// ListCloudLinksByMessage returns the links belonging to a mail: those created
// for it (MessageID) and those referenced by its attachments.
func (s *Store) ListCloudLinksByMessage(ctx context.Context, messageID int64) ([]CloudLink, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+cloudLinkCols+` FROM cloud_links
		WHERE message_id = ?1 OR id IN (SELECT cloud_link_id FROM attachments WHERE message_id = ?1)
		ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCloudLink)
}

// SetCloudLinkMessage records the mail a link was inserted into (0 clears it).
func (s *Store) SetCloudLinkMessage(ctx context.Context, id, messageID int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE cloud_links SET message_id = ? WHERE id = ?`, nullID(messageID), id))
}

// MarkCloudLinkRevoked records that the share was revoked at the given time
// (zero = now). Revoking twice keeps the first time.
func (s *Store) MarkCloudLinkRevoked(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE cloud_links SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`,
		at.Unix(), id))
}

// ListExpiringCloudLinks returns the links that are not revoked and expire at
// or before the given time (this includes already expired ones), soonest first.
func (s *Store) ListExpiringCloudLinks(ctx context.Context, before time.Time) ([]CloudLink, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+cloudLinkCols+` FROM cloud_links
		WHERE revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ? ORDER BY expires_at, id`, before.Unix())
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCloudLink)
}

// DeleteCloudLink deletes a cloud link; attachments referencing it lose the reference.
func (s *Store) DeleteCloudLink(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM cloud_links WHERE id = ?`, id))
}
