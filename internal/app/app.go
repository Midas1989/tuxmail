// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/ai"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/imageproxy"
	"github.com/Midas1989/Mailclient/internal/messenger"
	"github.com/Midas1989/Mailclient/internal/project"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// Modes of operation.
const (
	ModeDesktop = "desktop"
	ModeServer  = "server"
)

// Options configure an App. Only Store is required.
type Options struct {
	Store *store.Store
	// Blobs holds raw mails, attachments and uploads (default: DataDir/blobs).
	Blobs *blob.Store
	// Bus is the core event bus of this user (created if nil).
	Bus *events.Bus
	// Secrets stores account passwords and tokens (default: in memory).
	Secrets domain.SecretStore
	// Vault controls a portable master-password vault (desktop, optional).
	Vault VaultControl

	// Sync enables the sync engine (internal/sync); it is ignored when
	// Syncer is set. Without both, actions only change the local store.
	Sync   *SyncOptions
	Syncer Syncer // default: sync manager (Sync) or local-only fallback
	// Send enables sending through internal/send; ignored when Sender is set.
	Send       *SendOptions
	Sender     Sender          // default: MailSender (Send) or sending fails with send_not_configured
	Renderer   MessageRenderer // default: basic go-message renderer (text only)
	Discoverer Discoverer      // default: internal/autoconfig
	Push       PushBackend     // server mode Web Push; nil on the desktop
	AI         AIFactory       // default: no provider (ai_not_configured)
	// Messenger enables the messenger plugins (messenger_wiring.go).
	Messenger *MessengerOptions
	OAuth     *OAuthOptions // OAuth clients/redirect (default: environment), oauth_app.go
	// ImageSigner signs remote image proxy URLs (server mode: server key,
	// subject = user id); default: per-installation key, mailproto.go.
	ImageSigner *imageproxy.Signer
	// VacationServers overrides the server-side out-of-office backends
	// (default: EAS + Graph/EWS/Gmail + Sieve/JMAP), VacationMail the mail
	// backend of the local responder (default: the workflow mail actions);
	// both mainly for tests, vacation.go.
	VacationServers func(ctx context.Context) (map[int64]vacation.ServerVacationSync, error)
	VacationMail    workflow.MailActions
	// PIM configures calendars, tasks and contacts (pim.go). The background
	// loop (CalDAV/CardDAV sync, reminders, invitations) runs when PIM or
	// Sync is set and PIM.Disabled is false.
	PIM *PIMOptions
	// Team gives access to shared team mailboxes (server mode, team_source.go).
	Team TeamSource
	// WorkflowActions are registered in addition to the built-in actions
	// (e.g. the team action "assign" of a team mailbox runtime).
	WorkflowActions []workflow.ActionType
	// Notifier delivers the workflow action notify (channel "push" ...).
	Notifier workflow.Notifier

	// Locales are the built-in language files (locales/*.json); LocaleDir
	// holds additional/override files (data_dir/locales).
	Locales   fs.FS
	LocaleDir string

	// DataDir is the per-user data directory (uploads, blobs).
	DataDir string
	Mode    string // ModeDesktop or ModeServer
	Version string

	// HTTPClient is used for remote calls (provisioning, AI).
	HTTPClient *http.Client
	Now        func() time.Time
	Logger     *slog.Logger
	Location   *time.Location
}

// App is the application layer of one user: it implements the frontend API
// (api.ts) on top of the store and the core services. It is safe for
// concurrent use.
type App struct {
	kick    chan struct{} // wakes the timer loop early (e.g. after adding an account)
	opts    Options
	st      *store.Store
	blobs   *blob.Store
	bus     *events.Bus
	ownsBus bool
	hub     *Hub
	proj    *project.Service
	wf      *workflow.Engine
	wfReg   *workflow.Registry
	sync    Syncer
	mgr     *tsync.Manager
	provReg *tsync.Registry // providers of the sync engine (account tests, account_edit.go)
	render  MessageRenderer
	log     *slog.Logger
	msgr    *messenger.Service

	idxMu      sync.Mutex
	idxDirty   bool
	idxMsgMax  int64
	idxChatMax int64

	runOnce sync.Once
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	remoteMu sync.Mutex
	remote   map[string]*remoteSession // provisioning sessions by server URL

	syncCreds tsync.Credentials // credentials of the sync engine (EAS PIM, OOF)
	easPIM    easPIM            // EAS calendar/contacts scheduling (eas.go)
	cloud     cloudState        // cloud_accounts.go
	oauth     *oauthState       // oauth_app.go
	mp        mailProtoState    // mailproto.go
	vac       vacationState     // vacation.go
	pimOnce   sync.Once
	pim       *pimService
	comfort   *comfortState // comfort_state.go (spam, categories, S/MIME, PDF, ...)
	diag      diagState     // diagnostics.go
}

