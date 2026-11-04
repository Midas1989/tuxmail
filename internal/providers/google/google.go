// SPDX-License-Identifier: AGPL-3.0-or-later

// Package google implements Google Calendar (API v3: calendars, events,
// expanded recurrences, invitation responses, Meet links, free/busy) and
// Google Contacts (People API) on the pimapi interfaces, with plain
// net/http + JSON.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
)

// Default API roots.
const (
	CalendarBaseURL = "https://www.googleapis.com/calendar/v3"
	PeopleBaseURL   = "https://people.googleapis.com/v1"
)

// Config configures a Client.
type Config struct {
	CalendarURL, PeopleURL string
	Tokens                 rest.TokenSource
	HTTPClient             *http.Client
	// Self is the signed-in address (own attendee entry for responses).
	Self  string
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client accesses Google Calendar and People.
type Client struct {
	cfg      Config
	cal, ppl *rest.Client
}

var (
	_ pimapi.Calendars = (*Client)(nil)
	_ pimapi.Contacts  = (*Client)(nil)
	_ pimapi.FreeBusy  = (*Client)(nil)
)

// New creates a client.
func New(cfg Config) (*Client, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("google: a token source is required")
	}
	if cfg.CalendarURL == "" {
		cfg.CalendarURL = CalendarBaseURL
	}
	if cfg.PeopleURL == "" {
		cfg.PeopleURL = PeopleBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: time.Minute}
	}
	mk := func(base string) *rest.Client {
		return &rest.Client{BaseURL: strings.TrimRight(base, "/"), HTTP: cfg.HTTPClient, Tokens: cfg.Tokens, Sleep: cfg.Sleep,
			DecodeError: decodeError}
	}
	return &Client{cfg: cfg, cal: mk(cfg.CalendarURL), ppl: mk(cfg.PeopleURL)}, nil
}

func decodeError(status int, body []byte) error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		return &rest.StatusError{Status: status, Code: env.Error.Status, Message: env.Error.Message}
	}
	return &rest.StatusError{Status: status, Message: strings.TrimSpace(string(body))}
}

// ==== calendar ================================================================

type gTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

func (t gTime) time() (time.Time, bool) {
	if t.Date != "" {
		d, err := time.Parse("2006-01-02", t.Date)
		return d, err == nil
	}
	d, err := time.Parse(time.RFC3339, t.DateTime)
	return d.UTC(), err == nil
}

func toGTime(t time.Time, allDay bool) gTime {
	if allDay {
		return gTime{Date: t.Format("2006-01-02")}
	}
	return gTime{DateTime: t.UTC().Format(time.RFC3339)}
}

type gAttendee struct {
	Email          string `json:"email"`
	DisplayName    string `json:"displayName,omitempty"`
	Optional       bool   `json:"optional,omitempty"`
	Resource       bool   `json:"resource,omitempty"`
	ResponseStatus string `json:"responseStatus,omitempty"` // needsAction | accepted | tentative | declined
	Self           bool   `json:"self,omitempty"`
	Organizer      bool   `json:"organizer,omitempty"`
}

type gEvent struct {
	ID               string      `json:"id,omitempty"`
	ICalUID          string      `json:"iCalUID,omitempty"`
	Status           string      `json:"status,omitempty"`
	Summary          string      `json:"summary,omitempty"`
	Description      string      `json:"description,omitempty"`
	Location         string      `json:"location,omitempty"`
	Start            gTime       `json:"start"`
	End              gTime       `json:"end"`
	Recurrence       []string    `json:"recurrence,omitempty"`
	RecurringEventID string      `json:"recurringEventId,omitempty"`
	Attendees        []gAttendee `json:"attendees,omitempty"`
	Organizer        *gAttendee  `json:"organizer,omitempty"`
	HangoutLink      string      `json:"hangoutLink,omitempty"`
	ETag             string      `json:"etag,omitempty"`
	Updated          string      `json:"updated,omitempty"`
	ConferenceData   any         `json:"conferenceData,omitempty"`
}

var respStatus = map[string]string{"needsAction": "needs-action", "accepted": "accepted", "tentative": "tentative", "declined": "declined"}

