// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Sources of cloud links (cloud_link_meta.source).
const (
	CloudSourceMail     = "mail"
	CloudSourceFiles    = "files"
	CloudSourceWorkflow = "workflow"
	CloudSourceChat     = "chat"
)

// CloudLinkMeta is additional data of a cloud link.
type CloudLinkMeta struct {
	Source string
	Size   int64
}

// SetCloudLinkMeta records source and size of a link.
func (s *Store) SetCloudLinkMeta(ctx context.Context, linkID int64, m CloudLinkMeta) error {
	if m.Source == "" {
		m.Source = CloudSourceMail
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cloud_link_meta(link_id, source, size) VALUES (?, ?, ?)
		ON CONFLICT(link_id) DO UPDATE SET source = excluded.source, size = excluded.size`, linkID, m.Source, m.Size)
	return err
}

// CloudLinkMetas returns the metadata of all links by link id.
func (s *Store) CloudLinkMetas(ctx context.Context) (map[int64]CloudLinkMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT link_id, source, size FROM cloud_link_meta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]CloudLinkMeta{}
	for rows.Next() {
		var id int64
		var m CloudLinkMeta
		if err := rows.Scan(&id, &m.Source, &m.Size); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// CloudArchiveEntry is a file stored by a workflow.
type CloudArchiveEntry struct {
	AccountID  int64
	MessageKey string
	Kind       string // "attachment" or "pdf"
	Name       string
	Path       string
	CreatedAt  time.Time
}

// GetCloudArchive returns the stored file for (account, message, kind, name).
func (s *Store) GetCloudArchive(ctx context.Context, accountID int64, messageKey, kind, name string) (CloudArchiveEntry, error) {
	e := CloudArchiveEntry{AccountID: accountID, MessageKey: messageKey, Kind: kind, Name: name}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT path, created_at FROM cloud_archive
		WHERE account_id = ? AND message_key = ? AND kind = ? AND name = ?`, accountID, messageKey, kind, name).Scan(&e.Path, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.CreatedAt = time.Unix(at, 0).UTC()
	return e, err
}

// PutCloudArchive records a stored file (replacing an older record).
func (s *Store) PutCloudArchive(ctx context.Context, e CloudArchiveEntry) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cloud_archive(account_id, message_key, kind, name, path, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, message_key, kind, name) DO UPDATE SET path = excluded.path, created_at = excluded.created_at`,
		e.AccountID, e.MessageKey, e.Kind, e.Name, e.Path, e.CreatedAt.Unix())
	return err
}
