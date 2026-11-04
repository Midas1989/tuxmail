// SPDX-License-Identifier: AGPL-3.0-or-later

package rrule

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

func cal(t *testing.T, body string) []*ical.Component {
	t.Helper()
	c, err := ical.Parse("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\r\n") + "\r\nEND:VCALENDAR\r\n")
	if err != nil {
		t.Fatal(err)
	}
	return c.ChildrenNamed("VEVENT")
}

func starts(occ []Occurrence, loc *time.Location, layout string) []string {
	var out []string
	for _, o := range occ {
		out = append(out, o.Start.In(loc).Format(layout))
	}
	return out
}

func TestExpand(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	ny, _ := time.LoadLocation("America/New_York")
	tests := []struct {
		name     string
		body     string
		from, to time.Time
		loc      *time.Location
		layout   string
		want     []string
	}{
		{
			name: "weekly across DST keeps wall clock (Berlin)",
			body: `BEGIN:VEVENT
UID:a
DTSTART;TZID=Europe/Berlin:20261019T100000
DTEND;TZID=Europe/Berlin:20261019T110000
RRULE:FREQ=WEEKLY;COUNT=3
END:VEVENT`,
			from: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			loc: berlin, layout: "2006-01-02 15:04 MST",
			want: []string{"2026-10-19 10:00 CEST", "2026-10-26 10:00 CET", "2026-11-02 10:00 CET"},
		},
		{
			name: "daily across US spring forward",
			body: `BEGIN:VEVENT
UID:b
DTSTART;TZID=America/New_York:20260306T090000
DURATION:PT30M
RRULE:FREQ=DAILY;UNTIL=20260309T140000Z
END:VEVENT`,
			from: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			loc: ny, layout: "01-02 15:04",
			want: []string{"03-06 09:00", "03-07 09:00", "03-08 09:00", "03-09 09:00"},
		},
		{
			name: "EXDATE and override (moved instance)",
			body: `BEGIN:VEVENT
UID:c
DTSTART:20261102T080000Z
DTEND:20261102T090000Z
RRULE:FREQ=DAILY;COUNT=5
EXDATE:20261103T080000Z
END:VEVENT
BEGIN:VEVENT
UID:c
RECURRENCE-ID:20261104T080000Z
DTSTART:20261104T150000Z
DTEND:20261104T160000Z
SUMMARY:moved
END:VEVENT`,
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "02 15:04",
			want: []string{"02 08:00", "04 15:00", "05 08:00", "06 08:00"},
		},
		{
			name: "cancelled override and RDATE",
			body: `BEGIN:VEVENT
UID:d
DTSTART:20261102T080000Z
DTEND:20261102T090000Z
RRULE:FREQ=WEEKLY;COUNT=2
RDATE:20261120T120000Z
END:VEVENT
BEGIN:VEVENT
UID:d
RECURRENCE-ID:20261109T080000Z
STATUS:CANCELLED
END:VEVENT`,
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "02 15:04",
			want: []string{"02 08:00", "20 12:00"},
		},
		{
			name: "all-day yearly with date UNTIL",
			body: `BEGIN:VEVENT
UID:e
DTSTART;VALUE=DATE:20240229
DTEND;VALUE=DATE:20240301
RRULE:FREQ=YEARLY;UNTIL=20290301
END:VEVENT`,
			from: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "2006-01-02",
			want: []string{"2024-02-29", "2028-02-29"},
		},
		{
			name: "monthly last friday (BYSETPOS)",
			body: `BEGIN:VEVENT
UID:f
DTSTART:20261030T100000Z
RRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3
END:VEVENT`,
			from: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "2006-01-02",
			want: []string{"2026-10-30", "2026-11-30", "2026-12-31"},
		},
		{
			name: "window clips and long events overlap from before",
			body: `BEGIN:VEVENT
UID:g
DTSTART:20261101T220000Z
DTEND:20261102T020000Z
RRULE:FREQ=DAILY
END:VEVENT`,
			from: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "02 15:04",
			want: []string{"04 22:00", "05 22:00"},
		},
		{
			name: "DTSTART not matching rule is first instance",
			body: `BEGIN:VEVENT
UID:h
DTSTART:20261103T100000Z
RRULE:FREQ=WEEKLY;BYDAY=FR;COUNT=2
END:VEVENT`,
			from: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			loc: time.UTC, layout: "01-02",
			want: []string{"11-03", "11-06", "11-13"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			occ, err := Expand(cal(t, tc.body), ical.NewTZ(nil, time.UTC), tc.from, tc.to)
			if err != nil {
				t.Fatal(err)
			}
			got := starts(occ, tc.loc, tc.layout)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpandDSTDurationsAndKeys(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	comps := cal(t, `BEGIN:VEVENT
UID:x
DTSTART;TZID=Europe/Berlin:20260328T090000
DTEND;TZID=Europe/Berlin:20260328T100000
RRULE:FREQ=DAILY;COUNT=2
END:VEVENT`)
	occ, err := Expand(comps, ical.NewTZ(nil, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(occ) != 2 {
		t.Fatal(err, occ)
	}
	if got := occ[1].End.In(berlin).Format("15:04"); got != "10:00" {
		t.Fatalf("end after DST change = %s", got)
	}
	if occ[0].Key() != "20260328T080000Z" || occ[1].Key() != "20260329T070000Z" {
		t.Fatalf("keys %s %s", occ[0].Key(), occ[1].Key())
	}
}

func TestRuleRoundTripAndSplit(t *testing.T) {
	for _, s := range []string{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;WKST=MO", "FREQ=MONTHLY;COUNT=10;BYDAY=-1FR", "FREQ=YEARLY;BYMONTHDAY=1;BYMONTH=3;BYHOUR=9"} {
		r, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if r.String() != s {
			t.Fatalf("round trip %q -> %q", s, r.String())
		}
	}
	got, err := EndBefore("FREQ=DAILY;COUNT=10", time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC), false)
	if err != nil || got != "FREQ=DAILY;UNTIL=20261105T075959Z" {
		t.Fatal(got, err)
	}
	if got := RemainingCount("FREQ=DAILY;COUNT=10", 3); got != "FREQ=DAILY;COUNT=7" {
		t.Fatal(got)
	}
	if _, err := Parse("FREQ=SOMETIMES"); err == nil {
		t.Fatal("invalid freq accepted")
	}
	ev := ical.ReadEvent(cal(t, "BEGIN:VEVENT\nUID:i\nDTSTART:20261102T080000Z\nRRULE:FREQ=DAILY;COUNT=10\nEND:VEVENT")[0], ical.NewTZ(nil, time.UTC))
	if i := IndexOf(&ev, time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)); i != 3 {
		t.Fatal("index", i)
	}
}
