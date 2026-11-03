// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/providers/ews"
	"github.com/Midas1989/Mailclient/internal/providers/gmail"
	"github.com/Midas1989/Mailclient/internal/providers/google"
	"github.com/Midas1989/Mailclient/internal/providers/graph"
	"github.com/Midas1989/Mailclient/internal/providers/pimapi"
	"github.com/Midas1989/Mailclient/internal/providers/smtp"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// OAuth sign-in and cloud accounts (Microsoft 365 / Google, KONZEPT 7.3–7.5):
// account wizard flows, own client IDs ("Anmeldedienste"), shared mailboxes
// and identity delegation. Wiring: newOAuthState in New, configureOAuthSync
// and configureOAuthSend before the sync manager and the send service are
// created.

// OAuthOptions configure the OAuth sign-in of an App.
type OAuthOptions struct {
	// Clients are the admin (server configuration) or environment client
	// registrations. nil = oauth.ClientsFromEnv(nil).
	Clients oauth.Clients
	// DenyUserClients forbids own client IDs of the user (server policy).
	DenyUserClients bool
	// RedirectBase is the public server URL for the web redirect (server mode).
	RedirectBase string
	// Opener opens the system browser (desktop; default xdg-open & co.).
	Opener oauth.Opener
	// Endpoint overrides (tests): base URLs of Graph and Gmail stored in
	// the account extras of new accounts.
	GraphBaseURL, GmailBaseURL string
}

// OAuth error codes (ApiError.code).
const (
	CodeOAuthNotConfigured = "oauth_not_configured"
	CodeOAuthConsent       = "oauth_consent_required"
	CodeOAuthDenied        = "oauth_denied"
	CodeOAuthExpired       = "oauth_reauth_required"
	CodeOAuthUnsupported   = "oauth_unsupported"
)

func oauthError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, oauth.ErrNotConfigured):
		return &Error{Code: CodeOAuthNotConfigured, Status: http.StatusConflict, Err: err,
			Message: "no OAuth client ID is configured for this provider (Settings → Sign-in services)"}
	case errors.Is(err, oauth.ErrConsentRequired):
		return &Error{Code: CodeOAuthConsent, Status: http.StatusForbidden, Err: err, Message: err.Error()}
	case errors.Is(err, oauth.ErrAccessDenied):
		return &Error{Code: CodeOAuthDenied, Status: http.StatusForbidden, Err: err, Message: err.Error()}
	case errors.Is(err, oauth.ErrReauthRequired), errors.Is(err, oauth.ErrClientChanged), errors.Is(err, oauth.ErrFlowExpired),
		errors.Is(err, oauth.ErrStateMismatch):
		return &Error{Code: CodeOAuthExpired, Status: http.StatusUnauthorized, Err: err, Message: err.Error()}
	case errors.Is(err, oauth.ErrDeviceUnsupported), errors.Is(err, oauth.ErrUnknownProvider):
		return &Error{Code: CodeOAuthUnsupported, Status: http.StatusBadRequest, Err: err, Message: err.Error()}
	}
	return err
}

type oauthState struct {
	a   *App
	mgr *oauth.Manager
	opt OAuthOptions

	mu    sync.Mutex
	flows map[string]*oauthFlow
}

type oauthFlow struct {
	status OAuthFlowStatus
	in     OAuthStartInput
	flow   *oauth.Flow
}

func newOAuthState(a *App) *oauthState {
	opt := OAuthOptions{}
	if a.opts.OAuth != nil {
		opt = *a.opts.OAuth
	}
	if opt.Clients == nil {
		opt.Clients = oauth.ClientsFromEnv(nil)
	}
	s := &oauthState{a: a, opt: opt, flows: map[string]*oauthFlow{}}
	s.mgr = oauth.NewManager(oauth.Config{Clients: s, Secrets: a.opts.Secrets, HTTPClient: a.opts.HTTPClient, Opener: opt.Opener,
		RedirectBase: opt.RedirectBase, Pending: storePending{st: a.st}, Now: a.opts.Now, Logger: a.log})
	return s
}

