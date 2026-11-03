// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"strings"
	"testing"
)

func TestKeywordRoundTrip(t *testing.T) {
	m := DefaultKeywordMapper
	cases := []struct {
		tag     string
		keyword string // "" = only check validity and round trip
	}{
		{"Rechnung", "Rechnung"},
		{"Projekt/Kunde-A", "Projekt/Kunde-A"},
		{"Important", "$Label1"},
		{"Later", "$Label5"},
		{"To Do", "$Label4"},
		{"Büro", "B&APw-ro"},
		{"a b", "a&ACA-b"},
		{"R&D", "R&-D"},
		{"$Label1", ""},
		{"$Forwarded", ""},
		{"Junk", ""},
		{"NonJunk", ""},
		{`back\slash`, ""},
		{`(paren) {brace} "q" 100% * ]`, ""},
		{"日本語/タグ", ""},
		{"emoji 🎉 tag", ""},
		{"x,y;z", ""},
	}
	for _, c := range cases {
		kw, err := m.ToKeyword(c.tag)
		if err != nil {
			t.Fatalf("ToKeyword(%q): %v", c.tag, err)
		}
		if c.keyword != "" && kw != c.keyword {
			t.Errorf("ToKeyword(%q) = %q, want %q", c.tag, kw, c.keyword)
		}
		if !IsValidKeyword(kw) {
			t.Errorf("ToKeyword(%q) = %q is not a valid IMAP atom", c.tag, kw)
		}
		back, ok := m.FromKeyword(kw)
		if !ok || !strings.EqualFold(back, c.tag) {
			t.Errorf("FromKeyword(ToKeyword(%q) = %q) = %q, %v", c.tag, kw, back, ok)
		}
	}
	// German label names.
	de := KeywordMapper{LabelNames: GermanLabelNames}
	if kw, _ := de.ToKeyword("wichtig"); kw != "$Label1" {
		t.Errorf("german Wichtig -> %q", kw)
	}
	if tag, ok := de.FromKeyword("$label3"); !ok || tag != "Persönlich" {
		t.Errorf("german $label3 -> %q %v", tag, ok)
	}
}

func TestFromKeywordNonTags(t *testing.T) {
	m := DefaultKeywordMapper
	for _, kw := range []string{`\Seen`, `\Flagged`, "$Forwarded", "$MDNSent", "$Junk", "NonJunk", "$NotJunk", "$MailFlagBit1", ""} {
		if tag, ok := m.FromKeyword(kw); ok {
			t.Errorf("FromKeyword(%q) = %q, want no tag", kw, tag)
		}
	}
	// Foreign keywords that are not valid modified UTF-7 are taken verbatim.
	if tag, ok := m.FromKeyword("todo&later"); !ok || tag != "todo&later" {
		t.Errorf("verbatim keyword: %q %v", tag, ok)
	}
	if tag, ok := m.FromKeyword("work_item"); !ok || tag != "work_item" {
		t.Errorf("plain keyword: %q %v", tag, ok)
	}
}

func TestModifiedUTF7(t *testing.T) {
	for _, s := range []string{"", "INBOX", "Entwürfe", "a&b", "日本語", "Ünïcödé/Sub folder", "🎉"} {
		enc := EncodeModifiedUTF7(s)
		dec, err := DecodeModifiedUTF7(enc)
		if err != nil || dec != s {
			t.Errorf("round trip %q -> %q -> %q, %v", s, enc, dec, err)
		}
	}
	if got := EncodeModifiedUTF7("Entwürfe"); got != "Entw&APw-rfe" {
		t.Errorf("Entwürfe -> %q", got)
	}
	for _, bad := range []string{"&", "&AB", "&!!-", "ü"} {
		if _, err := DecodeModifiedUTF7(bad); err == nil {
			t.Errorf("DecodeModifiedUTF7(%q) succeeded", bad)
		}
	}
}

func TestGmailLabels(t *testing.T) {
	cases := []struct {
		label string
		tag   string
		ok    bool
	}{
		{"Projekt/Kunde-A", "Projekt/Kunde-A", true},
		{"  Rechnungen ", "Rechnungen", true},
		{`\Inbox`, "", false},
		{`\Important`, "", false},
		{"INBOX", "", false},
		{"SPAM", "", false},
		{"CATEGORY_PROMOTIONS", "", false},
		{"[Gmail]/Sent Mail", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		tag, ok := TagFromGmailLabel(c.label)
		if tag != c.tag || ok != c.ok {
			t.Errorf("TagFromGmailLabel(%q) = %q, %v; want %q, %v", c.label, tag, ok, c.tag, c.ok)
		}
		if ok {
			back, err := TagToGmailLabel(tag)
			if err != nil || back != tag {
				t.Errorf("TagToGmailLabel(%q) = %q, %v", tag, back, err)
			}
		}
	}
	if _, err := TagToGmailLabel("Inbox"); err == nil {
		t.Error("TagToGmailLabel(Inbox) should fail")
	}
}

func TestOutlookCategories(t *testing.T) {
	for _, c := range []struct{ tag, cat string }{
		{"Kunde A", "Kunde A"},
		{"Projekt/Kunde-A", "Projekt/Kunde-A"},
		{"a,b;c", "a b c"},
	} {
		got, err := TagToOutlookCategory(c.tag)
		if err != nil || got != c.cat {
			t.Errorf("TagToOutlookCategory(%q) = %q, %v; want %q", c.tag, got, err, c.cat)
		}
		back, ok := TagFromOutlookCategory(got)
		if !ok || back != c.cat {
			t.Errorf("TagFromOutlookCategory(%q) = %q, %v", got, back, ok)
		}
	}
	long := strings.Repeat("x", 60) + "/" + strings.Repeat("y", 60) + "/" + strings.Repeat("z", 60) + "/" + strings.Repeat("w", 60) + "/" + strings.Repeat("v", 60)
	got, err := TagToOutlookCategory(long)
	if err != nil || len([]rune(got)) != MaxOutlookCategory {
		t.Errorf("long category: %d runes, %v", len([]rune(got)), err)
	}
	for i := range OutlookPresets {
		p := "preset" + itoa(i)
		if got := OutlookPresetFor(OutlookPresetColor(p)); got != p {
			t.Errorf("preset round trip %s -> %s", p, got)
		}
	}
	if OutlookPresetFor("#e84a57") != "preset0" || OutlookPresetFor("") != "none" || OutlookPresetColor("none") != "" {
		t.Error("nearest preset / none handling")
	}
	if OutlookPresetFor("#000") != "preset14" {
		t.Errorf("#000 -> %s", OutlookPresetFor("#000"))
	}
}

func TestNormalizeTagPath(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"  Projekt / Kunde   A ", "Projekt/Kunde A", true},
		{"a//b/", "a/b", true},
		{"", "", false},
		{" / ", "", false},
		{"bad\x01", "", false},
		{strings.Repeat("a/", MaxTagDepth) + "x", "", false},
		{strings.Repeat("x", MaxTagSegment+1), "", false},
	}
	for _, c := range cases {
		got, err := NormalizeTagPath(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizeTagPath(%q) = %q, %v", c.in, got, err)
		}
	}
}
