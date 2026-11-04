// SPDX-License-Identifier: AGPL-3.0-or-later

package ical

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "time/tzdata" // embedded IANA time zone database (KONZEPT 9.1)
)

// Wire formats.
const (
	DateFormat     = "20060102"
	DateTimeFormat = "20060102T150405"
	UTCFormat      = "20060102T150405Z"
)

// TimeValue is a parsed DATE or DATE-TIME value.
type TimeValue struct {
	Time time.Time
	// AllDay is set for DATE values; Time is midnight UTC of that date.
	AllDay bool
	// Floating marks local times without zone (Time is in the resolver's
	// default location).
	Floating bool
	// TZID is the original TZID parameter ("" for UTC, floating and dates).
	TZID string
}

// IsZero reports whether no value is set.
func (v TimeValue) IsZero() bool { return v.Time.IsZero() }

// Date returns the calendar date (all-day values: the stored date).
func (v TimeValue) Date() (year int, month time.Month, day int) { return v.Time.Date() }

// TZ resolves TZIDs to locations using the VTIMEZONE components of a
// calendar, the embedded IANA database and a table of Windows zone names.
// The zero value resolves IANA names and uses UTC as default.
type TZ struct {
	// Default is the location of floating times (default UTC).
	Default *time.Location
	zones   map[string]*Component
	mu      sync.Mutex
	cache   map[string]*time.Location
}

// NewTZ returns a resolver for the VTIMEZONEs of cal (may be nil).
func NewTZ(cal *Component, def *time.Location) *TZ {
	tz := &TZ{Default: def, zones: map[string]*Component{}}
	if cal != nil {
		for _, z := range cal.ChildrenNamed("VTIMEZONE") {
			tz.zones[z.Value("TZID")] = z
		}
	}
	return tz
}

func (tz *TZ) def() *time.Location {
	if tz == nil || tz.Default == nil {
		return time.UTC
	}
	return tz.Default
}

var globalCache sync.Map // tzid -> *time.Location (IANA/Windows lookups)

// Location returns the location of a TZID (never nil).
func (tz *TZ) Location(tzid string) *time.Location {
	if tzid == "" {
		return tz.def()
	}
	if tz != nil {
		tz.mu.Lock()
		if l, ok := tz.cache[tzid]; ok {
			tz.mu.Unlock()
			return l
		}
		tz.mu.Unlock()
	}
	loc := LookupZone(tzid)
	if loc == nil && tz != nil {
		if z := tz.zones[tzid]; z != nil {
			if x := z.Value("X-LIC-LOCATION"); x != "" {
				loc = LookupZone(x)
			}
			if loc == nil {
				loc = fixedFromVTimezone(tzid, z)
			}
		}
	}
	if loc == nil {
		loc = tz.def()
	}
	if tz != nil {
		tz.mu.Lock()
		if tz.cache == nil {
			tz.cache = map[string]*time.Location{}
		}
		tz.cache[tzid] = loc
		tz.mu.Unlock()
	}
	return loc
}

// LookupZone maps a TZID to an IANA location: IANA names, prefixed names
// ("/mozilla.org/20050126_1/Europe/Berlin", "/softwarestudio.org/...") and
// common Windows names. It returns nil if nothing matches.
func LookupZone(tzid string) *time.Location {
	tzid = strings.Trim(strings.TrimSpace(tzid), `"`)
	if tzid == "" {
		return nil
	}
	if v, ok := globalCache.Load(tzid); ok {
		return v.(*time.Location)
	}
	var loc *time.Location
	try := func(name string) bool {
		if name == "" || strings.EqualFold(name, "local") {
			return false
		}
		if l, err := time.LoadLocation(name); err == nil {
			loc = l
			return true
		}
		return false
	}
	switch {
	case try(tzid):
	case try(windowsZones[tzid]):
	default:
		// Strip vendor prefixes: keep the last two (or three) segments.
		segs := strings.Split(strings.Trim(tzid, "/"), "/")
		for n := 3; n >= 1 && loc == nil; n-- {
			if len(segs) >= n {
				try(strings.Join(segs[len(segs)-n:], "/"))
			}
		}
	}
	if loc == nil && strings.EqualFold(tzid, "Z") {
		loc = time.UTC
	}
	if loc != nil {
		globalCache.Store(tzid, loc)
	}
	return loc
}

