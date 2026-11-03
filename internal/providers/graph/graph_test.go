// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/graph/graphtest"
	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

const rawMail = "From: Bob <bob@example.org>\r\nTo: anna@contoso.example\r\nSubject: Hello\r\nMessage-Id: <m1@example.org>\r\nX-Conv: a\r\n\r\nHi Anna\r\n"

func newProv(t *testing.T) (*graphtest.Server, *Provider) {
	t.Helper()
	srv := graphtest.New("tok")
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.BaseURL(), Tokens: rest.StaticToken("tok"),
		Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return srv, p
}

func TestFoldersAndDeltaSync(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	id := srv.AddMessage("inbox", rawMail)
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[domain.FolderRole]string{}
	for _, f := range fs {
		roles[f.Role] = f.RemoteID
	}
	if roles[domain.RoleInbox] != "inbox" || roles[domain.RoleTrash] != "deleteditems" || roles[domain.RoleSent] != "sentitems" {
		t.Fatal(fs)
	}
	d, st, err := p.Sync(ctx, "inbox", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || d.Added[0].Header.MessageID != "m1@example.org" || d.Added[0].Header.ServerThreadID != "conv-a" ||
		d.Added[0].Header.ServerObjectID != id || d.Added[0].Header.From[0].Email != "bob@example.org" {
		t.Fatalf("%+v", d)
	}
	// Flag change by another client, plus a new message, plus a removal.
	srv.SetRead(id, true)
	id2 := srv.AddMessage("inbox", strings.Replace(rawMail, "m1@", "m2@", 1))
	d, st, err = p.Sync(ctx, "inbox", st)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.FlagsChanged) != 1 || d.FlagsChanged[0].Flags[0] != domain.FlagSeen || len(d.Added) != 1 || d.Added[0].Ref.ID != id2 {
		t.Fatalf("%+v", d)
	}
	srv.Remove(id2)
	d, _, err = p.Sync(ctx, "inbox", st)
	if err != nil || len(d.Removed) != 1 || d.Removed[0].ID != id2 {
		t.Fatal(d, err)
	}
	// Invalid token → full resync.
	d, _, err = p.Sync(ctx, "inbox", domain.SyncState(`{"d":"`+srv.BaseURL()+`/me/mailFolders/inbox/messages/delta?$deltatoken=bogus"}`))
	if err != nil || !d.FullResync || len(d.Added) != 1 {
		t.Fatal(d, err)
	}
	rc, err := p.FetchRaw(ctx, domain.RemoteRef{FolderRemoteID: "inbox", ID: id})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	if string(raw) != rawMail {
		t.Fatal(string(raw))
	}
}

func TestFlagsCategoriesMoveDelete(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	id := srv.AddMessage("inbox", rawMail)
	ref := domain.RemoteRef{FolderRemoteID: "inbox", ID: id}
	kw, _ := p.km.ToKeyword("Kunde/Müller")
	srv.Throttle = 1 // first request throttled, retried
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref}, []domain.Flag{domain.FlagSeen, domain.FlagFlagged, domain.Flag(kw)}, nil); err != nil {
		t.Fatal(err)
	}
	m, _ := srv.Get(id)
	if !m.IsRead || !m.Flagged || len(m.Categories) != 1 || m.Categories[0] != "Kunde/Müller" {
		t.Fatalf("%+v", m)
	}
	d, _, _ := p.Sync(ctx, "inbox", "")
	if len(d.Added) != 1 || !hasFlag(d.Added[0].Flags, domain.Flag(kw)) {
		t.Fatal(d.Added)
	}
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref}, nil, []domain.Flag{domain.FlagSeen, domain.Flag(kw)}); err != nil {
		t.Fatal(err)
	}
	m, _ = srv.Get(id)
	if m.IsRead || len(m.Categories) != 0 {
		t.Fatalf("%+v", m)
	}
	out, err := p.Move(ctx, []domain.RemoteRef{ref}, "deleteditems")
	if err != nil || out[0].ID != id || out[0].FolderRemoteID != "deleteditems" {
		t.Fatal(out, err)
	}
	if err := p.Delete(ctx, out); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Get(id); ok {
		t.Fatal("not deleted")
	}
	// Append into drafts.
	r, err := p.CreateDraft(ctx, []byte(rawMail))
	if err != nil || r.FolderRemoteID != "drafts" {
		t.Fatal(r, err)
	}
	f, err := p.CreateFolder(ctx, "", "Projects")
	if err != nil || f.RemoteID != "fld-projects" {
		t.Fatal(f, err)
	}
}

