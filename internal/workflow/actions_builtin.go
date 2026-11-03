// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
)

// Built-in action type names.
const (
	ActMove           = "move"
	ActCopy           = "copy"
	ActFlag           = "flag"
	ActLabel          = "label"
	ActDelete         = "delete"
	ActArchive        = "archive"
	ActForward        = "forward"
	ActReplyTemplate  = "reply_template"
	ActReplyAuto      = "reply_automatic"
	ActUpload         = "upload_attachments"
	ActArchivePDF     = "archive_pdf"
	ActCreateTask     = "create_task"
	ActCreateEvent    = "create_event"
	ActNotify         = "notify"
	ActWebhook        = "webhook"
	ActSetVar         = "set_var"
	ActRunWorkflow    = "run_workflow"
	ActRemindNoReply  = "remind_no_reply"
	ActVacation       = "vacation_reply"
	ActWarn           = "warn"
	ActBlock          = "block"
	defaultWebhookTTL = 10 * time.Second
	maxWebhookTTL     = 2 * time.Minute
)

var mailTriggers = []events.Type{events.MailReceived, events.MailSent, events.MailMoved, events.MailFlagged,
	events.NoReplyTimeout, events.Manual}

func builtinActions() []ActionType {
	return []ActionType{
		{Name: ActMove, Description: "Move the mail to a folder", Triggers: mailTriggers,
			New: newFolderAction(ActMove, func(ctx context.Context, m MailActions, ref ItemRef, f string) error { return m.Move(ctx, ref, f) })},
		{Name: ActCopy, Description: "Copy the mail to a folder", Triggers: mailTriggers,
			New: newFolderAction(ActCopy, func(ctx context.Context, m MailActions, ref ItemRef, f string) error { return m.Copy(ctx, ref, f) })},
		{Name: ActFlag, Description: "Set or clear flags (seen, flagged, answered or keywords)", Triggers: mailTriggers, New: newFlagAction(false)},
		{Name: ActLabel, Description: "Add or remove labels/categories", Triggers: mailTriggers, New: newFlagAction(true)},
		{Name: ActDelete, Description: "Delete the mail (to trash unless permanent)", Triggers: mailTriggers, New: newDeleteAction},
		{Name: ActArchive, Description: "Move the mail to the archive folder", Triggers: mailTriggers, New: newArchiveAction},
		{Name: ActForward, Description: "Forward the mail", ExternalEffect: true, Triggers: mailTriggers, New: newForwardAction},
		{Name: ActReplyTemplate, Description: "Reply with a text template", ExternalEffect: true, Triggers: mailTriggers, New: newReplyAction},
		{Name: ActReplyAuto, Description: "Send an automatic reply (Auto-Submitted: auto-replied)", ExternalEffect: true,
			Triggers: []events.Type{events.MailReceived, events.Manual}, New: newReplyAutoAction},
		{Name: ActUpload, Description: "Upload attachments to cloud storage",
			Triggers: append([]events.Type{events.MailBeforeSend}, mailTriggers...), New: newUploadAction},
		{Name: ActArchivePDF, Description: "Archive the mail as PDF/A with embedded original", Triggers: mailTriggers, New: newPDFAction},
		{Name: ActCreateTask, Description: "Create a task", New: newTaskAction},
		{Name: ActCreateEvent, Description: "Create a calendar event", New: newEventAction},
		{Name: ActNotify, Description: "Notify me (desktop, push, Signal/Telegram/Matrix to myself)", New: newNotifyAction},
		{Name: ActWebhook, Description: "Call an HTTP webhook (POST JSON)", ExternalEffect: true, New: newWebhookAction},
		{Name: ActSetVar, Description: "Set a workflow variable", New: newSetVarAction},
		{Name: ActRunWorkflow, Description: "Run another workflow", New: newRunWorkflowAction},
		{Name: ActRemindNoReply, Description: "Remind me if there is no reply", Triggers: []events.Type{events.MailSent, events.Manual},
			New: newRemindAction},
		{Name: ActVacation, Description: "Out-of-office reply according to the vacation settings", ExternalEffect: true,
			Triggers: []events.Type{events.MailReceived, events.ChatReceived}, New: newVacationAction},
		{Name: ActWarn, Description: "Show a warning before sending", Triggers: []events.Type{events.MailBeforeSend},
			New: newFindingAction(events.SeverityWarn)},
		{Name: ActBlock, Description: "Prevent sending", Triggers: []events.Type{events.MailBeforeSend},
			New: newFindingAction(events.SeverityBlock)},
	}
}

