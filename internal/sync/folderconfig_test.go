// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// resync forces a folder-list sync and waits until the worker is idle again.
func (e *env) resync(cond func() bool) {
	e.t.Helper()
	t0 := time.Now()
	e.mgr.SyncNow(e.acc)
	e.eventually("resynced", func() bool {
		st, ok := e.mgr.Status(e.acc)
		return ok && st.State == StateIdle && st.LastSync.After(t0) && cond()
	})
}

func (e *env) hasFolder(remote string) bool {
	_, err := e.st.FolderByRemoteID(e.ctx, e.acc, remote)
	return err == nil
}

func (e *env) roleHolders(role domain.FolderRole) []string {
	fs, err := e.st.ListFolders(e.ctx, e.acc)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, f := range fs {
		if f.Role == role {
			out = append(out, f.RemoteID)
		}
	}
	return out
}

func TestFolderRoleOverrides(t *testing.T) {
	srv := newTestServer(t, "Sent", "Copies", "Trash")
	ext := srv.ext(t)
	e := newEnv(t, srv, nil)
	e.eventually("folders synced", func() bool { return e.hasFolder("Copies") && e.idle() })

	if f := e.folder("Sent"); f.Role != domain.RoleSent || f.RoleSource != domain.RoleSourceName {
		t.Fatalf("Sent detected as %q/%q", f.Role, f.RoleSource)
	}
	if f := e.folder("INBOX"); f.RoleSource != domain.RoleSourceServer {
		t.Errorf("inbox source %q", f.RoleSource)
	}

	// Override: one folder per role.
	if err := e.mgr.Actions().SetFolderRole(e.ctx, e.folder("Copies").ID, domain.RoleSent); err != nil {
		t.Fatal(err)
	}
	if got := e.roleHolders(domain.RoleSent); len(got) != 1 || got[0] != "Copies" {
		t.Fatalf("sent holders = %v", got)
	}
	if f := e.folder("Copies"); f.RoleSource != domain.RoleSourceUser {
		t.Errorf("Copies source %q", f.RoleSource)
	}
	if f := e.folder("Sent"); f.Role != domain.RoleNone || f.RoleSource != "" {
		t.Errorf("server Sent kept %q/%q", f.Role, f.RoleSource)
	}
	// The inbox keeps its role.
	if err := e.mgr.Actions().SetFolderRole(e.ctx, e.folder("INBOX").ID, domain.RoleSent); err == nil {
		t.Error("inbox accepted another role")
	}

	// Survives sync cycles.
	for i := 0; i < 2; i++ {
		e.resync(func() bool { return true })
		if got := e.roleHolders(domain.RoleSent); len(got) != 1 || got[0] != "Copies" {
			t.Fatalf("cycle %d: sent holders = %v", i, got)
		}
	}

	// Survives a rename on the server.
	if _, err := ext.RenameFolder(e.ctx, "Copies", "Kopien"); err != nil {
		t.Fatal(err)
	}
	e.resync(func() bool { return e.hasFolder("Kopien") && !e.hasFolder("Copies") })
	if f := e.folder("Kopien"); f.Role != domain.RoleSent || f.RoleSource != domain.RoleSourceUser {
		t.Fatalf("renamed folder role %q/%q", f.Role, f.RoleSource)
	}
	ovs, err := e.st.FolderRoleOverrides(e.ctx, e.acc)
	if err != nil || len(ovs) != 1 || ovs[0].RemoteID != "Kopien" || !ovs[0].Resolved {
		t.Fatalf("overrides = %+v, %v", ovs, err)
	}

	// Vanished folder: the override is kept (unresolved) and the server's
	// detection applies meanwhile; when the folder comes back it wins again.
	if err := ext.DeleteFolder(e.ctx, "Kopien"); err != nil {
		t.Fatal(err)
	}
	e.resync(func() bool { return !e.hasFolder("Kopien") })
	ovs, _ = e.st.FolderRoleOverrides(e.ctx, e.acc)
	if len(ovs) != 1 || ovs[0].Resolved {
		t.Fatalf("override after vanish = %+v", ovs)
	}
	if got := e.roleHolders(domain.RoleSent); len(got) != 1 || got[0] != "Sent" {
		t.Fatalf("fallback sent holders = %v", got)
	}
	if _, err := ext.CreateFolder(e.ctx, "", "Kopien"); err != nil {
		t.Fatal(err)
	}
	e.resync(func() bool { return e.hasFolder("Kopien") })
	if got := e.roleHolders(domain.RoleSent); len(got) != 1 || got[0] != "Kopien" {
		t.Fatalf("restored sent holders = %v", got)
	}

	// Detect automatically.
	if err := e.mgr.Actions().ClearFolderRole(e.ctx, e.acc, domain.RoleNone); err != nil {
		t.Fatal(err)
	}
	if got := e.roleHolders(domain.RoleSent); len(got) != 1 || got[0] != "Sent" {
		t.Fatalf("auto sent holders = %v", got)
	}

	// Missing role folder: created on the server and assigned.
	fid, err := e.mgr.Actions().CreateRoleFolder(e.ctx, e.acc, domain.RoleArchive, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	e.eventually("archive created on server", func() bool {
		fs, _ := ext.ListFolders(e.ctx)
		for _, f := range fs {
			if f.RemoteID == "Archive" {
				return true
			}
		}
		return false
	})
	e.resync(func() bool { return true })
	if f := e.folder("Archive"); f.ID != fid || f.Role != domain.RoleArchive {
		t.Fatalf("archive folder %+v", f)
	}
}

func TestArchiveModesAndTrashModes(t *testing.T) {
	srv := newTestServer(t, "Archive", "Trash")
	ext := srv.ext(t)
	d2025 := time.Date(2025, 3, 10, 12, 0, 0, 0, time.Local)
	appendMail(t, context.Background(), ext, "INBOX", mail("y1@x", "Bob <bob@example.org>", "a@example.org", "Y", d2025, "", "y"))
	appendMail(t, context.Background(), ext, "INBOX", mail("m1@x", "Bob <bob@example.org>", "a@example.org", "M", d2025, "", "m"))
	appendMail(t, context.Background(), ext, "INBOX", mail("t1@x", "Bob <bob@example.org>", "a@example.org", "T1", d2025, "", "t"))
	appendMail(t, context.Background(), ext, "INBOX", mail("t2@x", "Bob <bob@example.org>", "a@example.org", "T2", d2025, "", "t"))
	appendMail(t, context.Background(), ext, "INBOX", mail("t3@x", "Bob <bob@example.org>", "a@example.org", "T3", d2025, "", "t"))
	e := newEnv(t, srv, nil)
	e.eventually("synced", func() bool {
		return e.has("y1@x") && e.has("m1@x") && e.has("t1@x") && e.has("t2@x") && e.has("t3@x") && e.idle()
	})
	act := e.mgr.Actions()

	// Yearly subfolder.
	if err := e.st.SetFolderSettings(e.ctx, e.acc, store.FolderSettings{ArchiveMode: store.ArchiveYearly}); err != nil {
		t.Fatal(err)
	}
	if err := act.Archive(e.ctx, []int64{e.msg("y1@x").ID}); err != nil {
		t.Fatal(err)
	}
	e.eventually("archived yearly", func() bool { return e.inFolder("y1@x", "Archive/2025") })
	if f := e.folder("Archive/2025"); f.ParentID != e.folder("Archive").ID {
		t.Errorf("year folder parent = %d", f.ParentID)
	}
	// Monthly subfolder (reuses the year folder).
	if err := e.st.SetFolderSettings(e.ctx, e.acc, store.FolderSettings{ArchiveMode: store.ArchiveMonthly}); err != nil {
		t.Fatal(err)
	}
	if err := act.Archive(e.ctx, []int64{e.msg("m1@x").ID}); err != nil {
		t.Fatal(err)
	}
	e.eventually("archived monthly", func() bool { return e.inFolder("m1@x", "Archive/2025/2025-03") })
	e.eventually("folders on server", func() bool {
		fs, _ := ext.ListFolders(e.ctx)
		n := 0
		for _, f := range fs {
			if f.RemoteID == "Archive/2025" || f.RemoteID == "Archive/2025/2025-03" {
				n++
			}
		}
		return n == 2
	})

	// Trash: move (default).
	if err := act.Delete(e.ctx, []int64{e.msg("t1@x").ID}); err != nil {
		t.Fatal(err)
	}
	e.eventually("moved to trash", func() bool { return e.inFolder("t1@x", "Trash") })

	// Trash: mark \Deleted, then expunge.
	if err := e.st.SetFolderSettings(e.ctx, e.acc, store.FolderSettings{TrashMode: store.TrashMark}); err != nil {
		t.Fatal(err)
	}
	if err := act.Delete(e.ctx, []int64{e.msg("t2@x").ID}); err != nil {
		t.Fatal(err)
	}
	if m := e.msg("t2@x"); m.Flags&store.FlagDeleted == 0 || !e.inFolder("t2@x", "INBOX") {
		t.Fatalf("mark mode: flags %b", m.Flags)
	}
	e.eventually("flag on server", func() bool {
		_, fl, ok := serverMessage(e.ctx, ext, "INBOX", "t2@x")
		return ok && hasFlag(fl, domain.FlagDeleted)
	})
	if n, err := act.Expunge(e.ctx, e.folder("INBOX").ID); err != nil || n != 1 {
		t.Fatalf("expunge = %d, %v", n, err)
	}
	e.eventually("expunged on server", func() bool {
		_, _, ok := serverMessage(e.ctx, ext, "INBOX", "t2@x")
		return !ok
	})

	// Trash: delete immediately.
	if err := e.st.SetFolderSettings(e.ctx, e.acc, store.FolderSettings{TrashMode: store.TrashDelete}); err != nil {
		t.Fatal(err)
	}
	if err := act.Delete(e.ctx, []int64{e.msg("t3@x").ID}); err != nil {
		t.Fatal(err)
	}
	e.eventually("deleted on server", func() bool {
		_, _, inInbox := serverMessage(e.ctx, ext, "INBOX", "t3@x")
		_, _, inTrash := serverMessage(e.ctx, ext, "Trash", "t3@x")
		return !inInbox && !inTrash
	})
}

func TestRenamedFoldersPairing(t *testing.T) {
	v := []domain.Folder{{ID: 1, RemoteID: "A/Old", Name: "Old", Delimiter: "/"}, {ID: 2, RemoteID: "B", Name: "B", Delimiter: "/"}}
	n := []domain.Folder{{ID: 10, RemoteID: "A/New", Name: "New", Delimiter: "/"}, {ID: 11, RemoteID: "C/B", Name: "B", Delimiter: "/"},
		{ID: 12, RemoteID: "X", Name: "X", Delimiter: "/"}}
	got := renamedFolders(v, n)
	if got[1] != 10 || got[2] != 11 || len(got) != 2 {
		t.Errorf("pairs = %v", got)
	}
	if got := renamedFolders(v[:1], n[:1]); got[1] != 10 {
		t.Errorf("single pair = %v", got)
	}
	fs := []domain.Folder{{Role: domain.RoleSent}, {Role: domain.RoleSent, RoleSource: domain.RoleSourceName}}
	dedupeRoles(fs)
	if fs[0].Role != domain.RoleSent || fs[1].Role != domain.RoleNone {
		t.Errorf("dedupe = %+v", fs)
	}
}
