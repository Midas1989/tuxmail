// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenBrowser opens url with the platform's default handler.
func OpenBrowser(ctx context.Context, u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", u)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", u)
	}
	return cmd.Start()
}

// Flow is a running interactive sign-in (loopback or device code).
type Flow struct {
	// AuthURL is the URL the user has to open (loopback flow).
	AuthURL string
	// Device flow: the code to enter at VerificationURI.
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time

	done   chan struct{}
	grant  Grant
	err    error
	cancel context.CancelFunc
	once   sync.Once
}

func newFlow() *Flow { return &Flow{done: make(chan struct{})} }

func (f *Flow) finish(g Grant, err error) {
	f.once.Do(func() {
		f.grant, f.err = g, err
		close(f.done)
	})
}

// Done is closed when the flow finished.
func (f *Flow) Done() <-chan struct{} { return f.done }

// Result returns the grant once Done is closed.
func (f *Flow) Result() (Grant, error) {
	select {
	case <-f.done:
		return f.grant, f.err
	default:
		return Grant{}, errors.New("oauth: flow still running")
	}
}

// Wait blocks until the flow finished or ctx is done.
func (f *Flow) Wait(ctx context.Context) (Grant, error) {
	select {
	case <-f.done:
		return f.grant, f.err
	case <-ctx.Done():
		return Grant{}, ctx.Err()
	}
}

// Cancel aborts the flow.
func (f *Flow) Cancel() {
	if f.cancel != nil {
		f.cancel()
	}
	f.finish(Grant{}, context.Canceled)
}

// StartLoopback starts the desktop flow: a temporary listener on
// 127.0.0.1 (and ::1 on the same port when available) receives the
// redirect, the browser is opened with the authorization URL. The returned
// flow finishes after the redirect arrived or LoopbackTimeout elapsed.
func (m *Manager) StartLoopback(ctx context.Context, req StartRequest) (*Flow, error) {
	p, err := m.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth: loopback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	listeners := []net.Listener{ln}
	if p.ep.LoopbackHost == "localhost" {
		// Browsers may resolve localhost to ::1.
		if ln6, err := net.Listen("tcp", "[::1]:"+strconv.Itoa(port)); err == nil {
			listeners = append(listeners, ln6)
		}
	}
	host := p.ep.LoopbackHost
	if host == "" {
		host = "127.0.0.1"
	}
	path := p.ep.LoopbackPath
	if path == "" {
		path = "/callback"
	}
	p.redirect = "http://" + host + ":" + strconv.Itoa(port) + path
	if path == "/" {
		p.redirect = "http://" + host + ":" + strconv.Itoa(port)
	}

	f := newFlow()
	f.AuthURL = p.authURL(req.LoginHint)
	fctx, cancel := context.WithTimeout(context.Background(), m.cfg.LoopbackTimeout)
	f.cancel = cancel
	f.ExpiresAt = m.cfg.Now().Add(m.cfg.LoopbackTimeout)

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cbPath := path
		if r.URL.Path != cbPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != p.state {
			writeCallbackPage(w, false, "Invalid state")
			return
		}
		if e := q.Get("error"); e != "" {
			err := &Error{Code: e, Description: q.Get("error_description")}
			writeCallbackPage(w, false, err.Error())
			f.finish(Grant{}, err)
			return
		}
		g, err := m.exchange(fctx, p, q.Get("code"))
		if err != nil {
			writeCallbackPage(w, false, err.Error())
		} else {
			writeCallbackPage(w, true, "")
		}
		f.finish(g, err)
	})
	for _, l := range listeners {
		go func(l net.Listener) { _ = srv.Serve(l) }(l)
	}
	go func() {
		select {
		case <-f.done:
		case <-fctx.Done():
			f.finish(Grant{}, ErrFlowExpired)
		}
		// Let the browser receive the page before shutting down.
		sctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(sctx)
		c()
		cancel()
	}()
	if err := m.opener()(ctx, f.AuthURL); err != nil {
		m.cfg.Logger.Warn("oauth: opening the browser failed; the URL is shown instead", "err", err)
	}
	return f, nil
}

// CallbackHTML returns the page shown after a redirect.
func CallbackHTML(ok bool, msg string) string {
	title, text := "TuxMail: Anmeldung erfolgreich / Signed in",
		"Sie können dieses Fenster schließen und zu TuxMail zurückkehren. / You can close this window and return to TuxMail."
	if !ok {
		title = "TuxMail: Anmeldung fehlgeschlagen / Sign-in failed"
		text = html.EscapeString(msg)
	}
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + title + `</title>
<style>body{font-family:system-ui,sans-serif;margin:4rem auto;max-width:36rem;color:#222}</style></head>
<body><h1>` + title + `</h1><p>` + text + `</p><script>setTimeout(function(){window.close()},1500)</script></body></html>`
}

func writeCallbackPage(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
	}
	_, _ = w.Write([]byte(CallbackHTML(ok, msg)))
}

// ==== device authorization grant (RFC 8628) ==================================

type deviceResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	VerificationURL string `json:"verification_url"` // Google spelling
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
	Message         string `json:"message"`
}

// StartDevice starts the device code flow (Microsoft). The flow polls the
// token endpoint in the background.
func (m *Manager) StartDevice(ctx context.Context, req StartRequest) (*Flow, error) {
	p, err := m.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	if p.ep.DeviceURL == "" {
		return nil, ErrDeviceUnsupported
	}
	form := url.Values{"client_id": {p.client.ID}, "scope": {strings.Join(p.scopes, " ")}}
	var dr deviceResponse
	if err := postForm(ctx, m.cfg.HTTPClient, p.ep.DeviceURL, form, &dr); err != nil {
		return nil, err
	}
	f := newFlow()
	f.UserCode = dr.UserCode
	f.VerificationURI = dr.VerificationURI
	if f.VerificationURI == "" {
		f.VerificationURI = dr.VerificationURL
	}
	if dr.ExpiresIn <= 0 {
		dr.ExpiresIn = 900
	}
	f.ExpiresAt = m.cfg.Now().Add(time.Duration(dr.ExpiresIn) * time.Second)
	interval := time.Duration(dr.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	fctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Duration(dr.ExpiresIn)*time.Second))
	f.cancel = cancel
	go func() {
		defer cancel()
		res := primaryResource(p.ep, p.scopes)
		for {
			select {
			case <-fctx.Done():
				f.finish(Grant{}, ErrFlowExpired)
				return
			case <-time.After(deviceInterval(interval)):
			}
			tf := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {dr.DeviceCode},
				"client_id": {p.client.ID}}
			if p.client.Secret != "" {
				tf.Set("client_secret", p.client.Secret)
			}
			var tr tokenResponse
			err := postForm(fctx, m.cfg.HTTPClient, p.ep.TokenURL, tf, &tr)
			var oe *Error
			if errors.As(err, &oe) {
				switch oe.Code {
				case "authorization_pending":
					continue
				case "slow_down":
					interval += 5 * time.Second
					continue
				}
			}
			if err != nil {
				f.finish(Grant{}, err)
				return
			}
			f.finish(m.grantFrom(p, res, tr))
			return
		}
	}()
	return f, nil
}

// deviceInterval allows tests to poll faster.
var deviceInterval = func(d time.Duration) time.Duration { return d }
