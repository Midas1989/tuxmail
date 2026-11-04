// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/locales"
)

type quietLog struct{}

func (quietLog) Printf(string, ...interface{}) {}

func imapTestServer(t *testing.T, mailboxes ...string) domain.ServerConfig {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("alice", "secret")
	for _, mb := range append([]string{"INBOX"}, mailboxes...) {
		if err := user.Create(mb, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapListExtended: {},
			imap.CapListStatus: {}, imap.CapSpecialUse: {}},
		InsecureAuth: true,
		Logger:       quietLog{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return domain.ServerConfig{Host: host, Port: p, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestFolderConfigAndSentAppend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := imapTestServer(t, "Sent", "Ausgang")
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewMemory()
	if err := sec.Set("acc", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS, Secrets: sec,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC,
		Sync: &SyncOptions{Configure: func(c *tsync.Config) {
			c.PollInterval, c.FolderListInterval, c.Debounce = time.Minute, time.Minute, 20*time.Millisecond
			c.MinBackoff, c.MaxBackoff = 20*time.Millisecond, 200*time.Millisecond
		}}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Start()

	accID, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Alice", Incoming: &server,
		SecretRef: "acc", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	identID, err := st.CreateIdentity(ctx, domain.Identity{AccountID: accID, Name: "Alice", Email: "alice@example.org", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.mgr.AddAccount(ctx, accID); err != nil {
		t.Fatal(err)
	}
	folder := func(remote string) domain.Folder {
		f, _ := st.FolderByRemoteID(ctx, accID, remote)
		return f
	}
	waitFor(t, "folders", func() bool { return folder("Ausgang").ID != 0 && folder("Sent").Role == domain.RoleSent })

	cfg, err := a.GetFolderConfig(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	sent := cfg.Roles[0]
	if sent.Role != domain.RoleSent || sent.FolderID != folder("Sent").ID || sent.Source != domain.RoleSourceName || sent.Overridden {
		t.Fatalf("sent entry %+v", sent)
	}
	if !cfg.CanCreate || !cfg.Behaviour.SaveSent || !cfg.Behaviour.SaveSentAuto || cfg.Behaviour.TrashMode != store.TrashMove ||
		len(cfg.Identities) != 1 {
		t.Fatalf("config %+v", cfg)
	}

	// Override Sent and append a sent copy through the send adapter.
	cfg, err = a.SetFolderRole(ctx, accID, "sent", folder("Ausgang").ID)
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Roles[0]; e.FolderID != folder("Ausgang").ID || e.Source != domain.RoleSourceUser || !e.Overridden ||
		e.ServerFolderID != folder("Sent").ID || e.ServerSource != domain.RoleSourceName {
		t.Fatalf("overridden entry %+v", e)
	}
	acc, _ := st.GetAccount(ctx, accID)
	ident, _ := st.GetIdentity(ctx, identID)
	raw := []byte("From: alice@example.org\r\nTo: bob@example.org\r\nSubject: Hi\r\nMessage-ID: <s1@x>\r\n\r\nhi\r\n")
	ad := &sendAdapters{a: a}
	if err := ad.AppendSent(ctx, acc, ident, raw, []domain.Flag{domain.FlagSeen}); err != nil {
		t.Fatal(err)
	}
	inFolder := func(mid string, f domain.Folder) bool {
		m, err := st.FindMessageByMessageID(ctx, accID, mid)
		if err != nil {
			return false
		}
		locs, _ := st.Locations(ctx, m.ID)
		for _, l := range locs {
			if l.FolderID == f.ID {
				return true
			}
		}
		return false
	}
	if !inFolder("s1@x", folder("Ausgang")) || inFolder("s1@x", folder("Sent")) {
		t.Fatal("sent copy not in the overridden Sent folder")
	}

	// The identity's own sent folder beats the account role.
	if _, err := a.SetIdentityFolders(ctx, identID, folder("Sent").ID, 0); err != nil {
		t.Fatal(err)
	}
	raw2 := []byte("From: alice@example.org\r\nTo: bob@example.org\r\nSubject: Hi2\r\nMessage-ID: <s2@x>\r\n\r\nhi\r\n")
	if err := ad.AppendSent(ctx, acc, ident, raw2, nil); err != nil {
		t.Fatal(err)
	}
	if !inFolder("s2@x", folder("Sent")) {
		t.Fatal("identity sent folder ignored")
	}

	// Behaviour: switching off sent copies is what internal/send reads.
	cfg, err = a.SaveFolderBehaviour(ctx, accID, FolderBehaviour{SaveSent: false, TrashMode: store.TrashMark, ArchiveMode: store.ArchiveYearly})
	if err != nil {
		t.Fatal(err)
	}
	acc, _ = st.GetAccount(ctx, accID)
	if !send.ProviderSavesSent(acc) || cfg.Behaviour.SaveSent || cfg.Behaviour.SaveSentAuto || cfg.Behaviour.ProviderSavesSent ||
		cfg.Behaviour.TrashMode != store.TrashMark || cfg.Behaviour.ArchiveMode != store.ArchiveYearly {
		t.Fatalf("behaviour %+v extra %v", cfg.Behaviour, acc.Extra)
	}
	if _, err := a.SaveFolderBehaviour(ctx, accID, FolderBehaviour{TrashMode: "bogus"}); err == nil {
		t.Error("invalid trash mode accepted")
	}

	// Missing role folder: create it; reset clears overrides.
	cfg, err = a.CreateRoleFolder(ctx, accID, "archive", "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Roles[4]; e.Role != domain.RoleArchive || e.FolderID == 0 || e.Source != domain.RoleSourceUser {
		t.Fatalf("archive entry %+v", e)
	}
	if _, err := a.SetFolderRole(ctx, accID, "inbox", folder("Ausgang").ID); err == nil {
		t.Error("inbox role accepted")
	}
	cfg, err = a.ResetFolderRoles(ctx, accID)
	if err != nil {
		t.Fatal(err)
	}
	if e := cfg.Roles[0]; e.FolderID != folder("Sent").ID || e.Overridden {
		t.Fatalf("after reset %+v", e)
	}
}
