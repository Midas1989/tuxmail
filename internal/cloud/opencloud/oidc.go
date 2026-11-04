// SPDX-License-Identifier: AGPL-3.0-or-later

package opencloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Public desktop client registrations of the built-in identity providers
// (both accept loopback redirects on any port).
const (
	OpenCloudDesktopClientID = "OpenCloudDesktop"
	OCISDesktopClientID      = "xdXOt13JKxym1B1QcEncf2XDkLAexMBFwiT9j6EfhhHFJhs2KM9jbjTmf8JBXE69"
	OCISDesktopClientSecret  = "UBntmLjC2yYCeHwsyj73Uwo9TAaecAetRwMw0xYcvNL9yRdLSUi0hUAHfvCHFeFh"
)

// DefaultScopes are requested at login (offline_access for a refresh token).
var DefaultScopes = []string{"openid", "profile", "email", "offline_access"}

// Product identifies the server flavour.
type Product string

const (
	ProductOpenCloud Product = "opencloud"
	ProductOCIS      Product = "ocis"
)

// NormalizeServer turns user input into the server base URL.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("opencloud: server URL required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("opencloud: invalid server URL %q", s)
	}
	p := u.Path
	for _, cut := range []string{"/files/", "/dav/", "/graph/", "/remote.php"} {
		if i := strings.Index(p, cut); i >= 0 {
			p = p[:i]
		}
	}
	u.Path, u.RawQuery, u.Fragment, u.User = strings.TrimSuffix(p, "/"), "", "", nil
	return u.String(), nil
}

