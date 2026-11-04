// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
)

// pimRoutes are the calendar, task and contact endpoints (the PIM block of
// the route list in http.ts).
func pimRoutes() []Route {
	return []Route{
		// Calendars
		{"GET /api/calendars", func(w w, r r, a A) { send(w)(a.ListCalendars(r.Context())) }},
		{"POST /api/calendars", jsonHandler(func(w w, r r, a A, in app.CalendarInput) {
			in.ID = 0
			c, err := a.SaveCalendar(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, c)
		})},
		{"PUT /api/calendars/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.CalendarInput
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveCalendar(r.Context(), in))
			}
		})},
		{"DELETE /api/calendars/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteCalendar(r.Context(), id)) })},
		{"POST /api/calendars/sync", func(w w, r r, a A) { done(w, a.SyncPIM(r.Context())) }},
		{"POST /api/calendars/{id}/import", withID(func(w w, r r, a A, id int64) {
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
			if err != nil {
				badRequest(w, "invalid body")
				return
			}
			n, err := a.ImportICS(r.Context(), id, data)
			reply(w, app.AddedResult{Added: n}, err)
		})},

		// Events
		{"GET /api/calendar-events", func(w w, r r, a A) {
			q := r.URL.Query()
			send(w)(a.ListEvents(r.Context(), app.EventQuery{From: q.Get("from"), To: q.Get("to"), CalendarIDs: ids(q.Get("calendarIds")),
				IncludeHidden: qBool(r, "includeHidden")}))
		}},
		{"POST /api/calendar-events", jsonHandler(func(w w, r r, a A, in app.EventInput) {
			ev, err := a.CreateEvent(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, ev)
		})},
		{"GET /api/calendar-events/{id...}", func(w w, r r, a A) { send(w)(a.GetEvent(r.Context(), r.PathValue("id"))) }},
		{"PUT /api/calendar-events/{id...}", jsonHandler(func(w w, r r, a A, in app.EventInput) {
			send(w)(a.UpdateEvent(r.Context(), r.PathValue("id"), in, r.URL.Query().Get("scope")))
		})},
		{"DELETE /api/calendar-events/{id...}", func(w w, r r, a A) {
			done(w, a.DeleteEvent(r.Context(), r.PathValue("id"), r.URL.Query().Get("scope")))
		}},
		{"POST /api/event-conflicts/{id}/resolve", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Keep bool `json:"keep"`
			}
			if readJSON(w, r, &in) {
				done(w, a.ResolveConflict(r.Context(), id, in.Keep))
			}
		})},
		{"POST /api/freebusy", jsonHandler(func(w w, r r, a A, in app.FreeBusyQuery) { send(w)(a.GetFreeBusy(r.Context(), in)) })},
		{"POST /api/reminders/snooze", jsonHandler(func(w w, r r, a A, in struct {
			EventID string `json:"eventId"`
			Trigger string `json:"trigger"`
			Minutes int    `json:"minutes"`
		}) {
			done(w, a.SnoozeReminder(r.Context(), in.EventID, in.Trigger, in.Minutes))
		})},
		{"POST /api/dav-accounts", jsonHandler(func(w w, r r, a A, in app.DavAccountInput) {
			acc, err := a.AddDavAccount(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, acc)
		})},

		// Tasks
		{"GET /api/tasks", func(w w, r r, a A) {
			q := r.URL.Query()
			send(w)(a.ListTasks(r.Context(), app.TaskQuery{Filter: q.Get("filter"), CalendarIDs: ids(q.Get("calendarIds")), Search: q.Get("q")}))
		}},
		{"POST /api/tasks", jsonHandler(func(w w, r r, a A, in app.TaskInput) {
			in.ID = 0
			t, err := a.SaveTask(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, t)
		})},
		{"PUT /api/tasks/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.TaskInput
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveTask(r.Context(), in))
			}
		})},
		{"DELETE /api/tasks/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteTask(r.Context(), id)) })},

		// Invitations
		{"GET /api/messages/{id}/invitations", withID(func(w w, r r, a A, id int64) { send(w)(a.GetMessageInvitations(r.Context(), id)) })},
		{"GET /api/invitations", func(w w, r r, a A) { send(w)(a.ListInvitations(r.Context())) }},
		{"POST /api/invitations/{id}/respond", withID(func(w w, r r, a A, id int64) {
			var in app.InvitationResponse
			if readJSON(w, r, &in) {
				send(w)(a.RespondInvitation(r.Context(), id, in))
			}
		})},

		// Address books & contacts
		{"GET /api/addressbooks", func(w w, r r, a A) { send(w)(a.ListAddressBooks(r.Context())) }},
		{"POST /api/addressbooks", jsonHandler(func(w w, r r, a A, in app.AddressBookInput) {
			in.ID = 0
			b, err := a.SaveAddressBook(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, b)
		})},
		{"PUT /api/addressbooks/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.AddressBookInput
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveAddressBook(r.Context(), in))
			}
		})},
		{"DELETE /api/addressbooks/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteAddressBook(r.Context(), id)) })},
		{"GET /api/contacts", func(w w, r r, a A) {
			q := r.URL.Query()
			send(w)(a.ListContacts(r.Context(), app.ContactQuery{AddressBookID: qInt64(r, "addressBookId"), Search: q.Get("q"), Group: q.Get("group")}))
		}},
		{"POST /api/contacts", jsonHandler(func(w w, r r, a A, in app.PimContact) {
			in.ID = 0
			c, err := a.SaveContact(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, c)
		})},
		{"GET /api/contacts/duplicates", func(w w, r r, a A) { send(w)(a.FindDuplicateContacts(r.Context())) }},
		{"POST /api/contacts/merge", jsonHandler(func(w w, r r, a A, in struct {
			IDs    []int64         `json:"ids"`
			Merged *app.PimContact `json:"merged"`
		}) {
			send(w)(a.MergeContacts(r.Context(), in.IDs, in.Merged))
		})},
		{"GET /api/contacts/{id}", withID(func(w w, r r, a A, id int64) { send(w)(a.GetContact(r.Context(), id)) })},
		{"PUT /api/contacts/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.PimContact
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveContact(r.Context(), in))
			}
		})},
		{"DELETE /api/contacts/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteContact(r.Context(), id)) })},
		{"GET /api/contacts/{id}/photo", withID(func(w w, r r, a A, id int64) {
			c, err := a.ContactPhoto(r.Context(), id)
			if err != nil {
				writeErr(w, err)
				return
			}
			serveBlob(w, c, true)
		})},
		{"GET /api/contact-groups", func(w w, r r, a A) { send(w)(a.ListContactGroups(r.Context())) }},
		{"GET /api/autocomplete", func(w w, r r, a A) {
			send(w)(a.AutocompleteAddresses(r.Context(), r.URL.Query().Get("q"), qInt(r, "limit")))
		}},
	}
}

// ids parses "1,2,3".
func ids(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}
