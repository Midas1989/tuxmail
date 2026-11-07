// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/server/api"
	"github.com/Midas1989/Mailclient/internal/server/push"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Per-user application layer (KONZEPT 15.6: strictly separated data per
// user). Every user gets an internal/app.App on the store
// data_dir/users/<id>/tuxmail.db (UserStoreFile, the same file MailData
// reads for provisioning) and the blob store data_dir/users/<id>/blobs.
// Apps are opened on first use. Without requests and WebSocket subscribers
// for appIdleTimeout an App is
//
//   - kept running as a background App (limits.background_sync, default on)
//     when the user has at least one enabled account: mail sync, timers,
//     workflows, out-of-office and push notifications keep working while
//     nobody is logged in; only UI state is released (App.ReleaseUIState);
//   - closed otherwise.
//
// At startup and every backgroundReconcileInterval the pool also opens the
// Apps of users who have enabled accounts but no App yet (after a restart),
// one after another with backgroundStagger between them, at most
// limits.background_sync_max_users at a time and with an exponential
// backoff for users whose App fails to open (background.go).

const appIdleTimeout = time.Hour

type appEntry struct {
	app      *app.App
	st       *store.Store
	lastUsed time.Time
	// background marks an App kept open (or opened) only for background
	// sync; it is cleared by the next UI request.
	background bool
	// uiReleased is set once the UI state of a background App was released.
	uiReleased bool
}

type appPool struct {
	s      *Server
	mu     sync.Mutex
	m      map[int64]*appEntry
	closed bool

	bg backgroundState // background.go
	// open creates the App of a user (p.openApp; tests replace it).
	open func(ctx context.Context, userID int64) (*appEntry, error)
}

func newAppPool(s *Server) *appPool {
	p := &appPool{s: s, m: map[int64]*appEntry{}}
	p.open = p.openApp
	p.bg.failures = map[int64]bgFailure{}
	return p
}

// get returns (opening if needed) the App of a user for a UI request.
func (p *appPool) get(ctx context.Context, userID int64) (*app.App, error) {
	return p.acquire(ctx, userID, false)
}

// acquire returns the App of a user, opening it if needed. background
// opens do not count as UI use (lastUsed stays old so idle eviction and the
// background cap apply right away).
func (p *appPool) acquire(ctx context.Context, userID int64, background bool) (*app.App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("server: shutting down")
	}
	if e, ok := p.m[userID]; ok {
		if !background {
			e.lastUsed, e.background, e.uiReleased = time.Now(), false, false
		}
		return e.app, nil
	}
	e, err := p.open(ctx, userID)
	if err != nil {
		return nil, err
	}
	e.lastUsed = time.Now()
	if background {
		e.lastUsed, e.background = time.Time{}, true
	}
	p.m[userID] = e
	return e.app, nil
}

