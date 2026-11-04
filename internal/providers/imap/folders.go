// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// roleNames is the name-based fallback for servers without SPECIAL-USE.
// Keys are lower-case leaf names.
var roleNames = map[string]domain.FolderRole{
	"sent": domain.RoleSent, "sent items": domain.RoleSent, "sent messages": domain.RoleSent,
	"sent mail": domain.RoleSent, "sent-mail": domain.RoleSent, "gesendet": domain.RoleSent,
	"gesendete objekte": domain.RoleSent, "gesendete elemente": domain.RoleSent,
	"gesendete nachrichten": domain.RoleSent, "envoyés": domain.RoleSent,

	"drafts": domain.RoleDrafts, "draft": domain.RoleDrafts, "entwürfe": domain.RoleDrafts,
	"entwurf": domain.RoleDrafts, "brouillons": domain.RoleDrafts,

	"trash": domain.RoleTrash, "deleted": domain.RoleTrash, "deleted items": domain.RoleTrash,
	"deleted messages": domain.RoleTrash, "bin": domain.RoleTrash, "papierkorb": domain.RoleTrash,
	"gelöscht": domain.RoleTrash, "gelöschte objekte": domain.RoleTrash,
	"gelöschte elemente": domain.RoleTrash, "corbeille": domain.RoleTrash,

	"junk": domain.RoleJunk, "spam": domain.RoleJunk, "junk e-mail": domain.RoleJunk,
	"junk email": domain.RoleJunk, "junk-e-mail": domain.RoleJunk, "bulk mail": domain.RoleJunk,
	"spamverdacht": domain.RoleJunk, "unerwünscht": domain.RoleJunk,

	"archive": domain.RoleArchive, "archives": domain.RoleArchive, "archiv": domain.RoleArchive,

	"templates": domain.RoleTemplates, "template": domain.RoleTemplates, "vorlagen": domain.RoleTemplates,
	"modèles": domain.RoleTemplates,
}

// roleFromAttrs maps RFC 6154 attributes. \All (Gmail "All Mail") is only
// reported as a weak archive hint via the second return value.
func roleFromAttrs(attrs []imap.MailboxAttr) (role domain.FolderRole, allMail bool) {
	for _, a := range attrs {
		switch strings.ToLower(string(a)) {
		case `\sent`:
			return domain.RoleSent, false
		case `\drafts`:
			return domain.RoleDrafts, false
		case `\trash`:
			return domain.RoleTrash, false
		case `\junk`:
			return domain.RoleJunk, false
		case `\archive`:
			return domain.RoleArchive, false
		case `\all`:
			allMail = true
		}
	}
	return domain.RoleNone, allMail
}

// roleFromName applies the name-based fallback to a full mailbox path.
func roleFromName(name string, delim rune) domain.FolderRole {
	if strings.EqualFold(name, "INBOX") {
		return domain.RoleInbox
	}
	leaf := name
	if delim != 0 {
		parts := strings.Split(name, string(delim))
		// only top level or directly below INBOX (Courier/Cyrus "INBOX.Sent")
		if len(parts) > 2 || (len(parts) == 2 && !strings.EqualFold(parts[0], "INBOX")) {
			return domain.RoleNone
		}
		leaf = parts[len(parts)-1]
	}
	return roleNames[strings.ToLower(strings.TrimSpace(leaf))]
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if strings.EqualFold(string(a), string(want)) {
			return true
		}
	}
	return false
}

func leafName(name string, delim rune) string {
	if delim == 0 {
		return name
	}
	if i := strings.LastIndex(name, string(delim)); i >= 0 {
		return name[i+len(string(delim)):]
	}
	return name
}

// assignRoles computes folder roles: SPECIAL-USE first, then the name
// fallback for roles no folder claimed, then \All as archive fallback.
func assignRoles(list []*imap.ListData) []domain.FolderRole {
	roles, _ := assignRolesWithSource(list)
	return roles
}

