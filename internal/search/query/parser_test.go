// SPDX-License-Identifier: AGPL-3.0-or-later

package query

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func w(v string) *Term               { return &Term{Value: v} }
func fw(f, v string) *Term           { return &Term{Field: f, Value: v} }
func ph(v string) *Term              { return &Term{Value: v, Kind: Phrase} }
func and_(ns ...Node) *And           { return &And{Nodes: ns} }
func or_(ns ...Node) *Or             { return &Or{Nodes: ns} }
func not_(n Node) *Not               { return &Not{Node: n} }
func kind(t *Term, k TermKind) *Term { t.Kind = k; return t }
func boost(t *Term, b float64) *Term { t.Boost = b; return t }
func field(f string, t *Term) *Term  { t.Field = f; return t }
func rng(f, a, b string, ai, bi bool) *Range {
	return &Range{Field: f, From: a, To: b, FromIncl: ai, ToIncl: bi}
}

// stripOffsets zeroes all offsets for comparison.
func stripOffsets(n Node) Node {
	Walk(n, func(n Node) bool {
		switch v := n.(type) {
		case *Term:
			v.Offset = 0
		case *Range:
			v.Offset = 0
		}
		return true
	})
	return n
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Node
	}{
		{"", nil},
		{"   ", nil},
		{"anna", w("anna")},
		{"anna max", and_(w("anna"), w("max"))},
		{"anna AND max", and_(w("anna"), w("max"))},
		{"anna && max", and_(w("anna"), w("max"))},
		{"anna UND max", and_(w("anna"), w("max"))},
		{"anna OR max", or_(w("anna"), w("max"))},
		{"anna || max ODER moritz", or_(w("anna"), w("max"), w("moritz"))},
		{"and or not", and_(w("and"), w("or"), w("not"))}, // lowercase words are not operators
		// Precedence: NOT > AND > OR.
		{"a OR b AND c", or_(w("a"), and_(w("b"), w("c")))},
		{"a AND b OR c", or_(and_(w("a"), w("b")), w("c"))},
		{"a b OR c d", or_(and_(w("a"), w("b")), and_(w("c"), w("d")))},
		{"NOT a AND b", and_(not_(w("a")), w("b"))},
		{"NOT a OR b", or_(not_(w("a")), w("b"))},
		{"(a OR b) AND c", and_(or_(w("a"), w("b")), w("c"))},
		{"a AND (b OR (c AND NOT d))", and_(w("a"), or_(w("b"), and_(w("c"), not_(w("d")))))},
		{"NICHT a", not_(w("a"))},
		{"!a", not_(w("a"))},
		{"NOT NOT a", not_(not_(w("a")))},
		{"-a +b", and_(not_(w("a")), w("b"))},
		{"a-b c+d", and_(w("a-b"), w("c+d"))},
		{"- a", w("a")}, // lone "-" is not searchable
		// Fields.
		{"from:anna", fw("from", "anna")},
		{"FROM:anna", fw("from", "anna")},
		{"to:a cc:b bcc:c participant:d", and_(fw("to", "a"), fw("cc", "b"), fw("bcc", "c"), fw("participant", "d"))},
		{"subject:x body:y attachment:z filename:f", and_(fw("subject", "x"), fw("body", "y"), fw("attachment", "z"), fw("filename", "f"))},
		{"account:Work folder:INBOX in:Archiv", and_(fw("account", "Work"), fw("folder", "INBOX"), fw("folder", "Archiv"))},
		{"channel:signal type:event category:x label:l tag:t lang:de", and_(fw("channel", "signal"), fw("type", "event"),
			fw("category", "x"), fw("label", "l"), fw("label", "t"), fw("lang", "de"))},
		{"header.X-Mailer:Thunderbird", fw("header.x-mailer", "Thunderbird")},
		{`subject:"annual report"`, field("subject", ph("annual report"))},
		{"from:(anna OR max)", or_(fw("from", "anna"), fw("from", "max"))},
		{"from:(anna OR subject:max)", or_(fw("from", "anna"), fw("subject", "max"))},
		{"NOT from:(a b)", not_(and_(fw("from", "a"), fw("from", "b")))},
		{"http://example.org/x", w("http://example.org/x")}, // unknown field: plain word
		{"foo:bar", w("foo:bar")},
		// Flags.
		{"is:unread is:Ungelesen has:attachment has:Anhang", and_(fw("is", "unread"), fw("is", "unread"),
			fw("has", "attachment"), fw("has", "attachment"))},
		{"is:read is:flagged is:answered is:encrypted is:signed is:pinned", and_(fw("is", "read"), fw("is", "flagged"),
			fw("is", "answered"), fw("is", "encrypted"), fw("is", "signed"), fw("is", "pinned"))},
		{"has:link has:calendar has:pgp exists:subject _exists_:attachment", and_(fw("has", "link"), fw("has", "calendar"),
			fw("has", "pgp"), fw("exists", "subject"), fw("exists", "attachment"))},
		// Term kinds.
		{`"exact phrase"`, ph("exact phrase")},
		{`"a \"quoted\" word"`, ph(`a "quoted" word`)},
		{`"a b"~5`, &Term{Value: "a b", Kind: Phrase, Slop: 5}},
		{"anna*", kind(w("anna"), Prefix)},
		{"an?a", kind(w("an?a"), Wildcard)},
		{"*nna", kind(w("*nna"), Wildcard)},
		{"a*na", kind(w("a*na"), Wildcard)},
		{"*", &Term{Kind: All}},
		{`anna\*`, w("anna*")},
		{`a\ b`, w("a b")},
		{"anna~", &Term{Value: "anna", Kind: Fuzzy, Fuzz: 2}},
		{"anna~1", &Term{Value: "anna", Kind: Fuzzy, Fuzz: 1}},
		{"anna~0", w("anna")},
		{"/an+a/", &Term{Value: "an+a", Kind: Regex}},
		{`/a\/b/`, &Term{Value: "a/b", Kind: Regex}},
		{"anna^2", boost(w("anna"), 2)},
		{`"a b"^1.5`, boost(ph("a b"), 1.5)},
		{"subject:rechnung^3 body:rechnung", and_(boost(fw("subject", "rechnung"), 3), fw("body", "rechnung"))},
		{`"..." ,,, a`, w("a")}, // terms without letters/digits are ignored
		// Ranges and comparisons.
		{"date:[2025-01-01 TO 2025-06-30]", rng("date", "2025-01-01", "2025-06-30", true, true)},
		{"date:{2025-01-01 TO 2025-06-30]", rng("date", "2025-01-01", "2025-06-30", false, true)},
		{"date:[2025-01-01 BIS *]", rng("date", "2025-01-01", "", true, false)},
		{`date:["last month" TO today]`, rng("date", "last month", "today", true, true)},
		{"date:>=2025-01-01", rng("date", "2025-01-01", "", true, false)},
		{"date:>now-7d", rng("date", "now-7d", "", false, false)},
		{"date:<2025", rng("date", "", "2025", false, false)},
		{"date:<=2025-03", rng("date", "", "2025-03", false, true)},
		{"date:today", fw("date", "today")},
		{`date:"last week"`, field("date", ph("last week"))},
		{`date:"letzte Woche"`, field("date", ph("letzte Woche"))},
		{"size:>10MB attachments:>2", and_(rng("size", "10MB", "", false, false), rng("attachments", "2", "", false, false))},
		{"size:[1k TO 2M}", rng("size", "1k", "2M", true, false)},
		{"date:(>2025 AND <2026)", and_(rng("date", "2025", "", false, false), rng("date", "", "2026", false, false))},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got = stripOffsets(got); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q)\n got %s\nwant %s", c.in, String(got), String(c.want))
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		in     string
		column int
		msg    string
	}{
		{"(a OR b", 1, "missing ')'"},
		{"a)", 2, "unbalanced ')'"},
		{"a AND", 3, "after AND"},
		{"AND a", 1, "before AND"},
		{"a OR", 3, "after OR"},
		{"OR a", 1, "before OR"},
		{"a AND OR b", 3, "after AND"},
		{"NOT", 1, "after NOT"},
		{"a NOT", 3, "after NOT"},
		{`"open`, 1, "unterminated phrase"},
		{`subject:"open`, 9, "unterminated phrase"},
		{"/open", 1, "unterminated regular expression"},
		{"/a(/", 1, "invalid regular expression"},
		{"from:", 1, "missing value"},
		{"from: anna", 1, "missing value"},
		{"is:happy", 4, `unknown value "happy"`},
		{"has:x", 5, "unknown value"},
		{"date:yesterdayish", 6, "invalid date"},
		{"date:[2025 TO", 14, "missing range bound"},
		{"date:[2025 2026]", 12, "expected TO"},
		{"date:[2025 TO 2026", 1, "unterminated range"},
		{"date:[2025 TO bogus]", 15, "invalid date"},
		{"subject:[a TO b]", 1, "does not support ranges"},
		{"subject:>a", 1, "does not support comparisons"},
		{"size:>lots", 7, "invalid number"},
		{"size:>", 1, "missing value"},
		{"anna~5", 5, "fuzziness"},
		{"anna^", 5, "boost"},
		{"anna^0", 5, "boost"},
		{`"a b"~x`, 6, "proximity"},
		{`ä a)`, 4, "unbalanced"}, // columns count characters, not bytes
		{strings.Repeat("(", 100) + "a" + strings.Repeat(")", 100), 65, "nested too deeply"},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q): want syntax error, got %v", c.in, err)
			continue
		}
		if se.Column != c.column || !strings.Contains(se.Msg, c.msg) {
			t.Errorf("Parse(%q) = column %d %q; want column %d containing %q", c.in, se.Column, se.Msg, c.column, c.msg)
		}
	}
}