func (c *Client) toEvent(cal string, g gEvent) pimapi.Event {
	ev := pimapi.Event{ID: g.ID, CalendarID: cal, UID: g.ICalUID, Summary: g.Summary, Body: g.Description, Location: g.Location,
		SeriesID: g.RecurringEventID, Status: g.Status, ETag: g.ETag, OnlineURL: g.HangoutLink, IsOnline: g.HangoutLink != ""}
	if g.RecurringEventID != "" {
		ev.UID = "" // all instances share the iCalUID of the series
	}
	var ok bool
	if ev.Start, ok = g.Start.time(); ok && g.Start.Date != "" {
		ev.AllDay = true
	}
	ev.End, _ = g.End.time()
	for _, r := range g.Recurrence {
		if v, ok := strings.CutPrefix(r, "RRULE:"); ok {
			ev.RRule = v
		}
	}
	if g.Organizer != nil {
		ev.Organizer = &pimapi.Attendee{Email: g.Organizer.Email, Name: g.Organizer.DisplayName}
	}
	for _, a := range g.Attendees {
		role := "required"
		if a.Optional {
			role = "optional"
		}
		if a.Resource {
			role = "resource"
		}
		ev.Attendees = append(ev.Attendees, pimapi.Attendee{Email: a.Email, Name: a.DisplayName, Role: role, Status: respStatus[a.ResponseStatus]})
		if a.Self || (c.cfg.Self != "" && strings.EqualFold(a.Email, c.cfg.Self)) {
			ev.Response = respStatus[a.ResponseStatus]
		}
	}
	if t, err := time.Parse(time.RFC3339, g.Updated); err == nil {
		ev.LastModified = t
	}
	return ev
}

func fromEvent(ev pimapi.Event) gEvent {
	g := gEvent{Summary: ev.Summary, Description: ev.Body, Location: ev.Location, Start: toGTime(ev.Start, ev.AllDay),
		End: toGTime(ev.End, ev.AllDay)}
	if ev.RRule != "" {
		g.Recurrence = []string{"RRULE:" + ev.RRule}
	}
	for _, a := range ev.Attendees {
		g.Attendees = append(g.Attendees, gAttendee{Email: a.Email, DisplayName: a.Name, Optional: a.Role == "optional", Resource: a.Role == "resource"})
	}
	if ev.IsOnline && ev.OnlineURL == "" {
		g.ConferenceData = map[string]any{"createRequest": map[string]any{"requestId": fmt.Sprintf("tuxmail-%d", time.Now().UnixNano()),
			"conferenceSolutionKey": map[string]string{"type": "hangoutsMeet"}}}
	}
	return g
}

func calPath(id string) string {
	if id == "" {
		id = "primary"
	}
	return "/calendars/" + url.PathEscape(id)
}