func checkTemplates(fields map[string]string) error {
	for name, t := range fields {
		if err := CheckTemplate(t); err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
	}
	return nil
}

func needMail(rc *RunContext) (MailActions, ItemRef, error) {
	if rc.Deps == nil || rc.Deps.Mail == nil {
		return nil, ItemRef{}, Permanent(errors.New("no mail backend configured"))
	}
	ref := rc.Item()
	if ref.Kind != "mail" || ref.Key == "" {
		return nil, ItemRef{}, Permanent(errors.New("event does not refer to a mail"))
	}
	return rc.Deps.Mail, ref, nil
}

// ---- move / copy ----

type folderAction struct {
	name   string
	Folder string `yaml:"folder"`
	do     func(context.Context, MailActions, ItemRef, string) error
}

func newFolderAction(name string, do func(context.Context, MailActions, ItemRef, string) error) func(map[string]any) (Action, error) {
	return func(p map[string]any) (Action, error) {
		a := &folderAction{name: name, do: do}
		if err := DecodeParams(p, a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Folder) == "" {
			return nil, errors.New("folder: required")
		}
		return a, checkTemplates(map[string]string{"folder": a.Folder})
	}
}

func (a *folderAction) folder(rc *RunContext) (string, error) {
	f, err := rc.Render(a.Folder)
	if err != nil {
		return "", Permanent(fmt.Errorf("folder: %w", err))
	}
	return f, nil
}

func (a *folderAction) Plan(rc *RunContext) (Plan, error) {
	f, err := a.folder(rc)
	return Plan{Type: a.name, Summary: fmt.Sprintf("%s mail to folder %q", a.name, f), Params: map[string]string{"folder": f}}, err
}

func (a *folderAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	f, err := a.folder(rc)
	if err != nil {
		return nil, err
	}
	return Output{"folder": f}, a.do(ctx, m, ref, f)
}

// ---- flag / label ----

type flagAction struct {
	labels bool
	Add    []string `yaml:"add,omitempty"`
	Remove []string `yaml:"remove,omitempty"`
}

var flagNames = map[string]string{"seen": `\Seen`, "read": `\Seen`, "flagged": `\Flagged`, "answered": `\Answered`,
	"important": `\Flagged`, "forwarded": `$Forwarded`, "junk": `$Junk`, "notjunk": `$NotJunk`}

func newFlagAction(labels bool) func(map[string]any) (Action, error) {
	return func(p map[string]any) (Action, error) {
		a := &flagAction{labels: labels}
		if err := DecodeParams(p, a); err != nil {
			return nil, err
		}
		if len(a.Add)+len(a.Remove) == 0 {
			return nil, errors.New("add or remove: at least one entry required")
		}
		if !labels {
			for i, f := range a.Add {
				a.Add[i] = mapFlag(f)
			}
			for i, f := range a.Remove {
				a.Remove[i] = mapFlag(f)
			}
		}
		return a, nil
	}
}

func mapFlag(f string) string {
	if m, ok := flagNames[strings.ToLower(f)]; ok {
		return m
	}
	return f
}

func (a *flagAction) name() string {
	if a.labels {
		return ActLabel
	}
	return ActFlag
}

func (a *flagAction) Plan(*RunContext) (Plan, error) {
	return Plan{Type: a.name(), Summary: fmt.Sprintf("%s: add %v, remove %v", a.name(), a.Add, a.Remove),
		Params: map[string]string{"add": strings.Join(a.Add, ","), "remove": strings.Join(a.Remove, ",")}}, nil
}

func (a *flagAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	if a.labels {
		return nil, m.SetLabels(ctx, ref, a.Add, a.Remove)
	}
	return nil, m.SetFlags(ctx, ref, a.Add, a.Remove)
}

// ---- delete / archive ----

type deleteAction struct {
	Permanent bool `yaml:"permanent,omitempty"`
}

func newDeleteAction(p map[string]any) (Action, error) {
	a := &deleteAction{}
	return a, DecodeParams(p, a)
}

func (a *deleteAction) Plan(*RunContext) (Plan, error) {
	s := "move mail to trash"
	if a.Permanent {
		s = "delete mail permanently"
	}
	return Plan{Type: ActDelete, Summary: s}, nil
}

