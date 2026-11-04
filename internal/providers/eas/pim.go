// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// ==== models ======================================================================
//
// The PIM models are deliberately small and protocol-oriented; adapters
// (see package easstore) convert them to the local storage format, so the
// PIM module can plug in its own model without touching the protocol code.

// Attendee of a meeting.
type Attendee struct {
	Name, Email string
	// Status: 0 unknown, 2 tentative, 3 accept, 4 decline, 5 not responded.
	Status int
	// Type: 1 required, 2 optional, 3 resource.
	Type int
}

// Recurrence is an EAS recurrence pattern ([MS-ASCAL] 2.2.2.38).
type Recurrence struct {
	// Type: 0 daily, 1 weekly, 2 monthly, 3 monthly on the nth day,
	// 5 yearly, 6 yearly on the nth day.
	Type        int
	Interval    int
	Occurrences int
	Until       time.Time
	// DayOfWeek is a bit mask: 1 Sunday, 2 Monday ... 64 Saturday.
	DayOfWeek   int
	DayOfMonth  int
	WeekOfMonth int // 1-4, 5 = last
	MonthOfYear int
}

// Event is a calendar item.
type Event struct {
	UID        string
	Subject    string
	Location   string
	Body       string
	Start, End time.Time
	AllDay     bool
	OrganizerName,
	OrganizerEmail string
	Attendees  []Attendee
	Recurrence *Recurrence
	// ExceptionsDeleted are start times of deleted occurrences.
	ExceptionsDeleted []time.Time
	Categories        []string
	// ReminderMinutes before start (-1 = none).
	ReminderMinutes int
	// BusyStatus: 0 free, 1 tentative, 2 busy, 3 out of office, 4 elsewhere.
	BusyStatus int
	// Sensitivity: 0 normal, 1 personal, 2 private, 3 confidential.
	Sensitivity   int
	MeetingStatus int
	// TimeZone is the EAS TIME_ZONE_INFORMATION blob (base64), kept as is.
	TimeZone string
}

// Contact is a contacts item.
type Contact struct {
	FirstName, MiddleName, LastName, Suffix, Title string
	FileAs, NickName                               string
	Company, Department, JobTitle, OfficeLocation  string
	Emails                                         []string // up to 3
	// Phones maps EAS property names (MobilePhoneNumber, HomePhoneNumber,
	// BusinessPhoneNumber, ...) to numbers.
	Phones      map[string]string
	Home        Address
	Business    Address
	Other       Address
	Birthday    time.Time
	WebPage     string
	Notes       string
	Categories  []string
	IMAddresses []string
}

// Address is a postal address.
type Address struct {
	Street, City, State, PostalCode, Country string
}

// IsZero reports whether a has no field set.
func (a Address) IsZero() bool { return a == Address{} }

// Task is a tasks item.
type Task struct {
	Subject     string
	Body        string
	Start, Due  time.Time
	Complete    bool
	CompletedAt time.Time
	// Importance: 0 low, 1 normal, 2 high.
	Importance int
	Categories []string
	ReminderAt time.Time
}

// PIMItem is one calendar, contacts or tasks item.
type PIMItem struct {
	Class   string // "Calendar", "Contacts", "Tasks"
	Event   *Event
	Contact *Contact
	Task    *Task
}

// ==== ApplicationData conversion ==================================================

