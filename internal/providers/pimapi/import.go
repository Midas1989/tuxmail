// SPDX-License-Identifier: AGPL-3.0-or-later

package pimapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// Store is the part of *store.Store used by Import.
type Store interface {
	UpsertCalendar(ctx context.Context, c store.Calendar) (int64, error)
	UpsertCalObject(ctx context.Context, o store.CalObject) (int64, bool, error)
	ReplaceInstances(ctx context.Context, objectID int64, insts []store.Instance) error
	ListCalObjects(ctx context.Context, calendarID int64) ([]store.CalObject, error)
	DeleteCalObject(ctx context.Context, id int64) error
	UpsertAddressbook(ctx context.Context, a store.Addressbook) (int64, error)
	UpsertContact(ctx context.Context, c store.Contact) (int64, bool, error)
	ListContacts(ctx context.Context, addressbookID int64) ([]store.Contact, error)
	DeleteContact(ctx context.Context, id int64) error
}

// ImportResult counts imported objects.
type ImportResult struct {
	Calendars    int `json:"calendars"`
	Events       int `json:"events"`
	AddressBooks int `json:"addressBooks"`
	Contacts     int `json:"contacts"`
	Tasks        int `json:"tasks"`
	Deleted      int `json:"deleted"`
}

// ImportOptions bound the calendar window (default -30 days … +1 year).
type ImportOptions struct {
	From, To time.Time
}

// Import mirrors calendars, events, contacts and tasks of src into the
// store. src may implement any of Calendars, Contacts and Tasks. Objects
// missing on the server are deleted locally (server wins, KONZEPT 9.2).
func Import(ctx context.Context, st Store, accountID int64, src any, opt ImportOptions) (ImportResult, error) {
	var res ImportResult
	now := time.Now()
	if opt.From.IsZero() {
		opt.From = now.AddDate(0, 0, -30)
	}
	if opt.To.IsZero() {
		opt.To = now.AddDate(1, 0, 0)
	}
	if cs, ok := src.(Calendars); ok {
		cals, err := cs.ListCalendars(ctx)
		if err != nil {
			return res, fmt.Errorf("pim: list calendars: %w", err)
		}
		for _, c := range cals {
			cid, err := st.UpsertCalendar(ctx, store.Calendar{AccountID: accountID, RemoteID: "cal:" + c.ID, Name: c.Name,
				Color: c.Color, ReadOnly: c.ReadOnly})
			if err != nil {
				return res, err
			}
			res.Calendars++
			evs, err := cs.ListEvents(ctx, c.ID, opt.From, opt.To)
			if err != nil {
				return res, fmt.Errorf("pim: list events of %q: %w", c.Name, err)
			}
			// Group occurrences by series: one object per series/single event.
			type obj struct {
				ev    Event
				insts []store.Instance
			}
			byUID := map[string]*obj{}
			var order []string
			for _, ev := range evs {
				uid := ev.UID
				if uid == "" {
					uid = ev.ID
					if ev.SeriesID != "" {
						uid = ev.SeriesID
					}
				}
				o := byUID[uid]
				if o == nil {
					o = &obj{ev: ev}
					byUID[uid] = o
					order = append(order, uid)
				}
				o.insts = append(o.insts, store.Instance{Start: ev.Start, End: ev.End})
			}
			seen := map[string]bool{}
			for _, uid := range order {
				o := byUID[uid]
				seen[uid] = true
				id, _, err := st.UpsertCalObject(ctx, store.CalObject{CalendarID: cid, UID: uid, Kind: store.CalEvent,
					Href: o.ev.ID, ETag: o.ev.ETag, ICal: EventICal(uid, o.ev), DTStart: o.ev.Start, DTEnd: o.ev.End,
					RRule: o.ev.RRule, Summary: o.ev.Summary, Status: strings.ToUpper(o.ev.Status)})
				if err != nil {
					return res, err
				}
				if err := st.ReplaceInstances(ctx, id, o.insts); err != nil {
					return res, err
				}
				res.Events++
			}
			n, err := deleteMissingObjects(ctx, st, cid, store.CalEvent, seen)
			if err != nil {
				return res, err
			}
			res.Deleted += n
		}
	}
	if ts, ok := src.(Tasks); ok {
		lists, err := ts.ListTaskLists(ctx)
		if err != nil {
			return res, fmt.Errorf("pim: list task lists: %w", err)
		}
		for _, l := range lists {
			cid, err := st.UpsertCalendar(ctx, store.Calendar{AccountID: accountID, RemoteID: "tasks:" + l.ID, Name: l.Name})
			if err != nil {
				return res, err
			}
			tasks, err := ts.ListTasks(ctx, l.ID)
			if err != nil {
				return res, fmt.Errorf("pim: list tasks: %w", err)
			}
			seen := map[string]bool{}
			for _, t := range tasks {
				seen[t.ID] = true
				status := "NEEDS-ACTION"
				if t.Done {
					status = "COMPLETED"
				}
				if _, _, err := st.UpsertCalObject(ctx, store.CalObject{CalendarID: cid, UID: t.ID, Kind: store.CalTodo,
					Href: t.ID, ICal: TaskICal(t), Summary: t.Title, Due: t.Due, CompletedAt: t.Completed, Status: status}); err != nil {
					return res, err
				}
				res.Tasks++
			}
			n, err := deleteMissingObjects(ctx, st, cid, store.CalTodo, seen)
			if err != nil {
				return res, err
			}
			res.Deleted += n
		}
	}
	if cs, ok := src.(Contacts); ok {
		books, err := cs.ListAddressBooks(ctx)
		if err != nil {
			return res, fmt.Errorf("pim: list address books: %w", err)
		}
		for _, b := range books {
			bid, err := st.UpsertAddressbook(ctx, store.Addressbook{AccountID: accountID, RemoteID: "book:" + b.ID, Name: b.Name})
			if err != nil {
				return res, err
			}
			res.AddressBooks++
			list, err := cs.ListContacts(ctx, b.ID)
			if err != nil {
				return res, fmt.Errorf("pim: list contacts: %w", err)
			}
			seen := map[string]bool{}
			for _, c := range list {
				seen[c.ID] = true
				if _, _, err := st.UpsertContact(ctx, store.Contact{AddressbookID: bid, UID: c.ID, Href: c.ID, ETag: c.ETag,
					VCard: ContactVCard(c), DisplayName: c.DisplayName, Emails: c.Emails}); err != nil {
					return res, err
				}
				res.Contacts++
			}
			existing, err := st.ListContacts(ctx, bid)
			if err != nil {
				return res, err
			}
			for _, e := range existing {
				if !seen[e.UID] {
					if err := st.DeleteContact(ctx, e.ID); err != nil {
						return res, err
					}
					res.Deleted++
				}
			}
		}
	}
	return res, nil
}

