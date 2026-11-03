// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestDetectForwardsFixtures(t *testing.T) {
	utc := func(y int, m time.Month, d, hh, mm, ss int) time.Time {
		return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
	}
	tests := []struct {
		file     string
		marker   ForwardMarker
		from     string
		fromName string
		subject  string
		date     time.Time
		hasZone  bool
		to       []string
	}{
		{"gmail_en.txt", MarkerGmail, "jane.doe@example.com", "Jane Doe", "Re: Q1 budget review", utc(2024, 1, 15, 10, 30, 0), false, []string{"bob@example.org", "finance@example.org"}},
		{"gmail_de.txt", MarkerGerman, "rechnung@beispiel-gmbh.de", "Buchhaltung Beispiel GmbH", "Ihre Rechnung 2024-001", utc(2024, 1, 15, 10, 30, 0), false, []string{"max.mustermann@beispiel.de"}},
		{"thunderbird_de.txt", MarkerGerman, "anna.schmidt@firma.de", "Anna Schmidt", "Projekt Phoenix: Statusbericht KW 3", utc(2024, 1, 16, 8, 12, 45), true, []string{"team@firma.de"}},
		{"apple_en.txt", MarkerApple, "john.appleseed@icloud.com", "John Appleseed", "Dinner on Friday", utc(2024, 1, 12, 18, 45, 12), true, []string{"kate@example.com"}},
		{"apple_de.txt", MarkerApple, "klaus.weber@web.de", "Klaus Weber", "Termin Montag", utc(2024, 1, 12, 18, 45, 12), true, []string{"petra.lang@gmx.de"}},
		{"outlook_en.txt", MarkerOutlook, "sarah.connor@contoso.com", "Sarah Connor", "Release schedule for 2.0", utc(2024, 1, 17, 15, 15, 0), false, []string{"team-alpha@contoso.com", "tom@contoso.com", "john@contoso.com"}},
		{"outlook_de.txt", MarkerOriginal, "thomas.mueller@firma.de", "Müller, Thomas", "WG: Angebot 4711", utc(2024, 1, 17, 15, 15, 0), false, nil},
		{"outlook_legacy_en.txt", MarkerOriginal, "max.mustermann@example.com", "Mustermann, Max", "RE: Server maintenance window", utc(2024, 1, 18, 8, 5, 0), false, nil},
		{"outlook_bare_de.txt", MarkerOutlook, "s.koch@stadtwerke-beispiel.de", "Sabine Koch", "Zählerstand Wohnung 3", utc(2024, 1, 19, 11, 47, 0), false, []string{"info@hausverwaltung.de"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", "forwards", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got := DetectForwards(string(body))
			if len(got) != 1 {
				t.Fatalf("got %d blocks: %+v", len(got), got)
			}
			fh := got[0]
			if fh.Marker != tt.marker {
				t.Errorf("Marker = %q, want %q", fh.Marker, tt.marker)
			}
			if fh.From.Email != tt.from || fh.From.Name != tt.fromName {
				t.Errorf("From = %+v, want %q <%s>", fh.From, tt.fromName, tt.from)
			}
			if fh.Subject != tt.subject {
				t.Errorf("Subject = %q, want %q", fh.Subject, tt.subject)
			}
			if !fh.Date.Equal(tt.date) || fh.DateHasZone != tt.hasZone {
				t.Errorf("Date = %v (zone %v) from %q, want %v (zone %v)", fh.Date, fh.DateHasZone, fh.DateRaw, tt.date, tt.hasZone)
			}
			if tt.to != nil {
				var to []string
				for _, a := range fh.To {
					to = append(to, a.Email)
				}
				if !reflect.DeepEqual(to, tt.to) {
					t.Errorf("To = %q, want %q", to, tt.to)
				}
			}
		})
	}
}

func TestDetectForwardsNegative(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "forwards", "no_forward.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := DetectForwards(string(body)); len(got) != 0 {
		t.Fatalf("expected no forwards, got %+v", got)
	}
	for _, s := range []string{"", "\n\n", "---------- Forwarded message ---------", "From: a@b.c\n"} {
		if got := DetectForwards(s); len(got) != 0 {
			t.Errorf("DetectForwards(%q) = %+v", s, got)
		}
	}
}