// New creates the application layer.
func New(ctx context.Context, opts Options) (*App, error) {
	if opts.Store == nil {
		return nil, errors.New("app: a store is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Mode == "" {
		opts.Mode = ModeDesktop
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	a := &App{kick: make(chan struct{}, 1), opts: opts, st: opts.Store, bus: opts.Bus, hub: NewHub(), log: opts.Logger, idxDirty: true,
		remote: map[string]*remoteSession{}}
	if a.bus == nil {
		a.bus, a.ownsBus = events.New(), true
	}
	a.blobs = opts.Blobs
	if a.blobs == nil && opts.DataDir != "" {
		b, err := blob.New(filepath.Join(opts.DataDir, "blobs"))
		if err != nil {
			return nil, err
		}
		a.blobs = b
	}
	if a.opts.Secrets == nil {
		a.opts.Secrets = newMemorySecrets()
	}
	a.oauth = newOAuthState(a)
	a.render = opts.Renderer
	if a.render == nil {
		a.render = NewMIMERenderer()
	}
	if a.opts.AI == nil {
		hc := opts.HTTPClient
		a.opts.AI = func(s AiSettings, key string) (AIProvider, error) {
			if s.Provider == "none" {
				return nil, nil
			}
			return ai.New(s.Provider, s.BaseURL, s.Model, key, hc)
		}
	}
	if a.opts.Discoverer == nil {
		a.opts.Discoverer = autoconfigDiscoverer{}
	}
	a.proj = project.New(a.st, a.bus, project.Options{Now: opts.Now, Logger: opts.Logger})
	a.comfort = newComfortState(a)
	a.sync = opts.Syncer
	var mail workflow.MailActions = &workflowMail{a: a}
	if a.opts.Sender == nil && opts.Send != nil {
		ms, err := newMailSender(a, opts.Send)
		if err != nil {
			return nil, err
		}
		a.opts.Sender = ms
	}
	if a.sync == nil && opts.Sync != nil && a.blobs != nil {
		cfg := tsync.Config{Store: a.st, Blobs: a.blobs, Bus: a.bus, Secrets: a.opts.Secrets, Projects: a.proj, Tags: a.proj,
			Logger: opts.Logger, Now: opts.Now, Extractor: ParseExtractor{}}
		if ms, ok := a.opts.Sender.(*MailSender); ok {
			cfg.Sender = syncSender{act: func() *send.Actions { return ms.Actions }}
		}
		a.configureEASSync(&cfg)
		a.configureOAuthSync(&cfg)
		a.configureComfortSync(&cfg)
		if opts.Sync.Configure != nil {
			opts.Sync.Configure(&cfg)
		}
		a.syncCreds = cfg.Credentials
		a.provReg = cfg.Providers
		m, err := tsync.New(cfg)
		if err != nil {
			return nil, err
		}
		a.mgr, a.sync, mail = m, managerSyncer{m: m}, m.MailActions()
	}
	if a.sync == nil {
		a.sync = &localSyncer{st: a.st}
	}
	a.wfReg = workflow.DefaultRegistry()
	for _, t := range opts.WorkflowActions {
		if err := a.wfReg.Register(t); err != nil {
			return nil, err
		}
	}
	deps := workflow.Deps{Mail: mail, Tasks: &workflowTasks{a: a}, HTTPClient: opts.HTTPClient,
		Cloud: workflowCloud{a: a}, PDF: workflowPDF{a: a}, Notifier: opts.Notifier}
	a.proj.Workflow().Deps(&deps)
	a.vac.mail, deps.Vacation = mail, vacationProxy{a: a} // vacation.go
	loc := workflow.RunLocal
	if opts.Mode == ModeServer {
		loc = workflow.RunServer
	}
	wf, err := workflow.New(workflow.Config{Store: a.st, Bus: a.bus, Registry: a.wfReg, Deps: deps, Location: loc,
		TimeZone: opts.Location, Now: opts.Now, Logger: opts.Logger})
	if err != nil {
		return nil, err
	}
	a.wf = wf
	if err := a.wf.Load(ctx); err != nil {
		a.log.Warn("app: some workflows could not be loaded", "err", err)
	}
	if err := a.initMessenger(); err != nil {
		return nil, err
	}
	return a, nil
}

// Start runs the background work (event bridge, workflow engine, scheduled
// messages, snooze wake-up) until Close. It is idempotent.
func (a *App) Start() {
	a.runOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		if n, err := a.st.PurgeSystemKeywordTags(ctx); err != nil {
			a.log.Warn("app: purging system keyword tags", "err", err)
		} else if n > 0 {
			a.log.Info("app: removed tags made from system keywords", "count", n)
		}
		if a.mgr != nil {
			if err := a.mgr.Start(ctx); err != nil {
				a.log.Warn("app: starting sync failed", "err", err)
			}
		}
		a.wg.Add(3)
		go func() { defer a.wg.Done(); a.bridgeBus(ctx) }()
		go func() {
			defer a.wg.Done()
			if err := a.wf.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.log.Warn("app: workflow engine stopped", "err", err)
			}
		}()
		go func() { defer a.wg.Done(); a.timers(ctx) }()
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.comfortLoop(ctx) }()
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.loadVacation(ctx) }() // vacation.go
		if a.opts.PIM != nil || a.opts.Sync != nil {
			a.wg.Add(1)
			go func() { defer a.wg.Done(); a.runPIM(ctx) }()
		}
		if a.msgr != nil {
			a.msgr.Start()
		}
	})
}