// assignRolesWithSource is assignRoles that also reports how each role was
// found (attribute = server, name fallback = name).
func assignRolesWithSource(list []*imap.ListData) ([]domain.FolderRole, []domain.RoleSource) {
	roles := make([]domain.FolderRole, len(list))
	srcs := make([]domain.RoleSource, len(list))
	taken := map[domain.FolderRole]bool{}
	allIdx := -1
	for i, d := range list {
		if strings.EqualFold(d.Mailbox, "INBOX") {
			roles[i], srcs[i] = domain.RoleInbox, domain.RoleSourceServer
			taken[domain.RoleInbox] = true
			continue
		}
		r, all := roleFromAttrs(d.Attrs)
		if r != domain.RoleNone {
			roles[i], srcs[i] = r, domain.RoleSourceServer
			taken[r] = true
		} else if all && allIdx < 0 {
			allIdx = i
		}
	}
	// Name fallback; prefer shorter (top-level) paths when several match.
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(list[order[a]].Mailbox) < len(list[order[b]].Mailbox) })
	for _, i := range order {
		if roles[i] != domain.RoleNone {
			continue
		}
		r := roleFromName(list[i].Mailbox, list[i].Delim)
		if r != domain.RoleNone && !taken[r] {
			roles[i], srcs[i] = r, domain.RoleSourceName
			taken[r] = true
		}
	}
	if allIdx >= 0 && !taken[domain.RoleArchive] && roles[allIdx] == domain.RoleNone {
		roles[allIdx], srcs[allIdx] = domain.RoleArchive, domain.RoleSourceServer
	}
	return roles, srcs
}

// ListFolders lists all selectable mailboxes. Unread/Total are filled when
// the server supports LIST-STATUS (otherwise they are left zero; the sync
// worker derives counts from synced data).
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	var folders []domain.Folder
	err := p.do(ctx, func(c *imapclient.Client) error {
		caps := c.Caps()
		ext := caps.Has(imap.CapListExtended)
		opts := &imap.ListOptions{}
		if ext {
			opts.ReturnSubscribed = true
			if caps.Has(imap.CapSpecialUse) {
				opts.ReturnSpecialUse = true
			}
		}
		if caps.Has(imap.CapListStatus) {
			opts.ReturnStatus = &imap.StatusOptions{NumMessages: true, NumUnseen: true}
		}
		if !ext && opts.ReturnStatus == nil {
			opts = nil
		}
		list, err := c.List("", "*", opts).Collect()
		if err != nil {
			return fmt.Errorf("imap: list: %w", err)
		}
		var usable []*imap.ListData
		for _, d := range list {
			if hasAttr(d.Attrs, imap.MailboxAttrNoSelect) || hasAttr(d.Attrs, imap.MailboxAttrNonExistent) {
				continue
			}
			if d.Delim != 0 && p.delim == 0 {
				p.delim = d.Delim
			}
			usable = append(usable, d)
		}
		roles, srcs := assignRolesWithSource(usable)
		for i, d := range usable {
			f := domain.Folder{
				RemoteID:   d.Mailbox,
				Name:       leafName(d.Mailbox, d.Delim),
				Role:       roles[i],
				RoleSource: srcs[i],
				Subscribed: !ext || hasAttr(d.Attrs, imap.MailboxAttrSubscribed),
			}
			if d.Delim != 0 {
				f.Delimiter = string(d.Delim)
			}
			if st := d.Status; st != nil {
				if st.NumMessages != nil {
					f.Total = int(*st.NumMessages)
				}
				if st.NumUnseen != nil {
					f.Unread = int(*st.NumUnseen)
				}
			}
			folders = append(folders, f)
		}
		return nil
	})
	return folders, err
}

// delimiterLocked returns the hierarchy delimiter (LIST "" "").
func (p *Provider) delimiterLocked(c *imapclient.Client) (rune, error) {
	if p.delim != 0 {
		return p.delim, nil
	}
	list, err := c.List("", "", nil).Collect()
	if err != nil {
		return 0, fmt.Errorf("imap: list delimiter: %w", err)
	}
	for _, d := range list {
		if d.Delim != 0 {
			p.delim = d.Delim
		}
	}
	return p.delim, nil
}

