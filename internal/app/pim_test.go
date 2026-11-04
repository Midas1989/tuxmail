// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/pim/vcard"
	"github.com/Midas1989/Mailclient/internal/providers/davtest"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

type sentMail struct {
	to      []string
	subject string
	ics     string
	mime    string
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentMail
}

func (s *fakeSender) Send(_ context.Context, r app.SendRequest) (app.SendResult, error) {
	m := sentMail{subject: r.Message.Subject}
	for _, a := range r.Message.To {
		m.to = append(m.to, a.Email)
	}
	for _, at := range r.Attachments {
		rc, err := at.Open()
		if err == nil {
			b, _ := io.ReadAll(rc)
			rc.Close()
			m.ics, m.mime = string(b), at.MIMEType
		}
	}
	s.mu.Lock()
	s.sent = append(s.sent, m)
	s.mu.Unlock()
	return app.SendResult{}, nil
}

type pimFixture struct {
	a      *app.App
	st     *store.Store
	blobs  *blob.Store
	d      demo.Result
	sender *fakeSender
}

func newPIMFixture(t *testing.T) *pimFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := demo.Seed(ctx, st, blobs, now)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC, Sender: fs, PIM: &app.PIMOptions{Disabled: true, PhoneRegion: "DE"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return &pimFixture{a: a, st: st, blobs: blobs, d: d, sender: fs}
}

func titles(evs []app.CalendarEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Title+"@"+e.Start)
	}
	return out
}

func TestPIMCalendarsAndRecurrenceScopes(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	cals := must[[]app.PimCalendar](t)(f.a.ListCalendars(ctx))
	var local, bday app.PimCalendar
	for _, c := range cals {
		switch c.Kind {
		case "local":
			local = c
		case "birthdays":
			bday = c
		}
	}
	if !local.IsDefault || bday.ID == 0 || !bday.ReadOnly {
		t.Fatalf("calendars %+v", cals)
	}
	ev := must[app.CalendarEvent](t)(f.a.CreateEvent(ctx, app.EventInput{Title: "Standup", Start: "2026-11-02T09:00:00Z", End: "2026-11-02T09:15:00Z",
		Recurrence: &app.RecurrenceRule{Freq: "DAILY", Count: 5}, Alarms: []app.EventAlarm{{MinutesBefore: 10}}, MeetingURL: "https://meet.example/x"}))
	if !ev.Recurring || ev.MeetingURL != "https://meet.example/x" || len(ev.Alarms) != 1 || ev.Alarms[0].MinutesBefore != 10 {
		t.Fatalf("%+v", ev)
	}
	q := app.EventQuery{From: "2026-11-01T00:00:00Z", To: "2026-11-10T00:00:00Z"}
	list := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q))
	if len(list) != 5 {
		t.Fatalf("expected 5 occurrences: %v", titles(list))
	}
	// "this": move the 2nd occurrence.
	second := list[1]
	in := app.EventInput{Title: "Standup (moved)", Start: "2026-11-03T14:00:00Z", End: "2026-11-03T14:15:00Z"}
	moved := must[app.CalendarEvent](t)(f.a.UpdateEvent(ctx, second.ID, in, app.ScopeThis))
	if !moved.Exception || moved.Title != "Standup (moved)" {
		t.Fatalf("%+v", moved)
	}
	// "following" from the 4th: new series with a new title.
	list = must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q))
	fourth := list[3]
	if fourth.Start != "2026-11-05T09:00:00Z" {
		t.Fatalf("order: %v", titles(list))
	}
	must[app.CalendarEvent](t)(f.a.UpdateEvent(ctx, fourth.ID, app.EventInput{Title: "Daily", Start: fourth.Start, End: "2026-11-05T09:30:00Z",
		Recurrence: &app.RecurrenceRule{Freq: "DAILY", Count: 5}}, app.ScopeFollowing))
	list = must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q))
	got := strings.Join(titles(list), " | ")
	want := "Standup@2026-11-02T09:00:00Z | Standup (moved)@2026-11-03T14:00:00Z | Standup@2026-11-04T09:00:00Z | Daily@2026-11-05T09:00:00Z | Daily@2026-11-06T09:00:00Z"
	if got != want {
		t.Fatalf("after following split:\n got %s\nwant %s", got, want)
	}
	// Delete one occurrence (EXDATE).
	if err := f.a.DeleteEvent(ctx, list[2].ID, app.ScopeThis); err != nil {
		t.Fatal(err)
	}
	if n := len(must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q))); n != 4 {
		t.Fatalf("after delete this: %d", n)
	}
	// Move the whole series by dragging an occurrence one hour later.
	first := list[0]
	must[app.CalendarEvent](t)(f.a.UpdateEvent(ctx, first.ID, app.EventInput{Title: "Standup", Start: "2026-11-02T10:00:00Z", End: "2026-11-02T10:15:00Z",
		Recurrence: first.Recurrence}, app.ScopeAll))
	list = must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q))
	if list[0].Start != "2026-11-02T10:00:00Z" {
		t.Fatalf("series shift: %v", titles(list))
	}
	// All-day event in a hidden calendar.
	cal := must[app.PimCalendar](t)(f.a.SaveCalendar(ctx, app.CalendarInput{Name: "Private", Color: "#ff0000"}))
	must[app.CalendarEvent](t)(f.a.CreateEvent(ctx, app.EventInput{CalendarID: cal.ID, Title: "Holiday", Start: "2026-11-06", End: "2026-11-08", AllDay: true}))
	hidden := false
	must[app.PimCalendar](t)(f.a.SaveCalendar(ctx, app.CalendarInput{ID: cal.ID, Visible: &hidden}))
	for _, e := range must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, q)) {
		if e.Title == "Holiday" {
			t.Fatal("hidden calendar listed")
		}
	}
	all := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: q.From, To: q.To, CalendarIDs: []int64{cal.ID}}))
	if len(all) != 1 || !all[0].AllDay || all[0].End != "2026-11-08" {
		t.Fatalf("%+v", all)
	}
	// Free slots respect recurring events.
	slots := must[[]app.TimeSlot](t)(f.a.FindFreeSlots(ctx, app.FreeSlotQuery{From: "2026-11-02T08:00:00Z", To: "2026-11-02T12:00:00Z", DurationMin: 60}))
	for _, s := range slots {
		if s.Start == "2026-11-02T10:00:00Z" || s.Start == "2026-11-02T09:30:00Z" {
			t.Fatalf("busy slot offered: %+v", slots)
		}
	}
	// Read-only calendars reject writes.
	if _, err := f.a.CreateEvent(ctx, app.EventInput{CalendarID: bday.ID, Title: "x", Start: "2026-11-02T10:00:00Z"}); code(err) != app.CodeReadOnly {
		t.Fatal("birthday calendar writable", err)
	}
}

