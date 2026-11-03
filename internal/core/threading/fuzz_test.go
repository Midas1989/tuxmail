// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/domain"
)

var headerSeeds = []string{
	"",
	"<a@b.c>",
	"<a@b.c> <d@E.F>\r\n\t<g@h.i>",
	`<a@b.c> (Max's message of "Mon, 15 Jan 2024 10:30:00 +0100")`,
	`"<fake@x.y>" <real@x.y>`,
	"((nested) comment) <x@y>",
	"<broken@x.y <ok@x.y>",
	"a@b.c, d@e.f",
	"<\"quoted local\"@example.com>",
	"(unterminated <x@y>",
	"\\<x@y>",
	"<a\xff@b.c>",
	"<<<>>>",
}

func FuzzParseMessageIDs(f *testing.F) {
	for _, s := range headerSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ids := ParseMessageIDs(s)
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" {
				t.Fatalf("empty id from %q", s)
			}
			if strings.ContainsAny(id, "<> \t\r\n") {
				t.Fatalf("id %q from %q contains forbidden characters", id, s)
			}
			if !utf8.ValidString(id) {
				t.Fatalf("invalid UTF-8 id %q", id)
			}
			if len(id) > MaxMessageIDLength {
				t.Fatalf("id too long")
			}
			if seen[id] {
				t.Fatalf("duplicate id %q", id)
			}
			seen[id] = true
			// Normalization is idempotent.
			if n := NormalizeMessageID("<" + id + ">"); n != id {
				t.Fatalf("not idempotent: %q -> %q", id, n)
			}
		}
		n := NormalizeMessageID(s)
		if n != "" {
			if NormalizeMessageID("<"+n+">") != n {
				t.Fatalf("NormalizeMessageID not idempotent: %q -> %q -> %q", s, n, NormalizeMessageID("<"+n+">"))
			}
			if got := normalizeIDs([]string{n}); len(got) != 1 || got[0] != n {
				t.Fatalf("normalizeIDs(%q) = %q", n, got)
			}
		}
		// Relation extraction must never panic or produce self references.
		in := MessageInput{Header: domain.MessageHeaderInfo{
			MessageID:  s,
			InReplyTo:  []string{s},
			References: []string{s, "<x@y>"},
		}, Fingerprint: "ff", AttachedMessageIDs: []string{s}}
		key := in.ResolveKey()
		for _, r := range ExtractRelations(in) {
			if r.SourceKey != key || r.TargetKey == key || r.TargetKey == "" {
				t.Fatalf("bad relation %+v for key %q", r, key)
			}
		}
	})
}

func FuzzDecodeThreadIndex(f *testing.F) {
	f.Add("AdpHncrgb5YZ/4uG0BG0LQDAT8lk/w==")
	f.Add("AdpHncrgb5YZ/4uG0BG0LQDAT8lk/wAAEjRWAAGrze8=")
	f.Add("")
	f.Add("====")
	f.Fuzz(func(t *testing.T, s string) {
		ti, err := DecodeThreadIndex(s)
		if err == nil && len(ti.Root) != 22 {
			t.Fatalf("root length %d", len(ti.Root))
		}
	})
}

func FuzzDetectForwards(f *testing.F) {
	f.Add("---------- Forwarded message ---------\nFrom: A <a@b.c>\nDate: Mon, Jan 15, 2024 at 10:30 AM\nSubject: x\nTo: b@c.d\n")
	f.Add("Von: X <x@y.de>\nGesendet: Montag, 15. Januar 2024 10:30\nAn: y@z.de\nBetreff: Test\n")
	f.Add("> > From: a\n> Sent:\n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, fh := range DetectForwards(s) {
			if fh.Line < 0 {
				t.Fatal("negative line")
			}
		}
		ParseLooseDate(s)
		ParseSubject(s)
	})
}
