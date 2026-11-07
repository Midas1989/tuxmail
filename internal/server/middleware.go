// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

type ctxKey int

const (
	ctxClient ctxKey = iota
	ctxAuth
	ctxCSRF
	ctxRequestID
	ctxCSPNonce
)

// client is the resolved peer of a request (after trusted proxy handling).
type client struct {
	IP     string
	Secure bool // request reached the public endpoint via HTTPS
}

func clientFrom(r *http.Request) client {
	c, _ := r.Context().Value(ctxClient).(client)
	return c
}

// authInfo is attached to authenticated requests.
type authInfo struct {
	User    *auth.User
	Session *auth.Session
	Token   string
	Bearer  bool // authenticated via Authorization header (device token)
}

func authFrom(r *http.Request) *authInfo {
	a, _ := r.Context().Value(ctxAuth).(*authInfo)
	return a
}

func (s *Server) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientInfo resolves the client IP and scheme. X-Forwarded-For and
// X-Forwarded-Proto are only honoured when the direct peer is a trusted proxy
// (or the connection came through the unix socket, which only the local
// reverse proxy can reach). The client IP is the right-most XFF entry that is
// not itself a trusted proxy.
func (s *Server) clientInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := client{Secure: r.TLS != nil}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, perr := netip.ParseAddr(host)
		trusted := (perr == nil && s.isTrusted(peer)) || (perr != nil && (host == "" || host == "@"))
		if perr == nil {
			c.IP = peer.Unmap().String()
		} else {
			c.IP = "local"
		}
		if trusted {
			if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
				parts := strings.Split(strings.Join(xff, ","), ",")
				for i := len(parts) - 1; i >= 0; i-- {
					a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
					if err != nil {
						break
					}
					c.IP = a.Unmap().String()
					if !s.isTrusted(a) {
						break
					}
				}
			} else if xr := r.Header.Get("X-Real-Ip"); xr != "" {
				if a, err := netip.ParseAddr(strings.TrimSpace(xr)); err == nil {
					c.IP = a.Unmap().String()
				}
			}
			if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
				c.Secure = strings.EqualFold(strings.TrimSpace(strings.Split(p, ",")[0]), "https")
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxClient, c)))
	})
}

// withRequestID gives every request an id: in the response header X-Request-ID
// and in the log, so that a support case can be matched (Basis spec/BETRIEB.md).
// An id sent by the client is not taken over, it would end up in the log.
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := randomToken()[:16]
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxRequestID).(string)
	return id
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic in handler", "request_id", requestID(r), "panic", v, "path", logPath(r.URL.Path), "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack supports WebSocket upgrades (/api/events).
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// secretPathPrefixes are paths whose rest is a secret token.
var secretPathPrefixes = []string{"/api/auth/invites/", "/api/io/downloads/"}

// logPath is the path of a request as it may appear in the log.
func logPath(p string) string {
	for _, prefix := range secretPathPrefixes {
		if strings.HasPrefix(p, prefix) {
			return prefix + "********"
		}
	}
	return p
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		lvl := s.log.Debug
		if isAPI(r.URL.Path) && r.URL.Path != "/healthz" {
			lvl = s.log.Info
		}
		// Never log query strings: they may contain codes or tokens.
		lvl("http request", "method", r.Method, "path", logPath(r.URL.Path), "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "ip", clientFrom(r).IP, "request_id", requestID(r))
	})
}

