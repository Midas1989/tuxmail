// SPDX-License-Identifier: AGPL-3.0-or-later

// Package messenger integrates messenger plugins (KONZEPT 11) into TuxMail:
// it owns the plugin host of one user, the opt-in state per plugin
// (KONZEPT 18.1), pairing of new accounts, and maps plugin events into the
// store (chats, chat_messages, persons/person_handles, blobs) and onto the
// event bus (chat.received). The application layer (internal/app) calls it
// to send, react, mark read and download media.
//
// Process model: every plugin is a separate executable (cmd/plugins/<name>)
// supervised by internal/messenger/host; sessions live in
// data_dir/plugins/<name>/<account>/.
package messenger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/messenger/host"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Errors of the service (mapped to API error codes by internal/app).
var (
	ErrDisabled     = errors.New("messenger plugin is not enabled")
	ErrRiskNotAcked = errors.New("the risk notice of this plugin must be acknowledged")
	ErrNotMessenger = errors.New("not a messenger account")
	ErrNoChat       = errors.New("no messenger chat for this conversation")
	ErrUnknownPair  = errors.New("unknown pairing")
)

// TypingInfo is an ephemeral typing notification.
type TypingInfo struct {
	ConversationID int64  `json:"conversationId"`
	ChatID         int64  `json:"chatId"`
	AccountID      int64  `json:"accountId"`
	PersonID       int64  `json:"personId,omitempty"`
	Name           string `json:"name"`
	Active         bool   `json:"active"`
}

// Notifier receives UI-relevant changes (implemented by internal/app).
type Notifier interface {
	ConversationChanged(conversationID int64, itemID string)
	ViewsChanged()
	PluginChanged(name string)
	AccountChanged(accountID int64)
	PairingChanged(p Pairing)
	Typing(t TypingInfo)
}

// NopNotifier ignores all notifications.
type NopNotifier struct{}

func (NopNotifier) ConversationChanged(int64, string) {}
func (NopNotifier) ViewsChanged()                     {}
func (NopNotifier) PluginChanged(string)              {}
func (NopNotifier) AccountChanged(int64)              {}
func (NopNotifier) PairingChanged(Pairing)            {}
func (NopNotifier) Typing(TypingInfo)                 {}

// Options configure a Service.
type Options struct {
	Store *store.Store
	Blobs *blob.Store
	Bus   *events.Bus
	// DataDir is the user's data directory; plugin sessions go to
	// DataDir/plugins.
	DataDir string
	// Dirs are the plugin directories (default host.DefaultDirs()).
	Dirs []string
	// Static adds in-process plugins (tests, demo).
	Static      []host.Manifest
	Launcher    host.Launcher
	HostVersion string
	// Env supplies settings from the environment per plugin (e.g. Telegram
	// api_id from TUXMAIL_TELEGRAM_API_ID); stored settings win.
	Env      func(plugin string) map[string]string
	Notifier Notifier
	Logger   *slog.Logger
	Now      func() time.Time
	// HistoryChats and HistoryMessages bound the initial sync of an account.
	HistoryChats    int
	HistoryMessages int
	// HostOptions may adjust the plugin host (timeouts, backoff).
	HostOptions func(*host.Options)
	// Components configures the on-demand install of external programs
	// (signal-cli); the zero value uses the pinned downloads.
	Components ComponentOptions
}

// Service is the messenger integration of one user.
type Service struct {
	opts   Options
	st     *store.Store
	blobs  *blob.Store
	bus    *events.Bus
	host   *host.Host
	notify Notifier
	log    *slog.Logger
	now    func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	pairings map[string]*Pairing
	states   map[int64]wire.AccountStatus
	syncing  map[int64]bool
	pending  map[pendingKey]pendingReceipt

	installs map[string]*install // plugin -> running or failed install
}

