// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
)

func evalExpr(t *testing.T, src string, ev events.Event, now time.Time) bool {
	t.Helper()
	p, err := CompileExpr(src)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	ok, err := p.Eval(NewEnv(ev, now, map[string]string{"x": "1"}))
	if err != nil {
		t.Fatalf("eval %q: %v", src, err)
	}
	return ok
}

func TestExprConditions(t *testing.T) {
	ev := mailEvent("k1", "Rechnung@Telekom.DE", "Ihre Rechnung Oktober")
	ev.Mail.Cc = []domain.Address{{Email: "boss@firma.de"}}
	ev.Mail.Size = 3 << 20
	ev.Mail.PGP.Signed = true
	ev.Mail.Category = "rechnung"
	now := time.Date(2026, 11, 7, 9, 15, 0, 0, time.UTC) // Saturday
	cases := map[string]bool{
		`mail.from == "rechnung@telekom.de"`:                          true,
		`mail.fromDomain == "telekom.de"`:                             true,
		`domain(mail.from) endsWith "telekom.de"`:                     true,
		`mail.subject contains "Rechnung"`:                            true,
		`icontains(mail.subject, "RECHNUNG")`:                         true,
		`mail.subject matches "(?i)^ihre rechnung"`:                   true,
		`"boss@firma.de" in mail.cc`:                                  true,
		`"me@firma.de" in mail.recipients`:                            true,
		`any(mail.attachments, {.mime == "application/pdf"})`:         true,
		`any(mail.attachments, {glob("rechnung*.pdf", .name)})`:       true,
		`anyGlob(mail.attachmentNames, "*.zip")`:                      false,
		`mail.size > mb(2) && mail.size < mb(4)`:                      true,
		`mail.maxAttachmentSize < kb(200)`:                            true,
		`mail.isInvoice && mail.category == "rechnung"`:               true,
		`mail.pgp.signed && !mail.pgp.encrypted`:                      true,
		`mail.headers["list-id"] != ""`:                               true,
		`containsAny(mail.body, ["rechnung", "invoice"])`:             true,
		`mail.account == "Work" && mail.folder == "INBOX"`:            true,
		`time.weekend && time.weekday == 6 && time.hour == 9`:         true,
		`time.clock >= "08:00" && time.clock < "18:00"`:               true,
		`time.date == "2026-11-07"`:                                   true,
		`vars.x == "1" && event == "mail.received"`:                   true,
		`chat.text == "" && calendar.summary == ""`:                   true,
		`len(mail.to) == 1 && mail.to[0] == "me@firma.de"`:            true,
		`mail.date.Year() == 2026`:                                    true,
		`not (mail.subject contains "Mahnung")`:                       true,
		`mail.from in ["a@b.de", "rechnung@telekom.de"]`:              true,
		`noreply.daysWaited == 0 && file.size == 0 && schedule == ""`: true,
	}
	for src, want := range cases {
		if got := evalExpr(t, src, ev, now); got != want {
			t.Errorf("%s: got %v, want %v", src, got, want)
		}
	}
}

func TestExprChatAndDraft(t *testing.T) {
	chat := events.Event{Type: events.ChatReceived, Chat: &events.Chat{Channel: "signal", Sender: "Anna", Text: "Bitte zurückrufen", Group: false}}
	if !evalExpr(t, `chat.channel == "signal" && chat.sender == "Anna" && chat.text contains "zurückrufen" && !chat.group`, chat, t0) {
		t.Error("chat condition")
	}
	draft := events.Event{Type: events.MailBeforeSend, Draft: &events.Draft{
		Identity: events.Identity{Email: "me@firma.de"}, To: []domain.Address{{Email: "x@gmail.com"}},
		Subject: "Angebot", Attachments: []events.Attachment{{Name: "big.zip", Size: 20 << 20}}, Encrypt: true,
	}}
	if !evalExpr(t, `mail.from == "me@firma.de" && mail.maxAttachmentSize > mb(10) && mail.pgp.encrypted && "x@gmail.com" in mail.to`, draft, t0) {
		t.Error("draft condition")
	}
}

func TestExprCompileErrors(t *testing.T) {
	for _, src := range []string{
		`mail.subjekt == "x"`,   // unknown field
		`mail.size == "big"`,    // type mismatch
		`mail.subject`,          // not bool
		`os.Exit(1)`,            // no such thing
		`mail.subject contains`, // syntax
	} {
		if _, err := CompileExpr(src); err == nil {
			t.Errorf("%q: want compile error", src)
		}
	}
	// Size limit.
	big := strings.Repeat("mail.isInvoice || ", 3000) + "true"
	if _, err := CompileExpr(big); err == nil {
		t.Error("huge expression should be rejected")
	}
}

