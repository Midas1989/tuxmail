// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sync is TuxMail's mail sync engine (KONZEPT 4.2, 5, 6). It wires
// the providers, the store, the threading engine, the project service and
// the event bus together:
//
//   - a Manager runs one Worker goroutine per enabled mail account;
//   - a Worker connects the account's provider, mirrors the folder list,
//     syncs folders incrementally (Inbox first, then Sent, then the rest),
//     watches the Inbox with IDLE, downloads bodies in the background and
//     replays the account's queued operations against the server;
//   - Actions are the local-first user operations (move, flag, delete,
//     append, folder management): they change the store immediately,
//     publish events and enqueue an operation for the worker;
//   - WorkflowMail implements workflow.MailActions on top of Actions.
//
// The UI reads only from the store and listens to the events.
package sync

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// State is the connection/sync state of an account.
type State string

const (
	StateStopped    State = "stopped"
	StateConnecting State = "connecting"
	StateSyncing    State = "syncing"
	StateIdle       State = "idle"
	StateError      State = "error"
)

// AccountStatus describes the state of an account's worker.
type AccountStatus struct {
	AccountID int64
	Name      string
	State     State
	// Folder is the remote id of the folder being synced (StateSyncing).
	Folder string
	// LastError and LastErrorAt describe the last failure (kept until the
	// next successful cycle).
	LastError   string
	LastErrorAt time.Time
	// NextRetry is set while waiting after an error.
	NextRetry time.Time
	// LastSync is the end of the last successful sync cycle.
	LastSync time.Time
}

// Manager runs the sync workers of all mail accounts. It is safe for
// concurrent use.
type Manager struct {
	cfg     Config
	log     *slog.Logger
	st      *store.Store
	repo    *store.ThreadRepo
	engine  *threading.Engine
	actions *Actions

	mu      stdsync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	workers map[int64]*Worker
	wg      stdsync.WaitGroup
}

// New creates a manager. Call Start to run it.
func New(cfg Config) (*Manager, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, log: cfg.Logger.With("component", "sync"), st: cfg.Store, repo: cfg.Store.ThreadRepository(),
		workers: map[int64]*Worker{}}
	m.engine = cfg.Threading
	if m.engine == nil {
		m.engine = threading.NewEngine(m.repo, cfg.ThreadingConfig)
	}
	m.actions = &Actions{m: m}
	return m, nil
}

func (m *Manager) now() time.Time { return m.cfg.Now() }

// Engine returns the threading engine.
func (m *Manager) Engine() *threading.Engine { return m.engine }

// Actions returns the local-first operations API.
func (m *Manager) Actions() *Actions { return m.actions }

// MailActions returns the workflow.MailActions implementation.
func (m *Manager) MailActions() *WorkflowMail { return &WorkflowMail{m: m} }

// Start resets operations left running by a previous process and starts a
// worker for every enabled account whose kind has a provider, the
// maintenance scheduler and (if configured) the workflow runner. It
// returns after the workers were started; they stop when ctx ends or Stop
// is called.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.ctx != nil {
		m.mu.Unlock()
		return errors.New("sync: manager already started")
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	runCtx := m.ctx
	m.mu.Unlock()

	if n, err := m.st.ResetRunningOps(ctx); err != nil {
		return err
	} else if n > 0 {
		m.log.Info("sync: reset operations left running", "count", n)
	}
	accs, err := m.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, a := range accs {
		if a.Enabled && m.cfg.Providers.Supports(a.Kind) {
			m.startWorker(a)
		}
	}
	m.goRun(func() { m.maintenanceLoop(runCtx) })
	if m.cfg.Bus != nil {
		sub := m.cfg.Bus.Subscribe(runCtx, events.SubscribeOptions{Name: "sync", Types: []events.Type{events.TagAdded,
			events.TagRemoved, events.TagChanged}, Buffer: 64})
		m.goRun(func() {
			for ev := range sub.C {
				if ev.Tag != nil && store.TagSource(ev.Tag.Source).Remote() {
					continue // read from a server, nothing to push
				}
				m.wakeAll()
			}
		})
	}
	if m.cfg.Workflow != nil {
		m.goRun(func() { m.workflowLoop(runCtx) })
	}
	return nil
}

