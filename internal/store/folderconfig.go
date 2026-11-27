// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Folder configuration (migration 0010): user overrides of folder roles and
// per-account folder behaviour.
//
// folders.role is the effective role. It is derived from the role the server
// reported (folders.server_role) and folder_role_overrides:
//   - the folder an override points to (by remote id) gets the override's role
//     (source "user");
//   - a folder whose server role is overridden by another, existing folder
//     loses that role, so every role is held by at most one folder;
//   - an override whose folder does not exist (yet / any more) is
//     "unresolved": it is kept but has no effect until the folder (re)appears.

// OverridableRoles are the roles the user may assign. The inbox is fixed.
var OverridableRoles = []domain.FolderRole{domain.RoleSent, domain.RoleDrafts, domain.RoleTrash, domain.RoleJunk,
	domain.RoleArchive, domain.RoleTemplates}

// ErrRoleNotOverridable is returned for roles that cannot be assigned.
var ErrRoleNotOverridable = errors.New("store: folder role cannot be assigned")

// IsOverridableRole reports whether r may be assigned by the user.
func IsOverridableRole(r domain.FolderRole) bool {
	for _, x := range OverridableRoles {
		if x == r {
			return true
		}
	}
	return false
}

// applyRolesSQL recomputes role / role_source of the selected folders rows.
const applyRolesSQL = `UPDATE folders SET
	role = CASE
		WHEN EXISTS (SELECT 1 FROM folder_role_overrides o WHERE o.account_id = folders.account_id AND o.remote_id = folders.remote_id)
			THEN (SELECT o.role FROM folder_role_overrides o WHERE o.account_id = folders.account_id AND o.remote_id = folders.remote_id)
		WHEN folders.server_role IS NOT NULL AND EXISTS (SELECT 1 FROM folder_role_overrides o
				JOIN folders f2 ON f2.account_id = o.account_id AND f2.remote_id = o.remote_id
				WHERE o.account_id = folders.account_id AND o.role = folders.server_role)
			THEN NULL
		ELSE folders.server_role END,
	role_source = CASE
		WHEN EXISTS (SELECT 1 FROM folder_role_overrides o WHERE o.account_id = folders.account_id AND o.remote_id = folders.remote_id)
			THEN 'user'
		WHEN folders.server_role IS NOT NULL AND EXISTS (SELECT 1 FROM folder_role_overrides o
				JOIN folders f2 ON f2.account_id = o.account_id AND f2.remote_id = o.remote_id
				WHERE o.account_id = folders.account_id AND o.role = folders.server_role)
			THEN NULL
		ELSE folders.server_role_source END`

// FolderRoleOverride is a role the user assigned to a folder.
type FolderRoleOverride struct {
	AccountID int64             `json:"accountId"`
	Role      domain.FolderRole `json:"role"`
	RemoteID  string            `json:"remoteId"`
	// FolderID is the local folder (0 = unresolved: the folder is missing).
	FolderID int64 `json:"folderId,omitempty"`
	Resolved bool  `json:"resolved"`
}

// SetFolderRoleOverride assigns role to the folder with the given remote id.
// It replaces an earlier override of the same role and any other role the
// folder had been assigned (one role per folder, one folder per role).
func (s *Store) SetFolderRoleOverride(ctx context.Context, accountID int64, role domain.FolderRole, remoteID string) error {
	if !IsOverridableRole(role) {
		return fmt.Errorf("%w: %q", ErrRoleNotOverridable, role)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var srv sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT server_role FROM folders WHERE account_id = ? AND remote_id = ?`,
			accountID, remoteID).Scan(&srv); err != nil {
			return notFound(err)
		}
		if domain.FolderRole(srv.String) == domain.RoleInbox {
			return fmt.Errorf("%w: the inbox keeps its role", ErrRoleNotOverridable)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM folder_role_overrides WHERE account_id = ? AND (role = ? OR remote_id = ?)`,
			accountID, string(role), remoteID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO folder_role_overrides(account_id, role, remote_id) VALUES (?, ?, ?)`,
			accountID, string(role), remoteID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, applyRolesSQL+` WHERE account_id = ?`, accountID)
		return err
	})
}

// ClearFolderRoleOverride removes the override of one role (the server's
// detection applies again).
func (s *Store) ClearFolderRoleOverride(ctx context.Context, accountID int64, role domain.FolderRole) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM folder_role_overrides WHERE account_id = ? AND role = ?`,
			accountID, string(role)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, applyRolesSQL+` WHERE account_id = ?`, accountID)
		return err
	})
}

