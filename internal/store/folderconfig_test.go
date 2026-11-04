// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestFolderRoleOverridesStore(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	up := func(remote string, role domain.FolderRole, src domain.RoleSource) int64 {
		return must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: remote, Name: remote, Delimiter: "/",
			Role: role, RoleSource: src}))
	}
	inbox := up("INBOX", domain.RoleInbox, domain.RoleSourceServer)
	trash := up("Trash", domain.RoleTrash, domain.RoleSourceServer)
	bin := up("Bin", domain.RoleNone, "")
	role := func(id int64) (domain.FolderRole, domain.RoleSource) {
		f := must[domain.Folder](t)(s.GetFolder(ctx, id))
		return f.Role, f.RoleSource
	}

	if err := s.SetFolderRoleOverride(ctx, acc, domain.RoleInbox, "Bin"); err == nil {
		t.Error("inbox role assignable")
	}
	if err := s.SetFolderRoleOverride(ctx, acc, domain.RoleSent, "INBOX"); err == nil {
		t.Error("inbox folder accepted another role")
	}
	noErr(t, s.SetFolderRoleOverride(ctx, acc, domain.RoleTrash, "Bin"))
	if r, src := role(bin); r != domain.RoleTrash || src != domain.RoleSourceUser {
		t.Errorf("Bin = %q/%q", r, src)
	}
	if r, _ := role(trash); r != domain.RoleNone {
		t.Errorf("Trash kept %q", r)
	}
	// A resync of the server's Trash does not steal the role back.
	up("Trash", domain.RoleTrash, domain.RoleSourceServer)
	if r, _ := role(trash); r != domain.RoleNone {
		t.Errorf("Trash after upsert %q", r)
	}
	// One role per folder.
	noErr(t, s.SetFolderRoleOverride(ctx, acc, domain.RoleJunk, "Bin"))
	if r, _ := role(bin); r != domain.RoleJunk {
		t.Errorf("Bin = %q", r)
	}
	if r, _ := role(trash); r != domain.RoleTrash {
		t.Errorf("Trash not restored: %q", r)
	}
	ovs := must[[]FolderRoleOverride](t)(s.FolderRoleOverrides(ctx, acc))
	if len(ovs) != 1 || ovs[0].Role != domain.RoleJunk || ovs[0].FolderID != bin || !ovs[0].Resolved {
		t.Fatalf("overrides = %+v", ovs)
	}

	// Local rename follows; server rename via MoveFolderReferences.
	noErr(t, s.UpdateFolderPath(ctx, bin, "Spam2", "Spam2", 0))
	if ovs := must[[]FolderRoleOverride](t)(s.FolderRoleOverrides(ctx, acc)); ovs[0].RemoteID != "Spam2" {
		t.Errorf("override after rename = %+v", ovs)
	}
	id := must[domain.Identity](t)(func() (domain.Identity, error) {
		iid, err := s.CreateIdentity(ctx, domain.Identity{AccountID: acc, Name: "A", Email: "a@x"})
		if err != nil {
			return domain.Identity{}, err
		}
		return s.GetIdentity(ctx, iid)
	}())
	noErr(t, s.SetIdentityFolders(ctx, id.ID, bin, 0))
	other := up("Spam3", domain.RoleNone, "")
	noErr(t, s.MoveFolderReferences(ctx, bin, other))
	if r, _ := role(other); r != domain.RoleJunk {
		t.Errorf("moved override role %q", r)
	}
	if sf, _ := must2(t)(s.IdentityFolders(ctx, id.ID)); sf != other {
		t.Errorf("identity sent folder = %d", sf)
	}
	noErr(t, s.DeleteFolder(ctx, other))
	if ovs := must[[]FolderRoleOverride](t)(s.FolderRoleOverrides(ctx, acc)); len(ovs) != 1 || ovs[0].Resolved {
		t.Errorf("unresolved override = %+v", ovs)
	}
	srv := must[map[domain.FolderRole]ServerRole](t)(s.ServerRoles(ctx, acc))
	if srv[domain.RoleTrash].FolderID != trash || srv[domain.RoleInbox].FolderID != inbox {
		t.Errorf("server roles = %+v", srv)
	}
	noErr(t, s.ClearFolderRoleOverrides(ctx, acc))
	if ovs := must[[]FolderRoleOverride](t)(s.FolderRoleOverrides(ctx, acc)); len(ovs) != 0 {
		t.Errorf("after clear = %+v", ovs)
	}

	// Behaviour settings.
	fs := must[FolderSettings](t)(s.FolderSettings(ctx, acc))
	if fs.TrashMode != TrashMove || fs.ArchiveMode != ArchiveSingle {
		t.Errorf("defaults = %+v", fs)
	}
	noErr(t, s.SetFolderSettings(ctx, acc, FolderSettings{TrashMode: TrashMark, ArchiveMode: "bogus"}))
	if fs := must[FolderSettings](t)(s.FolderSettings(ctx, acc)); fs.TrashMode != TrashMark || fs.ArchiveMode != ArchiveSingle {
		t.Errorf("stored = %+v", fs)
	}
}
