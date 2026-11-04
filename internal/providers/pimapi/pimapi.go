// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pimapi is the neutral calendar/contacts/tasks model of the cloud
// providers (Microsoft Graph, EWS, Google Calendar/People). Each provider
// implements the Calendars/Contacts/Tasks interfaces; Import writes the
// results into the store tables (calendars, cal_objects, addressbooks,
// contacts) as iCalendar/vCard, so that the existing calendar and contact
// views work. The PIM module (internal/pim) may replace this adapter with
// its own sync model later; the interfaces are deliberately small.
package pimapi

import (
	"context"
	"time"
)

// Calendar is a calendar (or task list) on the server.
type Calendar struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    string `json:"color,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	// Owner is the mailbox of a shared/delegated calendar ("" = own).
	Owner     string `json:"owner,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// Attendee of an event.
type Attendee struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
	// Role: required | optional | resource.
	Role string `json:"role,omitempty"`
	// Status: needs-action | accepted | tentative | declined.
	Status string `json:"status,omitempty"`
}

// Event is a calendar event (or one occurrence of a series).
type Event struct {
	ID         string    `json:"id"`
	CalendarID string    `json:"calendarId,omitempty"`
	UID        string    `json:"uid,omitempty"` // iCalendar UID if known
	Summary    string    `json:"summary"`
	Body       string    `json:"body,omitempty"`
	Location   string    `json:"location,omitempty"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	AllDay     bool      `json:"allDay,omitempty"`
	TimeZone   string    `json:"timeZone,omitempty"`
	// RRule is the recurrence rule of a series master (RFC 5545 RRULE value).
	RRule string `json:"rrule,omitempty"`
	// SeriesID is set on occurrences/exceptions (id of the master).
	SeriesID     string     `json:"seriesId,omitempty"`
	Organizer    *Attendee  `json:"organizer,omitempty"`
	Attendees    []Attendee `json:"attendees,omitempty"`
	OnlineURL    string     `json:"onlineUrl,omitempty"` // Teams / Meet link
	IsOnline     bool       `json:"isOnline,omitempty"`
	Status       string     `json:"status,omitempty"`   // confirmed | tentative | cancelled
	Response     string     `json:"response,omitempty"` // own response status
	Categories   []string   `json:"categories,omitempty"`
	ETag         string     `json:"etag,omitempty"`
	LastModified time.Time  `json:"lastModified,omitempty"`
	Reminder     int        `json:"reminderMinutes,omitempty"`
}

// Response kinds for Respond.
const (
	Accept    = "accept"
	Tentative = "tentative"
	Decline   = "decline"
)

// Calendars is implemented by providers with calendar access.
type Calendars interface {
	ListCalendars(ctx context.Context) ([]Calendar, error)
	// ListEvents returns the expanded occurrences in [from, to).
	ListEvents(ctx context.Context, calendarID string, from, to time.Time) ([]Event, error)
	CreateEvent(ctx context.Context, calendarID string, ev Event) (Event, error)
	UpdateEvent(ctx context.Context, calendarID string, ev Event) (Event, error)
	DeleteEvent(ctx context.Context, calendarID, eventID string) error
	// Respond answers an invitation (Accept, Tentative, Decline).
	Respond(ctx context.Context, calendarID, eventID, response, comment string, notify bool) error
}

// BusySlot is a busy interval of a free/busy query.
type BusySlot struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Status string    `json:"status"` // busy | tentative | oof
}

// FreeBusy is implemented by providers with a free/busy query.
type FreeBusy interface {
	FreeBusy(ctx context.Context, emails []string, from, to time.Time) (map[string][]BusySlot, error)
}

// Contact is an address book entry.
type Contact struct {
	ID           string    `json:"id"`
	FolderID     string    `json:"folderId,omitempty"`
	DisplayName  string    `json:"displayName"`
	GivenName    string    `json:"givenName,omitempty"`
	FamilyName   string    `json:"familyName,omitempty"`
	Emails       []string  `json:"emails,omitempty"`
	Phones       []string  `json:"phones,omitempty"`
	Company      string    `json:"company,omitempty"`
	JobTitle     string    `json:"jobTitle,omitempty"`
	Notes        string    `json:"notes,omitempty"`
	Birthday     string    `json:"birthday,omitempty"` // YYYY-MM-DD
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

// AddressBook is a contact folder.
type AddressBook struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner,omitempty"`
}

// Contacts is implemented by providers with contact access.
type Contacts interface {
	ListAddressBooks(ctx context.Context) ([]AddressBook, error)
	ListContacts(ctx context.Context, bookID string) ([]Contact, error)
	CreateContact(ctx context.Context, bookID string, c Contact) (Contact, error)
	UpdateContact(ctx context.Context, bookID string, c Contact) (Contact, error)
	DeleteContact(ctx context.Context, bookID, id string) error
}

// Task is a to-do item.
type Task struct {
	ID          string    `json:"id"`
	ListID      string    `json:"listId,omitempty"`
	Title       string    `json:"title"`
	Notes       string    `json:"notes,omitempty"`
	Due         time.Time `json:"due,omitempty"`
	Completed   time.Time `json:"completed,omitempty"`
	Done        bool      `json:"done"`
	Importance  string    `json:"importance,omitempty"` // low | normal | high
	LastChanged time.Time `json:"lastChanged,omitempty"`
}

// Tasks is implemented by providers with task lists (Microsoft To Do, Google Tasks).
type Tasks interface {
	ListTaskLists(ctx context.Context) ([]Calendar, error)
	ListTasks(ctx context.Context, listID string) ([]Task, error)
	CreateTask(ctx context.Context, listID string, t Task) (Task, error)
	UpdateTask(ctx context.Context, listID string, t Task) (Task, error)
	DeleteTask(ctx context.Context, listID, id string) error
}
