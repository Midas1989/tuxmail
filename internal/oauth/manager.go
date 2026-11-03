// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Opener opens a URL in the system browser.
type Opener func(ctx context.Context, url string) error

// Config configures a Manager.
type Config struct {
	// Clients resolves the client registrations (required).
	Clients ClientResolver
	// Secrets stores the grants (required for TokenSource/SaveGrant).
	Secrets domain.SecretStore
	// HTTPClient for the token endpoints (default 30 s timeout).
	HTTPClient *http.Client
	// Opener opens the browser for the loopback flow (default OpenBrowser).
	Opener Opener
	// RedirectBase is the public URL of the server (server mode web
	// redirect: RedirectBase + "/api/oauth/{provider}/callback").
	RedirectBase string
	// Pending keeps web-redirect flows between start and callback
	// (default: in memory).
	Pending PendingStore
	// LoopbackTimeout bounds how long the loopback listener waits (default 10 min).
	LoopbackTimeout time.Duration
	Now             func() time.Time
	Logger          *slog.Logger
}

// Manager runs sign-in flows and hands out token sources. It is safe for
// concurrent use.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	sources map[string]*TokenSource // ref + "|" + resource
	locks   map[string]*sync.Mutex  // per grant ref: serializes refreshes
}

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Opener == nil {
		cfg.Opener = OpenBrowser
	}
	if cfg.Pending == nil {
		cfg.Pending = NewMemoryPending()
	}
	if cfg.LoopbackTimeout <= 0 {
		cfg.LoopbackTimeout = 10 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Manager{cfg: cfg, sources: map[string]*TokenSource{}, locks: map[string]*sync.Mutex{}}
}

// SetOpener replaces the browser opener (the Wails shell sets its own).
func (m *Manager) SetOpener(o Opener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Opener = o
}

func (m *Manager) opener() Opener {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Opener
}

// Client returns the client registration of a provider or ErrNotConfigured.
func (m *Manager) Client(ctx context.Context, provider string) (Client, error) {
	if _, ok := Endpoints[provider]; !ok {
		return Client{}, ErrUnknownProvider
	}
	if m.cfg.Clients == nil {
		return Client{}, ErrNotConfigured
	}
	c, err := m.cfg.Clients.Client(ctx, provider)
	if err != nil {
		return Client{}, err
	}
	if !c.Configured() {
		return Client{}, fmt.Errorf("%w (%s)", ErrNotConfigured, provider)
	}
	return c, nil
}

// StartRequest describes a sign-in.
type StartRequest struct {
	Provider string
	// Kind is the access method of the account to create (imap, graph,
	// gmail, eas, ews); it selects the scopes.
	Kind domain.AccountKind
	// LoginHint pre-fills the account picker (the e-mail address).
	LoginHint string
	// Tenant overrides the client's Microsoft authority.
	Tenant string
	// Scopes overrides the computed scopes.
	Scopes []string
}

// prepared is a started authorization request.
type prepared struct {
	ep       Endpoint
	client   Client
	tenant   string
	scopes   []string
	verifier string
	state    string
	redirect string
}

func (m *Manager) prepare(ctx context.Context, req StartRequest) (*prepared, error) {
	client, err := m.Client(ctx, req.Provider)
	if err != nil {
		return nil, err
	}
	tenant := req.Tenant
	if tenant == "" {
		tenant = client.Tenant
	}
	ep, err := endpoint(req.Provider, tenant)
	if err != nil {
		return nil, err
	}
	if tenant == "" {
		tenant = ep.DefaultTenant
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = Scopes(req.Provider, req.Kind)
	}
	return &prepared{ep: ep, client: client, tenant: tenant, scopes: scopes, verifier: NewVerifier(), state: randomString(18)}, nil
}

// Scopes returns the scopes requested for an account kind.
func Scopes(provider string, kind domain.AccountKind) []string {
	ep := Endpoints[provider]
	if provider == Microsoft && kind != domain.KindGraph {
		return append(append([]string{}, ep.Base...), MicrosoftOutlookScopesFor(kind)...)
	}
	return ep.ScopesFor(ResourcesForKind(provider, kind)...)
}

