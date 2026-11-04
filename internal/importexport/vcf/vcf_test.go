// SPDX-License-Identifier: AGPL-3.0-or-later

package vcf

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func parseFile(t *testing.T, p string) []vcard.Card {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cards, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func TestVersions(t *testing.T) {
	c21 := parseFile(t, "../testdata/contacts21.vcf")
	if len(c21) != 1 {
		t.Fatal(len(c21))
	}
	c := c21[0]
	if c.Value(vcard.FieldFormattedName) != "Max Muster" || c.PreferredValue(vcard.FieldEmail) != "max@example.org" {
		t.Fatalf("2.1: %v", c)
	}
	if tel := c.Get(vcard.FieldTelephone); tel == nil || !tel.Params.HasType("home") {
		t.Fatalf("2.1 TEL params: %+v", tel)
	}
	if n := c.Value(vcard.FieldNote); n != "Zeile eins\nZeile zwei Müller" {
		t.Fatalf("2.1 QP note: %q", n)
	}
	if c.Value(vcard.FieldUID) == "" {
		t.Fatal("UID must be generated")
	}
	c3 := parseFile(t, "../testdata/contacts3.vcf")
	if len(c3) != 2 || len(Emails(c3[0])) != 2 {
		t.Fatalf("3.0: %d cards", len(c3))
	}
	c4 := parseFile(t, "../testdata/contacts4.vcf")
	if len(c4) != 1 || c4[0].PreferredValue(vcard.FieldEmail) != "jane@example.org" {
		t.Fatal("4.0")
	}
}

func TestRoundTrip(t *testing.T) {
	in := parseFile(t, "../testdata/contacts3.vcf")
	for _, v := range []Version{V3, V4} {
		var b bytes.Buffer
		if err := Write(&b, in, v); err != nil {
			t.Fatal(err)
		}
		out, err := Parse(&b)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != len(in) {
			t.Fatalf("%s: %d cards", v, len(out))
		}
		for i := range in {
			if out[i].Value(vcard.FieldVersion) != string(v) || out[i].Value(vcard.FieldUID) != in[i].Value(vcard.FieldUID) ||
				out[i].Value(vcard.FieldFormattedName) != in[i].Value(vcard.FieldFormattedName) ||
				len(Emails(out[i])) != len(Emails(in[i])) || out[i].PreferredValue(vcard.FieldEmail) != in[i].PreferredValue(vcard.FieldEmail) {
				t.Fatalf("%s card %d differs: %v", v, i, out[i])
			}
		}
	}
}

func TestCorrupt(t *testing.T) {
	f, _ := os.Open("../testdata/corrupt.vcf")
	defer f.Close()
	if _, err := Parse(f); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
	if _, err := Parse(bytes.NewReader([]byte("hello"))); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
}
