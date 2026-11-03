// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

const folderCols = `id, account_id, parent_id, remote_id, name, delimiter, role, subscribed, unread, total`

func scanFolder(sc scanner) (domain.Folder, error) {
	var (
		f           domain.Folder
		parent      sql.NullInt64
		delim, role sql.NullString
	)
	err := sc.Scan(&f.ID, &f.AccountID, &parent, &f.RemoteID, &f.Name, &delim, &role, &f.Subscribed, &f.Unread, &f.Total)
	f.ParentID, f.Delimiter, f.Role = parent.Int64, delim.String, domain.FolderRole(role.String)
	return f, err
}

// UpsertFolder inserts or updates a folder identified by (AccountID, RemoteID)
// and returns its id. The sync state is left untouched.
func (s *Store) UpsertFolder(ctx context.Context, f domain.Folder) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO folders(account_id, parent_id, remote_id, name, delimiter, role, subscribed, unread, total)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, remote_id) DO UPDATE SET
			parent_id = excluded.parent_id, name = excluded.name, delimiter = excluded.delimiter,
			role = excluded.role, subscribed = excluded.subscribed, unread = excluded.unread, total = excluded.total
		RETURNING id`,
		f.AccountID, nullID(f.ParentID), f.RemoteID, f.Name, nullString(f.Delimiter), nullString(string(f.Role)),
		boolInt(f.Subscribed), f.Unread, f.Total).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert folder %q: %w", f.RemoteID, err)
	}
	return id, nil
}

// GetFolder returns the folder with the given id.
func (s *Store) GetFolder(ctx context.Context, id int64) (domain.Folder, error) {
	f, err := scanFolder(s.db.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
	return f, notFound(err)
}

// FolderByRemoteID returns the folder of an account with the given remote id.
func (s *Store) FolderByRemoteID(ctx context.Context, accountID int64, remoteID string) (domain.Folder, error) {
	f, err := scanFolder(s.db.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id = ? AND remote_id = ?`,
		accountID, remoteID))
	return f, notFound(err)
}

// FolderByRole returns the first folder of an account with the given role.
func (s *Store) FolderByRole(ctx context.Context, accountID int64, role domain.FolderRole) (domain.Folder, error) {
	f, err := scanFolder(s.db.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id = ? AND role = ?
		ORDER BY id LIMIT 1`, accountID, string(role)))
	return f, notFound(err)
}

// ListFolders returns all folders of an account ordered by remote id.
func (s *Store) ListFolders(ctx context.Context, accountID int64) ([]domain.Folder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+folderCols+` FROM folders WHERE account_id = ? ORDER BY remote_id`, accountID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanFolder)
}

// FolderSyncState returns the opaque provider sync state of a folder ("" if none).
func (s *Store) FolderSyncState(ctx context.Context, folderID int64) (domain.SyncState, error) {
	var st sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT sync_state FROM folders WHERE id = ?`, folderID).Scan(&st)
	return domain.SyncState(st.String), notFound(err)
}

// SetFolderSyncState stores the opaque provider sync state of a folder.
func (s *Store) SetFolderSyncState(ctx context.Context, folderID int64, st domain.SyncState) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE folders SET sync_state = ? WHERE id = ?`,
		nullString(string(st)), folderID))
}

// DeleteFolder deletes a folder and its subfolders. Their message locations are
// removed; messages that are left without any location become orphaned
// (KONZEPT 6.5), so they can be reattached if they show up elsewhere.
func (s *Store) DeleteFolder(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE sub(id) AS (
				SELECT id FROM folders WHERE id = ?
				UNION SELECT f.id FROM folders f JOIN sub ON f.parent_id = sub.id)
			SELECT DISTINCT message_id FROM message_locations WHERE folder_id IN (SELECT id FROM sub)`, id)
		if err != nil {
			return err
		}
		ids, err := collect(rows, func(sc scanner) (int64, error) {
			var v int64
			return v, sc.Scan(&v)
		})
		if err != nil {
			return err
		}
		if err := checkAffected(tx.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, id)); err != nil {
			return err
		}
		now := time.Now()
		for _, mid := range ids {
			if err := orphanIfUnlocated(ctx, tx, mid, now); err != nil {
				return err
			}
		}
		return nil
	})
}
