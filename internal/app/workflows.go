// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"gopkg.in/yaml.v3"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/templates"
)

// ==== Conversion between the YAML model and the UI model ======================

func workflowDTO(w *workflow.Workflow, reg *workflow.Registry) Workflow {
	out := Workflow{ID: w.ID, Name: w.Name, Description: w.Description, Enabled: w.Enabled, Actions: []WorkflowAction{},
		Trigger: WorkflowTrigger{Event: string(w.Trigger.Event), Accounts: w.Trigger.Accounts, Folders: w.Trigger.Folders,
			Channels: w.Trigger.Channels, Schedule: w.Trigger.Schedule, Name: w.Trigger.Name}}
	switch {
	case w.Condition.Rule != nil && !ruleHasQuery(w.Condition.Rule):
		var r WorkflowRule
		if b, err := json.Marshal(w.Condition.Rule); err == nil && json.Unmarshal(b, &r) == nil {
			out.Condition = &WorkflowCondition{Rule: &r}
		}
	case !w.Condition.IsZero():
		if src, err := w.Condition.Source(); err == nil {
			out.Condition = &WorkflowCondition{Expr: src}
		}
	}
	for _, a := range w.Actions {
		p := a.Params
		if p == nil {
			p = map[string]any{}
		}
		out.Actions = append(out.Actions, WorkflowAction{Type: a.Type, Params: p, If: a.If, ContinueOnError: a.ContinueOnError})
	}
	if b, err := yaml.Marshal(w.Options); err == nil {
		var m map[string]any
		if yaml.Unmarshal(b, &m) == nil && len(m) > 0 {
			out.Options = m
		}
	}
	out.ExternalEffects = len(w.ExternalEffects(reg)) > 0
	return out
}

func ruleHasQuery(r *workflow.Rule) bool {
	if r == nil {
		return false
	}
	if r.Query != "" {
		return true
	}
	for i := range r.All {
		if ruleHasQuery(&r.All[i]) {
			return true
		}
	}
	for i := range r.Any {
		if ruleHasQuery(&r.Any[i]) {
			return true
		}
	}
	return ruleHasQuery(r.Not)
}

var reWorkflowID = regexp.MustCompile(`[^a-z0-9]+`)

func slugID(name string) string {
	s := strings.Trim(reWorkflowID.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "workflow"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

func toModel(in Workflow) (*workflow.Workflow, error) {
	w := &workflow.Workflow{ID: in.ID, Name: in.Name, Description: in.Description, Enabled: in.Enabled,
		Trigger: workflow.Trigger{Event: events.Type(in.Trigger.Event), Accounts: in.Trigger.Accounts, Folders: in.Trigger.Folders,
			Channels: in.Trigger.Channels, Schedule: in.Trigger.Schedule, Name: in.Trigger.Name}}
	if in.Condition != nil {
		switch {
		case in.Condition.Rule != nil:
			b, err := json.Marshal(in.Condition.Rule)
			if err != nil {
				return nil, err
			}
			var r workflow.Rule
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, invalid("invalid rule: %v", err)
			}
			w.Condition.Rule = &r
		case strings.TrimSpace(in.Condition.Expr) != "":
			w.Condition.Expr = in.Condition.Expr
		}
	}
	for _, a := range in.Actions {
		w.Actions = append(w.Actions, workflow.ActionSpec{Type: a.Type, If: a.If, ContinueOnError: a.ContinueOnError, Params: a.Params})
	}
	if len(in.Options) > 0 {
		b, err := yaml.Marshal(in.Options)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, &w.Options); err != nil {
			return nil, invalid("invalid options: %v", err)
		}
	}
	return w, nil
}

// ==== Schema ===================================================================

var actionGroups = map[string]string{
	"move": "mail", "copy": "mail", "archive": "mail", "delete": "mail", "flag": "mail", "forward": "mail",
	"reply_template": "mail", "vacation_reply": "mail", "label": "organize", "tag": "organize", "untag": "organize",
	"add_to_project": "organize", "set_project_status": "organize", "create_project": "organize",
	"create_search_folder": "organize", "notify": "notify", "create_task": "calendar", "create_event": "calendar",
	"remind_no_reply": "calendar", "archive_pdf": "files", "upload_attachments": "files",
}