func (m *Manager) goRun(fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn()
	}()
}

// Stop stops all workers and waits for them.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	m.workers = map[int64]*Worker{}
	m.mu.Unlock()
}

func (m *Manager) runCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// startWorker starts (or restarts) the worker of a. m.mu must not be held.
func (m *Manager) startWorker(a domain.Account) {
	m.mu.Lock()
	old := m.workers[a.ID]
	delete(m.workers, a.ID)
	m.mu.Unlock()
	if old != nil {
		old.stop()
		<-old.done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil {
		return
	}
	w := newWorker(m, a)
	m.workers[a.ID] = w
	ctx, cancel := context.WithCancel(m.ctx)
	w.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(w.done)
		w.run(ctx)
	}()
}

// AddAccount starts the worker of an account (or restarts it after its
// configuration changed). Disabled accounts and kinds without provider are
// stopped instead.
func (m *Manager) AddAccount(ctx context.Context, accountID int64) error {
	if m.runCtx() == nil {
		return ErrNotRunning
	}
	a, err := m.st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if !a.Enabled || !m.cfg.Providers.Supports(a.Kind) {
		m.RemoveAccount(accountID)
		return nil
	}
	m.startWorker(a)
	return nil
}

// ReloadAccount is AddAccount under another name (configuration changed).
func (m *Manager) ReloadAccount(ctx context.Context, accountID int64) error {
	return m.AddAccount(ctx, accountID)
}

// RemoveAccount stops the worker of an account and waits for it (call it
// before deleting the account).
func (m *Manager) RemoveAccount(accountID int64) {
	m.mu.Lock()
	w := m.workers[accountID]
	delete(m.workers, accountID)
	m.mu.Unlock()
	if w != nil {
		w.stop()
		<-w.done
		m.publishStatus(AccountStatus{AccountID: accountID, Name: w.acc.DisplayName, State: StateStopped})
	}
}

// SetAccountEnabled stores the enabled flag and starts or stops the worker.
func (m *Manager) SetAccountEnabled(ctx context.Context, accountID int64, enabled bool) error {
	a, err := m.st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	a.Enabled = enabled
	if err := m.st.UpdateAccount(ctx, a); err != nil {
		return err
	}
	if !enabled {
		m.RemoveAccount(accountID)
		return nil
	}
	return m.AddAccount(ctx, accountID)
}

func (m *Manager) worker(accountID int64) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workers[accountID]
}

func (m *Manager) wakeAll() {
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	for _, w := range ws {
		w.poke()
	}
}

// Status returns the status of an account's worker.
func (m *Manager) Status(accountID int64) (AccountStatus, bool) {
	w := m.worker(accountID)
	if w == nil {
		return AccountStatus{AccountID: accountID, State: StateStopped}, false
	}
	return w.Status(), true
}

// Statuses returns the status of all running workers ordered by account id.
func (m *Manager) Statuses() []AccountStatus {
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	out := make([]AccountStatus, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// SyncNow asks the worker of an account to sync the given folders (all
// folders and the folder list if none are given) as soon as possible. A
// worker waiting after an error retries immediately.
func (m *Manager) SyncNow(accountID int64, folderIDs ...int64) {
	if w := m.worker(accountID); w != nil {
		w.requestSync(folderIDs...)
	}
}

// FetchBody returns the raw message, downloading it through the account's
// worker if it is not stored yet (on-demand loading).
func (m *Manager) FetchBody(ctx context.Context, messageID int64) ([]byte, error) {
	msg, err := m.st.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if msg.BlobHash != "" {
		if raw, err := readBlob(m.cfg.Blobs, msg.BlobHash); err == nil {
			return raw, nil
		}
	}
	w := m.worker(msg.AccountID)
	if w == nil {
		return nil, ErrNotRunning
	}
	return w.fetchBody(ctx, msg)
}

func (m *Manager) workflowLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for {
			ok, err := m.cfg.Workflow.ProcessOne(ctx)
			if err != nil && ctx.Err() == nil {
				m.log.Warn("sync: workflow run", "err", err)
			}
			if !ok || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
