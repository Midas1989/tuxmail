// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Folder types ([MS-ASCMD] 2.2.3.186.3).
const (
	FolderUserGeneric  = 1
	FolderInbox        = 2
	FolderDrafts       = 3
	FolderDeleted      = 4
	FolderSent         = 5
	FolderOutbox       = 6
	FolderTasks        = 7
	FolderCalendar     = 8
	FolderContacts     = 9
	FolderNotes        = 10
	FolderJournal      = 11
	FolderUserMail     = 12
	FolderUserCalendar = 13
	FolderUserContacts = 14
	FolderUserTasks    = 15
	FolderUserJournal  = 16
	FolderUserNotes    = 17
	FolderUnknown      = 18
	FolderRecipients   = 19
)

// Folder is an entry of the EAS folder hierarchy.
type Folder struct {
	ServerID string
	ParentID string // "0" or "" for top-level folders
	Name     string
	Type     int
}

// Class returns the EAS class of the folder's items ("Email", "Calendar",
// "Contacts", "Tasks", "Notes" or "").
func (f Folder) Class() string { return classOfType(f.Type) }

func classOfType(t int) string {
	switch t {
	case FolderUserGeneric, FolderInbox, FolderDrafts, FolderDeleted, FolderSent, FolderOutbox, FolderUserMail:
		return "Email"
	case FolderCalendar, FolderUserCalendar:
		return "Calendar"
	case FolderContacts, FolderUserContacts:
		return "Contacts"
	case FolderTasks, FolderUserTasks:
		return "Tasks"
	case FolderNotes, FolderUserNotes:
		return "Notes"
	}
	return ""
}

// FolderSync status codes ([MS-ASCMD] 2.2.3.177.3).
const folderStatusInvalidKey = 9

func recsToFolders(recs []folderRec) []Folder {
	out := make([]Folder, len(recs))
	for i, r := range recs {
		out[i] = Folder{ServerID: r.ServerID, ParentID: r.ParentID, Name: r.Name, Type: r.Type}
	}
	return out
}

// FolderSync synchronizes the folder hierarchy and returns the complete
// folder list. The hierarchy sync key and the list are kept in the account
// state, so only changes are transferred.
func (c *Client) FolderSync(ctx context.Context) ([]Folder, error) {
	for attempt := 0; attempt < 2; attempt++ {
		st := c.snapshotState(ctx)
		key := st.HierarchyKey
		if key == "" {
			key = "0"
		}
		resp, err := c.Do(ctx, "FolderSync", wbxml.E("FolderHierarchy:FolderSync", wbxml.S("FolderHierarchy:SyncKey", key)))
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, errors.New("eas: FolderSync: empty response")
		}
		status := resp.IntOf("FolderHierarchy:Status", 1)
		if status == folderStatusInvalidKey && key != "0" {
			c.log.Info("eas: folder hierarchy sync key invalid, resyncing")
			c.updateState(ctx, func(s *accountState) { s.HierarchyKey, s.Folders = "", nil })
			continue
		}
		if status != 1 {
			return nil, &StatusError{Cmd: "FolderSync", Status: status}
		}
		folders := st.Folders
		if key == "0" {
			folders = nil
		}
		folders = applyFolderChanges(folders, resp.Child("FolderHierarchy:Changes"))
		newKey := resp.TextOf("FolderHierarchy:SyncKey")
		c.updateState(ctx, func(s *accountState) {
			if newKey != "" {
				s.HierarchyKey = newKey
			}
			s.Folders = folders
		})
		return recsToFolders(folders), nil
	}
	return nil, errors.New("eas: FolderSync: sync key keeps being rejected")
}

func applyFolderChanges(folders []folderRec, changes *wbxml.Node) []folderRec {
	if changes == nil {
		return folders
	}
	idx := func(id string) int {
		for i, f := range folders {
			if f.ServerID == id {
				return i
			}
		}
		return -1
	}
	for _, ch := range changes.Children {
		rec := folderRec{
			ServerID: ch.TextOf("FolderHierarchy:ServerId"),
			ParentID: ch.TextOf("FolderHierarchy:ParentId"),
			Name:     ch.TextOf("FolderHierarchy:DisplayName"),
			Type:     ch.IntOf("FolderHierarchy:Type", FolderUnknown),
		}
		if rec.ServerID == "" {
			continue
		}
		i := idx(rec.ServerID)
		switch {
		case ch.Is("FolderHierarchy:Add"), ch.Is("FolderHierarchy:Update"):
			if i >= 0 {
				if !ch.Has("FolderHierarchy:Type") {
					rec.Type = folders[i].Type
				}
				folders[i] = rec
			} else {
				folders = append(folders, rec)
			}
		case ch.Is("FolderHierarchy:Delete"):
			if i >= 0 {
				// Children of a deleted folder are deleted as well.
				gone := map[string]bool{rec.ServerID: true}
				for changed := true; changed; {
					changed = false
					for _, f := range folders {
						if !gone[f.ServerID] && gone[f.ParentID] {
							gone[f.ServerID], changed = true, true
						}
					}
				}
				kept := folders[:0]
				for _, f := range folders {
					if !gone[f.ServerID] {
						kept = append(kept, f)
					}
				}
				folders = kept
			}
		}
	}
	return folders
}

