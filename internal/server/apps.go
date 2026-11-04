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
	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/server/api"
	"github.com/Midas1989/Mailclient/internal/server/push"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Per-user application layer (KONZEPT 15.6: strictly separated data per
// user). Every user gets an internal/app.App on the store
// data_dir/users/<id>/tuxmail.db (UserStoreFile, the same file MailData
// reads for provisioning) and the blob store data_dir/users/<id>/blobs.
// Apps are opened on first use and closed after appIdleTimeout without
// requests and WebSocket subscribers.

const appIdleTimeout = time.Hour

type appEntry struct {
	app      *app.App
	st       *store.Store
	lastUsed time.Time
}

type appPool struct {
	s  *Server
	mu sync.Mutex
	m  map[int64]*appEntry
}

func newAppPool(s *Server) *appPool { return &appPool{s: s, m: map[int64]*appEntry{}} }

// get returns (opening if needed) the App of a user.
func (p *appPool) get(ctx context.Context, userID int64) (*app.App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.m[userID]; ok {
		e.lastUsed = time.Now()
		return e.app, nil
	}
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
		Push: &pushBackend{s: p.s, userID: userID}, Sync: &app.SyncOptions{}, Send: &app.SendOptions{}}
	opts.OAuth = p.s.oauthAppOptions()  // oauth.go
	p.s.configureUserApp(userID, &opts) // team.go: team mailboxes, workflow notifier
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
	p.m[userID] = &appEntry{app: a, st: st, lastUsed: time.Now()}
	return a, nil
}

// prune closes idle apps.
func (p *appPool) prune() {
	p.mu.Lock()
	var idle []*appEntry
	for id, e := range p.m {
		if time.Since(e.lastUsed) > appIdleTimeout && e.app.Events().Subscribers() == 0 {
			idle = append(idle, e)
			delete(p.m, id)
		}
	}
	p.mu.Unlock()
	for _, e := range idle {
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

func (p *appPool) closeAll() {
	p.mu.Lock()
	all := p.m
	p.m = map[int64]*appEntry{}
	p.mu.Unlock()
	for _, e := range all {
		e.close()
	}
}

func (e *appEntry) close() {
	_ = e.app.Close()
	_ = e.st.Close()
}

// appHandler authenticates the request (cookie session or Bearer device
// token) and passes the user's App to h.
func (s *Server) appHandler(h api.AppHandler) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, a *authInfo) {
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
