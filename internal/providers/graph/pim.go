// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
)

var (
	_ pimapi.Calendars = (*Client)(nil)
	_ pimapi.Contacts  = (*Client)(nil)
	_ pimapi.Tasks     = (*Client)(nil)
	_ pimapi.FreeBusy  = (*Client)(nil)
)

// utcPrefer makes Graph return all event times in UTC.
var utcPrefer = http.Header{"Prefer": {`IdType="ImmutableId"`, `outlook.timezone="UTC"`}}

type gDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

func (d gDateTime) time() time.Time {
	for _, l := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(l, d.DateTime); err == nil {
			if d.TimeZone != "" && d.TimeZone != "UTC" {
				if loc, err := time.LoadLocation(d.TimeZone); err == nil {
					t, _ = time.ParseInLocation(l, d.DateTime, loc)
				}
			}
			return t.UTC()
		}
	}
	return time.Time{}
}

func toGDateTime(t time.Time, allDay bool) gDateTime {
	if allDay {
		return gDateTime{DateTime: t.Format("2006-01-02") + "T00:00:00", TimeZone: "UTC"}
	}
	return gDateTime{DateTime: t.UTC().Format("2006-01-02T15:04:05"), TimeZone: "UTC"}
}

type gCalendar struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	HexColor          string `json:"hexColor"`
	CanEdit           bool   `json:"canEdit"`
	IsDefaultCalendar bool   `json:"isDefaultCalendar"`
	Owner             *struct {
		Address string `json:"address"`
	} `json:"owner"`
}

type gAttendee struct {
	EmailAddress struct {
		Name    string `json:"name,omitempty"`
		Address string `json:"address"`
	} `json:"emailAddress"`
	Type   string `json:"type,omitempty"` // required | optional | resource
	Status *struct {
		Response string `json:"response"`
	} `json:"status,omitempty"`
}

type gRecurrence struct {
	Pattern struct {
		Type           string   `json:"type"`
		Interval       int      `json:"interval"`
		DaysOfWeek     []string `json:"daysOfWeek,omitempty"`
		DayOfMonth     int      `json:"dayOfMonth,omitempty"`
		Month          int      `json:"month,omitempty"`
		Index          string   `json:"index,omitempty"`
		FirstDayOfWeek string   `json:"firstDayOfWeek,omitempty"`
	} `json:"pattern"`
	Range struct {
		Type                string `json:"type"`
		StartDate           string `json:"startDate"`
		EndDate             string `json:"endDate,omitempty"`
		NumberOfOccurrences int    `json:"numberOfOccurrences,omitempty"`
	} `json:"range"`
}

type gEvent struct {
	ID       string `json:"id,omitempty"`
	ICalUID  string `json:"iCalUId,omitempty"`
	Subject  string `json:"subject"`
	BodyPrev string `json:"bodyPreview,omitempty"`
	Body     *struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body,omitempty"`
	Start    gDateTime `json:"start"`
	End      gDateTime `json:"end"`
	IsAllDay bool      `json:"isAllDay"`
	Location *struct {
		DisplayName string `json:"displayName"`
	} `json:"location,omitempty"`
	Recurrence    *gRecurrence `json:"recurrence,omitempty"`
	SeriesMaster  string       `json:"seriesMasterId,omitempty"`
	Type          string       `json:"type,omitempty"`
	Organizer     *gAttendee   `json:"organizer,omitempty"`
	Attendees     []gAttendee  `json:"attendees,omitempty"`
	IsOnline      bool         `json:"isOnlineMeeting,omitempty"`
	OnlineMeeting *struct {
		JoinURL string `json:"joinUrl"`
	} `json:"onlineMeeting,omitempty"`
	OnlineProvider  string                     `json:"onlineMeetingProvider,omitempty"`
	ShowAs          string                     `json:"showAs,omitempty"`
	IsCancelled     bool                       `json:"isCancelled,omitempty"`
	Categories      []string                   `json:"categories,omitempty"`
	ChangeKey       string                     `json:"changeKey,omitempty"`
	LastModified    string                     `json:"lastModifiedDateTime,omitempty"`
	ReminderMinutes int                        `json:"reminderMinutesBeforeStart,omitempty"`
	ResponseStatus  *struct{ Response string } `json:"responseStatus,omitempty"`
	IsReminderOn    bool                       `json:"isReminderOn,omitempty"`
}

