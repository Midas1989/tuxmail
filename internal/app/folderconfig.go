// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Folder configuration per account (Settings → account → "Ordner"): which
// folder is Sent, Drafts, Trash, Junk, Archive and Templates, and how
// deleting, archiving and saving sent mail behave. Roles are detected from
// the server (SPECIAL-USE / provider folder types, then a name heuristic);
// the user can override every role. Overrides are applied by the store and
// survive folder-list syncs and server-side renames (internal/sync).

// FolderRoleEntry is one role of the folder configuration.
type FolderRoleEntry struct {
	Role domain.FolderRole `json:"role"`
	// FolderID is the folder that currently has the role (0 = none).
	FolderID int64 `json:"folderId,omitempty"`
	// Source is how FolderID was chosen: server | name | user.
	Source domain.RoleSource `json:"source,omitempty"`
	// ServerFolderID / ServerSource are what automatic detection chose.
	ServerFolderID int64             `json:"serverFolderId,omitempty"`
	ServerSource   domain.RoleSource `json:"serverSource,omitempty"`
	Overridden     bool              `json:"overridden"`
	// Unresolved: the overridden folder no longer exists on the server
	// (the override is kept; OverrideRemoteID names the missing folder).
	Unresolved       bool   `json:"unresolved,omitempty"`
	OverrideRemoteID string `json:"overrideRemoteId,omitempty"`
}

// FolderBehaviour is the folder behaviour of an account.
type FolderBehaviour struct {
	// SaveSent stores a copy of sent mail in the Sent folder. SaveSentAuto
	// follows the provider default (ProviderSavesSent: EAS, Graph, Gmail …
	// file sent mail themselves).
	SaveSent          bool   `json:"saveSent"`
	SaveSentAuto      bool   `json:"saveSentAuto"`
	ProviderSavesSent bool   `json:"providerSavesSent"`
	TrashMode         string `json:"trashMode"`   // move | mark | delete
	ArchiveMode       string `json:"archiveMode"` // single | yearly | monthly
}

