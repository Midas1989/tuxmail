// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Persistence of the remote image proxy (migration 0011_proxy.sql).

// ProxyCacheEntry is a cached remote image (bytes in the blob store).
type ProxyCacheEntry struct {
	Key         string
	BlobHash    string
	ContentType string
	Size        int64
	FetchedAt   time.Time
	ExpiresAt   time.Time
}

// GetProxyCache returns a non-expired cache entry.
func (s *Store) GetProxyCache(ctx context.Context, key string, now time.Time) (ProxyCacheEntry, error) {
	var e ProxyCacheEntry
	var fetched, expires int64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT key, blob_hash, content_type, size, fetched_at, expires_at FROM proxy_cache
		WHERE key = ? AND expires_at > ?`, key, now.Unix()).Scan(&e.Key, &e.BlobHash, &e.ContentType, &e.Size, &fetched, &expires)
	if err != nil {
		return e, notFound(err)
	}
	e.FetchedAt, e.ExpiresAt = time.Unix(fetched, 0), time.Unix(expires, 0)
	return e, nil
}

// PutProxyCache stores (or replaces) a cache entry.
func (s *Store) PutProxyCache(ctx context.Context, e ProxyCacheEntry) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO proxy_cache(key, blob_hash, content_type, size, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET blob_hash = excluded.blob_hash,
		content_type = excluded.content_type, size = excluded.size, fetched_at = excluded.fetched_at, expires_at = excluded.expires_at`,
		e.Key, e.BlobHash, e.ContentType, e.Size, e.FetchedAt.Unix(), e.ExpiresAt.Unix())
	return err
}

// PurgeProxyCache deletes expired entries and returns the blob hashes that
// are no longer referenced by a cache entry or a message (the caller
// deletes them from the blob store).
func (s *Store) PurgeProxyCache(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT DISTINCT blob_hash FROM proxy_cache WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return nil, err
	}
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		hashes = append(hashes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM proxy_cache WHERE expires_at <= ?`, now.Unix()); err != nil {
		return nil, err
	}
	var out []string
	for _, h := range hashes {
		var n int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM proxy_cache WHERE blob_hash = ?)
			+ (SELECT COUNT(*) FROM messages WHERE blob_hash = ?) + (SELECT COUNT(*) FROM attachments WHERE blob_hash = ?)
			+ (SELECT COUNT(*) FROM uploads WHERE blob_hash = ?) + (SELECT COUNT(*) FROM signature_images WHERE blob_hash = ?)`, h, h, h, h, h).Scan(&n); err != nil {
			return out, err
		}
		if n == 0 {
			out = append(out, h)
		}
	}
	return out, nil
}

// Remote content allowlist kinds.
const (
	AllowSender = "sender"
	AllowDomain = "domain"
)

// RemoteAllow is an allowlist entry for remote content.
type RemoteAllow struct {
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"createdAt"`
}

// ErrInvalidAllow is returned for malformed allowlist entries.
var ErrInvalidAllow = errors.New("store: invalid remote content allowlist entry")

func normAllow(kind, value string) (string, string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch kind {
	case AllowSender:
		if !strings.Contains(value, "@") {
			return "", "", ErrInvalidAllow
		}
	case AllowDomain:
		value = strings.TrimPrefix(value, "@")
		if value == "" || strings.ContainsAny(value, "@ /") {
			return "", "", ErrInvalidAllow
		}
	default:
		return "", "", ErrInvalidAllow
	}
	return kind, value, nil
}

// AddRemoteAllow adds an allowlist entry (idempotent).
func (s *Store) AddRemoteAllow(ctx context.Context, kind, value string) error {
	kind, value, err := normAllow(kind, value)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO remote_content_allow(kind, value, created_at) VALUES (?, ?, ?)
		ON CONFLICT(kind, value) DO NOTHING`, kind, value, time.Now().Unix())
	return err
}

// RemoveRemoteAllow deletes an allowlist entry.
func (s *Store) RemoveRemoteAllow(ctx context.Context, kind, value string) error {
	kind, value, err := normAllow(kind, value)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `DELETE FROM remote_content_allow WHERE kind = ? AND value = ?`, kind, value)
	return err
}

// ListRemoteAllow returns all allowlist entries.
func (s *Store) ListRemoteAllow(ctx context.Context) ([]RemoteAllow, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT kind, value, created_at FROM remote_content_allow ORDER BY kind, value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RemoteAllow{}
	for rows.Next() {
		var a RemoteAllow
		var at int64
		if err := rows.Scan(&a.Kind, &a.Value, &at); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// RemoteAllowed reports whether remote content of a sender address is
// allowed (by address, by its domain or a parent domain).
func (s *Store) RemoteAllowed(ctx context.Context, sender string) (bool, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	at := strings.LastIndexByte(sender, '@')
	if at < 0 {
		return false, nil
	}
	args := []any{AllowSender, sender}
	q := `SELECT 1 FROM remote_content_allow WHERE (kind = ? AND value = ?)`
	dom := sender[at+1:]
	for dom != "" {
		q += ` OR (kind = 'domain' AND value = ?)`
		args = append(args, dom)
		i := strings.IndexByte(dom, '.')
		if i < 0 || !strings.Contains(dom[i+1:], ".") {
			break
		}
		dom = dom[i+1:]
	}
	var one int
	err := s.q(ctx).QueryRowContext(ctx, q+` LIMIT 1`, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