// openApp opens the store and creates and starts the App of a user.
func (p *appPool) openApp(ctx context.Context, userID int64) (*appEntry, error) {
	dir := p.s.auth.UserDir(userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := backup.ApplyPending(dir); err != nil { // staged in-app "replace" restore
		return nil, err
	}
	st, err := store.Open(ctx, filepath.Join(dir, UserStoreFile))
	if err != nil {
		return nil, err
	}
	secrets, err := p.s.auth.UserSecrets(ctx, userID)
	if err != nil {
		st.Close()
		return nil, err
	}
	opts := app.Options{Store: st, DataDir: dir, Mode: app.ModeServer, Version: p.s.version, Secrets: secrets,
		Logger: p.s.log.With("user", userID), Locales: p.s.locales, LocaleDir: filepath.Join(p.s.cfg.DataDir, "locales"),
		Push: &pushBackend{s: p.s, userID: userID}, Sync: &app.SyncOptions{}, Send: &app.SendOptions{}, Messenger: &app.MessengerOptions{}}
	opts.OAuth = p.s.oauthAppOptions()         // oauth.go
	opts.ImageSigner = p.s.imageSigner(userID) // imageproxy.go
	p.s.configureUserApp(userID, &opts)        // team.go: team mailboxes, workflow notifier
	if p.s.appOptions != nil {
		p.s.appOptions(userID, &opts)
	}
	a, err := app.New(ctx, opts)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.Start()
	go p.s.watchUserApp(userID, a) // push_notify.go
	return &appEntry{app: a, st: st}, nil
}

// prune handles idle apps: users with enabled accounts keep a background
// App (up to the cap) whose UI state is released, the others are closed.
func (p *appPool) prune() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p.mu.Lock()
	var idle []int64
	for id, e := range p.m {
		if time.Since(e.lastUsed) > appIdleTimeout && e.app.Events().Subscribers() == 0 {
			idle = append(idle, id)
		}
	}
	p.mu.Unlock()
	var closeList, release []*appEntry
	for _, id := range idle {
		p.mu.Lock()
		e, ok := p.m[id]
		p.mu.Unlock()
		if !ok {
			continue
		}
		keep := p.backgroundEnabled() && hasEnabledAccounts(ctx, e.st)
		p.mu.Lock()
		if cur, ok := p.m[id]; !ok || cur != e || time.Since(e.lastUsed) <= appIdleTimeout {
			p.mu.Unlock()
			continue // used or dropped meanwhile
		}
		if keep && !e.background && p.backgroundCountLocked() >= p.maxBackground() {
			keep = false
			p.s.log.Warn("background sync limit reached, closing idle app", "user", id, "max", p.maxBackground())
		}
		if keep {
			e.background = true
			if !e.uiReleased {
				e.uiReleased = true
				release = append(release, e)
			}
		} else {
			delete(p.m, id)
			closeList = append(closeList, e)
		}
		p.mu.Unlock()
	}
	for _, e := range release {
		e.app.ReleaseUIState()
	}
	for _, e := range closeList {
		e.close()
	}
}

// drop closes the App of a user (e.g. after the user was deleted).
func (p *appPool) drop(userID int64) {
	p.mu.Lock()
	e := p.m[userID]
	delete(p.m, userID)
	p.mu.Unlock()
	if e != nil {
		e.close()
	}
}

// lockPGP forgets the OpenPGP passphrases of a user whose session ended. An App
// that is not open has none.
func (p *appPool) lockPGP(userID int64) {
	p.mu.Lock()
	e := p.m[userID]
	p.mu.Unlock()
	if e != nil && e.app != nil {
		e.app.LockPGPKeys()
	}
}

func (p *appPool) closeAll() {
	p.mu.Lock()
	all := p.m
	p.m = map[int64]*appEntry{}
	p.closed = true
	p.mu.Unlock()
	for _, e := range all {
		e.close()
	}
}

func (e *appEntry) close() {
	_ = e.app.Close()
	_ = e.st.Close()
}

// readonlyAllowed are the state-changing routes of the application API that
// users with the role readonly ("guest", KONZEPT 15.6) may call: queries
// sent as POST, read markers of team mentions, presence and UI preferences.
// Everything else that is not GET/HEAD is refused for them (audit A-03);
// their team mailbox role is capped at read (internal/team).
var readonlyAllowed = map[string]bool{
	"POST /api/search":                                    true,
	"POST /api/team/{teamId}/search":                      true,
	"POST /api/workflows/validate":                        true,
	"POST /api/workflows/complete":                        true,
	"POST /api/team/{teamId}/workflows/validate":          true,
	"POST /api/team/{teamId}/workflows/complete":          true,
	"POST /api/team/mentions/read":                        true,
	"POST /api/team/{teamId}/conversations/{id}/presence": true,
	"PUT /api/preferences":                                true,
}

// appHandler authenticates the request (cookie session or Bearer device
// token) and passes the user's App to h.
func (s *Server) appHandler(h api.AppHandler) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, a *authInfo) {
		if a.Session.Kind == auth.KindDevice && deviceForbiddenAppRoutes[r.Pattern] {
			refuseDevice(w)
			return
		}
		r = r.WithContext(api.WithGuards(r.Context(), api.Guards{
			Reauth: func(w http.ResponseWriter, r *http.Request, re api.Reauth) bool {
				if a.Session.Kind == auth.KindDevice {
					refuseDevice(w)
					return false
				}
				return s.reauth(w, r, a, re.Password, re.Code, re.RecoveryCode)
			},
			NoDevice: func(w http.ResponseWriter, _ *http.Request) bool {
				if a.Session.Kind == auth.KindDevice {
					refuseDevice(w)
					return false
				}
				return true
			},
		}))
		if a.User.Role == auth.RoleReadonly && !safeMethod(r.Method) && !readonlyAllowed[r.Pattern] {
			writeError(w, http.StatusForbidden, "read_only", "this account is read-only")
			return
		}
		ap, err := s.apps.get(r.Context(), a.User.ID)
		if err != nil {
			s.log.Error("open user data", "user", a.User.ID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "user data unavailable")
			return
		}
		h(w, r, ap)
	})
}

