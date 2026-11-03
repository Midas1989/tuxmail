// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
)

func TestParseRuleExample(t *testing.T) {
	src := `WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment ` +
		`THEN tag "Kunde", move "Kunden/Eingang", notify signal "Neue Kundenmail: {{betreff}}"`
	w, err := ParseRule(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := &Workflow{
		Trigger:   Trigger{Event: events.MailReceived, AccountNames: []string{"Firma"}},
		Condition: Condition{Rule: &Rule{Query: "from:*@kunde.de AND has:attachment"}},
		Actions: []ActionSpec{
			{Type: "tag", Params: map[string]any{"tags": []any{"Kunde"}}},
			{Type: "move", Params: map[string]any{"folder": "Kunden/Eingang"}},
			{Type: "notify", Params: map[string]any{"channel": "signal:me", "text": "Neue Kundenmail: {{betreff}}"}},
		},
	}
	if !reflect.DeepEqual(w, want) {
		t.Fatalf("got  %#v\nwant %#v", w, want)
	}
	w.ID, w.Name = "kunden", "Kundenmails"
	if err := w.Validate(nil); err != nil {
		t.Fatalf("parsed rule is invalid: %v", err)
	}
	// Same rule from YAML.
	y := mustParse(t, `id: kunden
name: Kundenmails
trigger: {event: mail.received, account_names: [Firma]}
condition: {query: "from:*@kunde.de AND has:attachment"}
actions:
  - {type: tag, tags: [Kunde]}
  - {type: move, folder: Kunden/Eingang}
  - {type: notify, channel: "signal:me", text: "Neue Kundenmail: {{betreff}}"}
`)
	if !reflect.DeepEqual(w, y) {
		t.Fatalf("rule and YAML differ:\n%#v\n%#v", w, y)
	}
}

func TestRuleRoundTrip(t *testing.T) {
	cases := []string{
		`WHEN mail.received THEN archive`,
		`RULE vip "VIP melden" ENABLED DESCRIPTION "Meldet VIPs" WHEN mail.received IN account:3, account:"Privat", folder:"INBOX" ` +
			"IF `mail.from in [\"chefin@example.com\"] || mail.fromDomain == \"vip.example\"` " +
			`THEN notify "signal:me" "VIP: {{absender}}" title="VIP-Mail", label add=["VIP"] remove=["Neu"] WITH max_per_message=1, window="1d", rate_max=20, rate_per="1h"`,
		`WHEN tag.added IN tag:"Projekt/Kunde-A" IF from:(anna OR max) THEN add_to_project "HB-2026" role="Korrespondenz"`,
		`WHEN project.status_changed IN project:"HB-2026", status:"done" THEN create_task "Abrechnen: {{var \"x\"}}" due_in="3d"`,
		`WHEN searchfolder.matched IN searchfolder:"Kunden" THEN tag "Kunde" "Neu", webhook "https://n8n.example/hook" headers={X-Token="abc", y="z"} include_body=true`,
		"WHEN mail.received IF from:x THEN move \"A\" if=`mail.size > mb(1)` continue_on_error=true, delete permanent=true WITH run_location=\"server\", allow_decrypted_pgp=true",
		`WHEN mail.received IN searchfolder:"Kunden" THEN create_project_from_template "Anfrage" "Anfrage {{absender}}" due_in="14d" link_item=false`,
		`WHEN schedule.tick IN schedule:"daily" THEN create_search_folder "Offen" "is:unread date:>now-7d" scopes=["mail", "chat"] chat_folder=true`,
		`WHEN chat.received IN channel:"signal" THEN set_var "a" "b", run_workflow "other"`,
		`RULE "" "Nur Name" DISABLED WHEN manual IN name:"Button" THEN flag "seen" "flagged"`,
	}
	for _, src := range cases {
		w, err := ParseRule(src, nil)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		out, err := FormatRule(w, nil)
		if err != nil {
			t.Fatalf("format %q: %v", src, err)
		}
		w2, err := ParseRule(out, nil)
		if err != nil {
			t.Fatalf("reparse %q: %v", out, err)
		}
		if !reflect.DeepEqual(w, w2) {
			t.Fatalf("round trip differs:\n%s\n%s\n%#v\n%#v", src, out, w, w2)
		}
		// YAML round trip of the parsed workflow keeps the rule text.
		yb, err := w.YAML()
		if err != nil {
			t.Fatal(err)
		}
		w3, err := Parse(yb)
		if err != nil {
			t.Fatalf("yaml %s: %v", yb, err)
		}
		out3, err := FormatRule(w3, nil)
		if err != nil || out3 != out {
			t.Fatalf("via YAML:\n%s\n%s\n%v", out, out3, err)
		}
	}
}

func TestFormatRuleFromYAML(t *testing.T) {
	for _, y := range []string{invoiceWF, `id: wait
name: Warten
trigger: {event: mail.sent}
actions:
  - {type: remind_no_reply, after: 3d}
options:
  loop_protection: {max_per_message: 1, window: 24h}
`} {
		w := mustParse(t, y)
		out, err := FormatRule(w, nil)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseRule(out, nil)
		if err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		// Durations come back as strings; compare through YAML.
		a, _ := w.YAML()
		b, _ := back.YAML()
		wa, _ := Parse(a)
		wb, _ := Parse(b)
		if !reflect.DeepEqual(normalizeParams(wa), normalizeParams(wb)) {
			t.Fatalf("lossy:\n%s\n%s\n%s", out, a, b)
		}
	}
	// Structured rules become expressions (documented limit).
	w := mustParse(t, "id: r\nname: R\ntrigger: {event: mail.received}\ncondition: {all: [{field: mail.subject, op: contains, value: x}]}\nactions: [{type: archive}]\n")
	out, err := FormatRule(w, nil)
	if err != nil || !strings.Contains(out, "IF `(lower(mail.subject) contains \"x\")`") {
		t.Fatalf("structured rule: %q %v", out, err)
	}
	// Unrepresentable values.
	w.Condition = Condition{Expr: "mail.subject == `x`"}
	if _, err := FormatRule(w, nil); err == nil {
		t.Fatal("backtick in expression accepted")
	}
}

func normalizeParams(w *Workflow) *Workflow {
	for i := range w.Actions {
		for k, v := range w.Actions[i].Params {
			if d, ok := v.(string); ok {
				if dur, err := ParseDuration(d); err == nil && strings.ContainsAny(d, "smhdw") {
					w.Actions[i].Params[k] = time.Duration(dur).String()
				}
			}
		}
	}
	return w
}

func TestParseRuleErrors(t *testing.T) {
	cases := []struct {
		src      string
		line     int
		col      int
		contains string
	}{
		{`THEN archive`, 1, 1, "expected WHEN"},
		{`WHEN`, 1, 5, "trigger event"},
		{`WHEN mail.received archive`, 1, 20, "expected THEN"},
		{`WHEN mail.received IN acount:x THEN archive`, 1, 23, `did you mean "account"`},
		{`WHEN mail.received IN account: "x" THEN archive`, 1, 23, "missing value"},
		{`WHEN mail.received IF from:( THEN archive`, 1, 28, "query"},
		{`WHEN mail.received IF from:x`, 1, 29, "expected THEN"},
		{`WHEN mail.received IF THEN archive`, 1, 20, "empty condition"},
		{"WHEN mail.received IF `mail.size > 1 THEN archive", 1, 23, "unterminated expression"},
		{`WHEN mail.received THEN move "a" "b"`, 1, 25, "too many arguments"},
		{`WHEN mail.received THEN moev "a"`, 1, 25, `did you mean "move"`},
		{`WHEN mail.received THEN move "a`, 1, 30, "unterminated string"},
		{`WHEN mail.received THEN tag [a, b`, 1, 34, "expected , or ]"},
		{`WHEN mail.received THEN archive WITH speed=1`, 1, 38, "unknown option"},
		{"WHEN mail.received\nTHEN move \"a\" folder=\"b\"", 2, 6, "given twice"},
		{`WHEN mail.received THEN archive extra`, 1, 25, "too many arguments"},
	}
	for _, c := range cases {
		_, err := ParseRule(c.src, nil)
		var se *RuleSyntaxError
		if !errors.As(err, &se) {
			t.Fatalf("%q: error %v", c.src, err)
		}
		if se.Line != c.line || se.Col != c.col || !strings.Contains(se.Msg, c.contains) {
			t.Errorf("%q: %d:%d %s; want %d:%d %q", c.src, se.Line, se.Col, se.Msg, c.line, c.col, c.contains)
		}
	}
}

func FuzzParseRule(f *testing.F) {
	for _, s := range []string{
		`WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment THEN tag "Kunde", move "Kunden/Eingang", notify signal "x"`,
		"RULE a \"b\" ENABLED WHEN tag.added IF `tag.path == \"x\"` THEN untag tags=[\"a\", \"b\"] WITH rate_max=3",
		`WHEN x THEN webhook "u" headers={a="b"}`, `WHEN`, `WHEN a IF "x THEN y`, `WHEN a THEN b [`, "WHEN a THEN b {c=}",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		w, err := ParseRule(src, nil)
		if err != nil {
			var se *RuleSyntaxError
			if !errors.As(err, &se) || se.Line < 1 || se.Col < 1 || se.Offset < 0 || se.Offset > len(src) {
				t.Fatalf("bad error %v for %q", err, src)
			}
			return
		}
		out, err := FormatRule(w, nil)
		if err != nil {
			return // not representable (e.g. floats); parsing succeeded
		}
		w2, err := ParseRule(out, nil)
		if err != nil {
			t.Fatalf("reparse of %q (from %q): %v", out, src, err)
		}
		out2, err := FormatRule(w2, nil)
		if err != nil || out2 != out {
			t.Fatalf("not stable:\n%q\n%q\n%v", out, out2, err)
		}
	})
}