// Folders returns the cached hierarchy, running FolderSync if it is empty.
func (c *Client) Folders(ctx context.Context) ([]Folder, error) {
	st := c.snapshotState(ctx)
	if st.HierarchyKey != "" && len(st.Folders) > 0 {
		return recsToFolders(st.Folders), nil
	}
	return c.FolderSync(ctx)
}

// folderCommand runs FolderCreate/FolderUpdate/FolderDelete with the
// current hierarchy key, refreshing it once if the server rejects it.
func (c *Client) folderCommand(ctx context.Context, cmd string, build func(key string) *wbxml.Node,
	apply func(resp *wbxml.Node, st *accountState)) error {
	for attempt := 0; attempt < 2; attempt++ {
		st := c.snapshotState(ctx)
		if st.HierarchyKey == "" {
			if _, err := c.FolderSync(ctx); err != nil {
				return err
			}
			st = c.snapshotState(ctx)
		}
		resp, err := c.Do(ctx, cmd, build(st.HierarchyKey))
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("eas: %s: empty response", cmd)
		}
		status := resp.IntOf("FolderHierarchy:Status", 1)
		if status == folderStatusInvalidKey && attempt == 0 {
			if _, err := c.FolderSync(ctx); err != nil {
				return err
			}
			continue
		}
		if status != 1 {
			return folderStatusError(cmd, status)
		}
		c.updateState(ctx, func(s *accountState) {
			if k := resp.TextOf("FolderHierarchy:SyncKey"); k != "" {
				s.HierarchyKey = k
			}
			apply(resp, s)
		})
		return nil
	}
	return fmt.Errorf("eas: %s: sync key keeps being rejected", cmd)
}

func folderStatusError(cmd string, status int) error {
	e := &StatusError{Cmd: cmd, Status: status}
	switch status {
	case 2:
		return fmt.Errorf("%w: a folder with this name already exists", e)
	case 3:
		return fmt.Errorf("%w: special folders cannot be changed", e)
	case 4:
		return fmt.Errorf("%w: %w", ErrItemNotFound, e)
	case 5:
		return fmt.Errorf("%w: parent folder not found", e)
	}
	return e
}

// CreateFolder creates a folder below parentID ("0" or "" = top level).
func (c *Client) CreateFolder(ctx context.Context, parentID, name string, typ int) (Folder, error) {
	if parentID == "" {
		parentID = "0"
	}
	if typ == 0 {
		typ = FolderUserMail
	}
	var f Folder
	err := c.folderCommand(ctx, "FolderCreate", func(key string) *wbxml.Node {
		return wbxml.E("FolderHierarchy:FolderCreate", wbxml.S("FolderHierarchy:SyncKey", key),
			wbxml.S("FolderHierarchy:ParentId", parentID), wbxml.S("FolderHierarchy:DisplayName", name),
			wbxml.S("FolderHierarchy:Type", strconv.Itoa(typ)))
	}, func(resp *wbxml.Node, st *accountState) {
		f = Folder{ServerID: resp.TextOf("FolderHierarchy:ServerId"), ParentID: parentID, Name: name, Type: typ}
		st.Folders = append(st.Folders, folderRec{ServerID: f.ServerID, ParentID: parentID, Name: name, Type: typ})
	})
	if err == nil && f.ServerID == "" {
		return f, errors.New("eas: FolderCreate: no ServerId in response")
	}
	return f, err
}

// UpdateFolder renames and/or moves a folder.
func (c *Client) UpdateFolder(ctx context.Context, serverID, parentID, name string) (Folder, error) {
	if parentID == "" {
		parentID = "0"
	}
	var f Folder
	err := c.folderCommand(ctx, "FolderUpdate", func(key string) *wbxml.Node {
		return wbxml.E("FolderHierarchy:FolderUpdate", wbxml.S("FolderHierarchy:SyncKey", key),
			wbxml.S("FolderHierarchy:ServerId", serverID), wbxml.S("FolderHierarchy:ParentId", parentID),
			wbxml.S("FolderHierarchy:DisplayName", name))
	}, func(_ *wbxml.Node, st *accountState) {
		for i := range st.Folders {
			if st.Folders[i].ServerID == serverID {
				st.Folders[i].Name, st.Folders[i].ParentID = name, parentID
				f = recsToFolders(st.Folders[i : i+1])[0]
			}
		}
	})
	if err == nil && f.ServerID == "" {
		f = Folder{ServerID: serverID, ParentID: parentID, Name: name, Type: FolderUserMail}
	}
	return f, err
}

// DeleteFolder deletes a folder and its subfolders.
func (c *Client) DeleteFolder(ctx context.Context, serverID string) error {
	return c.folderCommand(ctx, "FolderDelete", func(key string) *wbxml.Node {
		return wbxml.E("FolderHierarchy:FolderDelete", wbxml.S("FolderHierarchy:SyncKey", key),
			wbxml.S("FolderHierarchy:ServerId", serverID))
	}, func(_ *wbxml.Node, st *accountState) {
		st.Folders = applyFolderChanges(st.Folders, wbxml.E("FolderHierarchy:Changes",
			wbxml.E("FolderHierarchy:Delete", wbxml.S("FolderHierarchy:ServerId", serverID))))
	})
}