// App returns the application layer of a user (for other server components).
func (s *Server) App(ctx context.Context, userID int64) (*app.App, error) {
	return s.apps.get(ctx, userID)
}

// pushBackend implements app.PushBackend for one user on the server's Web
// Push store and sender.
type pushBackend struct {
	s      *Server
	userID int64
}

func (b *pushBackend) VAPIDPublicKey() string { return b.s.push.Keys.PublicKey }

func (b *pushBackend) Subscribe(ctx context.Context, in app.PushSubscriptionInput) error {
	sub := push.Subscription{UserID: b.userID, Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth,
		DeviceName: in.DeviceName, UserAgent: in.UserAgent, Kind: push.KindWebPush}
	if err := push.ValidateSubscription(sub); err != nil {
		return app.Errorf(http.StatusBadRequest, "invalid_subscription", "%v", err)
	}
	_, err := b.s.pushStore.Save(ctx, sub)
	return err
}

func (b *pushBackend) Unsubscribe(ctx context.Context, endpoint string) error {
	return b.s.pushStore.Delete(ctx, b.userID, endpoint)
}

// Settings maps the push preferences to NotificationSettings.
func (b *pushBackend) Settings(ctx context.Context) (app.NotificationSettings, error) {
	p, err := b.s.pushStore.Prefs(ctx, b.userID)
	if err != nil {
		return app.NotificationSettings{}, err
	}
	out := app.DefaultNotificationSettings()
	if !p.ShowContent {
		out.Privacy = "generic"
	}
	if p.QuietHours != nil {
		out.QuietHours = app.QuietHours{Enabled: true, Start: p.QuietHours.Start, End: p.QuietHours.End}
	}
	for k, v := range p.Accounts {
		out.Accounts[k] = v
	}
	for k, v := range p.Channels {
		if k == "mail" {
			k = "email"
		}
		out.Channels[k] = v
	}
	return out, nil
}

func (b *pushBackend) SaveSettings(ctx context.Context, in app.NotificationSettings) (app.NotificationSettings, error) {
	cur, err := b.s.pushStore.Prefs(ctx, b.userID)
	if err != nil {
		return app.NotificationSettings{}, err
	}
	p := push.Prefs{Enabled: true, ShowContent: in.Privacy != "generic", Accounts: in.Accounts,
		Channels: map[string]bool{}}
	if p.Accounts == nil {
		p.Accounts = map[string]bool{}
	}
	for k, v := range in.Channels {
		if k == "email" {
			k = "mail"
		}
		p.Channels[k] = v
	}
	if in.QuietHours.Enabled {
		tz := ""
		if cur.QuietHours != nil {
			tz = cur.QuietHours.Timezone
		}
		p.QuietHours = &push.QuietHours{Start: in.QuietHours.Start, End: in.QuietHours.End, Timezone: tz}
	}
	if err := b.s.pushStore.SetPrefs(ctx, b.userID, p); err != nil {
		return app.NotificationSettings{}, app.Errorf(http.StatusBadRequest, app.CodeInvalidInput, "%v", err)
	}
	return b.Settings(ctx)
}

func (b *pushBackend) Test(ctx context.Context) error {
	subs, err := b.s.pushStore.List(ctx, b.userID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return app.Errorf(http.StatusConflict, "no_subscriptions", "no device is subscribed to notifications")
	}
	n := push.Notification{Payload: push.Payload{Type: "test", Title: "TuxMail", Body: "Test", URL: "./", Tag: "test"}, Urgency: push.UrgencyNormal}
	var errs []error
	for _, sub := range subs {
		if err := b.s.push.SendTo(ctx, sub, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sub.DeviceName, err))
		}
	}
	return errors.Join(errs...)
}
