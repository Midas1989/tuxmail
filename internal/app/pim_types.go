// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/vcard"
)

// Calendar, tasks and contacts (KONZEPT 9, 10). The JSON shapes mirror the
// "// --- PIM ---" section of frontend/src/lib/types.ts. Times are RFC 3339
// in UTC; all-day values are dates ("2026-11-03", end exclusive).

// PIM UI event types.
const (
	EventCalendarChanged  = "calendar.changed"
	EventContactsChanged  = "contacts.changed"
	EventCalendarReminder = "calendar.reminder" // ItemID = event id
	EventInvitation       = "calendar.invitation"
)

// PimCalendar is a calendar or task list.
type PimCalendar struct {
	ID             int64    `json:"id"`
	AccountID      int64    `json:"accountId,omitempty"`
	AccountName    string   `json:"accountName,omitempty"`
	Name           string   `json:"name"`
	Color          string   `json:"color"`
	Kind           string   `json:"kind"` // local, caldav, subscription, birthdays
	ReadOnly       bool     `json:"readOnly"`
	Visible        bool     `json:"visible"`
	IsDefault      bool     `json:"isDefault"`
	Components     []string `json:"components"`
	URL            string   `json:"url,omitempty"`
	RefreshMinutes int      `json:"refreshMinutes,omitempty"`
	Description    string   `json:"description,omitempty"`
	LastSync       string   `json:"lastSync,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// CalendarInput creates (ID 0) or updates a calendar. Kind is "local" or
// "subscription" for new calendars; CalDAV calendars come from the server.
type CalendarInput struct {
	ID             int64    `json:"id,omitempty"`
	Name           string   `json:"name"`
	Color          string   `json:"color,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	URL            string   `json:"url,omitempty"`
	RefreshMinutes int      `json:"refreshMinutes,omitempty"`
	Visible        *bool    `json:"visible,omitempty"`
	IsDefault      *bool    `json:"isDefault,omitempty"`
	Components     []string `json:"components,omitempty"`
}

// RecurrenceRule is the editor form of an RRULE. Until is a date
// (YYYY-MM-DD, inclusive).
type RecurrenceRule struct {
	Freq       string   `json:"freq"`
	Interval   int      `json:"interval,omitempty"`
	Count      int      `json:"count,omitempty"`
	Until      string   `json:"until,omitempty"`
	ByDay      []string `json:"byDay,omitempty"`
	ByMonthDay []int    `json:"byMonthDay,omitempty"`
	ByMonth    []int    `json:"byMonth,omitempty"`
	BySetPos   []int    `json:"bySetPos,omitempty"`
	// Raw is the RRULE value (read-only; parts the editor does not know
	// are kept when the rule is written back).
	Raw string `json:"raw,omitempty"`
}

// Attendee of an event.
type Attendee = ical.Attendee

// EventAlarm is a reminder relative to the start.
type EventAlarm struct {
	MinutesBefore int    `json:"minutesBefore"`
	Action        string `json:"action,omitempty"` // DISPLAY (default), EMAIL, AUDIO
}

// CalendarEvent is one occurrence of an event.
type CalendarEvent struct {
	// ID addresses the occurrence: "<objectId>", "<objectId>/<instance>"
	// for recurring events, "bday/<contactId>/<year>" for birthdays and
	// "inv/<invitationId>" for unanswered invitations.
	ID           string          `json:"id"`
	ObjectID     int64           `json:"objectId"`
	CalendarID   int64           `json:"calendarId"`
	UID          string          `json:"uid"`
	RecurrenceID string          `json:"recurrenceId,omitempty"`
	Kind         string          `json:"kind"` // event, birthday, invitation
	Title        string          `json:"title"`
	Description  string          `json:"description,omitempty"`
	Location     string          `json:"location,omitempty"`
	URL          string          `json:"url,omitempty"`
	MeetingURL   string          `json:"meetingUrl,omitempty"`
	Start        string          `json:"start"`
	End          string          `json:"end"`
	AllDay       bool            `json:"allDay"`
	TimeZone     string          `json:"timeZone,omitempty"`
	Recurrence   *RecurrenceRule `json:"recurrence,omitempty"`
	Recurring    bool            `json:"recurring"`
	Exception    bool            `json:"exception,omitempty"`
	Organizer    *Attendee       `json:"organizer,omitempty"`
	Attendees    []Attendee      `json:"attendees"`
	Alarms       []EventAlarm    `json:"alarms"`
	IsOrganizer  bool            `json:"isOrganizer,omitempty"`
	MyPartStat   string          `json:"myPartStat,omitempty"`
	Status       string          `json:"status,omitempty"`
	Busy         bool            `json:"busy"`
	Categories   []string        `json:"categories"`
	Color        string          `json:"color,omitempty"`
	ReadOnly     bool            `json:"readOnly,omitempty"`
	Conflict     bool            `json:"conflict,omitempty"`
	InvitationID int64           `json:"invitationId,omitempty"`
	BirthYear    int             `json:"birthYear,omitempty"`
}

