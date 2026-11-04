// SPDX-License-Identifier: AGPL-3.0-or-later

package easstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
	"github.com/Midas1989/Mailclient/internal/store"
)

func TestICalRoundtrip(t *testing.T) {
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	e := &eas.Event{Subject: "Team; sync, weekly", Location: "Room 1", Body: "Line1\nLine2", Start: start, End: start.Add(time.Hour),
		OrganizerName: "Bob", OrganizerEmail: "bob@x", Attendees: []eas.Attendee{{Name: "Ann", Email: "ann@x", Status: 3, Type: 2}},
		Recurrence: &eas.Recurrence{Type: 1, Interval: 2, DayOfWeek: 2 | 8, Occurrences: 10},
		Categories: []string{"Work", "A,B"}, ReminderMinutes: 15, BusyStatus: 2, Sensitivity: 2,
		ExceptionsDeleted: []time.Time{start.AddDate(0, 0, 14)}, TimeZone: "AAAA"}
	ical := eventICal("uid-1", e)
	got, uid := parseEventICal(ical)
	if uid != "uid-1" || got.Subject != e.Subject || got.Body != e.Body || !got.Start.Equal(start) || got.ReminderMinutes != 15 ||
		got.Recurrence == nil || got.Recurrence.DayOfWeek != 10 || got.Recurrence.Interval != 2 || got.Recurrence.Occurrences != 10 ||
		len(got.Attendees) != 1 || got.Attendees[0].Status != 3 || got.Attendees[0].Type != 2 || got.OrganizerName != "Bob" ||
		len(got.Categories) != 2 || got.Categories[1] != "A,B" || len(got.ExceptionsDeleted) != 1 || got.Sensitivity != 2 || got.TimeZone != "AAAA" {
		t.Fatalf("event roundtrip:\n%s\n%+v", ical, got)
	}
	for _, r := range []eas.Recurrence{{Type: 0, Interval: 1}, {Type: 2, DayOfMonth: 15, Interval: 1},
		{Type: 3, WeekOfMonth: 5, DayOfWeek: 32, Interval: 1}, {Type: 5, MonthOfYear: 4, DayOfMonth: 1, Interval: 1},
		{Type: 6, MonthOfYear: 11, WeekOfMonth: 4, DayOfWeek: 16, Interval: 1, Until: start}} {
		back := parseRRule(rrule(&r))
		if back == nil || back.Type != r.Type || back.DayOfWeek != r.DayOfWeek || back.WeekOfMonth != r.WeekOfMonth ||
			back.DayOfMonth != r.DayOfMonth || back.MonthOfYear != r.MonthOfYear || !back.Until.Equal(r.Until) {
			t.Errorf("rrule %q -> %+v (want %+v)", rrule(&r), back, r)
		}
	}
	task := &eas.Task{Subject: "Pay", Due: start, Complete: true, CompletedAt: start, Importance: 2, ReminderAt: start.Add(-time.Hour)}
	gt, _ := parseTaskICal(taskICal("t1", task))
	if gt.Subject != "Pay" || !gt.Due.Equal(start) || !gt.Complete || gt.Importance != 2 || !gt.ReminderAt.Equal(task.ReminderAt) {
		t.Fatalf("task %+v", gt)
	}
	c := &eas.Contact{FirstName: "Anna", LastName: "Müller", Company: "ACME", Department: "R&D", JobTitle: "Dev",
		Emails: []string{"anna@x", "a2@x"}, Phones: map[string]string{"MobilePhoneNumber": "+49 1", "BusinessFaxNumber": "2"},
		Home:     eas.Address{Street: "Weg 1", City: "Berlin", PostalCode: "10115", Country: "DE"},
		Birthday: time.Date(1990, 5, 17, 0, 0, 0, 0, time.UTC), Notes: "VIP; important", Categories: []string{"Family"}}
	vc := contactVCard("c1", c)
	gc, uid := parseVCard(vc)
	if uid != "c1" || gc.FirstName != "Anna" || gc.LastName != "Müller" || gc.Department != "R&D" || len(gc.Emails) != 2 ||
		gc.Phones["MobilePhoneNumber"] != "+49 1" || gc.Phones["BusinessFaxNumber"] != "2" || gc.Home.City != "Berlin" ||
		!gc.Birthday.Equal(c.Birthday) || gc.Notes != c.Notes || gc.Categories[0] != "Family" {
		t.Fatalf("vcard:\n%s\n%+v", vc, gc)
	}
	// Long lines are folded and unfold correctly.
	long := strings.Repeat("ä", 100)
	if got, _ := parseEventICal(eventICal("x", &eas.Event{Subject: long, Start: start, ReminderMinutes: -1})); got.Subject != long {
		t.Fatal("folding")
	}
}