func (a *deleteAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	return nil, m.Delete(ctx, ref, a.Permanent)
}

type archiveAction struct{}

func newArchiveAction(p map[string]any) (Action, error) {
	a := &archiveAction{}
	return a, DecodeParams(p, a)
}

func (*archiveAction) Plan(*RunContext) (Plan, error) {
	return Plan{Type: ActArchive, Summary: "move mail to the archive folder"}, nil
}

func (*archiveAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	return nil, m.Archive(ctx, ref)
}

// ---- forward / replies ----

var simpleAddr = regexp.MustCompile(`^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$`)

type forwardAction struct {
	To           []string `yaml:"to"`
	Note         string   `yaml:"note,omitempty"`
	AsAttachment bool     `yaml:"as_attachment,omitempty"`
}

func newForwardAction(p map[string]any) (Action, error) {
	a := &forwardAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if len(a.To) == 0 {
		return nil, errors.New("to: at least one address required")
	}
	for _, t := range a.To {
		if !simpleAddr.MatchString(t) {
			return nil, fmt.Errorf("to: %q is not a mail address", t)
		}
	}
	return a, checkTemplates(map[string]string{"note": a.Note})
}

// pgpGuard enforces "no forwarding of decrypted PGP content without explicit
// permission" (KONZEPT 8.5).
func pgpGuard(rc *RunContext, what string) error {
	if rc.Env.Mail.PGP.Encrypted && (rc.Workflow == nil || !rc.Workflow.Options.AllowDecryptedPGP) {
		return Permanent(fmt.Errorf("%s of PGP-encrypted content requires options.allow_decrypted_pgp", what))
	}
	return nil
}

func (a *forwardAction) Plan(rc *RunContext) (Plan, error) {
	note, err := rc.Render(a.Note)
	if err == nil {
		err = pgpGuard(rc, "forwarding")
	}
	return Plan{Type: ActForward, ExternalEffect: true, Summary: "forward mail to " + strings.Join(a.To, ", "),
		Params: map[string]string{"to": strings.Join(a.To, ","), "note": note}}, err
}

func (a *forwardAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	if err := pgpGuard(rc, "forwarding"); err != nil {
		return nil, err
	}
	note, err := rc.Render(a.Note)
	if err != nil {
		return nil, Permanent(err)
	}
	return nil, m.Forward(ctx, ref, ForwardRequest{To: a.To, Note: note, AsAttachment: a.AsAttachment})
}

type replyAction struct {
	auto     bool
	Subject  string `yaml:"subject,omitempty"`
	Body     string `yaml:"body"`
	ReplyAll bool   `yaml:"reply_all,omitempty"`
	Send     bool   `yaml:"send,omitempty"` // reply_template only: send instead of creating a draft
}

func newReplyAction(p map[string]any) (Action, error) { return buildReply(p, false) }

func newReplyAutoAction(p map[string]any) (Action, error) { return buildReply(p, true) }

func buildReply(p map[string]any, auto bool) (Action, error) {
	a := &replyAction{auto: auto}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Body) == "" {
		return nil, errors.New("body: required")
	}
	if auto && (a.ReplyAll || a.Send) {
		return nil, errors.New("reply_all and send are not available for automatic replies")
	}
	return a, checkTemplates(map[string]string{"subject": a.Subject, "body": a.Body})
}

func (a *replyAction) typ() string {
	if a.auto {
		return ActReplyAuto
	}
	return ActReplyTemplate
}

func (a *replyAction) request(rc *RunContext) (ReplyRequest, error) {
	subj, err := rc.Render(a.Subject)
	if err != nil {
		return ReplyRequest{}, Permanent(fmt.Errorf("subject: %w", err))
	}
	body, err := rc.Render(a.Body)
	if err != nil {
		return ReplyRequest{}, Permanent(fmt.Errorf("body: %w", err))
	}
	r := ReplyRequest{Subject: subj, Body: body, ReplyAll: a.ReplyAll, Send: a.Send}
	if a.auto {
		r.Send = true
		r.Headers = map[string]string{"Auto-Submitted": "auto-replied"}
	}
	return r, nil
}

func (a *replyAction) Plan(rc *RunContext) (Plan, error) {
	r, err := a.request(rc)
	s := "create reply draft"
	if r.Send {
		s = "send reply to " + rc.Env.Mail.From
	}
	return Plan{Type: a.typ(), ExternalEffect: true, Summary: s,
		Params: map[string]string{"subject": r.Subject, "body": r.Body}}, err
}