var fieldAliases = map[string]string{
	"mail.from": "from", "mail.fromName": "name", "mail.fromDomain": "domain", "mail.subject": "subject", "mail.body": "body",
	"mail.account": "account", "mail.folder": "folder", "mail.category": "category", "mail.project": "project",
	"chat.channel": "channel", "chat.chat": "chat", "chat.sender": "sender", "chat.text": "text", "file.name": "filename",
	"mail.to": "to", "mail.cc": "cc", "mail.attachmentNames": "attachment", "mail.labels": "label", "mail.tags": "tag",
	"mail.size": "size", "mail.attachmentCount": "attachments",
}

var uiTriggerFilters = map[string]bool{"accounts": true, "folders": true, "channels": true, "schedule": true, "name": true}

func triggerGroup(ev string) string {
	switch {
	case strings.HasPrefix(ev, "mail."), strings.HasPrefix(ev, "noreply."):
		return "mail"
	case strings.HasPrefix(ev, "chat."):
		return "chat"
	case strings.HasPrefix(ev, "calendar."):
		return "calendar"
	case strings.HasPrefix(ev, "contact."):
		return "contact"
	case strings.HasPrefix(ev, "file."):
		return "file"
	case strings.HasPrefix(ev, "schedule."):
		return "time"
	}
	return "other"
}

func paramKind(p workflow.ParamInfo) string {
	switch p.Name {
	case "folder":
		return "folder"
	case "tags", "tag":
		return "tag"
	case "project":
		return "project"
	case "channel":
		return "channel"
	case "url":
		return "url"
	case "path", "dir":
		return "path"
	case "template":
		return "template"
	case "text", "body", "note":
		return "text"
	}
	switch p.Type {
	case "bool":
		return "bool"
	case "int":
		return "number"
	case "duration":
		return "duration"
	case "list":
		return "list"
	}
	if p.Template {
		return "text"
	}
	return "string"
}

// DescribeWorkflows returns the editor schema derived from internal/workflow.
func (a *App) DescribeWorkflows(context.Context) (WorkflowSchema, error) {
	d := workflow.Describe(a.wfReg)
	s := WorkflowSchema{Operators: map[string][]string{}, Triggers: []WorkflowTriggerSpec{}, Fields: []WorkflowFieldSpec{},
		Actions: []WorkflowActionSpec{}, Variables: []string{}}
	for _, t := range d.Triggers {
		ts := WorkflowTriggerSpec{Event: t.Event, Group: triggerGroup(t.Event), Filters: []string{}}
		for _, f := range t.Filters {
			if uiTriggerFilters[f] {
				ts.Filters = append(ts.Filters, f)
			}
		}
		s.Triggers = append(s.Triggers, ts)
	}
	for _, f := range workflow.RuleFields() {
		if f.Kind == "" || f.Name == "event" || f.Name == "schedule" {
			continue
		}
		s.Fields = append(s.Fields, WorkflowFieldSpec{Name: f.Name, Kind: string(f.Kind), Alias: fieldAliases[f.Name]})
	}
	for k, ops := range workflow.RuleOps {
		s.Operators[string(k)] = append([]string{}, ops...)
	}
	for _, act := range d.Actions {
		as := WorkflowActionSpec{Type: act.Type, Group: actionGroups[act.Type], External: act.ExternalEffect,
			Triggers: append([]string{}, act.Triggers...), Params: []WorkflowParamSpec{}}
		if as.Group == "" {
			as.Group = "advanced"
		}
		params := slices.Clone(act.Params)
		sort.SliceStable(params, func(i, j int) bool {
			pi, pj := params[i].Positional, params[j].Positional
			if (pi == 0) != (pj == 0) {
				return pi != 0
			}
			return pi < pj
		})
		for _, p := range params {
			as.Params = append(as.Params, WorkflowParamSpec{Name: p.Name, Kind: paramKind(p), Required: p.Required,
				Positional: p.Positional > 0, Options: p.Enum})
		}
		s.Actions = append(s.Actions, as)
	}
	for _, p := range d.Placeholders {
		name := strings.Trim(p.Name, "{} ")
		if name != "" && !strings.ContainsAny(name, " (|") {
			s.Variables = append(s.Variables, name)
		}
	}
	return s, nil
}