var graphResponse = map[string]string{"accepted": "accepted", "tentativelyAccepted": "tentative", "declined": "declined",
	"notResponded": "needs-action", "none": "needs-action", "organizer": "accepted"}

func (c *Client) calPath(calendarID string) string {
	if calendarID == "" {
		return c.user() + "/calendar"
	}
	return c.user() + "/calendars/" + url.PathEscape(calendarID)
}

// ListCalendars implements pimapi.Calendars.
func (c *Client) ListCalendars(ctx context.Context) ([]pimapi.Calendar, error) {
	cs, err := list[gCalendar](ctx, c, c.user()+"/calendars?$top=100")
	if err != nil {
		return nil, err
	}
	out := make([]pimapi.Calendar, 0, len(cs))
	for _, g := range cs {
		cal := pimapi.Calendar{ID: g.ID, Name: g.Name, Color: g.HexColor, ReadOnly: !g.CanEdit, IsDefault: g.IsDefaultCalendar}
		if g.Owner != nil && c.cfg.Mailbox == "" && !strings.EqualFold(g.Owner.Address, c.cfg.Self) {
			cal.Owner = g.Owner.Address
		}
		out = append(out, cal)
	}
	return out, nil
}

func (c *Client) toEvent(calID string, g gEvent) pimapi.Event {
	ev := pimapi.Event{ID: g.ID, CalendarID: calID, UID: g.ICalUID, Summary: g.Subject, Start: g.Start.time(), End: g.End.time(),
		AllDay: g.IsAllDay, SeriesID: g.SeriesMaster, IsOnline: g.IsOnline, Categories: g.Categories, ETag: g.ChangeKey,
		Status: "confirmed", Reminder: g.ReminderMinutes}
	if g.Type == "occurrence" || g.Type == "exception" {
		ev.UID = "" // occurrences share the master's iCalUId
	}
	if g.Body != nil {
		ev.Body = g.Body.Content
	} else {
		ev.Body = g.BodyPrev
	}
	if g.Location != nil {
		ev.Location = g.Location.DisplayName
	}
	if g.OnlineMeeting != nil {
		ev.OnlineURL = g.OnlineMeeting.JoinURL
	}
	if g.IsCancelled {
		ev.Status = "cancelled"
	} else if g.ShowAs == "tentative" {
		ev.Status = "tentative"
	}
	if g.ResponseStatus != nil {
		ev.Response = graphResponse[g.ResponseStatus.Response]
	}
	if g.Organizer != nil {
		ev.Organizer = &pimapi.Attendee{Name: g.Organizer.EmailAddress.Name, Email: g.Organizer.EmailAddress.Address}
	}
	for _, a := range g.Attendees {
		pa := pimapi.Attendee{Name: a.EmailAddress.Name, Email: a.EmailAddress.Address, Role: a.Type, Status: "needs-action"}
		if a.Status != nil {
			pa.Status = graphResponse[a.Status.Response]
		}
		ev.Attendees = append(ev.Attendees, pa)
	}
	if g.Recurrence != nil {
		ev.RRule = RRuleFromGraph(*g.Recurrence)
	}
	if t, err := time.Parse(time.RFC3339Nano, g.LastModified); err == nil {
		ev.LastModified = t
	}
	return ev
}