func deleteMissingObjects(ctx context.Context, st Store, calID int64, kind string, seen map[string]bool) (int, error) {
	existing, err := st.ListCalObjects(ctx, calID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range existing {
		if o.Kind == kind && !seen[o.UID] {
			if err := st.DeleteCalObject(ctx, o.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ==== iCalendar / vCard rendering ============================================

func icalEscape(s string) string {
	return stripCtl(strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\r", `\n`, "\n", `\n`).Replace(s))
}

// stripCtl removes control characters (including a lone CR/LF and DEL) so a
// value cannot start a new content line (property/component injection).
func stripCtl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// safeToken cleans a value that is written unescaped (e-mail address, RRULE)
// and drops it entirely (empty result) if it contains whitespace or
// delimiters that could break out of the property.
func safeToken(s string, extra string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\r\n\"\\<>"+extra) || stripCtl(s) != s {
		return ""
	}
	return s
}

// safeURL returns s if it is a plain http(s) URL without control characters
// or whitespace, else "".
func safeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\"<>\\") || stripCtl(s) != s {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return s
}

// fold folds a content line at 75 octets (RFC 5545 3.1).
func fold(line string) string {
	if len(line) <= 75 {
		return line + "\r\n"
	}
	var b strings.Builder
	cur := 0
	for i, r := range line {
		rl := len(string(r))
		if cur+rl > 75 {
			b.WriteString("\r\n ")
			cur = 1
		}
		b.WriteString(line[i : i+rl])
		cur += rl
	}
	b.WriteString("\r\n")
	return b.String()
}

func icalTime(t time.Time, allDay bool) string {
	if allDay {
		return ";VALUE=DATE:" + t.Format("20060102")
	}
	return ":" + t.UTC().Format("20060102T150405Z")
}

var partstat = map[string]string{"accepted": "ACCEPTED", "tentative": "TENTATIVE", "declined": "DECLINED", "needs-action": "NEEDS-ACTION"}

// EventICal renders ev as a VCALENDAR with one VEVENT.
func EventICal(uid string, ev Event) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(fold(s)) }
	w("BEGIN:VCALENDAR")
	w("VERSION:2.0")
	w("PRODID:-//TuxMail//Cloud PIM//EN")
	w("BEGIN:VEVENT")
	w("UID:" + icalEscape(uid))
	stamp := ev.LastModified
	if stamp.IsZero() {
		stamp = time.Unix(0, 0)
	}
	w("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z"))
	w("DTSTART" + icalTime(ev.Start, ev.AllDay))
	if !ev.End.IsZero() {
		w("DTEND" + icalTime(ev.End, ev.AllDay))
	}
	w("SUMMARY:" + icalEscape(ev.Summary))
	if ev.Location != "" {
		w("LOCATION:" + icalEscape(ev.Location))
	}
	if ev.Body != "" {
		w("DESCRIPTION:" + icalEscape(ev.Body))
	}
	if rr := safeToken(ev.RRule, ""); rr != "" {
		w("RRULE:" + rr)
	}
	if ev.Status != "" {
		w("STATUS:" + strings.ToUpper(ev.Status))
	}
	if u := safeURL(ev.OnlineURL); u != "" {
		w("URL:" + u)
		w("CONFERENCE;VALUE=URI;FEATURE=VIDEO:" + u)
	}
	if len(ev.Categories) > 0 {
		cs := make([]string, len(ev.Categories))
		for i, c := range ev.Categories {
			cs[i] = icalEscape(c)
		}
		w("CATEGORIES:" + strings.Join(cs, ","))
	}
	if ev.Organizer != nil && safeToken(ev.Organizer.Email, ";,:") != "" {
		w("ORGANIZER;CN=" + quoteParam(ev.Organizer.Name) + ":mailto:" + safeToken(ev.Organizer.Email, ";,:"))
	}
	for _, a := range ev.Attendees {
		addr := safeToken(a.Email, ";,:")
		if addr == "" {
			continue
		}
		role := "REQ-PARTICIPANT"
		switch a.Role {
		case "optional":
			role = "OPT-PARTICIPANT"
		case "resource":
			role = "NON-PARTICIPANT"
		}
		ps := partstat[a.Status]
		if ps == "" {
			ps = "NEEDS-ACTION"
		}
		w("ATTENDEE;CN=" + quoteParam(a.Name) + ";ROLE=" + role + ";PARTSTAT=" + ps + ":mailto:" + addr)
	}
	if ev.Reminder > 0 {
		w("BEGIN:VALARM")
		w("ACTION:DISPLAY")
		w("DESCRIPTION:" + icalEscape(ev.Summary))
		w(fmt.Sprintf("TRIGGER:-PT%dM", ev.Reminder))
		w("END:VALARM")
	}
	w("END:VEVENT")
	w("END:VCALENDAR")
	return b.String()
}

func quoteParam(s string) string {
	// RFC 6868 caret encoding; other control characters are dropped.
	s = strings.NewReplacer("^", "^^", `"`, "^'", "\r\n", "^n", "\r", "^n", "\n", "^n").Replace(s)
	return `"` + stripCtl(s) + `"`
}

// TaskICal renders t as a VCALENDAR with one VTODO.
func TaskICal(t Task) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(fold(s)) }
	w("BEGIN:VCALENDAR")
	w("VERSION:2.0")
	w("PRODID:-//TuxMail//Cloud PIM//EN")
	w("BEGIN:VTODO")
	w("UID:" + icalEscape(t.ID))
	stamp := t.LastChanged
	if stamp.IsZero() {
		stamp = time.Unix(0, 0)
	}
	w("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z"))
	w("SUMMARY:" + icalEscape(t.Title))
	if t.Notes != "" {
		w("DESCRIPTION:" + icalEscape(t.Notes))
	}
	if !t.Due.IsZero() {
		w("DUE" + icalTime(t.Due, false))
	}
	switch t.Importance {
	case "high":
		w("PRIORITY:1")
	case "low":
		w("PRIORITY:9")
	}
	if t.Done {
		w("STATUS:COMPLETED")
		if !t.Completed.IsZero() {
			w("COMPLETED" + icalTime(t.Completed, false))
		}
	} else {
		w("STATUS:NEEDS-ACTION")
	}
	w("END:VTODO")
	w("END:VCALENDAR")
	return b.String()
}