func (a *replyAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	m, ref, err := needMail(rc)
	if err != nil {
		return nil, err
	}
	r, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	if a.auto {
		return nil, m.ReplyAutomatic(ctx, ref, r)
	}
	return nil, m.ReplyTemplate(ctx, ref, r)
}

// ---- cloud upload ----

type uploadAction struct {
	Target          string   `yaml:"target"`
	Dir             string   `yaml:"dir"`
	Pattern         string   `yaml:"pattern,omitempty"`
	MinSize         string   `yaml:"min_size,omitempty"`
	ShareLink       bool     `yaml:"share_link,omitempty"`
	ShareExpiry     Duration `yaml:"share_expiry,omitempty"`
	ReplaceWithLink bool     `yaml:"replace_with_link,omitempty"`
	minSize         int64
}

func newUploadAction(p map[string]any) (Action, error) {
	a := &uploadAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.Target == "" {
		return nil, errors.New("target: required (cloud account, e.g. nextcloud)")
	}
	if a.Dir == "" {
		return nil, errors.New("dir: required")
	}
	if a.MinSize != "" {
		n, err := ParseSize(a.MinSize)
		if err != nil {
			return nil, fmt.Errorf("min_size: %v", err)
		}
		a.minSize = n
	}
	if a.ReplaceWithLink {
		a.ShareLink = true
	}
	return a, checkTemplates(map[string]string{"dir": a.Dir})
}

func (a *uploadAction) request(rc *RunContext) (UploadRequest, error) {
	dir, err := rc.RenderPath(a.Dir)
	if err != nil {
		return UploadRequest{}, Permanent(fmt.Errorf("dir: %w", err))
	}
	return UploadRequest{Target: a.Target, Dir: dir, Pattern: a.Pattern, MinSize: a.minSize, ShareLink: a.ShareLink,
		ShareExpiry: a.ShareExpiry.D(), ReplaceWithLink: a.ReplaceWithLink}, nil
}

func (a *uploadAction) Plan(rc *RunContext) (Plan, error) {
	r, err := a.request(rc)
	n := 0
	for _, at := range rc.Env.Mail.Attachments {
		if at.Size >= r.MinSize && (r.Pattern == "" || globMatch(r.Pattern, at.Name)) {
			n++
		}
	}
	return Plan{Type: ActUpload, Summary: fmt.Sprintf("upload %d attachment(s) to %s:%s", n, a.Target, r.Dir),
		Params: map[string]string{"target": a.Target, "dir": r.Dir, "count": fmt.Sprint(n)}}, err
}

func (a *uploadAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Cloud == nil {
		return nil, Permanent(errors.New("no cloud backend configured"))
	}
	r, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	files, err := rc.Deps.Cloud.UploadAttachments(ctx, rc.Item(), r)
	if err != nil {
		return nil, err
	}
	out := Output{"count": fmt.Sprint(len(files))}
	var links []string
	for _, f := range files {
		if f.URL != "" {
			links = append(links, f.URL)
		}
	}
	if len(links) > 0 {
		out["var.links"] = strings.Join(links, "\n")
	}
	return out, nil
}

// ---- PDF archive ----

type pdfAction struct {
	Target             string `yaml:"target"`
	Path               string `yaml:"path"`
	EmbedOriginal      *bool  `yaml:"embed_original,omitempty"`      // default true
	IncludeAttachments *bool  `yaml:"include_attachments,omitempty"` // default true
	SkipDuplicates     *bool  `yaml:"skip_duplicates,omitempty"`     // default true
}