func TestDetectForwardsNested(t *testing.T) {
	body := "FYI\n\n---------- Forwarded message ---------\nFrom: A <a@x.de>\nDate: Mon, Jan 15, 2024 at 10:30 AM\nSubject: Fwd: Plan\nTo: b@x.de\n\nsee below\n\n---------- Forwarded message ---------\nFrom: C <c@x.de>\nDate: Sun, Jan 14, 2024 at 9:00 AM\nSubject: Plan\nTo: a@x.de\n\nthe plan\n"
	got := DetectForwards(body)
	if len(got) != 2 {
		t.Fatalf("got %d blocks", len(got))
	}
	if got[0].From.Email != "a@x.de" || got[1].From.Email != "c@x.de" || got[1].Subject != "Plan" {
		t.Errorf("unexpected blocks %+v", got)
	}
	if got[0].Line != 2 {
		t.Errorf("Line = %d", got[0].Line)
	}
}

func TestDetectForwardsMessageIDAndCRLF(t *testing.T) {
	body := "-------- Forwarded Message --------\r\nSubject: Test\r\nDate: Tue, 16 Jan 2024 09:12:45 +0100\r\nFrom: X <x@y.de>\r\nMessage-ID: <ID@Y.DE>\r\n\r\nbody"
	got := DetectForwards(body)
	if len(got) != 1 || got[0].MessageID != "ID@y.de" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseAddressList(t *testing.T) {
	tests := []struct {
		in   string
		want []domain.Address
	}{
		{"Max <max@x.de>", []domain.Address{{Name: "Max", Email: "max@x.de"}}},
		{"max@X.de", []domain.Address{{Email: "max@x.de"}}},
		{"Mustermann, Max [mailto:Max@x.de]", []domain.Address{{Name: "Mustermann, Max", Email: "max@x.de"}}},
		{"Müller, Thomas <t@x.de>; Schulz, Petra <p@x.de>", []domain.Address{{Name: "Müller, Thomas", Email: "t@x.de"}, {Name: "Schulz, Petra", Email: "p@x.de"}}},
		{"Doe, Jane", []domain.Address{{Name: "Doe, Jane"}}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := parseAddressList(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseAddressList(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestMatchForward(t *testing.T) {
	d := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	cands := []Candidate{
		{Key: "wrong-sender@x", From: "other@example.com", Subject: "Q1 budget review", Date: d},
		{Key: "far@x", From: "jane.doe@example.com", Subject: "Q1 budget review", Date: d.Add(-72 * time.Hour)},
		{Key: "match@x", From: "Jane.Doe@example.com", Subject: "AW: Q1 budget review", Date: d.Add(-1 * time.Hour)},
		{Key: "closer@x", From: "jane.doe@example.com", Subject: "Q1 budget review", Date: d.Add(-30 * time.Minute)},
	}
	fh := ForwardedHeader{From: domain.Address{Email: "jane.doe@example.com"}, Subject: "Re: Q1 budget review", Date: d}
	got, ok := MatchForward(fh, cands)
	if !ok || got.Key != "closer@x" {
		t.Fatalf("MatchForward = %+v, %v", got, ok)
	}

	fh.DateHasZone = true
	if _, ok := MatchForward(fh, cands); ok {
		t.Fatal("with a known zone the tolerance is 5 minutes")
	}
	fh.Date = d.Add(-30*time.Minute + 2*time.Minute)
	if got, ok := MatchForward(fh, cands); !ok || got.Key != "closer@x" {
		t.Fatalf("MatchForward = %+v, %v", got, ok)
	}

	// Only one criterion (subject) is not enough.
	if _, ok := MatchForward(ForwardedHeader{Subject: "Q1 budget review"}, cands); ok {
		t.Fatal("single criterion must not match")
	}
	// Message-ID wins.
	if got, ok := MatchForward(ForwardedHeader{MessageID: "far@x"}, cands); !ok || got.Key != "far@x" {
		t.Fatal("message-id match")
	}
}