// ==== CRUD =====================================================================

func (a *App) decorate(ctx context.Context, w *workflow.Workflow, metas map[string]store.WorkflowMeta) (Workflow, error) {
	out := workflowDTO(w, a.wfReg)
	m := metas[w.ID]
	out.ProjectID, out.TemplateID = m.ProjectID, m.TemplateID
	if rec, err := a.st.GetWorkflow(ctx, w.ID); err == nil {
		out.UpdatedAt = rfc3339(rec.UpdatedAt)
		out.Enabled = rec.Enabled
	}
	runs, err := a.st.ListWorkflowRuns(ctx, w.ID, 1)
	if err != nil {
		return out, err
	}
	if len(runs) > 0 {
		s := runSummary(runs[0])
		out.LastRun = &s
	}
	return out, nil
}

func runStatus(s store.RunStatus) string {
	switch s {
	case store.RunSucceeded:
		return "ok"
	case store.RunFailed:
		return "failed"
	case store.RunSkipped:
		return "skipped"
	}
	return "running"
}

func runSummary(r store.WorkflowRun) WorkflowRunSummary {
	at := r.StartedAt
	if at.IsZero() {
		at = r.CreatedAt
	}
	return WorkflowRunSummary{ID: strconv.FormatInt(r.ID, 10), StartedAt: rfc3339(at), Status: runStatus(r.Status), Message: r.Error}
}

// listModels returns all stored workflows (also invalid/disabled ones the
// engine did not load).
func (a *App) listModels(ctx context.Context) ([]*workflow.Workflow, error) {
	recs, err := a.st.ListWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	out := []*workflow.Workflow{}
	for _, r := range recs {
		w, err := workflow.Parse([]byte(r.YAML))
		if err != nil {
			a.log.Warn("app: stored workflow is invalid", "id", r.ID, "err", err)
			continue
		}
		w.Enabled = r.Enabled
		out = append(out, w)
	}
	return out, nil
}

// ListWorkflows returns the workflows (of a project when projectID != 0).
func (a *App) ListWorkflows(ctx context.Context, projectID int64) ([]Workflow, error) {
	ws, err := a.listModels(ctx)
	if err != nil {
		return nil, err
	}
	metas, err := a.st.WorkflowMetas(ctx)
	if err != nil {
		return nil, err
	}
	out := []Workflow{}
	for _, w := range ws {
		if projectID != 0 && metas[w.ID].ProjectID != projectID {
			continue
		}
		d, err := a.decorate(ctx, w, metas)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GetWorkflow returns a workflow.
func (a *App) GetWorkflow(ctx context.Context, id string) (Workflow, error) {
	rec, err := a.st.GetWorkflow(ctx, id)
	if err != nil {
		return Workflow{}, err
	}
	w, err := workflow.Parse([]byte(rec.YAML))
	if err != nil {
		return Workflow{}, err
	}
	metas, err := a.st.WorkflowMetas(ctx)
	if err != nil {
		return Workflow{}, err
	}
	return a.decorate(ctx, w, metas)
}

// SaveWorkflow validates and stores a workflow. Saving from the UI counts as
// confirmation of actions with external effect (the editor shows the warning).
func (a *App) SaveWorkflow(ctx context.Context, in Workflow) (Workflow, error) {
	if strings.TrimSpace(in.Name) == "" {
		return Workflow{}, invalid("a workflow needs a name")
	}
	if in.ID == "" {
		base := slugID(in.Name)
		in.ID = base
		for i := 2; ; i++ {
			if _, err := a.st.GetWorkflow(ctx, in.ID); errors.Is(err, store.ErrNotFound) {
				break
			}
			in.ID = fmt.Sprintf("%s-%d", base, i)
		}
	}
	w, err := toModel(in)
	if err != nil {
		return Workflow{}, err
	}
	if err := a.wf.Save(ctx, w, true); err != nil {
		var ve *workflow.ValidationError
		if errors.As(err, &ve) {
			return Workflow{}, invalid("%s", strings.Join(ve.Problems, "; "))
		}
		return Workflow{}, err
	}
	if err := a.st.PutWorkflowMeta(ctx, store.WorkflowMeta{WorkflowID: w.ID, ProjectID: in.ProjectID, TemplateID: in.TemplateID}); err != nil {
		return Workflow{}, err
	}
	if in.ProjectID != 0 {
		a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: in.ProjectID})
	}
	a.sieveSyncSoon() // mailproto.go
	return a.GetWorkflow(ctx, w.ID)
}