func fromEvent(ev pimapi.Event) gEvent {
	g := gEvent{Subject: ev.Summary, Start: toGDateTime(ev.Start, ev.AllDay), End: toGDateTime(ev.End, ev.AllDay),
		IsAllDay: ev.AllDay, Categories: ev.Categories}
	if ev.Body != "" {
		g.Body = &struct {
			ContentType string `json:"contentType"`
			Content     string `json:"content"`
		}{"text", ev.Body}
	}
	if ev.Location != "" {
		g.Location = &struct {
			DisplayName string `json:"displayName"`
		}{ev.Location}
	}
	for _, a := range ev.Attendees {
		ga := gAttendee{Type: a.Role}
		if ga.Type == "" {
			ga.Type = "required"
		}
		ga.EmailAddress.Name, ga.EmailAddress.Address = a.Name, a.Email
		g.Attendees = append(g.Attendees, ga)
	}
	if ev.IsOnline {
		g.IsOnline = true
		g.OnlineProvider = "teamsForBusiness"
	}
	if ev.RRule != "" {
		if r, ok := RRuleToGraph(ev.RRule, ev.Start); ok {
			g.Recurrence = &r
		}
	}
	if ev.Reminder > 0 {
		g.ReminderMinutes, g.IsReminderOn = ev.Reminder, true
	}
	return g
}

const eventSelect = "id,iCalUId,subject,bodyPreview,start,end,isAllDay,location,seriesMasterId,type,organizer,attendees," +
	"isOnlineMeeting,onlineMeeting,showAs,isCancelled,categories,changeKey,lastModifiedDateTime,reminderMinutesBeforeStart,responseStatus,recurrence"

// ListEvents implements pimapi.Calendars with calendarView (series are
// expanded by the server).
func (c *Client) ListEvents(ctx context.Context, calendarID string, from, to time.Time) ([]pimapi.Event, error) {
	path := fmt.Sprintf("%s/calendarView?startDateTime=%s&endDateTime=%s&$top=200&$select=%s", c.calPath(calendarID),
		url.QueryEscape(from.UTC().Format(time.RFC3339)), url.QueryEscape(to.UTC().Format(time.RFC3339)), eventSelect)
	var out []pimapi.Event
	for path != "" {
		var p page[gEvent]
		if err := c.rest.Do(ctx, rest.Request{Method: http.MethodGet, Path: path, Header: utcPrefer}, &p); err != nil {
			return nil, err
		}
		for _, g := range p.Value {
			out = append(out, c.toEvent(calendarID, g))
		}
		path = p.NextLink
	}
	return out, nil
}

// Instances returns the occurrences of a series in [from, to).
func (c *Client) Instances(ctx context.Context, eventID string, from, to time.Time) ([]pimapi.Event, error) {
	path := fmt.Sprintf("%s/events/%s/instances?startDateTime=%s&endDateTime=%s&$select=%s", c.user(), url.PathEscape(eventID),
		url.QueryEscape(from.UTC().Format(time.RFC3339)), url.QueryEscape(to.UTC().Format(time.RFC3339)), eventSelect)
	var p page[gEvent]
	if err := c.rest.Do(ctx, rest.Request{Method: http.MethodGet, Path: path, Header: utcPrefer}, &p); err != nil {
		return nil, err
	}
	out := make([]pimapi.Event, 0, len(p.Value))
	for _, g := range p.Value {
		out = append(out, c.toEvent("", g))
	}
	return out, nil
}

// CreateEvent implements pimapi.Calendars (IsOnline creates a Teams meeting).
func (c *Client) CreateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	var g gEvent
	err := c.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: c.calPath(calendarID) + "/events", JSON: fromEvent(ev), Header: utcPrefer}, &g)
	return c.toEvent(calendarID, g), err
}

// UpdateEvent implements pimapi.Calendars.
func (c *Client) UpdateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	var g gEvent
	err := c.rest.Do(ctx, rest.Request{Method: http.MethodPatch, Path: c.user() + "/events/" + url.PathEscape(ev.ID),
		JSON: fromEvent(ev), Header: utcPrefer}, &g)
	return c.toEvent(calendarID, g), err
}

