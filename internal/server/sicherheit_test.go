// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"strconv"

	"encoding/base32"
	"github.com/Midas1989/Mailclient/internal/store"
	"os"
	"path/filepath"

	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/server/api"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
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
	if resp, r := w.do("DELETE", "/api/auth/sessions/"+itoa(int(m["session_id"].(float64))), map[string]string{"password": pw}); resp.StatusCode/100 != 2 {
		t.Fatal(resp.Status, r)
	}
	wantStatus(t, d2, "GET", "/api/auth/me", 401, "")
}

// An open tab that polls and holds its event stream does not stay signed in.
func TestOpenTabDoesNotStopTheIdleLimit(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	b.background = true
	wsHeader := cookieHeader(t, e, b)
	dial := func() int {
		req, _ := http.NewRequest("GET", e.ts.URL+"/api/events", nil)
		for k, v := range wsHeader {
			req.Header[k] = v
		}
		req.Header.Set("Origin", e.ts.URL)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set(activityHeader, "1") // not even if it claims so
		resp, err := testTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	start := time.Now()
	for i := 1; i <= 11; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute)
		e.svc.Now = func() time.Time { return at }
		wantStatus(t, b, "GET", "/api/auth/me", 200, "")
		if got := dial(); got != 101 && got != 200 {
			t.Fatalf("handshake %d: %d", i, got)
		}
	}
	e.svc.Now = func() time.Time { return start.Add(2*time.Hour + time.Minute) }
	wantStatus(t, b, "GET", "/api/auth/me", 401, "")

	// The same requests with a person in front of them keep it going.
	e.svc.Now = time.Now
	a := e.browser()
	a.login("ida")
	start = time.Now()
	for i := 1; i <= 11; i++ {
		at := start.Add(time.Duration(i) * 20 * time.Minute)
		e.svc.Now = func() time.Time { return at }
		wantStatus(t, a, "GET", "/api/auth/me", 200, "")
	}
}

