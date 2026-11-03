// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rrule expands recurring iCalendar components (RRULE, RDATE,
// EXDATE and RECURRENCE-ID overrides, RFC 5545 3.8.5) on top of
// github.com/teambition/rrule-go, and provides the structured form of an
// RRULE used by the recurrence editor plus helpers for "this and following"
// edits. Occurrences are computed in the time zone of DTSTART, so wall-clock
// times are kept across DST changes.
package rrule

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// Rule is the structured form of an RRULE value. Parts the editor does not
// know (BYHOUR, BYWEEKNO ...) are kept in Extra and written back unchanged.
type Rule struct {
	Freq       string   `json:"freq"` // DAILY, WEEKLY, MONTHLY, YEARLY (also SECONDLY ... HOURLY)
	Interval   int      `json:"interval,omitempty"`
	Count      int      `json:"count,omitempty"`
	Until      string   `json:"until,omitempty"` // raw UNTIL value (date or UTC date-time)
	ByDay      []string `json:"byDay,omitempty"` // "MO", "1MO", "-1FR"
	ByMonthDay []int    `json:"byMonthDay,omitempty"`
	ByMonth    []int    `json:"byMonth,omitempty"`
	BySetPos   []int    `json:"bySetPos,omitempty"`
	WeekStart  string   `json:"weekStart,omitempty"`
	Extra      []string `json:"extra,omitempty"` // "KEY=VALUE" parts not covered above
}

// Parse parses an RRULE value ("FREQ=WEEKLY;BYDAY=MO,WE").
func Parse(s string) (Rule, error) {
	var r Rule
	s = strings.TrimPrefix(strings.TrimSpace(s), "RRULE:")
	for _, part := range strings.Split(s, ";") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return Rule{}, fmt.Errorf("rrule: invalid part %q", part)
		}
		k = strings.ToUpper(k)
		switch k {
		case "FREQ":
			r.Freq = strings.ToUpper(v)
		case "INTERVAL":
			r.Interval, _ = strconv.Atoi(v)
		case "COUNT":
			r.Count, _ = strconv.Atoi(v)
		case "UNTIL":
			r.Until = v
		case "BYDAY":
			for _, d := range strings.Split(strings.ToUpper(v), ",") {
				if d = strings.TrimSpace(d); d != "" {
					r.ByDay = append(r.ByDay, strings.TrimPrefix(d, "+"))
				}
			}
		case "BYMONTHDAY":
			r.ByMonthDay = ints(v)
		case "BYMONTH":
			r.ByMonth = ints(v)
		case "BYSETPOS":
			r.BySetPos = ints(v)
		case "WKST":
			r.WeekStart = strings.ToUpper(v)
		default:
			r.Extra = append(r.Extra, k+"="+v)
		}
	}
	switch r.Freq {
	case "SECONDLY", "MINUTELY", "HOURLY", "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return Rule{}, fmt.Errorf("rrule: invalid FREQ %q", r.Freq)
	}
	return r, nil
}

func ints(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

// String formats the rule as RRULE value.
func (r Rule) String() string {
	parts := []string{"FREQ=" + r.Freq}
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if r.Count > 0 {
		parts = append(parts, "COUNT="+strconv.Itoa(r.Count))
	} else if r.Until != "" {
		parts = append(parts, "UNTIL="+r.Until)
	}
	if len(r.ByDay) > 0 {
		parts = append(parts, "BYDAY="+strings.Join(r.ByDay, ","))
	}
	if len(r.ByMonthDay) > 0 {
		parts = append(parts, "BYMONTHDAY="+joinInts(r.ByMonthDay))
	}
	if len(r.ByMonth) > 0 {
		parts = append(parts, "BYMONTH="+joinInts(r.ByMonth))
	}
	if len(r.BySetPos) > 0 {
		parts = append(parts, "BYSETPOS="+joinInts(r.BySetPos))
	}
	if r.WeekStart != "" {
		parts = append(parts, "WKST="+r.WeekStart)
	}
	parts = append(parts, r.Extra...)
	return strings.Join(parts, ";")
}

// UntilValue formats the UNTIL of a rule ending at last (inclusive): a date
// for all-day series, otherwise UTC.
func UntilValue(last time.Time, allDay bool) string {
	if allDay {
		return last.Format(ical.DateFormat)
	}
	return last.UTC().Format(ical.UTCFormat)
}

// EndBefore returns rule s ending just before the occurrence at rid (used
// to split a series for "this and following" edits). For COUNT rules the
// count is replaced by UNTIL.
func EndBefore(s string, rid time.Time, allDay bool) (string, error) {
	r, err := Parse(s)
	if err != nil {
		return "", err
	}
	r.Count = 0
	if allDay {
		r.Until = UntilValue(rid.AddDate(0, 0, -1), true)
	} else {
		r.Until = UntilValue(rid.Add(-time.Second), false)
	}
	return r.String(), nil
}

// RemainingCount adjusts a COUNT rule for a series that starts at the
// occurrence index skipped (0-based): it returns the rule with COUNT
// reduced, or s unchanged for rules without COUNT.
func RemainingCount(s string, skipped int) string {
	r, err := Parse(s)
	if err != nil || r.Count == 0 {
		return s
	}
	r.Count -= skipped
	if r.Count < 1 {
		r.Count = 1
	}
	return r.String()
}

// Weekday codes in RRULE order.
var weekdayCodes = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// WeekdayCode returns the BYDAY code of a weekday.
func WeekdayCode(d time.Weekday) string { return weekdayCodes[d] }

// IsWeekdayCode reports whether c is a valid BYDAY entry.
func IsWeekdayCode(c string) bool {
	c = strings.TrimLeft(c, "+-0123456789")
	return slices.Contains(weekdayCodes, c)
}