func TestEmptyConditionIsTrue(t *testing.T) {
	p, err := Condition{}.Compile()
	if err != nil || p != nil {
		t.Fatal(p, err)
	}
	ok, err := p.Eval(&Env{})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestRuleBuilder(t *testing.T) {
	ev := mailEvent("k1", "rechnung@mail.telekom.de", "Ihre Rechnung \"Oktober\"\n")
	ev.Mail.Size = 12 << 20
	ev.Mail.Labels = []string{"Wichtig"}
	cases := []struct {
		r    Rule
		want bool
	}{
		{Rule{Field: "mail.subject", Op: "contains", Value: "RECHNUNG"}, true},
		{Rule{Field: "mail.subject", Op: "contains", Value: `"Oktober"`}, true},
		{Rule{Field: "mail.subject", Op: "not_contains", Value: "Mahnung"}, true},
		{Rule{Field: "mail.subject", Op: "starts_with", Value: "ihre"}, true},
		{Rule{Field: "mail.subject", Op: "matches", Value: `Rechnung\s+"`}, true},
		{Rule{Field: "mail.from", Op: "domain_is", Value: "telekom.de"}, true},
		{Rule{Field: "mail.from", Op: "domain_is", Value: "elekom.de"}, false},
		{Rule{Field: "mail.from", Op: "in", Value: []any{"a@b.c", "Rechnung@mail.telekom.de"}}, true},
		{Rule{Field: "mail.from", Op: "not_in", Value: []any{"a@b.c"}}, true},
		{Rule{Field: "mail.to", Op: "contains", Value: "firma.de"}, true},
		{Rule{Field: "mail.to", Op: "eq", Value: "ME@firma.de"}, true},
		{Rule{Field: "mail.to", Op: "ne", Value: "me@firma.de"}, false},
		{Rule{Field: "mail.to", Op: "domain_is", Value: "@firma.de"}, true},
		{Rule{Field: "mail.cc", Op: "empty"}, true},
		{Rule{Field: "mail.attachmentNames", Op: "ends_with", Value: ".pdf"}, true},
		{Rule{Field: "mail.labels", Op: "count_gt", Value: 0}, true},
		{Rule{Field: "mail.size", Op: "gt", Value: "10MB"}, true},
		{Rule{Field: "mail.size", Op: "lte", Value: 1024}, false},
		{Rule{Field: "mail.attachmentCount", Op: "eq", Value: 1}, true},
		{Rule{Field: "mail.accountId", Op: "in", Value: []any{1, 2}}, true},
		{Rule{Field: "mail.isInvoice", Op: "is_true"}, true},
		{Rule{Field: "mail.pgp.encrypted", Op: "eq", Value: false}, true},
		{Rule{Field: "mail.headers.List-Id", Op: "contains", Value: "news"}, true},
		{Rule{Field: "vars.missing", Op: "empty"}, true},
		{Rule{All: []Rule{
			{Field: "mail.isInvoice", Op: "is_true"},
			{Not: &Rule{Field: "mail.folder", Op: "eq", Value: "spam"}},
			{Any: []Rule{{Field: "mail.size", Op: "lt", Value: 1}, {Field: "time.hour", Op: "gte", Value: 0}}},
		}}, true},
		{Rule{Any: []Rule{{Field: "mail.isInvoice", Op: "is_false"}, {Field: "mail.subject", Op: "empty"}}}, false},
	}
	for i, c := range cases {
		src, err := c.r.Expr()
		if err != nil {
			t.Errorf("case %d: %v", i, err)
			continue
		}
		if got := evalExpr(t, src, ev, t0); got != c.want {
			t.Errorf("case %d (%s): got %v want %v", i, src, got, c.want)
		}
	}
}

func TestRuleBuilderErrors(t *testing.T) {
	cases := map[string]Rule{
		"exactly one":          {},
		"unknown field":        {Field: "mail.colour", Op: "eq", Value: "x"},
		"unknown operator":     {Field: "mail.size", Op: "contains", Value: "x"},
		"invalid regular":      {Field: "mail.subject", Op: "matches", Value: "("},
		"must be a number":     {Field: "mail.size", Op: "gt", Value: true},
		"value must be a list": {Field: "mail.from", Op: "in", Value: "x"},
		"true or false":        {Field: "mail.isInvoice", Op: "eq", Value: "yes"},
		"condition.all[1]":     {All: []Rule{{Field: "mail.isInvoice", Op: "is_true"}, {Field: "nope", Op: "eq"}}},
		"missing name":         {Field: "vars.", Op: "eq", Value: "x"},
	}
	for want, r := range cases {
		_, err := r.Expr()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
	if len(RuleFields()) < 30 || len(RuleOps[KindString]) == 0 {
		t.Error("field catalogue looks incomplete")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"10": 10, "1KB": 1024, "2.5 MB": 2621440, "1gib": 1 << 30, "7b": 7} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	if _, err := ParseSize("ten"); err == nil {
		t.Error("want error")
	}
}
