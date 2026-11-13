// SPDX-License-Identifier: AGPL-3.0-or-later

package pimapi

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// L-9: CR/LF in names, e-mail, URL, RRULE and text must not inject
// properties or components into the generated iCalendar.
func TestEventICalNoInjection(t *testing.T) {
	evil := "x\r\nURL:https://evil.example/\r\nBEGIN:VALARM\r\nACTION:EMAIL\r\nEND:VALARM\r\nX-A:"
	ev := Event{
		Summary:   "S" + evil,
		Location:  "L\rURL:https://evil.example/",
		Start:     time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
		RRule:     "FREQ=DAILY\r\nURL:https://evil.example/",
		OnlineURL: "https://ok.example/\r\nURL:https://evil.example/",
		Organizer: &Attendee{Name: evil, Email: "o@example.com\r\nURL:https://evil.example/"},
		Attendees: []Attendee{
			{Name: evil, Email: "a@example.com"},
			{Name: "Bob \"B\" ^x", Email: "b@example.com\nBEGIN:VALARM"},
		},
	}
	out := EventICal("uid-1", ev)
	root, err := ical.Parse(out)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	for _, c := range root.Children {
		if len(c.Children) != 0 {
			t.Fatalf("injected sub-component %s in %s:\n%s", c.Children[0].Name, c.Name, out)
		}
		for _, p := range c.Props {
			if p.Name == "URL" && strings.Contains(p.Value, "evil") {
				t.Fatalf("injected URL property:\n%s", out)
			}
			if p.Name == "ACTION" && p.Value == "EMAIL" {
				t.Fatalf("injected ACTION:\n%s", out)
			}
		}
	}
	// The attendee with the hostile e-mail is dropped, the other one stays
	// with its name round-tripped.
	vev := root.Children[0]
	as := vev.PropsNamed("ATTENDEE")
	if len(as) != 1 {
		t.Fatalf("attendees = %d\n%s", len(as), out)
	}
	if !strings.Contains(as[0].Param("CN"), "URL:https://evil.example/") {
		t.Fatalf("CN not preserved as data: %q", as[0].Param("CN"))
	}
}

func TestContactVCardNoInjection(t *testing.T) {
	out := ContactVCard(Contact{ID: "1", DisplayName: "A\rEND:VCARD\rBEGIN:VCARD", Emails: []string{"a@example.com\r\nTEL:1"}})
	n := 0
	for _, l := range strings.Split(out, "\r\n") {
		if l == "BEGIN:VCARD" {
			n++
		}
		if strings.HasPrefix(l, "TEL:") {
			t.Fatalf("injected TEL:\n%q", out)
		}
	}
	if n != 1 {
		t.Fatalf("injection:\n%q", out)
	}
}
