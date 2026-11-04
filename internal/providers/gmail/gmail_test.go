// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/gmail/gmailtest"
	"github.com/Midas1989/Mailclient/internal/providers/google"
	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

const raw1 = "From: Dora <dora@example.org>\r\nTo: carla@gmail.com\r\nSubject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nMessage-ID: <g1@example.org>\r\nDate: Mon, 02 Nov 2026 08:00:00 +0000\r\nX-Thread: t-1\r\n\r\nHallo\r\n"

func newProv(t *testing.T) (*gmailtest.Server, *Provider) {
	srv := gmailtest.New("tok")
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL, Tokens: rest.StaticToken("tok")})
	if err != nil {
		t.Fatal(err)
	}
	return srv, p
}

func flagSet(fs []domain.Flag) map[domain.Flag]bool {
	m := map[domain.Flag]bool{}
	for _, f := range fs {
		m[f] = true
	}
	return m
}

func TestLabelsAndHistorySync(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	srv.AddLabel("Label_1", "Kunde")
	srv.AddLabel("Label_2", "Kunde/Müller")
	id := srv.AddMessage(raw1, "INBOX", "UNREAD", "Label_2")
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRemote := map[string]domain.Folder{}
	for _, f := range fs {
		byRemote[f.RemoteID] = f
	}
	if byRemote["INBOX"].Role != domain.RoleInbox || byRemote["Label_1/Label_2"].Name != "Müller" || byRemote["SPAM"].Role != domain.RoleJunk {
		t.Fatal(fs)
	}
	if _, ok := byRemote["STARRED"]; ok {
		t.Fatal("STARRED must not be a folder")
	}
	d, st, err := p.Sync(ctx, "INBOX", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 {
		t.Fatal(d)
	}
	a := d.Added[0]
	if a.Header.Subject != "Grüße" || a.Header.ServerThreadID != "t-1" || a.Header.MessageID != "g1@example.org" ||
		flagSet(a.Flags)[domain.FlagSeen] || len(a.Flags) != 1 {
		t.Fatalf("%+v", a)
	}
	// Another client: read + star, new mail, archive (remove INBOX).
	srv.Modify(id, []string{"STARRED"}, []string{"UNREAD"})
	id2 := srv.AddMessage(strings.Replace(raw1, "g1@", "g2@", 1), "INBOX")
	d, st, err = p.Sync(ctx, "INBOX", st)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.FlagsChanged) != 1 || !flagSet(d.FlagsChanged[0].Flags)[domain.FlagFlagged] || len(d.Added) != 1 || d.Added[0].Ref.ID != id2 {
		t.Fatalf("%+v", d)
	}
	srv.Modify(id2, nil, []string{"INBOX"})
	d, st, err = p.Sync(ctx, "INBOX", st)
	if err != nil || len(d.Removed) != 1 || d.Removed[0].ID != id2 {
		t.Fatal(d, err)
	}
	srv.ExpireHistory()
	d, _, err = p.Sync(ctx, "INBOX", st)
	if err != nil || !d.FullResync || len(d.Added) != 1 {
		t.Fatal(d, err)
	}
	rc, err := p.FetchRaw(ctx, domain.RemoteRef{FolderRemoteID: "INBOX", ID: id})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if string(b) != raw1 {
		t.Fatal(string(b))
	}
}

func TestModifySemantics(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	srv.AddLabel("Label_1", "Archiv")
	id := srv.AddMessage(raw1, "INBOX", "UNREAD")
	ref := domain.RemoteRef{FolderRemoteID: "INBOX", ID: id}
	kw, _ := p.km.ToKeyword("Projekt/X")
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref}, []domain.Flag{domain.FlagSeen, domain.FlagFlagged, domain.Flag(kw)}, nil); err != nil {
		t.Fatal(err)
	}
	m, _ := srv.Get(id)
	if m.Labels["UNREAD"] || !m.Labels["STARRED"] || len(m.Labels) != 3 {
		t.Fatal(m.Labels)
	}
	out, err := p.Move(ctx, []domain.RemoteRef{ref}, "Label_1")
	if err != nil || out[0].FolderRemoteID != "Label_1" {
		t.Fatal(out, err)
	}
	m, _ = srv.Get(id)
	if m.Labels["INBOX"] || !m.Labels["Label_1"] {
		t.Fatal(m.Labels)
	}
	if _, err := p.Move(ctx, out, "TRASH"); err != nil {
		t.Fatal(err)
	}
	m, _ = srv.Get(id)
	if !m.Labels["TRASH"] {
		t.Fatal(m.Labels)
	}
	if err := p.Delete(ctx, []domain.RemoteRef{{ID: id}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Get(id); ok {
		t.Fatal("not deleted")
	}
	r, err := p.Append(ctx, "DRAFT", strings.NewReader(raw1), nil)
	if err != nil || r.ID == "" {
		t.Fatal(r, err)
	}
	if err := p.Send(ctx, domain.OutgoingMessage{Raw: []byte(raw1), Recipients: []string{"carla@gmail.com", "secret@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.Sent[0], "Bcc: secret@example.org") {
		t.Fatal(srv.Sent[0])
	}
	if err := p.Push(ctx, vacation.Settings{Enabled: true, ExternalText: "Urlaub", End: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if srv.Vacation["enableAutoReply"] != true || srv.Vacation["responseBodyPlainText"] != "Urlaub" || srv.Vacation["endTime"] == nil {
		t.Fatal(srv.Vacation)
	}
}

func TestGoogleCalendarPeople(t *testing.T) {
	srv := gmailtest.New("tok")
	defer srv.Close()
	c, err := google.New(google.Config{CalendarURL: srv.URL + "/calendar/v3", PeopleURL: srv.URL + "/people/v1",
		Tokens: rest.StaticToken("tok"), Self: "carla@gmail.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cals, err := c.ListCalendars(ctx)
	if err != nil || len(cals) != 1 || !cals[0].IsDefault {
		t.Fatal(cals, err)
	}
	ev, err := c.CreateEvent(ctx, "primary", pimapi.Event{Summary: "Call", Start: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC),
		End: time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC), IsOnline: true, RRule: "FREQ=WEEKLY;COUNT=3"})
	if err != nil || !strings.Contains(ev.OnlineURL, "meet.google.com") || ev.RRule != "FREQ=WEEKLY;COUNT=3" {
		t.Fatalf("%+v %v", ev, err)
	}
	if err := c.Respond(ctx, "primary", ev.ID, pimapi.Decline, "Sorry", true); err != nil {
		t.Fatal(err)
	}
	evs, err := c.ListEvents(ctx, "primary", time.Now(), time.Now().AddDate(0, 1, 0))
	if err != nil || len(evs) != 1 || evs[0].Response != "declined" {
		t.Fatalf("%+v %v", evs, err)
	}
	fb, err := c.FreeBusy(ctx, []string{"x@example.org"}, time.Now(), time.Now().Add(time.Hour))
	if err != nil || len(fb["x@example.org"]) != 1 {
		t.Fatal(fb, err)
	}
	cs, err := c.ListContacts(ctx, "default")
	if err != nil || len(cs) != 1 || cs[0].Emails[0] != "carla@kunde.example" || cs[0].FamilyName != "Kunde" {
		t.Fatal(cs, err)
	}
}
