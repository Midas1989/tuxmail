// SPDX-License-Identifier: AGPL-3.0-or-later

package vcard

import (
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

const v3 = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:u1\r\nFN:Anna Müller\r\nN:Müller;Anna;;Dr.;\r\nORG:ACME GmbH;Vertrieb\r\n" +
	"EMAIL;TYPE=INTERNET,WORK,pref:anna@acme.example\r\nitem1.EMAIL;TYPE=INTERNET:anna@home.example\r\nitem1.X-ABLabel:_$!<Other>!$_\r\n" +
	"TEL;TYPE=CELL:0171 1234567\r\nADR;TYPE=WORK:;;Hauptstr. 1;Berlin;;10115;Germany\r\nBDAY:1604-03-01\r\n" +
	"X-SOCIALPROFILE;TYPE=twitter:https://x.com/anna\r\nIMPP:xmpp:anna@jabber.example\r\nCATEGORIES:Kunden,VIP\r\n" +
	"X-CUSTOM-THING:keep me\r\nPHOTO;ENCODING=b;TYPE=JPEG:/9j/AA==\r\nEND:VCARD\r\n"

func TestReadWriteRoundTrip(t *testing.T) {
	cards, err := Parse(v3)
	if err != nil {
		t.Fatal(err)
	}
	c := Read(cards[0])
	if c.FN != "Anna Müller" || c.N.Prefix != "Dr." || c.Org != "ACME GmbH" || c.Department != "Vertrieb" {
		t.Fatalf("%+v", c)
	}
	if len(c.Emails) != 2 || !c.Emails[0].Pref || c.Emails[0].Type != "work" || c.Emails[1].Label != "Other" {
		t.Fatalf("emails %+v", c.Emails)
	}
	if c.Birthday != "1604-03-01" || len(c.Photo) != 4 || c.PhotoType != "image/jpeg" || len(c.Categories) != 2 {
		t.Fatalf("%+v", c)
	}
	c.Title = "Head of Sales"
	Write(cards[0], c)
	out := cards[0].Encode()
	if !strings.Contains(out, "X-CUSTOM-THING:keep me") || !strings.Contains(out, "TITLE:Head of Sales") {
		t.Fatal(out)
	}
	again, _ := Parse(out)
	c2 := Read(again[0])
	c2.Version, c.Version = "", ""
	if c2.FN != c.FN || len(c2.Emails) != 2 || c2.Emails[1].Label != "Other" || c2.Title != c.Title || len(c2.Photo) != 4 || len(c2.Addresses) != 1 {
		t.Fatalf("round trip lost data:\n%s\n%+v", out, c2)
	}
	if strings.Count(out, "X-ABLabel")+strings.Count(out, "X-ABLABEL") != 1 {
		t.Fatalf("duplicate labels:\n%s", out)
	}
}

func TestVersionsAndDates(t *testing.T) {
	in := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:u2\r\nFN:Bob\r\nBDAY:--0315\r\nANNIVERSARY:20100612\r\nPHOTO:data:image/png;base64,iVBORw==\r\nTEL;VALUE=uri;TYPE=home:tel:+49-30-123456\r\nEND:VCARD\r\n"
	cards, _ := Parse(in)
	c := Read(cards[0])
	if c.Birthday != "--03-15" || c.Anniversary != "2010-06-12" || c.PhotoType != "image/png" || c.Phones[0].Value != "+49-30-123456" {
		t.Fatalf("%+v", c)
	}
	v3 := Convert(cards[0], "3.0")
	out := v3.Encode()
	if !strings.Contains(out, "BDAY;X-APPLE-OMIT-YEAR=1604:1604-03-15") || !strings.Contains(out, "PHOTO;ENCODING=b;TYPE=PNG:") {
		t.Fatal(out)
	}
	if Read(v3).Birthday != "--03-15" {
		t.Fatal("year-less birthday lost")
	}
	v21 := "BEGIN:VCARD\r\nVERSION:2.1\r\nN;CHARSET=UTF-8;ENCODING=QUOTED-PRINTABLE:M=C3=BCller;J=C3=B6rg\r\nTEL;WORK;VOICE:030/123\r\nEND:VCARD\r\n"
	cards, _ = Parse(v21)
	c = Read(cards[0])
	if c.N.Family != "Müller" || c.N.Given != "Jörg" || c.FN != "Jörg Müller" || c.Phones[0].Type != "work" {
		t.Fatalf("2.1: %+v", c)
	}
}

func TestDuplicatesAndMerge(t *testing.T) {
	list := []Contact{
		{FN: "Anna Müller", Emails: []Value{{Value: "Anna@acme.example"}}},
		{FN: "Müller, Anna", Phones: []Value{{Value: "+49 171 1234567"}}},
		{FN: "Someone", Phones: []Value{{Value: "0171 1234567"}}, Emails: []Value{{Value: "x@y.example"}}},
		{FN: "Bob Other", Emails: []Value{{Value: "bob@example.com"}}},
	}
	groups := Groups(list, "DE")
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups %v", groups)
	}
	m := Merge("DE", list[0], list[1], list[2])
	if len(m.Emails) != 2 || len(m.Phones) != 1 || m.FN != "Anna Müller" {
		t.Fatalf("merge %+v", m)
	}
	if NormalizePhone("0171 1234567", "DE") != "+491711234567" || NormalizePhone("0049 30 123456", "") != "+4930123456" {
		t.Fatal(NormalizePhone("0171 1234567", "DE"), NormalizePhone("0049 30 123456", ""))
	}
	if NameKey("Jörg  Müller") != NameKey("muller, jorg") || NameKey("Single") != "" {
		t.Fatal("name key")
	}
	_ = ical.EscapeText
}
