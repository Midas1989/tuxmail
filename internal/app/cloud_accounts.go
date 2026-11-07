// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/nextcloud"
	"github.com/Midas1989/Mailclient/internal/cloud/opencloud"
	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden"
	"github.com/Midas1989/Mailclient/internal/cloud/webdav"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// Cloud accounts (KONZEPT 12) are rows of the accounts table with kind
// nextcloud, opencloud, vaultwarden or webdav. Extra holds the server URL
// and user; the secret store holds (under SecretRef, JSON) the Nextcloud app
// password, the OpenCloud OAuth token, the Vaultwarden session (tokens and
// the decrypted user key, never the master password) or the WebDAV password.

// KindWebDAV is the account kind of plain WebDAV storage.
const KindWebDAV domain.AccountKind = "webdav"

// DefaultCloudUploadDir is the base directory of attachments sent as links
// (KONZEPT 12.2: /TuxMail/Mail-Anhänge/<date>/<subject>/<file>).
const DefaultCloudUploadDir = "/TuxMail/Mail-Anhänge"

// cloudFlowTimeout ends login flows the user never finishes.
const cloudFlowTimeout = 20 * time.Minute

// PDFRenderer renders a raw mail as (archive) PDF for the PDF workflow
// action; PDF generation itself lives outside the cloud module.
type PDFRenderer func(ctx context.Context, raw []byte, req PDFRenderRequest) ([]byte, error)

// PDFRenderRequest are the options of PDFRenderer.
type PDFRenderRequest struct {
	EmbedOriginal      bool
	IncludeAttachments bool
}

// cloudState is the per-App cloud runtime state.
type cloudState struct {
	mu      sync.Mutex
	clients map[int64]*cloudClient
	flows   map[string]*cloudFlow
	pdf     PDFRenderer
}

type cloudClient struct {
	kind    domain.AccountKind
	storage cloud.Storage
	sharer  cloud.Sharer
	revoker interface {
		Revoke(ctx context.Context, id string) error
	}
	policy cloud.PolicyProvider
	nc     *nextcloud.Client
	oc     *opencloud.Client
	vw     *vaultwarden.Client
}

type cloudFlow struct {
	mu      sync.Mutex
	info    CloudLoginFlow
	in      CloudLoginInput
	created time.Time
	nc      nextcloud.LoginFlow
	oidc    *opencloud.Login
	product opencloud.Product
	cancel  context.CancelFunc
}

func (f *cloudFlow) snapshot() CloudLoginFlow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

func (f *cloudFlow) finish(acc *CloudAccount, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.info.Status, f.info.Error = CloudFlowError, err.Error()
		return
	}
	f.info.Status, f.info.Account, f.info.LoginURL = CloudFlowDone, acc, ""
}

// SetPDFRenderer installs the renderer of the PDF archive action.
func (a *App) SetPDFRenderer(r PDFRenderer) {
	a.cloud.mu.Lock()
	a.cloud.pdf = r
	a.cloud.mu.Unlock()
}

// isCloudKind reports whether k is a cloud account kind.
func isCloudKind(k domain.AccountKind) bool {
	switch k {
	case domain.KindNextcloud, domain.KindOpenCloud, domain.KindVaultwarden, KindWebDAV:
		return true
	}
	return false
}

// cloudErr maps provider errors to API errors.
func cloudErr(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	switch {
	case errors.Is(err, cloud.ErrUnauthorized):
		return &Error{Code: "cloud_auth", Status: http.StatusUnauthorized, Message: "the cloud server rejected the credentials, please sign in again", Err: err}
	case errors.Is(err, cloud.ErrNotFound):
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: "not found on the cloud server", Err: err}
	case errors.Is(err, cloud.ErrExists):
		return &Error{Code: CodeConflict, Status: http.StatusConflict, Message: "already exists", Err: err}
	case errors.Is(err, cloud.ErrForbidden):
		return &Error{Code: "cloud_forbidden", Status: http.StatusForbidden, Message: "permission denied by the cloud server", Err: err}
	case errors.Is(err, cloud.ErrUnsupported):
		return &Error{Code: CodeNotSupported, Status: http.StatusNotImplemented, Message: err.Error(), Err: err}
	case errors.Is(err, cloud.ErrInsufficient):
		return &Error{Code: "cloud_quota", Status: http.StatusInsufficientStorage, Message: "not enough storage on the cloud server", Err: err}
	case errors.Is(err, vaultwarden.ErrNotAPI):
		return &Error{Code: "cloud_not_api", Status: http.StatusBadGateway, Message: "the server did not answer with the expected API (got a web page)", Err: err}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	if AsError(err).Code != CodeInternal {
		return err
	}
	var ne net.Error
	var ue *url.Error
	if errors.As(err, &ne) || errors.As(err, &ue) {
		return &Error{Code: "cloud_unreachable", Status: http.StatusBadGateway, Message: "the cloud server is not reachable", Err: err}
	}
	return &Error{Code: "cloud_error", Status: http.StatusBadGateway, Message: err.Error(), Err: err}
}