func (p *prepared) authURL(loginHint string) string {
	q := url.Values{
		"client_id":             {p.client.ID},
		"response_type":         {"code"},
		"redirect_uri":          {p.redirect},
		"scope":                 {strings.Join(p.scopes, " ")},
		"state":                 {p.state},
		"code_challenge":        {Challenge(p.verifier)},
		"code_challenge_method": {"S256"},
	}
	for k, v := range p.ep.AuthParams {
		q.Set(k, v)
	}
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	sep := "?"
	if strings.Contains(p.ep.AuthURL, "?") {
		sep = "&"
	}
	return p.ep.AuthURL + sep + q.Encode()
}

// exchange redeems an authorization code.
func (m *Manager) exchange(ctx context.Context, p *prepared, code string) (Grant, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.redirect},
		"client_id":     {p.client.ID},
		"code_verifier": {p.verifier},
	}
	if p.client.Secret != "" {
		form.Set("client_secret", p.client.Secret)
	}
	res := primaryResource(p.ep, p.scopes)
	if p.ep.MultiResource {
		// The token request must name scopes of a single resource.
		form.Set("scope", refreshScopes(p.ep, Grant{Scopes: p.scopes}, res)+" openid email profile")
	}
	var tr tokenResponse
	if err := postForm(ctx, m.cfg.HTTPClient, p.ep.TokenURL, form, &tr); err != nil {
		return Grant{}, err
	}
	return m.grantFrom(p, res, tr)
}

func (m *Manager) grantFrom(p *prepared, resource string, tr tokenResponse) (Grant, error) {
	if tr.RefreshToken == "" {
		return Grant{}, errors.New("oauth: the provider returned no refresh token (offline access missing)")
	}
	now := m.cfg.Now()
	g := Grant{Provider: p.ep.ID, Tenant: p.tenant, ClientID: p.client.ID, Scopes: p.scopes, IssuedAt: now}
	g.applyIdentity(tr)
	g.storeToken(resource, tr, now)
	return g, nil
}

// ==== web redirect (server mode) =============================================

// PendingFlow is a started web-redirect flow.
type PendingFlow struct {
	State     string    `json:"state"`
	Provider  string    `json:"provider"`
	Tenant    string    `json:"tenant,omitempty"`
	ClientID  string    `json:"clientId"`
	Scopes    []string  `json:"scopes"`
	Verifier  string    `json:"verifier"`
	Redirect  string    `json:"redirect"`
	Created   time.Time `json:"created"`
	LoginHint string    `json:"loginHint,omitempty"`
	// Data is opaque caller data (e.g. the account to create).
	Data string `json:"data,omitempty"`
}

// PendingStore keeps pending web-redirect flows (Take removes the entry).
type PendingStore interface {
	PutPending(ctx context.Context, p PendingFlow) error
	TakePending(ctx context.Context, state string) (PendingFlow, error)
}

// pendingTTL bounds how long a web-redirect flow may take.
const pendingTTL = 15 * time.Minute

// CallbackPath returns the web redirect path of a provider.
func CallbackPath(provider string) string { return "/api/oauth/" + provider + "/callback" }

// StartRedirect starts a web-redirect flow and returns the authorization
// URL; the browser comes back to RedirectBase + CallbackPath(provider).
func (m *Manager) StartRedirect(ctx context.Context, req StartRequest, data string) (authURL, state string, err error) {
	if m.cfg.RedirectBase == "" {
		return "", "", errors.New("oauth: no public URL for the web redirect configured")
	}
	p, err := m.prepare(ctx, req)
	if err != nil {
		return "", "", err
	}
	p.redirect = strings.TrimRight(m.cfg.RedirectBase, "/") + CallbackPath(req.Provider)
	pf := PendingFlow{State: p.state, Provider: req.Provider, Tenant: p.tenant, ClientID: p.client.ID, Scopes: p.scopes,
		Verifier: p.verifier, Redirect: p.redirect, Created: m.cfg.Now(), LoginHint: req.LoginHint, Data: data}
	if err := m.cfg.Pending.PutPending(ctx, pf); err != nil {
		return "", "", err
	}
	return p.authURL(req.LoginHint), p.state, nil
}