func TestParseOrSimpleFallback(t *testing.T) {
	n, err := ParseOrSimple(`rechnung (offen`)
	if err == nil {
		t.Fatal("expected syntax error")
	}
	want := and_(w("rechnung"), w("(offen"))
	if got := stripOffsets(n); !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback %s", String(got))
	}
	if n, err := ParseOrSimple("a OR b"); err != nil || !reflect.DeepEqual(stripOffsets(n), or_(w("a"), w("b"))) {
		t.Fatalf("valid query: %v %v", n, err)
	}
	if got := stripOffsets(ParseSimple(`"a b" c* NOT d:e ...`)); !reflect.DeepEqual(got,
		and_(ph("a b"), kind(w("c"), Prefix), w("NOT"), w("d:e"))) {
		t.Fatalf("simple: %s", String(got))
	}
}

func TestStringRoundTrip(t *testing.T) {
	inputs := []string{
		"a b", "a OR b c", "(a OR b) c", "NOT (a OR b)", "NOT a b", "from:(anna OR max) subject:\"x y\"~3",
		`a\:b "say \"hi\"" c\ d`, "anna* an?a *nna anna~1 /re\\/x/ anna^2.5", "date:[2025-01-01 TO now-1d}",
		"date:>=2025 size:<10MB attachments:<=3", `date:["last week" TO *]`, "is:unread has:attachment exists:from",
		"header.x-spam:yes", `"AND" "OR"`, "*", "a OR (b AND (c OR d))", "-a -b +c", `\-x \+y`, `"" a`,
	}
	for _, in := range inputs {
		n1, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		s := String(n1)
		n2, err := Parse(s)
		if err != nil {
			t.Fatalf("re-parse of %q (%q): %v", in, s, err)
		}
		if !reflect.DeepEqual(stripOffsets(n1), stripOffsets(n2)) {
			t.Errorf("round trip %q -> %q\n %s\n %s", in, s, String(n1), String(n2))
		}
	}
}

