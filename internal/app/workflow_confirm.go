// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Confirmation and attribution of workflows with external effect (re-audit
// 2026-11-05, NH-03 and NH-10).
//
// An enabled workflow whose actions act outside TuxMail (forward, webhook,
// automatic reply ...) is only stored when the request says so explicitly
// (Workflow.ConfirmExternal): the API no longer treats every save as
// confirmed. Saving an unchanged workflow again needs no new confirmation.
// For team mailboxes the acting member is put into the context by the
// server (WithWorkflowActor); every change is then audited with the external
// targets, the author is kept on the workflow and the other managers are
// told.

// CodeConfirmationRequired is the error code of a refused save: the client
// must show the effects to the user and repeat the request with
// confirmExternal set.
const CodeConfirmationRequired = "confirmation_required"

func confirmationRequired(effects []string) error {
	return Errorf(http.StatusConflict, CodeConfirmationRequired,
		"this workflow acts outside TuxMail (%s); it has to be confirmed explicitly (confirmExternal)", strings.Join(effects, ", "))
}

// targetParams are the parameters of external actions that say where data
// goes (forward, webhook).
var targetParams = []string{"to", "cc", "bcc", "url", "method", "include_body"}

// externalTargets describes the external actions of w for the audit log,
// e.g. `forward(to=[boss@example.org])`, `webhook(url=https://x.example/h)`.
func externalTargets(w *workflow.Workflow, reg *workflow.Registry) []string {
	ext := map[string]bool{}
	for _, t := range w.ExternalEffects(reg) {
		ext[t] = true
	}
	var out []string
	for _, act := range w.Actions {
		if !ext[act.Type] {
			continue
		}
		var parts []string
		for _, k := range targetParams {
			if v, ok := act.Params[k]; ok {
				parts = append(parts, fmt.Sprintf("%s=%v", k, v))
			}
		}
		out = append(out, act.Type+"("+strings.Join(parts, " ")+")")
	}
	return out
}

// externalFingerprint identifies what an enabled workflow does outside
// TuxMail: its trigger, its condition and its external actions with all
// parameters. Changing any of them (a new forward address, a broader
// condition) needs a new confirmation.
func externalFingerprint(w *workflow.Workflow, reg *workflow.Registry) string {
	ext := map[string]bool{}
	for _, t := range w.ExternalEffects(reg) {
		ext[t] = true
	}
	var acts []workflow.ActionSpec
	for _, act := range w.Actions {
		if ext[act.Type] {
			acts = append(acts, act)
		}
	}
	b, err := yaml.Marshal(struct {
		Trigger   workflow.Trigger
		Condition workflow.Condition
		Actions   []workflow.ActionSpec
		Options   workflow.Options
	}{w.Trigger, w.Condition, acts, w.Options})
	if err != nil {
		return "" // never equal: confirmation is required
	}
	return string(b)
}

// needsConfirmation reports whether storing w (enabled, with external
// effects) requires an explicit confirmation: always, unless the stored
// version is enabled, was confirmed and does exactly the same outside TuxMail.
func (a *App) needsConfirmation(ctx context.Context, w *workflow.Workflow, enabling bool) (effects []string, need bool) {
	effects = w.ExternalEffects(a.wfReg)
	if !w.Enabled || len(effects) == 0 {
		return effects, false
	}
	if !enabling {
		if rec, err := a.st.GetWorkflow(ctx, w.ID); err == nil && rec.Enabled && rec.ExternalConfirmed {
			if old, err := workflow.Parse([]byte(rec.YAML)); err == nil {
				fp := externalFingerprint(w, a.wfReg)
				if fp != "" && fp == externalFingerprint(old, a.wfReg) {
					return effects, false
				}
			}
		}
	}
	return effects, true
}

// ==== Team attribution ========================================================

// WorkflowActor identifies the team member behind a request that runs on the
// App of a team mailbox. The server puts it into the context of the proxied
// workflow routes.
type WorkflowActor struct {
	// Name is the display name of the member (stored as author).
	Name string
	// Record writes the audit entry (and tells the managers when notify is
	// set); nil = no audit.
	Record func(ctx context.Context, action, details string, notify bool)
}

type workflowActorKey struct{}

// WithWorkflowActor returns a context carrying the acting team member.
func WithWorkflowActor(ctx context.Context, wa WorkflowActor) context.Context {
	return context.WithValue(ctx, workflowActorKey{}, wa)
}

func workflowActorFrom(ctx context.Context) (WorkflowActor, bool) {
	wa, ok := ctx.Value(workflowActorKey{}).(WorkflowActor)
	return wa, ok
}

// TeamWorkflowActor returns the actor of the user on a team mailbox for
// WithWorkflowActor: audit entries "team.workflow.*" and the notice to the
// other managers go through the team service.
func (a *App) TeamWorkflowActor(ctx context.Context, mailboxID int64) (WorkflowActor, error) {
	actor, svc, err := a.teamActor(ctx)
	if err != nil {
		return WorkflowActor{}, err
	}
	return WorkflowActor{Name: actor.Name, Record: func(ctx context.Context, action, details string, notify bool) {
		svc.RecordWorkflow(ctx, actor, mailboxID, action, details, notify)
	}}, nil
}

const workflowAuthorKey = "workflow.author."

type workflowAuthor struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

func (a *App) workflowAuthor(ctx context.Context, id string) string {
	v, err := a.st.ComfortGet(ctx, workflowAuthorKey+id)
	if err != nil || v == "" {
		return ""
	}
	var au workflowAuthor
	if json.Unmarshal([]byte(v), &au) != nil {
		return ""
	}
	return au.Name
}

// workflowChanged keeps the author and writes the audit entry when the
// request comes from a team member. action is save, enable, disable or
// delete; w is nil for delete.
func (a *App) workflowChanged(ctx context.Context, id, action string, w *workflow.Workflow) {
	wa, ok := workflowActorFrom(ctx)
	if !ok {
		return
	}
	details := "workflow=" + id
	notify := false
	switch {
	case action == "delete":
		_ = a.st.ComfortSet(ctx, workflowAuthorKey+id, "")
	case w != nil:
		if wa.Name != "" {
			b, _ := json.Marshal(workflowAuthor{Name: wa.Name, At: a.now().UTC().Format(time.RFC3339)})
			if err := a.st.ComfortSet(ctx, workflowAuthorKey+id, string(b)); err != nil {
				a.log.Warn("app: storing the workflow author failed", "workflow", id, "err", err)
			}
		}
		details += fmt.Sprintf(" enabled=%t", w.Enabled)
		if t := externalTargets(w, a.wfReg); len(t) > 0 {
			details += " external=" + strings.Join(t, "; ")
			notify = w.Enabled
		}
	}
	if wa.Record != nil {
		wa.Record(ctx, action, details, notify)
	}
}
