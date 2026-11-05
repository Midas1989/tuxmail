// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/server/sealed"
)

const pw = "correct horse battery"

// testTransport is the transport of the test clients: New installs the
// SSRF policy (internal/netguard), which also applies to
// http.DefaultTransport and would block the test clients' requests to
// the loopback test server.
var testTransport = &http.Transport{}

func TestMain(m *testing.M) {
	http.DefaultClient = &http.Client{Transport: testTransport}
	os.Exit(m.Run())
}

type env struct {
	t   *testing.T
	cfg *config.Config
	svc *auth.Service
	srv *Server
	ts  *httptest.Server
}

func newEnv(t *testing.T, opts Options, mod ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	for _, m := range mod {
		m(&cfg)
	}
	svc, err := auth.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	svc.PasswordParams = auth.ArgonParams{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 32}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	srv, err := New(&cfg, svc, opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, cfg: &cfg, svc: svc, srv: srv, ts: ts}
}

func (e *env) user(name string, role auth.Role) *auth.User {
	e.t.Helper()
	u, err := e.svc.CreateUser(context.Background(), auth.NewUser{Username: name, Email: name + "@example.de", Role: role, Password: pw})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// browser is a cookie-holding client that sends the CSRF header like the SPA.
type browser struct {
	e      *env
	c      *http.Client
	csrf   string
	bearer string
	header http.Header
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar, Transport: testTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, body any) (*http.Response, map[string]any) {
	b.e.t.Helper()
	var rd io.Reader
	if body != nil {
		js, _ := json.Marshal(body)
		rd = bytes.NewReader(js)
	}
	req, _ := http.NewRequest(method, b.e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		req.Header.Set(csrfHeader, b.csrf)
	}
	if b.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	for k, v := range b.header {
		req.Header[k] = v
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp, m
}

func (b *browser) fetchCSRF() {
	_, m := b.do("GET", "/api/auth/csrf", nil)
	b.csrf, _ = m["csrfToken"].(string)
	if len(b.csrf) < 32 {
		b.e.t.Fatal("no csrf token", m)
	}
}

func (b *browser) login(user string) {
	b.e.t.Helper()
	b.fetchCSRF()
	resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": user, "password": pw})
	if resp.StatusCode != 200 || m["status"] != "ok" {
		b.e.t.Fatal(resp.Status, m)
	}
	b.fetchCSRF() // rotated on login
}

func TestHealthAndHeaders(t *testing.T) {
	e := newEnv(t, Options{Version: "1.2.3"})
	resp, m := e.browser().do("GET", "/healthz", nil)
	if resp.StatusCode != 200 || m["status"] != "ok" || m["version"] != "1.2.3" {
		t.Fatal(resp.Status, m)
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on http public_url")
	}
	resp, m = e.browser().do("GET", "/api/nope", nil)
	if resp.StatusCode != 404 || m["error"] != "not_found" {
		t.Fatal(resp.Status, m)
	}
}

func TestLoginFlowEndToEnd(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("alice", auth.RoleUser)
	b := e.browser()

	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatal("me without session:", resp.Status)
	}
	// Without CSRF token the login is rejected.
	if resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": pw}); resp.StatusCode != 403 || m["error"] != "csrf" {
		t.Fatal(resp.Status, m)
	}
	b.fetchCSRF()
	if resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": "wrong password"}); resp.StatusCode != 401 || m["error"] != "invalid_credentials" {
		t.Fatal(resp.Status, m)
	}
	resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": pw})
	if resp.StatusCode != 200 || m["status"] != "ok" {
		t.Fatal(resp.Status, m)
	}
	var sc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			sc = c
		}
	}
	if sc == nil || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Secure || sc.MaxAge <= 0 {
		t.Fatalf("session cookie: %+v", sc)
	}
	// Old CSRF token is invalid after rotation.
	if resp, _ := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 403 {
		t.Fatal("stale CSRF token accepted", resp.Status)
	}
	b.fetchCSRF()
	resp, m = b.do("GET", "/api/auth/me", nil)
	if resp.StatusCode != 200 || m["user"].(map[string]any)["username"] != "alice" {
		t.Fatal(resp.Status, m)
	}
	_, m = b.do("GET", "/api/auth/sessions", nil)
	sessions := m["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["current"] != true {
		t.Fatal(m)
	}
	// Change password; current session stays valid.
	if resp, m := b.do("POST", "/api/auth/password", map[string]string{"currentPassword": pw, "newPassword": "short"}); resp.StatusCode != 400 || m["error"] != "weak_password" {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := b.do("POST", "/api/auth/password", map[string]string{"currentPassword": pw, "newPassword": "an even better password"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != 200 {
		t.Fatal("session lost after password change")
	}
	// Settings round trip.
	if resp, _ := b.do("PUT", "/api/settings/ui", map[string]string{"theme": "dark"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if _, m := b.do("GET", "/api/settings/ui", nil); m["theme"] != "dark" {
		t.Fatal(m)
	}
	if resp, _ := b.do("POST", "/api/auth/logout", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatal("session valid after logout")
	}
	if n, _ := e.svc.ListSessions(context.Background(), u.ID); len(n) != 0 {
		t.Fatal("session not deleted")
	}
}

func TestTOTPOverHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("bob", auth.RoleUser)
	b := e.browser()
	b.login("bob")
	resp, m := b.do("POST", "/api/auth/totp/setup", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(m["secret"].(string))
	totp := auth.TOTP{Secret: secret}
	resp, m = b.do("POST", "/api/auth/totp/confirm", map[string]string{"code": totp.Code(time.Now())})
	if resp.StatusCode != 200 || len(m["recoveryCodes"].([]any)) != 10 {
		t.Fatal(resp.Status, m)
	}
	recovery := m["recoveryCodes"].([]any)[0].(string)
	recovery2 := m["recoveryCodes"].([]any)[1].(string)

	b2 := e.browser()
	b2.fetchCSRF()
	_, m = b2.do("POST", "/api/auth/login", map[string]string{"username": "bob", "password": pw})
	if m["status"] != "mfa_required" {
		t.Fatal(m)
	}
	if resp, _ := b2.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatal("logged in before second factor")
	}
	resp, m = b2.do("POST", "/api/auth/login/totp", map[string]string{"challenge": m["challenge"].(string), "recoveryCode": recovery})
	if resp.StatusCode != 200 || m["status"] != "ok" {
		t.Fatal(resp.Status, m)
	}
	b2.fetchCSRF()
	_, m = b2.do("GET", "/api/auth/me", nil)
	if m["session"].(map[string]any)["mfa"] != true || m["recoveryCodesLeft"].(float64) != 9 {
		t.Fatal(m)
	}
	// Disabling requires password + second factor.
	if resp, _ := b2.do("POST", "/api/auth/totp/disable", map[string]string{"password": pw, "code": "000000"}); resp.StatusCode != 400 {
		t.Fatal(resp.Status)
	}
	if resp, _ := b2.do("POST", "/api/auth/totp/disable", map[string]string{"password": "wrong password", "recoveryCode": recovery}); resp.StatusCode != 401 {
		t.Fatal("wrong password accepted", resp.Status)
	}
	if resp, _ := b2.do("POST", "/api/auth/totp/disable", map[string]string{"password": pw, "recoveryCode": "zzzzz-zzzzz"}); resp.StatusCode != 400 {
		t.Fatal("bogus recovery code accepted", resp.Status)
	}
	if resp, _ := b2.do("POST", "/api/auth/totp/disable", map[string]string{"password": pw, "recoveryCode": recovery2}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if _, m := b2.do("GET", "/api/auth/me", nil); m["user"].(map[string]any)["totpEnabled"] != false {
		t.Fatal(m)
	}
}

func TestCSRFProtection(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("carol", auth.RoleUser)
	b := e.browser()
	b.login("carol")
	// Foreign origin is rejected even with a valid token.
	b.header = http.Header{"Origin": {"https://evil.example"}}
	if resp, m := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 403 || m["error"] != "csrf" {
		t.Fatal(resp.Status, m)
	}
	// Same origin passes.
	b.header = http.Header{"Origin": {e.ts.URL}}
	if resp, _ := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	// Configured public origin passes too.
	b.header = http.Header{"Origin": {"http://127.0.0.1:8080"}}
	if resp, _ := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	// Wrong token fails.
	b.header = nil
	b.csrf = strings.Repeat("A", 43)
	if resp, _ := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 403 {
		t.Fatal(resp.Status)
	}
	// Safe methods need no token.
	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
}

func TestSecureCookiesForHTTPS(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.PublicURL = "https://mail.example.de" })
	e.user("dora", auth.RoleUser)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/csrf", nil))
	c := rec.Result().Cookies()[0]
	if c.Name != "__Host-"+csrfCookie || !c.Secure || c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("%+v", c)
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS missing")
	}
	var tok struct{ CSRFToken string }
	json.Unmarshal(rec.Body.Bytes(), &tok)
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"dora","password":"`+pw+`"}`))
	req.Header.Set("Origin", "https://mail.example.de")
	req.Header.Set(csrfHeader, tok.CSRFToken)
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, sc := range rec.Result().Cookies() {
		if sc.Name == "__Host-"+sessionCookie && sc.Secure && sc.HttpOnly {
			return
		}
	}
	t.Fatal("secure session cookie missing")
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Limits.LoginRatePerMinute = 3 })
	b := e.browser()
	b.fetchCSRF()
	for i := 0; i < 3; i++ {
		if resp, _ := b.do("POST", "/api/auth/login", map[string]string{"username": "x", "password": "y"}); resp.StatusCode != 401 {
			t.Fatal(i, resp.Status)
		}
	}
	resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": "x", "password": "y"})
	if resp.StatusCode != 429 || m["error"] != "rate_limited" || resp.Header.Get("Retry-After") == "" {
		t.Fatal(resp.Status, m)
	}
	// Another username from the same IP still has budget (per IP+user), up
	// to the per-IP limit.
	if resp, _ := b.do("POST", "/api/auth/login", map[string]string{"username": "z", "password": "y"}); resp.StatusCode != 401 {
		t.Fatal(resp.Status)
	}
}

func TestRateLimiterBucket(t *testing.T) {
	l := newRateLimiter(60, 2)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	if ok, _ := l.allow("k"); !ok {
		t.Fatal()
	}
	if ok, _ := l.allow("k"); !ok {
		t.Fatal()
	}
	ok, wait := l.allow("k")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatal(ok, wait)
	}
	now = now.Add(time.Second)
	if ok, _ := l.allow("k"); !ok {
		t.Fatal("not refilled")
	}
	now = now.Add(time.Hour)
	l.prune()
	if len(l.buckets) != 0 {
		t.Fatal("not pruned")
	}
}

func TestAdminAuthorization(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("root", auth.RoleAdmin)
	e.user("eve", auth.RoleUser)
	user := e.browser()
	user.login("eve")
	if resp, m := user.do("GET", "/api/admin/users", nil); resp.StatusCode != 403 || m["error"] != "forbidden" {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := e.browser().do("GET", "/api/admin/users", nil); resp.StatusCode != 401 {
		t.Fatal(resp.Status)
	}
	admin := e.browser()
	admin.login("root")
	_, m := admin.do("GET", "/api/admin/users", nil)
	if len(m["users"].([]any)) != 2 {
		t.Fatal(m)
	}
	resp, m := admin.do("POST", "/api/admin/users", map[string]string{"username": "fred", "email": "fred@example.de", "role": "readonly"})
	if resp.StatusCode != 201 || len(m["password"].(string)) < 20 {
		t.Fatal(resp.Status, m)
	}
	fredID := int(m["user"].(map[string]any)["id"].(float64))
	fredPW := m["password"].(string)
	resp, m = admin.do("PATCH", "/api/admin/users/"+itoa(fredID), map[string]any{"role": "user", "disabled": true, "displayName": "Fred"})
	if resp.StatusCode != 200 || m["user"].(map[string]any)["disabled"] != true || m["user"].(map[string]any)["role"] != "user" {
		t.Fatal(resp.Status, m)
	}
	fred := e.browser()
	fred.fetchCSRF()
	if resp, _ := fred.do("POST", "/api/auth/login", map[string]string{"username": "fred", "password": fredPW}); resp.StatusCode != 401 {
		t.Fatal("disabled user logged in", resp.Status)
	}
	admin.do("PATCH", "/api/admin/users/"+itoa(fredID), map[string]any{"disabled": false})
	_, m = admin.do("POST", "/api/admin/users/"+itoa(fredID)+"/reset-password", nil)
	if len(m["password"].(string)) < 20 {
		t.Fatal(m)
	}
	if resp, _ := admin.do("POST", "/api/admin/users/"+itoa(fredID)+"/reset-2fa", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	// Last admin cannot demote themselves.
	_, me := admin.do("GET", "/api/auth/me", nil)
	rootID := int(me["user"].(map[string]any)["id"].(float64))
	if resp, m := admin.do("PATCH", "/api/admin/users/"+itoa(rootID), map[string]any{"role": "user"}); resp.StatusCode != 409 || m["error"] != "last_admin" {
		t.Fatal(resp.Status, m)
	}
	// Groups, templates, audit.
	resp, m = admin.do("POST", "/api/admin/groups", map[string]string{"name": "sales"})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	gid := int(m["group"].(map[string]any)["id"].(float64))
	if resp, _ := admin.do("PUT", "/api/admin/groups/"+itoa(gid)+"/members/"+itoa(fredID), nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	resp, m = admin.do("POST", "/api/admin/provisioning/templates", map[string]any{"name": "firma", "groupId": gid,
		"body": map[string]any{"accounts": []any{map[string]any{"kind": "imap", "incoming": map[string]any{"host": "imap.example.de", "username": "%EMAIL%"}}}}})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	_, m = admin.do("GET", "/api/admin/audit?limit=500", nil)
	if len(m["entries"].([]any)) < 5 {
		t.Fatal(m)
	}
	if resp, _ := admin.do("DELETE", "/api/admin/users/"+itoa(fredID), nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	// Invites need registration: invite.
	if resp, _ := admin.do("POST", "/api/admin/invites", map[string]any{}); resp.StatusCode != 409 {
		t.Fatal(resp.Status)
	}
}

func TestAdminTOTPRequired(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	e.user("root", auth.RoleAdmin)
	b := e.browser()
	b.login("root")
	if resp, m := b.do("GET", "/api/admin/users", nil); resp.StatusCode != 403 || m["error"] != "mfa_setup_required" {
		t.Fatal(resp.Status, m)
	}
}

func TestInviteRegistration(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Auth.Registration = "invite" })
	e.user("root", auth.RoleAdmin)
	admin := e.browser()
	admin.login("root")
	resp, m := admin.do("POST", "/api/admin/invites", map[string]any{"email": "neu@example.de", "expiresInHours": 2})
	if resp.StatusCode != 201 || !strings.Contains(m["url"].(string), "/register?invite=") {
		t.Fatal(resp.Status, m)
	}
	tok := m["token"].(string)
	b := e.browser()
	b.fetchCSRF()
	if _, m := b.do("GET", "/api/auth/invites/"+tok, nil); m["email"] != "neu@example.de" {
		t.Fatal(m)
	}
	resp, m = b.do("POST", "/api/auth/register", map[string]string{"inviteToken": tok, "username": "neu", "email": "neu@example.de", "password": pw})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	b.fetchCSRF()
	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != 200 {
		t.Fatal("not logged in after registration")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestTrustedProxy(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("gil", auth.RoleUser)
	b := e.browser()
	b.header = http.Header{"X-Forwarded-For": {"198.51.100.7, 127.0.0.1"}, "X-Forwarded-Proto": {"https"}}
	b.login("gil")
	_, m := b.do("GET", "/api/auth/me", nil)
	if ip := m["session"].(map[string]any)["ip"]; ip != "198.51.100.7" {
		t.Fatal("forwarded IP not used:", ip)
	}
	// Untrusted peer: header ignored.
	e2 := newEnv(t, Options{}, func(c *config.Config) { c.TrustedProxies = nil })
	e2.user("gil", auth.RoleUser)
	b2 := e2.browser()
	b2.header = http.Header{"X-Forwarded-For": {"198.51.100.7"}}
	b2.login("gil")
	_, m = b2.do("GET", "/api/auth/me", nil)
	if ip := m["session"].(map[string]any)["ip"]; ip != "127.0.0.1" {
		t.Fatal("spoofed IP accepted:", ip)
	}
}

func TestStaticSPA(t *testing.T) {
	e := newEnv(t, Options{})
	resp, err := http.Get(e.ts.URL + "/mail/inbox")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "TuxMail") {
		t.Fatal("placeholder not served")
	}
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>APP</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	e2 := newEnv(t, Options{Static: fsys})
	e2.user("spa", auth.RoleUser)
	br := e2.browser()
	get := func(p string) (int, string, http.Header) {
		resp, err := br.c.Get(e2.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	// Without a session the SPA redirects to the login page.
	if code, _, h := get("/settings/accounts"); code != http.StatusSeeOther || !strings.HasPrefix(h.Get("Location"), "/login?next=") {
		t.Fatal("login redirect", code, h)
	}
	if code, body, _ := get("/login"); code != 200 || !strings.Contains(body, "/login.js") {
		t.Fatal("login page", code)
	}
	br.login("spa")
	isApp := func(body string) bool {
		return strings.Contains(body, "APP") && strings.Contains(body, `<meta name="tuxmail-api" content="http">`)
	}
	if code, body, _ := get("/"); code != 200 || !isApp(body) {
		t.Fatal(code, body)
	}
	if code, body, _ := get("/settings/accounts"); code != 200 || !isApp(body) {
		t.Fatal("SPA fallback", code, body)
	}
	if code, body, h := get("/assets/app.js"); code != 200 || body != "console.log(1)" || !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Fatal(code, body, h)
	}
	if code, _, _ := get("/assets/missing.js"); code != 404 {
		t.Fatal(code)
	}
	if code, _, _ := get("/../../etc/passwd"); code != 200 && code != http.StatusSeeOther && code != http.StatusMovedPermanently && code != http.StatusTemporaryRedirect {
		t.Fatal(code)
	}
}

func TestPushEndpoints(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("hal", auth.RoleUser)
	b := e.browser()
	_, m := b.do("GET", "/api/push/vapid-public-key", nil)
	if k, _ := m["publicKey"].(string); len(k) < 80 {
		t.Fatal(m)
	}
	b.login("hal")
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	sub := map[string]any{
		"endpoint":   "https://push.example.net/send/abc",
		"keys":       map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(authSecret)},
		"deviceName": "Pixel",
	}
	if resp, m := b.do("POST", "/api/push/subscribe", sub); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := b.do("POST", "/api/push/subscribe", map[string]any{"endpoint": "http://x"}); resp.StatusCode != 400 {
		t.Fatal(resp.Status)
	}
	_, m = b.do("GET", "/api/push/subscriptions", nil)
	subs := m["subscriptions"].([]any)
	if len(subs) != 1 || subs[0].(map[string]any)["deviceName"] != "Pixel" {
		t.Fatal(m)
	}
	if resp, m := b.do("PUT", "/api/push/preferences", map[string]any{"enabled": true, "showContent": false,
		"quietHours": map[string]string{"start": "22:00", "end": "06:30", "timezone": "Europe/Berlin"}, "channels": map[string]bool{"chat": false}}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	_, m = b.do("GET", "/api/push/preferences", nil)
	if m["showContent"] != false || m["quietHours"].(map[string]any)["end"] != "06:30" {
		t.Fatal(m)
	}
	if resp, _ := b.do("PUT", "/api/push/preferences", map[string]any{"quietHours": map[string]string{"start": "x", "end": "y"}}); resp.StatusCode != 400 {
		t.Fatal(resp.Status)
	}
	if resp, _ := b.do("DELETE", "/api/push/subscribe", map[string]string{"endpoint": "https://push.example.net/send/abc"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if _, m := b.do("GET", "/api/push/subscriptions", nil); len(m["subscriptions"].([]any)) != 0 {
		t.Fatal(m)
	}
	if e.srv.Notifier() == nil {
		t.Fatal("no notifier")
	}
}

func TestDeviceFlowAndProvisioning(t *testing.T) {
	var userDir string
	e := newEnv(t, Options{MailData: func(ctx context.Context, dir string) (*MailData, error) {
		userDir = dir
		return &MailData{
			Accounts:   []domain.Account{{ID: 1, Kind: domain.KindIMAP, DisplayName: "Work", SecretRef: "acct-1"}},
			Identities: []domain.Identity{{ID: 1, AccountID: 1, Email: "ivy@example.de"}},
			Signatures: []domain.Signature{},
		}, nil
	}})
	u := e.user("ivy", auth.RoleUser)
	vault, err := e.svc.UserSecrets(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	vault.Set("acct-1", []byte("imap-secret"))
	e.svc.CreateTemplate(context.Background(), auth.Template{Name: "corp", Body: json.RawMessage(`{"smtp":"smtp.%DOMAIN%"}`)})

	clientKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	desktop := e.browser() // no cookies used, no CSRF token
	resp, m := desktop.do("POST", "/api/device/authorize", map[string]string{"client_name": "Ivy's Laptop",
		"client_public_key": base64.RawURLEncoding.EncodeToString(clientKey.PublicKey().Bytes())})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	deviceCode, userCode := m["device_code"].(string), m["user_code"].(string)
	if !strings.HasSuffix(m["verification_uri"].(string), "/device") || !strings.Contains(m["verification_uri_complete"].(string), "user_code=") {
		t.Fatal(m)
	}
	if _, m := desktop.do("POST", "/api/device/token", map[string]string{"device_code": deviceCode}); m["error"] != "authorization_pending" {
		t.Fatal(m)
	}
	// User approves in the browser.
	web := e.browser()
	web.login("ivy")
	_, m = web.do("GET", "/api/device/request?user_code="+userCode, nil)
	if m["request"].(map[string]any)["clientName"] != "Ivy's Laptop" {
		t.Fatal(m)
	}
	if resp, m := web.do("POST", "/api/device/approve", map[string]string{"user_code": userCode}); resp.StatusCode != 204 {
		t.Fatal(resp.Status, m)
	}
	// Poll interval enforced by the service clock; advance it.
	e.svc.Now = func() time.Time { return time.Now().Add(time.Minute) }
	resp, m = desktop.do("POST", "/api/device/token", map[string]string{"device_code": deviceCode})
	if resp.StatusCode != 200 || m["token_type"] != "Bearer" {
		t.Fatal(resp.Status, m)
	}
	e.svc.Now = time.Now
	desktop.bearer = m["access_token"].(string)

	// Bearer requests bypass CSRF (no ambient credentials).
	_, m = desktop.do("GET", "/api/provisioning/profile", nil)
	if m["mode"].(map[string]any)["recommended"] != ModeThin || len(m["accounts"].([]any)) != 1 || len(m["templates"].([]any)) != 1 {
		t.Fatal(m)
	}
	if m["secrets"].(map[string]any)["available"] != true {
		t.Fatal(m["secrets"])
	}
	if userDir != e.svc.UserDir(u.ID) {
		t.Fatal(userDir)
	}
	resp, m = desktop.do("GET", "/api/provisioning/secrets", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "imap-secret") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("imap-secret"))) {
		t.Fatal("secret in plain text")
	}
	var box sealed.Box
	json.Unmarshal(raw, &box)
	plain, err := sealed.Open(clientKey, &box)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Secrets map[string][]byte }
	json.Unmarshal(plain, &got)
	if string(got.Secrets["acct-1"]) != "imap-secret" {
		t.Fatal(string(plain))
	}
	// Device token is listed with the sessions and revocable.
	_, m = web.do("GET", "/api/auth/sessions", nil)
	var deviceID float64
	for _, s := range m["sessions"].([]any) {
		if s.(map[string]any)["kind"] == "device" {
			deviceID = s.(map[string]any)["id"].(float64)
		}
	}
	if deviceID == 0 {
		t.Fatal("device not listed", m)
	}
	// A browser session cannot fetch secrets.
	if resp, _ := web.do("GET", "/api/provisioning/secrets", nil); resp.StatusCode != 403 {
		t.Fatal(resp.Status)
	}
	if resp, _ := web.do("DELETE", "/api/auth/sessions/"+itoa(int(deviceID)), nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := desktop.do("GET", "/api/provisioning/profile", nil); resp.StatusCode != 401 {
		t.Fatal("revoked device token still valid", resp.Status)
	}
}

func TestDeviceDirectLogin(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("jo", auth.RoleUser)
	enr, _ := e.svc.BeginTOTP(context.Background(), u.ID)
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	totp := auth.TOTP{Secret: secret}
	e.svc.Now = func() time.Time { return time.Now().Add(-time.Minute) }
	if _, err := e.svc.ConfirmTOTP(context.Background(), u.ID, totp.Code(e.svc.Now())); err != nil {
		t.Fatal(err)
	}
	e.svc.Now = time.Now
	c := e.browser()
	resp, m := c.do("POST", "/api/device/login", map[string]string{"username": "jo", "password": pw, "device_name": "Desktop"})
	if resp.StatusCode != 401 || m["error"] != "mfa_required" {
		t.Fatal(resp.Status, m)
	}
	resp, m = c.do("POST", "/api/device/login", map[string]string{"username": "jo", "password": pw, "device_name": "Desktop",
		"totp_code": totp.Code(time.Now())})
	if resp.StatusCode != 200 || m["access_token"] == nil {
		t.Fatal(resp.Status, m)
	}
	c.bearer = m["access_token"].(string)
	_, m = c.do("GET", "/api/auth/me", nil)
	if m["session"].(map[string]any)["kind"] != "device" || m["session"].(map[string]any)["deviceName"] != "Desktop" {
		t.Fatal(m)
	}
	// No client key registered: secrets endpoint explains.
	if resp, m := c.do("GET", "/api/provisioning/secrets", nil); resp.StatusCode != 409 {
		t.Fatal(resp.Status, m)
	}
}

func TestUnixSocketAndShutdown(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "run", "tuxmail.sock")
	e := newEnv(t, Options{}, func(c *config.Config) { c.Listen = "unix:" + sock })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.srv.Run(ctx) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		resp, err = client.Get("http://tuxmail/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no graceful shutdown")
	}
}