func newPDFAction(p map[string]any) (Action, error) {
	a := &pdfAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.Target == "" {
		return nil, errors.New("target: required (local, nextcloud, opencloud, webdav or an account name)")
	}
	if a.Path == "" {
		return nil, errors.New("path: required, e.g. /Archiv/{{jahr}}/{{datum}}_{{absender}}_{{betreff}}.pdf")
	}
	return a, checkTemplates(map[string]string{"path": a.Path})
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func (a *pdfAction) request(rc *RunContext) (PDFRequest, error) {
	p, err := rc.RenderPath(a.Path)
	if err != nil {
		return PDFRequest{}, Permanent(fmt.Errorf("path: %w", err))
	}
	if !strings.HasSuffix(strings.ToLower(p), ".pdf") {
		p += ".pdf"
	}
	return PDFRequest{Target: a.Target, Path: p, EmbedOriginal: boolOr(a.EmbedOriginal, true),
		IncludeAttachments: boolOr(a.IncludeAttachments, true), SkipDuplicates: boolOr(a.SkipDuplicates, true)}, nil
}

func (a *pdfAction) Plan(rc *RunContext) (Plan, error) {
	r, err := a.request(rc)
	return Plan{Type: ActArchivePDF, Summary: fmt.Sprintf("archive mail as PDF/A to %s:%s", r.Target, r.Path),
		Params: map[string]string{"target": r.Target, "path": r.Path}}, err
}

func (a *pdfAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.PDF == nil {
		return nil, Permanent(errors.New("no PDF backend configured"))
	}
	if ref := rc.Item(); ref.Kind != "mail" {
		return nil, Permanent(errors.New("event does not refer to a mail"))
	}
	r, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	p, err := rc.Deps.PDF.ArchivePDF(ctx, rc.Item(), r)
	return Output{"path": p, "var.pdf_path": p}, err
}

// ---- tasks / events ----

type taskAction struct {
	List  string   `yaml:"list,omitempty"`
	Title string   `yaml:"title"`
	Notes string   `yaml:"notes,omitempty"`
	DueIn Duration `yaml:"due_in,omitempty"`
}

func newTaskAction(p map[string]any) (Action, error) {
	a := &taskAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.Title == "" {
		return nil, errors.New("title: required")
	}
	return a, checkTemplates(map[string]string{"title": a.Title, "notes": a.Notes})
}

func (a *taskAction) request(rc *RunContext) (TaskRequest, error) {
	title, err := rc.Render(a.Title)
	if err != nil {
		return TaskRequest{}, Permanent(err)
	}
	notes, err := rc.Render(a.Notes)
	if err != nil {
		return TaskRequest{}, Permanent(err)
	}
	t := TaskRequest{List: a.List, Title: title, Notes: notes, Ref: rc.Item()}
	if a.DueIn > 0 {
		t.Due = rc.Now.Add(a.DueIn.D())
	}
	return t, nil
}

func (a *taskAction) Plan(rc *RunContext) (Plan, error) {
	t, err := a.request(rc)
	due := ""
	if !t.Due.IsZero() {
		due = t.Due.Format(time.RFC3339)
	}
	return Plan{Type: ActCreateTask, Summary: fmt.Sprintf("create task %q", t.Title),
		Params: map[string]string{"title": t.Title, "due": due, "list": t.List}}, err
}

func (a *taskAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Tasks == nil {
		return nil, Permanent(errors.New("no task backend configured"))
	}
	t, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	id, err := rc.Deps.Tasks.CreateTask(ctx, t)
	return Output{"id": id}, err
}

type eventAction struct {
	Calendar string   `yaml:"calendar,omitempty"`
	Title    string   `yaml:"title"`
	Notes    string   `yaml:"notes,omitempty"`
	Location string   `yaml:"location,omitempty"`
	StartIn  Duration `yaml:"start_in,omitempty"`
	Duration Duration `yaml:"duration,omitempty"` // default 1h
}

func newEventAction(p map[string]any) (Action, error) {
	a := &eventAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.Title == "" {
		return nil, errors.New("title: required")
	}
	if a.Duration == 0 {
		a.Duration = Duration(time.Hour)
	}
	return a, checkTemplates(map[string]string{"title": a.Title, "notes": a.Notes, "location": a.Location})
}

func (a *eventAction) request(rc *RunContext) (EventRequest, error) {
	var out [3]string
	for i, t := range []string{a.Title, a.Notes, a.Location} {
		s, err := rc.Render(t)
		if err != nil {
			return EventRequest{}, Permanent(err)
		}
		out[i] = s
	}
	return EventRequest{Calendar: a.Calendar, Title: out[0], Notes: out[1], Location: out[2],
		Start: rc.Now.Add(a.StartIn.D()), Duration: a.Duration.D(), Ref: rc.Item()}, nil
}

func (a *eventAction) Plan(rc *RunContext) (Plan, error) {
	e, err := a.request(rc)
	return Plan{Type: ActCreateEvent, Summary: fmt.Sprintf("create event %q at %s", e.Title, e.Start.Format(time.RFC3339)),
		Params: map[string]string{"title": e.Title, "start": e.Start.Format(time.RFC3339), "duration": a.Duration.String()}}, err
}

func (a *eventAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Tasks == nil {
		return nil, Permanent(errors.New("no calendar backend configured"))
	}
	e, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	id, err := rc.Deps.Tasks.CreateEvent(ctx, e)
	return Output{"id": id}, err
}

// ---- notify ----

type notifyAction struct {
	Channel string `yaml:"channel"`
	Title   string `yaml:"title,omitempty"`
	Text    string `yaml:"text"`
}

var channelRe = regexp.MustCompile(`^(desktop|push|(signal|telegram|matrix|whatsapp|mail):[^\s]+)$`)

func newNotifyAction(p map[string]any) (Action, error) {
	a := &notifyAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.Channel == "" {
		a.Channel = "desktop"
	}
	if !channelRe.MatchString(a.Channel) {
		return nil, fmt.Errorf("channel %q: use desktop, push or <messenger>:<target> (e.g. signal:me)", a.Channel)
	}
	if a.Text == "" {
		return nil, errors.New("text: required")
	}
	return a, checkTemplates(map[string]string{"title": a.Title, "text": a.Text})
}

func (a *notifyAction) render(rc *RunContext) (string, string, error) {
	title, err := rc.Render(a.Title)
	if err != nil {
		return "", "", Permanent(err)
	}
	text, err := rc.Render(a.Text)
	if err != nil {
		return "", "", Permanent(err)
	}
	return title, text, nil
}

func (a *notifyAction) Plan(rc *RunContext) (Plan, error) {
	title, text, err := a.render(rc)
	return Plan{Type: ActNotify, Summary: fmt.Sprintf("notify via %s: %s", a.Channel, text),
		Params: map[string]string{"channel": a.Channel, "title": title, "text": text}}, err
}

func (a *notifyAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Notifier == nil {
		return nil, Permanent(errors.New("no notifier configured"))
	}
	title, text, err := a.render(rc)
	if err != nil {
		return nil, err
	}
	return nil, rc.Deps.Notifier.Notify(ctx, a.Channel, title, text)
}

// ---- webhook ----

type webhookAction struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method,omitempty"` // POST (default) or PUT
	Headers map[string]string `yaml:"headers,omitempty"`
	// Body is a text template; empty sends the default JSON document.
	Body    string   `yaml:"body,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty"`
	// IncludeBody adds the mail/chat text to the default JSON document.
	IncludeBody bool `yaml:"include_body,omitempty"`
}

