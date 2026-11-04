// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

func commandNames(cs []Command, out *[]string) {
	for _, c := range cs {
		*out = append(*out, c.Name)
		commandNames(c.Children, out)
	}
}

// AUDIT-C: values from workflows (conditions, folder names, redirect
// targets) and the vacation texts must stay string literals in the
// generated script; they must never add commands such as discard or
// redirect.
func TestSieveInjection(t *testing.T) {
	payloads := []string{
		`x"; discard; #`,
		"x\"\r\ndiscard;\r\n#",
		`x\"; redirect "evil@example.org"; keep; #`,
		"text:\n.\ndiscard;\n",
		"a\\",
	}
	for _, p := range payloads {
		ws := []*workflow.Workflow{{ID: "w", Name: "w", Enabled: true, Trigger: workflow.Trigger{Event: events.MailReceived},
			Condition: workflow.Condition{Rule: &workflow.Rule{All: []workflow.Rule{
				{Field: "mail.subject", Op: "contains", Value: p},
				{Field: "mail.headers.x-" + "test", Op: "eq", Value: []any{p, p}},
			}}},
			Actions: []workflow.ActionSpec{{Type: "move", Params: map[string]any{"folder": p}}}}}
		res := Generate(ws, GenerateOptions{})
		if len(res.Included) != 1 {
			t.Fatalf("%q: not generated: %+v", p, res.Skipped)
		}
		check(t, p, res.Script, "fileinto")

		s := vacation.Settings{Enabled: true, Subject: p, ExternalText: p + "\n" + p + "\n.\n" + p, Identities: []string{"a@example.org"}}
		part, err := VacationPart(s, GenerateOptions{}, time.Now(), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		sec := Section{}
		sec.SetPart(PartVacation, part)
		check(t, p, sec.Render(), "vacation")
	}
}

func check(t *testing.T, payload, script, want string) {
	t.Helper()
	r := Parse(script, false)
	if r.Error != "" {
		t.Fatalf("%q: script does not parse: %s\n%s", payload, r.Error, script)
	}
	var names []string
	commandNames(r.Commands, &names)
	for _, n := range names {
		switch n {
		case "discard", "redirect", "keep", "reject", "ereject":
			t.Fatalf("%q: injected command %s:\n%s", payload, n, script)
		}
	}
	if !strings.Contains(strings.Join(names, " "), want) {
		t.Fatalf("%q: %s missing (%v):\n%s", payload, want, names, script)
	}
}