// DeleteWorkflow deletes a workflow and its run log.
func (a *App) DeleteWorkflow(ctx context.Context, id string) error {
	if err := a.wf.Delete(ctx, id); err != nil {
		return err
	}
	a.sieveSyncSoon() // mailproto.go
	return nil
}

// SetWorkflowEnabled enables or disables a workflow.
func (a *App) SetWorkflowEnabled(ctx context.Context, id string, enabled bool) (Workflow, error) {
	if err := a.wf.SetEnabled(ctx, id, enabled, true); err != nil {
		return Workflow{}, err
	}
	a.sieveSyncSoon() // mailproto.go
	return a.GetWorkflow(ctx, id)
}

// ListWorkflowTemplates returns the workflow gallery.
func (a *App) ListWorkflowTemplates(context.Context) ([]WorkflowTemplate, error) {
	ts, err := templates.List()
	if err != nil {
		return nil, err
	}
	out := []WorkflowTemplate{}
	for _, t := range ts {
		w, err := t.Workflow()
		if err != nil {
			continue
		}
		d := workflowDTO(w, a.wfReg)
		d.TemplateID = t.ID
		out = append(out, WorkflowTemplate{ID: t.ID, Name: t.Name, Description: t.Description, Workflow: d})
	}
	return out, nil
}

// installProjectWorkflow instantiates a workflow template for a project.
func (a *App) installProjectWorkflow(ctx context.Context, p store.Project, ref string) error {
	t, err := templates.Get(ref)
	if err != nil {
		return err
	}
	w, err := t.Workflow()
	if err != nil {
		return err
	}
	d := workflowDTO(w, a.wfReg)
	d.ID = slugID(fmt.Sprintf("%s-%s", ref, p.Key+strconv.FormatInt(p.ID, 10)))
	d.ProjectID, d.TemplateID, d.Enabled = p.ID, ref, false
	_, err = a.SaveWorkflow(ctx, d)
	return err
}

