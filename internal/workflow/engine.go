// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// OpKind is the operations-queue kind of a workflow run.
const OpKind = "workflow.run"

// ErrConfirmationRequired is returned when a workflow with external-effect
// actions is enabled without the user's confirmation.
var ErrConfirmationRequired = errors.New("workflow: enabling this workflow requires confirmation of actions with external effect")

// Store is the persistence needed by the engine (implemented by *store.Store).
type Store interface {
	ListWorkflows(ctx context.Context) ([]store.WorkflowRecord, error)
	GetWorkflow(ctx context.Context, id string) (store.WorkflowRecord, error)
	PutWorkflow(ctx context.Context, w store.WorkflowRecord) error
	DeleteWorkflow(ctx context.Context, id string) error

	EnqueueWorkflowRun(ctx context.Context, r store.WorkflowRun, opKind string, payload func(runID int64) (string, error)) (store.WorkflowRun, error)
	InsertWorkflowRun(ctx context.Context, r store.WorkflowRun) (int64, error)
	UpdateWorkflowRun(ctx context.Context, r store.WorkflowRun) error
	GetWorkflowRun(ctx context.Context, id int64) (store.WorkflowRun, error)
	ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]store.WorkflowRun, error)

	ClaimNextOpOfKind(ctx context.Context, now time.Time, kinds ...string) (store.Op, bool, error)
	MarkOpDone(ctx context.Context, id int64) error
	MarkOpFailed(ctx context.Context, id int64, cause error, now time.Time, maxAttempts int, permanent bool) (store.OpState, error)
	RetryOp(ctx context.Context, id int64, now time.Time) error
}

// Config configures an Engine.
type Config struct {
	Store    Store
	Bus      *events.Bus // optional; required for Run
	Registry *Registry   // default: DefaultRegistry()
	Deps     Deps
	// Location is where this engine runs: RunLocal (desktop, default) or
	// RunServer. Workflows bound to the other location are ignored.
	Location RunLocation
	// TimeZone for time helpers and templates (default time.Local).
	TimeZone *time.Location
	Now      func() time.Time
	Logger   *slog.Logger
	// MaxAttempts per run before it fails for good (default 5).
	MaxAttempts int
	// PollInterval of the worker when idle (default 5s).
	PollInterval time.Duration
	// HookTimeout bounds mail.before_send workflows (default 5s).
	HookTimeout time.Duration
}

// Engine matches events against workflows and executes them.
type Engine struct {
	cfg  Config
	reg  *Registry
	log  *slog.Logger
	lim  *limiter
	wake chan struct{}

	mu  sync.RWMutex
	wfs map[string]*compiled

	skippedLoop atomic.Uint64
	skippedRate atomic.Uint64
}

type compiled struct {
	wf        *Workflow
	cond      *Program
	actions   []Action
	ifs       []*Program
	confirmed bool
}

// Stats are engine counters.
type Stats struct {
	Workflows   int
	SkippedLoop uint64 // runs suppressed by loop protection
	SkippedRate uint64 // runs suppressed by the rate limit
}

// New creates an engine.
func New(cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, errors.New("workflow: engine needs a store")
	}
	if cfg.Registry == nil {
		cfg.Registry = DefaultRegistry()
	}
	if cfg.Location == RunAnywhere {
		cfg.Location = RunLocal
	}
	if cfg.TimeZone == nil {
		cfg.TimeZone = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.HookTimeout <= 0 {
		cfg.HookTimeout = 5 * time.Second
	}
	return &Engine{cfg: cfg, reg: cfg.Registry, log: cfg.Logger, lim: newLimiter(),
		wake: make(chan struct{}, 1), wfs: map[string]*compiled{}}, nil
}

func (e *Engine) now() time.Time { return e.cfg.Now().In(e.cfg.TimeZone) }

// Registry returns the action registry.
func (e *Engine) Registry() *Registry { return e.reg }

func (e *Engine) compile(w *Workflow) (*compiled, error) {
	if err := w.Validate(e.reg); err != nil {
		return nil, err
	}
	c := &compiled{wf: w}
	var err error
	if c.cond, err = w.compileCondition(); err != nil {
		return nil, err
	}
	for _, a := range w.Actions {
		act, err := e.reg.Build(a)
		if err != nil {
			return nil, err
		}
		var p *Program
		if a.If != "" {
			if p, err = compileExpr(a.If); err != nil {
				return nil, err
			}
		}
		c.actions = append(c.actions, act)
		c.ifs = append(c.ifs, p)
	}
	return c, nil
}

