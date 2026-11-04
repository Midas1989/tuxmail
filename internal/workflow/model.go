// SPDX-License-Identifier: AGPL-3.0-or-later

// Package workflow implements TuxMail's workflow system (KONZEPT 8.5):
// trigger → condition → actions, with a YAML representation, a safe
// expression language for conditions (expr-lang/expr), a registry of actions,
// durable execution through the operations queue, a run log, dry runs, loop
// protection and rate limiting.
//
// A workflow in YAML:
//
//	id: invoices-to-nextcloud
//	name: Rechnungen als PDF in Nextcloud
//	enabled: true
//	trigger:
//	  event: mail.received
//	  folders: [INBOX]
//	condition: mail.isInvoice || any(mail.attachments, {glob("rechnung*.pdf", .name)})
//	actions:
//	  - type: archive_pdf
//	    target: nextcloud
//	    path: "/Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}.pdf"
//	options:
//	  loop_protection: {max_per_message: 1, window: 24h}
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Midas1989/Mailclient/internal/events"
)

// Workflow is a single automation rule.
type Workflow struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Enabled     bool         `yaml:"enabled"`
	Trigger     Trigger      `yaml:"trigger"`
	Condition   Condition    `yaml:"condition,omitempty"`
	Actions     []ActionSpec `yaml:"actions"`
	Options     Options      `yaml:"options,omitempty"`
}

// Trigger selects the events a workflow reacts to. All set filters must match.
type Trigger struct {
	Event events.Type `yaml:"event"`
	// Accounts restricts mail/chat/file events to these account ids.
	Accounts []int64 `yaml:"accounts,omitempty"`
	// Folders restricts mail events to these folder names (case-insensitive;
	// "inbox" also matches the folder with the inbox role).
	Folders []string `yaml:"folders,omitempty"`
	// Channels restricts chat.received to these channels (whatsapp, signal ...).
	Channels []string `yaml:"channels,omitempty"`
	// Schedule is the schedule name for schedule.tick (required there).
	Schedule string `yaml:"schedule,omitempty"`
	// Name is the hook name for webhook triggers (empty matches all).
	Name string `yaml:"name,omitempty"`

	// Filters for tags, projects and search folders (see organize.go).
	AccountNames  []string `yaml:"account_names,omitempty"`
	Tags          []string `yaml:"tags,omitempty"`
	Projects      []string `yaml:"projects,omitempty"`
	Statuses      []string `yaml:"statuses,omitempty"`
	SearchFolders []string `yaml:"search_folders,omitempty"`
}

// ActionSpec is an action as written in YAML: a type plus type-specific
// parameters inlined next to it.
type ActionSpec struct {
	Type string `yaml:"type"`
	// If is an optional per-action condition (expression); the action is
	// skipped when it evaluates to false.
	If string `yaml:"if,omitempty"`
	// ContinueOnError keeps executing the following actions when this one fails.
	ContinueOnError bool           `yaml:"continue_on_error,omitempty"`
	Params          map[string]any `yaml:",inline"`
}

// RunLocation says where a workflow runs (KONZEPT 8.5 "Ausführungsort").
type RunLocation string

const (
	RunAnywhere RunLocation = ""       // default: wherever an engine runs
	RunLocal    RunLocation = "local"  // only in the desktop client
	RunServer   RunLocation = "server" // only in server mode
)

// Options are the safety and execution options of a workflow.
type Options struct {
	LoopProtection LoopProtection `yaml:"loop_protection,omitempty"`
	RateLimit      RateLimit      `yaml:"rate_limit,omitempty"`
	RunLocation    RunLocation    `yaml:"run_location,omitempty"`
	// AllowDecryptedPGP permits actions with external effect (forward,
	// webhook) to pass on decrypted content of PGP-encrypted mails.
	AllowDecryptedPGP bool `yaml:"allow_decrypted_pgp,omitempty"`
}

// LoopProtection limits how often a workflow may fire for the same item.
type LoopProtection struct {
	// MaxPerMessage is the maximum number of runs per item (event Ref) within
	// Window (default 3).
	MaxPerMessage int `yaml:"max_per_message,omitempty"`
	// Window is the sliding window (default 1h).
	Window Duration `yaml:"window,omitempty"`
	// MaxDepth is the maximum length of the workflow chain that caused an
	// event (default 5). A workflow never fires for an event it caused itself.
	MaxDepth int `yaml:"max_depth,omitempty"`
}

// RateLimit limits the number of runs of a workflow per time.
type RateLimit struct {
	Max int      `yaml:"max,omitempty"` // default 60
	Per Duration `yaml:"per,omitempty"` // default 1m
}

// Defaults for options.
const (
	DefaultMaxPerMessage = 3
	DefaultLoopWindow    = Duration(3600e9)
	DefaultMaxDepth      = 5
	// MaxAllowedDepth caps a configured max_depth (run_workflow recursion).
	MaxAllowedDepth = 32
	DefaultRateMax  = 60
	DefaultRatePer  = Duration(60e9)
)

func (o Options) maxPerMessage() int {
	if o.LoopProtection.MaxPerMessage > 0 {
		return o.LoopProtection.MaxPerMessage
	}
	return DefaultMaxPerMessage
}

func (o Options) loopWindow() Duration {
	if o.LoopProtection.Window > 0 {
		return o.LoopProtection.Window
	}
	return DefaultLoopWindow
}

func (o Options) maxDepth() int {
	if o.LoopProtection.MaxDepth > 0 {
		return min(o.LoopProtection.MaxDepth, MaxAllowedDepth)
	}
	return DefaultMaxDepth
}