// ClearFolderRoleOverrides removes all overrides of an account ("detect
// automatically").
func (s *Store) ClearFolderRoleOverrides(ctx context.Context, accountID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM folder_role_overrides WHERE account_id = ?`, accountID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, applyRolesSQL+` WHERE account_id = ?`, accountID)
		return err
	})
}

// FolderRoleOverrides lists the overrides of an account in role order.
func (s *Store) FolderRoleOverrides(ctx context.Context, accountID int64) ([]FolderRoleOverride, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT o.role, o.remote_id, f.id FROM folder_role_overrides o
		LEFT JOIN folders f ON f.account_id = o.account_id AND f.remote_id = o.remote_id
		WHERE o.account_id = ? ORDER BY o.role`, accountID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (FolderRoleOverride, error) {
		o := FolderRoleOverride{AccountID: accountID}
		var role string
		var fid sql.NullInt64
		err := sc.Scan(&role, &o.RemoteID, &fid)
		o.Role, o.FolderID, o.Resolved = domain.FolderRole(role), fid.Int64, fid.Valid
		return o, err
	})
}

// ApplyFolderRoles recomputes the effective roles of all folders of an
// account (after the folder list changed).
func (s *Store) ApplyFolderRoles(ctx context.Context, accountID int64) error {
	_, err := s.q(ctx).ExecContext(ctx, applyRolesSQL+` WHERE account_id = ?`, accountID)
	return err
}

// ServerRole is the folder the server (or the name heuristic) chose for a
// role, independent of the user's overrides.
type ServerRole struct {
	FolderID int64
	Source   domain.RoleSource
}

// ServerRoles returns the server-detected folder per role of an account
// (the first folder by id if a provider reported a role twice).
func (s *Store) ServerRoles(ctx context.Context, accountID int64) (map[domain.FolderRole]ServerRole, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, server_role, server_role_source FROM folders
		WHERE account_id = ? AND server_role IS NOT NULL ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.FolderRole]ServerRole{}
	for rows.Next() {
		var id int64
		var role string
		var src sql.NullString
		if err := rows.Scan(&id, &role, &src); err != nil {
			return nil, err
		}
		if _, ok := out[domain.FolderRole(role)]; ok {
			continue
		}
		if !src.Valid {
			src.String = string(domain.RoleSourceServer)
		}
		out[domain.FolderRole(role)] = ServerRole{FolderID: id, Source: domain.RoleSource(src.String)}
	}
	return out, rows.Err()
}

// MoveFolderReferences retargets everything that refers to folder from (the
// user's role override and the identities' sent/drafts folders) to folder
// to. The sync engine calls it when it detects that a folder was renamed on
// the server (the old folder vanished, the new one appeared).
func (s *Store) MoveFolderReferences(ctx context.Context, fromFolderID, toFolderID int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var acc, acc2 int64
		var fromRemote, toRemote string
		if err := tx.QueryRowContext(ctx, `SELECT account_id, remote_id FROM folders WHERE id = ?`, fromFolderID).
			Scan(&acc, &fromRemote); err != nil {
			return notFound(err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT account_id, remote_id FROM folders WHERE id = ?`, toFolderID).
			Scan(&acc2, &toRemote); err != nil {
			return notFound(err)
		}
		if acc != acc2 {
			return errors.New("store: folders belong to different accounts")
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM folder_role_overrides WHERE account_id = ? AND remote_id = ?`,
			acc, fromRemote).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM folder_role_overrides WHERE account_id = ? AND remote_id = ?`,
				acc, toRemote); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE folder_role_overrides SET remote_id = ? WHERE account_id = ? AND remote_id = ?`,
				toRemote, acc, fromRemote); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identities SET sent_folder_id = ? WHERE sent_folder_id = ?`,
			toFolderID, fromFolderID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identities SET drafts_folder_id = ? WHERE drafts_folder_id = ?`,
			toFolderID, fromFolderID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, applyRolesSQL+` WHERE account_id = ?`, acc)
		return err
	})
}