// ListWorkflowRuns returns the run log of a workflow.
func (a *App) ListWorkflowRuns(ctx context.Context, id string, limit int) ([]WorkflowRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	runs, err := a.st.ListWorkflowRuns(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	out := []WorkflowRun{}
	for _, r := range runs {
		wr := WorkflowRun{WorkflowRunSummary: runSummary(r), WorkflowID: r.WorkflowID, FinishedAt: rfc3339(r.FinishedAt),
			Trigger: r.EventType, Steps: []WorkflowRunStep{}}
		var steps []workflow.StepResult
		_ = json.Unmarshal([]byte(r.StepsJSON), &steps)
		prev := r.StartedAt
		for _, s := range steps {
			st := WorkflowRunStep{Action: s.Type, Summary: s.Error}
			switch s.Status {
			case workflow.StepDone:
				st.Status = "ok"
			case workflow.StepSkipped:
				st.Status = "skipped"
			default:
				st.Status = "failed"
			}
			if st.Summary == "" {
				var parts []string
				for k, v := range s.Output {
					parts = append(parts, k+"="+v)
				}
				sort.Strings(parts)
				st.Summary = strings.Join(parts, ", ")
			}
			if !prev.IsZero() && !s.At.IsZero() {
				st.DurationMs = s.At.Sub(prev).Milliseconds()
				prev = s.At
			}
			wr.Steps = append(wr.Steps, st)
		}
		if it, err := store.ParseItem(r.EventRef); err == nil {
			wi := &WorkflowRunItem{Kind: string(it.Kind), ID: it.ID}
			if s, err := a.st.ItemSummary(ctx, it); err == nil {
				wi.Title = s.Title
			}
			wr.Item = wi
		}
		out = append(out, wr)
	}
	return out, nil
}

// ==== Dry run ==================================================================

// DryRunWorkflow evaluates a workflow against recent mails without side effects.
func (a *App) DryRunWorkflow(ctx context.Context, in Workflow, limit int) (WorkflowDryRunResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if in.ID == "" {
		in.ID = "dry-run"
	}
	if in.Name == "" {
		in.Name = "dry run"
	}
	w, err := toModel(in)
	if err != nil {
		return WorkflowDryRunResult{}, err
	}
	if err := w.Validate(a.wfReg); err != nil {
		return WorkflowDryRunResult{}, invalid("%v", err)
	}
	msgs, _, err := a.st.ListFilteredMessages(ctx, store.MessageFilter{}, limit, 0)
	if err != nil {
		return WorkflowDryRunResult{}, err
	}
	evs := make([]events.Event, 0, len(msgs))
	for _, m := range msgs {
		evs = append(evs, a.mailEvent(ctx, w.Trigger.Event, m))
	}
	res, err := a.wf.DryRun(ctx, w, evs)
	if err != nil {
		return WorkflowDryRunResult{}, invalid("%v", err)
	}
	out := WorkflowDryRunResult{Samples: []WorkflowDryRunSample{}, Checked: len(res)}
	for i, r := range res {
		m := msgs[i]
		s := WorkflowDryRunSample{Item: WorkflowDryRunItem{Kind: string(store.ItemMessage), ID: m.ID, Title: m.Subject,
			Date: rfc3339(msgTime(m)), From: m.From}, Matched: r.TriggerMatched && r.ConditionMet, Planned: []WorkflowPlannedAction{}}
		for _, p := range r.Planned {
			s.Planned = append(s.Planned, WorkflowPlannedAction{Type: p.Type, Summary: p.Summary, External: p.ExternalEffect, Params: p.Params})
		}
		if s.Matched {
			out.Matched++
		}
		out.Samples = append(out.Samples, s)
	}
	return out, nil
}

// mailEvent builds a workflow event for a stored mail.
func (a *App) mailEvent(ctx context.Context, typ events.Type, m store.Message) events.Event {
	if typ == "" || !strings.HasPrefix(string(typ), "mail.") {
		typ = events.MailReceived
	}
	ev := events.Event{ID: fmt.Sprintf("dry-%d", m.ID), Type: typ, Time: msgTime(m), AccountID: m.AccountID,
		Ref: store.Item{Kind: store.ItemMessage, ID: m.ID}.String()}
	mail := &events.Mail{AccountID: m.AccountID, Key: m.Key(), MessageID: m.MessageIDHdr, Subject: m.Subject, Date: msgTime(m), Size: m.Size}
	if acc, err := a.st.GetAccount(ctx, m.AccountID); err == nil {
		mail.Account = acc.DisplayName
	}
	for _, f := range m.Flags.Domain() {
		mail.Flags = append(mail.Flags, string(f))
	}
	if addrs, err := a.st.MessageAddresses(ctx, m.ID); err == nil {
		for _, ad := range addrs {
			da := Address{Name: ad.Name, Email: ad.Email}
			switch ad.Role {
			case store.RoleFrom:
				mail.From = da
			case store.RoleTo:
				mail.To = append(mail.To, da)
			case store.RoleCc:
				mail.Cc = append(mail.Cc, da)
			}
		}
	}
	if mail.From.Email == "" {
		mail.From.Email = m.From
	}
	if locs, err := a.st.Locations(ctx, m.ID); err == nil && len(locs) > 0 {
		if f, err := a.st.GetFolder(ctx, locs[0].FolderID); err == nil {
			mail.Folder, mail.FolderRole = f.Name, string(f.Role)
		}
	}
	if atts, err := a.st.MessageAttachments(ctx, m.ID); err == nil {
		for _, at := range atts {
			mail.Attachments = append(mail.Attachments, events.Attachment{Name: at.Filename, MIME: at.MIMEType, Size: at.Size})
		}
	}
	mail.Body = a.bubble(ctx, m).visible
	if paths, err := a.st.TagPaths(ctx, store.Item{Kind: store.ItemMessage, ID: m.ID}); err == nil {
		mail.Labels = paths
	}
	ev.Mail = mail
	return ev
}

// ==== Validation and completion ================================================

func diagCode(code string) string {
	switch code {
	case "yaml-syntax", "rule":
		return "syntax"
	case "unknown-action":
		return "unknown_action"
	case "required":
		return "missing_param"
	}
	return strings.ReplaceAll(code, "-", "_")
}

// ValidateWorkflow parses and checks workflow source text.
func (a *App) ValidateWorkflow(ctx context.Context, src, mode string) (WorkflowValidation, error) {
	out := WorkflowValidation{Diagnostics: []WorkflowDiagnostic{}}
	var w *workflow.Workflow
	switch mode {
	case "yaml":
		for _, d := range workflow.ValidateText(src, a.wfReg) {
			out.Diagnostics = append(out.Diagnostics, WorkflowDiagnostic{Line: d.Line, Col: d.Col, EndLine: d.EndLine, EndCol: d.EndCol,
				Severity: string(d.Severity), Code: diagCode(d.Code), Detail: d.Path, Message: d.Message})
		}
		if !workflow.HasErrors(workflow.ValidateText(src, a.wfReg)) {
			var err error
			if w, err = workflow.Parse([]byte(src)); err != nil {
				out.Diagnostics = append(out.Diagnostics, WorkflowDiagnostic{Line: 1, Col: 1, EndLine: 1, EndCol: 2, Severity: "error",
					Code: "syntax", Message: err.Error()})
				w = nil
			}
		}
	case "rule":
		var err error
		w, err = workflow.ParseRule(src, a.wfReg)
		if err != nil {
			d := WorkflowDiagnostic{Line: 1, Col: 1, Severity: "error", Code: "syntax", Message: err.Error()}
			var se *workflow.RuleSyntaxError
			if errors.As(err, &se) {
				d.Line, d.Col, d.Message = se.Line, se.Col, se.Msg
			}
			var ve *workflow.ValidationError
			if errors.As(err, &ve) {
				d.Code, d.Message = "invalid", strings.Join(ve.Problems, "; ")
			}
			d.EndLine, d.EndCol = d.Line, d.Col+1
			out.Diagnostics = append(out.Diagnostics, d)
			w = nil
		}
	default:
		return out, invalid("mode must be rule or yaml")
	}
	if w != nil {
		if w.Name == "" {
			w.Name = w.ID
		}
		dto := workflowDTO(w, a.wfReg)
		out.Workflow = &dto
	}
	return out, nil
}

// utf16Offset converts a byte offset into a UTF-16 offset (JS strings).
func utf16Offset(s string, byteOff int) int {
	byteOff = min(max(byteOff, 0), len(s))
	n := 0
	for _, r := range s[:byteOff] {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// byteOffset converts a UTF-16 offset into a byte offset.
func byteOffset(s string, u16 int) int {
	n := 0
	for i, r := range s {
		if n >= u16 {
			return i
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return len(s)
}

var completionKinds = map[string]string{"keyword": "keyword", "trigger": "trigger", "field": "field", "operator": "operator",
	"action": "action", "param": "param", "value": "value", "snippet": "keyword"}

// CompleteWorkflow returns completions at a UTF-16 offset.
func (a *App) CompleteWorkflow(ctx context.Context, src string, offset int, mode string) (WorkflowCompletion, error) {
	m := workflow.ModeRule
	if mode == "yaml" {
		m = workflow.ModeYAML
	}
	bo := byteOffset(src, offset)
	cs := workflow.Complete(src, bo, m, a.wfReg)
	out := WorkflowCompletion{From: offset, To: offset, Items: []WorkflowCompletionItem{}}
	for i, c := range cs {
		if i == 0 {
			out.From, out.To = utf16Offset(src, c.From), utf16Offset(src, c.To)
		}
		k := completionKinds[string(c.Kind)]
		if k == "" {
			k = "keyword"
		}
		if k == "field" && strings.Contains(c.Label, "{{") {
			k = "variable"
		}
		out.Items = append(out.Items, WorkflowCompletionItem{Label: c.Label, Insert: c.Insert, Kind: k, Detail: c.Detail})
		if len(out.Items) >= 200 {
			break
		}
	}
	return out, nil
}