type env struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	srv *eastest.Server
	c   *eas.Client
	ps  *Store
	acc int64
}

func newEnv(t *testing.T) *env {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	acc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindEAS, DisplayName: "EAS", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := eastest.New()
	t.Cleanup(srv.Close)
	c, err := eas.NewClient(eas.ClientConfig{URL: srv.EndpointURL(), Username: "alice", Password: "secret", DeviceID: "PIMTEST",
		State: &eas.MemoryState{}})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, ctx: ctx, st: st, srv: srv, c: c, ps: New(st), acc: acc}
}

func (e *env) sync() eas.PIMResult {
	e.t.Helper()
	res, err := e.c.SyncPIM(e.ctx, e.ps, e.acc, eas.PIMOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(res.Errors) > 0 {
		e.t.Fatalf("errors: %v", res.Errors)
	}
	return res
}

func (e *env) calendar(remote string) store.Calendar {
	e.t.Helper()
	cals, err := e.st.ListAccountCalendars(e.ctx, e.acc)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range cals {
		if c.RemoteID == remote {
			return c
		}
	}
	e.t.Fatalf("calendar %s missing in %+v", remote, cals)
	return store.Calendar{}
}

func TestPIMTwoWay(t *testing.T) {
	e := newEnv(t)
	evID := e.srv.AddItem(eastest.Calendar, "Calendar", wbxml.S("Calendar:Subject", "Dentist"),
		wbxml.S("Calendar:StartTime", "20260310T150000Z"), wbxml.S("Calendar:EndTime", "20260310T160000Z"),
		wbxml.S("Calendar:UID", "dentist-uid"), wbxml.S("Calendar:Reminder", "30"),
		wbxml.E("AirSyncBase:Body", wbxml.S("AirSyncBase:Type", "1"), wbxml.S("AirSyncBase:Data", "Bring card")))
	ctID := e.srv.AddItem(eastest.Contacts, "Contacts", wbxml.S("Contacts:FirstName", "Carl"), wbxml.S("Contacts:LastName", "Example"),
		wbxml.S("Contacts:Email1Address", `"Carl Example" <carl@example.com>`), wbxml.S("Contacts:MobilePhoneNumber", "+491234"))
	e.srv.AddItem(eastest.Tasks, "Tasks", wbxml.S("Tasks:Subject", "Taxes"), wbxml.S("Tasks:Complete", "0"),
		wbxml.S("Tasks:UtcDueDate", "2026-05-31T00:00:00.000Z"))

	res := e.sync()
	if res.Collections != 3 || res.Added != 3 {
		t.Fatalf("result %+v", res)
	}
	cal := e.calendar("eas:" + eastest.Calendar)
	ev, err := e.st.CalObjectByHref(e.ctx, cal.ID, evID)
	if err != nil || ev.UID != "dentist-uid" || ev.Summary != "Dentist" || !strings.Contains(ev.ICal, "TRIGGER:-PT30M") ||
		!strings.Contains(ev.ICal, "DESCRIPTION:Bring card") {
		t.Fatalf("event %+v %v", ev, err)
	}
	if inst, _ := e.st.Instances(e.ctx, ev.ID); len(inst) != 1 {
		t.Fatalf("instances %v", inst)
	}
	tasks := e.calendar("eas:tasks/" + eastest.Tasks)
	todos, _ := e.st.ListCalObjects(e.ctx, tasks.ID)
	if len(todos) != 1 || todos[0].Kind != store.CalTodo || todos[0].Summary != "Taxes" || todos[0].Due.IsZero() {
		t.Fatalf("tasks %+v", todos)
	}
	books, _ := e.st.ListAccountAddressbooks(e.ctx, e.acc)
	if len(books) != 1 {
		t.Fatalf("books %+v", books)
	}
	contacts, _ := e.st.FindContactsByEmail(e.ctx, "carl@example.com")
	if len(contacts) != 1 || contacts[0].DisplayName != "Carl Example" || contacts[0].Href != ctID {
		t.Fatalf("contacts %+v", contacts)
	}

	// Server-side change and delete.
	e.srv.UpdateItem(eastest.Calendar, evID, wbxml.S("Calendar:Subject", "Dentist (moved)"),
		wbxml.S("Calendar:StartTime", "20260311T150000Z"), wbxml.S("Calendar:EndTime", "20260311T160000Z"),
		wbxml.S("Calendar:UID", "dentist-uid"))
	e.srv.DeleteItem(eastest.Contacts, ctID)
	res = e.sync()
	if res.Changed != 1 || res.Deleted != 1 || res.Pushed != 0 {
		t.Fatalf("result %+v", res)
	}
	if ev, _ = e.st.CalObjectByHref(e.ctx, cal.ID, evID); ev.Summary != "Dentist (moved)" || ev.DTStart.Day() != 11 {
		t.Fatalf("event %+v", ev)
	}
	if cs, _ := e.st.ListContacts(e.ctx, books[0].ID); len(cs) != 0 {
		t.Fatalf("contact not deleted: %+v", cs)
	}

	// Local changes: edit, add, delete.
	ev.ICal = strings.Replace(ev.ICal, "SUMMARY:Dentist (moved)", "SUMMARY:Dentist (local)", 1)
	if _, _, err := e.st.UpsertCalObject(e.ctx, ev); err != nil {
		t.Fatal(err)
	}
	newEv := eventICal("local-1", &eas.Event{Subject: "Lunch", Start: time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC),
		End: time.Date(2026, 3, 12, 13, 0, 0, 0, time.UTC), ReminderMinutes: -1})
	newID, _, err := e.st.UpsertCalObject(e.ctx, store.CalObject{CalendarID: cal.ID, UID: "local-1", Kind: store.CalEvent, ICal: newEv})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.UpsertContact(e.ctx, store.Contact{AddressbookID: books[0].ID, UID: "lc1",
		VCard: contactVCard("lc1", &eas.Contact{FirstName: "Dora", LastName: "Local", Emails: []string{"dora@example.com"}})}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteCalObject(e.ctx, todos[0].ID); err != nil {
		t.Fatal(err)
	}
	res = e.sync()
	if res.Pushed != 4 {
		t.Fatalf("pushed %+v", res)
	}
	if got := e.srv.Item(eastest.Calendar, evID).Data.TextOf("Calendar:Subject"); got != "Dentist (local)" {
		t.Fatalf("server subject %q", got)
	}
	items := e.srv.Items(eastest.Calendar)
	if len(items) != 2 {
		t.Fatalf("server calendar %+v", items)
	}
	added, _ := e.st.GetCalObject(e.ctx, newID)
	if added.Href == "" || e.srv.Item(eastest.Calendar, added.Href).Data.TextOf("Calendar:Subject") != "Lunch" {
		t.Fatalf("local add %+v", added)
	}
	if len(e.srv.Items(eastest.Tasks)) != 0 {
		t.Fatal("task not deleted on server")
	}
	if cts := e.srv.Items(eastest.Contacts); len(cts) != 1 || cts[0].Data.TextOf("Contacts:Email1Address") != "dora@example.com" {
		t.Fatalf("server contacts %+v", cts)
	}
	// Nothing pending afterwards; changes are not echoed back.
	res = e.sync()
	if res.Pushed+res.Added+res.Changed+res.Deleted != 0 {
		t.Fatalf("idle sync %+v", res)
	}
	// Read-only mode does not push.
	ev, _ = e.st.CalObjectByHref(e.ctx, cal.ID, evID)
	ev.ICal = strings.Replace(ev.ICal, "Dentist (local)", "X", 1)
	e.st.UpsertCalObject(e.ctx, ev)
	ro, err := e.c.SyncPIM(e.ctx, e.ps, e.acc, eas.PIMOptions{ReadOnly: true})
	if err != nil || ro.Pushed != 0 {
		t.Fatalf("read-only %+v %v", ro, err)
	}
}