// ==== accounts ================================================================

func (a *App) toCloudAccount(acc domain.Account) CloudAccount {
	x := acc.Extra
	out := CloudAccount{ID: acc.ID, Provider: string(acc.Kind), DisplayName: acc.DisplayName, Server: x["server"], User: x["user"]}
	switch acc.Kind {
	case domain.KindNextcloud:
		out.Capabilities = CloudCaps{Files: true, Share: true}
		uid := x["userId"]
		if uid == "" {
			uid = x["user"]
		}
		u := url.PathEscape(uid)
		out.DAV = &CloudDAV{CalDAV: x["server"] + "/remote.php/dav/calendars/" + u + "/", CardDAV: x["server"] + "/remote.php/dav/addressbooks/users/" + u + "/"}
	case domain.KindOpenCloud:
		out.Capabilities = CloudCaps{Files: true, Share: true}
	case domain.KindVaultwarden:
		out.Capabilities = CloudCaps{Send: true}
		out.User = x["email"]
	case KindWebDAV:
		out.Capabilities = CloudCaps{Files: true}
	}
	if out.Capabilities.Share {
		out.UploadDir = uploadDir(acc)
	}
	return out
}

func uploadDir(acc domain.Account) string {
	if d := acc.Extra["uploadDir"]; d != "" {
		return cloud.Clean(d)
	}
	return DefaultCloudUploadDir
}

// ListCloudAccounts returns the cloud accounts.
func (a *App) ListCloudAccounts(ctx context.Context) ([]CloudAccount, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := []CloudAccount{}
	for _, acc := range accs {
		if isCloudKind(acc.Kind) && acc.Enabled {
			out = append(out, a.toCloudAccount(acc))
		}
	}
	return out, nil
}

func (a *App) cloudAccount(ctx context.Context, id int64) (domain.Account, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return acc, err
	}
	if !isCloudKind(acc.Kind) {
		return acc, invalid("account %d is not a cloud account", id)
	}
	return acc, nil
}

// defaultCloudAccount returns the first enabled account of a provider.
func (a *App) defaultCloudAccount(ctx context.Context, provider string) (domain.Account, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	for _, acc := range accs {
		if acc.Enabled && string(acc.Kind) == provider && isCloudKind(acc.Kind) {
			return acc, nil
		}
	}
	return domain.Account{}, Errorf(http.StatusConflict, "cloud_not_configured", "no %s account is configured", provider)
}

// cloudAccountByTarget resolves a workflow target: provider name, account
// display name or id.
func (a *App) cloudAccountByTarget(ctx context.Context, target string) (domain.Account, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	t := strings.TrimSpace(target)
	for _, acc := range accs {
		if isCloudKind(acc.Kind) && acc.Enabled && (strings.EqualFold(acc.DisplayName, t) || fmt.Sprint(acc.ID) == t) {
			return acc, nil
		}
	}
	return a.defaultCloudAccount(ctx, strings.ToLower(t))
}

