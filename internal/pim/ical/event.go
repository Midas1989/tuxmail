// SPDX-License-Identifier: AGPL-3.0-or-later

package ical

import (
	"strconv"
	"strings"
	"time"
)

// Participation states (PARTSTAT).
const (
	PartNeedsAction = "NEEDS-ACTION"
	PartAccepted    = "ACCEPTED"
	PartDeclined    = "DECLINED"
	PartTentative   = "TENTATIVE"
	PartDelegated   = "DELEGATED"
)

// Attendee is an ATTENDEE or ORGANIZER.
type Attendee struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`     // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR, NON-PARTICIPANT
	PartStat string `json:"partStat,omitempty"` // NEEDS-ACTION, ACCEPTED ...
	RSVP     bool   `json:"rsvp,omitempty"`
	CUType   string `json:"cuType,omitempty"` // INDIVIDUAL, ROOM, RESOURCE, GROUP
}

// Alarm is a VALARM. Trigger is relative to the start (or end with
// Related "END"); Absolute is set for absolute triggers.
type Alarm struct {
	Action      string        `json:"action"` // DISPLAY, AUDIO, EMAIL
	Trigger     time.Duration `json:"trigger"`
	Related     string        `json:"related,omitempty"`
	Absolute    time.Time     `json:"absolute,omitzero"`
	Description string        `json:"description,omitempty"`
}

// Event is the typed view of a VEVENT or VTODO.
type Event struct {
	Kind          string // VEVENT or VTODO
	UID           string
	Sequence      int
	RecurrenceID  TimeValue
	ThisAndFuture bool // RECURRENCE-ID;RANGE=THISANDFUTURE

	Summary     string
	Description string
	Location    string
	URL         string
	Conference  string // online meeting link (CONFERENCE, X-*-MEETING-URL)
	Categories  []string
	Color       string
	Class       string
	Status      string
	Transparent bool

	Start    TimeValue
	End      TimeValue     // DTEND (events)
	Duration time.Duration // DURATION when no DTEND

	// Tasks.
	Due             TimeValue
	Completed       time.Time
	PercentComplete int
	Priority        int
	RelatedTo       string // parent UID (RELATED-TO;RELTYPE=PARENT)

	RRule   string
	ExDates []TimeValue
	RDates  []TimeValue

	Organizer *Attendee
	Attendees []Attendee
	Alarms    []Alarm

	DTStamp      time.Time
	LastModified time.Time
}

// EndTime returns the effective end: DTEND, DTSTART+DURATION, the next day
// for all-day events or the start itself.
func (e *Event) EndTime() time.Time {
	switch {
	case !e.End.IsZero():
		return e.End.Time
	case e.Duration > 0:
		return e.Start.Time.Add(e.Duration)
	case e.Start.AllDay:
		return e.Start.Time.AddDate(0, 0, 1)
	}
	return e.Start.Time
}

// Length is the duration of one occurrence.
func (e *Event) Length() time.Duration {
	if e.Start.IsZero() {
		return 0
	}
	if e.Start.AllDay && !e.End.IsZero() {
		// Count calendar days (DST-independent).
		return time.Duration(daysBetween(e.Start.Time, e.End.Time)) * 24 * time.Hour
	}
	return e.EndTime().Sub(e.Start.Time)
}

func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC).Sub(time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)) / (24 * time.Hour))
}

// MailAddress strips "mailto:" from a CAL-ADDRESS.
func MailAddress(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		v = v[7:]
	}
	return strings.ToLower(v)
}

func readAttendee(p *Prop) Attendee {
	a := Attendee{Email: MailAddress(p.Value), Name: p.Param("CN"), Role: strings.ToUpper(p.Param("ROLE")),
		PartStat: strings.ToUpper(p.Param("PARTSTAT")), CUType: strings.ToUpper(p.Param("CUTYPE"))}
	a.RSVP = strings.EqualFold(p.Param("RSVP"), "TRUE")
	if a.PartStat == "" && p.Name == "ATTENDEE" {
		a.PartStat = PartNeedsAction
	}
	return a
}

// ReadEvent extracts the typed fields of a VEVENT/VTODO.
func ReadEvent(c *Component, tz *TZ) Event {
	e := Event{Kind: c.Name, UID: c.Value("UID"), Summary: c.Text("SUMMARY"), Description: c.Text("DESCRIPTION"),
		Location: c.Text("LOCATION"), URL: c.Value("URL"), Class: strings.ToUpper(c.Value("CLASS")),
		Status: strings.ToUpper(c.Value("STATUS")), Color: c.Value("COLOR"), RRule: c.Value("RRULE"),
		RelatedTo: relatedParent(c)}
	e.Sequence, _ = strconv.Atoi(strings.TrimSpace(c.Value("SEQUENCE")))
	e.Priority, _ = strconv.Atoi(strings.TrimSpace(c.Value("PRIORITY")))
	e.PercentComplete, _ = strconv.Atoi(strings.TrimSpace(c.Value("PERCENT-COMPLETE")))
	e.Transparent = strings.EqualFold(c.Value("TRANSP"), "TRANSPARENT")
	e.Start, _ = tz.PropTime(c.Prop("DTSTART"))
	e.End, _ = tz.PropTime(c.Prop("DTEND"))
	e.Due, _ = tz.PropTime(c.Prop("DUE"))
	if p := c.Prop("RECURRENCE-ID"); p != nil {
		e.RecurrenceID, _ = tz.PropTime(p)
		e.ThisAndFuture = strings.EqualFold(p.Param("RANGE"), "THISANDFUTURE")
	}
	if v := c.Value("DURATION"); v != "" {
		e.Duration, _ = ParseDuration(v)
	}
	if v, err := tz.PropTime(c.Prop("COMPLETED")); err == nil {
		e.Completed = v.Time
	}
	if v, err := tz.PropTime(c.Prop("DTSTAMP")); err == nil {
		e.DTStamp = v.Time
	}
	if v, err := tz.PropTime(c.Prop("LAST-MODIFIED")); err == nil {
		e.LastModified = v.Time
	}
	for _, p := range c.PropsNamed("CATEGORIES") {
		e.Categories = append(e.Categories, SplitList(p.Value)...)
	}
	for _, p := range c.PropsNamed("EXDATE") {
		if ts, err := tz.PropTimes(p); err == nil {
			e.ExDates = append(e.ExDates, ts...)
		}
	}
	for _, p := range c.PropsNamed("RDATE") {
		if ts, err := tz.PropTimes(p); err == nil {
			e.RDates = append(e.RDates, ts...)
		}
	}
	if p := c.Prop("ORGANIZER"); p != nil {
		o := readAttendee(p)
		e.Organizer = &o
	}
	for _, p := range c.PropsNamed("ATTENDEE") {
		e.Attendees = append(e.Attendees, readAttendee(p))
	}
	e.Conference = conferenceURL(c)
	for _, a := range c.ChildrenNamed("VALARM") {
		e.Alarms = append(e.Alarms, readAlarm(a, tz))
	}
	return e
}

func relatedParent(c *Component) string {
	for _, p := range c.PropsNamed("RELATED-TO") {
		if rt := strings.ToUpper(p.Param("RELTYPE")); rt == "" || rt == "PARENT" {
			return strings.TrimSpace(p.Value)
		}
	}
	return ""
}

func conferenceURL(c *Component) string {
	for _, name := range []string{"CONFERENCE", "X-GOOGLE-CONFERENCE", "X-MICROSOFT-SKYPETEAMSMEETINGURL", "X-TUXMAIL-MEETING-URL"} {
		if v := strings.TrimSpace(c.Value(name)); v != "" {
			return v
		}
	}
	return ""
}

func readAlarm(c *Component, tz *TZ) Alarm {
	a := Alarm{Action: strings.ToUpper(c.Value("ACTION")), Description: c.Text("DESCRIPTION")}
	if p := c.Prop("TRIGGER"); p != nil {
		if strings.EqualFold(p.Param("VALUE"), "DATE-TIME") {
			if v, err := tz.PropTime(p); err == nil {
				a.Absolute = v.Time
			}
		} else {
			a.Trigger, _ = ParseDuration(p.Value)
			a.Related = strings.ToUpper(p.Param("RELATED"))
		}
	}
	return a
}

// ==== Writing ==================================================================

func attendeeProp(name string, a Attendee, old *Prop) *Prop {
	p := &Prop{Name: name, Value: "mailto:" + a.Email}
	if old != nil {
		p = old.Clone()
		p.Value = "mailto:" + a.Email
	}
	if a.Name != "" {
		p.SetParam("CN", a.Name)
	} else {
		p.SetParam("CN")
	}
	if name == "ATTENDEE" {
		role := a.Role
		if role == "" {
			role = "REQ-PARTICIPANT"
		}
		p.SetParam("ROLE", role)
		ps := a.PartStat
		if ps == "" {
			ps = PartNeedsAction
		}
		p.SetParam("PARTSTAT", ps)
		if a.RSVP {
			p.SetParam("RSVP", "TRUE")
		} else {
			p.SetParam("RSVP")
		}
		if a.CUType != "" {
			p.SetParam("CUTYPE", a.CUType)
		}
	}
	return p
}

// SetAttendees replaces the ATTENDEE properties, keeping unknown parameters
// of attendees that stay.
func SetAttendees(c *Component, list []Attendee) {
	old := map[string]*Prop{}
	for _, p := range c.PropsNamed("ATTENDEE") {
		old[MailAddress(p.Value)] = p
	}
	c.Remove("ATTENDEE")
	for _, a := range list {
		a.Email = strings.ToLower(strings.TrimSpace(a.Email))
		if a.Email == "" {
			continue
		}
		c.Add(attendeeProp("ATTENDEE", a, old[a.Email]))
	}
}

// SetOrganizer sets or removes the ORGANIZER.
func SetOrganizer(c *Component, o *Attendee) {
	old := c.Prop("ORGANIZER")
	c.Remove("ORGANIZER")
	if o != nil && o.Email != "" {
		c.Add(attendeeProp("ORGANIZER", *o, old))
	}
}

// SetAlarms replaces the VALARM children.
func SetAlarms(c *Component, alarms []Alarm, summary string) {
	c.RemoveChildren(func(ch *Component) bool { return ch.Name == "VALARM" })
	for _, a := range alarms {
		al := &Component{Name: "VALARM"}
		action := a.Action
		if action == "" {
			action = "DISPLAY"
		}
		al.Set("ACTION", action)
		if !a.Absolute.IsZero() {
			al.Add(&Prop{Name: "TRIGGER", Value: a.Absolute.UTC().Format(UTCFormat), Params: []Param{{Name: "VALUE", Values: []string{"DATE-TIME"}}}})
		} else {
			tp := &Prop{Name: "TRIGGER", Value: FormatDuration(a.Trigger)}
			if a.Related == "END" {
				tp.SetParam("RELATED", "END")
			}
			al.Add(tp)
		}
		desc := a.Description
		if desc == "" {
			desc = summary
		}
		if desc == "" {
			desc = "Reminder"
		}
		al.SetText("DESCRIPTION", desc)
		c.Children = append(c.Children, al)
	}
}

// SetTime sets (or with a zero time removes) a DATE/DATE-TIME property.
func SetTime(c *Component, name string, t time.Time, allDay bool) {
	if t.IsZero() {
		c.Remove(name)
		return
	}
	p := TimeProp(name, t, allDay)
	c.Set(name, p.Value, p.Params...)
}

// SetTimeValue writes a TimeValue keeping its TZID/floating form.
func SetTimeValue(c *Component, name string, v TimeValue) {
	if v.IsZero() {
		c.Remove(name)
		return
	}
	c.Set(name, FormatValue(v), ValueParams(v)...)
}

// FormatValue formats a TimeValue in its original form.
func FormatValue(v TimeValue) string {
	switch {
	case v.AllDay:
		return v.Time.Format(DateFormat)
	case v.Floating:
		return v.Time.Format(DateTimeFormat)
	case v.TZID != "":
		return v.Time.Format(DateTimeFormat)
	}
	return v.Time.UTC().Format(UTCFormat)
}

// ValueParams returns the parameters of a TimeValue (VALUE=DATE or TZID).
func ValueParams(v TimeValue) []Param {
	switch {
	case v.AllDay:
		return []Param{{Name: "VALUE", Values: []string{"DATE"}}}
	case v.TZID != "" && !v.Floating:
		return []Param{{Name: "TZID", Values: []string{v.TZID}}}
	}
	return nil
}

// TimeValueIn converts t into a TimeValue in loc (TZID = IANA name, or UTC
// for UTC/unnamed locations).
func TimeValueIn(t time.Time, allDay bool, loc *time.Location) TimeValue {
	if allDay {
		y, m, d := t.Date()
		return TimeValue{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), AllDay: true}
	}
	if loc == nil || loc == time.UTC || loc.String() == "Local" || loc.String() == "UTC" || LookupZone(loc.String()) == nil {
		return TimeValue{Time: t.UTC()}
	}
	return TimeValue{Time: t.In(loc), TZID: loc.String()}
}

// EnsureVTimezone adds a minimal VTIMEZONE for tzid if the calendar has none
// (receivers resolve IANA TZIDs themselves; the block keeps strict parsers
// happy). The rules are the current offsets of the zone.
func EnsureVTimezone(cal *Component, tzid string, ref time.Time) {
	if tzid == "" {
		return
	}
	for _, z := range cal.ChildrenNamed("VTIMEZONE") {
		if z.Value("TZID") == tzid {
			return
		}
	}
	loc := LookupZone(tzid)
	if loc == nil {
		return
	}
	z := &Component{Name: "VTIMEZONE"}
	z.Set("TZID", tzid)
	z.Set("X-LIC-LOCATION", tzid)
	year := ref.Year()
	jan := time.Date(year, 1, 1, 12, 0, 0, 0, loc)
	jul := time.Date(year, 7, 1, 12, 0, 0, 0, loc)
	_, offJan := jan.Zone()
	_, offJul := jul.Zone()
	fmtOff := func(o int) string {
		s := "+"
		if o < 0 {
			s, o = "-", -o
		}
		return s + twoDigits(o/3600) + twoDigits(o%3600/60)
	}
	if offJan == offJul {
		st := &Component{Name: "STANDARD"}
		st.Set("DTSTART", "19700101T000000")
		st.Set("TZOFFSETFROM", fmtOff(offJan))
		st.Set("TZOFFSETTO", fmtOff(offJan))
		z.Children = append(z.Children, st)
	} else {
		std, dst := offJan, offJul
		if std > dst {
			std, dst = dst, std // southern hemisphere
		}
		// Approximate EU/US style transitions from the actual change dates.
		dstStart, dstEnd := transitions(loc, year)
		d := &Component{Name: "DAYLIGHT"}
		d.Set("DTSTART", dstStart.Format(DateTimeFormat))
		d.Set("TZOFFSETFROM", fmtOff(std))
		d.Set("TZOFFSETTO", fmtOff(dst))
		s := &Component{Name: "STANDARD"}
		s.Set("DTSTART", dstEnd.Format(DateTimeFormat))
		s.Set("TZOFFSETFROM", fmtOff(dst))
		s.Set("TZOFFSETTO", fmtOff(std))
		z.Children = append(z.Children, d, s)
	}
	// VTIMEZONEs go before the first other component.
	cal.Children = append([]*Component{z}, cal.Children...)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// transitions finds the local wall-clock times (in the offset before the
// change) of the two offset changes in year.
func transitions(loc *time.Location, year int) (toDST, toSTD time.Time) {
	t := time.Date(year, 1, 1, 0, 0, 0, 0, loc).Truncate(time.Hour)
	_, prev := t.Zone()
	type change struct {
		at  time.Time
		inc bool
	}
	var changes []change
	for h := 1; h < 367*24 && len(changes) < 2; h++ {
		n := t.Add(time.Duration(h) * time.Hour)
		if _, off := n.Zone(); off != prev {
			changes = append(changes, change{at: n.In(time.FixedZone("", prev)), inc: off > prev})
			prev = off
		}
	}
	if len(changes) < 2 {
		return t, t
	}
	if changes[0].inc {
		return changes[0].at, changes[1].at
	}
	return changes[1].at, changes[0].at
}

// NewCalendar returns an empty VCALENDAR with VERSION and PRODID.
func NewCalendar() *Component {
	c := &Component{Name: "VCALENDAR"}
	c.Set("VERSION", "2.0")
	c.Set("PRODID", "-//TuxMail//TuxMail//EN")
	return c
}