// Load (re)loads all workflows from the store. Invalid workflows are skipped
// and reported in the returned error; valid ones are loaded regardless.
func (e *Engine) Load(ctx context.Context) error {
	recs, err := e.cfg.Store.ListWorkflows(ctx)
	if err != nil {
		return err
	}
	wfs := map[string]*compiled{}
	var errs []error
	for _, r := range recs {
		w, err := Parse([]byte(r.YAML))
		if err == nil {
			w.Enabled = r.Enabled
			var c *compiled
			if c, err = e.compile(w); err == nil {
				c.confirmed = r.ExternalConfirmed
				wfs[w.ID] = c
				continue
			}
		}
		errs = append(errs, fmt.Errorf("workflow %s: %w", r.ID, err))
	}
	e.mu.Lock()
	e.wfs = wfs
	e.mu.Unlock()
	return errors.Join(errs...)
}

// Save validates and stores a workflow (insert or update). If the workflow is
// enabled and has actions with external effect, confirmExternal must be true
// unless the user already confirmed the same set of such actions.
func (e *Engine) Save(ctx context.Context, w *Workflow, confirmExternal bool) error {
	// Keep a private copy so that later changes by the caller do not leak
	// into the running engine.
	enabled := w.Enabled
	w, err := w.Clone()
	if err != nil {
		return err
	}
	w.Enabled = enabled
	c, err := e.compile(w)
	if err != nil {
		return err
	}
	confirmed := confirmExternal
	if !confirmed {
		e.mu.RLock()
		old := e.wfs[w.ID]
		e.mu.RUnlock()
		if old != nil && old.confirmed && subset(w.ExternalEffects(e.reg), old.wf.ExternalEffects(e.reg)) {
			confirmed = true
		}
	}
	if w.Enabled && len(w.ExternalEffects(e.reg)) > 0 && !confirmed {
		return ErrConfirmationRequired
	}
	y, err := w.YAML()
	if err != nil {
		return err
	}
	if err := e.cfg.Store.PutWorkflow(ctx, store.WorkflowRecord{ID: w.ID, Name: w.Name, Enabled: w.Enabled,
		YAML: string(y), ExternalConfirmed: confirmed}); err != nil {
		return err
	}
	c.confirmed = confirmed
	e.mu.Lock()
	e.wfs[w.ID] = c
	e.mu.Unlock()
	return nil
}

