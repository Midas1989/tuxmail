// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/providers/gmail/gmailtest"
	"github.com/Midas1989/Mailclient/internal/providers/graph/graphtest"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

type staticOAuth struct{}

func (staticOAuth) TokenSource(context.Context, domain.Account, domain.SecretStore) (TokenSource, error) {
	return rest.StaticToken("tok"), nil
}

const (
	graphMail1 = "From: Bob <bob@example.org>\r\nTo: anna@contoso.example\r\nSubject: Angebot\r\nMessage-Id: <a1@example.org>\r\nX-Conv: x\r\n\r\nHallo\r\n"
	graphMail2 = "From: Anna <anna@contoso.example>\r\nTo: bob@example.org\r\nSubject: Re: Angebot\r\nMessage-Id: <a2@contoso.example>\r\n" +
		"In-Reply-To: <a1@example.org>\r\nReferences: <a1@example.org>\r\nX-Conv: x\r\n\r\nDanke\r\n"
	gmailMail1 = "From: Dora <dora@example.org>\r\nTo: carla@gmail.com\r\nSubject: Termin\r\nMessage-ID: <g1@example.org>\r\nDate: Mon, 02 Nov 2026 08:00:00 +0000\r\n\r\nHi\r\n"
	gmailMail2 = "From: Carla <carla@gmail.com>\r\nTo: dora@example.org\r\nSubject: Re: Termin\r\nMessage-ID: <g2@gmail.com>\r\n" +
		"In-Reply-To: <g1@example.org>\r\nReferences: <g1@example.org>\r\nDate: Mon, 02 Nov 2026 09:00:00 +0000\r\n\r\nOk\r\n"
)

// TestCloudAccountsSync runs the Manager with a Microsoft Graph and a Gmail
// account against the fakes: folders, messages, flags and conversations
// end up in the store.
func TestCloudAccountsSync(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	gs := graphtest.New("tok")
	defer gs.Close()
	gm := gmailtest.New("tok")
	defer gm.Close()
	gid1 := gs.AddMessage("inbox", graphMail1)
	gs.AddMessage("sentitems", graphMail2)
	gm.AddMessage(gmailMail1, "INBOX", "UNREAD")
	gm.AddMessage(gmailMail2, "SENT")

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
	graphAcc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindGraph, DisplayName: "Contoso", Enabled: true,
		Extra: map[string]string{ExtraGraphBaseURL: gs.BaseURL(), "oauthProvider": "microsoft"}})
	if err != nil {
		t.Fatal(err)
	}
	gmailAcc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindGmail, DisplayName: "Gmail", Enabled: true,
		Extra: map[string]string{ExtraGmailBaseURL: gm.URL, "oauthProvider": "google"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Store: st, Blobs: blobs, Bus: events.New(), Secrets: secrets.NewMemory(),
		Credentials: SecretCredentials{OAuth: staticOAuth{}}, Bodies: BodiesAll,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 200 * time.Millisecond, FolderListInterval: time.Minute, Debounce: 10 * time.Millisecond,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaintenanceDelay: time.Hour,
		ProviderOptions: ProviderOptions{PollInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	eventually := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", what)
	}
	find := func(acc int64, mid string) (store.Message, bool) {
		msg, err := st.FindMessageByMessageID(ctx, acc, mid)
		return msg, err == nil
	}
	sameConversation := func(acc int64, a, b string) bool {
		m1, ok1 := find(acc, a)
		m2, ok2 := find(acc, b)
		return ok1 && ok2 && m1.ConversationID != 0 && m1.ConversationID == m2.ConversationID
	}
	eventually("graph conversation", func() bool { return sameConversation(graphAcc, "a1@example.org", "a2@contoso.example") })
	eventually("gmail conversation", func() bool { return sameConversation(gmailAcc, "g1@example.org", "g2@gmail.com") })

	fs, err := st.ListFolders(ctx, graphAcc)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[domain.FolderRole]bool{}
	for _, f := range fs {
		roles[f.Role] = true
	}
	if !roles[domain.RoleInbox] || !roles[domain.RoleSent] || !roles[domain.RoleTrash] {
		t.Fatal(fs)
	}
	eventually("graph body download", func() bool {
		msg, _ := find(graphAcc, "a1@example.org")
		return msg.BlobHash != ""
	})
	msg, _ := find(graphAcc, "a1@example.org")
	if msg.ServerThreadID != "conv-x" {
		t.Fatalf("%+v", msg)
	}
	g1, _ := find(gmailAcc, "g1@example.org")
	if g1.Flags&store.FlagSeen != 0 {
		t.Fatal("gmail message should be unread")
	}

	// Server-side change on Graph: read elsewhere → flag arrives locally.
	gs.SetRead(gid1, true)
	m.SyncNow(graphAcc)
	eventually("graph read flag", func() bool {
		msg, ok := find(graphAcc, "a1@example.org")
		return ok && msg.Flags&store.FlagSeen != 0
	})
	// Local action → pushed to Graph.
	if err := m.Actions().SetFlags(ctx, []int64{g1.ID}, []domain.Flag{domain.FlagSeen}, nil); err != nil {
		t.Fatal(err)
	}
	eventually("gmail read pushed", func() bool {
		g, ok := gm.Get(g1.ServerObjectID)
		return ok && g.Labels["INBOX"] && !g.Labels["UNREAD"]
	})
}
