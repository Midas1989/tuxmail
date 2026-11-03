// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"strings"
	"testing"
)

// complete runs Complete at the position of "|" in src.
func complete(t *testing.T, src string, mode CompletionMode) []Completion {
	t.Helper()
	i := strings.Index(src, "|")
	if i < 0 {
		t.Fatalf("no cursor in %q", src)
	}
	return Complete(src[:i]+src[i+1:], i, mode, nil)
}

func labels(cs []Completion) map[string]CompletionKind {
	out := map[string]CompletionKind{}
	for _, c := range cs {
		out[c.Label] = c.Kind
	}
	return out
}

func TestCompleteRule(t *testing.T) {
	cases := []struct {
		src     string
		want    []string // labels that must be offered
		notWant []string
		kind    CompletionKind // kind of want[0]
	}{
		{"|", []string{"WHEN", "RULE"}, nil, CompleteKeyword},
		{"WHEN |", []string{"mail.received", "tag.added", "searchfolder.matched"}, []string{"THEN"}, CompleteTrigger},
		{"WHEN mail.re|", []string{"mail.received"}, []string{"mail.sent", "tag.added"}, CompleteTrigger},
		{"WHEN mail.received |", []string{"IN", "IF", "THEN"}, nil, CompleteKeyword},
		{"WHEN mail.received IN |", []string{"account:", "folder:", "searchfolder:"}, nil, CompleteParam},
		{"WHEN project.status_changed IN status:|", []string{"active", "done"}, nil, CompleteValue},
		{`WHEN mail.received IN account:"Firma" |`, []string{"IF", "THEN", ","}, nil, CompleteKeyword},
		{"WHEN mail.received IF |", []string{"from:", "has:", "NOT", "("}, []string{"AND"}, CompleteField},
		{"WHEN mail.received IF fr|", []string{"from:"}, []string{"has:"}, CompleteField},
		{"WHEN mail.received IF from:x |", []string{"AND", "OR", "THEN", "subject:"}, nil, CompleteOperator},
		{"WHEN mail.received IF (from:x |", []string{")", "AND"}, nil, CompleteOperator},
		{"WHEN mail.received IF has:|", []string{"attachment", "calendar"}, []string{"from:"}, CompleteValue},
		{"WHEN mail.received IF is:un|", []string{"unread"}, []string{"read"}, CompleteValue},
		{"WHEN mail.received IF `mail.fr|", []string{"mail.from", "mail.fromDomain"}, []string{"chat.text"}, CompleteField},
		{"WHEN mail.received IF from:x THEN |", []string{"move", "tag", "add_to_project", "notify"}, nil, CompleteAction},
		{`WHEN mail.received THEN tag "a", |`, []string{"move", "archive"}, nil, CompleteAction},
		{`WHEN mail.received THEN move "a" |`, []string{"if=", ",", "WITH"}, []string{"folder="}, CompleteParam},
		{"WHEN project.due_soon THEN set_project_status |", []string{"active", "on_hold", "project="}, nil, CompleteValue},
		{"WHEN mail.received THEN delete per|", []string{"permanent="}, nil, CompleteParam},
		{`WHEN mail.received THEN notify title="x" signal |`, []string{"if=", ","}, []string{"title=", "channel=", "desktop"}, CompleteParam},
		{"WHEN mail.received THEN webhook \"u\" method=|", []string{"POST", "PUT"}, nil, CompleteValue},
		{"WHEN mail.received THEN archive WITH |", []string{"max_per_message=", "run_location="}, nil, CompleteParam},
		{"WHEN mail.received THEN archive WITH run_location=|", []string{"local", "server"}, nil, CompleteValue},
	}
	for _, c := range cases {
		got := labels(complete(t, c.src, ModeRule))
		for i, w := range c.want {
			k, ok := got[w]
			if !ok {
				t.Errorf("%q: missing %q in %v", c.src, w, got)
				continue
			}
			if i == 0 && k != c.kind {
				t.Errorf("%q: %q has kind %s, want %s", c.src, w, k, c.kind)
			}
		}
		for _, w := range c.notWant {
			if _, ok := got[w]; ok {
				t.Errorf("%q: unexpected %q", c.src, w)
			}
		}
	}
	// The replaced range is the word being typed.
	cs := complete(t, "WHEN mail.re|", ModeRule)
	if len(cs) == 0 || cs[0].From != 5 || cs[0].To != 12 || cs[0].Insert != "mail.received" {
		t.Fatalf("range: %+v", cs)
	}
}

func TestCompleteYAML(t *testing.T) {
	cases := []struct {
		src     string
		want    []string
		notWant []string
	}{
		{"|", []string{"id", "name", "trigger", "actions"}, nil},
		{"id: x\nname: y\n|", []string{"trigger", "condition"}, []string{"id", "name"}},
		{"trigger:\n  event: |", []string{"mail.received", "project.due_soon"}, nil},
		{"trigger:\n  event: tag.added\n  |", []string{"tags", "search_folders"}, []string{"event", "folders", "channels"}},
		{"trigger:\n  event: mail.received\n  |", []string{"folders", "account_names"}, []string{"tags"}},
		{"actions:\n  - type: |", []string{"move", "tag", "create_search_folder"}, nil},
		{"actions:\n  - type: move\n    |", []string{"folder", "if", "continue_on_error"}, []string{"type"}},
		{"actions:\n  - type: move\n    folder: x\n  - type: tag\n    |", []string{"tags", "create"}, []string{"folder"}},
		{"actions:\n  - type: set_project_status\n    status: |", []string{"active", "done"}, nil},
		{"actions:\n  - type: delete\n    permanent: |", []string{"true", "false"}, nil},
		{"actions:\n  - type: create_search_folder\n    query: from:x |", []string{"AND", "has:"}, nil},
		{"condition: mail.su|", []string{"mail.subject"}, []string{"mail.from"}},
		{"condition:\n  |", []string{"all", "any", "query", "field"}, nil},
		{"condition:\n  all:\n    - field: mail.size\n      op: |", []string{"gt", "lte"}, []string{"contains"}},
		{"condition:\n  query: \"is:|", []string{"unread", "flagged"}, nil},
		{"options:\n  |", []string{"rate_limit", "run_location"}, nil},
		{"options:\n  run_location: |", []string{"local", "server"}, nil},
		{"options:\n  loop_protection:\n    |", []string{"max_per_message", "window"}, []string{"max"}},
		{"trigger:\n  event: project.status_changed\n  statuses:\n    - |", []string{"done", "archived"}, nil},
	}
	for _, c := range cases {
		got := labels(complete(t, c.src, ModeYAML))
		for _, w := range c.want {
			if _, ok := got[w]; !ok {
				t.Errorf("%q: missing %q in %v", c.src, w, got)
			}
		}
		for _, w := range c.notWant {
			if _, ok := got[w]; ok {
				t.Errorf("%q: unexpected %q", c.src, w)
			}
		}
	}
}