// DetectProduct reads /status.php (OpenCloud or ownCloud Infinite Scale).
func DetectProduct(ctx context.Context, hc *http.Client, server string) (Product, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/status.php", nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient(hc).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var st struct {
		ProductName string `json:"productname"`
		Product     string `json:"product"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&st) != nil {
		return "", fmt.Errorf("opencloud: %s does not look like an OpenCloud or oCIS server", server)
	}
	name := strings.ToLower(st.ProductName + " " + st.Product)
	if strings.Contains(name, "opencloud") {
		return ProductOpenCloud, nil
	}
	return ProductOCIS, nil
}

func httpClient(hc *http.Client) *http.Client {
	if hc == nil {
		return http.DefaultClient
	}
	return hc
}

// Discovery is the subset of the OpenID provider metadata TuxMail needs.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// Discover fetches {issuer}/.well-known/openid-configuration.
func Discover(ctx context.Context, hc *http.Client, issuer string) (Discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return Discovery{}, err
	}
	resp, err := httpClient(hc).Do(req)
	if err != nil {
		return Discovery{}, err
	}
	defer resp.Body.Close()
	var d Discovery
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil ||
		d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return Discovery{}, fmt.Errorf("opencloud: no OpenID Connect configuration at %s", issuer)
	}
	return d, nil
}

// LoginOptions configure the OIDC login.
type LoginOptions struct {
	// Issuer defaults to the server URL (built-in IdP).
	Issuer       string
	ClientID     string // default by product
	ClientSecret string
	Scopes       []string
	// RedirectURL is the loopback URL (desktop) or the TuxMail server
	// callback (server mode).
	RedirectURL string
	Product     Product
}

// Login is a started authorization code flow with PKCE.
type Login struct {
	Config   oauth2.Config
	Verifier string
	State    string
	AuthURL  string
	Server   string
}

// StartLogin prepares the authorization URL.
func StartLogin(ctx context.Context, hc *http.Client, server string, o LoginOptions) (*Login, error) {
	srv, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	if o.RedirectURL == "" {
		return nil, errors.New("opencloud: redirect URL required")
	}
	issuer := o.Issuer
	if issuer == "" {
		issuer = srv
	}
	d, err := Discover(ctx, hc, issuer)
	if err != nil {
		return nil, err
	}
	if o.ClientID == "" {
		if o.Product == ProductOCIS {
			o.ClientID, o.ClientSecret = OCISDesktopClientID, OCISDesktopClientSecret
		} else {
			o.ClientID = OpenCloudDesktopClientID
		}
	}
	if len(o.Scopes) == 0 {
		o.Scopes = DefaultScopes
	}
	l := &Login{Server: srv, Verifier: oauth2.GenerateVerifier(), State: oauth2.GenerateVerifier()[:22],
		Config: oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: o.RedirectURL, Scopes: o.Scopes,
			Endpoint: oauth2.Endpoint{AuthURL: d.AuthorizationEndpoint, TokenURL: d.TokenEndpoint}}}
	if o.ClientSecret == "" {
		l.Config.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	l.AuthURL = l.Config.AuthCodeURL(l.State, oauth2.S256ChallengeOption(l.Verifier), oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "select_account consent"))
	return l, nil
}

// Exchange trades the authorization code for tokens.
func (l *Login) Exchange(ctx context.Context, hc *http.Client, code, state string) (*oauth2.Token, error) {
	if state != l.State {
		return nil, errors.New("opencloud: OIDC state mismatch")
	}
	if code == "" {
		return nil, errors.New("opencloud: no authorization code")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient(hc))
	return l.Config.Exchange(ctx, code, oauth2.VerifierOption(l.Verifier))
}

// TokenSource returns a refreshing token source for tok.
func TokenSource(ctx context.Context, hc *http.Client, cfg oauth2.Config, tok *oauth2.Token) oauth2.TokenSource {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient(hc))
	return cfg.TokenSource(ctx, tok)
}

// PersistingTokenSource calls save whenever base returns a new token
// (refresh), so the refresh token survives restarts.
func PersistingTokenSource(base oauth2.TokenSource, initial *oauth2.Token, save func(*oauth2.Token)) oauth2.TokenSource {
	return &persisting{base: base, last: initial, save: save}
}

type persisting struct {
	mu   sync.Mutex
	base oauth2.TokenSource
	last *oauth2.Token
	save func(*oauth2.Token)
}

func (p *persisting) Token() (*oauth2.Token, error) {
	t, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	changed := p.last == nil || p.last.AccessToken != t.AccessToken
	p.last = t
	p.mu.Unlock()
	if changed && p.save != nil {
		p.save(t)
	}
	return t, nil
}

// ==== loopback redirect (desktop) =============================================

// Loopback receives the authorization redirect on 127.0.0.1 (RFC 8252).
type Loopback struct {
	URL  string
	ln   net.Listener
	srv  *http.Server
	once sync.Once
	ch   chan callback
}

type callback struct{ code, state, err string }

// ListenLoopback starts the redirect listener on a random port.
func ListenLoopback() (*Loopback, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &Loopback{URL: "http://" + ln.Addr().String() + "/", ln: ln, ch: make(chan callback, 1)}
	l.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(l.serve)}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

func (l *Loopback) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code") == "" && q.Get("error") == "" {
		http.NotFound(w, r)
		return
	}
	cb := callback{code: q.Get("code"), state: q.Get("state"), err: q.Get("error")}
	if d := q.Get("error_description"); d != "" {
		cb.err += ": " + d
	}
	select {
	case l.ch <- cb:
	default:
	}
	msg := "TuxMail is now connected. You can close this window."
	if cb.err != "" {
		msg = "Login failed: " + cb.err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>TuxMail</title><p style=\"font:16px sans-serif;margin:3em\">%s</p>", html.EscapeString(msg))
}

// Wait blocks until the redirect arrives (or ctx ends) and returns code and state.
func (l *Loopback) Wait(ctx context.Context) (code, state string, err error) {
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case cb := <-l.ch:
		if cb.err != "" {
			return "", "", fmt.Errorf("opencloud: login failed: %s", cb.err)
		}
		return cb.code, cb.state, nil
	}
}

// Close stops the listener.
func (l *Loopback) Close() {
	l.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = l.srv.Shutdown(ctx)
	})
}