// New creates the service; plugins start when they are needed (Start
// connects the accounts of enabled plugins).
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("messenger: store required")
	}
	if opts.Notifier == nil {
		opts.Notifier = NopNotifier{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Dirs == nil {
		opts.Dirs = host.DefaultDirs()
	}
	if opts.HistoryChats <= 0 {
		opts.HistoryChats = 50
	}
	if opts.HistoryMessages <= 0 {
		opts.HistoryMessages = 40
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{opts: opts, st: opts.Store, blobs: opts.Blobs, bus: opts.Bus, notify: opts.Notifier, log: opts.Logger.With("component", "messenger"),
		now: opts.Now, ctx: ctx, cancel: cancel, pairings: map[string]*Pairing{}, states: map[int64]wire.AccountStatus{}, syncing: map[int64]bool{}}
	dataDir := ""
	if opts.DataDir != "" {
		dataDir = filepath.Join(opts.DataDir, "plugins")
	} else {
		dataDir = filepath.Join(os.TempDir(), "tuxmail-plugins")
	}
	ho := host.Options{Dirs: opts.Dirs, Static: opts.Static, DataDir: dataDir, HostVersion: opts.HostVersion, Launcher: opts.Launcher,
		Config: s.pluginConfig, OnEvent: s.handleEvent, OnStarted: s.onStarted, Logger: opts.Logger,
		OnStatus: func(st host.Status) { s.notify.PluginChanged(st.Name) }}
	if opts.HostOptions != nil {
		opts.HostOptions(&ho)
	}
	s.host = host.New(ho)
	return s, nil
}

// Host returns the plugin host.
func (s *Service) Host() *host.Host { return s.host }

// Start connects the accounts of every enabled plugin (starting the plugin
// processes in the background).
func (s *Service) Start() {
	accs, err := s.st.ListMessengerAccounts(s.ctx, "")
	if err != nil {
		s.log.Warn("messenger: list accounts", "err", err)
		return
	}
	plugins := map[string]bool{}
	for _, a := range accs {
		plugins[a.Plugin] = true
	}
	for name := range plugins {
		p, err := s.st.GetMessengerPlugin(s.ctx, name)
		if err != nil || !p.Enabled {
			continue
		}
		s.goBackground(func(ctx context.Context) {
			if err := s.host.Ensure(ctx, name); err != nil {
				s.log.Warn("messenger: start plugin", "plugin", name, "err", err)
			}
		})
	}
}

// Close stops all plugins.
func (s *Service) Close() {
	s.cancel()
	s.host.Close()
	s.wg.Wait()
}

func (s *Service) goBackground(fn func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.ctx)
	}()
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pluginConfig merges environment and stored settings.
func (s *Service) pluginConfig(name string) map[string]string {
	cfg := map[string]string{}
	if s.opts.Env != nil {
		maps.Copy(cfg, s.opts.Env(name))
	}
	if p, err := s.st.GetMessengerPlugin(context.Background(), name); err == nil {
		for k, v := range p.Settings {
			if v != "" {
				cfg[k] = v
			}
		}
	}
	return cfg
}

// EnvConfig reads TUXMAIL_<PLUGIN>_<KEY> variables for the setting keys of
// the catalog (e.g. TUXMAIL_TELEGRAM_API_ID).
func EnvConfig(plugin string) map[string]string {
	out := map[string]string{}
	for _, k := range SpecFor(plugin).Settings {
		if v := os.Getenv("TUXMAIL_" + strings.ToUpper(plugin) + "_" + strings.ToUpper(k)); v != "" {
			out[k] = v
		}
	}
	return out
}

// ==== plugins and opt-in ======================================================

// PluginInfo is the settings view of a plugin.
type PluginInfo struct {
	Spec           Spec
	Installed      bool
	Enabled        bool
	RiskAcceptedAt time.Time
	Settings       map[string]string
	Status         host.Status
	Accounts       int
}

// Plugins lists the catalog plus any other discovered plugin.
func (s *Service) Plugins(ctx context.Context) ([]PluginInfo, error) {
	s.host.Rescan()
	names := []string{}
	seen := map[string]bool{}
	for _, sp := range Catalog {
		names, seen[sp.Name] = append(names, sp.Name), true
	}
	for _, st := range s.host.Statuses() {
		if !seen[st.Name] {
			names, seen[st.Name] = append(names, st.Name), true
		}
	}
	accs, err := s.st.ListMessengerAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	for _, a := range accs {
		count[a.Plugin]++
	}
	out := make([]PluginInfo, 0, len(names))
	for _, n := range names {
		p, err := s.st.GetMessengerPlugin(ctx, n)
		if err != nil {
			return nil, err
		}
		st := s.host.Status(n)
		out = append(out, PluginInfo{Spec: SpecFor(n), Installed: st.State != host.StateMissing, Enabled: p.Enabled,
			RiskAcceptedAt: p.RiskAcceptedAt, Settings: p.Settings, Status: st, Accounts: count[n]})
	}
	return out, nil
}

// PluginPatch changes the opt-in state or settings of a plugin.
type PluginPatch struct {
	Enabled      *bool
	AcceptRisk   bool
	Settings     map[string]string
	ResetSetting []string
}

// UpdatePlugin enables/disables a plugin. Enabling a plugin with a risk
// notice requires AcceptRisk (once).
func (s *Service) UpdatePlugin(ctx context.Context, name string, patch PluginPatch) error {
	if !validName(name) {
		return fmt.Errorf("messenger: invalid plugin name %q", name)
	}
	p, err := s.st.GetMessengerPlugin(ctx, name)
	if err != nil {
		return err
	}
	spec := SpecFor(name)
	if patch.AcceptRisk && p.RiskAcceptedAt.IsZero() {
		p.RiskAcceptedAt = s.now()
	}
	wasEnabled := p.Enabled
	if patch.Enabled != nil {
		if *patch.Enabled && spec.NeedsRiskAck() && p.RiskAcceptedAt.IsZero() {
			return ErrRiskNotAcked
		}
		p.Enabled = *patch.Enabled
	}
	settingsChanged := false
	for k, v := range patch.Settings {
		if p.Settings[k] != v {
			p.Settings[k], settingsChanged = v, true
		}
	}
	for _, k := range patch.ResetSetting {
		if _, ok := p.Settings[k]; ok {
			delete(p.Settings, k)
			settingsChanged = true
		}
	}
	p.UpdatedAt = s.now()
	if err := s.st.PutMessengerPlugin(ctx, p); err != nil {
		return err
	}
	switch {
	case wasEnabled && !p.Enabled:
		s.host.Stop(name)
	case p.Enabled && (!wasEnabled || settingsChanged) && s.hasAccounts(ctx, name):
		if settingsChanged && s.host.Running(name) {
			_ = s.host.Restart(name)
		} else {
			s.goBackground(func(ctx context.Context) { _ = s.host.Ensure(ctx, name) })
		}
	}
	s.notify.PluginChanged(name)
	return nil
}

func validName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (s *Service) hasAccounts(ctx context.Context, plugin string) bool {
	accs, err := s.st.ListMessengerAccounts(ctx, plugin)
	return err == nil && len(accs) > 0
}

func (s *Service) requireEnabled(ctx context.Context, plugin string) error {
	p, err := s.st.GetMessengerPlugin(ctx, plugin)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return fmt.Errorf("%w: %s", ErrDisabled, plugin)
	}
	return nil
}

