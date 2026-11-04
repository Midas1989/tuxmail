// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Calendars written by other providers (EAS easstore, Graph/Gmail pimapi)
// have no PIM meta row: they must still be listed and expanded and, for
// import-only accounts, be read-only.
func TestPIMForeignProviderCalendars(t *testing.T) {
	f := newPIMFixture(t)
	ctx := context.Background()
	graph, err := f.st.CreateAccount(ctx, domain.Account{Kind: domain.KindGraph, DisplayName: "M365", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	easCal, _ := f.st.UpsertCalendar(ctx, store.Calendar{AccountID: f.d.WorkAccount, RemoteID: "eas:1", Name: "EAS"})
	graphCal, _ := f.st.UpsertCalendar(ctx, store.Calendar{AccountID: graph, RemoteID: "cal:x", Name: "Outlook"})
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:ext-1\r\nDTSTART:20261105T100000Z\r\nDTEND:20261105T110000Z\r\nSUMMARY:Foreign\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	start, end := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	for _, c := range []int64{easCal, graphCal} {
		id, _, err := f.st.UpsertCalObject(ctx, store.CalObject{CalendarID: c, UID: "ext-1", Kind: store.CalEvent, ICal: ics, Summary: "Foreign",
			DTStart: start, DTEnd: end})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.ReplaceInstances(ctx, id, []store.Instance{{Start: start, End: end}}); err != nil {
			t.Fatal(err)
		}
	}
	ro := map[int64]bool{}
	for _, c := range must[[]app.PimCalendar](t)(f.a.ListCalendars(ctx)) {
		ro[c.ID] = c.ReadOnly
	}
	if ro[easCal] || !ro[graphCal] {
		t.Fatalf("read-only flags %v", ro)
	}
	evs := must[[]app.CalendarEvent](t)(f.a.ListEvents(ctx, app.EventQuery{From: "2026-11-05T00:00:00Z", To: "2026-11-06T00:00:00Z"}))
	if len(evs) != 2 {
		t.Fatalf("%v", titles(evs))
	}
	if _, err := f.a.CreateEvent(ctx, app.EventInput{CalendarID: graphCal, Title: "x", Start: "2026-11-05T12:00:00Z"}); code(err) != app.CodeReadOnly {
		t.Fatal("graph calendar writable", err)
	}
	if err := f.a.DeleteCalendar(ctx, easCal); err == nil {
		t.Fatal("account calendar deleted locally")
	}
}