func newWebhookAction(p map[string]any) (Action, error) {
	a := &webhookAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("url %q: must be an absolute http(s) URL", a.URL)
	}
	a.Method = strings.ToUpper(a.Method)
	if a.Method == "" {
		a.Method = http.MethodPost
	}
	if a.Method != http.MethodPost && a.Method != http.MethodPut {
		return nil, fmt.Errorf("method %q: only POST and PUT are supported", a.Method)
	}
	if a.Timeout == 0 {
		a.Timeout = Duration(defaultWebhookTTL)
	}
	if a.Timeout.D() > maxWebhookTTL {
		return nil, fmt.Errorf("timeout: at most %s", maxWebhookTTL)
	}
	tpls := map[string]string{"body": a.Body}
	for k, v := range a.Headers {
		tpls["headers."+k] = v
	}
	return a, checkTemplates(tpls)
}

// WebhookPayload is the default JSON document posted by the webhook action.
type WebhookPayload struct {
	Workflow string            `json:"workflow"`
	Event    string            `json:"event"`
	EventID  string            `json:"eventId"`
	Time     time.Time         `json:"time"`
	Ref      string            `json:"ref,omitempty"`
	Mail     *WebhookMail      `json:"mail,omitempty"`
	Chat     *WebhookChat      `json:"chat,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
}

// WebhookMail is the mail part of WebhookPayload.
type WebhookMail struct {
	From        string          `json:"from"`
	To          []string        `json:"to"`
	Subject     string          `json:"subject"`
	Folder      string          `json:"folder,omitempty"`
	Category    string          `json:"category,omitempty"`
	Attachments []AttachmentEnv `json:"attachments,omitempty"`
	Body        string          `json:"body,omitempty"`
}

// WebhookChat is the chat part of WebhookPayload.
type WebhookChat struct {
	Channel string `json:"channel"`
	Sender  string `json:"sender"`
	Text    string `json:"text,omitempty"`
}

func (a *webhookAction) body(rc *RunContext) ([]byte, string, error) {
	if a.Body != "" {
		s, err := rc.Render(a.Body)
		return []byte(s), "text/plain; charset=utf-8", err
	}
	if a.IncludeBody {
		if err := pgpGuard(rc, "sending"); err != nil {
			return nil, "", err
		}
	}
	p := WebhookPayload{Event: rc.Env.Event, EventID: rc.Event.ID, Time: rc.Now.UTC(), Ref: rc.Event.Ref, Vars: rc.Vars}
	if rc.Workflow != nil {
		p.Workflow = rc.Workflow.ID
	}
	if rc.Event.Mail != nil || rc.Event.Draft != nil {
		m := rc.Env.Mail
		p.Mail = &WebhookMail{From: m.From, To: m.To, Subject: m.Subject, Folder: m.Folder, Category: m.Category, Attachments: m.Attachments}
		if a.IncludeBody {
			p.Mail.Body = m.Body
		}
	}
	if rc.Event.Chat != nil {
		p.Chat = &WebhookChat{Channel: rc.Env.Chat.Channel, Sender: rc.Env.Chat.Sender}
		if a.IncludeBody {
			p.Chat.Text = rc.Env.Chat.Text
		}
	}
	b, err := json.Marshal(p)
	return b, "application/json", err
}

func (a *webhookAction) Plan(rc *RunContext) (Plan, error) {
	b, _, err := a.body(rc)
	return Plan{Type: ActWebhook, ExternalEffect: true, Summary: fmt.Sprintf("%s %s", a.Method, a.URL),
		Params: map[string]string{"url": a.URL, "method": a.Method, "body": string(b)}}, err
}

func (a *webhookAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	body, ctype, err := a.body(rc)
	if err != nil {
		return nil, Permanent(err)
	}
	ctx, cancel := context.WithTimeout(ctx, a.Timeout.D())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, a.Method, a.URL, bytes.NewReader(body))
	if err != nil {
		return nil, Permanent(err)
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "TuxMail-Workflow/1")
	for k, v := range a.Headers {
		s, err := rc.Render(v)
		if err != nil {
			return nil, Permanent(err)
		}
		req.Header.Set(k, s)
	}
	client := http.DefaultClient
	if rc.Deps != nil && rc.Deps.HTTPClient != nil {
		client = rc.Deps.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	out := Output{"status": fmt.Sprint(resp.StatusCode)}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return out, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout:
		return out, Permanent(fmt.Errorf("webhook: HTTP %d", resp.StatusCode))
	}
	return out, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
}

// ---- set_var / run_workflow ----

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

type setVarAction struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

func newSetVarAction(p map[string]any) (Action, error) {
	a := &setVarAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if !varName.MatchString(a.Name) {
		return nil, fmt.Errorf("name %q: must be an identifier (letters, digits, '_')", a.Name)
	}
	return a, checkTemplates(map[string]string{"value": a.Value})
}

func (a *setVarAction) Plan(rc *RunContext) (Plan, error) {
	v, err := rc.Render(a.Value)
	if err == nil {
		rc.Vars[a.Name] = v // later actions of the dry run see the value
	}
	return Plan{Type: ActSetVar, Summary: fmt.Sprintf("set %s = %q", a.Name, v), Params: map[string]string{a.Name: v}}, err
}

func (a *setVarAction) Execute(_ context.Context, rc *RunContext) (Output, error) {
	v, err := rc.Render(a.Value)
	if err != nil {
		return nil, Permanent(err)
	}
	return Output{"var." + a.Name: v}, nil
}

type runWorkflowAction struct {
	ID string `yaml:"id"`
}

func newRunWorkflowAction(p map[string]any) (Action, error) {
	a := &runWorkflowAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if !idPattern.MatchString(a.ID) {
		return nil, fmt.Errorf("id %q: not a valid workflow id", a.ID)
	}
	return a, nil
}

func (a *runWorkflowAction) Plan(*RunContext) (Plan, error) {
	return Plan{Type: ActRunWorkflow, Summary: "run workflow " + a.ID, Params: map[string]string{"id": a.ID}}, nil
}

func (a *runWorkflowAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.runSub == nil {
		return nil, Permanent(errors.New("sub-workflows are not available here"))
	}
	return rc.runSub(ctx, rc, a.ID)
}

// ---- remind_no_reply ----

type remindAction struct {
	After Duration `yaml:"after"`
}

func newRemindAction(p map[string]any) (Action, error) {
	a := &remindAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if a.After <= 0 {
		return nil, errors.New("after: required, e.g. 3d")
	}
	return a, nil
}

func (a *remindAction) Plan(rc *RunContext) (Plan, error) {
	return Plan{Type: ActRemindNoReply, Summary: "remind if no reply by " + rc.Now.Add(a.After.D()).Format(time.RFC3339),
		Params: map[string]string{"after": a.After.String()}}, nil
}

func (a *remindAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Followups == nil {
		return nil, Permanent(errors.New("no follow-up scheduler configured"))
	}
	ref := rc.Item()
	if ref.Kind != "mail" {
		return nil, Permanent(errors.New("event does not refer to a mail"))
	}
	m := rc.Env.Mail
	sent := m.Date
	if sent.IsZero() {
		sent = rc.Now
	}
	id, err := rc.Deps.Followups.Schedule(ctx, followup.Request{AccountID: ref.AccountID, MessageKey: ref.Key,
		Subject: m.Subject, Recipients: m.Recipients, SentAt: sent, After: a.After.D()})
	return Output{"followup": fmt.Sprint(id)}, err
}

// ---- vacation_reply ----

type vacationAction struct{}

func newVacationAction(p map[string]any) (Action, error) {
	a := &vacationAction{}
	return a, DecodeParams(p, a)
}

func (*vacationAction) Plan(rc *RunContext) (Plan, error) {
	to := rc.Env.Mail.From
	if to == "" {
		to = rc.Env.Chat.Sender
	}
	return Plan{Type: ActVacation, ExternalEffect: true,
		Summary: "send out-of-office reply to " + to + " (if not already answered and not bulk mail)"}, nil
}

func (*vacationAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	if rc.Deps == nil || rc.Deps.Vacation == nil {
		return nil, Permanent(errors.New("no out-of-office responder configured"))
	}
	replied, reason, err := rc.Deps.Vacation.AutoReply(ctx, rc.Event)
	return Output{"replied": fmt.Sprint(replied), "reason": reason}, err
}

// ---- warn / block (mail.before_send) ----

type findingAction struct {
	severity events.Severity
	Message  string `yaml:"message"`
	DE       string `yaml:"message_de,omitempty"`
	Key      string `yaml:"key,omitempty"`
}

func newFindingAction(sev events.Severity) func(map[string]any) (Action, error) {
	return func(p map[string]any) (Action, error) {
		a := &findingAction{severity: sev}
		if err := DecodeParams(p, a); err != nil {
			return nil, err
		}
		if a.Message == "" && a.DE == "" {
			return nil, errors.New("message: required")
		}
		return a, checkTemplates(map[string]string{"message": a.Message, "message_de": a.DE})
	}
}

func (a *findingAction) finding(rc *RunContext) (events.Finding, error) {
	en, err := rc.Render(a.Message)
	if err != nil {
		return events.Finding{}, Permanent(err)
	}
	de, err := rc.Render(a.DE)
	if err != nil {
		return events.Finding{}, Permanent(err)
	}
	if de == "" {
		de = en
	}
	if en == "" {
		en = de
	}
	key := a.Key
	if key == "" {
		key = "workflow.custom_check"
	}
	check := "workflow"
	if rc.Workflow != nil {
		check = "workflow:" + rc.Workflow.ID
	}
	return events.Finding{Check: check, Severity: a.severity, Key: key, DE: de, EN: en}, nil
}

func (a *findingAction) Plan(rc *RunContext) (Plan, error) {
	f, err := a.finding(rc)
	return Plan{Type: string(map[events.Severity]string{events.SeverityWarn: ActWarn, events.SeverityBlock: ActBlock}[a.severity]),
		Summary: string(a.severity) + ": " + f.EN, Params: map[string]string{"message": f.EN}}, err
}

func (a *findingAction) Execute(_ context.Context, rc *RunContext) (Output, error) {
	f, err := a.finding(rc)
	if err != nil {
		return nil, err
	}
	rc.Findings = append(rc.Findings, f)
	return Output{"severity": string(a.severity)}, nil
}

func globMatch(pattern, name string) bool {
	ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return ok
}
