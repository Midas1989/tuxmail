// SPDX-License-Identifier: AGPL-3.0-or-later

package query

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ResolveDate resolves a date expression to the interval [start, end) it
// denotes, in loc (nil = UTC):
//
//   - absolute: 2026-03-01, 2026/03/01, 01.03.2026, 1.3.2026 (one day),
//     2026-03 (one month), 2026 (one year), RFC 3339 (an instant: start = end);
//   - relative instants: now, now-7d, now+1d with units s, m (minutes), h, d,
//     w, M (months), y;
//   - names (English/German, case-insensitive): today/heute,
//     yesterday/gestern, tomorrow/morgen, "this week"/"diese Woche",
//     "last week"/"letzte Woche"/"vorige Woche", "this month"/"diesen Monat",
//     "last month"/"letzten Monat", "this year"/"dieses Jahr",
//     "last year"/"letztes Jahr". Weeks start on Monday.
func ResolveDate(v string, now time.Time, loc *time.Location) (start, end time.Time, err error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	s := strings.ToLower(strings.Join(strings.Fields(v), " "))
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc) }
	today := day(now)
	week := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	year := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc)
	switch s {
	case "today", "heute":
		return today, today.AddDate(0, 0, 1), nil
	case "yesterday", "gestern":
		return today.AddDate(0, 0, -1), today, nil
	case "tomorrow", "morgen":
		return today.AddDate(0, 0, 1), today.AddDate(0, 0, 2), nil
	case "this week", "diese woche":
		return week, week.AddDate(0, 0, 7), nil
	case "last week", "letzte woche", "vorige woche", "letzten woche":
		return week.AddDate(0, 0, -7), week, nil
	case "this month", "diesen monat", "dieser monat", "diesem monat":
		return month, month.AddDate(0, 1, 0), nil
	case "last month", "letzten monat", "letzter monat", "vorigen monat", "letztem monat":
		return month.AddDate(0, -1, 0), month, nil
	case "this year", "dieses jahr", "diesem jahr":
		return year, year.AddDate(1, 0, 0), nil
	case "last year", "letztes jahr", "letzten jahr", "voriges jahr", "letztem jahr":
		return year.AddDate(-1, 0, 0), year, nil
	}
	if strings.HasPrefix(s, "now") {
		t, err := relative(now, v[strings.Index(strings.ToLower(v), "now")+3:])
		return t, t, err
	}
	for _, l := range []struct {
		layout string
		years  int
		months int
		days   int
	}{
		{"2006-01-02", 0, 0, 1}, {"2006/01/02", 0, 0, 1}, {"02.01.2006", 0, 0, 1}, {"2.1.2006", 0, 0, 1},
		{"2006-01", 0, 1, 0}, {"2006/01", 0, 1, 0}, {"2006", 1, 0, 0},
	} {
		if t, err := time.ParseInLocation(l.layout, v, loc); err == nil {
			return t, t.AddDate(l.years, l.months, l.days), nil
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, t, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q", v)
}

// relative applies offsets like "-7d+2h" to now.
func relative(now time.Time, rest string) (time.Time, error) {
	t := now
	for rest != "" {
		sign := 1
		switch rest[0] {
		case '+':
		case '-':
			sign = -1
		default:
			return t, fmt.Errorf("invalid relative date near %q", rest)
		}
		i := 1
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 1 || i >= len(rest) {
			return t, fmt.Errorf("invalid relative date near %q", rest)
		}
		n, err := strconv.Atoi(rest[1:i])
		if err != nil || n > 100000 {
			return t, fmt.Errorf("invalid relative date near %q", rest)
		}
		n *= sign
		switch rest[i] {
		case 's':
			t = t.Add(time.Duration(n) * time.Second)
		case 'm':
			t = t.Add(time.Duration(n) * time.Minute)
		case 'h', 'H':
			t = t.Add(time.Duration(n) * time.Hour)
		case 'd':
			t = t.AddDate(0, 0, n)
		case 'w':
			t = t.AddDate(0, 0, 7*n)
		case 'M':
			t = t.AddDate(0, n, 0)
		case 'y':
			t = t.AddDate(n, 0, 0)
		default:
			return t, fmt.Errorf("unknown time unit %q (use s, m, h, d, w, M, y)", rest[i])
		}
		rest = rest[i+1:]
	}
	return t, nil
}

// ParseSize parses a number with an optional binary size unit (B, K/KB/KiB,
// M/MB/MiB, G/GB/GiB; case-insensitive), e.g. "10MB" or "1.5k".
func ParseSize(v string) (int64, error) {
	s := strings.ToLower(v)
	mult := 1.0
	for _, u := range []struct {
		suffix string
		mult   float64
	}{
		{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30},
		{"kb", 1 << 10}, {"mb", 1 << 20}, {"gb", 1 << 30},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}, {"b", 1},
	} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f*mult > math.MaxInt64/2 {
		return 0, errors.New("invalid number " + strconv.Quote(v))
	}
	return int64(f * mult), nil
}