// IdentityFolderConfig is the sent/drafts folder of one identity (0 = the
// account's role folder).
type IdentityFolderConfig struct {
	IdentityID     int64  `json:"identityId"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	SentFolderID   int64  `json:"sentFolderId,omitempty"`
	DraftsFolderID int64  `json:"draftsFolderId,omitempty"`
}

// FolderConfig is the folder configuration of an account.
type FolderConfig struct {
	AccountID  int64                  `json:"accountId"`
	Roles      []FolderRoleEntry      `json:"roles"`
	Behaviour  FolderBehaviour        `json:"behaviour"`
	Identities []IdentityFolderConfig `json:"identities"`
	// CanCreate: missing role folders can be created on the server.
	CanCreate bool `json:"canCreate"`
}

const sentAutosaveKey = "sent_autosave"

// GetFolderConfig returns the folder configuration of an account.
func (a *App) GetFolderConfig(ctx context.Context, accountID int64) (FolderConfig, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	folders, err := a.st.ListFolders(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	srv, err := a.st.ServerRoles(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	ovs, err := a.st.FolderRoleOverrides(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	byRole := map[domain.FolderRole]store.FolderRoleOverride{}
	for _, o := range ovs {
		byRole[o.Role] = o
	}
	cfg := FolderConfig{AccountID: accountID, Roles: []FolderRoleEntry{}, Identities: []IdentityFolderConfig{}, CanCreate: a.mgr != nil}
	for _, role := range store.OverridableRoles {
		e := FolderRoleEntry{Role: role}
		for _, f := range folders {
			if f.Role == role {
				e.FolderID, e.Source = f.ID, f.RoleSource
				break
			}
		}
		if s, ok := srv[role]; ok {
			e.ServerFolderID, e.ServerSource = s.FolderID, s.Source
		}
		if o, ok := byRole[role]; ok {
			e.Overridden, e.Unresolved, e.OverrideRemoteID = true, !o.Resolved, o.RemoteID
		}
		cfg.Roles = append(cfg.Roles, e)
	}
	fs, err := a.st.FolderSettings(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	auto := acc
	auto.Extra = map[string]string{}
	for k, v := range acc.Extra {
		if k != sentAutosaveKey {
			auto.Extra[k] = v
		}
	}
	_, explicit := acc.Extra[sentAutosaveKey]
	cfg.Behaviour = FolderBehaviour{SaveSent: !send.ProviderSavesSent(acc), SaveSentAuto: !explicit,
		ProviderSavesSent: send.ProviderSavesSent(auto), TrashMode: fs.TrashMode, ArchiveMode: fs.ArchiveMode}
	ids, err := a.st.ListIdentities(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	for _, id := range ids {
		sent, drafts, err := a.st.IdentityFolders(ctx, id.ID)
		if err != nil {
			return FolderConfig{}, err
		}
		cfg.Identities = append(cfg.Identities, IdentityFolderConfig{IdentityID: id.ID, Name: id.Name, Email: id.Email,
			SentFolderID: sent, DraftsFolderID: drafts})
	}
	return cfg, nil
}

func parseRole(role string) (domain.FolderRole, error) {
	r := domain.FolderRole(strings.ToLower(strings.TrimSpace(role)))
	if !store.IsOverridableRole(r) {
		return "", invalid("unknown folder role %q", role)
	}
	return r, nil
}

func (a *App) folderConfigChanged(ctx context.Context, accountID int64) (FolderConfig, error) {
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: accountID})
	return a.GetFolderConfig(ctx, accountID)
}

// SetFolderRole assigns a role to a folder (manual override). folderID 0
// removes the override, so automatic detection applies again.
func (a *App) SetFolderRole(ctx context.Context, accountID int64, role string, folderID int64) (FolderConfig, error) {
	r, err := parseRole(role)
	if err != nil {
		return FolderConfig{}, err
	}
	if folderID == 0 {
		if a.mgr != nil {
			err = a.mgr.Actions().ClearFolderRole(ctx, accountID, r)
		} else {
			err = a.st.ClearFolderRoleOverride(ctx, accountID, r)
		}
		if err != nil {
			return FolderConfig{}, err
		}
		return a.folderConfigChanged(ctx, accountID)
	}
	f, err := a.st.GetFolder(ctx, folderID)
	if err != nil {
		return FolderConfig{}, err
	}
	if f.AccountID != accountID {
		return FolderConfig{}, invalid("folder %d belongs to another account", folderID)
	}
	if a.mgr != nil {
		err = a.mgr.Actions().SetFolderRole(ctx, folderID, r)
	} else {
		err = a.st.SetFolderRoleOverride(ctx, accountID, r, f.RemoteID)
	}
	if errors.Is(err, store.ErrRoleNotOverridable) {
		return FolderConfig{}, invalid("%v", err)
	}
	if err != nil {
		return FolderConfig{}, err
	}
	return a.folderConfigChanged(ctx, accountID)
}

// ResetFolderRoles removes all overrides of an account ("detect
// automatically") and refreshes the folder list from the server.
func (a *App) ResetFolderRoles(ctx context.Context, accountID int64) (FolderConfig, error) {
	if _, err := a.st.GetAccount(ctx, accountID); err != nil {
		return FolderConfig{}, err
	}
	var err error
	if a.mgr != nil {
		err = a.mgr.Actions().ClearFolderRole(ctx, accountID, domain.RoleNone)
		a.mgr.SyncNow(accountID)
	} else {
		err = a.st.ClearFolderRoleOverrides(ctx, accountID)
	}
	if err != nil {
		return FolderConfig{}, err
	}
	return a.folderConfigChanged(ctx, accountID)
}

// CreateRoleFolder creates a missing role folder on the server (e.g.
// "Archive") and assigns the role to it.
func (a *App) CreateRoleFolder(ctx context.Context, accountID int64, role, name string) (FolderConfig, error) {
	r, err := parseRole(role)
	if err != nil {
		return FolderConfig{}, err
	}
	if strings.TrimSpace(name) == "" {
		return FolderConfig{}, invalid("folder name required")
	}
	if a.mgr == nil {
		return FolderConfig{}, notSupported("creating folders without the sync engine")
	}
	if _, err := a.st.GetAccount(ctx, accountID); err != nil {
		return FolderConfig{}, err
	}
	if _, err := a.mgr.Actions().CreateRoleFolder(ctx, accountID, r, strings.TrimSpace(name)); err != nil {
		if errors.Is(err, store.ErrRoleNotOverridable) {
			return FolderConfig{}, invalid("%v", err)
		}
		return FolderConfig{}, Errorf(http.StatusConflict, CodeConflict, "%v", err)
	}
	return a.folderConfigChanged(ctx, accountID)
}

// SaveFolderBehaviour stores the folder behaviour of an account.
func (a *App) SaveFolderBehaviour(ctx context.Context, accountID int64, b FolderBehaviour) (FolderConfig, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return FolderConfig{}, err
	}
	switch b.TrashMode {
	case "", store.TrashMove, store.TrashMark, store.TrashDelete:
	default:
		return FolderConfig{}, invalid("unknown trash mode %q", b.TrashMode)
	}
	switch b.ArchiveMode {
	case "", store.ArchiveSingle, store.ArchiveYearly, store.ArchiveMonthly:
	default:
		return FolderConfig{}, invalid("unknown archive mode %q", b.ArchiveMode)
	}
	if err := a.st.SetFolderSettings(ctx, accountID, store.FolderSettings{TrashMode: b.TrashMode, ArchiveMode: b.ArchiveMode}); err != nil {
		return FolderConfig{}, err
	}
	// Saving sent copies is read by internal/send from Extra["sent_autosave"]
	// ("true" = the provider files sent mail itself, so no copy is saved).
	extra := map[string]string{}
	for k, v := range acc.Extra {
		extra[k] = v
	}
	if b.SaveSentAuto {
		delete(extra, sentAutosaveKey)
	} else if b.SaveSent {
		extra[sentAutosaveKey] = "false"
	} else {
		extra[sentAutosaveKey] = "true"
	}
	if extra[sentAutosaveKey] != acc.Extra[sentAutosaveKey] || len(extra) != len(acc.Extra) {
		acc.Extra = extra
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return FolderConfig{}, err
		}
	}
	return a.folderConfigChanged(ctx, accountID)
}

// SetIdentityFolders sets the sent and drafts folder of an identity (0 =
// the account's Sent / Drafts folder).
func (a *App) SetIdentityFolders(ctx context.Context, identityID, sentFolderID, draftsFolderID int64) (FolderConfig, error) {
	id, err := a.st.GetIdentity(ctx, identityID)
	if err != nil {
		return FolderConfig{}, err
	}
	for _, fid := range []int64{sentFolderID, draftsFolderID} {
		if fid == 0 {
			continue
		}
		f, err := a.st.GetFolder(ctx, fid)
		if err != nil {
			return FolderConfig{}, err
		}
		if f.AccountID != id.AccountID {
			return FolderConfig{}, invalid("folder %d belongs to another account", fid)
		}
	}
	if err := a.st.SetIdentityFolders(ctx, identityID, sentFolderID, draftsFolderID); err != nil {
		return FolderConfig{}, err
	}
	return a.folderConfigChanged(ctx, id.AccountID)
}

// ExpungeFolder permanently removes the messages of a folder that are
// marked as deleted (trash mode "mark"). It returns their number.
func (a *App) ExpungeFolder(ctx context.Context, folderID int64) (int, error) {
	if a.mgr == nil {
		return 0, notSupported("expunging without the sync engine")
	}
	n, err := a.mgr.Actions().Expunge(ctx, folderID)
	if err == nil && n > 0 {
		a.invalidateViews()
	}
	return n, err
}

// archiveMessages archives mails with the sync engine (archive mode,
// yearly/monthly subfolders) or, without it, moves them to the Archive
// folder. It reports false if the account has no archive folder.
func (a *App) archiveMessages(ctx context.Context, accountID int64, ids []int64) (bool, error) {
	if a.mgr != nil {
		if _, err := a.st.FolderByRole(ctx, accountID, domain.RoleArchive); err != nil {
			return false, nil
		}
		before, _ := a.st.FoldersFor(ctx, ids)
		if err := a.mgr.Actions().Archive(ctx, ids); err != nil {
			return true, err
		}
		after, _ := a.st.FoldersFor(ctx, ids)
		var targets []int64
		for _, fs := range after {
			targets = append(targets, fs...)
		}
		a.afterMailChange(ctx, ids, before, targets...)
		return true, nil
	}
	f, err := a.st.FolderByRole(ctx, accountID, domain.RoleArchive)
	if err != nil {
		return false, nil
	}
	return true, a.Move(ctx, ids, f.ID)
}