// Close stops the background work. The store is owned by the caller.
func (a *App) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	if a.msgr != nil {
		a.msgr.Close()
	}
	if a.mgr != nil {
		a.mgr.Stop()
	}
	if a.ownsBus {
		a.bus.Close()
	}
	return nil
}

// ReleaseUIState drops state that only serves an interactive session while
// the App keeps running for background sync (server mode, idle users):
// desktop provisioning sessions, finished sign-in flows and cached cloud
// clients (rebuilt on demand). Sync, timers and workflows are unaffected.
func (a *App) ReleaseUIState() {
	a.remoteMu.Lock()
	a.remote = map[string]*remoteSession{}
	a.remoteMu.Unlock()
	if a.oauth != nil {
		a.oauth.mu.Lock()
		for id, f := range a.oauth.flows {
			if f.status.Status != "pending" {
				delete(a.oauth.flows, id)
			}
		}
		a.oauth.mu.Unlock()
	}
	a.cloud.mu.Lock()
	a.cloud.clients = nil
	a.cloud.mu.Unlock()
}

// Events returns the UI event hub.
func (a *App) Events() *Hub { return a.hub }

// Blobs returns the blob store (raw mails, attachments, uploads).
func (a *App) Blobs() *blob.Store { return a.blobs }

// Bus returns the core event bus.
func (a *App) Bus() *events.Bus { return a.bus }

// Store returns the store.
func (a *App) Store() *store.Store { return a.st }

// Project returns the tag/project/search folder service.
func (a *App) Project() *project.Service { return a.proj }

// Mode returns ModeDesktop or ModeServer.
func (a *App) Mode() string { return a.opts.Mode }

// timers wakes snoozed conversations and sends due scheduled messages.
func (a *App) timers(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		a.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.kick:
		}
	}
}

// kickTimers runs the timer loop's work now instead of at the next tick.
func (a *App) kickTimers() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *App) tick(ctx context.Context) {
	if ids, err := a.st.WakeSnoozed(ctx, a.now()); err == nil {
		for _, id := range ids {
			a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: id})
		}
	}
	a.sendDueScheduled(ctx)
	a.runSendOps(ctx)
	a.RunDueBackups(ctx)
	a.syncEASPIMDue(ctx)
	if hashes, err := a.st.PurgeUploads(ctx, a.now().Add(-24*time.Hour)); err == nil && a.blobs != nil {
		for _, h := range hashes {
			_ = a.blobs.Delete(h)
		}
	}
}

func (a *App) now() time.Time { return a.opts.Now() }

// ==== helpers ==================================================================

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, invalid("invalid time %q", s)
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func msgTime(m store.Message) time.Time {
	if !m.SentAt.IsZero() {
		return m.SentAt
	}
	return m.ReceivedAt
}

// Item ids of the chat timeline.
func mailItemID(id int64) string { return fmt.Sprintf("m:%d", id) }
func chatItemID(id int64) string { return fmt.Sprintf("c:%d", id) }

// parseItemID splits "m:12" / "c:7" (also "s:<scheduled id>").
func parseItemID(s string) (kind string, id int64, err error) {
	k, v, ok := strings.Cut(s, ":")
	if !ok || (k != "m" && k != "c") {
		return "", 0, invalid("invalid item id %q", s)
	}
	if _, err := fmt.Sscan(v, &id); err != nil || id <= 0 {
		return "", 0, invalid("invalid item id %q", s)
	}
	return k, id, nil
}

// userDataDir returns a subdirectory of the data dir, creating it.
func (a *App) userDataDir(name string) (string, error) {
	if a.opts.DataDir == "" {
		return "", errors.New("app: no data directory configured")
	}
	dir := filepath.Join(a.opts.DataDir, name)
	return dir, os.MkdirAll(dir, 0o700)
}

// memorySecrets is the default secret store (tests, no keyring available).
type memorySecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemorySecrets() *memorySecrets { return &memorySecrets{m: map[string][]byte{}} }

func (s *memorySecrets) Get(ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return nil, errNotFound
	}
	return append([]byte{}, v...), nil
}

func (s *memorySecrets) Set(ref string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = append([]byte{}, v...)
	return nil
}

func (s *memorySecrets) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}