func (o Options) rate() (int, Duration) {
	max, per := o.RateLimit.Max, o.RateLimit.Per
	if max <= 0 {
		max = DefaultRateMax
	}
	if per <= 0 {
		per = DefaultRatePer
	}
	return max, per
}

// Parse decodes a workflow from YAML. Unknown top-level fields are rejected.
// Parse does not validate actions and conditions; call Validate for that.
func Parse(data []byte) (*Workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var w Workflow
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("workflow: parse yaml: %w", err)
	}
	return &w, nil
}

// YAML serializes the workflow with two-space indentation; Parse reads it back.
func (w *Workflow) YAML() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ValidationError collects all problems found in a workflow.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "workflow invalid: " + e.Problems[0]
	}
	return "workflow invalid:\n  - " + strings.Join(e.Problems, "\n  - ")
}

func (e *ValidationError) add(format string, args ...any) {
	e.Problems = append(e.Problems, fmt.Sprintf(format, args...))
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Validate checks the workflow against the action registry: ids, trigger,
// condition syntax and types, action types and their parameters. It returns
// a *ValidationError listing every problem, or nil.
func (w *Workflow) Validate(reg *Registry) error {
	ve := &ValidationError{}
	if !idPattern.MatchString(w.ID) {
		ve.add("id %q: must be 1-64 characters of a-z, 0-9, '_', '.', '-' and start with a letter or digit", w.ID)
	}
	if strings.TrimSpace(w.Name) == "" {
		ve.add("name: required")
	}
	w.validateTrigger(ve)
	w.validateTriggerExtra(ve)
	if _, err := w.Condition.Compile(); err != nil {
		ve.add("condition: %v", err)
	}
	if len(w.Actions) == 0 {
		ve.add("actions: at least one action is required")
	}
	if reg == nil {
		reg = DefaultRegistry()
	}
	for i, a := range w.Actions {
		prefix := fmt.Sprintf("actions[%d]", i)
		if a.Type == "" {
			ve.add("%s: type is required", prefix)
			continue
		}
		prefix += " (" + a.Type + ")"
		at, ok := reg.Lookup(a.Type)
		if !ok {
			ve.add("%s: unknown action type; known types: %s", prefix, strings.Join(reg.Names(), ", "))
			continue
		}
		if len(at.Triggers) > 0 && !containsType(at.Triggers, w.Trigger.Event) {
			ve.add("%s: not allowed for trigger %q (allowed: %s)", prefix, w.Trigger.Event, joinTypes(at.Triggers))
		}
		if a.If != "" {
			if _, err := compileExpr(a.If); err != nil {
				ve.add("%s: if: %v", prefix, err)
			}
		}
		if _, err := at.New(a.Params); err != nil {
			ve.add("%s: %v", prefix, err)
		}
	}
	switch w.Options.RunLocation {
	case RunAnywhere, RunLocal, RunServer:
	default:
		ve.add("options.run_location %q: must be empty, %q or %q", w.Options.RunLocation, RunLocal, RunServer)
	}
	if w.Options.LoopProtection.MaxPerMessage < 0 || w.Options.LoopProtection.MaxDepth < 0 || w.Options.RateLimit.Max < 0 {
		ve.add("options: limits must not be negative")
	}
	if len(ve.Problems) > 0 {
		return ve
	}
	return nil
}

func (w *Workflow) validateTrigger(ve *ValidationError) {
	t := w.Trigger
	if t.Event == "" {
		ve.add("trigger.event: required (one of %s)", joinTypes(events.AllTypes))
		return
	}
	if !t.Event.Valid() {
		ve.add("trigger.event %q: unknown (one of %s)", t.Event, joinTypes(events.AllTypes))
		return
	}
	isMail := strings.HasPrefix(string(t.Event), "mail.")
	if len(t.Folders) > 0 && (!isMail || t.Event == events.MailBeforeSend) {
		ve.add("trigger.folders: only allowed for mail.received, mail.sent, mail.moved and mail.flagged")
	}
	if len(t.Channels) > 0 && t.Event != events.ChatReceived {
		ve.add("trigger.channels: only allowed for chat.received")
	}
	if t.Event == events.ScheduleTick && t.Schedule == "" {
		ve.add("trigger.schedule: required for schedule.tick")
	}
	if t.Schedule != "" && t.Event != events.ScheduleTick {
		ve.add("trigger.schedule: only allowed for schedule.tick")
	}
	if t.Name != "" && t.Event != events.Webhook && t.Event != events.Manual {
		ve.add("trigger.name: only allowed for webhook and manual")
	}
}

// ExternalEffects returns the types of the workflow's actions that have an
// effect outside TuxMail (auto-reply, forward, webhook ...). The UI must ask
// for confirmation before such a workflow is enabled (KONZEPT 8.5).
func (w *Workflow) ExternalEffects(reg *Registry) []string {
	if reg == nil {
		reg = DefaultRegistry()
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range w.Actions {
		if at, ok := reg.Lookup(a.Type); ok && at.ExternalEffect && !seen[a.Type] {
			seen[a.Type] = true
			out = append(out, a.Type)
		}
	}
	return out
}

// Clone returns a deep copy (via YAML) of the workflow.
func (w *Workflow) Clone() (*Workflow, error) {
	b, err := w.YAML()
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func containsType(ts []events.Type, t events.Type) bool {
	for _, x := range ts {
		if x == t {
			return true
		}
	}
	return false
}

func joinTypes(ts []events.Type) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}

// ErrNotFound is returned for unknown workflow ids.
var ErrNotFound = errors.New("workflow: not found")