// DeleteEvent implements pimapi.Calendars.
func (c *Client) DeleteEvent(ctx context.Context, _ string, eventID string) error {
	err := c.do(ctx, http.MethodDelete, c.user()+"/events/"+url.PathEscape(eventID), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Respond implements pimapi.Calendars (accept / tentativelyAccept / decline).
func (c *Client) Respond(ctx context.Context, _ string, eventID, response, comment string, notify bool) error {
	action := map[string]string{pimapi.Accept: "accept", pimapi.Tentative: "tentativelyAccept", pimapi.Decline: "decline"}[response]
	if action == "" {
		return fmt.Errorf("graph: unknown response %q", response)
	}
	return c.do(ctx, http.MethodPost, c.user()+"/events/"+url.PathEscape(eventID)+"/"+action,
		map[string]any{"comment": comment, "sendResponse": notify}, nil)
}

// FreeBusy implements pimapi.FreeBusy with calendar/getSchedule.
func (c *Client) FreeBusy(ctx context.Context, emails []string, from, to time.Time) (map[string][]pimapi.BusySlot, error) {
	in := map[string]any{"schedules": emails, "startTime": toGDateTime(from, false), "endTime": toGDateTime(to, false),
		"availabilityViewInterval": 15}
	var resp struct {
		Value []struct {
			ScheduleID    string `json:"scheduleId"`
			ScheduleItems []struct {
				Status string    `json:"status"`
				Start  gDateTime `json:"start"`
				End    gDateTime `json:"end"`
			} `json:"scheduleItems"`
		} `json:"value"`
	}
	if err := c.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: c.user() + "/calendar/getSchedule", JSON: in, Header: utcPrefer}, &resp); err != nil {
		return nil, err
	}
	out := map[string][]pimapi.BusySlot{}
	for _, v := range resp.Value {
		slots := []pimapi.BusySlot{}
		for _, it := range v.ScheduleItems {
			if it.Status == "free" {
				continue
			}
			slots = append(slots, pimapi.BusySlot{Start: it.Start.time(), End: it.End.time(), Status: it.Status})
		}
		out[strings.ToLower(v.ScheduleID)] = slots
	}
	return out, nil
}

// ==== recurrence ↔ RRULE ======================================================

var weekdays = map[string]string{"sunday": "SU", "monday": "MO", "tuesday": "TU", "wednesday": "WE", "thursday": "TH", "friday": "FR", "saturday": "SA"}
var indexes = map[string]string{"first": "1", "second": "2", "third": "3", "fourth": "4", "last": "-1"}

// RRuleFromGraph converts a Graph recurrence to an RRULE value.
func RRuleFromGraph(r gRecurrence) string {
	p := r.Pattern
	var parts []string
	days := func() string {
		var ds []string
		for _, d := range p.DaysOfWeek {
			ds = append(ds, weekdays[strings.ToLower(d)])
		}
		return strings.Join(ds, ",")
	}
	switch p.Type {
	case "daily":
		parts = append(parts, "FREQ=DAILY")
	case "weekly":
		parts = append(parts, "FREQ=WEEKLY")
		if d := days(); d != "" {
			parts = append(parts, "BYDAY="+d)
		}
	case "absoluteMonthly":
		parts = append(parts, "FREQ=MONTHLY", "BYMONTHDAY="+strconv.Itoa(p.DayOfMonth))
	case "relativeMonthly":
		parts = append(parts, "FREQ=MONTHLY", "BYDAY="+days(), "BYSETPOS="+indexes[p.Index])
	case "absoluteYearly":
		parts = append(parts, "FREQ=YEARLY", "BYMONTH="+strconv.Itoa(p.Month), "BYMONTHDAY="+strconv.Itoa(p.DayOfMonth))
	case "relativeYearly":
		parts = append(parts, "FREQ=YEARLY", "BYMONTH="+strconv.Itoa(p.Month), "BYDAY="+days(), "BYSETPOS="+indexes[p.Index])
	default:
		return ""
	}
	if p.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(p.Interval))
	}
	switch r.Range.Type {
	case "endDate":
		if t, err := time.Parse("2006-01-02", r.Range.EndDate); err == nil {
			parts = append(parts, "UNTIL="+t.Format("20060102")+"T235959Z")
		}
	case "numbered":
		parts = append(parts, "COUNT="+strconv.Itoa(r.Range.NumberOfOccurrences))
	}
	return strings.Join(parts, ";")
}

