// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
)

// fakeIdP is a Microsoft-like identity provider.
type fakeIdP struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	codes    map[string]string // code -> challenge
	refresh  int
	lastForm url.Values
	devPolls int
	revoked  bool
}

func idToken(claims map[string]string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func newFakeIdP(t *testing.T) *fakeIdP {
	f := &fakeIdP{t: t, codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /common/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" {
			http.Error(w, "bad", 400)
			return
		}
		f.mu.Lock()
		f.codes["code-1"] = q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=code-1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /common/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			ch := f.codes[r.PostForm.Get("code")]
			if ch == "" || Challenge(r.PostForm.Get("code_verifier")) != ch {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"PKCE mismatch"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-0", "expires_in": 3600, "refresh_token": "rt-0",
				"id_token": idToken(map[string]string{"preferred_username": "Anna@Contoso.example", "name": "Anna", "tid": "t1"})})
		case "refresh_token":
			if f.revoked {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"AADSTS700082: expired"}`)
				return
			}
			f.refresh++
			n := f.refresh
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + itoa(n) + "-" + r.PostForm.Get("scope"),
				"expires_in": 3600, "refresh_token": "rt-" + itoa(n)})
		case "urn:ietf:params:oauth:grant-type:device_code":
			f.devPolls++
			if f.devPolls < 2 {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "dev-at", "expires_in": 3600, "refresh_token": "dev-rt",
				"id_token": idToken(map[string]string{"email": "dev@contoso.example"})})
		}
	})
	mux.HandleFunc("POST /common/devicecode", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-EFGH",
			"verification_uri": "https://microsoft.com/devicelogin", "expires_in": 60, "interval": 1})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	old := Endpoints[Microsoft]
	ep := old
	ep.AuthURL = f.srv.URL + "/{tenant}/authorize"
	ep.TokenURL = f.srv.URL + "/{tenant}/token"
	ep.DeviceURL = f.srv.URL + "/{tenant}/devicecode"
	ep.LoopbackHost = "127.0.0.1"
	Endpoints[Microsoft] = ep
	t.Cleanup(func() { Endpoints[Microsoft] = old })
	return f
}

func itoa(n int) string { return string(rune('0' + n)) }

func TestPKCE(t *testing.T) {
	// RFC 7636 appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
	if v := NewVerifier(); len(v) < 43 || v == NewVerifier() {
		t.Fatal(v)
	}
}

// browserOpener follows the authorization redirect like a browser.
func browserOpener(t *testing.T) Opener {
	return func(ctx context.Context, u string) error {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
		return nil
	}
}

func TestLoopbackFlowAndRefresh(t *testing.T) {
	idp := newFakeIdP(t)
	sec := secrets.NewMemory()
	m := NewManager(Config{Clients: StaticClients{Microsoft: {ID: "cid"}}, Secrets: sec, Opener: browserOpener(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := m.StartLoopback(ctx, StartRequest{Provider: Microsoft, Kind: domain.KindGraph, LoginHint: "anna@contoso.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.AuthURL, "login_hint=anna") {
		t.Fatal(f.AuthURL)
	}
	g, err := f.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Email != "anna@contoso.example" || g.Name != "Anna" || g.RefreshToken != "rt-0" || g.Tokens[ResourceGraph].Token != "at-0" {
		t.Fatalf("%+v", g)
	}
	if s := idp.lastForm.Get("scope"); !strings.Contains(s, "graph.microsoft.com/Mail.ReadWrite") || strings.Contains(s, "outlook.office.com") {
		t.Fatal("token scope", s)
	}
	if err := m.SaveGrant("oauth:1", g); err != nil {
		t.Fatal(err)
	}
	// Cached Graph token.
	tok, err := m.TokenSource("oauth:1", ResourceGraph).Token(ctx)
	if err != nil || tok != "at-0" {
		t.Fatal(tok, err)
	}
	// Outlook resource: refresh with outlook scopes (none granted → endpoint defaults).
	tok, err = m.TokenSource("oauth:1", ResourceOutlook).Token(ctx)
	if err != nil || !strings.Contains(tok, "outlook.office.com/IMAP.AccessAsUser.All") {
		t.Fatal(tok, err)
	}
	g2, _ := m.LoadGrant("oauth:1")
	if g2.RefreshToken != "rt-1" {
		t.Fatal("rotated refresh token not stored", g2.RefreshToken)
	}
	// Expired → refresh.
	m.cfg.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	tok, err = m.TokenSource("oauth:1", ResourceGraph).Token(ctx)
	if err != nil || !strings.HasPrefix(tok, "at-2-") {
		t.Fatal(tok, err)
	}
	// Revoked → ErrReauthRequired.
	idp.revoked = true
	m.cfg.Now = func() time.Time { return time.Now().Add(4 * time.Hour) }
	if _, err := m.TokenSource("oauth:1", ResourceGraph).Token(ctx); !errors.Is(err, ErrReauthRequired) {
		t.Fatal(err)
	}
}

func TestNotConfigured(t *testing.T) {
	m := NewManager(Config{Clients: StaticClients{}})
	_, err := m.StartLoopback(context.Background(), StartRequest{Provider: Google, Kind: domain.KindGmail})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
	if _, err := m.Client(context.Background(), "yahoo"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatal(err)
	}
}

func TestRedirectFlow(t *testing.T) {
	newFakeIdP(t)
	m := NewManager(Config{Clients: StaticClients{Microsoft: {ID: "cid", Secret: "s"}}, Secrets: secrets.NewMemory(),
		RedirectBase: "https://mail.example/"})
	ctx := context.Background()
	authURL, state, err := m.StartRedirect(ctx, StartRequest{Provider: Microsoft, Kind: domain.KindIMAP}, `{"x":1}`)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("redirect_uri") != "https://mail.example/api/oauth/microsoft/callback" ||
		!strings.Contains(u.Query().Get("scope"), "IMAP.AccessAsUser.All") || strings.Contains(u.Query().Get("scope"), "EAS.") {
		t.Fatal(authURL)
	}
	// Simulate the browser: authorize redirects with a code.
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	g, pf, err := m.CompleteRedirect(ctx, loc.Query())
	if err != nil || pf.Data != `{"x":1}` || g.RefreshToken != "rt-0" || pf.State != state {
		t.Fatal(g, pf, err)
	}
	if _, _, err := m.CompleteRedirect(ctx, loc.Query()); !errors.Is(err, ErrStateMismatch) {
		t.Fatal("replay accepted", err)
	}
	// Denied consent.
	_, state, _ = m.StartRedirect(ctx, StartRequest{Provider: Microsoft, Kind: domain.KindIMAP}, "")
	_, _, err = m.CompleteRedirect(ctx, url.Values{"state": {state}, "error": {"access_denied"},
		"error_description": {"AADSTS65004: User declined"}})
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatal(err)
	}
}

func TestDeviceFlow(t *testing.T) {
	newFakeIdP(t)
	old := deviceInterval
	deviceInterval = func(time.Duration) time.Duration { return 10 * time.Millisecond }
	defer func() { deviceInterval = old }()
	m := NewManager(Config{Clients: StaticClients{Microsoft: {ID: "cid"}}, Secrets: secrets.NewMemory()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, err := m.StartDevice(ctx, StartRequest{Provider: Microsoft, Kind: domain.KindEAS})
	if err != nil {
		t.Fatal(err)
	}
	if f.UserCode != "ABCD-EFGH" || f.VerificationURI == "" {
		t.Fatal(f)
	}
	g, err := f.Wait(ctx)
	if err != nil || g.Email != "dev@contoso.example" || g.Tokens[ResourceOutlook].Token != "dev-at" {
		t.Fatal(g, err)
	}
	gm := NewManager(Config{Clients: StaticClients{Google: {ID: "g"}}})
	if _, err := gm.StartDevice(ctx, StartRequest{Provider: Google}); !errors.Is(err, ErrDeviceUnsupported) {
		t.Fatal(err)
	}
}

func TestClientsFromEnv(t *testing.T) {
	env := map[string]string{EnvMSClientID: "ms", EnvMSTenant: "organizations", EnvGoogleClientID: "g", EnvGoogleClientSecret: "gs"}
	c := ClientsFromEnv(func(k string) string { return env[k] })
	if c[Microsoft].ID != "ms" || c[Microsoft].Tenant != "organizations" || c[Google].Secret != "gs" || c[Google].Source != "env" {
		t.Fatal(c)
	}
	m := c.Merge(Clients{Google: {ID: "own", Source: "user"}, Microsoft: {}})
	if m[Google].ID != "own" || m[Microsoft].ID != "ms" {
		t.Fatal(m)
	}
	if ResourceForKind(Microsoft, domain.KindIMAP) != ResourceOutlook || ResourceForKind(Google, domain.KindIMAP) != ResourceGoogle {
		t.Fatal("resource mapping")
	}
}