func subset(a, b []string) bool {
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

// SetEnabled enables or disables a workflow (see Save for confirmExternal).
func (e *Engine) SetEnabled(ctx context.Context, id string, enabled, confirmExternal bool) error {
	w, ok := e.Get(id)
	if !ok {
		return ErrNotFound
	}
	w.Enabled = enabled
	return e.Save(ctx, w, confirmExternal)
}

// Delete removes a workflow and its run log.
func (e *Engine) Delete(ctx context.Context, id string) error {
	if err := e.cfg.Store.DeleteWorkflow(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	e.mu.Lock()
	delete(e.wfs, id)
	e.mu.Unlock()
	return nil
}

// Get returns a copy of a loaded workflow.
func (e *Engine) Get(id string) (*Workflow, bool) {
	e.mu.RLock()
	c := e.wfs[id]
	e.mu.RUnlock()
	if c == nil {
		return nil, false
	}
	w, err := c.wf.Clone()
	if err != nil {
		return nil, false
	}
	w.Enabled = c.wf.Enabled
	return w, true
}

// List returns copies of all loaded workflows sorted by name.
func (e *Engine) List() []*Workflow {
	e.mu.RLock()
	ids := make([]string, 0, len(e.wfs))
	for id := range e.wfs {
		ids = append(ids, id)
	}
	e.mu.RUnlock()
	var out []*Workflow
	for _, id := range ids {
		if w, ok := e.Get(id); ok {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Runs returns the newest run log entries of a workflow ("" = all).
func (e *Engine) Runs(ctx context.Context, workflowID string, limit int) ([]store.WorkflowRun, error) {
	return e.cfg.Store.ListWorkflowRuns(ctx, workflowID, limit)
}

// Stats returns engine counters.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	n := len(e.wfs)
	e.mu.RUnlock()
	return Stats{Workflows: n, SkippedLoop: e.skippedLoop.Load(), SkippedRate: e.skippedRate.Load()}
}

// ==== Matching ================================================================

// Matches reports whether the trigger selects ev.
func (t Trigger) Matches(ev events.Event) bool {
	if ev.Type != t.Event {
		return false
	}
	if len(t.Accounts) > 0 {
		acc := ev.AccountID
		switch {
		case ev.Mail != nil:
			acc = ev.Mail.AccountID
		case ev.Draft != nil:
			acc = ev.Draft.AccountID
		case ev.Chat != nil:
			acc = ev.Chat.AccountID
		case ev.File != nil:
			acc = ev.File.AccountID
		}
		found := false
		for _, a := range t.Accounts {
			found = found || a == acc
		}
		if !found {
			return false
		}
	}
	if len(t.Folders) > 0 {
		if ev.Mail == nil {
			return false
		}
		found := false
		for _, f := range t.Folders {
			found = found || strings.EqualFold(f, ev.Mail.Folder) ||
				(ev.Mail.FolderRole != "" && strings.EqualFold(f, ev.Mail.FolderRole))
		}
		if !found {
			return false
		}
	}
	if len(t.Channels) > 0 {
		if ev.Chat == nil {
			return false
		}
		found := false
		for _, c := range t.Channels {
			found = found || strings.EqualFold(c, ev.Chat.Channel)
		}
		if !found {
			return false
		}
	}
	if t.Schedule != "" && (ev.Schedule == nil || ev.Schedule.Name != t.Schedule) {
		return false
	}
	if t.Name != "" && t.Event == events.Webhook && (ev.Webhook == nil || ev.Webhook.Name != t.Name) {
		return false
	}
	if t.Name != "" && t.Event == events.Manual && ev.Data["name"] != t.Name {
		return false
	}
	return t.matchesOrganize(ev)
}

func (e *Engine) locationOK(w *Workflow) bool {
	return w.Options.RunLocation == RunAnywhere || w.Options.RunLocation == e.cfg.Location
}

// guard applies chain, loop and rate protection; it records the run when allowed.
func (e *Engine) guard(w *Workflow, ev events.Event, now time.Time) (bool, string) {
	for _, id := range ev.Chain {
		if id == w.ID {
			e.skippedLoop.Add(1)
			return false, reasonChain
		}
	}
	if len(ev.Chain) >= w.Options.maxDepth() {
		e.skippedLoop.Add(1)
		return false, reasonDepth
	}
	ok, reason := e.lim.allow(w, ev.Ref, now)
	if !ok {
		if reason == reasonRate {
			e.skippedRate.Add(1)
		} else {
			e.skippedLoop.Add(1)
		}
	}
	return ok, reason
}

func (e *Engine) snapshot() []*compiled {
	e.mu.RLock()
	out := make([]*compiled, 0, len(e.wfs))
	for _, c := range e.wfs {
		out = append(out, c)
	}
	e.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].wf.ID < out[j].wf.ID })
	return out
}

// HandleEvent matches ev against all enabled workflows and enqueues a durable
// run for each match. It returns the ids of the queued runs. Events of type
// mail.before_send are handled by BeforeSend instead.
func (e *Engine) HandleEvent(ctx context.Context, ev events.Event) ([]int64, error) {
	if ev.Type.IsHook() {
		return nil, nil
	}
	if ev.ID == "" {
		ev.ID = events.NewID()
	}
	now := e.now()
	if ev.Time.IsZero() {
		ev.Time = now.UTC()
	}
	var ids []int64
	var errs []error
	for _, c := range e.snapshot() {
		w := c.wf
		if !w.Enabled || !e.locationOK(w) || !w.Trigger.Matches(ev) {
			continue
		}
		ok, err := c.cond.Eval(attachRuntime(ctx, NewEnv(ev, now, nil), &e.cfg.Deps, ev))
		if err != nil {
			errs = append(errs, fmt.Errorf("workflow %s: %w", w.ID, err))
			continue
		}
		if !ok {
			continue
		}
		if allowed, reason := e.guard(w, ev, now); !allowed {
			e.log.Warn("workflow run suppressed", "workflow", w.ID, "event", ev.Type, "ref", ev.Ref, "reason", reason)
			continue
		}
		id, err := e.enqueue(ctx, w.ID, ev, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		e.poke()
	}
	return ids, errors.Join(errs...)
}

type opPayload struct {
	Run      int64        `json:"run"`
	Workflow string       `json:"workflow"`
	Manual   bool         `json:"manual,omitempty"`
	Event    events.Event `json:"event"`
}

func (e *Engine) enqueue(ctx context.Context, wfID string, ev events.Event, manual bool) (int64, error) {
	r, err := e.cfg.Store.EnqueueWorkflowRun(ctx, store.WorkflowRun{
		WorkflowID: wfID, EventID: ev.ID, EventType: string(ev.Type), EventRef: ev.Ref, CreatedAt: e.cfg.Now(),
	}, OpKind, func(runID int64) (string, error) {
		b, err := json.Marshal(opPayload{Run: runID, Workflow: wfID, Manual: manual, Event: ev})
		return string(b), err
	})
	if err != nil {
		return 0, fmt.Errorf("workflow %s: enqueue: %w", wfID, err)
	}
	return r.ID, nil
}

// RunManual queues a run of workflow id for ev regardless of its trigger,
// condition and enabled state (the user started it explicitly). Loop
// protection and rate limiting still apply.
func (e *Engine) RunManual(ctx context.Context, id string, ev events.Event) (int64, error) {
	e.mu.RLock()
	c := e.wfs[id]
	e.mu.RUnlock()
	if c == nil {
		return 0, ErrNotFound
	}
	if ev.Type == "" {
		ev.Type = events.Manual
	}
	if ev.ID == "" {
		ev.ID = events.NewID()
	}
	now := e.now()
	if ev.Time.IsZero() {
		ev.Time = now.UTC()
	}
	if ok, reason := e.guard(c.wf, ev, now); !ok {
		return 0, fmt.Errorf("workflow %s: %s", id, reason)
	}
	runID, err := e.enqueue(ctx, id, ev, true)
	if err == nil {
		e.poke()
	}
	return runID, err
}

// Retry re-queues a failed run; completed steps are not repeated.
func (e *Engine) Retry(ctx context.Context, runID int64) error {
	r, err := e.cfg.Store.GetWorkflowRun(ctx, runID)
	if err != nil {
		return err
	}
	if r.Status != store.RunFailed || r.OpID == 0 {
		return fmt.Errorf("workflow: run %d is not a failed queued run", runID)
	}
	if err := e.cfg.Store.RetryOp(ctx, r.OpID, e.cfg.Now()); err != nil {
		return err
	}
	r.Status, r.Error, r.FinishedAt = store.RunQueued, "", time.Time{}
	if err := e.cfg.Store.UpdateWorkflowRun(ctx, r); err != nil {
		return err
	}
	e.poke()
	return nil
}

// ==== Execution ===============================================================

// StepResult is one entry of a run's step log (workflow_runs.steps_json).
type StepResult struct {
	Index  int       `json:"index"`
	Type   string    `json:"type"`
	Status string    `json:"status"` // done, skipped, failed
	Output Output    `json:"output,omitempty"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// Step statuses.
const (
	StepDone    = "done"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

// ProcessOne claims and executes the next due workflow run from the
// operations queue. It reports whether a run was processed.
func (e *Engine) ProcessOne(ctx context.Context) (bool, error) {
	op, ok, err := e.cfg.Store.ClaimNextOpOfKind(ctx, e.cfg.Now(), OpKind)
	if err != nil || !ok {
		return false, err
	}
	var p opPayload
	if err := json.Unmarshal([]byte(op.Payload), &p); err != nil {
		_, ferr := e.cfg.Store.MarkOpFailed(ctx, op.ID, fmt.Errorf("bad payload: %w", err), e.cfg.Now(), e.cfg.MaxAttempts, true)
		return true, errors.Join(err, ferr)
	}
	run, err := e.cfg.Store.GetWorkflowRun(ctx, p.Run)
	if err != nil {
		_, ferr := e.cfg.Store.MarkOpFailed(ctx, op.ID, err, e.cfg.Now(), e.cfg.MaxAttempts, errors.Is(err, store.ErrNotFound))
		return true, errors.Join(err, ferr)
	}
	run.Attempts = op.Attempts
	if run.StartedAt.IsZero() {
		run.StartedAt = e.cfg.Now()
	}

	e.mu.RLock()
	c := e.wfs[p.Workflow]
	e.mu.RUnlock()
	if c == nil || (!c.wf.Enabled && !p.Manual) || !e.locationOK(c.wf) {
		run.Status, run.Error, run.FinishedAt = store.RunSkipped, "workflow deleted, disabled or bound to another location", e.cfg.Now()
		return true, errors.Join(e.cfg.Store.UpdateWorkflowRun(ctx, run), e.cfg.Store.MarkOpDone(ctx, op.ID))
	}

	var steps []StepResult
	if run.StepsJSON != "" {
		_ = json.Unmarshal([]byte(run.StepsJSON), &steps)
	}
	run.Status, run.Error = store.RunRunning, ""
	if err := e.cfg.Store.UpdateWorkflowRun(ctx, run); err != nil {
		return true, err
	}

	rc := e.newRunContext(c.wf, p.Event, false)
	steps, execErr := e.execute(ctx, c, rc, steps, func(s []StepResult) {
		run.StepsJSON = mustJSON(s)
		if err := e.cfg.Store.UpdateWorkflowRun(ctx, run); err != nil {
			e.log.Warn("workflow: persist step", "run", run.ID, "err", err)
		}
	})
	run.StepsJSON = mustJSON(steps)
	if execErr == nil {
		run.Status, run.FinishedAt = store.RunSucceeded, e.cfg.Now()
		return true, errors.Join(e.cfg.Store.UpdateWorkflowRun(ctx, run), e.cfg.Store.MarkOpDone(ctx, op.ID))
	}
	state, ferr := e.cfg.Store.MarkOpFailed(ctx, op.ID, execErr, e.cfg.Now(), e.cfg.MaxAttempts, IsPermanent(execErr))
	run.Error = execErr.Error()
	if state == store.OpFailed {
		run.Status, run.FinishedAt = store.RunFailed, e.cfg.Now()
	} else {
		run.Status = store.RunQueued // will be retried
	}
	e.log.Warn("workflow run failed", "workflow", c.wf.ID, "run", run.ID, "attempt", op.Attempts, "err", execErr)
	return true, errors.Join(ferr, e.cfg.Store.UpdateWorkflowRun(ctx, run))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (e *Engine) newRunContext(w *Workflow, ev events.Event, dry bool) *RunContext {
	now := e.now()
	vars := map[string]string{}
	rc := &RunContext{Workflow: w, Event: ev, Env: attachRuntime(context.Background(), NewEnv(ev, now, vars), &e.cfg.Deps, ev),
		Vars: vars, Deps: &e.cfg.Deps, Now: now, DryRun: dry}
	rc.runSub = e.runSub
	return rc
}

// execute runs the actions of c, skipping steps already completed in prev
// (resume after a retry). persist is called after every step.
func (e *Engine) execute(ctx context.Context, c *compiled, rc *RunContext, prev []StepResult, persist func([]StepResult)) ([]StepResult, error) {
	done := map[int]StepResult{}
	for _, s := range prev {
		if s.Status == StepDone || s.Status == StepSkipped {
			done[s.Index] = s
		}
	}
	var steps []StepResult
	for i, act := range c.actions {
		spec := c.wf.Actions[i]
		if s, ok := done[i]; ok {
			applyVars(rc, s.Output)
			steps = append(steps, s)
			continue
		}
		res := StepResult{Index: i, Type: spec.Type, At: e.cfg.Now()}
		rc.Env.Vars = rc.Vars
		run := true
		if p := c.ifs[i]; p != nil {
			ok, err := p.Eval(rc.Env)
			if err != nil {
				res.Status, res.Error = StepFailed, err.Error()
				steps = append(steps, res)
				persist(steps)
				return steps, Permanent(fmt.Errorf("action %d (%s): if: %w", i, spec.Type, err))
			}
			run = ok
		}
		if !run {
			res.Status = StepSkipped
			steps = append(steps, res)
			persist(steps)
			continue
		}
		out, err := safeExecute(ctx, act, rc)
		res.Output = out
		if err != nil {
			res.Status, res.Error = StepFailed, err.Error()
			steps = append(steps, res)
			persist(steps)
			if spec.ContinueOnError {
				continue
			}
			if IsPermanent(err) {
				return steps, Permanent(fmt.Errorf("action %d (%s): %w", i, spec.Type, err))
			}
			return steps, fmt.Errorf("action %d (%s): %w", i, spec.Type, err)
		}
		res.Status = StepDone
		applyVars(rc, out)
		steps = append(steps, res)
		persist(steps)
	}
	return steps, nil
}

func safeExecute(ctx context.Context, a Action, rc *RunContext) (out Output, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Permanent(fmt.Errorf("panic: %v", p))
		}
	}()
	return a.Execute(ctx, rc)
}

func applyVars(rc *RunContext, out Output) {
	for k, v := range out {
		if name, ok := strings.CutPrefix(k, "var."); ok {
			rc.Vars[name] = v
		}
	}
	rc.Env.Vars = rc.Vars
}

// runSub executes another workflow inline (run_workflow action).
func (e *Engine) runSub(ctx context.Context, parent *RunContext, id string) (Output, error) {
	e.mu.RLock()
	c := e.wfs[id]
	e.mu.RUnlock()
	if c == nil {
		return nil, Permanent(fmt.Errorf("workflow %q not found", id))
	}
	// A switched-off rule never acts, also not through another rule: switching
	// on (with the confirmation of external effects) is the one way to arm it.
	if !c.wf.Enabled {
		return nil, Permanent(fmt.Errorf("workflow %q is switched off", id))
	}
	if parent.depth+1 > parent.Workflow.Options.maxDepth() {
		return nil, Permanent(errors.New(reasonDepth))
	}
	for _, x := range parent.Event.Chain {
		if x == id {
			return nil, Permanent(errors.New(reasonChain))
		}
	}
	ok, err := c.cond.Eval(parent.Env)
	if err != nil {
		return nil, Permanent(err)
	}
	if !ok {
		return Output{"result": "condition false"}, nil
	}
	ev := parent.Event
	ev.Chain = append(append([]string{}, ev.Chain...), parent.Workflow.ID)
	child := &RunContext{Workflow: c.wf, Event: ev, Env: parent.Env, Vars: parent.Vars, Deps: parent.Deps,
		Now: parent.Now, DryRun: parent.DryRun, runSub: parent.runSub, depth: parent.depth + 1}
	steps, err := e.execute(ctx, c, child, nil, func([]StepResult) {})
	parent.Findings = append(parent.Findings, child.Findings...)
	out := Output{"steps": fmt.Sprint(len(steps))}
	for k, v := range parent.Vars {
		out["var."+k] = v
	}
	return out, err
}

// ==== mail.before_send =========================================================

// BeforeSend runs the enabled mail.before_send workflows synchronously and
// returns their warn/block findings. It is registered as a bus hook by Run;
// call it directly when no bus is used.
func (e *Engine) BeforeSend(ctx context.Context, ev events.Event) (events.HookResult, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.HookTimeout)
	defer cancel()
	var res events.HookResult
	var errs []error
	for _, c := range e.snapshot() {
		w := c.wf
		if !w.Enabled || !e.locationOK(w) || !w.Trigger.Matches(ev) {
			continue
		}
		rc := e.newRunContext(w, ev, false)
		ok, err := c.cond.Eval(rc.Env)
		if err != nil {
			errs = append(errs, fmt.Errorf("workflow %s: %w", w.ID, err))
			continue
		}
		if !ok {
			continue
		}
		start := e.cfg.Now()
		steps, err := e.execute(ctx, c, rc, nil, func([]StepResult) {})
		run := store.WorkflowRun{WorkflowID: w.ID, EventID: ev.ID, EventType: string(ev.Type), EventRef: ev.Ref,
			Status: store.RunSucceeded, StepsJSON: mustJSON(steps), CreatedAt: start, StartedAt: start, FinishedAt: e.cfg.Now(), Attempts: 1}
		if err != nil {
			run.Status, run.Error = store.RunFailed, err.Error()
			errs = append(errs, fmt.Errorf("workflow %s: %w", w.ID, err))
		}
		if _, lerr := e.cfg.Store.InsertWorkflowRun(ctx, run); lerr != nil {
			e.log.Warn("workflow: log before_send run", "workflow", w.ID, "err", lerr)
		}
		for _, f := range rc.Findings {
			res.Findings = append(res.Findings, f)
			if f.Severity == events.SeverityBlock {
				res.Vetoed = true
			}
		}
	}
	return res, errors.Join(errs...)
}

// ==== Dry run =================================================================

// DryRunResult describes what a workflow would do for one event.
type DryRunResult struct {
	EventID        string `json:"eventId"`
	EventRef       string `json:"eventRef,omitempty"`
	TriggerMatched bool   `json:"triggerMatched"`
	ConditionMet   bool   `json:"conditionMet"`
	Planned        []Plan `json:"planned,omitempty"`
	Error          string `json:"error,omitempty"`
}

// DryRun evaluates w (which need not be saved or enabled) against evs and
// returns the planned actions without any side effects and without touching
// loop protection or the run log. Use it for the "Testlauf" and for applying
// a workflow retroactively to existing mails (KONZEPT 8.5).
func (e *Engine) DryRun(ctx context.Context, w *Workflow, evs []events.Event) ([]DryRunResult, error) {
	c, err := e.compile(w)
	if err != nil {
		return nil, err
	}
	out := make([]DryRunResult, 0, len(evs))
	for _, ev := range evs {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		r := DryRunResult{EventID: ev.ID, EventRef: ev.Ref, TriggerMatched: w.Trigger.Matches(ev)}
		rc := e.newRunContext(w, ev, true)
		rc.runSub = nil
		ok, err := c.cond.Eval(rc.Env)
		if err != nil {
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		r.ConditionMet = ok
		if r.TriggerMatched && ok {
			for i, act := range c.actions {
				if p := c.ifs[i]; p != nil {
					rc.Env.Vars = rc.Vars
					run, err := p.Eval(rc.Env)
					if err != nil {
						r.Error = err.Error()
						break
					}
					if !run {
						r.Planned = append(r.Planned, Plan{Type: w.Actions[i].Type, Summary: "skipped (if is false)", Skipped: true})
						continue
					}
				}
				p, err := act.Plan(rc)
				if at, ok := e.reg.Lookup(w.Actions[i].Type); ok && at.ExternalEffect {
					p.ExternalEffect = true
				}
				if p.Type == "" {
					p.Type = w.Actions[i].Type
				}
				if err != nil {
					r.Error = fmt.Sprintf("action %d (%s): %v", i, w.Actions[i].Type, err)
				}
				r.Planned = append(r.Planned, p)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// ==== Event loop ===============================================================

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run subscribes to the bus, registers the mail.before_send hook and
// processes queued runs until ctx is cancelled. Runs left in the queue by a
// previous process are picked up (call store.ResetRunningOps at startup).
func (e *Engine) Run(ctx context.Context) error {
	if e.cfg.Bus == nil {
		return errors.New("workflow: Run needs an event bus")
	}
	var types []events.Type
	for _, t := range events.AllTypes {
		if !t.IsHook() {
			types = append(types, t)
		}
	}
	sub := e.cfg.Bus.Subscribe(ctx, events.SubscribeOptions{Name: "workflow", Types: types, Policy: events.Block,
		BlockTimeout: 5 * time.Second, Buffer: 1024})
	unhook := e.cfg.Bus.RegisterHook(events.MailBeforeSend, "workflow", e.BeforeSend)
	defer unhook()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range sub.C {
			if _, err := e.HandleEvent(ctx, ev); err != nil && ctx.Err() == nil {
				e.log.Warn("workflow: handle event", "event", ev.Type, "err", err)
			}
		}
	}()
	defer wg.Wait()

	for {
		for {
			ok, err := e.ProcessOne(ctx)
			if err != nil && ctx.Err() == nil {
				e.log.Warn("workflow: process run", "err", err)
			}
			if !ok || ctx.Err() != nil {
				break
			}
		}
		t := time.NewTimer(e.cfg.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-e.wake:
		case <-t.C:
		}
		t.Stop()
	}
}