// contentSecurityPolicy is Basis spec/PARAMETER.md with one nonce per request for
// scripts and one for <style> elements. Only the style nonce can be read by page
// scripts (<meta name="tuxmail-style-nonce">, needed for the srcdoc documents of the
// mail view, which inherit this policy), so it is no help for injecting a script.
//
// style-src-attr 'unsafe-inline' is a documented deviation
// (docs/security/BASIS-ABWEICHUNGEN.md): a style attribute cannot carry a nonce,
// and HTML mail, shown in a srcdoc frame that inherits this policy, is made of them.
func contentSecurityPolicy(scriptNonce, styleNonce string) string {
	return "default-src 'self'; script-src 'self' 'nonce-" + scriptNonce + "' 'strict-dynamic'; script-src-attr 'none'; " +
		"style-src 'self' 'nonce-" + styleNonce + "'; style-src-attr 'unsafe-inline'; " +
		"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self' blob:; " +
		"worker-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// cspNonces are the nonces of a request.
type cspNonces struct{ script, style string }

func noncesFrom(r *http.Request) cspNonces {
	n, _ := r.Context().Value(ctxCSPNonce).(cspNonces)
	return n
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	hsts := s.cfg.SecureCookies() && s.cfg.Security.HSTS
	headers := s.cfg.Security.Headers
	// Two years (Basis spec/PARAMETER.md). security.hsts_include_subdomains can
	// switch off includeSubDomains, "preload" is never sent.
	hstsValue := "max-age=63072000"
	if s.cfg.Security.HSTSIncludeSubdomains {
		hstsValue += "; includeSubDomains"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if headers {
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
			nonces := cspNonces{script: randomToken()[:22], style: randomToken()[:22]}
			h.Set("Content-Security-Policy", contentSecurityPolicy(nonces.script, nonces.style))
			r = r.WithContext(context.WithValue(r.Context(), ctxCSPNonce, nonces))
		}
		if hsts {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		if isAPI(r.URL.Path) || r.URL.Path == "/healthz" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody enforces limits.max_upload_mb for every request body, also
// without a reverse proxy in front (nginx applies client_max_body_size);
// individual endpoints may limit further (A-09).
func (s *Server) limitBody(next http.Handler) http.Handler {
	limit := int64(s.cfg.Limits.MaxUploadMB) << 20
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("request body exceeds %d MB", s.cfg.Limits.MaxUploadMB))
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// Cookie names. With HTTPS the __Host- prefix pins cookies to this origin.
func (s *Server) cookieName(base string) string {
	if s.cfg.SecureCookies() {
		return "__Host-" + base
	}
	return base
}

const (
	sessionCookie = "tuxmail_session"
	csrfCookie    = "tuxmail_csrf"
	oidcCookie    = "tuxmail_oidc"
	csrfHeader    = "X-CSRF-Token"
)

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration, httpOnly bool, same http.SameSite) {
	c := &http.Cookie{
		Name: s.cookieName(name), Value: value, Path: "/", HttpOnly: httpOnly,
		Secure: s.cfg.SecureCookies(), SameSite: same,
	}
	if maxAge > 0 {
		c.MaxAge = int(maxAge / time.Second)
	} else if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (s *Server) cookie(r *http.Request, name string) string {
	c, err := r.Cookie(s.cookieName(name))
	if err != nil {
		return ""
	}
	return c.Value
}

// csrfExempt endpoints are called by native clients without cookies and
// return tokens in the response body only: there is no session a forged request
// could abuse, and the answer does not reach a cross-site caller.
var csrfExempt = map[string]bool{
	"/api/device/authorize": true,
	"/api/device/token":     true,
	"/api/device/login":     true,
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// csrfTokenFor is the token of a session: HMAC-SHA256(session secret,
// "basis-csrf-v1") in hex (Basis spec/WEB.md, python/basis_sec/csrf.py). The
// session secret is the value of the session cookie.
func csrfTokenFor(sessionSecret string) string {
	m := hmac.New(sha256.New, []byte(sessionSecret))
	m.Write([]byte("basis-csrf-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

// wsCSRFProtocol is the subprotocol prefix that carries the CSRF token of a
// browser's WebSocket handshake: browsers cannot set headers there.
const wsCSRFProtocol = "tuxmail.csrf."

// csrf protects cookie-authenticated requests with two independent checks
// (Basis spec/WEB.md).
//
//  1. Origin: state-changing requests need Sec-Fetch-Site same-origin or none;
//     without that header Origin or Referer must be ours; without both they are
//     refused (originAllowed).
//  2. Token: X-CSRF-Token must be the HMAC of the session cookie. Before a login
//     there is no session; the login requests themselves carry a double-submit
//     token instead (SameSite=Strict cookie), which is replaced by the HMAC when
//     the session starts.
//
// The WebSocket handshake with a session cookie is a GET but changes state in the
// sense of the specification: it passes the same checks, with the token as
// subprotocol. Requests authenticated with an Authorization header (device
// tokens) carry no ambient credentials and are exempt.
func (s *Server) csrf(next http.Handler) http.Handler {
	public := s.cfg.PublicOrigin()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookieVal := s.cookie(r, csrfCookie)
		var want, current string
		if sess := s.cookie(r, sessionCookie); sess != "" {
			want = csrfTokenFor(sess)
			current = want
			if cookieVal != want {
				s.setCookie(w, csrfCookie, current, 0, false, http.SameSiteStrictMode)
			}
		} else {
			want, current = cookieVal, cookieVal
			if len(want) < 32 {
				want = "" // missing or not issued by us: the request below must fail
				current = randomToken()
				s.setCookie(w, csrfCookie, current, 0, false, http.SameSiteStrictMode)
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxCSRF, current))
		exempt := !isAPI(r.URL.Path) || csrfExempt[r.URL.Path] || bearerToken(r) != ""
		switch {
		case exempt:
		case r.URL.Path == "/api/events" && r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket"):
			if !s.originAllowed(r, public) {
				writeError(w, http.StatusForbidden, "csrf", "cross-origin request rejected")
				return
			}
			if !tokenMatches(wsCSRFToken(r), want) {
				writeError(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
				return
			}
		case !safeMethod(r.Method):
			if !s.originAllowed(r, public) {
				writeError(w, http.StatusForbidden, "csrf", "cross-origin request rejected")
				return
			}
			if !tokenMatches(r.Header.Get(csrfHeader), want) {
				writeError(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func tokenMatches(got, want string) bool {
	return got != "" && want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// wsCSRFToken returns the token a browser offered as WebSocket subprotocol.
func wsCSRFToken(r *http.Request) string {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if t, ok := strings.CutPrefix(strings.TrimSpace(p), wsCSRFProtocol); ok {
				return t
			}
		}
	}
	return ""
}

// originAllowed is the first CSRF layer, see csrf. An Origin that a request does
// carry must be ours in any case, also next to Sec-Fetch-Site.
func (s *Server) originAllowed(r *http.Request, public string) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			return site != "" // neither Sec-Fetch-Site nor Origin nor Referer: refuse
		}
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		origin = u.Scheme + "://" + u.Host
	}
	if strings.EqualFold(origin, public) {
		return true
	}
	// Direct access without the proxy (e.g. http://127.0.0.1:8080 during setup).
	scheme := "http"
	if clientFrom(r).Secure {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// Browsers cannot set headers on WebSockets: native clients pass the
	// device token as subprotocol "tuxmail.bearer.<token>" next to
	// "tuxmail.v1" (the server answers with "tuxmail.v1").
	if r.URL.Path == "/api/events" {
		for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
			for _, p := range strings.Split(v, ",") {
				if t, ok := strings.CutPrefix(strings.TrimSpace(p), "tuxmail.bearer."); ok && t != "" {
					return t
				}
			}
		}
	}
	return ""
}

// wsOriginAllowed checks the Origin of a WebSocket handshake: the public
// origin or the direct host; with a device token (no ambient credentials)
// also the WebView origins of the native apps.
func (s *Server) wsOriginAllowed(r *http.Request, origin string) bool {
	if bearerToken(r) != "" {
		switch strings.ToLower(origin) {
		case "https://localhost", "http://localhost", "capacitor://localhost":
			return true
		}
	}
	rr := r.Clone(r.Context())
	rr.Header.Set("Origin", origin)
	return s.originAllowed(rr, s.cfg.PublicOrigin())
}

// activityHeader is set by the web client on requests within a moment of the
// user's input (frontend/src/lib/activity.ts). Polling, refetches after a
// server event and the event stream do not carry it.
const activityHeader = "X-TuxMail-Activity"

// userActivity reports whether a request extends the idle limit of a web session.
// The event stream never does. A client could send the header without any input,
// but one that has the session can keep it alive only up to the absolute limit.
func userActivity(r *http.Request) bool {
	return r.Header.Get(activityHeader) == "1" && r.URL.Path != "/api/events"
}

// authenticate resolves the session cookie or bearer token.
func (s *Server) authenticate(r *http.Request) (*authInfo, error) {
	tok, bearer := bearerToken(r), true
	if tok == "" {
		tok, bearer = s.cookie(r, sessionCookie), false
	}
	if tok == "" {
		return nil, auth.ErrNotFound
	}
	ctx := r.Context()
	if userActivity(r) {
		ctx = auth.WithUserActivity(ctx)
	}
	sess, u, err := s.auth.Authenticate(ctx, tok, clientFrom(r).IP)
	if err != nil {
		return nil, err
	}
	return &authInfo{User: u, Session: sess, Token: tok, Bearer: bearer}, nil
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, a *authInfo)

func (s *Server) withAuth(check func(http.ResponseWriter, *authInfo) bool, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := s.authenticate(r)
		if err != nil {
			if errors.Is(err, auth.ErrUserDisabled) {
				writeError(w, http.StatusForbidden, "user_disabled", "account is disabled")
				return
			}
			if !errors.Is(err, auth.ErrNotFound) {
				s.log.Error("authenticate", "err", err)
			}
			writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		if check != nil && !check(w, a) {
			return
		}
		ctx := context.WithValue(r.Context(), ctxAuth, a)
		ctx = auth.WithActor(ctx, auth.Actor{UserID: a.User.ID, Name: a.User.Username, IP: clientFrom(r).IP})
		h(w, r.WithContext(ctx), a)
	}
}

// requireScope lets browser sessions through and device tokens only with scope.
func requireScope(scope string) func(http.ResponseWriter, *authInfo) bool {
	return func(w http.ResponseWriter, a *authInfo) bool {
		if a.Session.Kind == auth.KindDevice && !a.Session.HasScope(scope) {
			writeError(w, http.StatusForbidden, "insufficient_scope", "this device token does not allow this ("+scope+")")
			return false
		}
		return true
	}
}

// signedIn requires any valid session or device token, whatever its scopes: for
// what concerns only the session itself (who am I, sign out).
func (s *Server) signedIn(h handlerFunc) http.HandlerFunc { return s.withAuth(nil, h) }

// user requires a logged-in user (cookie session or device token with the scope
// for the user's own data).
func (s *Server) user(h handlerFunc) http.HandlerFunc {
	return s.withAuth(requireScope(auth.ScopeData), h)
}

// provisioning requires a cookie session or a device token with that scope.
func (s *Server) provisioning(h handlerFunc) http.HandlerFunc {
	return s.withAuth(requireScope(auth.ScopeProvisioning), h)
}

// admin requires the admin role and, if configured, an enrolled second factor.
// Administration is impossible with a device token, whoever it belongs to: it is
// a long-lived credential on a device that can get lost (AUDIT-A I-01).
func (s *Server) admin(h handlerFunc) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, a *authInfo) bool {
		if a.Session.Kind == auth.KindDevice {
			writeError(w, http.StatusForbidden, "device_token_forbidden", "administration needs a browser session")
			return false
		}
		if !a.User.IsAdmin() {
			writeError(w, http.StatusForbidden, "forbidden", "administrator role required")
			return false
		}
		if s.auth.MFASetupRequired(a.User, a.Session) {
			writeError(w, http.StatusForbidden, "mfa_setup_required", "set up two-factor authentication first")
			return false
		}
		return true
	}, h)
}

// device requires a device token (Authorization: Bearer).
func (s *Server) device(h handlerFunc) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, a *authInfo) bool {
		if !a.Bearer || a.Session.Kind != auth.KindDevice {
			writeError(w, http.StatusForbidden, "device_token_required", "a device token is required")
			return false
		}
		return true
	}, h)
}

func refuseDevice(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "device_token_forbidden", "this needs a browser session")
}

// session lets browser sessions through, no device tokens, whatever their scopes:
// for what protects the account. A device token is a long-lived credential on a
// device that can get lost; it must not be able to lock the owner out or add ways in.
func (s *Server) session(h handlerFunc) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, a *authInfo) bool {
		if a.Session.Kind == auth.KindDevice {
			refuseDevice(w)
			return false
		}
		return true
	}, h)
}