func (p *Provider) folderInfo(c *imapclient.Client, name string) domain.Folder {
	f := domain.Folder{RemoteID: name, Name: name, Subscribed: true}
	list, err := c.List("", name, nil).Collect()
	var attrs []imap.MailboxAttr
	if err == nil && len(list) > 0 {
		attrs = list[0].Attrs
		if list[0].Delim != 0 {
			f.Delimiter = string(list[0].Delim)
			f.Name = leafName(name, list[0].Delim)
		}
	}
	r, _ := roleFromAttrs(attrs)
	if r == domain.RoleNone {
		r = roleFromName(name, p.delim)
	}
	f.Role = r
	return f
}

// CreateFolder creates name below parentRemoteID ("" = top level) and
// subscribes it.
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	var f domain.Folder
	if strings.TrimSpace(name) == "" {
		return f, errors.New("imap: empty folder name")
	}
	err := p.do(ctx, func(c *imapclient.Client) error {
		delim, err := p.delimiterLocked(c)
		if err != nil {
			return err
		}
		if delim != 0 && strings.ContainsRune(name, delim) {
			return fmt.Errorf("imap: folder name %q contains the hierarchy delimiter %q", name, delim)
		}
		full := name
		if parentRemoteID != "" {
			full = parentRemoteID + string(delim) + name
		}
		if err := c.Create(full, nil).Wait(); err != nil {
			return fmt.Errorf("imap: create %q: %w", full, err)
		}
		_ = c.Subscribe(full).Wait()
		f = p.folderInfo(c, full)
		return nil
	})
	return f, err
}

// RenameFolder renames remoteID. newName is the new leaf name, kept below the
// same parent; if it contains the hierarchy delimiter it is taken as the new
// full path (moving the folder).
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	var f domain.Folder
	if strings.TrimSpace(newName) == "" {
		return f, errors.New("imap: empty folder name")
	}
	err := p.do(ctx, func(c *imapclient.Client) error {
		delim, err := p.delimiterLocked(c)
		if err != nil {
			return err
		}
		full := newName
		if delim == 0 || !strings.ContainsRune(newName, delim) {
			if i := strings.LastIndex(remoteID, string(delim)); delim != 0 && i >= 0 {
				full = remoteID[:i+len(string(delim))] + newName
			}
		}
		if mb := c.Mailbox(); mb != nil && mb.Name == remoteID {
			if err := unselect(c); err != nil {
				return err
			}
		}
		if err := c.Rename(remoteID, full, nil).Wait(); err != nil {
			return fmt.Errorf("imap: rename %q: %w", remoteID, err)
		}
		_ = c.Unsubscribe(remoteID).Wait()
		_ = c.Subscribe(full).Wait()
		f = p.folderInfo(c, full)
		return nil
	})
	return f, err
}

// DeleteFolder deletes remoteID (and unsubscribes it).
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	if strings.EqualFold(remoteID, "INBOX") {
		return errors.New("imap: INBOX cannot be deleted")
	}
	return p.do(ctx, func(c *imapclient.Client) error {
		if mb := c.Mailbox(); mb != nil && mb.Name == remoteID {
			if err := unselect(c); err != nil {
				return err
			}
		}
		if err := c.Delete(remoteID).Wait(); err != nil {
			return fmt.Errorf("imap: delete %q: %w", remoteID, err)
		}
		_ = c.Unsubscribe(remoteID).Wait()
		return nil
	})
}

func unselect(c *imapclient.Client) error {
	if c.Caps().Has(imap.CapUnselect) {
		return c.Unselect().Wait()
	}
	// Selecting a non-existent mailbox deselects without expunging.
	_, _ = c.Select("TuxMail-Nonexistent-Unselect", &imap.SelectOptions{ReadOnly: true}).Wait()
	return nil
}
