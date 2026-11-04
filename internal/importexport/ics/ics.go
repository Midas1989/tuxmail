// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ics imports and exports iCalendar (RFC 5545) files with
// github.com/emersion/go-ical (KONZEPT 13.2).
//
// Parse splits a file into Objects: one per UID, holding the master
// component, its overrides (RECURRENCE-ID) and the VTIMEZONEs they use as a
// self-contained VCALENDAR. That is the unit stored per calendar object.
// The CalendarImporter/CalendarExporter interfaces decouple the format from
// the storage; internal/app implements them on the store (the PIM module
// may replace that with its richer model later).
package ics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// ProdID is written into exported calendars.
const ProdID = "-//TuxMail//TuxMail Import Export//EN"

// Object kinds.
const (
	KindEvent   = "event"
	KindTodo    = "todo"
	KindJournal = "journal"
)

// Object is one calendar object (all components sharing a UID).
type Object struct {
	UID     string
	Kind    string
	Summary string
	Start   time.Time
	End     time.Time
	Due     time.Time
	RRule   string
	Status  string
	// ICal is a complete VCALENDAR with this object only.
	ICal string
}

// DedupeMode decides what happens when the UID already exists.
type DedupeMode string

const (
	Skip      DedupeMode = "skip"
	Overwrite DedupeMode = "overwrite"
	Copy      DedupeMode = "copy" // import with a new UID
)

