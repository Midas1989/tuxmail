// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// A device client as it is on the wire: no cookies, no Origin, no CSRF token.
func deviceClient(t *testing.T, e *env) *browser {
	t.Helper()
	b := e.browser()
	b.noOrigin = true
	return b
}

func deviceLoginHTTP(t *testing.T, e *env, name string, scope string) (*browser, map[string]any) {
	t.Helper()
	b := deviceClient(t, e)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"username": name, "password": pw, "device_name": "Handy",
		"client_public_key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}
	if scope != "" {
		body["scope"] = scope
	}
	resp, m := b.do("POST", "/api/device/login", body)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	b.bearer = m["access_token"].(string)
	return b, m
}

func wantStatus(t *testing.T, b *browser, method, path string, want int, code string) {
	t.Helper()
	resp, m := b.do(method, path, nil)
	if resp.StatusCode != want || (code != "" && m["error"] != code) {
		t.Fatalf("%s %s: %s %v, want %d %s", method, path, resp.Status, m, want, code)
	}
}

// Verwaltung is impossible with a device token, even of an administrator.
func TestDeviceTokenNeverAdministers(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("chef", auth.RoleAdmin)
	b, m := deviceLoginHTTP(t, e, "chef", "")
	if m["scope"] != "data provisioning" {
		t.Fatal(m["scope"])
	}
	for _, p := range []string{"/api/admin/users", "/api/admin/audit", "/api/admin/groups", "/api/admin/backup", "/api/admin/provisioning/templates"} {
		wantStatus(t, b, "GET", p, 403, "device_token_forbidden")
	}
	if resp, m := b.do("POST", "/api/admin/users", map[string]string{"username": "x"}); resp.StatusCode != 403 || m["error"] != "device_token_forbidden" {
		t.Fatal(resp.Status, m)
	}
	// As a cookie, too: the check goes by the kind of session, not the transport.
	c := e.browser()
	c.fetchCSRF()
	u := c.cookieJarURL()
	c.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: b.bearer, Path: "/"}})
	c.fetchCSRF()
	wantStatus(t, c, "GET", "/api/admin/users", 403, "device_token_forbidden")
	// The same user in a browser may.
	w := e.browser()
	w.login("chef")
	wantStatus(t, w, "GET", "/api/admin/users", 200, "")
}

func TestDeviceTokenScopes(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)

	// Default, as old clients get it.
	all, _ := deviceLoginHTTP(t, e, "ida", "")
	wantStatus(t, all, "GET", "/api/settings", 200, "")
	wantStatus(t, all, "GET", "/api/provisioning/profile", 200, "")
	wantStatus(t, all, "GET", "/api/provisioning/secrets", 200, "")

	// Own data only (the Android app).
	data, m := deviceLoginHTTP(t, e, "ida", "data")
	if m["scope"] != "data" {
		t.Fatal(m["scope"])
	}
	wantStatus(t, data, "GET", "/api/settings", 200, "")
	wantStatus(t, data, "GET", "/api/provisioning/profile", 403, "insufficient_scope")
	wantStatus(t, data, "GET", "/api/provisioning/secrets", 403, "insufficient_scope")

	// Provisioning only.
	prov, _ := deviceLoginHTTP(t, e, "ida", "provisioning")
	wantStatus(t, prov, "GET", "/api/provisioning/profile", 200, "")
	wantStatus(t, prov, "GET", "/api/settings", 403, "insufficient_scope")
	wantStatus(t, prov, "GET", "/api/auth/me", 200, "")
	wantStatus(t, prov, "POST", "/api/auth/logout", 204, "")
	wantStatus(t, prov, "GET", "/api/auth/me", 401, "")

	// Unknown scopes and "admin" are refused, not ignored.
	for _, bad := range []string{"admin", "data admin", "*"} {
		c := deviceClient(t, e)
		resp, m := c.do("POST", "/api/device/login", map[string]string{"username": "ida", "password": pw, "scope": bad})
		if resp.StatusCode != 400 || m["error"] != "invalid_scope" {
			t.Fatal(bad, resp.Status, m)
		}
		resp, m = c.do("POST", "/api/device/authorize", map[string]string{"client_name": "x", "scope": bad})
		if resp.StatusCode != 400 || m["error"] != "invalid_scope" {
			t.Fatal(bad, resp.Status, m)
		}
	}
	// Browser sessions have no scopes.
	w := e.browser()
	w.login("ida")
	wantStatus(t, w, "GET", "/api/provisioning/profile", 200, "")
}

func TestDeviceRotateHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b, first := deviceLoginHTTP(t, e, "ida", "")
	t1 := b.bearer
	resp, m := b.do("POST", "/api/device/rotate", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	t2, _ := m["access_token"].(string)
	if t2 == "" || t2 == t1 || m["session_id"] != first["session_id"] || m["scope"] != "data provisioning" {
		t.Fatal(m)
	}
	if exp, _ := m["expires_in"].(float64); exp > (90*24*time.Hour).Seconds() || exp < (89*24*time.Hour).Seconds() {
		t.Fatal("expires_in", exp)
	}
	wantStatus(t, b, "GET", "/api/auth/me", 200, "") // T1 before the first use of T2
	b.bearer = t2
	wantStatus(t, b, "GET", "/api/auth/me", 200, "")
	// In-flight requests with T1 pass, rotating with it does not.
	b.bearer = t1
	wantStatus(t, b, "GET", "/api/auth/me", 200, "")
	wantStatus(t, b, "POST", "/api/device/rotate", 409, "token_superseded")
	// After the leeway: reuse, 401, family gone.
	e.svc.Now = func() time.Time { return time.Now().Add(auth.DeviceTokenLeeway + time.Minute) }
	wantStatus(t, b, "GET", "/api/auth/me", 401, "")
	b.bearer = t2
	wantStatus(t, b, "GET", "/api/auth/me", 401, "")
	// Rotation is for device tokens.
	e.svc.Now = time.Now
	w := e.browser()
	w.login("ida")
	wantStatus(t, w, "POST", "/api/device/rotate", 403, "device_token_required")
}

// A client that knows nothing about rotation (the current desktop client) works
// on: its requests have no Origin and it never calls /api/device/rotate.
func TestClientWithoutRotationKeepsWorking(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b, m := deviceLoginHTTP(t, e, "ida", "")
	if m["expires_in"].(float64) < (89 * 24 * time.Hour).Seconds() {
		t.Fatal(m["expires_in"])
	}
	for range 3 {
		wantStatus(t, b, "GET", "/api/provisioning/profile", 200, "")
		wantStatus(t, b, "GET", "/api/provisioning/secrets", 200, "")
	}
	if resp, m := b.do("PUT", "/api/settings/x", map[string]any{"value": 1}); resp.StatusCode/100 != 2 {
		t.Fatal(resp.Status, m)
	}
}

// Basis spec/AUTH.md §6: what concerns sessions and tokens over HTTP.
func TestAuthSection6HTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)

	// Session cookie after logout.
	a := e.browser()
	a.login("ida")
	stale := a.sessionCookieValue()
	wantStatus(t, a, "POST", "/api/auth/logout", 204, "")
	a.setSessionCookie(stale)
	a.fetchCSRF()
	wantStatus(t, a, "GET", "/api/auth/me", 401, "")

	// Session after a password change on another device.
	other := e.browser()
	other.login("ida")
	this := e.browser()
	this.login("ida")
	tok, _ := deviceLoginHTTP(t, e, "ida", "")
	if resp, m := this.do("POST", "/api/auth/password", map[string]string{"currentPassword": pw, "newPassword": "ein-ganz-anderes-kennwort"}); resp.StatusCode/100 != 2 {
		t.Fatal(resp.Status, m)
	}
	wantStatus(t, other, "GET", "/api/auth/me", 401, "")
	wantStatus(t, tok, "GET", "/api/auth/me", 401, "") // device tokens as well
	wantStatus(t, this, "GET", "/api/auth/me", 200, "")

	// Device token expired / revoked: 401; missing scope is above (403). IP lists
	// do not exist for device tokens.
	e2 := newEnv(t, Options{})
	e2.user("ben", auth.RoleUser)
	d1, _ := deviceLoginHTTP(t, e2, "ben", "")
	e2.svc.Now = func() time.Time { return time.Now().Add(91 * 24 * time.Hour) }
	wantStatus(t, d1, "GET", "/api/auth/me", 401, "")
	e2.svc.Now = time.Now
	d2, m := deviceLoginHTTP(t, e2, "ben", "")
	w := e2.browser()
	w.login("ben")
	if resp, r := w.do("DELETE", "/api/auth/sessions/"+itoa(int(m["session_id"].(float64))), nil); resp.StatusCode/100 != 2 {
		t.Fatal(resp.Status, r)
	}
	wantStatus(t, d2, "GET", "/api/auth/me", 401, "")
}

func TestSessionIdleOverHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	wantStatus(t, b, "GET", "/api/auth/me", 200, "")
	e.svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	wantStatus(t, b, "GET", "/api/auth/me", 401, "")
}

// csrfRequest sends POST /api/settings-like request with exactly these headers.
func csrfRequest(t *testing.T, b *browser, set map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", b.e.ts.URL+"/api/settings/x", strings.NewReader(`{"value":1}`))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range set {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// Basis spec/WEB.md: both layers, no fallbacks.
func TestCSRFBothLayers(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	good := b.csrf
	if good != csrfTokenFor(b.sessionCookieValue()) {
		t.Fatalf("token %q is not the HMAC of the session", good)
	}
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"neither Sec-Fetch-Site nor Origin nor Referer": {map[string]string{csrfHeader: good}, 403},
		"Sec-Fetch-Site cross-site":                     {map[string]string{csrfHeader: good, "Sec-Fetch-Site": "cross-site", "Origin": e.ts.URL}, 403},
		"Sec-Fetch-Site same-site":                      {map[string]string{csrfHeader: good, "Sec-Fetch-Site": "same-site"}, 403},
		"foreign Origin":                                {map[string]string{csrfHeader: good, "Origin": "https://evil.example"}, 403},
		"foreign Origin next to same-origin":            {map[string]string{csrfHeader: good, "Sec-Fetch-Site": "same-origin", "Origin": "https://evil.example"}, 403},
		"foreign Referer":                               {map[string]string{csrfHeader: good, "Referer": "https://evil.example/x"}, 403},
		"no token":                                      {map[string]string{"Origin": e.ts.URL}, 403},
		"wrong token":                                   {map[string]string{csrfHeader: strings.Repeat("0", 64), "Origin": e.ts.URL}, 403},
		"Sec-Fetch-Site same-origin":                    {map[string]string{csrfHeader: good, "Sec-Fetch-Site": "same-origin"}, 200},
		"Sec-Fetch-Site none":                           {map[string]string{csrfHeader: good, "Sec-Fetch-Site": "none"}, 200},
		"Origin":                                        {map[string]string{csrfHeader: good, "Origin": e.ts.URL}, 200},
		"Referer as fallback":                           {map[string]string{csrfHeader: good, "Referer": e.ts.URL + "/mail"}, 200},
	} {
		resp, raw := csrfRequest(t, b, c.h)
		if (c.want == 200) != (resp.StatusCode/100 == 2) || (c.want == 403 && resp.StatusCode != 403) {
			t.Errorf("%s: %s %s", name, resp.Status, raw)
		}
	}
	// A cookie the attacker planted does not help: the token depends on the session.
	planted := strings.Repeat("a", 64)
	b.c.Jar.SetCookies(b.cookieJarURL(), []*http.Cookie{{Name: csrfCookie, Value: planted, Path: "/"}})
	if resp, _ := csrfRequest(t, b, map[string]string{csrfHeader: planted, "Origin": e.ts.URL}); resp.StatusCode != 403 {
		t.Errorf("double submit with a planted cookie: %s", resp.Status)
	}
	// Logout is protected as well.
	if resp, _ := func() (*http.Response, string) {
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/auth/logout", nil)
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp, ""
	}(); resp.StatusCode != 403 {
		t.Errorf("logout without CSRF: %s", resp.Status)
	}
}

