// SPDX-License-Identifier: AGPL-3.0-or-later

// Package host runs messenger plugins as separate processes (KONZEPT 4.4):
// discovery in plugins/ next to the executable, start on demand, health
// checks, restart with exponential backoff and crash isolation. A crashing
// plugin only fails its own calls; the host restarts it and tells the
// owner (OnStarted) to reconnect the accounts.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Plugin states.
const (
	StateMissing  = "missing"  // no executable found
	StateStopped  = "stopped"  // not running (not needed or stopped)
	StateStarting = "starting" // process started, handshake running
	StateRunning  = "running"
	StateCrashed  = "crashed" // exited unexpectedly; restart pending (backoff)
	StateFailed   = "failed"  // crashed too often; needs a manual restart
)

// ErrUnavailable is returned for calls to a plugin that is missing, failed
// or crashed during the call.
var ErrUnavailable = errors.New("messenger plugin unavailable")

// Status describes one plugin.
type Status struct {
	Name       string           `json:"name"`
	Path       string           `json:"path,omitempty"`
	State      string           `json:"state"`
	PID        int              `json:"pid,omitempty"`
	Info       wire.HelloResult `json:"info"`
	Restarts   int              `json:"restarts"`
	LastError  string           `json:"lastError,omitempty"`
	StartedAt  time.Time        `json:"startedAt,omitzero"`
	LastCrash  time.Time        `json:"lastCrash,omitzero"`
	NextRetry  time.Time        `json:"nextRetry,omitzero"`
	UpdateHint string           `json:"updateHint,omitempty"`
}

// Options configure a Host.
type Options struct {
	// Dirs are searched for plugin executables (see Discover, DefaultDirs).
	Dirs []string
	// Static adds plugins that are not discovered (in-process plugins).
	Static []Manifest
	// DataDir is the root of the plugin data (data_dir/plugins); every
	// plugin gets DataDir/<name>.
	DataDir     string
	HostVersion string
	Launcher    Launcher
	// Config returns the settings handed to a plugin in hello.
	Config func(name string) map[string]string
	// OnEvent receives the plugin events (called from one goroutine per
	// plugin, in order).
	OnEvent func(plugin string, ev wire.Event)
	// OnStatus is called after every state change.
	OnStatus func(Status)
	// OnStarted is called (in its own goroutine) after a plugin was
	// (re)started, e.g. to connect its accounts again.
	OnStarted func(ctx context.Context, plugin string)
	Logger    *slog.Logger

	StartTimeout   time.Duration // handshake (default 20s)
	HealthInterval time.Duration // default 30s
	HealthTimeout  time.Duration // default 10s
	BackoffMin     time.Duration // default 1s
	BackoffMax     time.Duration // default 2min
	// MaxCrashes crashes within CrashWindow put a plugin into StateFailed
	// (defaults 5 and 10min).
	MaxCrashes  int
	CrashWindow time.Duration
}

// Host supervises the plugins of one user.
type Host struct {
	opts   Options
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	procs map[string]*supervisor
	found map[string]Manifest
}