// renameOverrides rewrites override remote ids after a folder (and its
// subfolders) moved from oldRemote to newRemote.
func renameOverrides(ctx context.Context, tx *sql.Tx, accountID int64, oldRemote, newRemote, delim string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE folder_role_overrides SET remote_id = ? WHERE account_id = ? AND remote_id = ?`,
		newRemote, accountID, oldRemote); err != nil {
		return err
	}
	if delim == "" {
		return nil
	}
	prefix := oldRemote + delim
	_, err := tx.ExecContext(ctx, `UPDATE folder_role_overrides SET remote_id = ? || substr(remote_id, ?)
		WHERE account_id = ? AND substr(remote_id, 1, ?) = ?`,
		newRemote+delim, len(prefix)+1, accountID, len(prefix), prefix)
	return err
}

// ==== Behaviour =============================================================

// Trash modes of FolderSettings.TrashMode.
const (
	TrashMove   = "move"   // move to the Trash folder (default)
	TrashMark   = "mark"   // set \Deleted; removed when the folder is expunged
	TrashDelete = "delete" // delete (expunge) immediately
)

// Archive modes of FolderSettings.ArchiveMode.
const (
	ArchiveSingle  = "single"  // one archive folder (default)
	ArchiveYearly  = "yearly"  // Archive/2026
	ArchiveMonthly = "monthly" // Archive/2026/2026-03
)

// FolderSettings is the folder behaviour of an account. Empty fields mean
// the default. Saving a copy of sent mail is stored in the account's
// Extra["sent_autosave"] (read by internal/send) and not here.
type FolderSettings struct {
	TrashMode   string `json:"trashMode,omitempty"`
	ArchiveMode string `json:"archiveMode,omitempty"`
}

// Normalize replaces unknown values by the defaults.
func (f FolderSettings) Normalize() FolderSettings {
	switch f.TrashMode {
	case TrashMove, TrashMark, TrashDelete:
	default:
		f.TrashMode = TrashMove
	}
	switch f.ArchiveMode {
	case ArchiveSingle, ArchiveYearly, ArchiveMonthly:
	default:
		f.ArchiveMode = ArchiveSingle
	}
	return f
}

// FolderSettings returns the folder behaviour of an account (defaults when
// nothing is stored).
func (s *Store) FolderSettings(ctx context.Context, accountID int64) (FolderSettings, error) {
	var raw string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT settings_json FROM account_folder_settings WHERE account_id = ?`, accountID).Scan(&raw)
	var fs FolderSettings
	if errors.Is(err, sql.ErrNoRows) {
		return fs.Normalize(), nil
	}
	if err != nil {
		return fs.Normalize(), err
	}
	if err := json.Unmarshal([]byte(raw), &fs); err != nil {
		return FolderSettings{}.Normalize(), nil
	}
	return fs.Normalize(), nil
}

// SetFolderSettings stores the folder behaviour of an account.
func (s *Store) SetFolderSettings(ctx context.Context, accountID int64, fs FolderSettings) error {
	b, err := json.Marshal(fs.Normalize())
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO account_folder_settings(account_id, settings_json) VALUES (?, ?)
		ON CONFLICT(account_id) DO UPDATE SET settings_json = excluded.settings_json`, accountID, string(b))
	return err
}