// RRuleToGraph converts the common RRULE forms to a Graph recurrence.
func RRuleToGraph(rrule string, start time.Time) (gRecurrence, bool) {
	var r gRecurrence
	kv := map[string]string{}
	for _, part := range strings.Split(rrule, ";") {
		k, v, _ := strings.Cut(part, "=")
		kv[strings.ToUpper(k)] = v
	}
	rev := map[string]string{}
	for k, v := range weekdays {
		rev[v] = k
	}
	var days []string
	for _, d := range strings.Split(kv["BYDAY"], ",") {
		if d == "" {
			continue
		}
		d = strings.TrimLeft(d, "+-0123456789")
		if n, ok := rev[d]; ok {
			days = append(days, n)
		}
	}
	r.Pattern.Interval = 1
	if n, err := strconv.Atoi(kv["INTERVAL"]); err == nil && n > 0 {
		r.Pattern.Interval = n
	}
	revIdx := map[string]string{}
	for k, v := range indexes {
		revIdx[v] = k
	}
	switch kv["FREQ"] {
	case "DAILY":
		r.Pattern.Type = "daily"
	case "WEEKLY":
		r.Pattern.Type = "weekly"
		if len(days) == 0 {
			days = []string{strings.ToLower(start.Weekday().String())}
		}
		r.Pattern.DaysOfWeek = days
	case "MONTHLY":
		if pos := kv["BYSETPOS"]; pos != "" && len(days) > 0 {
			r.Pattern.Type, r.Pattern.DaysOfWeek, r.Pattern.Index = "relativeMonthly", days, revIdx[pos]
		} else {
			r.Pattern.Type = "absoluteMonthly"
			r.Pattern.DayOfMonth = start.Day()
			if n, err := strconv.Atoi(kv["BYMONTHDAY"]); err == nil {
				r.Pattern.DayOfMonth = n
			}
		}
	case "YEARLY":
		r.Pattern.Type, r.Pattern.Month, r.Pattern.DayOfMonth = "absoluteYearly", int(start.Month()), start.Day()
	default:
		return r, false
	}
	r.Range.StartDate = start.Format("2006-01-02")
	r.Range.Type = "noEnd"
	if u := kv["UNTIL"]; len(u) >= 8 {
		if t, err := time.Parse("20060102", u[:8]); err == nil {
			r.Range.Type, r.Range.EndDate = "endDate", t.Format("2006-01-02")
		}
	}
	if n, err := strconv.Atoi(kv["COUNT"]); err == nil {
		r.Range.Type, r.Range.NumberOfOccurrences = "numbered", n
	}
	return r, true
}

// ==== contacts ================================================================

type gContact struct {
	ID             string   `json:"id,omitempty"`
	DisplayName    string   `json:"displayName,omitempty"`
	GivenName      string   `json:"givenName,omitempty"`
	Surname        string   `json:"surname,omitempty"`
	EmailAddresses []gMail  `json:"emailAddresses,omitempty"`
	BusinessPhones []string `json:"businessPhones,omitempty"`
	HomePhones     []string `json:"homePhones,omitempty"`
	MobilePhone    string   `json:"mobilePhone,omitempty"`
	CompanyName    string   `json:"companyName,omitempty"`
	JobTitle       string   `json:"jobTitle,omitempty"`
	PersonalNotes  string   `json:"personalNotes,omitempty"`
	Birthday       string   `json:"birthday,omitempty"`
	ChangeKey      string   `json:"changeKey,omitempty"`
	LastModified   string   `json:"lastModifiedDateTime,omitempty"`
}

