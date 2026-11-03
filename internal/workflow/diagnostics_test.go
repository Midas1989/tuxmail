// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"strings"
	"testing"
)

// pos marks the expected position with ^ in the line below the source line
// it refers to; see posOf.
type diagCase struct {
	name string
	src  string
	// want: code, path and the 1-based line and column of the first
	// diagnostic with that code.
	code, path string
	line, col  int
	msg        string // substring of the message
}

func TestValidateTextPositions(t *testing.T) {
	cases := []diagCase{
		{name: "yaml syntax", src: "id: x\nname: X\n  bad: indent\n", code: "yaml-syntax", line: 3, col: 1, msg: "mapping values"},
		{name: "unknown top key", src: "id: x\nname: X\ntriger: {event: mail.received}\nactions: [{type: archive}]\n",
			code: "unknown-key", path: "triger", line: 3, col: 1, msg: `did you mean "trigger"`},
		{name: "invalid id", src: "id: Bad Id\nname: X\ntrigger: {event: mail.received}\nactions: [{type: archive}]\n",
			code: "invalid-id", path: "id", line: 1, col: 5},
		{name: "unknown event", src: "id: x\nname: X\ntrigger:\n  event: mail.recieved\nactions: [{type: archive}]\n",
			code: "trigger", path: "trigger.event", line: 4, col: 10, msg: "unknown"},
		{name: "folders on chat", src: "id: x\nname: X\ntrigger:\n  event: chat.received\n  folders: [INBOX]\nactions: [{type: notify, text: hi}]\n",
			code: "trigger", path: "trigger.folders", line: 5, col: 12},
		{name: "expr error plain", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition: mail.subject == 'a' && mail.nope\nactions: [{type: archive}]\n",
			code: "expr", path: "condition", line: 4, col: 40, msg: "no field nope"},
		{name: "expr error quoted", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition: \"mail.size > mb(1) && mail.nope\"\nactions: [{type: archive}]\n",
			code: "expr", path: "condition", line: 4, col: 39},
		{name: "expr error literal block", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition: |\n  mail.size > 1 &&\n    mail.nope\nactions: [{type: archive}]\n",
			code: "expr", path: "condition", line: 6, col: 10},
		{name: "expr error folded block", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition: >-\n  mail.size > 1 &&\n  mail.nope\nactions: [{type: archive}]\n",
			code: "expr", path: "condition", line: 6, col: 8},
		{name: "query literal in expr", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition: 'query(\"from:anna AND (x\")'\nactions: [{type: archive}]\n",
			code: "query", path: "condition", line: 4, col: 34, msg: "missing ')'"},
		{name: "query rule", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition:\n  query: \"has:attachment AND size:>zz\"\nactions: [{type: archive}]\n",
			code: "query", path: "condition.query", line: 5, col: 36, msg: "invalid number"},
		{name: "rule unknown field", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition:\n  all:\n    - field: mail.subjekt\n      op: contains\n      value: x\nactions: [{type: archive}]\n",
			code: "rule", path: "condition.all[0].field", line: 6, col: 14, msg: `did you mean "mail.subject"`},
		{name: "rule bad op", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition:\n  any:\n    - {field: mail.size, op: contains, value: 1}\nactions: [{type: archive}]\n",
			code: "rule", path: "condition.any[0].op", line: 6, col: 30},
		{name: "rule two forms", src: "id: x\nname: X\ntrigger: {event: mail.received}\ncondition:\n  query: x\n  field: mail.subject\nactions: [{type: archive}]\n",
			code: "rule", path: "condition", line: 5, col: 3},
		{name: "unknown action", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: moev\n    folder: x\n",
			code: "unknown-action", path: "actions[0].type", line: 5, col: 11, msg: `did you mean "move"`},
		{name: "action not allowed", src: "id: x\nname: X\ntrigger: {event: chat.received}\nactions:\n  - type: move\n    folder: x\n",
			code: "action-trigger", path: "actions[0].type", line: 5, col: 11},
		{name: "unknown param", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: move\n    foldr: x\n",
			code: "unknown-key", path: "actions[0].foldr", line: 6, col: 5, msg: `did you mean "folder"`},
		{name: "missing param", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: move\n",
			code: "required", path: "actions[0].folder", line: 5, col: 11},
		{name: "enum param", src: "id: x\nname: X\ntrigger: {event: project.due_soon}\nactions:\n  - type: set_project_status\n    status: activ\n",
			code: "enum", path: "actions[0].status", line: 6, col: 13, msg: `did you mean "active"`},
		{name: "bool param", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: delete\n    permanent: yes please\n",
			code: "type", path: "actions[0].permanent", line: 6, col: 16},
		{name: "param via constructor", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: notify\n    channel: fax\n    text: hi\n",
			code: "param", path: "actions[0].channel", line: 6, col: 14},
		{name: "action if expr", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: archive\n    if: mail.size >\n",
			code: "expr", path: "actions[0].if", line: 6, col: 19},
		{name: "query param", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: create_search_folder\n    name: n\n    query: \"from:(a\"\n",
			code: "query", path: "actions[0].query", line: 7, col: 18},
		{name: "template param", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions:\n  - type: move\n    folder: \"{{nope}}\"\n",
			code: "param", path: "actions[0].folder", line: 6, col: 13},
		{name: "options", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions: [{type: archive}]\noptions:\n  run_location: cloud\n",
			code: "enum", path: "options.run_location", line: 6, col: 17},
		{name: "options duration", src: "id: x\nname: X\ntrigger: {event: mail.received}\nactions: [{type: archive}]\noptions:\n  rate_limit: {max: 3, per: soon}\n",
			code: "type", path: "options.rate_limit.per", line: 6, col: 29},
		{name: "missing name", src: "id: x\ntrigger: {event: mail.received}\nactions: [{type: archive}]\n",
			code: "required", path: "name", line: 1, col: 1},
		{name: "duplicate key", src: "id: x\nname: X\nname: Y\ntrigger: {event: mail.received}\nactions: [{type: archive}]\n",
			code: "duplicate-key", path: "name", line: 3, col: 1},
		{name: "external effect warning", src: "id: x\nname: X\nenabled: true\ntrigger: {event: mail.received}\nactions:\n  - type: forward\n    to: [a@b.de]\n",
			code: "external-effect", path: "actions[0].type", line: 6, col: 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := ValidateText(c.src, nil)
			for _, d := range ds {
				if d.Code != c.code {
					continue
				}
				if c.path != "" && d.Path != c.path {
					continue
				}
				if d.Line != c.line || d.Col != c.col {
					t.Fatalf("%s at %d:%d, want %d:%d (all: %v)", c.code, d.Line, d.Col, c.line, c.col, ds)
				}
				if c.msg != "" && !strings.Contains(d.Message, c.msg) {
					t.Fatalf("message %q does not contain %q", d.Message, c.msg)
				}
				if d.EndLine < d.Line || d.EndLine == d.Line && d.EndCol <= d.Col {
					t.Fatalf("bad range %+v", d)
				}
				return
			}
			t.Fatalf("no %s diagnostic at %q; got %v", c.code, c.path, ds)
		})
	}
}

