// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
)

// Action is an instantiated, validated action of a workflow.
type Action interface {
	// Plan describes what Execute would do, without side effects (dry run).
	Plan(rc *RunContext) (Plan, error)
	// Execute performs the action.
	Execute(ctx context.Context, rc *RunContext) (Output, error)
}

// Plan is the dry-run description of an action.
type Plan struct {
	Type           string            `json:"type"`
	Summary        string            `json:"summary"`
	Params         map[string]string `json:"params,omitempty"` // rendered parameters
	ExternalEffect bool              `json:"externalEffect"`
	Skipped        bool              `json:"skipped,omitempty"` // per-action "if" was false
}

// Output is what an executed action reports; it is stored in the run log.
// Keys starting with "var." are set as workflow variables for later actions.
type Output map[string]string

// ActionType describes a registered action.
type ActionType struct {
	Name        string
	Description string
	// ExternalEffect marks actions that act outside TuxMail (auto reply,
	// forward, webhook). Workflows containing them need confirmation when
	// they are enabled.
	ExternalEffect bool
	// Triggers restricts the action to these trigger events (nil = all).
	Triggers []events.Type
	// New decodes and validates parameters.
	New func(params map[string]any) (Action, error)
	// Params describes the parameters for editors (Describe, JSONSchema,
	// rule syntax, completion). Built-in actions without Params use the
	// table in describe.go.
	Params []ParamInfo
}

// Registry maps action type names to implementations.
type Registry struct {
	types map[string]ActionType
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{types: map[string]ActionType{}} }

// DefaultRegistry returns a new registry with all built-in actions.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, t := range builtinActions() {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
	return r
}

// Register adds an action type. Names must be unique.
func (r *Registry) Register(t ActionType) error {
	if t.Name == "" || t.New == nil {
		return errors.New("workflow: action type needs a name and a constructor")
	}
	if _, dup := r.types[t.Name]; dup {
		return fmt.Errorf("workflow: action type %q already registered", t.Name)
	}
	r.types[t.Name] = t
	return nil
}

// Lookup returns a registered action type.
func (r *Registry) Lookup(name string) (ActionType, bool) {
	t, ok := r.types[name]
	return t, ok
}

// Names returns the sorted names of all action types.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.types))
	for n := range r.types {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Types returns all action types sorted by name.
func (r *Registry) Types() []ActionType {
	out := make([]ActionType, 0, len(r.types))
	for _, n := range r.Names() {
		out = append(out, r.types[n])
	}
	return out
}

// Build instantiates an action from its spec.
func (r *Registry) Build(spec ActionSpec) (Action, error) {
	t, ok := r.types[spec.Type]
	if !ok {
		return nil, fmt.Errorf("unknown action type %q", spec.Type)
	}
	return t.New(spec.Params)
}

// DecodeParams decodes YAML action parameters into a struct with yaml tags,
// rejecting unknown fields. Use it in custom action constructors.
func DecodeParams(params map[string]any, into any) error {
	if params == nil {
		params = map[string]any{}
	}
	b, err := yaml.Marshal(params)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		// Strip the generic yaml prefix and line numbers of the re-encoded
		// document, which are meaningless to the user.
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		msg = strings.TrimPrefix(msg, "unmarshal errors:\n  ")
		if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "line ") {
			msg = msg[i+2:]
		}
		return errors.New(msg)
	}
	return nil
}

// ==== Backend interfaces (implemented by other packages) ======================

// ItemRef identifies the item an action works on.
type ItemRef struct {
	Kind      string `json:"kind"` // "mail", "draft", "chat", "file"
	AccountID int64  `json:"accountId"`
	Key       string `json:"key"`
	Folder    string `json:"folder,omitempty"`
	RemoteID  string `json:"remoteId,omitempty"`
}

// ForwardRequest parameterizes MailActions.Forward.
type ForwardRequest struct {
	To           []string
	Note         string
	AsAttachment bool
}

// ReplyRequest parameterizes MailActions.ReplyTemplate and ReplyAutomatic.
type ReplyRequest struct {
	Subject  string // empty = "Re: <original subject>"
	Body     string // rendered text
	ReplyAll bool
	// Send sends immediately; otherwise a draft is created.
	Send bool
	// Headers are extra headers (e.g. Auto-Submitted: auto-replied).
	Headers map[string]string
	// To overrides the recipient (default: Reply-To or From of the original).
	To string
}

// MailActions performs mail operations. Implemented by the mail core.
type MailActions interface {
	Move(ctx context.Context, ref ItemRef, folder string) error
	Copy(ctx context.Context, ref ItemRef, folder string) error
	SetFlags(ctx context.Context, ref ItemRef, add, remove []string) error
	SetLabels(ctx context.Context, ref ItemRef, add, remove []string) error
	Delete(ctx context.Context, ref ItemRef, permanent bool) error
	Archive(ctx context.Context, ref ItemRef) error
	Forward(ctx context.Context, ref ItemRef, req ForwardRequest) error
	ReplyTemplate(ctx context.Context, ref ItemRef, req ReplyRequest) error
	// ReplyAutomatic sends an automatic reply (out-of-office). Implementations
	// must set "Auto-Submitted: auto-replied" (RFC 3834) and send from the
	// identity that received ref.
	ReplyAutomatic(ctx context.Context, ref ItemRef, req ReplyRequest) error
}