// fixedFromVTimezone approximates an unknown VTIMEZONE with a fixed zone at
// its STANDARD offset (DST rules of custom zones are not evaluated).
func fixedFromVTimezone(tzid string, z *Component) *time.Location {
	for _, name := range []string{"STANDARD", "DAYLIGHT"} {
		if c := z.Child(name); c != nil {
			if off, ok := ParseUTCOffset(c.Value("TZOFFSETTO")); ok {
				return time.FixedZone(tzid, off)
			}
		}
	}
	return nil
}

// ParseUTCOffset parses "+0100" / "-053000" into seconds east of UTC.
func ParseUTCOffset(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 5 && len(s) != 7 {
		return 0, false
	}
	sign := 1
	switch s[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return 0, false
	}
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[3:5])
	sec := 0
	var err3 error
	if len(s) == 7 {
		sec, err3 = strconv.Atoi(s[5:7])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	return sign * (h*3600 + m*60 + sec), true
}

// ParseTime parses a DATE or DATE-TIME value with the given TZID.
func (tz *TZ) ParseTime(value, tzid string, isDate bool) (TimeValue, error) {
	value = strings.TrimSpace(value)
	if len(value) == 8 || isDate {
		if len(value) < 8 {
			return TimeValue{}, fmt.Errorf("%w: invalid date %q", ErrSyntax, value)
		}
		t, err := time.ParseInLocation(DateFormat, value[:8], time.UTC)
		if err != nil {
			return TimeValue{}, fmt.Errorf("%w: invalid date %q", ErrSyntax, value)
		}
		return TimeValue{Time: t, AllDay: true}, nil
	}
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse(UTCFormat, value)
		if err != nil {
			return TimeValue{}, fmt.Errorf("%w: invalid date-time %q", ErrSyntax, value)
		}
		return TimeValue{Time: t}, nil
	}
	if tzid != "" {
		loc := tz.Location(tzid)
		t, err := time.ParseInLocation(DateTimeFormat, value, loc)
		if err != nil {
			return TimeValue{}, fmt.Errorf("%w: invalid date-time %q", ErrSyntax, value)
		}
		return TimeValue{Time: t, TZID: tzid}, nil
	}
	t, err := time.ParseInLocation(DateTimeFormat, value, tz.def())
	if err != nil {
		return TimeValue{}, fmt.Errorf("%w: invalid date-time %q", ErrSyntax, value)
	}
	return TimeValue{Time: t, Floating: true}, nil
}

// PropTime parses a DATE/DATE-TIME property (DTSTART, DTEND, DUE ...).
func (tz *TZ) PropTime(p *Prop) (TimeValue, error) {
	if p == nil {
		return TimeValue{}, nil
	}
	return tz.ParseTime(p.Value, p.Param("TZID"), strings.EqualFold(p.Param("VALUE"), "DATE"))
}

// PropTimes parses a list property (EXDATE, RDATE) into values.
func (tz *TZ) PropTimes(p *Prop) ([]TimeValue, error) {
	var out []TimeValue
	if strings.EqualFold(p.Param("VALUE"), "PERIOD") {
		for _, v := range strings.Split(p.Value, ",") {
			start, _, _ := strings.Cut(v, "/")
			t, err := tz.ParseTime(start, p.Param("TZID"), false)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	}
	for _, v := range strings.Split(p.Value, ",") {
		if strings.TrimSpace(v) == "" {
			continue
		}
		t, err := tz.ParseTime(v, p.Param("TZID"), strings.EqualFold(p.Param("VALUE"), "DATE"))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// TimeProp builds a DATE/DATE-TIME property. All-day values are written as
// VALUE=DATE; times with a named IANA location get a TZID; UTC and other
// times are written in UTC.
func TimeProp(name string, t time.Time, allDay bool) *Prop {
	if allDay {
		return &Prop{Name: name, Value: t.Format(DateFormat), Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}}
	}
	if tzid := t.Location().String(); t.Location() != time.UTC && tzid != "Local" && tzid != "" && LookupZone(tzid) != nil {
		return &Prop{Name: name, Value: t.Format(DateTimeFormat), Params: []Param{{Name: "TZID", Values: []string{tzid}}}}
	}
	return &Prop{Name: name, Value: t.UTC().Format(UTCFormat)}
}

// FormatTimeValue formats t like TimeProp for list properties (EXDATE).
func FormatTimeValue(t time.Time, allDay bool) string {
	return TimeProp("X", t, allDay).Value
}

// ==== Durations ================================================================

// ParseDuration parses an RFC 5545 duration ("PT15M", "-P1D", "P1W").
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("%w: invalid duration %q", ErrSyntax, s)
	}
	s = s[1:]
	var d time.Duration
	inTime := false
	num := ""
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'T':
			inTime = true
		default:
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, fmt.Errorf("%w: invalid duration %q", ErrSyntax, s)
			}
			num = ""
			switch {
			case r == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				d += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				d += time.Duration(n) * time.Second
			default:
				return 0, fmt.Errorf("%w: invalid duration %q", ErrSyntax, s)
			}
		}
	}
	if num != "" {
		return 0, fmt.Errorf("%w: invalid duration %q", ErrSyntax, s)
	}
	if neg {
		d = -d
	}
	return d, nil
}

