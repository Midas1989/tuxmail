// SPDX-License-Identifier: AGPL-3.0-or-later

package rrule

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	rr "github.com/teambition/rrule-go"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// MaxOccurrences bounds the number of occurrences returned by Expand.
const MaxOccurrences = 5000

// Occurrence is one instance of a (possibly recurring) component.
type Occurrence struct {
	Start  time.Time
	End    time.Time
	AllDay bool
	// RecurrenceID is the original start of the instance (zero for
	// non-recurring components).
	RecurrenceID time.Time
	// Component is the master or the override (RECURRENCE-ID) defining the
	// instance.
	Component *ical.Component
	// Override is true when Component is a RECURRENCE-ID override.
	Override bool
}

// Key returns a stable instance key: the RECURRENCE-ID in UTC
// ("20261103T090000Z", or the date for all-day series), "" for
// non-recurring components.
func (o Occurrence) Key() string {
	if o.RecurrenceID.IsZero() {
		return ""
	}
	return RIDKey(o.RecurrenceID, o.AllDay)
}

// RIDKey formats a recurrence id as instance key.
func RIDKey(t time.Time, allDay bool) string {
	if allDay {
		return t.Format(ical.DateFormat)
	}
	return t.UTC().Format(ical.UTCFormat)
}

// ParseRIDKey parses an instance key.
func ParseRIDKey(k string) (time.Time, bool, error) {
	if len(k) == 8 {
		t, err := time.Parse(ical.DateFormat, k)
		return t, true, err
	}
	t, err := time.Parse(ical.UTCFormat, k)
	return t, false, err
}

// SplitComponents returns the master (no RECURRENCE-ID; may be nil) and the
// overrides of the components of one UID.
func SplitComponents(comps []*ical.Component) (master *ical.Component, overrides []*ical.Component) {
	for _, c := range comps {
		if c.Prop("RECURRENCE-ID") != nil {
			overrides = append(overrides, c)
		} else if master == nil {
			master = c
		}
	}
	return master, overrides
}

func sameInstant(a, b time.Time, allDay bool) bool {
	if allDay {
		ay, am, ad := a.Date()
		by, bm, bd := b.Date()
		return ay == by && am == bm && ad == bd
	}
	return a.Equal(b)
}

func overlaps(start, end, from, to time.Time) bool {
	if end.Equal(start) {
		return !start.Before(from) && start.Before(to)
	}
	return start.Before(to) && end.After(from)
}

func addLength(start time.Time, ev *ical.Event) time.Time {
	if ev.Start.AllDay {
		days := int(ev.Length() / (24 * time.Hour))
		if days < 1 {
			days = 1
		}
		return start.AddDate(0, 0, days)
	}
	return start.Add(ev.Length())
}

// Expand returns the occurrences of the components of one UID (master and
// overrides) overlapping [from, to), ordered by start. Cancelled overrides
// (STATUS:CANCELLED) and EXDATEs are skipped. All-day occurrences have
// Start/End at midnight UTC of their dates; callers compare dates for
// those. tz resolves TZIDs and floating times.
//
// Expansion is bounded (maxSteps iterator steps per series). If a series is
// too expensive, Expand degrades: the returned occurrences then contain only
// the first instance (DTSTART) of that series plus the overrides, and the
// error wraps ErrTooExpensive so that callers can log it. Callers that
// ignore ErrTooExpensive still get a usable, bounded result.
func Expand(comps []*ical.Component, tz *ical.TZ, from, to time.Time) ([]Occurrence, error) {
	return ExpandContext(context.Background(), comps, tz, from, to)
}

