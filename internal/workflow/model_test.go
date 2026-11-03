// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
)

const fullYAML = `id: invoices
name: Rechnungen
description: Test workflow
enabled: true
trigger:
  event: mail.received
  accounts: [1, 2]
  folders: [inbox]
condition: mail.isInvoice && mail.fromDomain endsWith "telekom.de"
actions:
  - type: archive_pdf
    target: nextcloud
    path: /Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}.pdf
  - type: move
    folder: Rechnungen/{{jahr}}
    if: mail.size < mb(5)
    continue_on_error: true
  - type: flag
    add: [seen, flagged]
  - type: webhook
    url: https://n8n.example.com/hook
    timeout: 5s
options:
  loop_protection:
    max_per_message: 2
    window: 3d
    max_depth: 4
  rate_limit:
    max: 10
    per: 1h
  run_location: server
`

func TestYAMLRoundTrip(t *testing.T) {
	w := mustParse(t, fullYAML)
	if err := w.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if w.Options.LoopProtection.Window.D() != 72*time.Hour || w.Options.RateLimit.Per.D() != time.Hour {
		t.Fatalf("durations: %+v", w.Options)
	}
	if w.Actions[1].If == "" || !w.Actions[1].ContinueOnError || w.Actions[1].Params["folder"] != "Rechnungen/{{jahr}}" {
		t.Fatalf("action spec: %+v", w.Actions[1])
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatal(err)
	}
	w2 := mustParse(t, string(out))
	if !reflect.DeepEqual(w, w2) {
		t.Fatalf("round trip differs:\n%s\n%#v\n%#v", out, w, w2)
	}
	out2, _ := w2.YAML()
	if string(out) != string(out2) {
		t.Fatalf("not stable:\n%s\n---\n%s", out, out2)
	}
}

func TestYAMLRoundTripRule(t *testing.T) {
	y := `id: rule
name: Rule
enabled: false
trigger: {event: mail.received}
condition:
  all:
    - {field: mail.fromDomain, op: domain_is, value: example.com}
    - not: {field: mail.subject, op: contains, value: spam}
    - any:
        - {field: mail.size, op: gt, value: 10MB}
        - {field: mail.isInvoice, op: is_true}
actions:
  - type: archive
`
	w := mustParse(t, y)
	if w.Condition.Rule == nil || len(w.Condition.Rule.All) != 3 {
		t.Fatalf("rule not parsed: %+v", w.Condition)
	}
	if err := w.Validate(nil); err != nil {
		t.Fatal(err)
	}
	out, _ := w.YAML()
	w2 := mustParse(t, string(out))
	if !reflect.DeepEqual(w.Condition, w2.Condition) {
		t.Fatalf("rule round trip:\n%s", out)
	}
	if strings.Contains(string(out), "options") {
		t.Fatalf("empty options should be omitted:\n%s", out)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("id: x\nname: y\ntrigger: {event: manual}\nactions: []\nbogus: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestValidateMessages(t *testing.T) {
	y := `id: "Bad ID"
name: ""
trigger: {event: mail.exploded, channels: [signal]}
condition: mail.subject +
actions:
  - type: move
  - type: teleport
  - type: forward
    to: [not-an-address]
  - type: warn
    message: hi
  - type: move
    folder: x
    colour: red
`
	w := mustParse(t, y)
	err := w.Validate(nil)
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	want := []string{
		`id "Bad ID"`, "name: required", `trigger.event "mail.exploded": unknown`, "condition:",
		"actions[0] (move): folder: required", "actions[1] (teleport): unknown action type",
		`actions[2] (forward): to: "not-an-address" is not a mail address`,
		`actions[3] (warn): not allowed for trigger "mail.exploded"`,
		"actions[4] (move): field colour not found",
	}
	all := strings.Join(ve.Problems, "\n")
	for _, s := range want {
		if !strings.Contains(all, s) {
			t.Errorf("missing problem %q in:\n%s", s, all)
		}
	}
}

func TestValidateTriggerFilters(t *testing.T) {
	cases := map[string]string{
		"trigger: {event: schedule.tick}":                     "trigger.schedule: required",
		"trigger: {event: mail.received, channels: [signal]}": "trigger.channels: only allowed",
		"trigger: {event: chat.received, folders: [inbox]}":   "trigger.folders: only allowed",
		"trigger: {event: mail.received, schedule: daily}":    "trigger.schedule: only allowed",
	}
	for trig, want := range cases {
		w := mustParse(t, "id: t\nname: t\n"+trig+"\nactions: [{type: notify, text: hi}]\n")
		err := w.Validate(nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", trig, want, err)
		}
	}
}

func TestExternalEffects(t *testing.T) {
	w := mustParse(t, fullYAML)
	if got := w.ExternalEffects(nil); !reflect.DeepEqual(got, []string{"webhook"}) {
		t.Fatalf("external effects = %v", got)
	}
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"3d": 72 * time.Hour, "1w": 7 * 24 * time.Hour, "1d12h": 36 * time.Hour, "90m": 90 * time.Minute, "1w2d3h": 219 * time.Hour,
	} {
		d, err := ParseDuration(in)
		if err != nil || d.D() != want {
			t.Errorf("%s: %v %v", in, d.D(), err)
		}
		back, err := ParseDuration(d.String())
		if err != nil || back != d {
			t.Errorf("%s: string %q does not round trip", in, d.String())
		}
	}
	for _, bad := range []string{"", "x", "3x", "-1h", "d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if s := Duration(time.Hour).String(); s != "1h" {
		t.Errorf("1h renders as %q", s)
	}
}

func TestTriggerMatches(t *testing.T) {
	ev := mailEvent("k1", "a@b.de", "s")
	cases := []struct {
		t    Trigger
		want bool
	}{
		{Trigger{Event: events.MailReceived}, true},
		{Trigger{Event: events.MailSent}, false},
		{Trigger{Event: events.MailReceived, Folders: []string{"Inbox"}}, true},
		{Trigger{Event: events.MailReceived, Folders: []string{"Archive"}}, false},
		{Trigger{Event: events.MailReceived, Accounts: []int64{2}}, false},
		{Trigger{Event: events.MailReceived, Accounts: []int64{2, 1}}, true},
	}
	for i, c := range cases {
		if got := c.t.Matches(ev); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
	tick := events.Event{Type: events.ScheduleTick, Schedule: &events.Schedule{Name: "daily"}}
	if !(Trigger{Event: events.ScheduleTick, Schedule: "daily"}).Matches(tick) ||
		(Trigger{Event: events.ScheduleTick, Schedule: "hourly"}).Matches(tick) {
		t.Error("schedule matching")
	}
	chat := events.Event{Type: events.ChatReceived, Chat: &events.Chat{Channel: "Signal"}}
	if !(Trigger{Event: events.ChatReceived, Channels: []string{"signal"}}).Matches(chat) {
		t.Error("channel matching")
	}
}
