// SPDX-License-Identifier: AGPL-3.0-or-later

package ical

import (
	"bufio"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// unfoldReference is the previous (quadratic) implementation, kept to check
// that the linear one returns exactly the same lines.
func unfoldReference(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff")
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	qpCont := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case len(out) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			out[len(out)-1] += line[1:]
		case qpCont && len(out) > 0:
			out[len(out)-1] += line
		case line == "":
			continue
		default:
			out = append(out, line)
		}
		last := out[len(out)-1]
		qpCont = strings.HasSuffix(last, "=") && isQPLine(last)
		if qpCont {
			out[len(out)-1] = strings.TrimSuffix(last, "=")
		}
	}
	return out
}

func TestUnfoldEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"crlf and lf", "A:1\r\nB:2\nC:3", []string{"A:1", "B:2", "C:3"}},
		{"fold with space", "DESCRIPTION:ab\r\n cd\r\n ef\r\nX:1", []string{"DESCRIPTION:abcdef", "X:1"}},
		{"fold with tab", "A:ab\r\n\tcd", []string{"A:abcd"}},
		{"only first char removed", "A:ab\r\n  cd", []string{"A:ab cd"}},
		{"empty continuation", "A:ab\r\n \r\nB:1", []string{"A:ab", "B:1"}},
		{"leading continuation without line", " x\r\nA:1", []string{" x", "A:1"}},
		{"blank lines skipped", "A:1\r\n\r\n\r\nB:2\r\n", []string{"A:1", "B:2"}},
		{"bom", "\ufeffA:1", []string{"A:1"}},
		{"fold after blank line", "A:1\r\n\r\n x", []string{"A:1x"}},
		{"quoted-printable soft breaks", "NOTE;ENCODING=QUOTED-PRINTABLE:=C3=\r\n=A4=\r\nok\r\nX:1", []string{"NOTE;ENCODING=QUOTED-PRINTABLE:=C3=A4ok", "X:1"}},
		{"trailing = without QP is kept", "A:1=\r\nB:2", []string{"A:1=", "B:2"}},
		{"QP param after quoted colon", "X;P=\"a:b\";ENCODING=QUOTED-PRINTABLE:x=\r\ny", []string{"X;P=\"a:b\";ENCODING=QUOTED-PRINTABLE:xy"}},
		{"QP in the value only is not QP", "X:QUOTED-PRINTABLE=\r\nB:1", []string{"X:QUOTED-PRINTABLE=", "B:1"}},
	} {
		got := unfold(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: unfold = %q, want %q", tc.name, got, tc.want)
		}
		if ref := unfoldReference(tc.in); !reflect.DeepEqual(ref, tc.want) {
			t.Errorf("%s: reference = %q (test expectation is wrong)", tc.name, ref)
		}
	}
}

func TestUnfoldMatchesReference(t *testing.T) {
	pieces := []string{"A:b", " c", "\tx", "=", "TEL;ENCODING=QUOTED-PRINTABLE:=41=", `X;"a:b`, `:`, "", "\r", " ", "QUOTED-PRINTABLE", `"`, "N;QUOTED-PRINTABLE:q=", "é"}
	rng := rand.New(rand.NewSource(1))
	for range 3000 {
		var b strings.Builder
		for range rng.Intn(14) {
			b.WriteString(pieces[rng.Intn(len(pieces))])
			b.WriteString([]string{"\r\n", "\n", "\r\n "}[rng.Intn(3)])
		}
		in := b.String()
		if got, want := unfold(in), unfoldReference(in); !reflect.DeepEqual(got, want) {
			t.Fatalf("input %q:\n got  %q\n want %q", in, got, want)
		}
	}
}