// ExpandContext is Expand with a context; a cancelled or expired context
// aborts the expansion of the series like ErrTooExpensive does (the context
// error is wrapped).
func ExpandContext(ctx context.Context, comps []*ical.Component, tz *ical.TZ, from, to time.Time) ([]Occurrence, error) {
	var degraded error
	master, overrides := SplitComponents(comps)
	var out []Occurrence
	type ov struct {
		c   *ical.Component
		rid ical.TimeValue
	}
	var ovs []ov
	for _, c := range overrides {
		rid, err := tz.PropTime(c.Prop("RECURRENCE-ID"))
		if err != nil {
			continue
		}
		ovs = append(ovs, ov{c, rid})
	}
	isOverridden := func(t time.Time, allDay bool) bool {
		for _, o := range ovs {
			if sameInstant(o.rid.Time, t, allDay) {
				return true
			}
		}
		return false
	}

	var exdates []ical.TimeValue
	if master != nil {
		mev := ical.ReadEvent(master, tz)
		exdates = mev.ExDates
		if mev.Start.IsZero() {
			// Tasks without DTSTART use DUE as their single instance.
			if !mev.Due.IsZero() {
				out = append(out, Occurrence{Start: mev.Due.Time, End: mev.Due.Time, AllDay: mev.Due.AllDay, Component: master})
			}
		} else if mev.RRule == "" && len(mev.RDates) == 0 {
			end := mev.EndTime()
			if overlaps(mev.Start.Time, end, from, to) {
				out = append(out, Occurrence{Start: mev.Start.Time, End: end, AllDay: mev.Start.AllDay, Component: master})
			}
		} else {
			starts, err := occurrences(ctx, &mev, from.Add(-mev.Length()-24*time.Hour), to)
			if err != nil {
				if !errors.Is(err, ErrTooExpensive) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				// Hostile or pathological rule: fall back to the first
				// instance only (RFC 5545: DTSTART is always instance one).
				degraded = err
				starts = nil
				if !mev.Start.Time.Before(from.Add(-mev.Length()-24*time.Hour)) && !mev.Start.Time.After(to) {
					starts = []time.Time{mev.Start.Time}
				}
			}
			for _, s := range starts {
				if isExcluded(s, exdates, mev.Start.AllDay) || isOverridden(s, mev.Start.AllDay) {
					continue
				}
				end := addLength(s, &mev)
				if !overlaps(s, end, from, to) {
					continue
				}
				out = append(out, Occurrence{Start: s, End: end, AllDay: mev.Start.AllDay, RecurrenceID: s, Component: master})
				if len(out) >= MaxOccurrences {
					break
				}
			}
		}
	}
	for _, o := range ovs {
		if strings.EqualFold(o.c.Value("STATUS"), "CANCELLED") || isExcluded(o.rid.Time, exdates, o.rid.AllDay) {
			continue
		}
		ev := ical.ReadEvent(o.c, tz)
		start := ev.Start
		if start.IsZero() {
			start = o.rid
		}
		end := ev.EndTime()
		if ev.End.IsZero() && ev.Duration == 0 && master != nil {
			// Overrides without end inherit the length of the master.
			mev := ical.ReadEvent(master, tz)
			end = addLength(start.Time, &mev)
		}
		if end.Before(start.Time) {
			end = start.Time
		}
		if overlaps(start.Time, end, from, to) {
			out = append(out, Occurrence{Start: start.Time, End: end, AllDay: start.AllDay, RecurrenceID: o.rid.Time,
				Component: o.c, Override: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, degraded
}

func isExcluded(t time.Time, ex []ical.TimeValue, allDay bool) bool {
	for _, e := range ex {
		if sameInstant(e.Time, t, allDay || e.AllDay) {
			return true
		}
	}
	return false
}

// occurrences returns the starts of the RRULE/RDATE set of ev within
// [after, before] (EXDATE not applied).
//
// Every iterator step counts against maxSteps, also those before the window
// (and those yielding nothing the caller keeps), so rules producing huge
// numbers of instances per period cannot run away; ErrTooExpensive is
// returned when the budget is exhausted, the context error when ctx ends.
func occurrences(ctx context.Context, ev *ical.Event, after, before time.Time) ([]time.Time, error) {
	var out []time.Time
	if ev.RRule != "" {
		rule, err := build(ev, before)
		if err != nil {
			return nil, err
		}
		if after.Before(ev.Start.Time) {
			after = ev.Start.Time
		}
		it := rule.Iterator()
		for steps := 0; ; steps++ {
			if steps >= maxSteps {
				return nil, ErrTooExpensive
			}
			if steps&0x3ff == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			t, ok := it()
			if !ok || t.After(before) {
				break
			}
			if !t.Before(after) {
				out = append(out, t)
			}
			if len(out) >= MaxOccurrences {
				break
			}
		}
		// RFC 5545: DTSTART is always the first instance, even if the
		// rule does not match it.
		if !ev.Start.Time.Before(after) && !ev.Start.Time.After(before) && !containsTime(out, ev.Start.Time) {
			out = append([]time.Time{ev.Start.Time}, out...)
		}
	} else if !ev.Start.Time.Before(after) && !ev.Start.Time.After(before) {
		out = append(out, ev.Start.Time)
	}
	for _, r := range ev.RDates {
		t := r.Time
		if r.AllDay && !ev.Start.AllDay {
			// RDATE as date for a timed series: keep the time of DTSTART.
			y, m, d := t.Date()
			st := ev.Start.Time
			t = time.Date(y, m, d, st.Hour(), st.Minute(), st.Second(), 0, st.Location())
		}
		if !t.Before(after) && !t.After(before) && !containsTime(out, t) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

func containsTime(list []time.Time, t time.Time) bool {
	for _, x := range list {
		if x.Equal(t) {
			return true
		}
	}
	return false
}

// maxPeriods bounds the number of FREQ periods between DTSTART and the end
// of the requested window: rrule-go walks every period from DTSTART (also
// those that yield no instance), so a hostile invitation such as
// "DTSTART:19000101T000000Z, FREQ=SECONDLY" or an impossible rule would
// otherwise keep a CPU busy for hours (audit B, docs/security).
const maxPeriods = 1_000_000

// maxSteps is the hard budget of iterator steps (instances produced by the
// rule, whether inside the window or not) per series. maxPeriods alone does
// not bound rules with very many instances per period such as
// "FREQ=DAILY;BYHOUR=0,...,23;BYMINUTE=0,...,59;BYSECOND=0,...,59".
const maxSteps = 1_000_000

// ErrTooExpensive is returned for rules whose expansion would be unbounded.
var ErrTooExpensive = errors.New("rrule: recurrence rule too expensive to expand")

var freqPeriod = map[rr.Frequency]time.Duration{
	rr.SECONDLY: time.Second, rr.MINUTELY: time.Minute, rr.HOURLY: time.Hour, rr.DAILY: 24 * time.Hour,
	rr.WEEKLY: 7 * 24 * time.Hour, rr.MONTHLY: 28 * 24 * time.Hour, rr.YEARLY: 365 * 24 * time.Hour,
}

// build creates the rrule-go rule of ev in the location of DTSTART. The
// iteration never runs past before (UNTIL is capped, which does not change
// the instances up to before, also with COUNT).
func build(ev *ical.Event, before time.Time) (*rr.RRule, error) {
	loc := ev.Start.Time.Location()
	opt, err := rr.StrToROptionInLocation(ev.RRule, loc)
	if err != nil {
		return nil, err
	}
	opt.Dtstart = ev.Start.Time
	if !opt.Until.IsZero() && ev.Start.AllDay {
		// A DATE UNTIL is inclusive for the whole day.
		y, m, d := opt.Until.Date()
		opt.Until = time.Date(y, m, d, 23, 59, 59, 0, time.UTC)
	} else if r, err := Parse(ev.RRule); err == nil && r.Until != "" && !strings.HasSuffix(strings.ToUpper(r.Until), "Z") {
		// Floating/local UNTIL: interpret in the series' zone.
		u := opt.Until
		opt.Until = time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), 0, loc)
	}
	if opt.Interval <= 0 {
		opt.Interval = 1
	}
	if !before.IsZero() {
		if opt.Until.IsZero() || opt.Until.After(before) {
			opt.Until = before
		}
		if p, ok := freqPeriod[opt.Freq]; ok && before.After(opt.Dtstart) {
			if before.Sub(opt.Dtstart)/p/time.Duration(opt.Interval) > maxPeriods {
				return nil, ErrTooExpensive
			}
		}
	}
	r, err := rr.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ErrNotRecurring is returned for edits that need a recurring series.
var ErrNotRecurring = errors.New("rrule: component is not recurring")

// IndexOf returns the 0-based index of the occurrence starting at rid in the
// series of ev (for COUNT adjustments), or -1.
func IndexOf(ev *ical.Event, rid time.Time) int {
	if ev.RRule == "" {
		return -1
	}
	starts, err := occurrences(context.Background(), ev, ev.Start.Time, rid.Add(time.Second))
	if err != nil {
		return -1
	}
	for i, s := range starts {
		if sameInstant(s, rid, ev.Start.AllDay) {
			return i
		}
	}
	return -1
}