// deviceForbiddenAppRoutes are the routes of the application API that device tokens
// may not use, although they belong to the user's own data: they hand out key
// material or decide what is trusted. The reasons for all sensitive routes:
// docs/security/BASIS-ABWEICHUNGEN.md, "Geräte-Token: Routen". A test keeps this
// list complete (TestDeviceTokenSensitiveRoutesAreClassified).
var deviceForbiddenAppRoutes = map[string]bool{
	"POST /api/comfort/pgp/keys/{id}/secret":         true, // exports the private key
	"POST /api/comfort/pgp/keys/{id}/passphrase":     true,
	"DELETE /api/comfort/pgp/keys/{id}":              true,
	"PUT /api/comfort/pgp/keys/{id}/trust":           true,
	"POST /api/comfort/pgp/peers/accept":             true,
	"POST /api/comfort/pgp/peers/verify":             true,
	"POST /api/comfort/pgp/peers/delete":             true,
	"DELETE /api/comfort/smime/certificates/{id}":    true,
	"PUT /api/comfort/smime/certificates/{id}/trust": true,
	"PUT /api/oauth/clients/{provider}":              true, // whose app gets the mail tokens
	"DELETE /api/oauth/clients/{provider}":           true,
}

// deviceProvisioning is device plus the provisioning scope.
func (s *Server) deviceProvisioning(h handlerFunc) http.HandlerFunc {
	return s.device(func(w http.ResponseWriter, r *http.Request, a *authInfo) {
		if requireScope(auth.ScopeProvisioning)(w, a) {
			h(w, r, a)
		}
	})
}