func TestAddClause(t *testing.T) {
	root, _ := Parse("rechnung OR angebot")
	got := AddClause(root, fw("account", "2"))
	if s := String(got); s != "(rechnung OR angebot) AND account:2" {
		t.Fatalf("got %q", s)
	}
	got = AddClause(got, not_(fw("has", "attachment")))
	if s := String(got); s != "(rechnung OR angebot) AND account:2 AND NOT has:attachment" {
		t.Fatalf("got %q", s)
	}
	if AddClause(nil, w("x")).(*Term).Value != "x" || AddClause(w("x"), nil).(*Term).Value != "x" {
		t.Fatal("nil handling")
	}
	if !HasField(got, "account") || HasField(got, "folder") {
		t.Fatal("HasField")
	}
}

func TestResolveDate(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Date(2026, 11, 4, 15, 30, 0, 0, berlin) // a Wednesday
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, berlin) }
	cases := []struct {
		in         string
		start, end time.Time
	}{
		{"2026-03-01", d(2026, 3, 1), d(2026, 3, 2)},
		{"01.03.2026", d(2026, 3, 1), d(2026, 3, 2)},
		{"2026-03", d(2026, 3, 1), d(2026, 4, 1)},
		{"2026", d(2026, 1, 1), d(2027, 1, 1)},
		{"today", d(2026, 11, 4), d(2026, 11, 5)},
		{"Heute", d(2026, 11, 4), d(2026, 11, 5)},
		{"gestern", d(2026, 11, 3), d(2026, 11, 4)},
		{"this week", d(2026, 11, 2), d(2026, 11, 9)},
		{"letzte  Woche", d(2026, 10, 26), d(2026, 11, 2)},
		{"last month", d(2026, 10, 1), d(2026, 11, 1)},
		{"dieses Jahr", d(2026, 1, 1), d(2027, 1, 1)},
		{"letztes Jahr", d(2025, 1, 1), d(2026, 1, 1)},
		{"now-7d", now.AddDate(0, 0, -7), now.AddDate(0, 0, -7)},
		{"now-1M+2h", now.AddDate(0, -1, 0).Add(2 * time.Hour), now.AddDate(0, -1, 0).Add(2 * time.Hour)},
		{"2026-03-01T10:00:00Z", time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		s, e, err := ResolveDate(c.in, now, berlin)
		if err != nil || !s.Equal(c.start) || !e.Equal(c.end) {
			t.Errorf("ResolveDate(%q) = %v, %v, %v; want %v, %v", c.in, s, e, err, c.start, c.end)
		}
	}
	for _, bad := range []string{"", "now-", "now-7x", "now7d", "2026-13-01", "soon", "now-99999999d"} {
		if _, _, err := ResolveDate(bad, now, berlin); err == nil {
			t.Errorf("ResolveDate(%q) should fail", bad)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"10": 10, "10b": 10, "1k": 1024, "1KB": 1024, "1.5MiB": 1572864, "2G": 2 << 30, "0": 0}
	for in, want := range cases {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "-1", "1TB", "NaN", "1e300"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"a OR b", `from:(a OR "b c"~2) -d`, "date:[2025 TO now-1d}", "/x+/ y~1 z^2",
		`"\"`, "((a)", "a:b:c", "NOT !a && || b", "size:>1MB attachments:<=2", "\x00\xff", "header.x:y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		n, err := Parse(in)
		if err != nil {
			var se *SyntaxError
			if !errors.As(err, &se) || se.Offset < 0 || se.Offset > len(in) {
				t.Fatalf("bad error %v", err)
			}
			_ = ParseSimple(in)
			return
		}
		// Serialization round trip must be stable.
		s := String(n)
		n2, err := Parse(s)
		if err != nil {
			t.Fatalf("re-parse of %q (from %q): %v", s, in, err)
		}
		if s2 := String(n2); s2 != s {
			t.Fatalf("unstable serialization %q -> %q -> %q", in, s, s2)
		}
	})
}