// RestartPlugin restarts a plugin (also after StateFailed).
func (s *Service) RestartPlugin(ctx context.Context, name string) error {
	if err := s.requireEnabled(ctx, name); err != nil {
		return err
	}
	s.host.Rescan()
	return s.host.Restart(name)
}

// AccountState returns the last reported state of an account.
func (s *Service) AccountState(accountID int64) (wire.AccountStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[accountID]
	return st, ok
}

// onStarted connects all accounts of a plugin after it (re)started and
// syncs them.
func (s *Service) onStarted(ctx context.Context, plugin string) {
	accs, err := s.st.ListMessengerAccounts(ctx, plugin)
	if err != nil {
		return
	}
	for _, a := range accs {
		if a.Mode == store.MessengerBridge {
			continue // the Matrix session itself is a direct account
		}
		if err := s.host.Call(ctx, plugin, wire.MethodConnect, wire.AccountRef{Account: a.PluginAccount}, nil); err != nil {
			s.log.Warn("messenger: connect", "plugin", plugin, "account", a.AccountID, "err", err)
			_ = s.st.SetMessengerAccountState(ctx, a.AccountID, wire.StateError, err.Error())
			s.notify.AccountChanged(a.AccountID)
			continue
		}
		s.SyncAccount(a.AccountID)
	}
}

// RemoveAccount logs out the plugin session and deletes the account with
// all its chats.
func (s *Service) RemoveAccount(ctx context.Context, accountID int64) error {
	ma, err := s.st.GetMessengerAccount(ctx, accountID)
	if err != nil {
		return ErrNotMessenger
	}
	if err := s.host.Call(ctx, ma.Plugin, wire.MethodRemoveAccount, wire.AccountRef{Account: ma.PluginAccount}, nil); err != nil {
		// The session is removed locally anyway; the linked device may stay
		// visible on the phone until the user removes it there.
		s.log.Warn("messenger: remove account in plugin", "plugin", ma.Plugin, "err", err)
		_ = os.RemoveAll(filepath.Join(s.host.PluginDataDir(ma.Plugin), ma.PluginAccount))
	}
	if err := s.st.DeleteAccount(ctx, accountID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.states, accountID)
	s.mu.Unlock()
	s.notify.AccountChanged(accountID)
	s.notify.ViewsChanged()
	return nil
}