// DeleteCloudAccount removes a cloud account, its credentials and (for
// Nextcloud) the app password on the server.
func (a *App) DeleteCloudAccount(ctx context.Context, id int64) error {
	acc, err := a.cloudAccount(ctx, id)
	if err != nil {
		return err
	}
	if c, err := a.cloudClient(ctx, id); err == nil && c.nc != nil {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := c.nc.RevokeAppPassword(rctx); err != nil {
			a.log.Info("app: revoking the Nextcloud app password failed", "account", id, "err", err)
		}
		cancel()
	}
	a.dropCloudClient(id)
	if acc.SecretRef != "" {
		_ = a.opts.Secrets.Delete(acc.SecretRef)
	}
	if err := a.st.DeleteAccount(ctx, id); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return nil
}

func cloudSecretRef(id int64) string { return fmt.Sprintf("cloud:%d", id) }

// saveCloudAccount creates or updates (same kind, server and user) an
// account and stores its secret.
func (a *App) saveCloudAccount(ctx context.Context, kind domain.AccountKind, name string, extra map[string]string, secret any) (CloudAccount, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return CloudAccount{}, err
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return CloudAccount{}, err
	}
	var acc domain.Account
	for _, x := range accs {
		if x.Kind == kind && x.Extra["server"] == extra["server"] && x.Extra["user"] == extra["user"] && x.Extra["email"] == extra["email"] {
			acc = x
		}
	}
	if acc.ID == 0 {
		acc = domain.Account{Kind: kind, DisplayName: name, Enabled: true, Extra: extra, SortOrder: len(accs)}
		id, err := a.st.CreateAccount(ctx, acc)
		if err != nil {
			return CloudAccount{}, err
		}
		acc.ID = id
	} else {
		for k, v := range extra {
			acc.Extra[k] = v
		}
		acc.Enabled = true
	}
	acc.SecretRef = cloudSecretRef(acc.ID)
	if err := a.opts.Secrets.Set(acc.SecretRef, raw); err != nil {
		return CloudAccount{}, err
	}
	if err := a.st.UpdateAccount(ctx, acc); err != nil {
		return CloudAccount{}, err
	}
	a.dropCloudClient(acc.ID)
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: acc.ID})
	return a.toCloudAccount(acc), nil
}

type ncSecret struct {
	AppPassword string `json:"appPassword"`
}

type ocSecret struct {
	Token *oauth2.Token `json:"token"`
}

type davSecret struct {
	Password string `json:"password"`
}

// ==== clients =================================================================

func (a *App) dropCloudClient(id int64) {
	a.cloud.mu.Lock()
	delete(a.cloud.clients, id)
	a.cloud.mu.Unlock()
}

// cloudClient returns the (cached) provider client of an account.
func (a *App) cloudClient(ctx context.Context, id int64) (*cloudClient, error) {
	a.cloud.mu.Lock()
	if c, ok := a.cloud.clients[id]; ok {
		a.cloud.mu.Unlock()
		return c, nil
	}
	a.cloud.mu.Unlock()
	acc, err := a.cloudAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	raw, err := a.opts.Secrets.Get(acc.SecretRef)
	if err != nil {
		return nil, &Error{Code: "cloud_auth", Status: http.StatusUnauthorized, Message: "no credentials stored for this cloud account", Err: err}
	}
	x, hc := acc.Extra, a.opts.HTTPClient
	c := &cloudClient{kind: acc.Kind}
	switch acc.Kind {
	case domain.KindNextcloud:
		var s ncSecret
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		nc, err := nextcloud.New(x["server"], x["user"], x["userId"], s.AppPassword, hc)
		if err != nil {
			return nil, err
		}
		c.nc, c.storage, c.sharer, c.revoker, c.policy = nc, nc, nc, nc, nc
	case domain.KindOpenCloud:
		var s ocSecret
		if err := json.Unmarshal(raw, &s); err != nil || s.Token == nil {
			return nil, &Error{Code: "cloud_auth", Status: http.StatusUnauthorized, Message: "invalid OpenCloud token, please sign in again", Err: err}
		}
		cfg := oauth2.Config{ClientID: x["clientId"], ClientSecret: x["clientSecret"],
			Endpoint: oauth2.Endpoint{AuthURL: x["authUrl"], TokenURL: x["tokenUrl"]}}
		if cfg.ClientSecret == "" {
			cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
		}
		ref := acc.SecretRef
		ts := opencloud.PersistingTokenSource(opencloud.TokenSource(context.Background(), hc, cfg, s.Token), s.Token, func(t *oauth2.Token) {
			if b, err := json.Marshal(ocSecret{Token: t}); err == nil {
				_ = a.opts.Secrets.Set(ref, b)
			}
		})
		oc, err := opencloud.New(x["server"], hc, ts)
		if err != nil {
			return nil, err
		}
		c.oc, c.storage, c.sharer, c.revoker, c.policy = oc, oc, oc, oc, oc
	case domain.KindVaultwarden:
		var s vaultwarden.Session
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		vw, err := vaultwarden.New(x["server"], hc)
		if err != nil {
			return nil, err
		}
		vw.Now = a.opts.Now
		vw.SetSession(s)
		ref := acc.SecretRef
		vw.OnSession = func(s vaultwarden.Session) {
			// #nosec G117 -- serialized only into the secret store (keyring/vault)
			if b, err := json.Marshal(s); err == nil {
				_ = a.opts.Secrets.Set(ref, b)
			}
		}
		c.vw, c.revoker = vw, vw
	case KindWebDAV:
		var s davSecret
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		d, err := webdav.New(x["server"], x["user"], s.Password, hc)
		if err != nil {
			return nil, err
		}
		c.storage = d
	}
	a.cloud.mu.Lock()
	if a.cloud.clients == nil {
		a.cloud.clients = map[int64]*cloudClient{}
	}
	a.cloud.clients[id] = c
	a.cloud.mu.Unlock()
	return c, nil
}

