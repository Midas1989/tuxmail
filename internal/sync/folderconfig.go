// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Folder configuration: role overrides (store.SetFolderRoleOverride) are
// applied by the store whenever a folder is upserted; this file holds the
// sync-side helpers (one folder per role, rename detection) and the
// behaviour-dependent actions (trash mode, archive mode, role folders).

// dedupeRoles keeps every role on the first folder that reports it (IMAP
// already guarantees this; other providers may report a role twice).
func dedupeRoles(fs []domain.Folder) {
	seen := map[domain.FolderRole]bool{}
	for i := range fs {
		r := fs[i].Role
		if r == domain.RoleNone {
			continue
		}
		if seen[r] {
			fs[i].Role, fs[i].RoleSource = domain.RoleNone, ""
			continue
		}
		seen[r] = true
	}
}

// renamedFolders pairs folders that vanished from the server with folders
// that appeared in the same folder-list sync, where the pairing is
// unambiguous: a single vanished and a single new folder, or exactly one new
// folder with the same name (moved), or exactly one vanished and one new
// folder below the same parent (renamed). It maps old to new local ids.
func renamedFolders(vanished, appeared []domain.Folder) map[int64]int64 {
	out := map[int64]int64{}
	if len(vanished) == 0 || len(appeared) == 0 {
		return out
	}
	if len(vanished) == 1 && len(appeared) == 1 {
		out[vanished[0].ID] = appeared[0].ID
		return out
	}
	used := map[int64]bool{}
	for _, v := range vanished {
		var byName, byParent []domain.Folder
		for _, n := range appeared {
			if used[n.ID] {
				continue
			}
			if strings.EqualFold(n.Name, v.Name) {
				byName = append(byName, n)
			}
			if parentPath(n) == parentPath(v) {
				byParent = append(byParent, n)
			}
		}
		switch {
		case len(byName) == 1:
			out[v.ID], used[byName[0].ID] = byName[0].ID, true
		case len(byParent) == 1 && vanishedBelow(vanished, parentPath(v)) == 1:
			out[v.ID], used[byParent[0].ID] = byParent[0].ID, true
		}
	}
	return out
}

func vanishedBelow(vs []domain.Folder, parent string) int {
	n := 0
	for _, v := range vs {
		if parentPath(v) == parent {
			n++
		}
	}
	return n
}

// rolesChanged reports whether any effective role differs from before.
func rolesChanged(ctx context.Context, st *store.Store, accountID int64, before []domain.Folder) bool {
	after, err := st.ListFolders(ctx, accountID)
	if err != nil {
		return true
	}
	old := map[int64]domain.Folder{}
	for _, f := range before {
		old[f.ID] = f
	}
	for _, f := range after {
		o, ok := old[f.ID]
		if !ok || o.Role != f.Role || o.RoleSource != f.RoleSource {
			return true
		}
	}
	return false
}

// ==== behaviour-dependent actions ===========================================

// ErrNoRoleFolder is returned when an account has no folder for a role.
var ErrNoRoleFolder = errors.New("sync: account has no folder for this role")

// SetFolderRole assigns a role to a folder (user override) and publishes
// the folder-list change.
func (a *Actions) SetFolderRole(ctx context.Context, folderID int64, role domain.FolderRole) error {
	f, err := a.folder(ctx, folderID)
	if err != nil {
		return err
	}
	if err := a.m.st.SetFolderRoleOverride(ctx, f.AccountID, role, f.RemoteID); err != nil {
		return err
	}
	a.m.publish(ctx, folderListEvent(f.AccountID))
	return nil
}

// ClearFolderRole removes the user override of a role (role "" = all).
func (a *Actions) ClearFolderRole(ctx context.Context, accountID int64, role domain.FolderRole) error {
	var err error
	if role == domain.RoleNone {
		err = a.m.st.ClearFolderRoleOverrides(ctx, accountID)
	} else {
		err = a.m.st.ClearFolderRoleOverride(ctx, accountID, role)
	}
	if err != nil {
		return err
	}
	a.m.publish(ctx, folderListEvent(accountID))
	return nil
}

