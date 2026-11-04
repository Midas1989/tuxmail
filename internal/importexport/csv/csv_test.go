// SPDX-License-Identifier: AGPL-3.0-or-later

package csv

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/emersion/go-vcard"
	"golang.org/x/text/encoding/charmap"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
	"github.com/Midas1989/Mailclient/internal/importexport/vcf"
)

func readFile(t *testing.T, p string) *File {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Read(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPresets(t *testing.T) {
	for _, tc := range []struct {
		file, preset, name, email string
	}{
		{"../testdata/outlook.csv", "outlook", "Max Muster", "max@example.org"},
		{"../testdata/google.csv", "google", "Jane Roe", "jane@example.org"},
		{"../testdata/thunderbird.csv", "thunderbird", "John Doe", "john@example.com"},
	} {
		f := readFile(t, tc.file)
		preset, mapping := Suggest(f.Header)
		if preset != tc.preset {
			t.Fatalf("%s: preset %q", tc.file, preset)
		}
		cards := f.Cards(mapping)
		if len(cards) == 0 || cards[0].Value(vcard.FieldFormattedName) != tc.name || cards[0].PreferredValue(vcard.FieldEmail) != tc.email {
			t.Fatalf("%s: %v", tc.file, cards)
		}
	}
	f := readFile(t, "../testdata/outlook.csv")
	_, m := Suggest(f.Header)
	c := f.Cards(m)[0]
	if len(vcf.Emails(c)) != 2 || c.Value(vcard.FieldNote) != "Note, with comma" || c.Value(vcard.FieldOrganization) != "Example AG" {
		t.Fatalf("outlook details: %v", c)
	}
}

func TestEncodingsAndRoundTrip(t *testing.T) {
	latin, _ := charmap.Windows1252.NewEncoder().String("Vorname;Nachname;E-Mail-Adresse\nJürgen;Müller;j@example.org\n")
	f, err := Read(bytes.NewReader([]byte(latin)))
	if err != nil {
		t.Fatal(err)
	}
	_, m := Suggest(f.Header)
	cards := f.Cards(m)
	if len(cards) != 1 || cards[0].Value(vcard.FieldFormattedName) != "Jürgen Müller" {
		t.Fatalf("latin1: %v", cards)
	}
	for _, p := range []Preset{Outlook, Google} {
		var b bytes.Buffer
		if err := Write(&b, cards, p); err != nil {
			t.Fatal(err)
		}
		back, err := Read(&b)
		if err != nil {
			t.Fatal(err)
		}
		id, m := Suggest(back.Header)
		if id != p.ID {
			t.Fatalf("export %s detected as %s", p.ID, id)
		}
		got := back.Cards(m)
		if len(got) != 1 || got[0].PreferredValue(vcard.FieldEmail) != "j@example.org" || got[0].Name().FamilyName != "Müller" {
			t.Fatalf("%s round trip: %v", p.ID, got)
		}
	}
	if _, err := Read(bytes.NewReader([]byte("single"))); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
}
