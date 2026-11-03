// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeMessageID(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "<abc@example.com>", "abc@example.com"},
		{"domain lowercased", "<ABC.Def@Example.COM>", "ABC.Def@example.com"},
		{"no brackets", "abc@EXAMPLE.com", "abc@example.com"},
		{"surrounding whitespace", "  <abc@example.com>\r\n", "abc@example.com"},
		{"folded whitespace inside", "<abc\r\n @example.com>", "abc@example.com"},
		{"comment before", "(comment) <abc@example.com>", "abc@example.com"},
		{"comment after", "<abc@example.com> (Joe's message)", "abc@example.com"},
		{"nested comment", "((a) b) <x@y.z>", "x@y.z"},
		{"quoted local part", `<"foo bar"@example.com>`, `"foobar"@example.com`},
		{"last at sign is domain", `<a@b@EXAMPLE.org>`, "a@b@example.org"},
		{"no at sign bracketed", "<AM0PR01MB1234.EURPRD01.PROD.OUTLOOK.COM>", "AM0PR01MB1234.EURPRD01.PROD.OUTLOOK.COM"},
		{"no at sign bare", "  1234567890.ABCDEF  ", "1234567890.ABCDEF"},
		{"mailto prefix", "<mailto:abc@Example.com>", "abc@example.com"},
		{"empty", "", ""},
		{"only brackets", "<>", ""},
		{"only whitespace", " \t ", ""},
		{"two bare words", "hello world", ""},
		{"first of several", "<a@x.de> <b@x.de>", "a@x.de"},
		{"unterminated", "<abc@example.com", "abc@example.com"},
		{"gmail id", "<CAF=abc123+xyz-_Q@mail.gmail.com>", "CAF=abc123+xyz-_Q@mail.gmail.com"},
		{"too long", "<" + strings.Repeat("a", MaxMessageIDLength) + "@x.de>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMessageID(tt.in); got != tt.want {
				t.Errorf("NormalizeMessageID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseMessageIDs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "<a@b.c>", []string{"a@b.c"}},
		{"references folded", "<a@b.c>\r\n\t<d@E.F>\r\n <g@h.i>", []string{"a@b.c", "d@e.f", "g@h.i"}},
		{"no separator", "<a@b.c><d@e.f>", []string{"a@b.c", "d@e.f"}},
		{"duplicates keep first", "<a@b.c> <d@e.f> <A@B.C> <a@b.c>", []string{"a@b.c", "d@e.f", "A@b.c"}},
		{"in-reply-to with quoted date", `<a@b.c> (Max's message of "Mon, 15 Jan 2024 10:30:00 +0100")`, []string{"a@b.c"}},
		{"quoted string with brackets is ignored", `"<fake@x.y>" <real@x.y>`, []string{"real@x.y"}},
		{"comment with brackets is ignored", `(see <fake@x.y>) <real@x.y>`, []string{"real@x.y"}},
		{"escaped paren in comment", `(a \) <fake@x.y>) <real@x.y>`, []string{"real@x.y"}},
		{"bare comma separated", "a@b.c, d@e.f", []string{"a@b.c", "d@e.f"}},
		{"bare garbage without at", "hello world", nil},
		{"unterminated followed by valid", "<broken@x.y <ok@x.y>", []string{"broken@x.y", "ok@x.y"}},
		{"empty brackets skipped", "<> <a@b.c>", []string{"a@b.c"}},
		{"unbalanced comment swallows rest", "<a@b.c> (oops <d@e.f>", []string{"a@b.c"}},
		{"stray closing bracket", "> <a@b.c> >", []string{"a@b.c"}},
		{"non utf8", "<a\xff@b.c>", []string{"a@b.c"}},
		{"mixed text and ids", "Message from Joe of 1 Jan <id1@host.example>", []string{"id1@host.example"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMessageIDs(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseMessageIDs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseReferencesAndInReplyTo(t *testing.T) {
	refs := ParseReferences("<1@x> <2@x> <3@x>")
	if !reflect.DeepEqual(refs, []string{"1@x", "2@x", "3@x"}) {
		t.Fatalf("ParseReferences = %v", refs)
	}
	irt := ParseInReplyTo("<1@x> <2@x>")
	if len(irt) != 2 {
		t.Fatalf("ParseInReplyTo = %v", irt)
	}
}

func TestNormalizeIDs(t *testing.T) {
	got := normalizeIDs([]string{"a@B.c", "<a@b.c>", "<d@e.f> <g@h.i>", "", "bareid"})
	want := []string{"a@b.c", "d@e.f", "g@h.i", "bareid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeIDs = %q, want %q", got, want)
	}
}