// CompleteRedirect handles the callback query of a web-redirect flow.
func (m *Manager) CompleteRedirect(ctx context.Context, query url.Values) (Grant, PendingFlow, error) {
	pf, err := m.cfg.Pending.TakePending(ctx, query.Get("state"))
	if err != nil {
		return Grant{}, PendingFlow{}, ErrStateMismatch
	}
	if m.cfg.Now().Sub(pf.Created) > pendingTTL {
		return Grant{}, pf, ErrFlowExpired
	}
	if e := query.Get("error"); e != "" {
		return Grant{}, pf, &Error{Code: e, Description: query.Get("error_description")}
	}
	client, err := m.Client(ctx, pf.Provider)
	if err != nil {
		return Grant{}, pf, err
	}
	if client.ID != pf.ClientID {
		return Grant{}, pf, ErrClientChanged
	}
	ep, err := endpoint(pf.Provider, pf.Tenant)
	if err != nil {
		return Grant{}, pf, err
	}
	p := &prepared{ep: ep, client: client, tenant: pf.Tenant, scopes: pf.Scopes, verifier: pf.Verifier,
		state: pf.State, redirect: pf.Redirect}
	g, err := m.exchange(ctx, p, query.Get("code"))
	return g, pf, err
}

// MemoryPending is an in-memory PendingStore.
type MemoryPending struct {
	mu sync.Mutex
	m  map[string]PendingFlow
}

// NewMemoryPending returns an empty MemoryPending.
func NewMemoryPending() *MemoryPending { return &MemoryPending{m: map[string]PendingFlow{}} }

// PutPending implements PendingStore.
func (s *MemoryPending) PutPending(_ context.Context, p PendingFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.m {
		if time.Since(v.Created) > pendingTTL {
			delete(s.m, k)
		}
	}
	s.m[p.State] = p
	return nil
}

// TakePending implements PendingStore.
func (s *MemoryPending) TakePending(_ context.Context, state string) (PendingFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[state]
	if !ok || state == "" {
		return PendingFlow{}, ErrStateMismatch
	}
	delete(s.m, state)
	return p, nil
}

// ==== grants & token sources =================================================

// SaveGrant stores a grant in the secret store.
func (m *Manager) SaveGrant(ref string, g Grant) error {
	if m.cfg.Secrets == nil {
		return errors.New("oauth: no secret store")
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	m.mu.Lock()
	for k, ts := range m.sources {
		if strings.HasPrefix(k, ref+"|") {
			ts.reset()
		}
	}
	m.mu.Unlock()
	return m.cfg.Secrets.Set(ref, b)
}

// LoadGrant reads a grant from the secret store.
func (m *Manager) LoadGrant(ref string) (Grant, error) {
	if m.cfg.Secrets == nil {
		return Grant{}, errors.New("oauth: no secret store")
	}
	b, err := m.cfg.Secrets.Get(ref)
	if err != nil {
		return Grant{}, fmt.Errorf("%w: %v", ErrReauthRequired, err)
	}
	var g Grant
	if err := json.Unmarshal(b, &g); err != nil || g.RefreshToken == "" {
		return Grant{}, fmt.Errorf("%w: stored grant is invalid", ErrReauthRequired)
	}
	return g, nil
}

// TokenSource returns the (shared) token source of a grant for resource.
func (m *Manager) TokenSource(ref, resource string) *TokenSource {
	key := ref + "|" + resource
	m.mu.Lock()
	defer m.mu.Unlock()
	if ts, ok := m.sources[key]; ok {
		return ts
	}
	l, ok := m.locks[ref]
	if !ok {
		l = &sync.Mutex{}
		m.locks[ref] = l
	}
	ts := &TokenSource{m: m, ref: ref, resource: resource, lock: l}
	m.sources[key] = ts
	return ts
}

// ForAccount returns the token source an account of acc.Kind needs.
func (m *Manager) ForAccount(acc domain.Account) (*TokenSource, error) {
	ref := SecretRef(acc)
	if ref == "" {
		return nil, fmt.Errorf("%w: account %d has no OAuth grant", ErrReauthRequired, acc.ID)
	}
	p := ProviderOf(acc)
	if p == "" {
		g, err := m.LoadGrant(ref)
		if err != nil {
			return nil, err
		}
		p = g.Provider
	}
	return m.TokenSource(ref, ResourceForKind(p, acc.Kind)), nil
}

// TokenSource returns valid access tokens of one resource and refreshes
// them with the stored refresh token. It implements the token source
// interfaces of internal/sync, internal/providers/imap and smtp.
type TokenSource struct {
	m        *Manager
	ref      string
	resource string
	lock     *sync.Mutex // shared by all resources of the grant

	mu    sync.Mutex
	cache AccessToken
}

func (t *TokenSource) reset() {
	t.mu.Lock()
	t.cache = AccessToken{}
	t.mu.Unlock()
}

// Token implements the TokenSource interfaces.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	now := t.m.cfg.Now()
	t.mu.Lock()
	c := t.cache
	t.mu.Unlock()
	if c.Valid(now) {
		return c.Token, nil
	}
	t.lock.Lock()
	defer t.lock.Unlock()
	g, err := t.m.LoadGrant(t.ref)
	if err != nil {
		return "", err
	}
	if c := g.Tokens[t.resource]; c.Valid(now) {
		t.set(c)
		return c.Token, nil
	}
	if err := t.m.refresh(ctx, &g, t.resource); err != nil {
		return "", err
	}
	if err := t.m.saveGrantLocked(t.ref, g); err != nil {
		t.m.cfg.Logger.Warn("oauth: storing refreshed grant failed", "err", err)
	}
	c = g.Tokens[t.resource]
	t.set(c)
	return c.Token, nil
}