// Client implements oauth.ClientResolver: user override → admin/env.
func (s *oauthState) Client(ctx context.Context, provider string) (oauth.Client, error) {
	if !s.opt.DenyUserClients {
		cs, err := s.a.st.ListOAuthClients(ctx)
		if err != nil {
			return oauth.Client{}, err
		}
		for _, c := range cs {
			if c.Provider == provider {
				cl := oauth.Client{ID: c.ClientID, Tenant: c.Tenant, Source: "user"}
				if c.SecretRef != "" {
					if b, err := s.a.opts.Secrets.Get(c.SecretRef); err == nil {
						cl.Secret = string(b)
					}
				}
				return cl, nil
			}
		}
	}
	return s.opt.Clients[provider], nil
}

// storePending persists web-redirect flows in the user's store.
type storePending struct{ st *store.Store }

func (p storePending) PutPending(ctx context.Context, f oauth.PendingFlow) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return p.st.PutOAuthPending(ctx, f.State, f.Provider, string(b), 15*time.Minute)
}

func (p storePending) TakePending(ctx context.Context, state string) (oauth.PendingFlow, error) {
	js, err := p.st.TakeOAuthPending(ctx, state)
	if err != nil {
		return oauth.PendingFlow{}, oauth.ErrStateMismatch
	}
	var f oauth.PendingFlow
	err = json.Unmarshal([]byte(js), &f)
	return f, err
}

// OAuthManager returns the OAuth manager of the App (token sources).
func (a *App) OAuthManager() *oauth.Manager { return a.oauth.mgr }

// SetOAuthOpener replaces the browser opener (the Wails shell passes
// runtime.BrowserOpenURL).
func (a *App) SetOAuthOpener(o oauth.Opener) { a.oauth.mgr.SetOpener(o) }

func (a *App) oauthTokens() send.TokenFunc {
	return func(_ context.Context, acc domain.Account) (smtp.TokenSource, error) {
		ts, err := a.oauth.mgr.ForAccount(acc)
		if err != nil {
			return nil, oauthError(err)
		}
		return ts, nil
	}
}

// configureOAuthSync makes the sync manager use the OAuth manager.
func (a *App) configureOAuthSync(cfg *tsync.Config) {
	cfg.Credentials = tsync.SecretCredentials{Secrets: a.opts.Secrets, OAuth: tsync.OAuthAdapter{M: a.oauth.mgr}}
}

// configureOAuthSend routes Graph/Gmail/EWS accounts to their API
// transports and gives SMTP its XOAUTH2 token source.
func (a *App) configureOAuthSend(cfg *send.Config) {
	tokens := a.oauthTokens()
	def := send.SMTPFactory{Secrets: a.opts.Secrets, TokenSource: tokens}
	cfg.Transports = send.KindRouter{Default: def, Kinds: send.CloudFactories(tokens, a.opts.Secrets)}
}

// ==== API types ================================================================

// OAuthProviderInfo describes a sign-in service for the settings page.
type OAuthProviderInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Configured  bool   `json:"configured"`
	// Source of the active client: env | admin | user | builtin | "".
	Source   string `json:"source,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
	// UserClient is the user's own client (if any).
	UserClient *OAuthClientInput `json:"userClient,omitempty"`
	// AllowUserClients is false when the server admin forbids own clients.
	AllowUserClients bool     `json:"allowUserClients"`
	DeviceFlow       bool     `json:"deviceFlow"`
	Kinds            []string `json:"kinds"`
	RedirectURI      string   `json:"redirectUri,omitempty"`
}

// OAuthClientInput is an own client registration.
type OAuthClientInput struct {
	Provider     string `json:"provider"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"` // write only; never returned
	HasSecret    bool   `json:"hasSecret,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
}

// OAuthStartInput starts a sign-in from the account wizard.
type OAuthStartInput struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	// Kind is the access method: graph | imap | eas | ews (Microsoft),
	// gmail | imap (Google).
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName,omitempty"`
	Name        string `json:"name,omitempty"`
	Tenant      string `json:"tenant,omitempty"`
	// Mode: loopback (desktop default), redirect (server default), device.
	Mode string `json:"mode,omitempty"`
	// AccountID re-authenticates an existing account instead of creating one.
	AccountID int64 `json:"accountId,omitempty"`
}

