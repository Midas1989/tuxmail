// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

func forwardWorkflow(name, to string, enabled bool) Workflow {
	return Workflow{Name: name, Enabled: enabled, Trigger: WorkflowTrigger{Event: "mail.received"},
		Actions: []WorkflowAction{{Type: "forward", Params: map[string]any{"to": []any{to}}}}}
}

func wantConfirmation(t *testing.T, what string, err error) {
	t.Helper()
	if e := AsError(err); e == nil || e.Code != CodeConfirmationRequired || e.Status != 409 {
		t.Fatalf("%s: err = %v, want 409 %s", what, err, CodeConfirmationRequired)
	}
}

// NH-03: saving is not confirmation. An enabled workflow with external
// effects needs confirmExternal; unchanged re-saves do not; changed targets do.
func TestWorkflowExternalEffectNeedsExplicitConfirmation(t *testing.T) {
	a := newIsolationApp(t, Options{})
	ctx := context.Background()

	_, err := a.SaveWorkflow(ctx, forwardWorkflow("Spiegel", "boss@example.org", true))
	wantConfirmation(t, "enabled forward without flag", err)
	if ws, _ := a.ListWorkflows(ctx, 0); len(ws) != 0 {
		t.Fatalf("stored without confirmation: %+v", ws)
	}

	// Disabled: no confirmation needed, but enabling later is.
	w, err := a.SaveWorkflow(ctx, forwardWorkflow("Spiegel", "boss@example.org", false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.SetWorkflowEnabled(ctx, w.ID, true, false)
	wantConfirmation(t, "enabling without flag", err)
	if w, err = a.SetWorkflowEnabled(ctx, w.ID, true, true); err != nil || !w.Enabled {
		t.Fatalf("enabling with flag: %+v, %v", w, err)
	}

	// Unchanged outside TuxMail: renaming needs no new confirmation.
	again := forwardWorkflow("Spiegel umbenannt", "boss@example.org", true)
	again.ID = w.ID
	if _, err := a.SaveWorkflow(ctx, again); err != nil {
		t.Fatalf("unchanged external effect: %v", err)
	}
	// A different target, a broader condition or a new external action need it.
	changed := forwardWorkflow("Spiegel", "evil@example.net", true)
	changed.ID = w.ID
	_, err = a.SaveWorkflow(ctx, changed)
	wantConfirmation(t, "changed forward address", err)
	changed = again
	changed.Condition = &WorkflowCondition{Expr: `mail.from == "x@example.org"`}
	_, err = a.SaveWorkflow(ctx, changed)
	wantConfirmation(t, "changed condition", err)
	changed = again
	changed.Actions = append(changed.Actions, WorkflowAction{Type: "webhook", Params: map[string]any{"url": "https://hook.example.net/x"}})
	_, err = a.SaveWorkflow(ctx, changed)
	wantConfirmation(t, "added webhook", err)
	changed.ConfirmExternal = true
	if _, err := a.SaveWorkflow(ctx, changed); err != nil {
		t.Fatalf("confirmed change: %v", err)
	}

	// Disabling needs nothing; the flag is input only.
	off, err := a.SetWorkflowEnabled(ctx, w.ID, false, false)
	if err != nil || off.Enabled || off.ConfirmExternal {
		t.Fatalf("disable: %+v, %v", off, err)
	}

	// Workflows without external effects keep working without the flag.
	plain := Workflow{Name: "Archiv", Enabled: true, Trigger: WorkflowTrigger{Event: "mail.received"},
		Actions: []WorkflowAction{{Type: "archive", Params: map[string]any{}}}}
	if _, err := a.SaveWorkflow(ctx, plain); err != nil {
		t.Fatalf("non-external workflow: %v", err)
	}
}

// NH-03: on a team mailbox the acting member is recorded as author and in the
// audit log with the external targets; the managers are only told about
// enabled external effects.
func TestWorkflowTeamActorAuditAndAuthor(t *testing.T) {
	a := newIsolationApp(t, Options{})
	type rec struct {
		action, details string
		notify          bool
	}
	var got []rec
	ctx := WithWorkflowActor(context.Background(), WorkflowActor{Name: "Anna", Record: func(_ context.Context, action, details string, notify bool) {
		got = append(got, rec{action, details, notify})
	}})
	w, err := a.SaveWorkflow(ctx, func() Workflow {
		w := forwardWorkflow("Spiegel", "boss@example.org", true)
		w.ConfirmExternal = true
		return w
	}())
	if err != nil {
		t.Fatal(err)
	}
	if w.Author != "Anna" {
		t.Fatalf("author = %q", w.Author)
	}
	if len(got) != 1 || got[0].action != "save" || !got[0].notify ||
		!strings.Contains(got[0].details, "workflow="+w.ID) || !strings.Contains(got[0].details, "boss@example.org") {
		t.Fatalf("audit records: %+v", got)
	}
	// A disabled workflow is audited but nobody is notified.
	got = nil
	if _, err := a.SetWorkflowEnabled(ctx, w.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].action != "disable" || got[0].notify {
		t.Fatalf("disable records: %+v", got)
	}
	got = nil
	if err := a.DeleteWorkflow(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].action != "delete" {
		t.Fatalf("delete records: %+v", got)
	}
	// Without an actor (personal mailbox) nothing is recorded and no author is kept.
	w2, err := a.SaveWorkflow(context.Background(), Workflow{Name: "Archiv", Enabled: true, Trigger: WorkflowTrigger{Event: "mail.received"},
		Actions: []WorkflowAction{{Type: "archive", Params: map[string]any{}}}})
	if err != nil || w2.Author != "" {
		t.Fatalf("%+v, %v", w2, err)
	}
}

// NH-10: the out-of-office forwarding is validated and needs the same
// explicit confirmation as a forwarding workflow.
func TestVacationForwardToValidatedAndConfirmed(t *testing.T) {
	for _, bad := range []string{"", "no-at-sign", "a@b", "x@evil.example\r\nBcc: spy@evil.example", "Boss <boss@example.org>", "a b@example.org", "a@b@example.org"} {
		s := vacation.Settings{Enabled: true, ExternalText: "away", ForwardTo: []string{bad}}
		if err := s.Validate(); err == nil {
			t.Errorf("forwardTo %q accepted", bad)
		}
	}
	if err := (vacation.Settings{Enabled: true, ExternalText: "away", ForwardTo: []string{"deputy@example.org"}}).Validate(); err != nil {
		t.Fatalf("valid address refused: %v", err)
	}

	a := newIsolationApp(t, Options{})
	ctx := context.Background()
	s := vacation.Settings{Enabled: true, ExternalText: "away", ForwardTo: []string{"deputy@example.org"}, End: time.Now().Add(24 * time.Hour)}
	_, err := a.SaveVacationSettings(ctx, s)
	wantConfirmation(t, "forwarding without flag", err)
	if ws, _ := a.ListWorkflows(ctx, 0); len(ws) != 0 {
		t.Fatalf("workflow installed without confirmation: %+v", ws)
	}
	s.ConfirmExternal = true
	v, err := a.SaveVacationSettings(ctx, s)
	if err != nil || v.ConfirmExternal {
		t.Fatalf("confirmed forwarding: %+v, %v", v, err)
	}
	// Unchanged forwarding stays; another address needs the confirmation again.
	s.ConfirmExternal = false
	if _, err := a.SaveVacationSettings(ctx, s); err != nil {
		t.Fatalf("unchanged forwarding: %v", err)
	}
	s.ForwardTo = []string{"other@example.net"}
	_, err = a.SaveVacationSettings(ctx, s)
	wantConfirmation(t, "changed forwarding address", err)
	// Without forwarding the out-of-office reply needs no confirmation.
	s.ForwardTo = nil
	if _, err := a.SaveVacationSettings(ctx, s); err != nil {
		t.Fatalf("reply only: %v", err)
	}
}