func vcEscape(s string) string {
	return stripCtl(strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\r\n", `\n`, "\r", `\n`, "\n", `\n`).Replace(s))
}

// ContactVCard renders c as vCard 4.0.
func ContactVCard(c Contact) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(fold(s)) }
	w("BEGIN:VCARD")
	w("VERSION:4.0")
	w("UID:" + vcEscape(c.ID))
	fn := c.DisplayName
	if fn == "" {
		fn = strings.TrimSpace(c.GivenName + " " + c.FamilyName)
	}
	if fn == "" && len(c.Emails) > 0 {
		fn = c.Emails[0]
	}
	w("FN:" + vcEscape(fn))
	w("N:" + vcEscape(c.FamilyName) + ";" + vcEscape(c.GivenName) + ";;;")
	for _, e := range c.Emails {
		w("EMAIL:" + vcEscape(e))
	}
	for _, p := range c.Phones {
		w("TEL:" + vcEscape(p))
	}
	if c.Company != "" {
		w("ORG:" + vcEscape(c.Company))
	}
	if c.JobTitle != "" {
		w("TITLE:" + vcEscape(c.JobTitle))
	}
	if c.Birthday != "" {
		w("BDAY:" + stripCtl(strings.ReplaceAll(c.Birthday, "-", "")))
	}
	if c.Notes != "" {
		w("NOTE:" + vcEscape(c.Notes))
	}
	w("END:VCARD")
	return b.String()
}