func (a *App) cloudStorage(ctx context.Context, id int64) (*cloudClient, error) {
	c, err := a.cloudClient(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.storage == nil {
		return nil, Errorf(http.StatusNotImplemented, CodeNotSupported, "%s accounts have no file storage", c.kind)
	}
	return c, nil
}

// resolve maps a user path to the provider path (OpenCloud: personal space
// unless the path starts with a space id).
func (c *cloudClient) resolve(ctx context.Context, p string) (string, error) {
	if c.oc != nil {
		return c.oc.ResolvePath(ctx, p)
	}
	return cloud.Clean(p), nil
}

// ==== login flows =============================================================

func (a *App) addFlow(f *cloudFlow) {
	a.cloud.mu.Lock()
	defer a.cloud.mu.Unlock()
	if a.cloud.flows == nil {
		a.cloud.flows = map[string]*cloudFlow{}
	}
	for id, old := range a.cloud.flows {
		if a.now().Sub(old.created) > cloudFlowTimeout {
			if old.cancel != nil {
				old.cancel()
			}
			delete(a.cloud.flows, id)
		}
	}
	a.cloud.flows[f.info.ID] = f
}

func (a *App) flow(id string) (*cloudFlow, error) {
	a.cloud.mu.Lock()
	defer a.cloud.mu.Unlock()
	f, ok := a.cloud.flows[id]
	if !ok {
		return nil, errNotFound
	}
	return f, nil
}

// StartCloudLogin adds a cloud account. Nextcloud and OpenCloud return a
// pending flow whose LoginURL the UI opens in the browser (then
// PollCloudLogin); Vaultwarden and WebDAV log in directly (Vaultwarden may
// answer with status two_factor: call again with TOTP).
func (a *App) StartCloudLogin(ctx context.Context, in CloudLoginInput) (CloudLoginFlow, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	f := &cloudFlow{in: in, created: a.now(), info: CloudLoginFlow{ID: newID("cl_"), Provider: in.Provider, Status: CloudFlowPending}}
	hc := a.opts.HTTPClient
	switch in.Provider {
	case CloudNextcloud:
		nf, err := nextcloud.StartLogin(ctx, hc, in.Server)
		if err != nil {
			return CloudLoginFlow{}, cloudErr(err)
		}
		f.nc, f.info.LoginURL = nf, nf.LoginURL
	case CloudOpenCloud:
		if err := a.startOIDC(ctx, f); err != nil {
			return CloudLoginFlow{}, cloudErr(err)
		}
	case CloudVaultwarden:
		acc, err := a.loginVaultwarden(ctx, in)
		var tf *vaultwarden.TwoFactorError
		if errors.As(err, &tf) {
			f.info.Status = CloudFlowTwoFactor
			f.info.Error = "two_factor_required"
			return f.info, nil
		}
		if err != nil {
			return CloudLoginFlow{}, cloudErr(err)
		}
		f.finish(&acc, nil)
		return f.snapshot(), nil
	case CloudWebDAV:
		acc, err := a.loginWebDAV(ctx, in)
		if err != nil {
			return CloudLoginFlow{}, cloudErr(err)
		}
		f.finish(&acc, nil)
		return f.snapshot(), nil
	default:
		return CloudLoginFlow{}, invalid("unknown cloud provider %q", in.Provider)
	}
	a.addFlow(f)
	return f.snapshot(), nil
}

// PollCloudLogin returns the state of a login flow (Nextcloud: polls the
// server once).
func (a *App) PollCloudLogin(ctx context.Context, id string) (CloudLoginFlow, error) {
	f, err := a.flow(id)
	if err != nil {
		return CloudLoginFlow{}, err
	}
	if f.info.Provider == CloudNextcloud && f.snapshot().Status == CloudFlowPending {
		cr, err := nextcloud.PollLogin(ctx, a.opts.HTTPClient, f.nc)
		switch {
		case errors.Is(err, nextcloud.ErrPending):
			if a.now().Sub(f.created) > cloudFlowTimeout {
				f.finish(nil, errors.New("login timed out"))
			}
		case err != nil:
			f.finish(nil, err)
		default:
			acc, err := a.finishNextcloud(ctx, f.in, cr)
			f.finish(&acc, err)
		}
	}
	return f.snapshot(), nil
}

// CancelCloudLogin aborts a login flow.
func (a *App) CancelCloudLogin(_ context.Context, id string) error {
	a.cloud.mu.Lock()
	defer a.cloud.mu.Unlock()
	f, ok := a.cloud.flows[id]
	if !ok {
		return errNotFound
	}
	if f.cancel != nil {
		f.cancel()
	}
	delete(a.cloud.flows, id)
	return nil
}

func (a *App) finishNextcloud(ctx context.Context, in CloudLoginInput, cr nextcloud.Credentials) (CloudAccount, error) {
	nc, err := nextcloud.New(cr.Server, cr.LoginName, "", cr.AppPassword, a.opts.HTTPClient)
	if err != nil {
		return CloudAccount{}, err
	}
	u, err := nc.ResolveUser(ctx)
	if err != nil {
		return CloudAccount{}, err
	}
	name := in.DisplayName
	if name == "" {
		host := cr.Server
		if pu, err := url.Parse(cr.Server); err == nil {
			host = pu.Host
		}
		name = "Nextcloud (" + host + ")"
	}
	return a.saveCloudAccount(ctx, domain.KindNextcloud, name,
		map[string]string{"server": cr.Server, "user": cr.LoginName, "userId": nc.UserID, "displayName": u.DisplayName},
		ncSecret{AppPassword: cr.AppPassword})
}

// startOIDC prepares the OpenCloud login: loopback redirect on the desktop
// (a goroutine waits for it), the given redirect URL in server mode
// (CompleteCloudOAuth is called by the callback route).
func (a *App) startOIDC(ctx context.Context, f *cloudFlow) error {
	hc := a.opts.HTTPClient
	srv, err := opencloud.NormalizeServer(f.in.Server)
	if err != nil {
		return err
	}
	prod, err := opencloud.DetectProduct(ctx, hc, srv)
	if err != nil {
		return err
	}
	f.product = prod
	var lb *opencloud.Loopback
	redirect := f.in.RedirectURL
	if redirect == "" {
		if a.opts.Mode == ModeServer {
			return invalid("redirectUrl required in server mode")
		}
		if lb, err = opencloud.ListenLoopback(); err != nil {
			return err
		}
		redirect = lb.URL
	}
	l, err := opencloud.StartLogin(ctx, hc, srv, opencloud.LoginOptions{RedirectURL: redirect, Product: prod, ClientID: f.in.ClientID})
	if err != nil {
		if lb != nil {
			lb.Close()
		}
		return err
	}
	f.oidc, f.info.LoginURL = l, l.AuthURL
	if lb != nil {
		wctx, cancel := context.WithTimeout(context.Background(), cloudFlowTimeout)
		f.cancel = cancel
		go func() {
			defer cancel()
			defer lb.Close()
			code, state, err := lb.Wait(wctx)
			if err != nil {
				f.finish(nil, err)
				return
			}
			acc, err := a.finishOIDC(wctx, f, code, state)
			f.finish(&acc, err)
		}()
	}
	return nil
}

// CompleteCloudOAuth finishes an OpenCloud login from the server-mode
// callback (state identifies the flow).
func (a *App) CompleteCloudOAuth(ctx context.Context, state, code, errText string) (CloudLoginFlow, error) {
	a.cloud.mu.Lock()
	var f *cloudFlow
	for _, x := range a.cloud.flows {
		if x.oidc != nil && x.oidc.State == state {
			f = x
		}
	}
	a.cloud.mu.Unlock()
	if f == nil || state == "" {
		return CloudLoginFlow{}, invalid("unknown or expired login")
	}
	if errText != "" {
		f.finish(nil, fmt.Errorf("login failed: %s", errText))
		return f.snapshot(), nil
	}
	acc, err := a.finishOIDC(ctx, f, code, state)
	f.finish(&acc, err)
	return f.snapshot(), nil
}

func (a *App) finishOIDC(ctx context.Context, f *cloudFlow, code, state string) (CloudAccount, error) {
	hc := a.opts.HTTPClient
	tok, err := f.oidc.Exchange(ctx, hc, code, state)
	if err != nil {
		return CloudAccount{}, err
	}
	oc, err := opencloud.New(f.oidc.Server, hc, oauth2.StaticTokenSource(tok))
	if err != nil {
		return CloudAccount{}, err
	}
	name, mail, err := oc.Me(ctx)
	if err != nil {
		return CloudAccount{}, err
	}
	user := mail
	if user == "" {
		user = name
	}
	disp := f.in.DisplayName
	if disp == "" {
		label := "OpenCloud"
		if f.product == opencloud.ProductOCIS {
			label = "ownCloud"
		}
		host := f.oidc.Server
		if pu, err := url.Parse(host); err == nil {
			host = pu.Host
		}
		disp = label + " (" + host + ")"
	}
	cfg := f.oidc.Config
	return a.saveCloudAccount(ctx, domain.KindOpenCloud, disp, map[string]string{"server": f.oidc.Server, "user": user,
		"product": string(f.product), "clientId": cfg.ClientID, "clientSecret": cfg.ClientSecret, "authUrl": cfg.Endpoint.AuthURL,
		"tokenUrl": cfg.Endpoint.TokenURL}, ocSecret{Token: tok})
}

func (a *App) loginVaultwarden(ctx context.Context, in CloudLoginInput) (CloudAccount, error) {
	if in.Email == "" || in.Password == "" {
		return CloudAccount{}, invalid("e-mail address and master password required")
	}
	vw, err := vaultwarden.New(in.Server, a.opts.HTTPClient)
	if err != nil {
		return CloudAccount{}, err
	}
	vw.Now = a.opts.Now
	s, err := vw.Login(ctx, in.Email, in.Password, vaultwarden.LoginOptions{TwoFactorToken: in.TOTP})
	if err != nil {
		return CloudAccount{}, err
	}
	name := in.DisplayName
	if name == "" {
		name = "Vaultwarden Send (" + s.Email + ")"
	}
	return a.saveCloudAccount(ctx, domain.KindVaultwarden, name, map[string]string{"server": vw.Vault, "email": s.Email}, s)
}

func (a *App) loginWebDAV(ctx context.Context, in CloudLoginInput) (CloudAccount, error) {
	d, err := webdav.New(strings.TrimSpace(in.Server), in.Username, in.Password, a.opts.HTTPClient)
	if err != nil {
		return CloudAccount{}, invalid("%v", err)
	}
	if err := d.Ping(ctx); err != nil {
		return CloudAccount{}, err
	}
	name := in.DisplayName
	if name == "" {
		name = "WebDAV (" + d.Base.Host + ")"
	}
	return a.saveCloudAccount(ctx, KindWebDAV, name, map[string]string{"server": d.Base.String(), "user": in.Username},
		davSecret{Password: in.Password})
}