// NB-02: a line folded over millions of continuation lines used to take
// minutes (quadratic string concatenation).
func TestUnfoldIsLinear(t *testing.T) {
	const n = 1_000_000 // ~4 MB of input
	folded := "DESCRIPTION:" + strings.Repeat("\r\n x", n)
	qpEvil := "X;\"unterminated" + strings.Repeat("\r\n =", n) // every fragment ends with '='
	qpReal := "NOTE;ENCODING=QUOTED-PRINTABLE:" + strings.Repeat("ab=\r\n", n/2)
	for name, in := range map[string]string{"folded": folded, "quote-without-colon": qpEvil, "quoted-printable": qpReal} {
		start := time.Now()
		lines := unfold(in)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: unfold of %d bytes took %v", name, len(in), d)
		}
		if len(lines) != 1 {
			t.Errorf("%s: %d lines, want 1", name, len(lines))
		}
	}
	start := time.Now()
	c, err := Parse("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n" + folded + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Parse of a 4 MB folded description took %v", d)
	}
	if v := c.Children[0].Props[0].Value; len(v) != n {
		t.Errorf("description has %d bytes, want %d", len(v), n)
	}
}

// NB-03: nesting depth is bounded.
func TestParseRejectsDeepNesting(t *testing.T) {
	ok := strings.Repeat("BEGIN:X\r\n", MaxDepth) + strings.Repeat("END:X\r\n", MaxDepth)
	if _, err := Parse(ok); err != nil {
		t.Fatalf("depth %d must parse: %v", MaxDepth, err)
	}
	tooDeep := strings.Repeat("BEGIN:X\r\n", MaxDepth+1) + strings.Repeat("END:X\r\n", MaxDepth+1)
	if _, err := Parse(tooDeep); !errors.Is(err, ErrTooDeep) || !errors.Is(err, ErrSyntax) {
		t.Fatalf("depth %d: err = %v, want ErrTooDeep", MaxDepth+1, err)
	}
	// 3 million nested BEGIN lines: fails fast and does not crash.
	huge := strings.Repeat("BEGIN:X\n", 3_000_000)
	start := time.Now()
	if _, err := ParseAll(huge); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("3M nested: err = %v", err)
	}
	limit := 5 * time.Second
	if raceEnabled {
		limit = 30 * time.Second // the race detector slows this loop down about tenfold
	}
	if d := time.Since(start); d > limit {
		t.Errorf("3M nested BEGIN lines took %v", d)
	}
	// Ordinary calendars are unaffected.
	if _, err := Parse(sample); err != nil {
		t.Fatal(err)
	}
}

// NB-03: Clone and Encode must not recurse on the Go stack, whatever way the
// tree was built.
func TestCloneEncodeDeepTree(t *testing.T) {
	depth := 3_000_000
	if testing.Short() {
		depth = 200_000
	}
	root := &Component{Name: "A", Props: []*Prop{{Name: "UID", Value: "root"}}}
	cur := root
	for range depth {
		ch := &Component{Name: "X"}
		cur.Children = append(cur.Children, ch)
		cur = ch
	}
	cur.Props = []*Prop{{Name: "UID", Value: "leaf"}}
	cl := root.Clone()
	n := 0
	for c := cl; len(c.Children) > 0; c = c.Children[0] {
		n++
		if len(c.Children) != 1 {
			t.Fatalf("level %d: %d children", n, len(c.Children))
		}
	}
	if n != depth {
		t.Fatalf("clone depth %d, want %d", n, depth)
	}
	if cl == root || cl.Children[0] == root.Children[0] {
		t.Fatal("clone shares components")
	}
	enc := root.Encode()
	if got := strings.Count(enc, "BEGIN:"); got != depth+1 {
		t.Fatalf("encoded %d BEGIN lines, want %d", got, depth+1)
	}
	if !strings.HasPrefix(enc, "BEGIN:A\r\nUID:root\r\nBEGIN:X\r\n") || !strings.HasSuffix(enc, "END:X\r\nEND:A\r\n") {
		t.Fatalf("unexpected encoding: %q ... %q", enc[:40], enc[len(enc)-20:])
	}
}

// Encode/Clone keep the order and structure of ordinary trees.
func TestCloneEncodeKeepStructure(t *testing.T) {
	c, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	cl := c.Clone()
	if !reflect.DeepEqual(c, cl) {
		t.Fatal("clone differs from the original")
	}
	if c.Encode() != cl.Encode() {
		t.Fatal("encodings differ")
	}
	re, err := Parse(c.Encode())
	if err != nil || !reflect.DeepEqual(re, c) {
		t.Fatalf("round trip: %v", err)
	}
	if got := (&Component{Name: "E"}).Clone(); got.Children != nil || got.Props != nil || got.Name != "E" {
		t.Fatalf("empty clone: %+v", got)
	}
}