func TestValidateTextValid(t *testing.T) {
	for _, src := range []string{invoiceWF, `id: tags
name: Kunden taggen
enabled: true
trigger:
  event: mail.received
  account_names: [Firma]
  search_folders: [Kunden]
condition:
  all:
    - query: "from:*@kunde.de AND has:attachment"
    - {field: mail.size, op: lt, value: 10MB}
actions:
  - type: tag
    tags: [Kunde]
  - type: add_to_project
    project: HB-2026
    if: mail.hasAttachments
options:
  rate_limit: {max: 10, per: 1h}
`} {
		ds := ValidateText(src, nil)
		if HasErrors(ds) {
			t.Fatalf("valid workflow has errors: %v", ds)
		}
		w := mustParse(t, src)
		if err := w.Validate(nil); err != nil {
			t.Fatalf("Validate disagrees: %v", err)
		}
	}
}

// TestValidateTextCoversValidate checks that every workflow rejected by
// Validate gets at least one error diagnostic.
func TestValidateTextCoversValidate(t *testing.T) {
	for _, src := range []string{
		"id: x\nname: X\ntrigger: {event: schedule.tick}\nactions: [{type: archive}]\n",
		"id: x\nname: X\ntrigger: {event: mail.received, schedule: s}\nactions: [{type: archive}]\n",
		"id: x\nname: X\ntrigger: {event: mail.received}\nactions: []\n",
		"id: x\nname: X\ntrigger: {event: mail.received}\nactions: [{type: forward, to: []}]\n",
		"id: x\nname: \"\"\ntrigger: {event: mail.received}\nactions: [{type: archive}]\n",
		"id: x\nname: X\ntrigger: {event: mail.received}\nactions: [{type: archive}]\noptions: {rate_limit: {max: -1}}\n",
	} {
		w, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if w.Validate(nil) == nil {
			t.Fatalf("test case is valid: %s", src)
		}
		if ds := ValidateText(src, nil); !HasErrors(ds) {
			t.Errorf("no error diagnostic for %q", src)
		}
	}
}