// Result counts an import.
type Result struct {
	Created     int `json:"created"`
	Updated     int `json:"updated"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
	Duplicates  int `json:"duplicates"`
	TotalParsed int `json:"total"`
}

// CalendarImporter stores parsed objects in a calendar.
type CalendarImporter interface {
	ImportObjects(ctx context.Context, calendarID int64, objs []Object, mode DedupeMode) (Result, error)
}

// CalendarExporter lists objects for export (calendarIDs empty: all;
// zero times: no range limit).
type CalendarExporter interface {
	ExportObjects(ctx context.Context, calendarIDs []int64, from, to time.Time) ([]Object, error)
}

// MaxSize limits the input of Parse.
const MaxSize = 64 << 20

var kinds = map[string]string{ical.CompEvent: KindEvent, ical.CompToDo: KindTodo, ical.CompJournal: KindJournal}

// Parse reads one or more concatenated VCALENDARs.
func Parse(r io.Reader) ([]Object, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("ics: input larger than %d bytes", MaxSize)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.Contains(bytes.ToUpper(data[:min(len(data), 4096)]), []byte("BEGIN:VCALENDAR")) {
		return nil, ie.Corrupt("no BEGIN:VCALENDAR found")
	}
	dec := ical.NewDecoder(bytes.NewReader(data))
	type group struct {
		kind  string
		comps []*ical.Component
	}
	groups := map[string]*group{}
	var order []string
	tzs := map[string]*ical.Component{}
	for {
		cal, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(order) > 0 {
				break // trailing garbage after valid calendars
			}
			return nil, ie.Corrupt("ics: %v", err)
		}
		for _, c := range cal.Children {
			if c.Name == ical.CompTimezone {
				if id := propValue(c, ical.PropTimezoneID); id != "" {
					tzs[id] = c
				}
				continue
			}
			kind, ok := kinds[c.Name]
			if !ok {
				continue
			}
			uid := propValue(c, ical.PropUID)
			if uid == "" {
				uid = NewUID()
				c.Props.SetText(ical.PropUID, uid)
			}
			g := groups[uid]
			if g == nil {
				g = &group{kind: kind}
				groups[uid] = g
				order = append(order, uid)
			}
			g.comps = append(g.comps, c)
		}
	}
	if len(order) == 0 {
		return nil, ie.Corrupt("ics: no events, tasks or journal entries")
	}
	out := make([]Object, 0, len(order))
	for _, uid := range order {
		g := groups[uid]
		o, err := build(uid, g.kind, g.comps, tzs)
		if err != nil {
			return out, ie.Corrupt("ics: %s: %v", uid, err)
		}
		out = append(out, o)
	}
	return out, nil
}

func propValue(c *ical.Component, name string) string {
	if p := c.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

func propTime(c *ical.Component, name string) time.Time {
	p := c.Props.Get(name)
	if p == nil {
		return time.Time{}
	}
	t, err := p.DateTime(time.UTC)
	if err != nil {
		// Unknown TZID (e.g. Windows zone names): read as floating UTC.
		cp := *p
		cp.Params = ical.Params{}
		if v := p.Params.Get(ical.ParamValue); v != "" {
			cp.Params.Set(ical.ParamValue, v)
		}
		t, _ = cp.DateTime(time.UTC)
	}
	return t
}

func build(uid, kind string, comps []*ical.Component, tzs map[string]*ical.Component) (Object, error) {
	// The master is the component without RECURRENCE-ID.
	sort.SliceStable(comps, func(i, j int) bool {
		return comps[i].Props.Get(ical.PropRecurrenceID) == nil && comps[j].Props.Get(ical.PropRecurrenceID) != nil
	})
	m := comps[0]
	o := Object{UID: uid, Kind: kind, Summary: propValue(m, ical.PropSummary), RRule: propValue(m, ical.PropRecurrenceRule),
		Status: strings.ToUpper(propValue(m, ical.PropStatus)), Start: propTime(m, ical.PropDateTimeStart)}
	o.End = propTime(m, ical.PropDateTimeEnd)
	if o.End.IsZero() && !o.Start.IsZero() {
		if p := m.Props.Get(ical.PropDuration); p != nil {
			if d, err := p.Duration(); err == nil {
				o.End = o.Start.Add(d)
			}
		}
		if o.End.IsZero() {
			o.End = o.Start
		}
	}
	o.Due = propTime(m, ical.PropDue)
	cal := newCalendar()
	used := map[string]bool{}
	for _, c := range comps {
		ensureStamp(c)
		for _, ps := range c.Props {
			for _, p := range ps {
				if id := p.Params.Get(ical.ParamTimezoneID); id != "" {
					used[id] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if tz := tzs[id]; tz != nil {
			cal.Children = append(cal.Children, tz)
		}
	}
	cal.Children = append(cal.Children, comps...)
	var b bytes.Buffer
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		return o, err
	}
	o.ICal = b.String()
	return o, nil
}

func newCalendar() *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, ProdID)
	return cal
}

func ensureStamp(c *ical.Component) {
	if c.Props.Get(ical.PropDateTimeStamp) == nil {
		c.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	}
}

// NewUID returns a random UID.
func NewUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@tuxmail"
}

// WithNewUID returns a copy of o with a new UID (DedupeMode Copy).
func WithNewUID(o Object) (Object, error) {
	cal, err := ical.NewDecoder(strings.NewReader(o.ICal)).Decode()
	if err != nil {
		return o, err
	}
	uid := NewUID()
	for _, c := range cal.Children {
		if _, ok := kinds[c.Name]; ok {
			c.Props.SetText(ical.PropUID, uid)
		}
	}
	var b bytes.Buffer
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		return o, err
	}
	o.UID, o.ICal = uid, b.String()
	return o, nil
}

// InRange reports whether an object overlaps [from, to) (zero bounds are
// open). Recurring objects are included when they start before to.
func InRange(o Object, from, to time.Time) bool {
	start := o.Start
	if start.IsZero() {
		start = o.Due
	}
	if start.IsZero() {
		return true
	}
	end := o.End
	if end.Before(start) {
		end = start
	}
	if !to.IsZero() && !start.Before(to) {
		return false
	}
	if o.RRule != "" {
		return true
	}
	return from.IsZero() || !end.Before(from)
}

// Write merges objects into one VCALENDAR (time zones deduplicated).
func Write(w io.Writer, objs []Object, name string) error {
	cal := newCalendar()
	if name != "" {
		cal.Props.SetText("X-WR-CALNAME", name)
	}
	tzSeen := map[string]bool{}
	var tzs, comps []*ical.Component
	for _, o := range objs {
		c, err := ical.NewDecoder(strings.NewReader(o.ICal)).Decode()
		if err != nil {
			continue
		}
		for _, ch := range c.Children {
			if ch.Name == ical.CompTimezone {
				id := propValue(ch, ical.PropTimezoneID)
				if !tzSeen[id] {
					tzSeen[id] = true
					tzs = append(tzs, ch)
				}
				continue
			}
			ensureStamp(ch)
			comps = append(comps, ch)
		}
	}
	if len(comps) == 0 {
		// go-ical refuses empty calendars; write a minimal one by hand.
		_, err := io.WriteString(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:"+ProdID+"\r\nEND:VCALENDAR\r\n")
		return err
	}
	cal.Children = append(tzs, comps...)
	return ical.NewEncoder(w).Encode(cal)
}
