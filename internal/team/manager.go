// SPDX-License-Identifier: AGPL-3.0-or-later

package team

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Midas1989/Mailclient/internal/store"
)

// StoreFile is the name of the team store inside data_dir/teams/<id>/.
const StoreFile = "tuxmail.db"

// Runtime is the running backend of one team mailbox: the server builds an
// internal/app.App on the team store with sync and send enabled, so the
// mailbox is synced exactly once for the whole team.
type Runtime interface {
	Store() *store.Store
	// Close stops sync and background work (the store is closed by the Manager).
	Close() error
}

// Factory creates and starts the runtime of a mailbox on its opened store.
// dir is data_dir/teams/<id> (blobs, uploads, secrets vault).
type Factory func(ctx context.Context, mb Mailbox, st *store.Store, dir string) (Runtime, error)

// Manager owns the team stores and their runtimes (lifecycle: Start opens
// all enabled mailboxes, Open opens one on demand, Close/Remove stop one,
// Stop stops all). It is safe for concurrent use.
type Manager struct {
	svc     *Service
	dataDir string
	factory Factory
	log     *slog.Logger

	mu      sync.Mutex
	running map[int64]*runtimeEntry
	stopped bool
}

type runtimeEntry struct {
	st *store.Store
	rt Runtime
}

// NewManager creates a manager for data_dir/teams.
func NewManager(svc *Service, dataDir string, factory Factory, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{svc: svc, dataDir: dataDir, factory: factory, log: log, running: map[int64]*runtimeEntry{}}
}

// Dir returns data_dir/teams/<id>.
func (m *Manager) Dir(id int64) string { return filepath.Join(m.dataDir, "teams", fmt.Sprint(id)) }

// Start opens every enabled mailbox. Failures are logged and do not stop
// the others.
func (m *Manager) Start(ctx context.Context) error {
	mbs, err := m.svc.ListMailboxes(ctx)
	if err != nil {
		return err
	}
	for _, mb := range mbs {
		if mb.Disabled {
			continue
		}
		if _, err := m.Open(ctx, mb.ID); err != nil {
			m.log.Warn("team: cannot start mailbox", "team", mb.ID, "err", err)
		}
	}
	return nil
}

// ErrStopped is returned after Stop.
var ErrStopped = errors.New("team: manager stopped")

// Open returns the runtime of a mailbox, opening the team store and
// starting the runtime on first use.
func (m *Manager) Open(ctx context.Context, id int64) (Runtime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, ErrStopped
	}
	if e, ok := m.running[id]; ok {
		return e.rt, nil
	}
	mb, err := m.svc.GetMailbox(ctx, id)
	if err != nil {
		return nil, err
	}
	dir := m.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, filepath.Join(dir, StoreFile))
	if err != nil {
		return nil, err
	}
	rt, err := m.factory(ctx, mb, st, dir)
	if err != nil {
		st.Close()
		return nil, err
	}
	m.running[id] = &runtimeEntry{st: st, rt: rt}
	m.log.Info("team mailbox started", "team", id, "name", mb.Name)
	return rt, nil
}

// Get returns a running runtime without opening it.
func (m *Manager) Get(id int64) (Runtime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.running[id]
	if !ok {
		return nil, false
	}
	return e.rt, true
}

// Running returns the ids of the running mailboxes.
func (m *Manager) Running() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, 0, len(m.running))
	for id := range m.running {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Close stops the runtime of a mailbox (e.g. when it was disabled).
func (m *Manager) Close(id int64) error {
	m.mu.Lock()
	e := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if e == nil {
		return nil
	}
	return e.close()
}

// Remove stops a mailbox and deletes its data directory.
func (m *Manager) Remove(id int64) error {
	if err := m.Close(id); err != nil {
		m.log.Warn("team: closing mailbox failed", "team", id, "err", err)
	}
	return os.RemoveAll(m.Dir(id))
}

// Stop stops all runtimes; later Open calls fail with ErrStopped.
func (m *Manager) Stop() {
	m.mu.Lock()
	all := m.running
	m.running = map[int64]*runtimeEntry{}
	m.stopped = true
	m.mu.Unlock()
	for id, e := range all {
		if err := e.close(); err != nil {
			m.log.Warn("team: stopping mailbox failed", "team", id, "err", err)
		}
	}
}

func (e *runtimeEntry) close() error {
	err := e.rt.Close()
	return errors.Join(err, e.st.Close())
}