type gMail struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

func toContact(book string, g gContact) pimapi.Contact {
	c := pimapi.Contact{ID: g.ID, FolderID: book, DisplayName: g.DisplayName, GivenName: g.GivenName, FamilyName: g.Surname,
		Company: g.CompanyName, JobTitle: g.JobTitle, Notes: g.PersonalNotes, ETag: g.ChangeKey}
	for _, e := range g.EmailAddresses {
		c.Emails = append(c.Emails, e.Address)
	}
	c.Phones = append(append(c.Phones, g.BusinessPhones...), g.HomePhones...)
	if g.MobilePhone != "" {
		c.Phones = append(c.Phones, g.MobilePhone)
	}
	if len(g.Birthday) >= 10 {
		c.Birthday = g.Birthday[:10]
	}
	if t, err := time.Parse(time.RFC3339Nano, g.LastModified); err == nil {
		c.LastModified = t
	}
	return c
}

func fromContact(c pimapi.Contact) gContact {
	g := gContact{DisplayName: c.DisplayName, GivenName: c.GivenName, Surname: c.FamilyName, CompanyName: c.Company,
		JobTitle: c.JobTitle, PersonalNotes: c.Notes}
	for _, e := range c.Emails {
		g.EmailAddresses = append(g.EmailAddresses, gMail{Address: e})
	}
	g.BusinessPhones = c.Phones
	if c.Birthday != "" {
		g.Birthday = c.Birthday + "T00:00:00Z"
	}
	return g
}

func (c *Client) bookPath(book string) string {
	if book == "" || book == "default" {
		return c.user() + "/contacts"
	}
	return c.user() + "/contactFolders/" + url.PathEscape(book) + "/contacts"
}

// ListAddressBooks implements pimapi.Contacts (the default folder has id "default").
func (c *Client) ListAddressBooks(ctx context.Context) ([]pimapi.AddressBook, error) {
	type gcf struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	fs, err := list[gcf](ctx, c, c.user()+"/contactFolders?$top=100")
	if err != nil {
		return nil, err
	}
	out := []pimapi.AddressBook{{ID: "default", Name: "Contacts", Owner: c.cfg.Mailbox}}
	for _, f := range fs {
		out = append(out, pimapi.AddressBook{ID: f.ID, Name: f.DisplayName, Owner: c.cfg.Mailbox})
	}
	return out, nil
}

// ListContacts implements pimapi.Contacts.
func (c *Client) ListContacts(ctx context.Context, book string) ([]pimapi.Contact, error) {
	gs, err := list[gContact](ctx, c, c.bookPath(book)+"?$top=200")
	if err != nil {
		return nil, err
	}
	out := make([]pimapi.Contact, 0, len(gs))
	for _, g := range gs {
		out = append(out, toContact(book, g))
	}
	return out, nil
}

// CreateContact implements pimapi.Contacts.
func (c *Client) CreateContact(ctx context.Context, book string, ct pimapi.Contact) (pimapi.Contact, error) {
	var g gContact
	err := c.do(ctx, http.MethodPost, c.bookPath(book), fromContact(ct), &g)
	return toContact(book, g), err
}

// UpdateContact implements pimapi.Contacts.
func (c *Client) UpdateContact(ctx context.Context, book string, ct pimapi.Contact) (pimapi.Contact, error) {
	var g gContact
	err := c.do(ctx, http.MethodPatch, c.user()+"/contacts/"+url.PathEscape(ct.ID), fromContact(ct), &g)
	return toContact(book, g), err
}

