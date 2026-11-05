// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// unfoldICalReference is the previous (quadratic) implementation.
func unfoldICalReference(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestUnfoldICalEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"empty", "", []string{""}},
		{"single", "A:1", []string{"A:1"}},
		{"crlf", "A:1\r\nB:2\r\n", []string{"A:1", "B:2", ""}},
		{"fold space", "DESCRIPTION:ab\r\n cd\r\n ef\r\nX:1", []string{"DESCRIPTION:abcdef", "X:1"}},
		{"fold tab", "A:ab\n\tcd", []string{"A:abcd"}},
		{"only first char dropped", "A:ab\n  cd", []string{"A:ab cd"}},
		{"empty continuation", "A:ab\n \nB:1", []string{"A:ab", "B:1"}},
		{"continuation first", " x\nA:1", []string{" x", "A:1"}},
		{"blank line then fold", "A:1\n\n x", []string{"A:1", "x"}},
		{"fold after fold", "A:\n b\n c\nB:\n d", []string{"A:bc", "B:d"}},
	} {
		if got := unfoldICal(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: unfoldICal = %q, want %q", tc.name, got, tc.want)
		}
		if ref := unfoldICalReference(tc.in); !reflect.DeepEqual(ref, tc.want) {
			t.Errorf("%s: reference = %q (test expectation is wrong)", tc.name, ref)
		}
	}
}

func TestUnfoldICalMatchesReference(t *testing.T) {
	pieces := []string{"A:b", " c", "\tx", "", " ", "É", "\r", ":"}
	seed := uint32(7)
	next := func(n int) int { seed = seed*1664525 + 1013904223; return int(seed>>8) % n }
	for range 3000 {
		var b strings.Builder
		for range next(12) {
			b.WriteString(pieces[next(len(pieces))])
			b.WriteString([]string{"\r\n", "\n"}[next(2)])
		}
		in := b.String()
		if got, want := unfoldICal(in), unfoldICalReference(in); !reflect.DeepEqual(got, want) {
			t.Fatalf("input %q:\n got  %q\n want %q", in, got, want)
		}
	}
}

// NB-02: a calendar part with one line folded over a million continuation
// lines pinned the CPU for ~100 s (4 MB) when it was received.
func TestCalendarPartFoldBombIsFast(t *testing.T) {
	const n = 1_000_000 // ~4 MB
	start := time.Now()
	if lines := unfoldICal("DESCRIPTION:" + strings.Repeat("\r\n x", n)); len(lines) != 1 || len(lines[0]) != len("DESCRIPTION:")+n {
		t.Fatalf("unfold: %d lines", len(lines))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("unfoldICal of 4 MB took %v", d)
	}

	// The real sync path: the whole message through Parse.
	body := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:bomb@example.org\r\nSUMMARY:Hi\r\nDESCRIPTION:" +
		strings.Repeat("\r\n x", n) + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	raw := "From: a@example.org\r\nTo: b@example.org\r\nSubject: bomb\r\nDate: Thu, 05 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@example.org>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" + body
	start = time.Now()
	msg, err := Parse([]byte(raw), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Parse of a calendar mail with a 4 MB folded line took %v", d)
	}
	if len(msg.CalendarParts) != 1 || msg.CalendarParts[0].UID != "bomb@example.org" || msg.CalendarParts[0].Summary != "Hi" {
		t.Fatalf("calendar part: %+v", msg.CalendarParts)
	}
}
