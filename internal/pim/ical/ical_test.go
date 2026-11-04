// SPDX-License-Identifier: AGPL-3.0-or-later

package ical

import (
	"strings"
	"testing"
	"time"
)

const sample = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nX-WR-CALNAME:Work\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:W. Europe Standard Time\r\nBEGIN:STANDARD\r\nDTSTART:16010101T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Custom Zone\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0530\r\nTZOFFSETTO:+0530\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:42@example.com\r\nDTSTAMP:20261101T120000Z\r\n" +
	"DTSTART;TZID=W. Europe Standard Time:20261105T100000\r\nDTEND;TZID=Custom Zone:20261105T150000\r\n" +
	"SUMMARY:Planung\\, Teil 1\\; Raum \"A\"\r\nDESCRIPTION:Zeile 1\\nZeile 2 mit sehr langem Text der gefaltet werden muss \r\n weil er über 75 Oktette hinausgeht – äöü\r\n" +
	"ORGANIZER;CN=\"Müller, Anna\":mailto:anna@example.com\r\n" +
	"ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED;X-NUM-GUESTS=0:MAILTO:Bob@Example.com\r\n" +
	"X-ALT-DESC;FMTTYPE=text/html:<p>hi</p>\r\nCATEGORIES:Work,Plan\\,ning\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Reminder\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseRoundTrip(t *testing.T) {
	c, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	out := c.Encode()
	c2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Encode() != out {
		t.Fatal("encoding is not stable")
	}
	for _, line := range strings.Split(out, "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line longer than 75 octets: %q", line)
		}
	}
	if !strings.Contains(out, "X-ALT-DESC;FMTTYPE=text/html:<p>hi</p>") || !strings.Contains(out, "X-NUM-GUESTS=0") {
		t.Fatal("unknown properties/parameters lost:\n" + out)
	}
	ev := ReadEvent(c.Child("VEVENT"), NewTZ(c, time.UTC))
	if ev.Summary != `Planung, Teil 1; Raum "A"` {
		t.Fatalf("summary %q", ev.Summary)
	}
	if !strings.Contains(ev.Description, "Zeile 1\nZeile 2") || !strings.HasSuffix(ev.Description, "äöü") {
		t.Fatalf("description %q", ev.Description)
	}
	if ev.Start.Time.UTC().Format(time.RFC3339) != "2026-11-05T09:00:00Z" {
		t.Fatalf("windows zone: %v", ev.Start.Time)
	}
	if ev.End.Time.UTC().Format(time.RFC3339) != "2026-11-05T09:30:00Z" {
		t.Fatalf("custom zone: %v", ev.End.Time)
	}
	if ev.Organizer == nil || ev.Organizer.Name != "Müller, Anna" || ev.Attendees[0].Email != "bob@example.com" || ev.Attendees[0].PartStat != "ACCEPTED" {
		t.Fatalf("attendees %+v %+v", ev.Organizer, ev.Attendees)
	}
	if len(ev.Categories) != 2 || ev.Categories[1] != "Plan,ning" {
		t.Fatalf("categories %q", ev.Categories)
	}
	if len(ev.Alarms) != 1 || ev.Alarms[0].Trigger != -15*time.Minute {
		t.Fatalf("alarms %+v", ev.Alarms)
	}
	// Organizer CN with a comma must be quoted on output.
	if !strings.Contains(out, `ORGANIZER;CN="Müller, Anna":mailto:anna@example.com`) {
		t.Fatal(out)
	}
}

func TestFoldingKeepsUTF8(t *testing.T) {
	c := &Component{Name: "VEVENT"}
	c.SetText("SUMMARY", strings.Repeat("ä", 100))
	out := c.Encode()
	for _, l := range strings.Split(out, "\r\n") {
		if !utf8Valid(strings.TrimPrefix(l, " ")) {
			t.Fatalf("broken UTF-8 in %q", l)
		}
	}
	c2, _ := Parse(out)
	if c2.Text("SUMMARY") != strings.Repeat("ä", 100) {
		t.Fatal("unfold mismatch")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestDurationsAndTimes(t *testing.T) {
	for in, want := range map[string]time.Duration{"PT15M": 15 * time.Minute, "-P1D": -24 * time.Hour, "P1W": 7 * 24 * time.Hour,
		"P1DT2H3M4S": 26*time.Hour + 3*time.Minute + 4*time.Second, "+PT0S": 0} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Fatalf("%s: %v %v", in, got, err)
		}
	}
	if FormatDuration(-15*time.Minute) != "-PT15M" || FormatDuration(48*time.Hour) != "P2D" || FormatDuration(time.Hour+30*time.Second) != "PT1H30S" {
		t.Fatal("format duration")
	}
	if _, err := ParseDuration("PX"); err == nil {
		t.Fatal("bad duration accepted")
	}
	tz := NewTZ(nil, time.UTC)
	for _, tzid := range []string{"Europe/Berlin", "/mozilla.org/20050126_1/Europe/Berlin", "W. Europe Standard Time"} {
		v, err := tz.ParseTime("20260701T120000", tzid, false)
		if err != nil || v.Time.UTC().Hour() != 10 {
			t.Fatalf("%s: %v %v", tzid, v.Time, err)
		}
	}
	v, _ := tz.ParseTime("20261224", "", true)
	if !v.AllDay || TimeProp("DTSTART", v.Time, true).Line() != "DTSTART;VALUE=DATE:20261224" {
		t.Fatal("date prop")
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	if TimeProp("DTSTART", time.Date(2026, 1, 2, 9, 0, 0, 0, berlin), false).Line() != "DTSTART;TZID=Europe/Berlin:20260102T090000" {
		t.Fatal("tzid prop")
	}
}

func TestVTimezoneGeneration(t *testing.T) {
	cal := NewCalendar()
	EnsureVTimezone(cal, "Europe/Berlin", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	z := cal.Child("VTIMEZONE")
	if z == nil || z.Child("DAYLIGHT").Value("DTSTART") != "20260329T020000" || z.Child("STANDARD").Value("DTSTART") != "20261025T030000" {
		t.Fatalf("vtimezone:\n%s", cal.Encode())
	}
	if z.Child("DAYLIGHT").Value("TZOFFSETTO") != "+0200" {
		t.Fatal(cal.Encode())
	}
}

func TestVCard21QuotedPrintable(t *testing.T) {
	in := "BEGIN:VCARD\r\nVERSION:2.1\r\nN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:M=C3=BCller;Anna=\r\n;;;\r\nTEL;HOME;VOICE:+49 30 1234\r\nEND:VCARD\r\n"
	c, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if n := c.Prop("N"); n == nil || n.Value != "M=C3=BCller;Anna;;;" {
		t.Fatalf("qp join: %+v", n)
	}
	tel := c.Prop("TEL")
	if !tel.HasParamValue("TYPE", "home") || !tel.HasParamValue("TYPE", "voice") {
		t.Fatalf("bare params: %+v", tel.Params)
	}
}