// EventInput creates or updates an event.
type EventInput struct {
	CalendarID  int64           `json:"calendarId"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Location    string          `json:"location,omitempty"`
	URL         string          `json:"url,omitempty"`
	MeetingURL  string          `json:"meetingUrl,omitempty"`
	Start       string          `json:"start"`
	End         string          `json:"end"`
	AllDay      bool            `json:"allDay"`
	TimeZone    string          `json:"timeZone,omitempty"`
	Recurrence  *RecurrenceRule `json:"recurrence,omitempty"`
	Attendees   []Attendee      `json:"attendees,omitempty"`
	Alarms      []EventAlarm    `json:"alarms,omitempty"`
	Busy        *bool           `json:"busy,omitempty"`
	Categories  []string        `json:"categories,omitempty"`
	Status      string          `json:"status,omitempty"`
	// NotifyAttendees sends iMIP invitations/updates (default true when
	// attendees are set; ignored for servers that schedule themselves).
	NotifyAttendees *bool `json:"notifyAttendees,omitempty"`
}

// EventQuery selects occurrences in [From, To).
type EventQuery struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	CalendarIDs []int64 `json:"calendarIds,omitempty"`
	// IncludeHidden returns events of hidden calendars too.
	IncludeHidden bool `json:"includeHidden,omitempty"`
}

// Recurrence edit scopes.
const (
	ScopeThis      = "this"
	ScopeFollowing = "following"
	ScopeAll       = "all"
)

// PimTask is a VTODO.
type PimTask struct {
	ID          int64           `json:"id"`
	CalendarID  int64           `json:"calendarId"`
	UID         string          `json:"uid"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Start       string          `json:"start,omitempty"`
	Due         string          `json:"due,omitempty"`
	DueAllDay   bool            `json:"dueAllDay,omitempty"`
	Priority    int             `json:"priority"` // 0 none, 1 high ... 9 low
	Progress    int             `json:"progress"` // 0..100
	Status      string          `json:"status"`
	Done        bool            `json:"done"`
	Completed   string          `json:"completed,omitempty"`
	ParentID    int64           `json:"parentId,omitempty"`
	ParentUID   string          `json:"parentUid,omitempty"`
	Categories  []string        `json:"categories"`
	Recurrence  *RecurrenceRule `json:"recurrence,omitempty"`
	Overdue     bool            `json:"overdue,omitempty"`
	ReadOnly    bool            `json:"readOnly,omitempty"`
	Color       string          `json:"color,omitempty"`
}

