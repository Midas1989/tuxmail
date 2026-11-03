// SPDX-License-Identifier: AGPL-3.0-or-later

package ldif

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func TestParseAndRoundTrip(t *testing.T) {
	f, err := os.Open("../testdata/contacts.ldif")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cards, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("%d cards (lists must be skipped)", len(cards))
	}
	c := cards[0]
	if c.Value(vcard.FieldFormattedName) != "Max Muster" || c.PreferredValue(vcard.FieldEmail) != "max@example.org" || c.Value(vcard.FieldNote) != "Müller Ärger" {
		t.Fatalf("%v", c)
	}
	var b bytes.Buffer
	if err := Write(&b, cards); err != nil {
		t.Fatal(err)
	}
	back, err := Parse(&b)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Value(vcard.FieldNote) != "Müller Ärger" || back[0].Name().GivenName != "Max" {
		t.Fatalf("round trip: %v\n%s", back, b.String())
	}
}

func TestCorrupt(t *testing.T) {
	for _, in := range []string{"", "no colon here\n", "cn: x\n", "dn: cn=x\ncn:: !!!\n"} {
		if _, err := Parse(bytes.NewReader([]byte(in))); !errors.Is(err, ie.ErrCorrupt) {
			t.Fatalf("%q: %v", in, err)
		}
	}
}