func TestPIMTasks(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	parent := must[app.PimTask](t)(f.a.SaveTask(ctx, app.TaskInput{Title: "Release", Due: "2026-11-03", DueAllDay: true, Priority: 1}))
	sub := must[app.PimTask](t)(f.a.SaveTask(ctx, app.TaskInput{Title: "Changelog", ParentID: parent.ID, Due: "2026-11-01T10:00:00Z", Progress: 50}))
	must[app.PimTask](t)(f.a.SaveTask(ctx, app.TaskInput{Title: "Someday"}))
	if sub.ParentID != parent.ID || sub.Status != "IN-PROCESS" || !sub.Overdue {
		t.Fatalf("%+v", sub)
	}
	today := must[[]app.PimTask](t)(f.a.ListTasks(ctx, app.TaskQuery{Filter: "today"}))
	if len(today) != 2 || today[0].Title != "Changelog" {
		t.Fatalf("today %+v", today)
	}
	if over := must[[]app.PimTask](t)(f.a.ListTasks(ctx, app.TaskQuery{Filter: "overdue"})); len(over) != 1 {
		t.Fatalf("overdue %+v", over)
	}
	if err := f.a.SetTaskDone(ctx, sub.ID, true); err != nil {
		t.Fatal(err)
	}
	if done := must[[]app.PimTask](t)(f.a.ListTasks(ctx, app.TaskQuery{Filter: "completed"})); len(done) != 1 || done[0].Progress != 100 {
		t.Fatalf("completed %+v", done)
	}
	// Recurring task: completing moves it to the next due date.
	rec := must[app.PimTask](t)(f.a.SaveTask(ctx, app.TaskInput{Title: "Backup", Due: "2026-11-03", DueAllDay: true, Recurrence: &app.RecurrenceRule{Freq: "WEEKLY"}}))
	rec = must[app.PimTask](t)(f.a.SaveTask(ctx, app.TaskInput{ID: rec.ID, Title: "Backup", Due: rec.Due, DueAllDay: true, Recurrence: rec.Recurrence, Done: true}))
	if rec.Done || rec.Due != "2026-11-10" {
		t.Fatalf("recurring %+v", rec)
	}
	// Workflow action create_task goes to the default list.
	if err := f.a.DeleteTask(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPIMContactsPersonsDuplicates(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	in := app.PimContact{Contact: vcard.Contact{N: vcard.Name{Given: "Maria", Family: "Sommer"}, Emails: []vcard.Value{{Value: "maria@sommer-design.example", Type: "work"}},
		Phones: []vcard.Value{{Value: "0171 5550000", Type: "cell"}}, Birthday: "1990-11-05", Categories: []string{"Kunden"}},
		Photo: "data:image/png;base64,iVBORw0KGgo="}
	c := must[app.PimContact](t)(f.a.SaveContact(ctx, in))
	if c.DisplayName != "Maria Sommer" || c.PersonID != f.d.Maria || !c.HasPhoto {
		t.Fatalf("contact %+v (maria person %d)", c, f.d.Maria)
	}
	hasPhone := false
	for _, h := range c.Channels {
		hasPhone = hasPhone || (h.Kind == "phone" && h.Value == "+491715550000")
	}
	if !hasPhone {
		t.Fatalf("E.164 handle missing: %+v", c.Channels)
	}
	photo := must[app.AttachmentContent](t)(f.a.ContactPhoto(ctx, c.ID))
	if photo.MIMEType != "image/png" {
		t.Fatal(photo.MIMEType)
	}
	dup := must[app.PimContact](t)(f.a.SaveContact(ctx, app.PimContact{Contact: vcard.Contact{FN: "Sommer, Maria", Phones: []vcard.Value{{Value: "+49 171 5550000"}},
		Org: "Sommer Design"}}))
	groups := must[[]app.DuplicateGroup](t)(f.a.FindDuplicateContacts(ctx))
	if len(groups) != 1 || len(groups[0].Contacts) != 2 || groups[0].Preview.Org != "Sommer Design" {
		t.Fatalf("duplicates %+v", groups)
	}
	merged := must[app.PimContact](t)(f.a.MergeContacts(ctx, []int64{c.ID, dup.ID}, nil))
	if merged.Org != "Sommer Design" || !merged.HasPhoto {
		t.Fatalf("merged %+v", merged)
	}
	if list := must[[]app.PimContact](t)(f.a.ListContacts(ctx, app.ContactQuery{Search: "sommer"})); len(list) != 1 {
		t.Fatalf("after merge %d", len(list))
	}
	if g := must[[]app.ContactGroup](t)(f.a.ListContactGroups(ctx)); len(g) != 1 || g[0].Name != "Kunden" {
		t.Fatalf("groups %+v", g)
	}
	sug := must[[]app.AddressSuggestion](t)(f.a.AutocompleteAddresses(ctx, "mar", 5))
	if len(sug) == 0 || sug[0].Email != "maria@sommer-design.example" || sug[0].Source != "contact" {
		t.Fatalf("autocomplete %+v", sug)
	}
	if sug := must[[]app.AddressSuggestion](t)(f.a.AutocompleteAddresses(ctx, "jonas", 5)); len(sug) == 0 || sug[0].Source != "recent" {
		t.Fatalf("recent %+v", sug)
	}
	// Birthdays calendar.
	evs := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-01T00:00:00Z", To: "2026-11-30T00:00:00Z"}))
	found := false
	for _, e := range evs {
		found = found || (e.Kind == "birthday" && e.Start == "2026-11-05" && e.BirthYear == 1990)
	}
	if !found {
		t.Fatalf("birthday missing: %v", titles(evs))
	}
}

func invitationMail(method, body string) []byte {
	return []byte("From: Maria Sommer <maria@sommer-design.example>\r\nTo: alex@berger-kanzlei.example\r\nSubject: Invitation\r\n" +
		"Message-ID: <inv-" + method + "@sommer-design.example>\r\nDate: Tue, 03 Nov 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nPlease come.\r\n--b\r\n" +
		"Content-Type: text/calendar; charset=utf-8; method=" + method + "\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\n--b--\r\n")
}

func (f *pimFixture) addMail(t *testing.T, raw []byte, key string) int64 {
	t.Helper()
	return f.addMailFrom(t, raw, key, "maria@sommer-design.example")
}

func (f *pimFixture) addMailFrom(t *testing.T, raw []byte, key, from string) int64 {
	t.Helper()
	ctx := context.Background()
	hash, err := f.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := f.st.UpsertMessage(ctx, store.Message{AccountID: f.d.WorkAccount, MessageIDHdr: key, Fingerprint: "fp-" + key, Subject: "Invitation",
		From: from, SentAt: now, ReceivedAt: now, Size: int64(len(raw)), BlobHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const inviteICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Other//EN
METHOD:REQUEST
BEGIN:VEVENT
UID:design-review@sommer-design.example
SEQUENCE:0
DTSTAMP:20261103T100000Z
DTSTART;TZID=Europe/Berlin:20261110T100000
DTEND;TZID=Europe/Berlin:20261110T110000
SUMMARY:Design review
ORGANIZER;CN=Maria Sommer:mailto:maria@sommer-design.example
ATTENDEE;CN=Alex Berger;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:alex@berger-kanzlei.example
END:VEVENT
END:VCALENDAR`

func TestPIMInvitations(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	msg := f.addMail(t, invitationMail("REQUEST", inviteICS), "inv-request@sommer-design.example")
	invs := must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, msg))
	if len(invs) != 1 || invs[0].Method != "REQUEST" || !invs[0].CanRespond || invs[0].Status != "pending" || invs[0].Start != "2026-11-10T09:00:00Z" {
		t.Fatalf("%+v", invs)
	}
	// Unanswered invitations show up as tentative events.
	evs := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-10T00:00:00Z", To: "2026-11-11T00:00:00Z"}))
	if len(evs) != 1 || evs[0].Kind != "invitation" {
		t.Fatalf("%+v", evs)
	}
	inv := must[app.Invitation](t)(f.a.RespondInvitation(ctx, invs[0].ID, app.InvitationResponse{PartStat: "ACCEPTED", Comment: "Gerne"}))
	if inv.Status != "accepted" || inv.EventID == "" || inv.MyPartStat != "ACCEPTED" {
		t.Fatalf("%+v", inv)
	}
	if len(f.sender.sent) != 1 || f.sender.sent[0].to[0] != "maria@sommer-design.example" ||
		!strings.Contains(f.sender.sent[0].ics, "METHOD:REPLY") || !strings.Contains(f.sender.sent[0].mime, "method=REPLY") {
		t.Fatalf("reply %+v", f.sender.sent)
	}
	evs = must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-10T00:00:00Z", To: "2026-11-11T00:00:00Z"}))
	if len(evs) != 1 || evs[0].Kind != "event" || evs[0].MyPartStat != "ACCEPTED" || evs[0].IsOrganizer {
		t.Fatalf("%+v", evs)
	}
	// Update with a new time (sequence 1) needs a new answer; then cancel.
	upd := strings.ReplaceAll(strings.ReplaceAll(inviteICS, "SEQUENCE:0", "SEQUENCE:1"), "T100000\nDTEND", "T120000\nDTEND")
	upd = strings.ReplaceAll(upd, "20261110T110000", "20261110T130000")
	m2 := f.addMail(t, invitationMail("REQUEST", upd), "inv-update@sommer-design.example")
	invs = must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, m2))
	if invs[0].Status != "pending" || invs[0].MyPartStat != "NEEDS-ACTION" {
		t.Fatalf("update %+v", invs[0])
	}
	cancel := strings.ReplaceAll(strings.ReplaceAll(inviteICS, "METHOD:REQUEST", "METHOD:CANCEL"), "SEQUENCE:0", "SEQUENCE:2")
	// Audit B: a CANCEL from somebody else than the organizer is ignored.
	spoof := f.addMailFrom(t, invitationMail("CANCEL", cancel), "inv-spoof-cancel@evil.example", "Mallory <mallory@evil.example>")
	invs = must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, spoof))
	if invs[0].Status == "cancelled" || !invs[0].OrganizerMismatch {
		t.Fatalf("spoofed cancel %+v", invs[0])
	}
	if ev := must[app.CalendarEvent](t)(f.a.GetEvent(ctx, inv.EventID)); ev.Status == "CANCELLED" {
		t.Fatalf("spoofed cancel applied %+v", ev)
	}
	m3 := f.addMail(t, invitationMail("CANCEL", cancel), "inv-cancel@sommer-design.example")
	invs = must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, m3))
	if invs[0].Status != "cancelled" {
		t.Fatalf("cancel %+v", invs[0])
	}
	ev := must[app.CalendarEvent](t)(f.a.GetEvent(ctx, inv.EventID))
	if ev.Status != "CANCELLED" {
		t.Fatalf("%+v", ev)
	}

	// Organizer side: invite an attendee, receive the REPLY.
	f.sender.sent = nil
	mine := must[app.CalendarEvent](t)(f.a.CreateEvent(ctx, app.EventInput{Title: "Kickoff", Start: "2026-11-12T08:00:00Z", End: "2026-11-12T09:00:00Z",
		Attendees: []app.Attendee{{Email: "jonas.keller@post.example", Name: "Jonas"}}}))
	if !mine.IsOrganizer || len(f.sender.sent) != 1 || !strings.Contains(f.sender.sent[0].ics, "METHOD:REQUEST") {
		t.Fatalf("invite %+v %+v", mine, f.sender.sent)
	}
	reply := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nMETHOD:REPLY\nBEGIN:VEVENT\nUID:" + mine.UID + "\nSEQUENCE:0\nDTSTAMP:20261103T110000Z\n" +
		"ORGANIZER:mailto:alex@berger-kanzlei.example\nATTENDEE;PARTSTAT=TENTATIVE:mailto:jonas.keller@post.example\nEND:VEVENT\nEND:VCALENDAR"
	// Audit B: a REPLY on behalf of another attendee (here from maria) is
	// not applied and flagged.
	forged := f.addMail(t, invitationMail("REPLY", strings.ReplaceAll(reply, "TENTATIVE", "DECLINED")), "inv-forged-reply@sommer-design.example")
	invs = must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, forged))
	if !invs[0].OrganizerMismatch {
		t.Fatalf("forged reply not flagged %+v", invs[0])
	}
	if ev := must[app.CalendarEvent](t)(f.a.GetEvent(ctx, mine.ID)); ev.Attendees[0].PartStat == "DECLINED" {
		t.Fatalf("forged reply applied %+v", ev.Attendees)
	}
	m4 := f.addMailFrom(t, invitationMail("REPLY", reply), "inv-reply@post.example", "Jonas <jonas.keller@post.example>")
	invs = must[[]app.Invitation](t)(f.a.GetMessageInvitations(ctx, m4))
	if invs[0].Status != "applied" || invs[0].ReplyPartStat != "TENTATIVE" {
		t.Fatalf("reply %+v", invs[0])
	}
	ev = must[app.CalendarEvent](t)(f.a.GetEvent(ctx, mine.ID))
	if ev.Attendees[0].PartStat != "TENTATIVE" {
		t.Fatalf("%+v", ev.Attendees)
	}
	// Deleting the event cancels it for the attendees.
	if err := f.a.DeleteEvent(ctx, mine.ID, app.ScopeAll); err != nil {
		t.Fatal(err)
	}
	if last := f.sender.sent[len(f.sender.sent)-1]; !strings.Contains(last.ics, "METHOD:CANCEL") {
		t.Fatalf("cancel not sent: %+v", last)
	}
}

func TestPIMCalDAVCardDAVSync(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	calSrv := davtest.NewCalDAV(davtest.Options{SyncCollection: true, Username: "alice", Password: "pw"})
	defer calSrv.Close()
	cardSrv := davtest.NewCardDAV(davtest.Options{Username: "alice", Password: "pw"})
	defer cardSrv.Close()
	calSrv.Store.Put(davtest.CalendarPath+"server.ics", "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nBEGIN:VEVENT\nUID:server-1\nDTSTAMP:20261101T000000Z\n"+
		"DTSTART:20261104T100000Z\nDTEND:20261104T110000Z\nSUMMARY:From server\nEND:VEVENT\nEND:VCALENDAR\n")
	cardSrv.Store.Put(davtest.AddressBookPath+"jonas.vcf", "BEGIN:VCARD\nVERSION:3.0\nUID:jonas\nFN:Jonas Keller\nN:Keller;Jonas;;;\nEMAIL:jonas.keller@post.example\nEND:VCARD\n")
	acc := must[app.Account](t)(f.a.AddDavAccount(ctx, app.DavAccountInput{DisplayName: "Nextcloud", URL: calSrv.URL, CardDAVURL: cardSrv.URL + davtest.Principal,
		Username: "alice", Password: "pw"}))
	if acc.Kind != domain.KindCalDAV {
		t.Fatal(acc.Kind)
	}
	var davCal app.PimCalendar
	for _, c := range must[[]app.PimCalendar](t)(f.a.ListCalendars(ctx)) {
		if c.Kind == "caldav" {
			davCal = c
		}
	}
	if davCal.Name != "Work" || davCal.AccountID != acc.ID {
		t.Fatalf("caldav calendar missing: %+v", davCal)
	}
	evs := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-01T00:00:00Z", To: "2026-11-30T00:00:00Z", CalendarIDs: []int64{davCal.ID}}))
	if len(evs) != 1 || evs[0].Title != "From server" {
		t.Fatalf("%+v", evs)
	}
	// Local create is pushed.
	created := must[app.CalendarEvent](t)(f.a.CreateEvent(ctx, app.EventInput{CalendarID: davCal.ID, Title: "Local", Start: "2026-11-05T10:00:00Z"}))
	if err := f.a.SyncPIM(ctx); err != nil {
		t.Fatal(err)
	}
	pushed := false
	for _, p := range calSrv.Store.Paths(davtest.CalendarPath) {
		pushed = pushed || strings.Contains(calSrv.Store.Get(p), "SUMMARY:Local")
	}
	if !pushed {
		t.Fatalf("not pushed: %v", calSrv.Store.Paths(davtest.CalendarPath))
	}
	// Conflict: edit locally while the server changed the object -> server wins, local copy kept.
	srvEv := evs[0]
	calSrv.Store.Put(davtest.CalendarPath+"server.ics", "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nBEGIN:VEVENT\nUID:server-1\nDTSTAMP:20261102T000000Z\n"+
		"DTSTART:20261104T100000Z\nDTEND:20261104T110000Z\nSUMMARY:Server edit\nEND:VEVENT\nEND:VCALENDAR\n")
	must[app.CalendarEvent](t)(f.a.UpdateEvent(ctx, srvEv.ID, app.EventInput{Title: "Local edit", Start: srvEv.Start, End: srvEv.End}, app.ScopeAll))
	if err := f.a.SyncPIM(ctx); err != nil {
		t.Fatal(err)
	}
	evs = must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-04T00:00:00Z", To: "2026-11-05T00:00:00Z", CalendarIDs: []int64{davCal.ID}}))
	var server, copyEv *app.CalendarEvent
	for i := range evs {
		if evs[i].Conflict {
			copyEv = &evs[i]
		} else {
			server = &evs[i]
		}
	}
	if server == nil || server.Title != "Server edit" || copyEv == nil || copyEv.Title != "Local edit" {
		t.Fatalf("conflict handling: %v", titles(evs))
	}
	// Server-side delete is applied.
	for _, p := range calSrv.Store.Paths(davtest.CalendarPath) {
		if strings.Contains(calSrv.Store.Get(p), "SUMMARY:Local") {
			calSrv.Store.Delete(p)
		}
	}
	if err := f.a.SyncPIM(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.GetEvent(ctx, created.ID); code(err) != app.CodeNotFound {
		t.Fatal("server delete not applied", err)
	}
	// CardDAV: pulled contact links to the existing person; local edit is pushed.
	contacts := must[[]app.PimContact](t)(f.a.ListContacts(ctx, app.ContactQuery{Search: "jonas"}))
	if len(contacts) != 1 || contacts[0].PersonID == 0 {
		t.Fatalf("%+v", contacts)
	}
	c := contacts[0]
	c.Title = "Engineer"
	must[app.PimContact](t)(f.a.SaveContact(ctx, c))
	if err := f.a.SyncPIM(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cardSrv.Store.Get(davtest.AddressBookPath+"jonas.vcf"), "TITLE:Engineer") {
		t.Fatal(cardSrv.Store.Get(davtest.AddressBookPath + "jonas.vcf"))
	}
}