// TaskInput creates (ID 0) or updates a task.
type TaskInput struct {
	ID          int64           `json:"id,omitempty"`
	CalendarID  int64           `json:"calendarId,omitempty"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Due         string          `json:"due,omitempty"`
	DueAllDay   bool            `json:"dueAllDay,omitempty"`
	Priority    int             `json:"priority,omitempty"`
	Progress    int             `json:"progress,omitempty"`
	Status      string          `json:"status,omitempty"`
	Done        bool            `json:"done,omitempty"`
	ParentID    int64           `json:"parentId,omitempty"`
	Categories  []string        `json:"categories,omitempty"`
	Recurrence  *RecurrenceRule `json:"recurrence,omitempty"`
}

// TaskQuery filters tasks. Filter: open (default), today, overdue,
// upcoming, completed, all.
type TaskQuery struct {
	Filter      string  `json:"filter,omitempty"`
	CalendarIDs []int64 `json:"calendarIds,omitempty"`
	Search      string  `json:"search,omitempty"`
}

// Invitation is an iTIP message received by mail.
type Invitation struct {
	ID            int64      `json:"id"`
	MessageID     int64      `json:"messageId,omitempty"`
	Method        string     `json:"method"` // REQUEST, REPLY, CANCEL, COUNTER ...
	Status        string     `json:"status"` // pending, accepted, tentative, declined, applied, cancelled, obsolete
	UID           string     `json:"uid"`
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	Location      string     `json:"location,omitempty"`
	MeetingURL    string     `json:"meetingUrl,omitempty"`
	Start         string     `json:"start"`
	End           string     `json:"end"`
	AllDay        bool       `json:"allDay"`
	Recurring     bool       `json:"recurring"`
	Organizer     *Attendee  `json:"organizer,omitempty"`
	Attendees     []Attendee `json:"attendees"`
	MyPartStat    string     `json:"myPartStat,omitempty"`
	Sender        string     `json:"sender,omitempty"`
	Comment       string     `json:"comment,omitempty"`
	EventID       string     `json:"eventId,omitempty"`
	Conflicts     []string   `json:"conflicts"` // titles of overlapping events
	CanRespond    bool       `json:"canRespond"`
	ReplyPartStat string     `json:"replyPartStat,omitempty"` // REPLY: answer of the attendee
}

// InvitationResponse answers an invitation.
type InvitationResponse struct {
	PartStat   string `json:"partStat"` // ACCEPTED, TENTATIVE, DECLINED (COUNTER: ACCEPTED/DECLINED)
	Comment    string `json:"comment,omitempty"`
	CalendarID int64  `json:"calendarId,omitempty"`
	// SendReply mails the answer to the organizer (default true).
	SendReply *bool `json:"sendReply,omitempty"`
	// ProposeStart/End send a COUNTER (new time) instead of a reply.
	ProposeStart string `json:"proposeStart,omitempty"`
	ProposeEnd   string `json:"proposeEnd,omitempty"`
}

// FreeBusyQuery asks for busy times.
type FreeBusyQuery struct {
	Attendees []string `json:"attendees"`
	From      string   `json:"from"`
	To        string   `json:"to"`
}

// FreeBusy is the busy time of one address. Source: local (own calendars),
// caldav (server scheduling) or unknown.
type FreeBusy struct {
	Email  string     `json:"email"`
	Source string     `json:"source"`
	Busy   []TimeSlot `json:"busy"`
}

// DavAccountInput connects a CalDAV/CardDAV server.
type DavAccountInput struct {
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
	CardDAVURL  string `json:"carddavUrl,omitempty"` // default: URL
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email,omitempty"`
}

// PimAddressBook is an address book.
type PimAddressBook struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"accountId,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	Name        string `json:"name"`
	Kind        string `json:"kind"` // local, carddav
	IsDefault   bool   `json:"isDefault"`
	Count       int    `json:"count"`
	URL         string `json:"url,omitempty"`
	LastSync    string `json:"lastSync,omitempty"`
	Error       string `json:"error,omitempty"`
}

// AddressBookInput creates or renames a local address book.
type AddressBookInput struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name"`
	IsDefault *bool  `json:"isDefault,omitempty"`
}

// PimContact is a contact (vCard fields plus app data).
type PimContact struct {
	ID            int64  `json:"id"`
	AddressBookID int64  `json:"addressBookId"`
	PersonID      int64  `json:"personId,omitempty"`
	DisplayName   string `json:"displayName"`
	HasPhoto      bool   `json:"hasPhoto"`
	PhotoURL      string `json:"photoUrl,omitempty"`
	// Photo is input only: a data: URL sets the photo, "-" removes it,
	// "" keeps it.
	Photo    string         `json:"photo,omitempty"`
	Channels []PersonHandle `json:"channels"`
	vcard.Contact
}

// ContactQuery filters contacts.
type ContactQuery struct {
	AddressBookID int64  `json:"addressBookId,omitempty"`
	Search        string `json:"search,omitempty"`
	Group         string `json:"group,omitempty"`
}

// ContactGroup is a contact category with its member count.
type ContactGroup struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// DuplicateGroup is a set of probable duplicates with a merge preview.
type DuplicateGroup struct {
	Contacts []PimContact `json:"contacts"`
	Preview  PimContact   `json:"preview"`
}

// AddressSuggestion is an autocomplete result for the composer.
type AddressSuggestion struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	ContactID int64  `json:"contactId,omitempty"`
	PersonID  int64  `json:"personId,omitempty"`
	Source    string `json:"source"` // contact, recent
}