// OAuthFlowStatus is the state of a running sign-in.
type OAuthFlowStatus struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Mode            string `json:"mode"`
	Status          string `json:"status"` // pending | done | error | canceled
	AuthURL         string `json:"authUrl,omitempty"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	AccountID       int64  `json:"accountId,omitempty"`
	Email           string `json:"email,omitempty"`
	Error           string `json:"error,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
}

// SharedMailboxInput adds a shared/delegated mailbox (KONZEPT 7.3a).
type SharedMailboxInput struct {
	AccountID int64  `json:"accountId"`
	Address   string `json:"address"`
	// SendMode: send_as | send_on_behalf | "" (read only, no identity).
	SendMode    string `json:"sendMode,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

var providerKinds = map[string][]string{
	oauth.Microsoft: {string(domain.KindGraph), string(domain.KindIMAP), string(domain.KindEAS), string(domain.KindEWS)},
	oauth.Google:    {string(domain.KindGmail), string(domain.KindIMAP)},
}

// ==== providers & own clients ================================================

// OAuthProviders lists the sign-in services and their configuration.
func (a *App) OAuthProviders(ctx context.Context) ([]OAuthProviderInfo, error) {
	user, err := a.st.ListOAuthClients(ctx)
	if err != nil {
		return nil, err
	}
	var out []OAuthProviderInfo
	for _, id := range []string{oauth.Microsoft, oauth.Google} {
		ep := oauth.Endpoints[id]
		c, err := a.oauth.Client(ctx, id)
		if err != nil {
			return nil, err
		}
		info := OAuthProviderInfo{ID: id, DisplayName: ep.DisplayName, Configured: c.Configured(), Source: c.Source, ClientID: c.ID,
			Tenant: c.Tenant, AllowUserClients: !a.oauth.opt.DenyUserClients, DeviceFlow: ep.DeviceURL != "", Kinds: providerKinds[id]}
		if a.oauth.opt.RedirectBase != "" {
			info.RedirectURI = strings.TrimRight(a.oauth.opt.RedirectBase, "/") + oauth.CallbackPath(id)
		}
		for _, u := range user {
			if u.Provider == id {
				info.UserClient = &OAuthClientInput{Provider: id, ClientID: u.ClientID, Tenant: u.Tenant, HasSecret: u.SecretRef != ""}
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func oauthClientSecretRef(provider string) string { return "oauth-client:" + provider }

// SaveOAuthClient stores an own client registration of the user.
func (a *App) SaveOAuthClient(ctx context.Context, in OAuthClientInput) error {
	if a.oauth.opt.DenyUserClients {
		return Errorf(http.StatusForbidden, CodeReadOnly, "own client IDs are disabled by the administrator")
	}
	if _, ok := oauth.Endpoints[in.Provider]; !ok {
		return invalid("unknown provider %q", in.Provider)
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	if in.ClientID == "" {
		return invalid("client ID required")
	}
	c := store.OAuthClient{Provider: in.Provider, ClientID: in.ClientID, Tenant: strings.TrimSpace(in.Tenant)}
	ref := oauthClientSecretRef(in.Provider)
	switch {
	case in.ClientSecret != "":
		if err := a.opts.Secrets.Set(ref, []byte(in.ClientSecret)); err != nil {
			return err
		}
		c.SecretRef = ref
	case in.HasSecret:
		c.SecretRef = ref // keep the stored secret
	default:
		_ = a.opts.Secrets.Delete(ref)
	}
	return a.st.PutOAuthClient(ctx, c)
}

// DeleteOAuthClient removes an own client registration.
func (a *App) DeleteOAuthClient(ctx context.Context, provider string) error {
	_ = a.opts.Secrets.Delete(oauthClientSecretRef(provider))
	return a.st.DeleteOAuthClient(ctx, provider)
}

// ==== flows ===================================================================

func (o *oauthState) put(f *oauthFlow) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, x := range o.flows {
		if x.status.Status != "pending" && len(o.flows) > 20 {
			delete(o.flows, id)
		}
	}
	o.flows[f.status.ID] = f
}

func (o *oauthState) update(id string, fn func(*OAuthFlowStatus)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if f, ok := o.flows[id]; ok {
		fn(&f.status)
	}
}

// StartOAuth starts a sign-in. Desktop: the browser opens and the flow
// finishes in the background; poll OAuthFlow. Server: open AuthURL in the
// browser; the callback finishes the flow. On success the account is
// created (or its grant replaced for AccountID).
func (a *App) StartOAuth(ctx context.Context, in OAuthStartInput) (OAuthFlowStatus, error) {
	if _, ok := oauth.Endpoints[in.Provider]; !ok {
		return OAuthFlowStatus{}, invalid("unknown provider %q", in.Provider)
	}
	if in.Kind == "" {
		in.Kind = providerKinds[in.Provider][0]
	}
	ok := false
	for _, k := range providerKinds[in.Provider] {
		ok = ok || k == in.Kind
	}
	if !ok {
		return OAuthFlowStatus{}, invalid("access method %q is not available for %s", in.Kind, in.Provider)
	}
	if in.Mode == "" {
		in.Mode = "loopback"
		if a.opts.Mode == ModeServer {
			in.Mode = "redirect"
			if a.oauth.opt.RedirectBase == "" && oauth.Endpoints[in.Provider].DeviceURL != "" {
				in.Mode = "device"
			}
		}
	}
	req := oauth.StartRequest{Provider: in.Provider, Kind: domain.AccountKind(in.Kind), LoginHint: in.Email, Tenant: in.Tenant}
	f := &oauthFlow{in: in, status: OAuthFlowStatus{ID: newID("oa_"), Provider: in.Provider, Mode: in.Mode, Status: "pending", Email: in.Email}}
	switch in.Mode {
	case "redirect":
		data, _ := json.Marshal(struct {
			Flow string          `json:"flow"`
			In   OAuthStartInput `json:"in"`
		}{f.status.ID, in})
		u, _, err := a.oauth.mgr.StartRedirect(ctx, req, string(data))
		if err != nil {
			return OAuthFlowStatus{}, oauthError(err)
		}
		f.status.AuthURL = u
		f.status.ExpiresAt = rfc3339(a.now().Add(15 * time.Minute))
		a.oauth.put(f)
		return f.status, nil
	case "loopback", "device":
		var fl *oauth.Flow
		var err error
		if in.Mode == "device" {
			fl, err = a.oauth.mgr.StartDevice(ctx, req)
		} else {
			fl, err = a.oauth.mgr.StartLoopback(ctx, req)
		}
		if err != nil {
			return OAuthFlowStatus{}, oauthError(err)
		}
		f.flow = fl
		f.status.AuthURL, f.status.UserCode, f.status.VerificationURI = fl.AuthURL, fl.UserCode, fl.VerificationURI
		f.status.ExpiresAt = rfc3339(fl.ExpiresAt)
		a.oauth.put(f)
		go func() {
			<-fl.Done()
			g, err := fl.Result()
			a.finishOAuth(context.Background(), f.status.ID, in, g, err)
		}()
		return f.status, nil
	}
	return OAuthFlowStatus{}, invalid("unknown sign-in mode %q", in.Mode)
}

// finishOAuth stores the grant and creates/updates the account.
func (a *App) finishOAuth(ctx context.Context, flowID string, in OAuthStartInput, g oauth.Grant, err error) (int64, error) {
	var accID int64
	if err == nil {
		accID, err = a.accountFromGrant(ctx, in, g)
	}
	a.oauth.update(flowID, func(s *OAuthFlowStatus) {
		if err != nil {
			s.Status, s.Error = "error", err.Error()
			if errors.Is(err, context.Canceled) {
				s.Status = "canceled"
			}
			s.ErrorCode = AsError(oauthError(err)).Code
			return
		}
		s.Status, s.AccountID, s.Email = "done", accID, g.Email
	})
	if err != nil {
		a.log.Info("app: OAuth sign-in failed", "provider", in.Provider, "err", err)
	}
	return accID, err
}

// OAuthFlow returns the state of a sign-in.
func (a *App) OAuthFlow(_ context.Context, id string) (OAuthFlowStatus, error) {
	a.oauth.mu.Lock()
	defer a.oauth.mu.Unlock()
	f, ok := a.oauth.flows[id]
	if !ok {
		return OAuthFlowStatus{}, errNotFound
	}
	return f.status, nil
}

// CancelOAuth aborts a running sign-in.
func (a *App) CancelOAuth(_ context.Context, id string) error {
	a.oauth.mu.Lock()
	f, ok := a.oauth.flows[id]
	a.oauth.mu.Unlock()
	if !ok {
		return errNotFound
	}
	if f.flow != nil {
		f.flow.Cancel()
	}
	a.oauth.update(id, func(s *OAuthFlowStatus) {
		if s.Status == "pending" {
			s.Status = "canceled"
		}
	})
	return nil
}

// OAuthCallback finishes a web-redirect flow (server mode) and returns the
// HTML page shown in the browser.
func (a *App) OAuthCallback(ctx context.Context, query url.Values) (string, error) {
	g, pf, err := a.oauth.mgr.CompleteRedirect(ctx, query)
	var data struct {
		Flow string          `json:"flow"`
		In   OAuthStartInput `json:"in"`
	}
	_ = json.Unmarshal([]byte(pf.Data), &data)
	if data.Flow == "" && err == nil {
		err = oauth.ErrStateMismatch
	}
	if data.Flow != "" {
		if _, ferr := a.OAuthFlow(ctx, data.Flow); ferr != nil {
			// The App was restarted between start and callback.
			a.oauth.put(&oauthFlow{in: data.In, status: OAuthFlowStatus{ID: data.Flow, Provider: pf.Provider, Mode: "redirect", Status: "pending"}})
		}
		_, err = a.finishOAuth(ctx, data.Flow, data.In, g, err)
	}
	if err != nil {
		return oauth.CallbackHTML(false, AsError(oauthError(err)).Error()), oauthError(err)
	}
	return oauth.CallbackHTML(true, ""), nil
}

// ==== accounts ================================================================

func oauthSecretRef(id int64) string { return fmt.Sprintf("oauth:%d", id) }

// accountTemplate returns servers and extras of an OAuth account.
func (a *App) accountTemplate(provider string, kind domain.AccountKind, email string) (in, out *ServerConfig, extra map[string]string) {
	extra = map[string]string{oauth.ExtraProvider: provider, "address": email}
	oa := domain.AuthOAuth2
	switch {
	case provider == oauth.Microsoft && kind == domain.KindIMAP:
		in = &ServerConfig{Host: "outlook.office365.com", Port: 993, TLS: domain.TLSImplicit, Auth: oa, Username: email}
		out = &ServerConfig{Host: "smtp.office365.com", Port: 587, TLS: domain.TLSStartTLS, Auth: oa, Username: email}
	case provider == oauth.Microsoft && kind == domain.KindEAS:
		in = &ServerConfig{Host: "outlook.office365.com", Port: 443, TLS: domain.TLSImplicit, Auth: oa, Username: email}
		extra["easUrl"] = "https://outlook.office365.com/Microsoft-Server-ActiveSync"
	case provider == oauth.Microsoft && kind == domain.KindEWS:
		in = &ServerConfig{Host: "outlook.office365.com", Port: 443, TLS: domain.TLSImplicit, Auth: oa, Username: email}
		extra[tsync.ExtraEWSURL], extra[tsync.ExtraEWSAuth] = "https://outlook.office365.com/EWS/Exchange.asmx", ews.AuthBearer
	case provider == oauth.Google && kind == domain.KindIMAP:
		in = &ServerConfig{Host: "imap.gmail.com", Port: 993, TLS: domain.TLSImplicit, Auth: oa, Username: email}
		out = &ServerConfig{Host: "smtp.gmail.com", Port: 465, TLS: domain.TLSImplicit, Auth: oa, Username: email}
	case kind == domain.KindGraph && a.oauth.opt.GraphBaseURL != "":
		extra[tsync.ExtraGraphBaseURL] = a.oauth.opt.GraphBaseURL
	case kind == domain.KindGmail && a.oauth.opt.GmailBaseURL != "":
		extra[tsync.ExtraGmailBaseURL] = a.oauth.opt.GmailBaseURL
	}
	return in, out, extra
}

func (a *App) accountFromGrant(ctx context.Context, in OAuthStartInput, g oauth.Grant) (int64, error) {
	email := g.Email
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(in.Email))
	}
	if in.AccountID != 0 {
		acc, err := a.st.GetAccount(ctx, in.AccountID)
		if err != nil {
			return 0, err
		}
		ref := oauth.SecretRef(acc)
		if ref == "" {
			ref = oauthSecretRef(acc.ID)
			acc.SecretRef = ref
			if err := a.st.UpdateAccount(ctx, acc); err != nil {
				return 0, err
			}
		}
		if err := a.oauth.mgr.SaveGrant(ref, g); err != nil {
			return 0, err
		}
		if m, ok := a.sync.(managerSyncer); ok {
			_ = m.m.ReloadAccount(ctx, acc.ID)
		}
		a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: acc.ID})
		return acc.ID, nil
	}
	kind := domain.AccountKind(in.Kind)
	srvIn, srvOut, extra := a.accountTemplate(in.Provider, kind, email)
	name := in.DisplayName
	if name == "" {
		name = email
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return 0, err
	}
	acc := domain.Account{Kind: kind, DisplayName: name, Incoming: srvIn, Outgoing: srvOut, Extra: extra, Enabled: true, SortOrder: len(accs)}
	id, err := a.st.CreateAccount(ctx, acc)
	if err != nil {
		return 0, err
	}
	acc.ID, acc.SecretRef = id, oauthSecretRef(id)
	if err := a.oauth.mgr.SaveGrant(acc.SecretRef, g); err != nil {
		_ = a.st.DeleteAccount(ctx, id)
		return 0, err
	}
	if err := a.st.UpdateAccount(ctx, acc); err != nil {
		return 0, err
	}
	person := in.Name
	if person == "" {
		person = g.Name
	}
	if person == "" {
		person = name
	}
	if email != "" {
		if _, err := a.st.CreateIdentity(ctx, domain.Identity{AccountID: id, Name: person, Email: email, IsDefault: true}); err != nil {
			return 0, err
		}
	}
	if err := a.sync.AddAccount(ctx, id); err != nil {
		a.log.Warn("app: starting sync failed", "account", id, "err", err)
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return id, nil
}

// AddSharedMailbox adds a shared/delegated mailbox of a Microsoft account
// as its own account (Graph /users/{address}/… or EWS Mailbox element) that
// reuses the owner's sign-in. With SendMode an identity "Senden als" or
// "Senden im Auftrag von" is created.
func (a *App) AddSharedMailbox(ctx context.Context, in SharedMailboxInput) (Account, error) {
	owner, err := a.st.GetAccount(ctx, in.AccountID)
	if err != nil {
		return Account{}, err
	}
	addr := strings.ToLower(strings.TrimSpace(in.Address))
	if !strings.Contains(addr, "@") {
		return Account{}, invalid("invalid e-mail address")
	}
	switch in.SendMode {
	case "", graph.SendAs, graph.SendOnBehalf:
	default:
		return Account{}, invalid("unknown send mode %q", in.SendMode)
	}
	if owner.Kind != domain.KindGraph && owner.Kind != domain.KindEWS {
		return Account{}, Errorf(http.StatusBadRequest, CodeNotSupported, "shared mailboxes need a Microsoft Graph or EWS account")
	}
	name := in.DisplayName
	if owner.Kind == domain.KindGraph {
		ts, err := a.oauth.mgr.ForAccount(owner)
		if err != nil {
			return Account{}, oauthError(err)
		}
		c, err := graph.NewClient(graph.Config{BaseURL: owner.Extra[tsync.ExtraGraphBaseURL], Tokens: ts})
		if err != nil {
			return Account{}, err
		}
		dn, err := c.CheckMailbox(ctx, addr)
		if err != nil {
			return Account{}, Errorf(http.StatusBadRequest, CodeNotFound, "%v", err)
		}
		if name == "" {
			name = dn
		}
	}
	if name == "" {
		name = addr
	}
	extra := map[string]string{}
	for k, v := range owner.Extra {
		extra[k] = v
	}
	self := owner.Extra["address"]
	extra[tsync.ExtraMailbox], extra[tsync.ExtraSendMode], extra["address"], extra["ownerAccountId"] = addr, in.SendMode, self,
		fmt.Sprint(owner.ID)
	ref := oauth.SecretRef(owner)
	if ref != "" && (owner.Kind == domain.KindGraph || owner.Incoming != nil && owner.Incoming.Auth == domain.AuthOAuth2) {
		extra[oauth.ExtraSecretRef] = ref
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return Account{}, err
	}
	acc := domain.Account{Kind: owner.Kind, DisplayName: name, Incoming: owner.Incoming, Outgoing: owner.Outgoing, Extra: extra,
		Enabled: true, SortOrder: len(accs), SecretRef: owner.SecretRef}
	id, err := a.st.CreateAccount(ctx, acc)
	if err != nil {
		return Account{}, err
	}
	if in.SendMode != "" {
		iid, err := a.st.CreateIdentity(ctx, domain.Identity{AccountID: id, Name: name, Email: addr, IsDefault: true})
		if err != nil {
			return Account{}, err
		}
		if err := a.st.SetIdentityDelegation(ctx, store.IdentityDelegation{IdentityID: iid, Mode: in.SendMode, Mailbox: addr,
			OwnerAccountID: owner.ID}); err != nil {
			return Account{}, err
		}
	}
	if err := a.sync.AddAccount(ctx, id); err != nil {
		a.log.Warn("app: starting sync failed", "account", id, "err", err)
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return a.st.GetAccount(ctx, id)
}

// ListIdentityDelegations returns the send modes of shared-mailbox identities.
func (a *App) ListIdentityDelegations(ctx context.Context) ([]store.IdentityDelegation, error) {
	m, err := a.st.IdentityDelegations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.IdentityDelegation, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IdentityID < out[j].IdentityID })
	return out, nil
}

// SetIdentityDelegation changes the send mode of an identity (empty mode
// removes it) and the send mode of its account.
func (a *App) SetIdentityDelegation(ctx context.Context, d store.IdentityDelegation) error {
	switch d.Mode {
	case "", graph.SendAs, graph.SendOnBehalf:
	default:
		return invalid("unknown send mode %q", d.Mode)
	}
	ident, err := a.st.GetIdentity(ctx, d.IdentityID)
	if err != nil {
		return err
	}
	if d.Mailbox == "" {
		d.Mailbox = ident.Email
	}
	if err := a.st.SetIdentityDelegation(ctx, d); err != nil {
		return err
	}
	acc, err := a.st.GetAccount(ctx, ident.AccountID)
	if err == nil && acc.Extra[tsync.ExtraMailbox] != "" {
		acc.Extra[tsync.ExtraSendMode] = d.Mode
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return err
		}
	}
	return nil
}

// ==== calendar/contacts & vacation of cloud accounts =========================

// cloudPIM returns the calendar/contacts adapter of a cloud account.
func (a *App) cloudPIM(ctx context.Context, acc domain.Account) (any, error) {
	creds := tsync.SecretCredentials{Secrets: a.opts.Secrets, OAuth: tsync.OAuthAdapter{M: a.oauth.mgr}}
	switch acc.Kind {
	case domain.KindGraph:
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return nil, oauthError(err)
		}
		return graph.NewClient(graph.Config{BaseURL: acc.Extra[tsync.ExtraGraphBaseURL], Tokens: ts, Mailbox: acc.Extra[tsync.ExtraMailbox],
			Self: acc.Extra["address"]})
	case domain.KindGmail:
		ts, err := a.oauth.mgr.ForAccount(acc)
		if err != nil {
			return nil, oauthError(err)
		}
		return google.New(google.Config{Tokens: ts, Self: acc.Extra["address"]})
	case domain.KindEWS:
		cfg, err := tsync.EWSConfig(ctx, acc, creds)
		if err != nil {
			return nil, err
		}
		return ews.NewClient(cfg)
	}
	return nil, notSupported("calendar sync for this account type")
}

// SyncCloudPIM imports calendars, events, contacts and tasks of a Graph,
// Gmail or EWS account into the local calendar and contact tables.
func (a *App) SyncCloudPIM(ctx context.Context, accountID int64) (pimapi.ImportResult, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return pimapi.ImportResult{}, err
	}
	src, err := a.cloudPIM(ctx, acc)
	if err != nil {
		return pimapi.ImportResult{}, err
	}
	return pimapi.Import(ctx, a.st, accountID, src, pimapi.ImportOptions{})
}

// VacationServers returns the server-side vacation support of all cloud
// accounts (for vacation.Config.Servers).
func (a *App) VacationServers(ctx context.Context) (map[int64]vacation.ServerVacationSync, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]vacation.ServerVacationSync{}
	creds := tsync.SecretCredentials{Secrets: a.opts.Secrets, OAuth: tsync.OAuthAdapter{M: a.oauth.mgr}}
	for _, acc := range accs {
		switch acc.Kind {
		case domain.KindGraph:
			ts, err := a.oauth.mgr.ForAccount(acc)
			if err != nil {
				continue
			}
			if c, err := graph.NewClient(graph.Config{BaseURL: acc.Extra[tsync.ExtraGraphBaseURL], Tokens: ts, Mailbox: acc.Extra[tsync.ExtraMailbox]}); err == nil {
				out[acc.ID] = graph.Vacation{C: c}
			}
		case domain.KindGmail:
			ts, err := a.oauth.mgr.ForAccount(acc)
			if err != nil {
				continue
			}
			if p, err := gmail.New(gmail.Config{BaseURL: acc.Extra[tsync.ExtraGmailBaseURL], Tokens: ts}); err == nil {
				out[acc.ID] = p
			}
		case domain.KindEWS:
			cfg, err := tsync.EWSConfig(ctx, acc, creds)
			if err != nil {
				continue
			}
			if c, err := ews.NewClient(cfg); err == nil {
				addr := acc.Extra[tsync.ExtraMailbox]
				if addr == "" {
					addr = acc.Extra["address"]
				}
				out[acc.ID] = ews.Vacation{C: c, Address: addr}
			}
		}
	}
	return out, nil
}