// CreateRoleFolder creates a top-level folder on the server and assigns it
// the role (e.g. a missing "Archive"). It returns the folder id.
func (a *Actions) CreateRoleFolder(ctx context.Context, accountID int64, role domain.FolderRole, name string) (int64, error) {
	if !store.IsOverridableRole(role) {
		return 0, store.ErrRoleNotOverridable
	}
	if f, err := a.m.st.FolderByRole(ctx, accountID, role); err == nil {
		return f.ID, nil
	}
	parent := int64(0)
	// Courier/Cyrus-style servers keep everything below INBOX.
	if inbox, err := a.m.st.FolderByRole(ctx, accountID, domain.RoleInbox); err == nil && inbox.Delimiter == "." &&
		strings.EqualFold(inbox.RemoteID, "INBOX") && a.belowInbox(ctx, accountID) {
		parent = inbox.ID
	}
	fid, err := a.childFolder(ctx, accountID, parent, name)
	if err != nil {
		return 0, err
	}
	return fid, a.SetFolderRole(ctx, fid, role)
}

// belowInbox reports whether the account's other folders live below INBOX.
func (a *Actions) belowInbox(ctx context.Context, accountID int64) bool {
	fs, err := a.m.st.ListFolders(ctx, accountID)
	if err != nil {
		return false
	}
	below, top := 0, 0
	for _, f := range fs {
		if strings.EqualFold(f.RemoteID, "INBOX") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(f.RemoteID), "INBOX"+f.Delimiter) {
			below++
		} else {
			top++
		}
	}
	return below > 0 && top == 0
}

// childFolder returns the subfolder of parent (0 = top level) with the
// given name, creating it (locally first, queued for the server) if needed.
func (a *Actions) childFolder(ctx context.Context, accountID, parent int64, name string) (int64, error) {
	fs, err := a.m.st.ListFolders(ctx, accountID)
	if err != nil {
		return 0, err
	}
	for _, f := range fs {
		if f.ParentID == parent && strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return a.CreateFolder(ctx, accountID, parent, name)
}

// archiveFolder returns the folder a message is archived to, following the
// account's archive mode (subfolders by year or month of the message date).
func (a *Actions) archiveFolder(ctx context.Context, msg store.Message, mode string) (int64, error) {
	base, err := a.m.st.FolderByRole(ctx, msg.AccountID, domain.RoleArchive)
	if err != nil {
		return 0, fmt.Errorf("%w (archive) for account %d: %w", ErrNoRoleFolder, msg.AccountID, err)
	}
	if mode != store.ArchiveYearly && mode != store.ArchiveMonthly {
		return base.ID, nil
	}
	t := msg.SentAt
	if t.IsZero() {
		t = msg.ReceivedAt
	}
	if t.IsZero() {
		t = a.m.now()
	}
	t = t.Local()
	year, err := a.childFolder(ctx, msg.AccountID, base.ID, t.Format("2006"))
	if err != nil || mode == store.ArchiveYearly {
		return year, err
	}
	return a.childFolder(ctx, msg.AccountID, year, t.Format("2006-01"))
}

// trashMode returns the account's trash behaviour (cached per call).
func (a *Actions) trashMode(ctx context.Context, cache map[int64]string, accountID int64) string {
	if m, ok := cache[accountID]; ok {
		return m
	}
	fs, _ := a.m.st.FolderSettings(ctx, accountID)
	cache[accountID] = fs.TrashMode
	return fs.TrashMode
}

// Expunge permanently deletes the messages of a folder that are marked
// \Deleted (trash mode "mark").
func (a *Actions) Expunge(ctx context.Context, folderID int64) (int, error) {
	if _, err := a.folder(ctx, folderID); err != nil {
		return 0, err
	}
	ids, err := a.m.st.MatchingMessageIDs(ctx, store.MessageFilter{FolderIDs: []int64{folderID}, FlagsSet: store.FlagDeleted})
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), a.DeletePermanently(ctx, ids)
}