// Invalidate drops the cached token (after a 401 from the API).
func (t *TokenSource) Invalidate() {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.reset()
	if g, err := t.m.LoadGrant(t.ref); err == nil && g.Tokens != nil {
		delete(g.Tokens, t.resource)
		_ = t.m.saveGrantLocked(t.ref, g)
	}
}

func (t *TokenSource) set(c AccessToken) {
	t.mu.Lock()
	t.cache = c
	t.mu.Unlock()
}

func (m *Manager) saveGrantLocked(ref string, g Grant) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return m.cfg.Secrets.Set(ref, b)
}

// refresh obtains a new access token of resource for g.
func (m *Manager) refresh(ctx context.Context, g *Grant, resource string) error {
	client, err := m.Client(ctx, g.Provider)
	if err != nil {
		return err
	}
	if client.ID != g.ClientID {
		return ErrClientChanged
	}
	ep, err := endpoint(g.Provider, g.Tenant)
	if err != nil {
		return err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {g.RefreshToken}, "client_id": {client.ID}}
	if client.Secret != "" {
		form.Set("client_secret", client.Secret)
	}
	if s := refreshScopes(ep, *g, resource); s != "" {
		form.Set("scope", s)
	}
	var tr tokenResponse
	if err := postForm(ctx, m.cfg.HTTPClient, ep.TokenURL, form, &tr); err != nil {
		return err
	}
	if tr.AccessToken == "" {
		return errors.New("oauth: token endpoint returned no access token")
	}
	g.storeToken(resource, tr, m.cfg.Now())
	return nil
}

// Revoke deletes a grant (Google: also revokes it at the provider).
func (m *Manager) Revoke(ctx context.Context, ref string) error {
	g, err := m.LoadGrant(ref)
	if err == nil && g.Provider == Google && revokeURL != "" {
		form := url.Values{"token": {g.RefreshToken}}
		var ignored map[string]any
		_ = postForm(ctx, m.cfg.HTTPClient, revokeURL, form, &ignored)
	}
	m.mu.Lock()
	for k := range m.sources {
		if strings.HasPrefix(k, ref+"|") {
			delete(m.sources, k)
		}
	}
	m.mu.Unlock()
	if m.cfg.Secrets == nil {
		return nil
	}
	return m.cfg.Secrets.Delete(ref)
}

// revokeURL is Google's token revocation endpoint (tests clear it).
var revokeURL = "https://oauth2.googleapis.com/revoke"