// UploadRequest parameterizes CloudActions.UploadAttachments.
type UploadRequest struct {
	Target          string // cloud account/provider name, e.g. "nextcloud"
	Dir             string // rendered target directory
	Pattern         string // file-name glob (case-insensitive), "" = all
	MinSize         int64
	ShareLink       bool
	ShareExpiry     time.Duration
	ReplaceWithLink bool // drafts: replace the attachment by a share link
}

// UploadedFile is the result of an upload.
type UploadedFile struct {
	Name string
	Path string
	URL  string // share link if requested
}

// CloudActions uploads attachments to cloud storage (Nextcloud, OpenCloud, WebDAV).
type CloudActions interface {
	UploadAttachments(ctx context.Context, ref ItemRef, req UploadRequest) ([]UploadedFile, error)
}

// PDFRequest parameterizes PDFActions.ArchivePDF.
type PDFRequest struct {
	Target             string // "local", "nextcloud", "opencloud", "webdav" or account name
	Path               string // rendered, sanitized path incl. file name
	EmbedOriginal      bool   // embed the .eml (PDF/A-3)
	IncludeAttachments bool
	SkipDuplicates     bool // duplicate protection via Message-ID
}

// PDFActions renders and archives mails as PDF/A (KONZEPT 8.11).
type PDFActions interface {
	ArchivePDF(ctx context.Context, ref ItemRef, req PDFRequest) (path string, err error)
}

// TaskRequest describes a task to create.
type TaskRequest struct {
	List  string
	Title string
	Notes string
	Due   time.Time // zero = no due date
	Ref   ItemRef
}

// EventRequest describes a calendar event to create.
type EventRequest struct {
	Calendar string
	Title    string
	Notes    string
	Location string
	Start    time.Time
	Duration time.Duration
	Ref      ItemRef
}

// TaskActions creates tasks and calendar events.
type TaskActions interface {
	CreateTask(ctx context.Context, t TaskRequest) (id string, err error)
	CreateEvent(ctx context.Context, e EventRequest) (id string, err error)
}

// Notifier delivers notifications. Channels: "desktop", "push", or a
// messenger target like "signal:me", "telegram:me", "matrix:me".
type Notifier interface {
	Notify(ctx context.Context, channel, title, text string) error
}

// FollowupScheduler schedules "waiting for reply" reminders.
type FollowupScheduler interface {
	Schedule(ctx context.Context, req followup.Request) (int64, error)
}

// VacationResponder handles out-of-office replies (implemented by
// internal/workflow/vacation.Responder). It decides itself whether to reply
// (time window, scope, bulk mail, once per sender) and returns the decision.
type VacationResponder interface {
	AutoReply(ctx context.Context, ev events.Event) (replied bool, reason string, err error)
}

// Deps are the backends available to actions. Missing backends make the
// corresponding actions fail permanently with a clear error.
type Deps struct {
	Mail      MailActions
	Cloud     CloudActions
	PDF       PDFActions
	Tasks     TaskActions
	Notifier  Notifier
	Followups FollowupScheduler
	Vacation  VacationResponder
	// HTTPClient is used by the webhook action (a client with a 30s timeout
	// if nil). Tests inject a client pointing at httptest servers.
	HTTPClient *http.Client

	// Tags, projects, search folders and query conditions (organize.go,
	// implemented by internal/project).
	Tags          TagActions
	Projects      ProjectActions
	SearchFolders SearchFolderActions
	Query         QueryChecker
}

// ==== Run context =============================================================

// RunContext is passed to actions.
type RunContext struct {
	Workflow *Workflow
	Event    events.Event
	Env      *Env
	Vars     map[string]string
	Deps     *Deps
	Now      time.Time
	DryRun   bool
	// Findings collects warn/block results of mail.before_send workflows.
	Findings []events.Finding

	runSub func(ctx context.Context, rc *RunContext, id string) (Output, error)
	depth  int
}

// Render renders a text template.
func (rc *RunContext) Render(tpl string) (string, error) {
	rc.Env.Vars = rc.Vars
	return RenderText(rc.Env, rc.Vars, rc.Now, tpl)
}

// RenderPath renders a path template (see RenderPath).
func (rc *RunContext) RenderPath(tpl string) (string, error) {
	rc.Env.Vars = rc.Vars
	return RenderPath(rc.Env, rc.Vars, rc.Now, tpl)
}

// Item returns the reference of the item the event is about.
func (rc *RunContext) Item() ItemRef {
	ev := rc.Event
	switch {
	case ev.Mail != nil:
		return ItemRef{Kind: "mail", AccountID: ev.Mail.AccountID, Key: ev.Mail.Key, Folder: ev.Mail.Folder, RemoteID: ev.Mail.RemoteID}
	case ev.Draft != nil:
		return ItemRef{Kind: "draft", AccountID: ev.Draft.AccountID, Key: ev.Draft.Key}
	case ev.NoReply != nil:
		return ItemRef{Kind: "mail", AccountID: ev.NoReply.AccountID, Key: ev.NoReply.MessageKey}
	case ev.Chat != nil:
		return ItemRef{Kind: "chat", AccountID: ev.Chat.AccountID, Key: ev.Chat.Key}
	case ev.File != nil:
		return ItemRef{Kind: "file", AccountID: ev.File.AccountID, Key: ev.File.Path}
	}
	return ItemRef{AccountID: ev.AccountID, Key: ev.Ref}
}

// PermanentError marks an error that must not be retried.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err so that the engine does not retry the run.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err (or a wrapped error) is permanent.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

func eventsZero() events.Event { return events.Event{} }