// FormatDuration formats d as an RFC 5545 duration.
func FormatDuration(d time.Duration) string {
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	b.WriteByte('P')
	if d == 0 {
		return b.String() + "T0S"
	}
	if d%(7*24*time.Hour) == 0 {
		fmt.Fprintf(&b, "%dW", d/(7*24*time.Hour))
		return b.String()
	}
	if days := d / (24 * time.Hour); days > 0 {
		fmt.Fprintf(&b, "%dD", days)
		d -= days * 24 * time.Hour
	}
	if d > 0 {
		b.WriteByte('T')
		if h := d / time.Hour; h > 0 {
			fmt.Fprintf(&b, "%dH", h)
			d -= h * time.Hour
		}
		if m := d / time.Minute; m > 0 {
			fmt.Fprintf(&b, "%dM", m)
			d -= m * time.Minute
		}
		if s := d / time.Second; s > 0 {
			fmt.Fprintf(&b, "%dS", s)
		}
	}
	return b.String()
}

// windowsZones maps common Windows time zone names (Outlook/Exchange) to
// IANA zones (subset of CLDR windowsZones.xml).
var windowsZones = map[string]string{
	"Dateline Standard Time":          "Etc/GMT+12",
	"UTC-11":                          "Etc/GMT+11",
	"Hawaiian Standard Time":          "Pacific/Honolulu",
	"Alaskan Standard Time":           "America/Anchorage",
	"Pacific Standard Time":           "America/Los_Angeles",
	"US Mountain Standard Time":       "America/Phoenix",
	"Mountain Standard Time":          "America/Denver",
	"Central Standard Time":           "America/Chicago",
	"Central America Standard Time":   "America/Guatemala",
	"Eastern Standard Time":           "America/New_York",
	"Atlantic Standard Time":          "America/Halifax",
	"Newfoundland Standard Time":      "America/St_Johns",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Argentina Standard Time":         "America/Buenos_Aires",
	"UTC":                             "Etc/UTC",
	"Coordinated Universal Time":      "Etc/UTC",
	"GMT Standard Time":               "Europe/London",
	"Greenwich Standard Time":         "Atlantic/Reykjavik",
	"W. Europe Standard Time":         "Europe/Berlin",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Romance Standard Time":           "Europe/Paris",
	"Central European Standard Time":  "Europe/Warsaw",
	"W. Central Africa Standard Time": "Africa/Lagos",
	"GTB Standard Time":               "Europe/Bucharest",
	"E. Europe Standard Time":         "Europe/Chisinau",
	"FLE Standard Time":               "Europe/Kiev",
	"Israel Standard Time":            "Asia/Jerusalem",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"Turkey Standard Time":            "Europe/Istanbul",
	"Russian Standard Time":           "Europe/Moscow",
	"Arab Standard Time":              "Asia/Riyadh",
	"Arabian Standard Time":           "Asia/Dubai",
	"Iran Standard Time":              "Asia/Tehran",
	"Pakistan Standard Time":          "Asia/Karachi",
	"India Standard Time":             "Asia/Calcutta",
	"Nepal Standard Time":             "Asia/Katmandu",
	"Bangladesh Standard Time":        "Asia/Dhaka",
	"SE Asia Standard Time":           "Asia/Bangkok",
	"China Standard Time":             "Asia/Shanghai",
	"Singapore Standard Time":         "Asia/Singapore",
	"Taipei Standard Time":            "Asia/Taipei",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Korea Standard Time":             "Asia/Seoul",
	"AUS Central Standard Time":       "Australia/Darwin",
	"E. Australia Standard Time":      "Australia/Brisbane",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"New Zealand Standard Time":       "Pacific/Auckland",
}
