// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/server/push"
)

func bgEnv(t *testing.T, mod ...func(*config.Config)) (*env, *recFCM) {
	t.Helper()
	old := backgroundStagger
	backgroundStagger = 0
	t.Cleanup(func() { backgroundStagger = old })
	f := &recFCM{}
	e := newEnv(t, Options{FCM: f, AppOptions: func(_ int64, o *app.Options) { o.Sync = nil }}, mod...)
	t.Cleanup(e.srv.apps.closeAll)
	return e, f
}

// bgUser creates a user whose store holds one account (enabled or not;
// none if accounts is 0) and closes the App again.
func bgUser(t *testing.T, e *env, name string, accounts int, enabled bool) *auth.User {
	t.Helper()
	u := e.user(name, auth.RoleUser)
	a, err := e.srv.App(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < accounts; i++ {
		if _, err := a.Store().CreateAccount(context.Background(), domain.Account{Kind: domain.KindIMAP, DisplayName: name,
			Enabled: enabled, Incoming: &domain.ServerConfig{Host: "127.0.0.1", Port: 1, TLS: domain.TLSNone}}); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func (e *env) idle(userIDs ...int64) {
	p := e.srv.apps
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range userIDs {
		if en, ok := p.m[id]; ok {
			en.lastUsed = time.Now().Add(-2 * appIdleTimeout)
		}
	}
}

func (e *env) loaded(id int64) bool {
	_, ok := e.srv.apps.loadedApp(id)
	return ok
}

func TestBackgroundAppKeptAndNotifies(t *testing.T) {
	e, f := bgEnv(t)
	anna := bgUser(t, e, "anna", 1, true)
	ben := bgUser(t, e, "ben", 1, false)
	cara := bgUser(t, e, "cara", 0, false)
	b := e.browser()
	b.login("anna")
	if resp, m := b.do("POST", "/api/push/subscribe", map[string]any{"type": "fcm", "token": "fcm-anna-0123456789abcdefghij",
		"deviceName": "Pixel", "appId": "org.tuxmail.app"}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}

	e.idle(anna.ID, ben.ID, cara.ID)
	e.srv.apps.prune()
	if !e.loaded(anna.ID) || e.loaded(ben.ID) || e.loaded(cara.ID) {
		t.Fatalf("loaded: anna=%v ben=%v cara=%v", e.loaded(anna.ID), e.loaded(ben.ID), e.loaded(cara.ID))
	}
	if got := e.srv.apps.backgroundUsers(); !slices.Equal(got, []int64{anna.ID}) {
		t.Fatalf("background users %v", got)
	}
	// A second prune keeps it (the UI state is released only once).
	e.srv.apps.prune()
	if !e.loaded(anna.ID) {
		t.Fatal("background app closed")
	}

	// New mail and chat messages notify although nobody uses the UI.
	a, _ := e.srv.apps.loadedApp(anna.ID)
	a.Bus().Publish(events.Event{Type: events.MailReceived, Source: "sync", Mail: &events.Mail{AccountID: 1, Key: "k1",
		FolderRole: "inbox", From: domain.Address{Name: "Bob"}, Subject: "Angebot", Date: time.Now()}})
	if p := f.wait(t, func(p push.Payload) bool { return p.Type == "mail" }); p.Title != push.PrivacyTitle || p.Sender != "" || p.Tag != "mail-k1" {
		t.Fatalf("mail payload %+v", p)
	}
	a.Bus().Publish(events.Event{Type: events.ChatReceived, Chat: &events.Chat{AccountID: 1, ChatID: "c1", Sender: "Clara", Text: "Hallo"}})
	f.wait(t, func(p push.Payload) bool {
		return p.Type == "chat" && p.Tag == "chat-c1" && p.Title == push.PrivacyTitle && p.Body == push.PrivacyBody
	})

	// UI use turns it back into a normal App.
	if _, err := e.srv.App(context.Background(), anna.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.srv.apps.backgroundUsers()) != 0 {
		t.Fatal("still marked background after UI use")
	}

	// Disabling the last account lets the App go at the next idle prune.
	accs, _ := a.Store().ListAccounts(context.Background())
	accs[0].Enabled = false
	if err := a.Store().UpdateAccount(context.Background(), accs[0]); err != nil {
		t.Fatal(err)
	}
	e.idle(anna.ID)
	e.srv.apps.prune()
	if e.loaded(anna.ID) {
		t.Fatal("app without enabled accounts kept")
	}
}

func TestBackgroundSyncDisabled(t *testing.T) {
	e, _ := bgEnv(t, func(c *config.Config) { c.Limits.BackgroundSync = false })
	anna := bgUser(t, e, "anna", 1, true)
	e.idle(anna.ID)
	e.srv.apps.prune()
	if e.loaded(anna.ID) {
		t.Fatal("idle app kept although background sync is off")
	}
	if n := e.srv.apps.reconcile(context.Background()); n != 0 || e.loaded(anna.ID) {
		t.Fatal("reconcile started an app")
	}
}

func TestBackgroundReconcileCapAndBackoff(t *testing.T) {
	e, _ := bgEnv(t, func(c *config.Config) { c.Limits.BackgroundSyncMaxUsers = 1 })
	ctx := context.Background()
	anna := bgUser(t, e, "anna", 1, true)
	ben := bgUser(t, e, "ben", 0, false)
	cara := bgUser(t, e, "cara", 2, true)
	for _, u := range []*auth.User{anna, ben, cara} {
		e.srv.apps.drop(u.ID) // as after a restart
	}
	dora := e.user("dora", auth.RoleUser) // never logged in: no store at all

	if n := e.srv.apps.reconcile(ctx); n != 1 {
		t.Fatalf("started %d, want 1 (cap)", n)
	}
	if e.loaded(ben.ID) || e.loaded(dora.ID) || e.loaded(anna.ID) == e.loaded(cara.ID) {
		t.Fatalf("anna=%v ben=%v cara=%v", e.loaded(anna.ID), e.loaded(ben.ID), e.loaded(cara.ID))
	}
	// An idle prune keeps background apps within the cap.
	e.idle(anna.ID, cara.ID)
	e.srv.apps.prune()
	if len(e.srv.apps.backgroundUsers()) != 1 {
		t.Fatal(e.srv.apps.backgroundUsers())
	}

	e.cfg.Limits.BackgroundSyncMaxUsers = 5
	if n := e.srv.apps.reconcile(ctx); n != 1 || !e.loaded(anna.ID) || !e.loaded(cara.ID) {
		t.Fatalf("second round started %d", n)
	}

	// Disabled users lose their background app.
	if err := e.svc.SetDisabled(ctx, cara.ID, true); err != nil {
		t.Fatal(err)
	}
	e.srv.apps.reconcile(ctx)
	if e.loaded(cara.ID) {
		t.Fatal("disabled user still synced")
	}
	if err := e.svc.SetDisabled(ctx, cara.ID, false); err != nil {
		t.Fatal(err)
	}

	// A failing open is retried only after the backoff.
	calls := 0
	orig := e.srv.apps.open
	e.srv.apps.open = func(ctx context.Context, id int64) (*appEntry, error) {
		if id == cara.ID {
			calls++
			return nil, errors.New("disk on fire")
		}
		return orig(ctx, id)
	}
	e.srv.apps.reconcile(ctx)
	e.srv.apps.reconcile(ctx)
	if calls != 1 || e.loaded(cara.ID) {
		t.Fatalf("open calls %d", calls)
	}
	e.srv.apps.mu.Lock()
	f := e.srv.apps.bg.failures[cara.ID]
	f.next = time.Now().Add(-time.Second)
	e.srv.apps.bg.failures[cara.ID] = f
	e.srv.apps.mu.Unlock()
	e.srv.apps.open = orig
	if n := e.srv.apps.reconcile(ctx); n != 1 || !e.loaded(cara.ID) {
		t.Fatalf("retry after backoff started %d", n)
	}
	e.srv.apps.mu.Lock()
	_, still := e.srv.apps.bg.failures[cara.ID]
	e.srv.apps.mu.Unlock()
	if still {
		t.Fatal("failure kept after success")
	}

	// Shutdown: no more background starts.
	e.srv.apps.closeAll()
	if n := e.srv.apps.reconcile(ctx); n != 0 {
		t.Fatal("started after close")
	}
}
