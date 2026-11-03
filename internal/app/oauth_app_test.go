// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/providers/graph/graphtest"
	"github.com/Midas1989/Mailclient/internal/store"
)

// fakeMicrosoft is a minimal identity provider issuing the token "tok".
func fakeMicrosoft(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /common/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /common/token", func(w http.ResponseWriter, r *http.Request) {
		claims, _ := json.Marshal(map[string]string{"preferred_username": "anna@contoso.example", "name": "Anna Admin"})
		idt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".x"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 3600, "refresh_token": "rt", "id_token": idt})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := oauth.Endpoints[oauth.Microsoft]
	ep := old
	ep.AuthURL, ep.TokenURL, ep.LoopbackHost = srv.URL+"/{tenant}/authorize", srv.URL+"/{tenant}/token", "127.0.0.1"
	oauth.Endpoints[oauth.Microsoft] = ep
	t.Cleanup(func() { oauth.Endpoints[oauth.Microsoft] = old })
}

func browser(_ context.Context, u string) error {
	go func() {
		if resp, err := http.Get(u); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	return nil
}

func newOAuthApp(t *testing.T, opts *app.OAuthOptions) (*app.App, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := app.New(ctx, app.Options{Store: st, OAuth: opts, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, st
}

func waitFlow(t *testing.T, a *app.App, id string) app.OAuthFlowStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := a.OAuthFlow(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != "pending" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("flow did not finish")
	return app.OAuthFlowStatus{}
}

func TestOAuthWizardGraphAccount(t *testing.T) {
	fakeMicrosoft(t)
	gs := graphtest.New("tok")
	defer gs.Close()
	ctx := context.Background()

	// Not configured: clear error code.
	a0, _ := newOAuthApp(t, &app.OAuthOptions{Clients: oauth.Clients{}})
	_, err := a0.StartOAuth(ctx, app.OAuthStartInput{Provider: "microsoft", Email: "anna@contoso.example", Kind: "graph"})
	if app.AsError(err).Code != app.CodeOAuthNotConfigured {
		t.Fatal(err)
	}
	// Own client ID in the settings makes it work.
	if err := a0.SaveOAuthClient(ctx, app.OAuthClientInput{Provider: "microsoft", ClientID: "own-id", Tenant: "organizations"}); err != nil {
		t.Fatal(err)
	}
	ps, _ := a0.OAuthProviders(ctx)
	if !ps[0].Configured || ps[0].Source != "user" || ps[0].ClientID != "own-id" || ps[1].Configured {
		t.Fatalf("%+v", ps)
	}

	a, st := newOAuthApp(t, &app.OAuthOptions{Clients: oauth.Clients{"microsoft": {ID: "cid", Source: "env"}}, Opener: browser,
		GraphBaseURL: gs.BaseURL()})
	fl, err := a.StartOAuth(ctx, app.OAuthStartInput{Provider: "microsoft", Email: "anna@contoso.example", Kind: "graph"})
	if err != nil {
		t.Fatal(err)
	}
	if fl.Mode != "loopback" || fl.AuthURL == "" {
		t.Fatalf("%+v", fl)
	}
	done := waitFlow(t, a, fl.ID)
	if done.Status != "done" || done.AccountID == 0 || done.Email != "anna@contoso.example" {
		t.Fatalf("%+v", done)
	}
	acc, err := st.GetAccount(ctx, done.AccountID)
	if err != nil || acc.Kind != domain.KindGraph || acc.Extra["oauthProvider"] != "microsoft" || acc.SecretRef == "" {
		t.Fatalf("%+v %v", acc, err)
	}
	ids, _ := st.ListIdentities(ctx, acc.ID)
	if len(ids) != 1 || ids[0].Name != "Anna Admin" {
		t.Fatal(ids)
	}
	tok, err := a.OAuthManager().TokenSource(acc.SecretRef, oauth.ResourceGraph).Token(ctx)
	if err != nil || tok != "tok" {
		t.Fatal(tok, err)
	}

	// Shared mailbox with Send-on-Behalf identity.
	shared, err := a.AddSharedMailbox(ctx, app.SharedMailboxInput{AccountID: acc.ID, Address: "info@contoso.example", SendMode: "send_on_behalf"})
	if err != nil {
		t.Fatal(err)
	}
	if shared.Extra["mailbox"] != "info@contoso.example" || shared.Extra[oauth.ExtraSecretRef] != acc.SecretRef {
		t.Fatalf("%+v", shared)
	}
	dels, _ := a.ListIdentityDelegations(ctx)
	if len(dels) != 1 || dels[0].Mode != "send_on_behalf" || dels[0].Mailbox != "info@contoso.example" {
		t.Fatal(dels)
	}
	if err := a.SetIdentityDelegation(ctx, store.IdentityDelegation{IdentityID: dels[0].IdentityID, Mode: "send_as"}); err != nil {
		t.Fatal(err)
	}
	shared2, _ := st.GetAccount(ctx, shared.ID)
	if shared2.Extra["sendMode"] != "send_as" {
		t.Fatal(shared2.Extra)
	}

	// Calendars and contacts of the Graph account land in the store.
	res, err := a.SyncCloudPIM(ctx, acc.ID)
	if err != nil || res.Calendars != 1 || res.Contacts != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	cs, _ := st.FindContactsByEmail(ctx, "boss@contoso.example")
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	vs, err := a.VacationServers(ctx)
	if err != nil || len(vs) != 2 {
		t.Fatal(vs, err)
	}
}

func TestOAuthRedirectFlowServerMode(t *testing.T) {
	fakeMicrosoft(t)
	ctx := context.Background()
	a, st := newOAuthApp(t, &app.OAuthOptions{Clients: oauth.Clients{"microsoft": {ID: "cid", Secret: "s", Source: "admin"}},
		RedirectBase: "https://mail.example"})
	fl, err := a.StartOAuth(ctx, app.OAuthStartInput{Provider: "microsoft", Email: "anna@contoso.example", Kind: "imap", Mode: "redirect"})
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Get(fl.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Path != "/api/oauth/microsoft/callback" {
		t.Fatal(loc)
	}
	if _, err := a.OAuthCallback(ctx, loc.Query()); err != nil {
		t.Fatal(err)
	}
	s, _ := a.OAuthFlow(ctx, fl.ID)
	if s.Status != "done" {
		t.Fatalf("%+v", s)
	}
	acc, _ := st.GetAccount(ctx, s.AccountID)
	if acc.Kind != domain.KindIMAP || acc.Incoming.Host != "outlook.office365.com" || acc.Incoming.Auth != domain.AuthOAuth2 ||
		acc.Outgoing.Host != "smtp.office365.com" {
		t.Fatalf("%+v", acc)
	}
	if _, err := a.OAuthCallback(ctx, loc.Query()); !errors.Is(err, oauth.ErrStateMismatch) {
		t.Fatal("replayed callback accepted", err)
	}
}