// Same computation as Basis python/basis_sec/csrf.py token_for(b"abc").
func TestCSRFTokenFormat(t *testing.T) {
	if got := csrfTokenFor("abc"); got != "77964432772eab807102b29535ba90bd244a74d7858352e23680efc4e67672bd" {
		t.Fatal(got)
	}
}

func TestCSRFLoginBeforeSession(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.fetchCSRF()
	body := `{"username":"ida","password":"` + pw + `"}`
	for name, h := range map[string]map[string]string{
		"no origin":  {csrfHeader: b.csrf},
		"cross-site": {csrfHeader: b.csrf, "Sec-Fetch-Site": "cross-site"},
		"no token":   {"Origin": e.ts.URL},
	} {
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	b.login("ida") // with both layers
}

func TestCSRFWebSocketHandshake(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	cookie := cookieHeader(t, e, b).Get("Cookie")
	try := func(set map[string]string) int {
		req, _ := http.NewRequest("GET", e.ts.URL+"/api/events", nil)
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		for k, v := range set {
			req.Header.Set(k, v)
		}
		resp, err := testTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	proto := "tuxmail.v1, " + wsCSRFProtocol + b.csrf
	for name, set := range map[string]map[string]string{
		"no Origin":      {"Sec-WebSocket-Protocol": proto},
		"foreign Origin": {"Sec-WebSocket-Protocol": proto, "Origin": "https://evil.example"},
		"no token":       {"Origin": e.ts.URL},
		"wrong token":    {"Origin": e.ts.URL, "Sec-WebSocket-Protocol": "tuxmail.v1, " + wsCSRFProtocol + strings.Repeat("0", 64)},
	} {
		if got := try(set); got != 403 {
			t.Errorf("%s: %d", name, got)
		}
	}
	if got := try(map[string]string{"Origin": e.ts.URL, "Sec-WebSocket-Protocol": proto}); got != 101 {
		t.Errorf("web client handshake: %d", got)
	}
}

func TestSecurityHeadersValues(t *testing.T) {
	e := newEnv(t, Options{})
	b := e.browser()
	for _, p := range []string{"/api/auth/config", "/healthz", "/login", "/"} {
		resp, _ := b.do("GET", p, nil)
		h := resp.Header
		for k, want := range map[string]string{
			"Referrer-Policy":              "strict-origin-when-cross-origin",
			"X-Content-Type-Options":       "nosniff",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if h.Get(k) != want {
				t.Errorf("%s %s: %q", p, k, h.Get(k))
			}
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'", "script-src-attr 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s CSP without %s: %s", p, want, csp)
			}
		}
		if h.Get("X-Request-ID") == "" {
			t.Errorf("%s: no X-Request-ID", p)
		}
	}
}

func TestHeadersCanBeSwitchedOff(t *testing.T) {
	e := newEnv(t, Options{})
	e.cfg.PublicURL = "https://mail.example.de"
	e.cfg.Security.Headers = false
	e.cfg.Security.HSTS = false
	s := e.srv.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/x", nil)
	s.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal(rec.Header())
	}
}

func TestRequestIDInHeaderAndLog(t *testing.T) {
	var logs bytes.Buffer
	e := newEnv(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	b := e.browser()
	b.header = http.Header{"X-Request-Id": {"evil-id"}}
	resp, _ := b.do("GET", "/api/auth/config", nil)
	id := resp.Header.Get("X-Request-ID")
	if len(id) != 16 || strings.Contains(id, "evil") {
		t.Fatalf("id %q", id)
	}
	found := false
	for _, line := range strings.Split(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["path"] == "/api/auth/config" {
			found = m["request_id"] == id
		}
	}
	if !found {
		t.Fatal("request id not in the log:", logs.String())
	}
	if strings.Contains(logs.String(), "evil") {
		t.Fatal("client id logged")
	}
}