// ListCalendars implements pimapi.Calendars.
func (c *Client) ListCalendars(ctx context.Context) ([]pimapi.Calendar, error) {
	var out []pimapi.Calendar
	q := url.Values{"maxResults": {"250"}}
	for {
		var resp struct {
			Items []struct {
				ID              string `json:"id"`
				Summary         string `json:"summary"`
				SummaryOverride string `json:"summaryOverride"`
				BackgroundColor string `json:"backgroundColor"`
				AccessRole      string `json:"accessRole"`
				Primary         bool   `json:"primary"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.cal.Get(ctx, "/users/me/calendarList?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			name := it.Summary
			if it.SummaryOverride != "" {
				name = it.SummaryOverride
			}
			cal := pimapi.Calendar{ID: it.ID, Name: name, Color: it.BackgroundColor, IsDefault: it.Primary,
				ReadOnly: it.AccessRole != "owner" && it.AccessRole != "writer"}
			if !it.Primary && strings.Contains(it.ID, "@") && !strings.HasSuffix(it.ID, "calendar.google.com") {
				cal.Owner = it.ID
			}
			out = append(out, cal)
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", resp.NextPageToken)
	}
}

// ListEvents implements pimapi.Calendars (singleEvents=true expands series).
func (c *Client) ListEvents(ctx context.Context, calendarID string, from, to time.Time) ([]pimapi.Event, error) {
	q := url.Values{"timeMin": {from.UTC().Format(time.RFC3339)}, "timeMax": {to.UTC().Format(time.RFC3339)},
		"singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {"2500"}}
	var out []pimapi.Event
	for {
		var resp struct {
			Items         []gEvent `json:"items"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := c.cal.Get(ctx, calPath(calendarID)+"/events?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, g := range resp.Items {
			if g.Status == "cancelled" {
				continue
			}
			out = append(out, c.toEvent(calendarID, g))
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", resp.NextPageToken)
	}
}

// Instances returns the occurrences of a recurring event.
func (c *Client) Instances(ctx context.Context, calendarID, eventID string, from, to time.Time) ([]pimapi.Event, error) {
	q := url.Values{"timeMin": {from.UTC().Format(time.RFC3339)}, "timeMax": {to.UTC().Format(time.RFC3339)}}
	var resp struct {
		Items []gEvent `json:"items"`
	}
	if err := c.cal.Get(ctx, calPath(calendarID)+"/events/"+url.PathEscape(eventID)+"/instances?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]pimapi.Event, 0, len(resp.Items))
	for _, g := range resp.Items {
		out = append(out, c.toEvent(calendarID, g))
	}
	return out, nil
}

// CreateEvent implements pimapi.Calendars (IsOnline adds a Meet link).
func (c *Client) CreateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	var g gEvent
	err := c.cal.Do(ctx, rest.Request{Method: http.MethodPost, Path: calPath(calendarID) + "/events?conferenceDataVersion=1&sendUpdates=all",
		JSON: fromEvent(ev)}, &g)
	return c.toEvent(calendarID, g), err
}

// UpdateEvent implements pimapi.Calendars.
func (c *Client) UpdateEvent(ctx context.Context, calendarID string, ev pimapi.Event) (pimapi.Event, error) {
	var g gEvent
	err := c.cal.Do(ctx, rest.Request{Method: http.MethodPatch,
		Path: calPath(calendarID) + "/events/" + url.PathEscape(ev.ID) + "?conferenceDataVersion=1&sendUpdates=all", JSON: fromEvent(ev)}, &g)
	return c.toEvent(calendarID, g), err
}

// DeleteEvent implements pimapi.Calendars.
func (c *Client) DeleteEvent(ctx context.Context, calendarID, eventID string) error {
	err := c.cal.Do(ctx, rest.Request{Method: http.MethodDelete, Path: calPath(calendarID) + "/events/" + url.PathEscape(eventID) + "?sendUpdates=all"}, nil)
	if rest.IsStatus(err, http.StatusNotFound, http.StatusGone) {
		return nil
	}
	return err
}

// Respond implements pimapi.Calendars by patching the own attendee entry.
func (c *Client) Respond(ctx context.Context, calendarID, eventID, response, comment string, notify bool) error {
	status := map[string]string{pimapi.Accept: "accepted", pimapi.Tentative: "tentative", pimapi.Decline: "declined"}[response]
	if status == "" {
		return fmt.Errorf("google: unknown response %q", response)
	}
	var g gEvent
	if err := c.cal.Get(ctx, calPath(calendarID)+"/events/"+url.PathEscape(eventID), &g); err != nil {
		return err
	}
	found := false
	for i := range g.Attendees {
		if g.Attendees[i].Self || strings.EqualFold(g.Attendees[i].Email, c.cfg.Self) {
			g.Attendees[i].ResponseStatus = status
			found = true
		}
	}
	if !found {
		return fmt.Errorf("google: you are not an attendee of this event")
	}
	type att struct {
		Email          string `json:"email"`
		ResponseStatus string `json:"responseStatus,omitempty"`
		Comment        string `json:"comment,omitempty"`
		Optional       bool   `json:"optional,omitempty"`
	}
	var as []att
	for _, a := range g.Attendees {
		x := att{Email: a.Email, ResponseStatus: a.ResponseStatus, Optional: a.Optional}
		if a.Self || strings.EqualFold(a.Email, c.cfg.Self) {
			x.Comment = comment
		}
		as = append(as, x)
	}
	send := "none"
	if notify {
		send = "all"
	}
	return c.cal.Do(ctx, rest.Request{Method: http.MethodPatch, Path: calPath(calendarID) + "/events/" + url.PathEscape(eventID) + "?sendUpdates=" + send,
		JSON: map[string]any{"attendees": as}}, nil)
}

// FreeBusy implements pimapi.FreeBusy.
func (c *Client) FreeBusy(ctx context.Context, emails []string, from, to time.Time) (map[string][]pimapi.BusySlot, error) {
	items := make([]map[string]string, len(emails))
	for i, e := range emails {
		items[i] = map[string]string{"id": e}
	}
	var resp struct {
		Calendars map[string]struct {
			Busy []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"busy"`
		} `json:"calendars"`
	}
	if err := c.cal.Do(ctx, rest.Request{Method: http.MethodPost, Path: "/freeBusy", JSON: map[string]any{
		"timeMin": from.UTC().Format(time.RFC3339), "timeMax": to.UTC().Format(time.RFC3339), "items": items}}, &resp); err != nil {
		return nil, err
	}
	out := map[string][]pimapi.BusySlot{}
	for id, cal := range resp.Calendars {
		slots := []pimapi.BusySlot{}
		for _, b := range cal.Busy {
			s, _ := time.Parse(time.RFC3339, b.Start)
			e, _ := time.Parse(time.RFC3339, b.End)
			slots = append(slots, pimapi.BusySlot{Start: s.UTC(), End: e.UTC(), Status: "busy"})
		}
		out[strings.ToLower(id)] = slots
	}
	return out, nil
}

// ==== People API ==============================================================

const personFields = "names,emailAddresses,phoneNumbers,organizations,birthdays,biographies,metadata"

type person struct {
	ResourceName string `json:"resourceName,omitempty"`
	ETag         string `json:"etag,omitempty"`
	Names        []struct {
		DisplayName string `json:"displayName,omitempty"`
		GivenName   string `json:"givenName,omitempty"`
		FamilyName  string `json:"familyName,omitempty"`
	} `json:"names,omitempty"`
	EmailAddresses []struct {
		Value string `json:"value"`
	} `json:"emailAddresses,omitempty"`
	PhoneNumbers []struct {
		Value string `json:"value"`
	} `json:"phoneNumbers,omitempty"`
	Organizations []struct {
		Name  string `json:"name,omitempty"`
		Title string `json:"title,omitempty"`
	} `json:"organizations,omitempty"`
	Birthdays []struct {
		Date *struct {
			Year  int `json:"year,omitempty"`
			Month int `json:"month"`
			Day   int `json:"day"`
		} `json:"date,omitempty"`
	} `json:"birthdays,omitempty"`
	Biographies []struct {
		Value string `json:"value"`
	} `json:"biographies,omitempty"`
}

func toContact(p person) pimapi.Contact {
	c := pimapi.Contact{ID: p.ResourceName, FolderID: "default", ETag: p.ETag}
	if len(p.Names) > 0 {
		c.DisplayName, c.GivenName, c.FamilyName = p.Names[0].DisplayName, p.Names[0].GivenName, p.Names[0].FamilyName
	}
	for _, e := range p.EmailAddresses {
		c.Emails = append(c.Emails, e.Value)
	}
	for _, ph := range p.PhoneNumbers {
		c.Phones = append(c.Phones, ph.Value)
	}
	if len(p.Organizations) > 0 {
		c.Company, c.JobTitle = p.Organizations[0].Name, p.Organizations[0].Title
	}
	if len(p.Birthdays) > 0 && p.Birthdays[0].Date != nil {
		d := p.Birthdays[0].Date
		if d.Year > 0 {
			c.Birthday = fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
		} else {
			c.Birthday = fmt.Sprintf("--%02d-%02d", d.Month, d.Day)
		}
	}
	if len(p.Biographies) > 0 {
		c.Notes = p.Biographies[0].Value
	}
	return c
}

func fromContact(c pimapi.Contact) map[string]any {
	out := map[string]any{"names": []map[string]string{{"givenName": c.GivenName, "familyName": c.FamilyName, "unstructuredName": c.DisplayName}}}
	var es, ps []map[string]string
	for _, e := range c.Emails {
		es = append(es, map[string]string{"value": e})
	}
	for _, p := range c.Phones {
		ps = append(ps, map[string]string{"value": p})
	}
	out["emailAddresses"], out["phoneNumbers"] = es, ps
	if c.Company != "" || c.JobTitle != "" {
		out["organizations"] = []map[string]string{{"name": c.Company, "title": c.JobTitle}}
	}
	if c.Notes != "" {
		out["biographies"] = []map[string]string{{"value": c.Notes}}
	}
	return out
}

// ListAddressBooks implements pimapi.Contacts (one book: "My Contacts").
func (c *Client) ListAddressBooks(context.Context) ([]pimapi.AddressBook, error) {
	return []pimapi.AddressBook{{ID: "default", Name: "Google Contacts"}}, nil
}

// ListContacts implements pimapi.Contacts.
func (c *Client) ListContacts(ctx context.Context, _ string) ([]pimapi.Contact, error) {
	q := url.Values{"personFields": {personFields}, "pageSize": {"1000"}}
	var out []pimapi.Contact
	for {
		var resp struct {
			Connections   []person `json:"connections"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := c.ppl.Get(ctx, "/people/me/connections?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Connections {
			out = append(out, toContact(p))
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", resp.NextPageToken)
	}
}

// CreateContact implements pimapi.Contacts.
func (c *Client) CreateContact(ctx context.Context, _ string, ct pimapi.Contact) (pimapi.Contact, error) {
	var p person
	err := c.ppl.Do(ctx, rest.Request{Method: http.MethodPost, Path: "/people:createContact?personFields=" + personFields, JSON: fromContact(ct)}, &p)
	return toContact(p), err
}

// UpdateContact implements pimapi.Contacts (the etag is required by Google).
func (c *Client) UpdateContact(ctx context.Context, _ string, ct pimapi.Contact) (pimapi.Contact, error) {
	body := fromContact(ct)
	body["etag"] = ct.ETag
	var p person
	err := c.ppl.Do(ctx, rest.Request{Method: http.MethodPatch,
		Path: "/" + ct.ID + ":updateContact?updatePersonFields=names,emailAddresses,phoneNumbers,organizations,biographies&personFields=" + personFields,
		JSON: body}, &p)
	return toContact(p), err
}

// DeleteContact implements pimapi.Contacts.
func (c *Client) DeleteContact(ctx context.Context, _ string, id string) error {
	err := c.ppl.Do(ctx, rest.Request{Method: http.MethodDelete, Path: "/" + id + ":deleteContact"}, nil)
	if rest.IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}