// New creates a host. No plugin is started until it is needed.
func New(opts Options) *Host {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Launcher == nil {
		opts.Launcher = ExecLauncher{Logger: opts.Logger}
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.StartTimeout, 20*time.Second)
	def(&opts.HealthInterval, 30*time.Second)
	def(&opts.HealthTimeout, 10*time.Second)
	def(&opts.BackoffMin, time.Second)
	def(&opts.BackoffMax, 2*time.Minute)
	def(&opts.CrashWindow, 10*time.Minute)
	if opts.MaxCrashes <= 0 {
		opts.MaxCrashes = 5
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{opts: opts, log: opts.Logger, ctx: ctx, cancel: cancel, procs: map[string]*supervisor{}}
	h.Rescan()
	return h
}

// Rescan looks for plugin executables again (e.g. after an update).
func (h *Host) Rescan() {
	found := map[string]Manifest{}
	for _, m := range Discover(h.opts.Dirs...) {
		found[m.Name] = m
	}
	for _, m := range h.opts.Static {
		found[m.Name] = m
	}
	h.mu.Lock()
	h.found = found
	for name, s := range h.procs {
		if m, ok := found[name]; ok {
			s.setManifest(m)
		}
	}
	h.mu.Unlock()
}

// Manifest returns the discovered plugin of that name.
func (h *Host) Manifest(name string) (Manifest, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.found[name]
	return m, ok
}

// Statuses returns the state of every discovered or started plugin.
func (h *Host) Statuses() []Status {
	h.mu.Lock()
	names := map[string]bool{}
	for n := range h.found {
		names[n] = true
	}
	for n := range h.procs {
		names[n] = true
	}
	h.mu.Unlock()
	out := make([]Status, 0, len(names))
	for n := range names {
		out = append(out, h.Status(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status returns the state of one plugin.
func (h *Host) Status(name string) Status {
	h.mu.Lock()
	s := h.procs[name]
	m, found := h.found[name]
	h.mu.Unlock()
	if s != nil {
		return s.status()
	}
	if !found {
		return Status{Name: name, State: StateMissing}
	}
	return Status{Name: name, Path: m.Path, State: StateStopped}
}

func (h *Host) supervisor(name string) (*supervisor, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if s := h.procs[name]; s != nil {
		return s, nil
	}
	m, ok := h.found[name]
	if !ok {
		return nil, fmt.Errorf("%w: plugin %q not installed", ErrUnavailable, name)
	}
	s := newSupervisor(h, m)
	h.procs[name] = s
	h.wg.Add(1)
	go func() { defer h.wg.Done(); s.run() }()
	return s, nil
}

// Ensure starts the plugin if needed and waits until it is running.
func (h *Host) Ensure(ctx context.Context, name string) error {
	s, err := h.supervisor(name)
	if err != nil {
		return err
	}
	_, err = s.ready(ctx)
	return err
}

// Running reports whether the plugin is running (without starting it).
func (h *Host) Running(name string) bool {
	h.mu.Lock()
	s := h.procs[name]
	h.mu.Unlock()
	return s != nil && s.status().State == StateRunning
}

// Call invokes method on the plugin (starting it on demand).
func (h *Host) Call(ctx context.Context, name, method string, params, out any) error {
	s, err := h.supervisor(name)
	if err != nil {
		return err
	}
	conn, err := s.ready(ctx)
	if err != nil {
		return err
	}
	err = conn.Call(ctx, method, params, out)
	if errors.Is(err, wire.ErrClosed) {
		return fmt.Errorf("%w: %s exited during %s", ErrUnavailable, name, method)
	}
	return err
}

// Restart stops the plugin (if running) and starts it again; it also
// clears StateFailed.
func (h *Host) Restart(name string) error {
	s, err := h.supervisor(name)
	if err != nil {
		return err
	}
	s.restart()
	return nil
}

// Stop stops a plugin until it is needed again.
func (h *Host) Stop(name string) {
	h.mu.Lock()
	s := h.procs[name]
	h.mu.Unlock()
	if s != nil {
		s.stop()
	}
}

// Close stops all plugins and waits for them.
func (h *Host) Close() {
	h.mu.Lock()
	procs := make([]*supervisor, 0, len(h.procs))
	for _, s := range h.procs {
		procs = append(procs, s)
	}
	h.mu.Unlock()
	for _, s := range procs {
		s.stop()
	}
	h.cancel()
	h.wg.Wait()
}

// PluginDataDir returns the data directory of a plugin.
func (h *Host) PluginDataDir(name string) string { return filepath.Join(h.opts.DataDir, name) }

// ==== supervisor ===============================================================

type supervisor struct {
	h   *Host
	log *slog.Logger

	mu      sync.Mutex
	m       Manifest
	st      Status
	conn    *wire.Conn
	proc    Proc
	readyCh chan struct{} // closed when running or failed
	want    bool
	wake    chan struct{}
	stopReq chan struct{} // closed to stop the current process
	crashes []time.Time
}

func newSupervisor(h *Host, m Manifest) *supervisor {
	return &supervisor{h: h, log: h.log.With("plugin", m.Name), m: m, st: Status{Name: m.Name, Path: m.Path, State: StateStopped},
		readyCh: make(chan struct{}), wake: make(chan struct{}, 1), stopReq: make(chan struct{})}
}

func (s *supervisor) setManifest(m Manifest) {
	s.mu.Lock()
	s.m, s.st.Path = m, m.Path
	s.mu.Unlock()
}

func (s *supervisor) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *supervisor) setState(fn func(st *Status)) {
	s.mu.Lock()
	fn(&s.st)
	st := s.st
	s.mu.Unlock()
	if s.h.opts.OnStatus != nil {
		s.h.opts.OnStatus(st)
	}
}

func (s *supervisor) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ready requests the plugin and waits until it runs.
func (s *supervisor) ready(ctx context.Context) (*wire.Conn, error) {
	for {
		s.mu.Lock()
		if s.st.State == StateRunning && s.conn != nil {
			c := s.conn
			s.mu.Unlock()
			return c, nil
		}
		if s.st.State == StateFailed {
			err := s.st.LastError
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: %s failed: %s", ErrUnavailable, s.m.Name, err)
		}
		s.want = true
		ch := s.readyCh
		s.mu.Unlock()
		s.poke()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.h.ctx.Done():
			return nil, ErrUnavailable
		}
	}
}

func (s *supervisor) restart() {
	s.mu.Lock()
	s.crashes, s.want = nil, true
	if s.st.State == StateFailed {
		s.st.State = StateStopped
	}
	stop := s.stopReq
	running := s.proc != nil
	s.mu.Unlock()
	if running {
		s.signalStop(stop, true)
	}
	s.poke()
}

func (s *supervisor) stop() {
	s.mu.Lock()
	s.want = false
	stop := s.stopReq
	s.mu.Unlock()
	s.signalStop(stop, false)
	s.poke()
}

// signalStop asks the current process to stop. With again the supervisor
// starts it again right away.
func (s *supervisor) signalStop(stop chan struct{}, again bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReq != stop {
		return
	}
	if again {
		s.want = true
	}
	close(s.stopReq)
	s.stopReq = make(chan struct{})
}

func (s *supervisor) run() {
	backoff := time.Duration(0)
	for {
		// Wait until the plugin is wanted.
		for {
			s.mu.Lock()
			want := s.want && s.st.State != StateFailed
			s.mu.Unlock()
			if want {
				break
			}
			select {
			case <-s.wake:
			case <-s.h.ctx.Done():
				s.releaseWaiters()
				return
			}
		}
		if backoff > 0 {
			next := time.Now().Add(backoff)
			s.setState(func(st *Status) { st.NextRetry = next })
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-s.h.ctx.Done():
				t.Stop()
				s.releaseWaiters()
				return
			case <-s.currentStop():
				// restart(): skip the remaining backoff
				t.Stop()
			}
		}
		s.mu.Lock()
		stop := s.stopReq
		m := s.m
		s.mu.Unlock()
		started := time.Now()
		crashed, err := s.runOnce(m, stop)
		if s.h.ctx.Err() != nil {
			s.releaseWaiters()
			return
		}
		if !crashed {
			backoff = 0
			s.setState(func(st *Status) {
				st.State, st.PID, st.NextRetry = StateStopped, 0, time.Time{}
			})
			continue
		}
		now := time.Now()
		if now.Sub(started) > s.h.opts.CrashWindow/2 {
			backoff = 0 // it ran stable for a while: start over
		}
		s.mu.Lock()
		s.crashes = append(s.crashes, now)
		cut := len(s.crashes)
		for i, t := range s.crashes {
			if now.Sub(t) <= s.h.opts.CrashWindow {
				cut = i
				break
			}
		}
		s.crashes = s.crashes[cut:]
		failed := len(s.crashes) >= s.h.opts.MaxCrashes
		s.mu.Unlock()
		msg := "exited unexpectedly"
		if err != nil {
			msg = err.Error()
		}
		s.log.Warn("messenger plugin crashed", "err", msg, "failed", failed)
		if backoff == 0 {
			backoff = s.h.opts.BackoffMin
		} else {
			backoff = min(backoff*2, s.h.opts.BackoffMax)
		}
		s.setState(func(st *Status) {
			st.Restarts++
			st.LastError, st.LastCrash, st.PID = msg, now, 0
			st.State = StateCrashed
			if failed {
				st.State = StateFailed
			}
		})
		if failed {
			backoff = 0
			s.releaseWaiters()
		}
	}
}

func (s *supervisor) currentStop() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopReq
}

// releaseWaiters wakes callers blocked in ready (they re-check the state).
func (s *supervisor) releaseWaiters() {
	s.mu.Lock()
	close(s.readyCh)
	s.readyCh = make(chan struct{})
	s.mu.Unlock()
}

// runOnce starts the plugin and supervises it until it exits or is
// stopped. It reports whether the plugin crashed.
func (s *supervisor) runOnce(m Manifest, stop chan struct{}) (crashed bool, err error) {
	s.setState(func(st *Status) { st.State = StateStarting })
	proc, err := s.h.opts.Launcher.Launch(s.h.ctx, m)
	if err != nil {
		return true, fmt.Errorf("start: %w", err)
	}
	events := make(chan wire.Event, 256)
	conn := wire.NewConn(proc.Stdout(), proc.Stdin(), func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != wire.NotifyEvent {
			return nil, wire.Errorf(wire.CodeMethodNotFound, "host has no method %q", method)
		}
		var ev wire.Event
		if err := json.Unmarshal(params, &ev); err != nil {
			return nil, err
		}
		select {
		case events <- ev:
		case <-s.h.ctx.Done():
		}
		return nil, nil
	})
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for {
			select {
			case ev := <-events:
				if s.h.opts.OnEvent != nil {
					s.h.opts.OnEvent(m.Name, ev)
				}
			case <-conn.Done():
				// Drain what arrived before the end.
				for {
					select {
					case ev := <-events:
						if s.h.opts.OnEvent != nil {
							s.h.opts.OnEvent(m.Name, ev)
						}
					default:
						return
					}
				}
			}
		}
	}()
	cleanup := func() {
		_ = proc.Kill()
		conn.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
		<-evDone
		s.mu.Lock()
		s.conn, s.proc = nil, nil
		s.mu.Unlock()
	}

	// Handshake.
	var cfg map[string]string
	if s.h.opts.Config != nil {
		cfg = s.h.opts.Config(m.Name)
	}
	dataDir := s.h.PluginDataDir(m.Name)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		cleanup()
		return true, err
	}
	hctx, cancel := context.WithTimeout(s.h.ctx, s.h.opts.StartTimeout)
	var info wire.HelloResult
	herr := conn.Call(hctx, wire.MethodHello, wire.HelloParams{ProtocolVersion: wire.ProtocolVersion, HostVersion: s.h.opts.HostVersion,
		DataDir: dataDir, Config: cfg}, &info)
	cancel()
	if herr == nil && info.ProtocolVersion != wire.ProtocolVersion {
		herr = wire.Errorf(wire.CodeOutdated, "plugin speaks protocol %d, host %d", info.ProtocolVersion, wire.ProtocolVersion)
	}
	if herr != nil {
		cleanup()
		if wire.ErrorCode(herr) == wire.CodeOutdated {
			s.setState(func(st *Status) { st.UpdateHint = herr.Error() })
		}
		return true, fmt.Errorf("handshake: %w", herr)
	}
	if info.Name == "" {
		info.Name = m.Name
	}
	pid := proc.PID()
	s.mu.Lock()
	s.conn, s.proc = conn, proc
	s.st.State, s.st.Info, s.st.PID, s.st.StartedAt, s.st.NextRetry = StateRunning, info, pid, time.Now(), time.Time{}
	s.st.UpdateHint = info.UpdateHint
	st := s.st
	close(s.readyCh)
	s.readyCh = make(chan struct{})
	s.mu.Unlock()
	if s.h.opts.OnStatus != nil {
		s.h.opts.OnStatus(st)
	}
	s.log.Info("messenger plugin running", "version", info.Version, "pid", pid)
	if s.h.opts.OnStarted != nil {
		go s.h.opts.OnStarted(s.h.ctx, m.Name)
	}

	t := time.NewTicker(s.h.opts.HealthInterval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case err := <-exited:
			exited <- err
			cleanup()
			if err == nil {
				err = errors.New("exited")
			}
			return true, err
		case <-conn.Done():
			cleanup()
			return true, fmt.Errorf("connection lost: %v", conn.Err())
		case <-stop:
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = conn.Call(sctx, wire.MethodShutdown, nil, nil)
			cancel()
			select {
			case err := <-exited:
				exited <- err
			case <-time.After(5 * time.Second):
			}
			cleanup()
			return false, nil
		case <-s.h.ctx.Done():
			cleanup()
			return false, nil
		case <-t.C:
			pctx, cancel := context.WithTimeout(s.h.ctx, s.h.opts.HealthTimeout)
			var pr wire.PingResult
			err := conn.Call(pctx, wire.MethodPing, nil, &pr)
			cancel()
			if err == nil && pr.OK {
				misses = 0
				continue
			}
			misses++
			s.log.Warn("messenger plugin health check failed", "err", err, "misses", misses)
			if misses >= 2 {
				cleanup()
				return true, fmt.Errorf("health check failed: %v", err)
			}
		}
	}
}