func hasFlag(fs []domain.Flag, f domain.Flag) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func TestTransportBccAndOnBehalf(t *testing.T) {
	srv := graphtest.New("tok")
	defer srv.Close()
	tr, err := NewTransport(Config{BaseURL: srv.BaseURL(), Tokens: rest.StaticToken("tok")})
	if err != nil {
		t.Fatal(err)
	}
	err = tr.Send(context.Background(), domain.OutgoingMessage{Raw: []byte(rawMail),
		Recipients: []string{"anna@contoso.example", "hidden@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.Sent[0], "Bcc: hidden@example.org\r\n") || srv.SentPaths[0] != "/v1.0/me/sendMail" {
		t.Fatal(srv.Sent[0], srv.SentPaths)
	}
	// Send-on-Behalf from a shared mailbox: /me/sendMail + Sender header.
	tr2, _ := NewTransport(Config{BaseURL: srv.BaseURL(), Tokens: rest.StaticToken("tok"), Mailbox: "info@contoso.example",
		SendMode: SendOnBehalf, Self: "anna@contoso.example"})
	if err := tr2.Send(context.Background(), domain.OutgoingMessage{Raw: []byte(rawMail), Recipients: []string{"anna@contoso.example"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(srv.Sent[1], "Sender: anna@contoso.example") || srv.SentPaths[1] != "/v1.0/me/sendMail" {
		t.Fatal(srv.Sent[1], srv.SentPaths)
	}
	// Send-As: /users/{mailbox}/sendMail.
	tr3, _ := NewTransport(Config{BaseURL: srv.BaseURL(), Tokens: rest.StaticToken("tok"), Mailbox: "info@contoso.example", SendMode: SendAs})
	if err := tr3.Send(context.Background(), domain.OutgoingMessage{Raw: []byte(rawMail), Recipients: []string{"anna@contoso.example"}}); err != nil {
		t.Fatal(err)
	}
	if srv.SentPaths[2] != "/v1.0/users/info@contoso.example/sendMail" || strings.Contains(srv.Sent[2], "Sender:") {
		t.Fatal(srv.SentPaths)
	}
}

func TestCalendarVacationFreeBusy(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	ev, err := p.CreateEvent(ctx, "cal1", pimapi.Event{Summary: "Weekly", Start: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
		End: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), IsOnline: true, RRule: "FREQ=WEEKLY;BYDAY=WE;COUNT=5",
		Attendees: []pimapi.Attendee{{Email: "bob@example.org"}}})
	if err != nil || ev.OnlineURL == "" || ev.RRule != "FREQ=WEEKLY;BYDAY=WE;COUNT=5" {
		t.Fatalf("%+v %v", ev, err)
	}
	evs, err := p.ListEvents(ctx, "cal1", time.Now(), time.Now().AddDate(0, 1, 0))
	if err != nil || len(evs) != 1 || !evs[0].Start.Equal(time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)) {
		t.Fatal(evs, err)
	}
	if err := p.Respond(ctx, "cal1", ev.ID, pimapi.Tentative, "", true); err != nil {
		t.Fatal(err)
	}
	if err := (Vacation{C: p.Client}).Push(ctx, vacation.Settings{Enabled: true, InternalText: "Bin weg",
		Start: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 1, 6, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	ars := srv.Settings["automaticRepliesSetting"].(map[string]any)
	if ars["status"] != "scheduled" || !strings.Contains(ars["externalReplyMessage"].(string), "Bin weg") {
		t.Fatal(ars)
	}
	fb, err := p.FreeBusy(ctx, []string{"boss@contoso.example"}, time.Now(), time.Now().Add(48*time.Hour))
	if err != nil || len(fb["boss@contoso.example"]) != 1 {
		t.Fatal(fb, err)
	}
	cs, err := p.ListContacts(ctx, "default")
	if err != nil || len(cs) != 1 || cs[0].Emails[0] != "boss@contoso.example" {
		t.Fatal(cs, err)
	}
}

func TestRRuleRoundTrip(t *testing.T) {
	start := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	for _, rr := range []string{"FREQ=DAILY;INTERVAL=2", "FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20261231T235959Z",
		"FREQ=MONTHLY;BYMONTHDAY=4;COUNT=3", "FREQ=MONTHLY;BYDAY=TU;BYSETPOS=-1"} {
		g, ok := RRuleToGraph(rr, start)
		if !ok {
			t.Fatal(rr)
		}
		if got := RRuleFromGraph(g); got != rr {
			t.Errorf("%s -> %s", rr, got)
		}
	}
}