func TestHealth(t *testing.T) {
	e := newEnv(t, Options{})
	b := e.browser()
	resp, m := b.do("GET", "/healthz", nil)
	if resp.StatusCode != 200 || m["status"] != "ok" || m["product"] != "tuxmail" || m["checks"] != nil {
		t.Fatal(resp.Status, m)
	}
	// Not yet proven, no backup key: not green (Basis spec/BETRIEB.md), but this
	// instance works, so a start script gets 200.
	resp, m = b.do("GET", "/healthz?deep=1", nil)
	if resp.StatusCode != 200 || m["status"] != "fail" {
		t.Fatal(resp.Status, m)
	}
	e.srv.runPeriodicChecks(t.Context())
	resp, m = b.do("GET", "/healthz?deep=1", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	names := map[string]string{}
	for _, c := range m["checks"].([]any) {
		x := c.(map[string]any)
		names[x["name"].(string)] = x["status"].(string)
	}
	for _, want := range []string{"db", "disk", "jobs", "backup", "audit_chain", "key_rotation"} {
		if names[want] == "" {
			t.Errorf("check %s missing: %v", want, names)
		}
	}
	if names["db"] != "ok" || names["audit_chain"] != "ok" || names["key_rotation"] != "ok" || names["jobs"] != "ok" {
		t.Fatal(names)
	}
	check := func(name string) string {
		t.Helper()
		_, m := b.do("GET", "/api/v1/monitoring/self", nil)
		for _, c := range m["checks"].([]any) {
			if x := c.(map[string]any); x["name"] == name {
				return x["status"].(string)
			}
		}
		t.Fatal("no check", name)
		return ""
	}
	// An open rotation is a warning.
	e.user("ida", auth.RoleUser)
	if _, err := e.svc.RotateKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.srv.runPeriodicChecks(t.Context())
	if got := check("key_rotation"); got != "warn" {
		t.Fatal(got)
	}
	// A broken chain fails, as soon as it is checked.
	e.srv.anchorDay = ""
	for _, q := range []string{`DROP TRIGGER audit_log_no_null`, `INSERT INTO audit_log(at, action) VALUES (1, 'fremd')`} {
		if _, err := e.svc.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.runPeriodicChecks(t.Context())
	if got := check("audit_chain"); got != "fail" {
		t.Fatal(got)
	}
	// Without the database: the plain answer lives on, the deep one fails.
	e.svc.DB().Close()
	if resp, _ := b.do("GET", "/healthz", nil); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if resp, m = b.do("GET", "/healthz?deep=1", nil); resp.StatusCode != 503 || m["status"] != "fail" {
		t.Fatal(resp.Status, m)
	}
	if resp, m = b.do("GET", "/api/v1/monitoring/self", nil); resp.StatusCode != 200 || m["status"] != "fail" {
		t.Fatal(resp.Status, m)
	}
}

// The deep answer has details: the token, or this machine.
func TestMonitoringAccess(t *testing.T) {
	tf := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("m", 40)
	os.WriteFile(tf, []byte(token+"\n"), 0o600)
	e := newEnv(t, Options{}, func(c *config.Config) { c.Monitoring.TokenFile = tf })
	h := e.srv.Handler()
	call := func(path, remote string, hdr map[string]string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	const outside = "203.0.113.5:4444"
	for _, p := range []string{"/healthz?deep=1", "/api/v1/monitoring/self"} {
		for name, c := range map[string]struct {
			remote string
			hdr    map[string]string
			want   int
		}{
			"outside":                 {outside, nil, 403},
			"outside, wrong token":    {outside, map[string]string{"Authorization": "Bearer " + strings.Repeat("x", 40)}, 403},
			"outside, token":          {outside, map[string]string{"Authorization": "Bearer " + token}, 200},
			"local":                   {"127.0.0.1:4444", nil, 200},
			"local proxy for outside": {"127.0.0.1:4444", map[string]string{"X-Forwarded-For": "203.0.113.5"}, 403},
			"local, wrong token":      {"127.0.0.1:4444", map[string]string{"Authorization": "Bearer x"}, 403},
		} {
			if got := call(p, c.remote, c.hdr); got != c.want {
				t.Errorf("%s, %s: %d, want %d", p, name, got, c.want)
			}
		}
	}
	if got := call("/healthz", outside, nil); got != 200 {
		t.Errorf("plain /healthz: %d", got)
	}
	// Without a configured token only this machine.
	e2 := newEnv(t, Options{})
	req := httptest.NewRequest("GET", "/api/v1/monitoring/self", nil)
	req.RemoteAddr = outside
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	e2.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("no token configured: %d", rec.Code)
	}
	// A short token file does not start.
	short := filepath.Join(t.TempDir(), "kurz")
	os.WriteFile(short, []byte("abc"), 0o600)
	cfg := *e2.cfg
	cfg.Monitoring.TokenFile = short
	if _, err := New(&cfg, e2.svc, Options{}); err == nil {
		t.Error("short monitoring token accepted")
	}
}

func deviceTok(t *testing.T, e *env, name, scope string) *browser {
	t.Helper()
	b, _ := deviceLoginHTTP(t, e, name, scope)
	return b
}

// Hochstufe 1 der Prüfung: with its own passphrase the caller gets the secrets in clear.
func TestDeviceTokenCannotGetSecretsFromBackup(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	dl := map[string]any{"kind": "download"}
	body := func(secrets bool) map[string]any {
		return map[string]any{"scope": map[string]any{"mode": "config", "secrets": secrets}, "passphrase": "eine lange passphrase", "target": dl}
	}
	for _, scope := range []string{"data", ""} { // "" = data and provisioning, as old tokens have
		d := deviceTok(t, e, "ida", scope)
		if resp, m := d.do("POST", "/api/backup", body(true)); resp.StatusCode != 403 || m["error"] != "device_token_forbidden" {
			t.Fatal(scope, resp.Status, m)
		}
		if resp, m := d.do("POST", "/api/backup/schedules", map[string]any{"name": "x", "scope": map[string]any{"mode": "config", "secrets": true}}); resp.StatusCode != 403 {
			t.Fatal(scope, resp.Status, m)
		}
		if resp, m := d.do("PUT", "/api/backup/schedules/x", map[string]any{"scope": map[string]any{"secrets": true}}); resp.StatusCode != 403 {
			t.Fatal(scope, resp.Status, m)
		}
		if resp, m := d.do("POST", "/api/backup/restore", map[string]any{"secrets": true}); resp.StatusCode != 403 {
			t.Fatal(scope, resp.Status, m)
		}
		// Without secrets it stays possible.
		if resp, m := d.do("POST", "/api/backup", body(false)); resp.StatusCode != 202 {
			t.Fatal(scope, resp.Status, m)
		}
	}
	// A browser session needs proof.
	w := e.browser()
	w.login("ida")
	if resp, m := w.do("POST", "/api/backup", body(true)); resp.StatusCode != 403 {
		t.Fatal("without reauth:", resp.Status, m)
	}
	w.fetchCSRF()
	withProof := body(true)
	withProof["reauth"] = map[string]string{"password": pw}
	if resp, m := w.do("POST", "/api/backup", withProof); resp.StatusCode != 202 {
		t.Fatal("with reauth:", resp.Status, m)
	}
}

func TestDeviceTokenRestrictedKeyRoutes(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	d := deviceTok(t, e, "ida", "data")
	re := regexp.MustCompile(`\{[a-zA-Z]+\}`)
	for pattern := range deviceForbiddenAppRoutes {
		method, path, _ := strings.Cut(pattern, " ")
		wantStatus(t, d, method, re.ReplaceAllString(path, "1"), 403, "device_token_forbidden")
	}
	wantStatus(t, d, "PUT", "/api/team/1/grants", 403, "device_token_forbidden")
	// Released for the app (decision 2026-11-07): trust and peers; they answer, whatever the data says.
	for _, r := range []struct{ method, path string }{
		{"PUT", "/api/comfort/pgp/keys/1/trust"}, {"POST", "/api/comfort/pgp/peers/accept"}, {"POST", "/api/comfort/pgp/peers/verify"},
		{"POST", "/api/comfort/pgp/peers/delete"}, {"PUT", "/api/comfort/smime/certificates/1/trust"},
	} {
		if resp, m := d.do(r.method, r.path, map[string]any{}); m["error"] == "device_token_forbidden" {
			t.Errorf("%s %s: %s %v", r.method, r.path, resp.Status, m)
		}
	}
}

// Restore replace puts account settings from the archive next to the stored secrets.
func TestRestoreReplaceRefusesDeviceTokens(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	d := deviceTok(t, e, "ida", "data")
	body := map[string]any{"mode": "replace", "source": map[string]string{"uploadToken": "x"}}
	if resp, m := d.do("POST", "/api/backup/restore", body); resp.StatusCode != 403 || m["error"] != "device_token_forbidden" {
		t.Fatal(resp.Status, m)
	}
	// Merge does not touch stored accounts: the unknown archive answers something else.
	body["mode"] = "merge"
	if resp, m := d.do("POST", "/api/backup/restore", body); m["error"] == "device_token_forbidden" {
		t.Fatal(resp.Status, m)
	}
	w := e.browser()
	w.login("ida")
	body["mode"] = "replace"
	if resp, m := w.do("POST", "/api/backup/restore", body); resp.StatusCode != 403 {
		t.Fatal("browser without proof:", resp.Status, m)
	}
}

// What the review showed with a probe: the stored password on its way to a foreign server.
func TestDeviceTokenCannotSendStoredPasswordElsewhere(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("ida", auth.RoleUser)
	ap, err := e.srv.App(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv := domain.ServerConfig{Host: "mail.example.org", Port: 993, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: "ida"}
	acc, err := ap.CreateAccount(t.Context(), app.NewAccountInput{Kind: "imap", DisplayName: "I", Email: "ida@example.org", Name: "I", Password: "geheim", Incoming: &srv})
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(acc.ID, 10)
	evil := map[string]any{"host": "evil.example", "port": 993, "tls": "tls", "auth": "password", "username": "ida"}
	for _, b := range []*browser{deviceTok(t, e, "ida", "data"), func() *browser { w := e.browser(); w.login("ida"); return w }()} {
		for _, c := range []struct {
			method, path string
			body         map[string]any
		}{
			{"POST", "/api/accounts/" + id + "/test", map[string]any{"update": map[string]any{"incoming": evil}}},
			{"POST", "/api/accounts/" + id + "/test", map[string]any{"update": map[string]any{"sieve": map[string]any{"host": "evil.example", "port": 4190}}}},
			{"PATCH", "/api/accounts/" + id, map[string]any{"incoming": evil}},
			{"PATCH", "/api/accounts/" + id, map[string]any{"sieve": map[string]any{"host": "evil.example", "port": 4190}}},
			{"PUT", "/api/accounts/" + id + "/sieve/config", map[string]any{"host": "evil.example", "port": 4190}},
		} {
			if resp, m := b.do(c.method, c.path, c.body); resp.StatusCode != 400 || m["error"] != "password_required" {
				t.Errorf("%s %s %v: %s %v", c.method, c.path, c.body, resp.Status, m)
			}
		}
	}
	// The app can still change servers, with the password for them, and other settings without.
	d := deviceTok(t, e, "ida", "data")
	if resp, m := d.do("PATCH", "/api/accounts/"+id, map[string]any{"displayName": "Neu"}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if resp, m := d.do("PATCH", "/api/accounts/"+id, map[string]any{"incoming": map[string]any{"host": "new.example.org", "port": 993, "tls": "tls", "auth": "password", "username": "ida"}, "password": "neu-geheim"}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
}

// Private keys stay with the browser; the keys of others may go.
func TestDeviceTokenDeletesOnlyPublicKeys(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("ida", auth.RoleUser)
	st, err := store.Open(t.Context(), filepath.Join(e.svc.UserDir(u.ID), UserStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	priv, err := st.PutPGPKey(t.Context(), store.PGPKey{Fingerprint: "AA", Email: "a@example.de", SecretRef: "pgp:AA", PublicKey: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := st.PutPGPKey(t.Context(), store.PGPKey{Fingerprint: "BB", Email: "b@example.de", PublicKey: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	d := deviceTok(t, e, "ida", "data")
	wantStatus(t, d, "DELETE", "/api/comfort/pgp/keys/"+strconv.FormatInt(priv, 10), 403, "device_token_forbidden")
	if resp, m := d.do("DELETE", "/api/comfort/pgp/keys/"+strconv.FormatInt(pub, 10), nil); m["error"] == "device_token_forbidden" {
		t.Fatal(resp.Status, m)
	}
}

// Every route that sounds like secrets, keys or trust is decided here, also future ones.
func TestDeviceTokenSensitiveRoutesAreClassified(t *testing.T) {
	allowed := map[string]string{
		"GET /api/vault/status":                          "only says whether the desktop vault is locked",
		"POST /api/vault/unlock":                         "needs the vault password, desktop only",
		"PUT /api/accounts/{id}/password":                "sets, never reads; the app must be able to fix a changed mail password",
		"GET /api/cloud/accounts/{id}/password":          "generates a random password, reads nothing stored",
		"GET /api/cloud/shares/{id}/password":            "the password of a share link the user made, part of the sharing dialog",
		"GET /api/comfort/pgp/keys":                      "public data",
		"POST /api/comfort/pgp/keys":                     "imports; hands nothing out",
		"POST /api/comfort/pgp/keys/generate":            "creates; hands nothing out",
		"POST /api/comfort/pgp/keys/lookup":              "public directory",
		"GET /api/comfort/pgp/keys/{id}/public":          "public data",
		"POST /api/comfort/pgp/keys/{id}/unlock":         "needs the passphrase of the key; reading encrypted mail in the app",
		"POST /api/comfort/pgp/lock":                     "only locks",
		"GET /api/comfort/smime/certificates":            "public data",
		"POST /api/comfort/smime/certificates":           "imports; hands nothing out",
		"GET /api/comfort/pgp/peers":                     "public data",
		"GET /api/comfort/pgp/status":                    "state only",
		"GET /api/comfort/smime/status":                  "state only",
		"GET /api/oauth/providers":                       "public data",
		"POST /api/oauth/start":                          "the user signs in at the provider",
		"GET /api/oauth/flows/{flowId}/status":           "state of the own flow",
		"DELETE /api/oauth/flows/{flowId}":               "ends the own flow",
		"GET /api/oauth/{provider}/callback":             "bound to the flow state",
		"PUT /api/comfort/pgp/keys/{id}/trust":           "released for the app, decision 2026-11-07",
		"POST /api/comfort/pgp/peers/accept":             "released for the app, the composer needs it",
		"POST /api/comfort/pgp/peers/verify":             "released for the app",
		"POST /api/comfort/pgp/peers/delete":             "released for the app",
		"PUT /api/comfort/smime/certificates/{id}/trust": "released for the app",
		"DELETE /api/comfort/pgp/keys/{id}":              "private keys: guarded in the handler",
		"DELETE /api/comfort/smime/certificates/{id}":    "private keys: guarded in the handler",
		"POST /api/backup":                               "secrets: guarded in the handler",
		"POST /api/backup/inspect":                       "needs the passphrase of the archive",
		"POST /api/backup/restore":                       "secrets: guarded in the handler",
		"GET /api/backup/schedules":                      "no secrets in the answer",
		"POST /api/backup/schedules":                     "secrets: guarded in the handler",
		"PUT /api/backup/schedules/{id}":                 "secrets: guarded in the handler",
		"DELETE /api/backup/schedules/{id}":              "own schedule",
		"POST /api/backup/schedules/{id}/run":            "goes to the configured target",
		"GET /api/backup/schedules/{id}/archives":        "names only",
		"GET /api/diagnostics/export":                    "redacted",
		"GET /api/diagnostics":                           "redacted",
		"GET /api/accounts/{id}/diagnostics":             "redacted",
		"GET /api/cloud/oauth/callback":                  "bound to the flow state",
	}
	sensitive := regexp.MustCompile(`(?i)secret|password|passphrase|vault|/pgp|smime|trust|oauth|backup|diagnostics`)
	for _, rt := range api.Routes(api.Options{}) {
		if !sensitive.MatchString(rt.Pattern) || deviceForbiddenAppRoutes[rt.Pattern] {
			continue
		}
		if allowed[rt.Pattern] == "" {
			t.Errorf("%s: decide whether device tokens may use it (deviceForbiddenAppRoutes or this list)", rt.Pattern)
		}
	}
}

func TestAccountSecurityRoutesRefuseDeviceTokens(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	proof := map[string]string{"password": pw, "currentPassword": pw, "newPassword": "ein-ganz-anderes-kennwort", "code": "123456", "user_code": "BCDF-GHJK"}
	for _, scope := range []string{"data", "provisioning", ""} {
		d := deviceTok(t, e, "ida", scope)
		for _, r := range []struct{ method, path string }{
			{"POST", "/api/auth/totp/setup"}, {"POST", "/api/auth/totp/confirm"}, {"POST", "/api/auth/totp/disable"},
			{"POST", "/api/auth/recovery-codes"}, {"POST", "/api/auth/password"},
			{"DELETE", "/api/auth/sessions/1"}, {"POST", "/api/auth/sessions/revoke-others"},
			{"POST", "/api/auth/passkeys/register/begin"}, {"POST", "/api/auth/passkeys/register/finish"},
			{"PATCH", "/api/auth/passkeys/1"}, {"DELETE", "/api/auth/passkeys/1"},
			{"GET", "/api/device/request?user_code=x"}, {"POST", "/api/device/approve"}, {"POST", "/api/device/approve/passkey"},
		} {
			if resp, m := d.do(r.method, r.path, proof); resp.StatusCode != 403 || m["error"] != "device_token_forbidden" {
				t.Errorf("%q %s %s: %s %v", scope, r.method, r.path, resp.Status, m)
			}
		}
	}
	// The owner still gets in: the token did not set up TOTP.
	w := e.browser()
	w.login("ida")
	if resp, m := w.do("GET", "/api/auth/me", nil); resp.StatusCode != 200 || m["user"].(map[string]any)["totpEnabled"] == true {
		t.Fatal(resp.Status, m)
	}
}

// A stolen session cookie alone is not enough.
func TestSessionNeedsReauthForAccountSecurity(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 })
	e.user("ida", auth.RoleUser)
	other := e.browser()
	other.login("ida")
	b := e.browser()
	b.login("ida")
	var sid float64
	_, me := other.do("GET", "/api/auth/me", nil)
	sid = me["session"].(map[string]any)["id"].(float64)

	for _, r := range []struct{ method, path string }{
		{"POST", "/api/auth/totp/setup"}, {"POST", "/api/auth/sessions/revoke-others"}, {"DELETE", "/api/auth/sessions/" + itoa(int(sid))},
	} {
		if resp, m := b.do(r.method, r.path, map[string]string{}); resp.StatusCode != 403 || m["error"] != "reauth_required" {
			t.Errorf("%s %s without proof: %s %v", r.method, r.path, resp.Status, m)
		}
		if resp, m := b.do(r.method, r.path, map[string]string{"password": "falsch-falsch-falsch"}); resp.StatusCode != 401 {
			t.Errorf("%s %s with a wrong password: %s %v", r.method, r.path, resp.Status, m)
		}
		e.svc.ClearStepUp(t.Context(), 1)
	}
	wantStatus(t, other, "GET", "/api/auth/me", 200, "")
	proof := map[string]string{"password": pw}
	b.fetchCSRF()
	resp, m := b.do("POST", "/api/auth/totp/setup", proof)
	if resp.StatusCode != 200 || m["secret"] == nil {
		t.Fatal(resp.Status, m)
	}
	b.fetchCSRF()
	if resp, m := b.do("DELETE", "/api/auth/sessions/"+itoa(int(sid)), proof); resp.StatusCode != 204 {
		t.Fatal(resp.Status, m)
	}
	wantStatus(t, other, "GET", "/api/auth/me", 401, "")
	// The own session needs nothing: that is signing out.
	_, me = b.do("GET", "/api/auth/me", nil)
	own := int(me["session"].(map[string]any)["id"].(float64))
	b.fetchCSRF()
	wantStatus(t, b, "DELETE", "/api/auth/sessions/"+itoa(own), 204, "")

	// Approving a device.
	g := deviceClient(t, e)
	_, grant := g.do("POST", "/api/device/authorize", map[string]string{"client_name": "x"})
	code := grant["user_code"].(string)
	a := e.browser()
	a.login("ida")
	if resp, m := a.do("POST", "/api/device/approve", map[string]string{"user_code": code}); resp.StatusCode != 403 {
		t.Fatal("approve without proof:", resp.Status, m)
	}
	a.fetchCSRF()
	if resp, m := a.do("POST", "/api/device/approve", map[string]any{"user_code": code, "approve": false}); resp.StatusCode != 204 {
		t.Fatal("deny needs no proof:", resp.Status, m)
	}
	_, grant = g.do("POST", "/api/device/authorize", map[string]string{"client_name": "x"})
	a.fetchCSRF()
	if resp, m := a.do("POST", "/api/device/approve", map[string]string{"user_code": grant["user_code"].(string), "password": pw}); resp.StatusCode != 204 {
		t.Fatal("approve with proof:", resp.Status, m)
	}
}

func TestStepUpGivesNewSessionID(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	old := b.sessionCookieValue()
	_, me := b.do("GET", "/api/auth/me", nil)
	sid := me["session"].(map[string]any)["id"]
	if resp, m := b.do("POST", "/api/auth/totp/setup", map[string]string{"password": pw}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	fresh := b.sessionCookieValue()
	if fresh == old || fresh == "" {
		t.Fatal("same session id after step-up")
	}
	b.fetchCSRF()
	if b.csrf != csrfTokenFor(fresh) {
		t.Fatal("CSRF token does not follow the session")
	}
	_, me = b.do("GET", "/api/auth/me", nil)
	if me["session"].(map[string]any)["id"] != sid {
		t.Fatal("not the same session")
	}
	// Requests still on their way pass, later the old cookie is dead, the session lives.
	c := e.browser()
	c.setSessionCookie(old)
	wantStatus(t, c, "GET", "/api/auth/me", 200, "")
	e.svc.Now = func() time.Time { return time.Now().Add(auth.DeviceTokenLeeway + time.Minute) }
	wantStatus(t, c, "GET", "/api/auth/me", 401, "")
	wantStatus(t, b, "GET", "/api/auth/me", 200, "")
}

// Standard on (decision 2026-11-07), without locking anybody out.
func TestAdminTOTPDefaultOnWithoutLockout(t *testing.T) {
	if !config.Default().Auth.TOTPRequiredForAdmins {
		t.Fatal("default off")
	}
	e := newEnv(t, Options{}, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	u := e.user("chef", auth.RoleAdmin)
	b := e.browser()
	b.login("chef")
	_, me := b.do("GET", "/api/auth/me", nil)
	if me["mfaSetupRequired"] != true {
		t.Fatal(me)
	}
	wantStatus(t, b, "GET", "/api/admin/users", 403, "mfa_setup_required")
	// The way out: the enrolment works for this very session, and it can sign out.
	resp, m := b.do("POST", "/api/auth/totp/setup", map[string]string{"password": pw})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(m["secret"].(string))
	if err != nil {
		t.Fatal(err)
	}
	b.fetchCSRF()
	resp, m = b.do("POST", "/api/auth/totp/confirm", map[string]string{"code": auth.TOTP{Secret: secret}.Code(time.Now())})
	if resp.StatusCode != 200 || len(m["recoveryCodes"].([]any)) != 10 {
		t.Fatal(resp.Status, m)
	}
	wantStatus(t, b, "GET", "/api/admin/users", 200, "")
	// Lost authenticator: recovery code at the login, or from the command line.
	c := e.browser()
	c.fetchCSRF()
	resp, m = c.do("POST", "/api/auth/login", map[string]string{"username": "chef", "password": pw, "recoveryCode": m["recoveryCodes"].([]any)[0].(string)})
	if resp.StatusCode != 200 || m["status"] != "ok" {
		t.Fatal(resp.Status, m)
	}
	if err := e.svc.ResetSecondFactors(t.Context(), u.ID); err != nil { // tuxmail user reset-2fa
		t.Fatal(err)
	}
	d := e.browser()
	d.login("chef")
	wantStatus(t, d, "GET", "/api/admin/users", 403, "mfa_setup_required")
	// Off by configuration.
	off := newEnv(t, Options{})
	off.user("chef", auth.RoleAdmin)
	a := off.browser()
	a.login("chef")
	wantStatus(t, a, "GET", "/api/admin/users", 200, "")
}

func TestLogPathHidesTokens(t *testing.T) {
	var logs bytes.Buffer
	e := newEnv(t, Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(c *config.Config) { c.Auth.Registration = "invite" })
	b := e.browser()
	for _, p := range []string{"/api/auth/invites/GEHEIMESTOKEN", "/api/io/downloads/GEHEIMESTOKEN"} {
		b.do("GET", p, nil)
	}
	if strings.Contains(logs.String(), "GEHEIMESTOKEN") {
		t.Fatal(logs.String())
	}
	if !strings.Contains(logs.String(), "/api/auth/invites/********") {
		t.Fatal(logs.String())
	}
}

func cspDirective(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

func nonceOf(t *testing.T, directive string) string {
	t.Helper()
	m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(directive)
	if m == nil {
		t.Fatalf("no nonce in %q", directive)
	}
	return m[1]
}

// Basis spec/PARAMETER.md, spec/WEB.md.
func TestCSPNonces(t *testing.T) {
	static := fstest.MapFS{
		"index.html":   {Data: []byte(`<!doctype html><html><head><link rel="modulepreload" href="/assets/a.js"><link rel="stylesheet" href="/assets/a.css"><script type="module" src="/assets/m.js"></script></head><body></body></html>`)},
		"offline.html": {Data: []byte(`<!doctype html><html><head><style>a{}</style></head><body></body></html>`)},
	}
	e := newEnv(t, Options{Static: static})
	e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")
	get := func(path string) (string, string, string) {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		csp := resp.Header.Get("Content-Security-Policy")
		return nonceOf(t, cspDirective(csp, "script-src")), nonceOf(t, cspDirective(csp, "style-src")), string(raw)
	}
	script, style, page := get("/")
	if script == style {
		t.Fatal("one nonce for scripts and styles")
	}
	for _, want := range []string{
		`<script nonce="` + script + `" type="module"`,
		`<link nonce="` + script + `" rel="modulepreload"`,
		`<meta name="tuxmail-style-nonce" content="` + style + `">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html without %s:\n%s", want, page)
		}
	}
	// The script nonce is not readable from the page except on script elements.
	if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(page, `<script nonce="`+script+`"`, ""), `<link nonce="`+script+`"`, ""), script) {
		t.Error("script nonce elsewhere in the page")
	}
	script2, _, _ := get("/")
	if script2 == script {
		t.Error("nonce not per request")
	}
	if _, style, page := get("/offline.html"); !strings.Contains(page, `<style nonce="`+style+`">`) {
		t.Error("offline.html:", page)
	}
	for _, p := range []string{"/login", "/device"} {
		script, style, page := get(p)
		if !strings.Contains(page, `<script nonce="`+script+`" src=`) || !strings.Contains(page, `<style nonce="`+style+`">`) {
			t.Errorf("%s: %s", p, page)
		}
	}
	resp, _ := b.do("GET", "/api/auth/config", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"'strict-dynamic'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'", "script-src-attr 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP without %s: %s", want, csp)
		}
	}
	if d := cspDirective(csp, "script-src"); strings.Contains(d, "unsafe-inline") {
		t.Error(d)
	}
	if d := cspDirective(csp, "style-src"); strings.Contains(d, "unsafe-inline") {
		t.Error(d)
	}
	if strings.Contains(csp, "Report-Only") || resp.Header.Get("Content-Security-Policy-Report-Only") != "" {
		t.Error("report-only")
	}
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

// A form sent without any proof is no guess (t-101): reauth_required, nothing
// counted. Wrong answers still lock the step-up of the user; the lock does not
// extend itself and never touches the normal sign-in.
func TestEmptyStepUpIsNoGuess(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 })
	u := e.user("ida", auth.RoleUser)
	b := e.browser()
	b.login("ida")

	for i := range 12 {
		for _, r := range []struct {
			method, path string
			body         map[string]string
		}{
			{"POST", "/api/auth/totp/setup", map[string]string{}},
			{"POST", "/api/auth/sessions/revoke-others", map[string]string{}},
			{"POST", "/api/auth/password", map[string]string{"newPassword": "ein-anderes-langes-passwort"}},
		} {
			b.fetchCSRF()
			if resp, m := b.do(r.method, r.path, r.body); resp.StatusCode != 403 || m["error"] != "reauth_required" {
				t.Fatalf("%d %s %s: %s %v", i, r.method, r.path, resp.Status, m)
			}
		}
	}
	if err := e.svc.StepUpCheck(t.Context(), u.ID); err != nil {
		t.Fatal("empty requests were counted:", err)
	}
	b.fetchCSRF()
	if resp, m := b.do("POST", "/api/auth/totp/setup", map[string]string{"password": pw}); resp.StatusCode != 200 {
		t.Fatal("right password after empty requests:", resp.Status, m)
	}

	// Wrong passwords count; the sixth is refused by the lock.
	for range 5 {
		b.fetchCSRF()
		if resp, _ := b.do("POST", "/api/auth/totp/setup", map[string]string{"password": "falsch-falsch-falsch"}); resp.StatusCode != 401 {
			t.Fatal("wrong password:", resp.Status)
		}
	}
	var te *auth.ThrottleError
	err := e.svc.StepUpCheck(t.Context(), u.ID)
	if !errors.As(err, &te) {
		t.Fatal("no lock after five wrong answers:", err)
	}
	until := te.Until
	// A locked step-up neither counts nor extends: wrong and empty requests alike.
	for _, body := range []map[string]string{{"password": "falsch-falsch-falsch"}, {"password": pw}, {}} {
		b.fetchCSRF()
		resp, m := b.do("POST", "/api/auth/totp/setup", body)
		if body["password"] != "" && resp.StatusCode != 429 {
			t.Fatalf("locked, %v: %s %v", body, resp.Status, m)
		}
	}
	if err := e.svc.StepUpCheck(t.Context(), u.ID); !errors.As(err, &te) || !te.Until.Equal(until) {
		t.Fatalf("the lock moved: %v -> %v", until, err)
	}
	// The normal sign-in is not affected, neither by the lock nor by the failures behind it.
	c := e.browser()
	c.login("ida")
	if err := e.svc.StepUpCheck(t.Context(), u.ID); err == nil {
		t.Fatal("signing in must not lift the step-up lock of a possibly stolen session")
	}
}