func calTime(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func textList(n *wbxml.Node, list, item string) []string {
	var out []string
	for _, c := range n.Child(list).All(item) {
		if v := strings.TrimSpace(c.Value()); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func bodyText(d *wbxml.Node, legacy string) string {
	if b := d.Child("AirSyncBase:Body"); b != nil {
		return b.TextOf("AirSyncBase:Data")
	}
	return d.TextOf(legacy)
}

// ParseItem converts the ApplicationData of a PIM item.
func ParseItem(class string, d *wbxml.Node) PIMItem {
	it := PIMItem{Class: class}
	switch class {
	case "Calendar":
		it.Event = parseEvent(d)
	case "Contacts":
		it.Contact = parseContact(d)
	case "Tasks":
		it.Task = parseTask(d)
	}
	return it
}

func parseRecurrence(r *wbxml.Node, page string) *Recurrence {
	if r == nil {
		return nil
	}
	return &Recurrence{
		Type: r.IntOf(page+":Type", 0), Interval: r.IntOf(page+":Interval", 1),
		Occurrences: r.IntOf(page+":Occurrences", 0), Until: parseEASTime(r.TextOf(page + ":Until")),
		DayOfWeek: r.IntOf(page+":DayOfWeek", 0), DayOfMonth: r.IntOf(page+":DayOfMonth", 0),
		WeekOfMonth: r.IntOf(page+":WeekOfMonth", 0), MonthOfYear: r.IntOf(page+":MonthOfYear", 0),
	}
}

func parseEvent(d *wbxml.Node) *Event {
	e := &Event{
		UID: d.TextOf("Calendar:UID"), Subject: d.TextOf("Calendar:Subject"), Location: d.TextOf("Calendar:Location"),
		Body: bodyText(d, "Calendar:Body"), Start: parseEASTime(d.TextOf("Calendar:StartTime")),
		End: parseEASTime(d.TextOf("Calendar:EndTime")), AllDay: d.TextOf("Calendar:AllDayEvent") == "1",
		OrganizerName: d.TextOf("Calendar:OrganizerName"), OrganizerEmail: d.TextOf("Calendar:OrganizerEmail"),
		Categories: textList(d, "Calendar:Categories", "Calendar:Category"), ReminderMinutes: d.IntOf("Calendar:Reminder", -1),
		BusyStatus: d.IntOf("Calendar:BusyStatus", 2), Sensitivity: d.IntOf("Calendar:Sensitivity", 0),
		MeetingStatus: d.IntOf("Calendar:MeetingStatus", 0), TimeZone: d.TextOf("Calendar:TimeZone"),
		Recurrence: parseRecurrence(d.Child("Calendar:Recurrence"), "Calendar"),
	}
	if e.Location == "" {
		e.Location = d.Path("AirSyncBase:Location").TextOf("AirSyncBase:DisplayName")
	}
	for _, a := range d.Path("Calendar:Attendees").All("Calendar:Attendee") {
		e.Attendees = append(e.Attendees, Attendee{Name: a.TextOf("Calendar:Name"), Email: a.TextOf("Calendar:Email"),
			Status: a.IntOf("Calendar:AttendeeStatus", 0), Type: a.IntOf("Calendar:AttendeeType", 1)})
	}
	for _, x := range d.Path("Calendar:Exceptions").All("Calendar:Exception") {
		if x.TextOf("Calendar:Deleted") == "1" {
			st := x.TextOf("Calendar:ExceptionStartTime")
			if st == "" {
				st = x.TextOf("Calendar:StartTime")
			}
			if t := parseEASTime(st); !t.IsZero() {
				e.ExceptionsDeleted = append(e.ExceptionsDeleted, t)
			}
		}
	}
	return e
}

// contactPhones are the phone properties of [MS-ASCNTC].
var contactPhones = []string{"MobilePhoneNumber", "HomePhoneNumber", "Home2PhoneNumber", "BusinessPhoneNumber",
	"Business2PhoneNumber", "BusinessFaxNumber", "HomeFaxNumber", "CarPhoneNumber", "PagerNumber",
	"RadioPhoneNumber", "AssistantPhoneNumber"}

func parseAddress(d *wbxml.Node, prefix string) Address {
	return Address{Street: d.TextOf("Contacts:" + prefix + "Street"), City: d.TextOf("Contacts:" + prefix + "City"),
		State: d.TextOf("Contacts:" + prefix + "State"), PostalCode: d.TextOf("Contacts:" + prefix + "PostalCode"),
		Country: d.TextOf("Contacts:" + prefix + "Country")}
}

func parseContact(d *wbxml.Node) *Contact {
	c := &Contact{
		FirstName: d.TextOf("Contacts:FirstName"), MiddleName: d.TextOf("Contacts:MiddleName"),
		LastName: d.TextOf("Contacts:LastName"), Suffix: d.TextOf("Contacts:Suffix"), Title: d.TextOf("Contacts:Title"),
		FileAs: d.TextOf("Contacts:FileAs"), NickName: d.TextOf("Contacts2:NickName"),
		Company: d.TextOf("Contacts:CompanyName"), Department: d.TextOf("Contacts:Department"),
		JobTitle: d.TextOf("Contacts:JobTitle"), OfficeLocation: d.TextOf("Contacts:OfficeLocation"),
		Home: parseAddress(d, "Home"), Business: parseAddress(d, "Business"), Other: parseAddress(d, "Other"),
		Birthday: parseEASTime(d.TextOf("Contacts:Birthday")), WebPage: d.TextOf("Contacts:WebPage"),
		Notes: bodyText(d, "Contacts:Body"), Categories: textList(d, "Contacts:Categories", "Contacts:Category"),
		Phones: map[string]string{},
	}
	for _, k := range []string{"Contacts:Email1Address", "Contacts:Email2Address", "Contacts:Email3Address"} {
		if v := d.TextOf(k); v != "" {
			if as := parseAddressList(v); len(as) > 0 {
				v = as[0].Email
			}
			c.Emails = append(c.Emails, v)
		}
	}
	for _, k := range contactPhones {
		if v := d.TextOf("Contacts:" + k); v != "" {
			c.Phones[k] = v
		}
	}
	if v := d.TextOf("Contacts2:CompanyMainPhone"); v != "" {
		c.Phones["CompanyMainPhone"] = v
	}
	for _, k := range []string{"Contacts2:IMAddress", "Contacts2:IMAddress2", "Contacts2:IMAddress3"} {
		if v := d.TextOf(k); v != "" {
			c.IMAddresses = append(c.IMAddresses, v)
		}
	}
	return c
}

func parseTask(d *wbxml.Node) *Task {
	t := &Task{
		Subject: d.TextOf("Tasks:Subject"), Body: bodyText(d, "Tasks:Body"),
		Start: parseEASTime(d.TextOf("Tasks:UtcStartDate")), Due: parseEASTime(d.TextOf("Tasks:UtcDueDate")),
		Complete: d.TextOf("Tasks:Complete") == "1", CompletedAt: parseEASTime(d.TextOf("Tasks:DateCompleted")),
		Importance: d.IntOf("Tasks:Importance", 1), Categories: textList(d, "Tasks:Categories", "Tasks:Category"),
	}
	if t.Start.IsZero() {
		t.Start = parseEASTime(d.TextOf("Tasks:StartDate"))
	}
	if t.Due.IsZero() {
		t.Due = parseEASTime(d.TextOf("Tasks:DueDate"))
	}
	if d.TextOf("Tasks:ReminderSet") == "1" {
		t.ReminderAt = parseEASTime(d.TextOf("Tasks:ReminderTime"))
	}
	return t
}

// utcTimeZone is the TIME_ZONE_INFORMATION of UTC (172 zero bytes).
var utcTimeZone = base64.StdEncoding.EncodeToString(make([]byte, 172))

func body(text string, v12 bool, legacy string) *wbxml.Node {
	if v12 {
		return wbxml.E("AirSyncBase:Body", wbxml.S("AirSyncBase:Type", "1"), wbxml.S("AirSyncBase:Data", text))
	}
	return wbxml.S(legacy, text)
}

func optS(name, v string) *wbxml.Node {
	if v == "" {
		return nil
	}
	return wbxml.S(name, v)
}

func catNode(list, item string, cats []string) *wbxml.Node {
	if len(cats) == 0 {
		return nil
	}
	n := wbxml.E(list)
	for _, c := range cats {
		n.Add(wbxml.S(item, c))
	}
	return n
}

// EncodeItem converts a PIM item to ApplicationData children. The element
// order follows the schemas of [MS-ASCAL], [MS-ASCNTC] and [MS-ASTASK].
func EncodeItem(it PIMItem, version string) []*wbxml.Node {
	v12 := compareVersions(version, "12.0") >= 0
	switch {
	case it.Event != nil:
		return encodeEvent(it.Event, v12)
	case it.Contact != nil:
		return encodeContact(it.Contact, v12)
	case it.Task != nil:
		return encodeTask(it.Task, v12)
	}
	return nil
}

func encodeEvent(e *Event, v12 bool) []*wbxml.Node {
	tz := e.TimeZone
	if tz == "" {
		tz = utcTimeZone
	}
	out := []*wbxml.Node{wbxml.S("Calendar:TimeZone", tz)}
	if e.AllDay {
		out = append(out, wbxml.S("Calendar:AllDayEvent", "1"))
	}
	if e.Body != "" {
		out = append(out, body(e.Body, v12, "Calendar:Body"))
	}
	out = append(out, wbxml.S("Calendar:BusyStatus", strconv.Itoa(e.BusyStatus)))
	if e.OrganizerName != "" {
		out = append(out, wbxml.S("Calendar:OrganizerName", e.OrganizerName))
	}
	if e.OrganizerEmail != "" {
		out = append(out, wbxml.S("Calendar:OrganizerEmail", e.OrganizerEmail))
	}
	out = append(out, wbxml.S("Calendar:DtStamp", calTime(time.Now())))
	end := e.End
	if end.IsZero() {
		end = e.Start.Add(time.Hour)
	}
	out = append(out, wbxml.S("Calendar:EndTime", calTime(end)))
	out = append(out, optS("Calendar:Location", e.Location), wbxml.S("Calendar:Reminder", reminderStr(e.ReminderMinutes)))
	out = append(out, wbxml.S("Calendar:Sensitivity", strconv.Itoa(e.Sensitivity)), wbxml.S("Calendar:Subject", e.Subject),
		wbxml.S("Calendar:StartTime", calTime(e.Start)))
	if e.UID != "" {
		out = append(out, wbxml.S("Calendar:UID", e.UID))
	}
	out = append(out, wbxml.S("Calendar:MeetingStatus", strconv.Itoa(e.MeetingStatus)))
	if len(e.Attendees) > 0 {
		at := wbxml.E("Calendar:Attendees")
		for _, a := range e.Attendees {
			n := wbxml.E("Calendar:Attendee", wbxml.S("Calendar:Email", a.Email), wbxml.S("Calendar:Name", nonEmpty(a.Name, a.Email)))
			if a.Type > 0 {
				n.Add(wbxml.S("Calendar:AttendeeType", strconv.Itoa(a.Type)))
			}
			at.Add(n)
		}
		out = append(out, at)
	}
	out = append(out, catNode("Calendar:Categories", "Calendar:Category", e.Categories))
	if r := e.Recurrence; r != nil {
		rn := wbxml.E("Calendar:Recurrence", wbxml.S("Calendar:Type", strconv.Itoa(r.Type)))
		if r.Occurrences > 0 {
			rn.Add(wbxml.S("Calendar:Occurrences", strconv.Itoa(r.Occurrences)))
		}
		if r.Interval > 1 {
			rn.Add(wbxml.S("Calendar:Interval", strconv.Itoa(r.Interval)))
		}
		if r.WeekOfMonth > 0 {
			rn.Add(wbxml.S("Calendar:WeekOfMonth", strconv.Itoa(r.WeekOfMonth)))
		}
		if r.DayOfMonth > 0 {
			rn.Add(wbxml.S("Calendar:DayOfMonth", strconv.Itoa(r.DayOfMonth)))
		}
		if r.DayOfWeek > 0 {
			rn.Add(wbxml.S("Calendar:DayOfWeek", strconv.Itoa(r.DayOfWeek)))
		}
		if r.MonthOfYear > 0 {
			rn.Add(wbxml.S("Calendar:MonthOfYear", strconv.Itoa(r.MonthOfYear)))
		}
		if !r.Until.IsZero() {
			rn.Add(wbxml.S("Calendar:Until", calTime(r.Until)))
		}
		out = append(out, rn)
	}
	if len(e.ExceptionsDeleted) > 0 {
		ex := wbxml.E("Calendar:Exceptions")
		for _, t := range e.ExceptionsDeleted {
			ex.Add(wbxml.E("Calendar:Exception", wbxml.S("Calendar:Deleted", "1"),
				wbxml.S("Calendar:ExceptionStartTime", calTime(t))))
		}
		out = append(out, ex)
	}
	return compact(out)
}

func reminderStr(m int) string {
	if m < 0 {
		return ""
	}
	return strconv.Itoa(m)
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func compact(ns []*wbxml.Node) []*wbxml.Node {
	out := ns[:0]
	for _, n := range ns {
		if n != nil && (n.Text != "" || n.IsOpaque || len(n.Children) > 0 || n.Is("Calendar:Attendees")) {
			out = append(out, n)
		}
	}
	return out
}

func encodeContact(c *Contact, v12 bool) []*wbxml.Node {
	var out []*wbxml.Node
	if !c.Birthday.IsZero() {
		out = append(out, wbxml.S("Contacts:Birthday", easTime(c.Birthday)))
	}
	if c.Notes != "" {
		out = append(out, body(c.Notes, v12, "Contacts:Body"))
	}
	addr := func(prefix string, a Address) {
		out = append(out, optS("Contacts:"+prefix+"City", a.City), optS("Contacts:"+prefix+"Country", a.Country),
			optS("Contacts:"+prefix+"PostalCode", a.PostalCode), optS("Contacts:"+prefix+"State", a.State),
			optS("Contacts:"+prefix+"Street", a.Street))
	}
	addr("Business", c.Business)
	out = append(out, catNode("Contacts:Categories", "Contacts:Category", c.Categories),
		optS("Contacts:CompanyName", c.Company), optS("Contacts:Department", c.Department))
	for i, e := range c.Emails {
		if i >= 3 {
			break
		}
		out = append(out, optS("Contacts:Email"+strconv.Itoa(i+1)+"Address", e))
	}
	fileAs := c.FileAs
	if fileAs == "" {
		fileAs = strings.Trim(strings.TrimSpace(c.LastName+", "+c.FirstName), ", ")
	}
	out = append(out, optS("Contacts:FileAs", fileAs), optS("Contacts:FirstName", c.FirstName))
	addr("Home", c.Home)
	out = append(out, optS("Contacts:JobTitle", c.JobTitle), optS("Contacts:LastName", c.LastName),
		optS("Contacts:MiddleName", c.MiddleName), optS("Contacts:OfficeLocation", c.OfficeLocation))
	addr("Other", c.Other)
	for _, k := range contactPhones {
		out = append(out, optS("Contacts:"+k, c.Phones[k]))
	}
	out = append(out, optS("Contacts:Suffix", c.Suffix), optS("Contacts:Title", c.Title), optS("Contacts:WebPage", c.WebPage))
	for i, im := range c.IMAddresses {
		name := "Contacts2:IMAddress"
		if i > 0 {
			name += strconv.Itoa(i + 1)
		}
		if i < 3 {
			out = append(out, optS(name, im))
		}
	}
	out = append(out, optS("Contacts2:CompanyMainPhone", c.Phones["CompanyMainPhone"]), optS("Contacts2:NickName", c.NickName))
	return compact(out)
}

func encodeTask(t *Task, v12 bool) []*wbxml.Node {
	var out []*wbxml.Node
	if t.Body != "" {
		out = append(out, body(t.Body, v12, "Tasks:Body"))
	}
	out = append(out, catNode("Tasks:Categories", "Tasks:Category", t.Categories),
		wbxml.S("Tasks:Complete", boolStr(t.Complete)))
	if t.Complete {
		ca := t.CompletedAt
		if ca.IsZero() {
			ca = time.Now()
		}
		out = append(out, wbxml.S("Tasks:DateCompleted", easTime(ca)))
	}
	if !t.Due.IsZero() {
		out = append(out, wbxml.S("Tasks:DueDate", easTime(t.Due)), wbxml.S("Tasks:UtcDueDate", easTime(t.Due)))
	}
	out = append(out, wbxml.S("Tasks:Importance", strconv.Itoa(t.Importance)))
	if !t.ReminderAt.IsZero() {
		out = append(out, wbxml.S("Tasks:ReminderSet", "1"), wbxml.S("Tasks:ReminderTime", easTime(t.ReminderAt)))
	} else {
		out = append(out, wbxml.S("Tasks:ReminderSet", "0"))
	}
	out = append(out, wbxml.S("Tasks:Sensitivity", "0"))
	if !t.Start.IsZero() {
		out = append(out, wbxml.S("Tasks:StartDate", easTime(t.Start)), wbxml.S("Tasks:UtcStartDate", easTime(t.Start)))
	}
	out = append(out, wbxml.S("Tasks:Subject", t.Subject))
	return compact(out)
}

// ==== PIM synchronization ===========================================================

// PIMCollection is the local counterpart of an EAS calendar, contacts or
// tasks folder.
type PIMCollection struct {
	// LocalID identifies the collection in the PIMStore.
	LocalID  int64
	ServerID string
	Class    string
	SyncKey  string
}

// LocalChange is a change made locally that must be sent to the server.
type LocalChange struct {
	// Kind is "Add", "Change" or "Delete".
	Kind string
	// LocalRef identifies a new item until the server assigned an id.
	LocalRef string
	ServerID string
	Item     PIMItem
}

// PIMStore is the local side of the PIM synchronization (implemented by
// easstore.Store on the store tables; the PIM module may provide its own).
type PIMStore interface {
	// Collection returns (creating it if needed) the local collection of
	// an EAS folder for the account.
	Collection(ctx context.Context, accountID int64, f Folder) (PIMCollection, error)
	// Collections lists the local EAS collections of the account.
	Collections(ctx context.Context, accountID int64) ([]PIMCollection, error)
	// RemoveCollection deletes a collection whose folder vanished.
	RemoveCollection(ctx context.Context, c PIMCollection) error
	// SetSyncKey stores the collection's SyncKey ("" resets it).
	SetSyncKey(ctx context.Context, c PIMCollection, key string) error
	// Reset removes all items of the collection (full resync).
	Reset(ctx context.Context, c PIMCollection) error
	// Put stores a server item (insert or replace).
	Put(ctx context.Context, c PIMCollection, serverID string, item PIMItem) error
	// Remove deletes a server item locally.
	Remove(ctx context.Context, c PIMCollection, serverID string) error
	// LocalChanges returns pending local changes.
	LocalChanges(ctx context.Context, c PIMCollection) ([]LocalChange, error)
	// Ack confirms that a local change reached the server; for Add,
	// serverID is the id assigned by the server.
	Ack(ctx context.Context, c PIMCollection, ch LocalChange, serverID string) error
}

// PIMOptions configure SyncPIM.
type PIMOptions struct {
	// Classes to synchronize (default Calendar, Contacts, Tasks).
	Classes []string
	// CalendarFilter is the calendar FilterType (default "0" = all;
	// "4" = 2 weeks back ... "7" = 6 months back).
	CalendarFilter string
	// TaskFilter is the tasks FilterType (default "0" = all; "8" = open).
	TaskFilter string
	// ReadOnly disables pushing local changes (one-way sync).
	ReadOnly   bool
	WindowSize int
}

// PIMResult counts the applied changes.
type PIMResult struct {
	Collections             int
	Added, Changed, Deleted int
	Pushed                  int
	Errors                  []error
}

// SyncPIM synchronizes the calendar, contacts and tasks folders of the
// account two-way with st: local changes are pushed first (Sync
// Add/Change/Delete), then server changes are applied. On conflicts the
// server wins (KONZEPT 9.2).
func (c *Client) SyncPIM(ctx context.Context, st PIMStore, accountID int64, o PIMOptions) (PIMResult, error) {
	var res PIMResult
	if len(o.Classes) == 0 {
		o.Classes = []string{"Calendar", "Contacts", "Tasks"}
	}
	want := map[string]bool{}
	for _, cl := range o.Classes {
		want[cl] = true
	}
	folders, err := c.FolderSync(ctx)
	if err != nil {
		return res, err
	}
	present := map[string]bool{}
	for _, f := range folders {
		if !want[f.Class()] {
			continue
		}
		present[f.ServerID] = true
		coll, err := st.Collection(ctx, accountID, f)
		if err != nil {
			return res, err
		}
		res.Collections++
		if err := c.syncPIMCollection(ctx, st, coll, o, &res); err != nil {
			if ctx.Err() != nil {
				return res, err
			}
			res.Errors = append(res.Errors, fmt.Errorf("%s %q: %w", f.Class(), f.Name, err))
		}
	}
	colls, err := st.Collections(ctx, accountID)
	if err != nil {
		return res, err
	}
	for _, coll := range colls {
		if !present[coll.ServerID] && want[coll.Class] {
			if err := st.RemoveCollection(ctx, coll); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func (c *Client) pimOptions(class string, o PIMOptions) SyncOptions {
	so := SyncOptions{Class: class, WindowSize: o.WindowSize,
		BodyPreferences: []BodyPref{{Type: BodyPlain, TruncationSize: 64 << 10}}}
	switch class {
	case "Calendar":
		so.FilterType = nonEmpty(o.CalendarFilter, "0")
	case "Tasks":
		so.FilterType = nonEmpty(o.TaskFilter, "0")
	}
	return so
}

func (c *Client) syncPIMCollection(ctx context.Context, st PIMStore, coll PIMCollection, o PIMOptions, res *PIMResult) error {
	opts := c.pimOptions(coll.Class, o)
	version, err := c.Version(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err = c.syncPIMOnce(ctx, st, &coll, opts, o, version, res)
		if errors.Is(err, ErrInvalidSyncKey) && attempt == 0 {
			c.log.Info("eas: PIM sync key rejected, full resync", "collection", coll.ServerID)
			if err := st.Reset(ctx, coll); err != nil {
				return err
			}
			coll.SyncKey = ""
			if err := st.SetSyncKey(ctx, coll, ""); err != nil {
				return err
			}
			continue
		}
		return err
	}
	return err
}

func (c *Client) syncPIMOnce(ctx context.Context, st PIMStore, coll *PIMCollection, opts SyncOptions, o PIMOptions,
	version string, res *PIMResult) error {
	if coll.SyncKey == "" || coll.SyncKey == "0" {
		r, err := c.SyncCollection(ctx, coll.ServerID, "0", opts, nil)
		if err != nil {
			return err
		}
		coll.SyncKey = r.SyncKey
		if err := st.SetSyncKey(ctx, *coll, coll.SyncKey); err != nil {
			return err
		}
	}
	// Push local changes.
	if !o.ReadOnly {
		changes, err := st.LocalChanges(ctx, *coll)
		if err != nil {
			return err
		}
		if len(changes) > 0 {
			var cc []ClientChange
			byRef := map[string]LocalChange{}
			for _, ch := range changes {
				x := ClientChange{Kind: ch.Kind, ServerID: ch.ServerID}
				if ch.Kind == "Add" {
					x.ClientID = ch.LocalRef
					byRef[ch.LocalRef] = ch
				} else {
					byRef["s:"+ch.ServerID] = ch
				}
				if ch.Kind != "Delete" {
					x.Data = EncodeItem(ch.Item, version)
				}
				cc = append(cc, x)
			}
			so := opts
			so.NoChanges = true
			r, err := c.SyncCollection(ctx, coll.ServerID, coll.SyncKey, so, cc)
			if err != nil {
				return err
			}
			failed := map[string]bool{}
			for _, ir := range r.Responses {
				key := "s:" + ir.ServerID
				if ir.Kind == "Add" {
					key = ir.ClientID
				}
				ch, ok := byRef[key]
				if !ok {
					continue
				}
				if ir.Status != SyncStatusOK {
					failed[key] = true
					if ir.Status != SyncStatusNotFound {
						res.Errors = append(res.Errors, fmt.Errorf("push %s %s: %w", ch.Kind, nonEmpty(ch.ServerID, ch.LocalRef), ItemStatusError(ir)))
						continue
					}
				}
				if err := st.Ack(ctx, *coll, ch, ir.ServerID); err != nil {
					return err
				}
				delete(byRef, key)
				res.Pushed++
			}
			// Changes and deletes without a response element succeeded.
			for key, ch := range byRef {
				if ch.Kind == "Add" || failed[key] {
					continue
				}
				if err := st.Ack(ctx, *coll, ch, ch.ServerID); err != nil {
					return err
				}
				res.Pushed++
			}
			if err := c.applyPIM(ctx, st, *coll, r, res); err != nil {
				return err
			}
			coll.SyncKey = r.SyncKey
			if err := st.SetSyncKey(ctx, *coll, coll.SyncKey); err != nil {
				return err
			}
		}
	}
	// Pull server changes.
	for round := 0; round < 1000; round++ {
		r, err := c.SyncCollection(ctx, coll.ServerID, coll.SyncKey, opts, nil)
		if err != nil {
			return err
		}
		if err := c.applyPIM(ctx, st, *coll, r, res); err != nil {
			return err
		}
		coll.SyncKey = r.SyncKey
		if err := st.SetSyncKey(ctx, *coll, coll.SyncKey); err != nil {
			return err
		}
		if !r.MoreAvailable {
			break
		}
	}
	return nil
}

func (c *Client) applyPIM(ctx context.Context, st PIMStore, coll PIMCollection, r *SyncResult, res *PIMResult) error {
	for _, it := range r.Added {
		if err := st.Put(ctx, coll, it.ServerID, ParseItem(coll.Class, it.Data)); err != nil {
			return err
		}
		res.Added++
	}
	for _, it := range r.Changed {
		if err := st.Put(ctx, coll, it.ServerID, ParseItem(coll.Class, it.Data)); err != nil {
			return err
		}
		res.Changed++
	}
	for _, id := range r.Deleted {
		if err := st.Remove(ctx, coll, id); err != nil {
			return err
		}
		res.Deleted++
	}
	return nil
}