// DeleteContact implements pimapi.Contacts.
func (c *Client) DeleteContact(ctx context.Context, _ string, id string) error {
	err := c.do(ctx, http.MethodDelete, c.user()+"/contacts/"+url.PathEscape(id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ==== Microsoft To Do =========================================================

type gTask struct {
	ID         string `json:"id,omitempty"`
	Title      string `json:"title"`
	Status     string `json:"status,omitempty"` // notStarted | completed ...
	Importance string `json:"importance,omitempty"`
	Body       *struct {
		Content     string `json:"content"`
		ContentType string `json:"contentType"`
	} `json:"body,omitempty"`
	DueDateTime       *gDateTime `json:"dueDateTime,omitempty"`
	CompletedDateTime *gDateTime `json:"completedDateTime,omitempty"`
	LastModified      string     `json:"lastModifiedDateTime,omitempty"`
}

func toTask(list string, g gTask) pimapi.Task {
	t := pimapi.Task{ID: g.ID, ListID: list, Title: g.Title, Done: g.Status == "completed", Importance: g.Importance}
	if g.Body != nil {
		t.Notes = g.Body.Content
	}
	if g.DueDateTime != nil {
		t.Due = g.DueDateTime.time()
	}
	if g.CompletedDateTime != nil {
		t.Completed = g.CompletedDateTime.time()
	}
	if ts, err := time.Parse(time.RFC3339Nano, g.LastModified); err == nil {
		t.LastChanged = ts
	}
	return t
}

func fromTask(t pimapi.Task) gTask {
	g := gTask{Title: t.Title, Status: "notStarted", Importance: t.Importance}
	if t.Done {
		g.Status = "completed"
	}
	if t.Notes != "" {
		g.Body = &struct {
			Content     string `json:"content"`
			ContentType string `json:"contentType"`
		}{t.Notes, "text"}
	}
	if !t.Due.IsZero() {
		d := toGDateTime(t.Due, false)
		g.DueDateTime = &d
	}
	return g
}

// ListTaskLists implements pimapi.Tasks.
func (c *Client) ListTaskLists(ctx context.Context) ([]pimapi.Calendar, error) {
	type gl struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	ls, err := list[gl](ctx, c, c.user()+"/todo/lists")
	if err != nil {
		return nil, err
	}
	out := make([]pimapi.Calendar, 0, len(ls))
	for _, l := range ls {
		out = append(out, pimapi.Calendar{ID: l.ID, Name: l.DisplayName})
	}
	return out, nil
}

// ListTasks implements pimapi.Tasks.
func (c *Client) ListTasks(ctx context.Context, listID string) ([]pimapi.Task, error) {
	gs, err := list[gTask](ctx, c, c.user()+"/todo/lists/"+url.PathEscape(listID)+"/tasks?$top=100")
	if err != nil {
		return nil, err
	}
	out := make([]pimapi.Task, 0, len(gs))
	for _, g := range gs {
		out = append(out, toTask(listID, g))
	}
	return out, nil
}

// CreateTask implements pimapi.Tasks.
func (c *Client) CreateTask(ctx context.Context, listID string, t pimapi.Task) (pimapi.Task, error) {
	var g gTask
	err := c.do(ctx, http.MethodPost, c.user()+"/todo/lists/"+url.PathEscape(listID)+"/tasks", fromTask(t), &g)
	return toTask(listID, g), err
}

// UpdateTask implements pimapi.Tasks.
func (c *Client) UpdateTask(ctx context.Context, listID string, t pimapi.Task) (pimapi.Task, error) {
	var g gTask
	err := c.do(ctx, http.MethodPatch, c.user()+"/todo/lists/"+url.PathEscape(listID)+"/tasks/"+url.PathEscape(t.ID), fromTask(t), &g)
	return toTask(listID, g), err
}

// DeleteTask implements pimapi.Tasks.
func (c *Client) DeleteTask(ctx context.Context, listID, id string) error {
	err := c.do(ctx, http.MethodDelete, c.user()+"/todo/lists/"+url.PathEscape(listID)+"/tasks/"+url.PathEscape(id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}
