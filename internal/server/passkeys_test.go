// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Passkey (WebAuthn) endpoints driven end to end with a software
// authenticator (internal/auth/passkeytest).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/auth/passkeytest"
	"github.com/Midas1989/Mailclient/internal/config"
)

const (
	pkOrigin = "http://localhost:8080"
	pkRPID   = "localhost"
)

// passkeyEnv is a server whose public_url can host passkeys (http://localhost
// is allowed for development).
func passkeyEnv(t *testing.T, mod ...func(*config.Config)) *env {
	t.Helper()
	return newEnv(t, Options{}, append([]func(*config.Config){func(c *config.Config) { c.PublicURL = pkOrigin }}, mod...)...)
}

func js(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// registerPasskeyHTTP registers a: begin (with re-authentication in extra) and finish.
func (b *browser) registerPasskeyHTTP(a *passkeytest.Authenticator, name string, reauth map[string]string) (*http.Response, map[string]any) {
	b.e.t.Helper()
	resp, m := b.do("POST", "/api/auth/passkeys/register/begin", reauth)
	if resp.StatusCode != 200 {
		return resp, m
	}
	return b.do("POST", "/api/auth/passkeys/register/finish", map[string]any{
		"challengeId": m["challengeId"], "name": name, "credential": json.RawMessage(a.Create(js(b.e.t, m["publicKey"]))),
	})
}

// passkeyLoginHTTP logs in with a on a fresh anonymous browser.
func (e *env) passkeyLoginHTTP(a *passkeytest.Authenticator, hdr http.Header) (*browser, *http.Response, map[string]any) {
	e.t.Helper()
	b := e.browser()
	b.header = hdr
	b.fetchCSRF()
	resp, m := b.do("POST", "/api/auth/passkeys/login/begin", nil)
	if resp.StatusCode != 200 {
		return b, resp, m
	}
	resp, m = b.do("POST", "/api/auth/passkeys/login/finish", map[string]any{
		"challengeId": m["challengeId"], "credential": json.RawMessage(a.Get(js(e.t, m["publicKey"]))),
	})
	if resp.StatusCode == 200 {
		b.fetchCSRF() // rotated on login
	}
	return b, resp, m
}

func TestPasskeyHTTPEndToEnd(t *testing.T) {
	e := passkeyEnv(t)
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")

	// The server announces the feature.
	if _, m := e.browser().do("GET", "/api/auth/config", nil); m["passkeysEnabled"] != true {
		t.Fatalf("config: %v", m)
	}
	if resp, m := b.do("GET", "/api/auth/passkeys", nil); resp.StatusCode != 200 || len(m["passkeys"].([]any)) != 0 || m["max"] != float64(10) || m["enabled"] != true {
		t.Fatal(resp.Status, m)
	} else if r := m["reauth"].(map[string]any); r["password"] != true || r["code"] != false {
		t.Fatalf("reauth hint: %v", r)
	}

	a := passkeytest.New(pkOrigin, pkRPID)
	resp, m := b.registerPasskeyHTTP(a, "Anna's phone", map[string]string{"password": pw})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	pk := m["passkey"].(map[string]any)
	id := int(pk["id"].(float64))
	if pk["name"] != "Anna's phone" || pk["usable"] != true || pk["credentialId"] != nil || pk["publicKey"] != nil {
		t.Fatalf("passkey view: %v", pk)
	}
	_, m = b.do("GET", "/api/auth/passkeys", nil)
	if len(m["passkeys"].([]any)) != 1 {
		t.Fatal(m)
	}
	_, me := b.do("GET", "/api/auth/me", nil)
	if me["user"].(map[string]any)["passkeys"] != float64(1) {
		t.Fatalf("me: %v", me)
	}

	// Login with the passkey from another browser: a normal session with login method 'passkey'.
	pb, resp, m := e.passkeyLoginHTTP(a, nil)
	if resp.StatusCode != 200 || m["status"] != "ok" || m["user"].(map[string]any)["username"] != "anna" || m["mfaSetupRequired"] != false {
		t.Fatal(resp.Status, m)
	}
	var sc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			sc = c
		}
	}
	if sc == nil || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Value == "" {
		t.Fatalf("session cookie: %+v", sc)
	}
	_, me = pb.do("GET", "/api/auth/me", nil)
	sess := me["session"].(map[string]any)
	if sess["loginMethod"] != "passkey" || sess["mfa"] != true || sess["kind"] != "web" {
		t.Fatalf("session: %v", sess)
	}
	// ... which works like any other: settings, logout, revocation.
	if resp, _ := pb.do("PUT", "/api/settings/ui", map[string]string{"theme": "dark"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	_, m = b.do("GET", "/api/auth/passkeys", nil)
	if m["passkeys"].([]any)[0].(map[string]any)["lastUsedAt"] == nil {
		t.Fatalf("last use not recorded: %v", m)
	}
	if resp, _ := pb.do("POST", "/api/auth/logout", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := pb.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatal("session survived logout")
	}

	// Rename (no re-authentication needed), validation, unknown ids.
	if resp, _ := b.do("PATCH", "/api/auth/passkeys/"+itoa(id), map[string]string{"name": "Work phone"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, m := b.do("PATCH", "/api/auth/passkeys/"+itoa(id), map[string]string{"name": strings.Repeat("x", 80)}); resp.StatusCode != 400 || m["error"] != "invalid_input" {
		t.Fatal(resp.Status, m)
	}
	if resp, m := b.do("PATCH", "/api/auth/passkeys/9999", map[string]string{"name": "x"}); resp.StatusCode != 404 {
		t.Fatal(resp.Status, m)
	}
	_, m = b.do("GET", "/api/auth/passkeys", nil)
	if m["passkeys"].([]any)[0].(map[string]any)["name"] != "Work phone" {
		t.Fatal(m)
	}

	// Delete needs the password; a wrong id changes nothing.
	if resp, m := b.do("DELETE", "/api/auth/passkeys/"+itoa(id), nil); resp.StatusCode != 401 || m["error"] != "invalid_credentials" {
		t.Fatalf("delete without re-authentication: %s %v", resp.Status, m)
	}
	if resp, _ := b.do("DELETE", "/api/auth/passkeys/"+itoa(id), map[string]string{"password": "wrong password!"}); resp.StatusCode != 401 {
		t.Fatal(resp.Status)
	}
	if resp, _ := b.do("DELETE", "/api/auth/passkeys/9999", map[string]string{"password": pw}); resp.StatusCode != 404 {
		t.Fatal(resp.Status)
	}
	if _, m = b.do("GET", "/api/auth/passkeys", nil); len(m["passkeys"].([]any)) != 1 {
		t.Fatal("passkey lost by a failed delete")
	}
	if resp, _ := b.do("DELETE", "/api/auth/passkeys/"+itoa(id), map[string]string{"password": pw}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if _, _, m := e.passkeyLoginHTTP(a, nil); m["error"] != "invalid_credentials" {
		t.Fatalf("deleted passkey: %v", m)
	}
	// Everything is in the audit log.
	for _, want := range []string{"passkey.register", "passkey.rename", "passkey.delete"} {
		entries, _ := e.svc.ListAudit(context.Background(), 200, 0)
		found := false
		for _, en := range entries {
			found = found || (en.Action == want && en.Success)
		}
		if !found {
			t.Errorf("no audit entry %s", want)
		}
	}
}

func TestPasskeyEndpointsNeedLoginAndCSRF(t *testing.T) {
	e := passkeyEnv(t)
	e.user("anna", auth.RoleUser)
	anon := e.browser()
	anon.fetchCSRF()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/auth/passkeys"}, {"POST", "/api/auth/passkeys/register/begin"}, {"POST", "/api/auth/passkeys/register/finish"},
		{"PATCH", "/api/auth/passkeys/1"}, {"DELETE", "/api/auth/passkeys/1"},
	} {
		if resp, _ := anon.do(c.method, c.path, map[string]string{}); resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Errorf("%s %s without session: %s", c.method, c.path, resp.Status)
		}
	}
	// The unauthenticated login endpoints are CSRF-protected like /api/auth/login.
	nocsrf := e.browser()
	for _, path := range []string{"/api/auth/passkeys/login/begin", "/api/auth/passkeys/login/finish"} {
		if resp, m := nocsrf.do("POST", path, map[string]string{}); resp.StatusCode != 403 || m["error"] != "csrf" {
			t.Errorf("%s without CSRF token: %s %v", path, resp.Status, m)
		}
		// Right token, foreign Origin.
		anon.header = http.Header{"Origin": {"https://evil.example"}}
		if resp, m := anon.do("POST", path, map[string]string{}); resp.StatusCode != 403 || m["error"] != "csrf" {
			t.Errorf("%s from a foreign origin: %s %v", path, resp.Status, m)
		}
		anon.header = nil
	}
	// Same for the logged-in state changes.
	b := e.browser()
	b.login("anna")
	good := b.csrf
	b.csrf = ""
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/auth/passkeys/register/begin"}, {"POST", "/api/auth/passkeys/register/finish"},
		{"PATCH", "/api/auth/passkeys/1"}, {"DELETE", "/api/auth/passkeys/1"},
	} {
		if resp, m := b.do(c.method, c.path, map[string]string{"password": pw}); resp.StatusCode != 403 || m["error"] != "csrf" {
			t.Errorf("%s %s without CSRF token: %s %v", c.method, c.path, resp.Status, m)
		}
	}
	b.csrf = good
	b.header = http.Header{"Origin": {"https://evil.example"}}
	if resp, m := b.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": pw}); resp.StatusCode != 403 || m["error"] != "csrf" {
		t.Errorf("register from a foreign origin: %s %v", resp.Status, m)
	}
}

func TestPasskeyRegistrationNeedsReauth(t *testing.T) {
	e := passkeyEnv(t)
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	a := passkeytest.New(pkOrigin, pkRPID)

	for name, body := range map[string]map[string]string{"nothing": nil, "wrong password": {"password": "wrong password!"}} {
		if resp, m := b.do("POST", "/api/auth/passkeys/register/begin", body); resp.StatusCode != 401 || m["error"] != "invalid_credentials" {
			t.Errorf("%s: %s %v", name, resp.Status, m)
		}
	}
	// Wrong answers lock the step-up of the user persistently (shared with TOTP disable, device approval ...).
	for range 6 {
		b.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": "wrong password!"})
	}
	if resp, m := b.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": pw}); resp.StatusCode != 429 || m["error"] != "throttled" {
		t.Fatalf("step-up lockout not applied: %s %v", resp.Status, m)
	}
	// There is no finish without a begin: the ceremony id is the proof that the re-authentication happened.
	cred := json.RawMessage(a.Create(js(t, map[string]any{"challenge": "AAAAAAAAAAAAAAAAAAAAAA", "user": map[string]string{"id": "AAAA"}})))
	if resp, m := b.do("POST", "/api/auth/passkeys/register/finish", map[string]any{"challengeId": "x", "credential": cred}); resp.StatusCode != 400 || m["error"] != "passkey_challenge" {
		t.Fatalf("finish without begin: %s %v", resp.Status, m)
	}
}

func TestPasskeyRegistrationLimit(t *testing.T) {
	e := passkeyEnv(t, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 })
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	for i := range 10 {
		a := passkeytest.New(pkOrigin, pkRPID)
		if resp, m := b.registerPasskeyHTTP(a, "", map[string]string{"password": pw}); resp.StatusCode != 201 {
			t.Fatalf("passkey %d: %s %v", i+1, resp.Status, m)
		}
	}
	if resp, m := b.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": pw}); resp.StatusCode != 409 || m["error"] != "passkey_limit" {
		t.Fatalf("11th passkey: %s %v", resp.Status, m)
	}
	_, m := b.do("GET", "/api/auth/passkeys", nil)
	list := m["passkeys"].([]any)
	if len(list) != 10 {
		t.Fatal(len(list))
	}
	first := int(list[0].(map[string]any)["id"].(float64))
	if resp, _ := b.do("DELETE", "/api/auth/passkeys/"+itoa(first), map[string]string{"password": pw}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, m := b.registerPasskeyHTTP(passkeytest.New(pkOrigin, pkRPID), "again", map[string]string{"password": pw}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
}

func TestPasskeyLoginFailuresAreGenericAndThrottled(t *testing.T) {
	e := passkeyEnv(t, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 })
	u := e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	a := passkeytest.New(pkOrigin, pkRPID)
	if resp, m := b.registerPasskeyHTTP(a, "k", map[string]string{"password": pw}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}

	// Unknown authenticator, disabled user, wrong origin: all the same answer.
	stranger := passkeytest.New(pkOrigin, pkRPID)
	stranger.UserHandle = a.UserHandle
	_, r1, m1 := e.passkeyLoginHTTP(stranger, http.Header{"X-Forwarded-For": {"203.0.113.1"}})
	phish := passkeytest.New(pkOrigin, pkRPID)
	phish.CredentialID, phish.Key, phish.UserHandle, phish.Origin = a.CredentialID, a.Key, a.UserHandle, "https://evil.example"
	_, r2, m2 := e.passkeyLoginHTTP(phish, http.Header{"X-Forwarded-For": {"203.0.113.2"}})
	if err := e.svc.SetDisabled(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	_, r3, m3 := e.passkeyLoginHTTP(a, http.Header{"X-Forwarded-For": {"203.0.113.3"}})
	for i, r := range []*http.Response{r1, r2, r3} {
		if r.StatusCode != 401 {
			t.Errorf("case %d: %s", i, r.Status)
		}
	}
	if !jsonEqual(m1, m2) || !jsonEqual(m1, m3) || m1["error"] != "invalid_credentials" {
		t.Fatalf("answers differ: %v / %v / %v", m1, m2, m3)
	}
	if strings.Contains(m1["message"].(string), "disabled") || strings.Contains(m1["message"].(string), "origin") {
		t.Fatalf("answer reveals the reason: %v", m1)
	}
	e.svc.SetDisabled(context.Background(), u.ID, false)

	// Repeated failures lock the network (429 + Retry-After) but not others, and not the password login.
	attacker := http.Header{"X-Forwarded-For": {"198.51.100.7"}}
	for range 5 {
		e.passkeyLoginHTTP(stranger, attacker)
	}
	_, resp, m := e.passkeyLoginHTTP(a, attacker)
	if resp.StatusCode != 429 || m["error"] != "throttled" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("locked network: %s %v", resp.Status, m)
	}
	if _, resp, m := e.passkeyLoginHTTP(a, http.Header{"X-Forwarded-For": {"198.51.100.8"}}); resp.StatusCode != 200 {
		t.Fatalf("other network: %s %v", resp.Status, m)
	}
	pwb := e.browser()
	pwb.header = attacker
	pwb.fetchCSRF()
	if resp, m := pwb.do("POST", "/api/auth/login", map[string]string{"username": "anna", "password": pw}); resp.StatusCode != 200 {
		t.Fatalf("password login from the passkey-locked network: %s %v", resp.Status, m)
	}
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestPasskeyLoginReplayExpiryAndMalformed(t *testing.T) {
	e := passkeyEnv(t, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 })
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	a := passkeytest.New(pkOrigin, pkRPID)
	b.registerPasskeyHTTP(a, "k", map[string]string{"password": pw})

	begin := func(h http.Header) (*browser, string, []byte) {
		x := e.browser()
		x.header = h
		x.fetchCSRF()
		_, m := x.do("POST", "/api/auth/passkeys/login/begin", nil)
		return x, m["challengeId"].(string), js(t, m["publicKey"])
	}
	// Replay.
	x, id, opts := begin(http.Header{"X-Forwarded-For": {"192.0.2.1"}})
	body := map[string]any{"challengeId": id, "credential": json.RawMessage(a.Get(opts))}
	if resp, m := x.do("POST", "/api/auth/passkeys/login/finish", body); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	y := e.browser()
	y.header = http.Header{"X-Forwarded-For": {"192.0.2.2"}}
	y.fetchCSRF()
	if resp, m := y.do("POST", "/api/auth/passkeys/login/finish", body); resp.StatusCode != 401 || m["error"] != "invalid_credentials" {
		t.Fatalf("replay: %s %v", resp.Status, m)
	}
	// Expired.
	x, id, opts = begin(http.Header{"X-Forwarded-For": {"192.0.2.3"}})
	cred := a.Get(opts)
	e.svc.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	defer func() { e.svc.Now = time.Now }()
	if resp, m := x.do("POST", "/api/auth/passkeys/login/finish", map[string]any{"challengeId": id, "credential": json.RawMessage(cred)}); resp.StatusCode != 401 || m["error"] != "invalid_credentials" {
		t.Fatalf("expired: %s %v", resp.Status, m)
	}
	e.svc.Now = time.Now
	// Malformed bodies never crash or log in.
	x, id, _ = begin(http.Header{"X-Forwarded-For": {"192.0.2.4"}})
	for _, bad := range []any{
		map[string]any{"challengeId": id},
		map[string]any{"challengeId": id, "credential": "text"},
		map[string]any{"challengeId": id, "credential": map[string]any{"id": "x", "type": "public-key", "response": map[string]any{}}},
		map[string]any{"challengeId": 5},
	} {
		if resp, _ := x.do("POST", "/api/auth/passkeys/login/finish", bad); resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("malformed body %v: %s", bad, resp.Status)
		}
	}
}

func TestPasskeyBeginIsRateLimited(t *testing.T) {
	e := passkeyEnv(t) // login_rate_per_minute 10
	b := e.browser()
	b.header = http.Header{"X-Forwarded-For": {"192.0.2.77"}}
	b.fetchCSRF()
	limited := false
	for range 30 {
		if resp, m := b.do("POST", "/api/auth/passkeys/login/begin", nil); resp.StatusCode == 429 && m["error"] == "rate_limited" && resp.Header.Get("Retry-After") != "" {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("unauthenticated begin requests are not rate limited")
	}
}

func TestPasskeysDisabled(t *testing.T) {
	for name, mod := range map[string]func(*config.Config){
		"switched off":    func(c *config.Config) { f := false; c.Auth.Passkeys.Enabled = &f },
		"insecure origin": func(c *config.Config) { c.PublicURL = "http://127.0.0.1:8080" },
	} {
		e := passkeyEnv(t, mod)
		e.user("anna", auth.RoleUser)
		if _, m := e.browser().do("GET", "/api/auth/config", nil); m["passkeysEnabled"] != false {
			t.Errorf("%s: config %v", name, m)
		}
		anon := e.browser()
		anon.fetchCSRF()
		for _, path := range []string{"/api/auth/passkeys/login/begin", "/api/auth/passkeys/login/finish"} {
			if resp, m := anon.do("POST", path, map[string]string{}); resp.StatusCode != 404 || m["error"] != "passkeys_disabled" {
				t.Errorf("%s %s: %s %v", name, path, resp.Status, m)
			}
		}
		b := e.browser()
		b.login("anna")
		if resp, m := b.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": pw}); resp.StatusCode != 404 || m["error"] != "passkeys_disabled" {
			t.Errorf("%s register: %s %v", name, resp.Status, m)
		}
		// Existing ones can still be listed.
		if resp, m := b.do("GET", "/api/auth/passkeys", nil); resp.StatusCode != 200 || m["enabled"] != false {
			t.Errorf("%s list: %s %v", name, resp.Status, m)
		}
	}
}

func TestPasskeyDeviceTokensCannotManage(t *testing.T) {
	e := passkeyEnv(t)
	e.user("anna", auth.RoleUser)
	c := e.browser()
	resp, m := c.do("POST", "/api/device/login", map[string]string{"username": "anna", "password": pw, "device_name": "Pixel"})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	c.bearer = m["access_token"].(string)
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/auth/passkeys/register/begin"}, {"PATCH", "/api/auth/passkeys/1"}, {"DELETE", "/api/auth/passkeys/1"},
	} {
		if resp, m := c.do(r.method, r.path, map[string]string{"password": pw, "name": "x"}); resp.StatusCode != 403 || m["error"] != "forbidden" {
			t.Errorf("%s %s with a device token: %s %v", r.method, r.path, resp.Status, m)
		}
	}
	if resp, _ := c.do("GET", "/api/auth/passkeys", nil); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
}

// reset-2fa of the administrator removes passkeys; the admin list shows their number.
func TestPasskeyAdminReset2FA(t *testing.T) {
	e := passkeyEnv(t)
	e.user("root", auth.RoleAdmin)
	u := e.user("anna", auth.RoleUser)
	other := e.user("bert", auth.RoleUser)
	ab := e.browser()
	ab.login("anna")
	a := passkeytest.New(pkOrigin, pkRPID)
	if resp, m := ab.registerPasskeyHTTP(a, "k", map[string]string{"password": pw}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	bb := e.browser()
	bb.login("bert")
	bb.registerPasskeyHTTP(passkeytest.New(pkOrigin, pkRPID), "b", map[string]string{"password": pw})

	admin := e.browser()
	admin.login("root")
	_, m := admin.do("GET", "/api/admin/users", nil)
	counts := map[string]float64{}
	for _, x := range m["users"].([]any) {
		um := x.(map[string]any)
		counts[um["username"].(string)] = um["passkeys"].(float64)
	}
	if counts["anna"] != 1 || counts["bert"] != 1 || counts["root"] != 0 {
		t.Fatalf("admin list counts: %v", counts)
	}
	if _, m := admin.do("GET", "/api/admin/users/"+itoa(int(u.ID)), nil); m["user"].(map[string]any)["passkeys"] != float64(1) {
		t.Fatalf("admin user: %v", m)
	}
	if resp, _ := admin.do("POST", "/api/admin/users/"+itoa(int(u.ID))+"/reset-2fa", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	_, m = admin.do("GET", "/api/admin/users/"+itoa(int(u.ID)), nil)
	if m["user"].(map[string]any)["passkeys"] != float64(0) {
		t.Fatalf("passkeys left: %v", m)
	}
	if _, _, m := e.passkeyLoginHTTP(a, nil); m["error"] != "invalid_credentials" {
		t.Fatalf("login with a reset passkey: %v", m)
	}
	if n, _ := e.svc.ListPasskeys(context.Background(), other.ID); len(n) != 1 {
		t.Fatal("reset touched another user")
	}
}

// totp_required_for_admins: a passkey session is exempt, a password session of
// the same admin is not, and a password session cannot be used to add a passkey.
func TestPasskeyMFARequirementForAdmins(t *testing.T) {
	e := passkeyEnv(t, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	admin, tp := e.totpUser("root", auth.RoleAdmin)

	// Register the passkey from a session with TOTP (password + code).
	web := e.browser()
	web.fetchCSRF()
	_, m := web.do("POST", "/api/auth/login", map[string]string{"username": "root", "password": pw})
	if m["status"] != "mfa_required" {
		t.Fatal(m)
	}
	if resp, m := web.do("POST", "/api/auth/login/totp", map[string]string{"challenge": m["challenge"].(string), "code": tp.Code(time.Now())}); resp.StatusCode != 200 || m["mfaSetupRequired"] != false {
		t.Fatal(resp.Status, m)
	}
	web.fetchCSRF()
	a := passkeytest.New(pkOrigin, pkRPID)
	if resp, m := web.registerPasskeyHTTP(a, "yubikey", map[string]string{"password": pw, "code": tp.Code(time.Now().Add(30 * time.Second))}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	// The administrator's TOTP is gone (lost phone, reset); the passkey remains.
	if err := e.svc.ResetTOTP(context.Background(), admin.ID); err != nil {
		t.Fatal(err)
	}

	pwb := e.browser()
	pwb.login("root")
	if resp, m := pwb.do("GET", "/api/admin/users", nil); resp.StatusCode != 403 || m["error"] != "mfa_setup_required" {
		t.Fatalf("password session: %s %v", resp.Status, m)
	}
	if _, me := pwb.do("GET", "/api/auth/me", nil); me["mfaSetupRequired"] != true {
		t.Fatalf("password session /me: %v", me)
	}
	// ... and it may not add a passkey to get around it.
	if resp, m := pwb.do("POST", "/api/auth/passkeys/register/begin", map[string]string{"password": pw}); resp.StatusCode != 403 || m["error"] != "mfa_setup_required" {
		t.Fatalf("passkey registration from a session owing the TOTP enrolment: %s %v", resp.Status, m)
	}
	// Login response of the password path says so too.
	pwb2 := e.browser()
	pwb2.fetchCSRF()
	if _, m := pwb2.do("POST", "/api/auth/login", map[string]string{"username": "root", "password": pw}); m["mfaSetupRequired"] != true {
		t.Fatalf("password login: %v", m)
	}

	pkb, resp, m := e.passkeyLoginHTTP(a, nil)
	if resp.StatusCode != 200 || m["mfaSetupRequired"] != false {
		t.Fatal(resp.Status, m)
	}
	if resp, m := pkb.do("GET", "/api/admin/users", nil); resp.StatusCode != 200 {
		t.Fatalf("passkey session on the admin API: %s %v", resp.Status, m)
	}
	if _, me := pkb.do("GET", "/api/auth/me", nil); me["mfaSetupRequired"] != false {
		t.Fatalf("passkey session /me: %v", me)
	}
}

// Sessions revoked / users disabled behave as for password sessions.
func TestPasskeySessionRevocationAndDisable(t *testing.T) {
	e := passkeyEnv(t)
	u := e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	a := passkeytest.New(pkOrigin, pkRPID)
	b.registerPasskeyHTTP(a, "k", map[string]string{"password": pw})

	pb, resp, m := e.passkeyLoginHTTP(a, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := b.do("POST", "/api/auth/sessions/revoke-others", nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := pb.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatalf("revoked passkey session still works: %s", resp.Status)
	}
	pb, _, _ = e.passkeyLoginHTTP(a, nil)
	e.svc.SetDisabled(context.Background(), u.ID, true) // ends all sessions, like for password sessions
	if resp, _ := pb.do("GET", "/api/auth/me", nil); resp.StatusCode != 401 {
		t.Fatalf("disabled user's passkey session still works: %s", resp.Status)
	}
	if _, resp, _ := e.passkeyLoginHTTP(a, nil); resp.StatusCode != 401 {
		t.Fatalf("disabled user logged in with a passkey: %s", resp.Status)
	}
}

func TestLoginPageOffersPasskeys(t *testing.T) {
	e := passkeyEnv(t)
	b := e.browser()
	req, _ := http.NewRequest("GET", e.ts.URL+"/login", nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	var tmp [4096]byte
	for {
		n, err := resp.Body.Read(tmp[:])
		buf.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	page := buf.String()
	if !strings.Contains(page, `id="passkey"`) || !strings.Contains(page, "Sign in with a passkey") {
		t.Fatalf("login page without passkey button: %s", page)
	}
	// The button starts hidden: only the script shows it, after feature detection and the config check.
	if !strings.Contains(page, `id="passkey" class="secondary hidden"`) {
		t.Fatal("passkey button not hidden by default")
	}
	resp2, err := b.c.Get(e.ts.URL + "/login.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body := new(strings.Builder)
	for {
		n, err := resp2.Body.Read(tmp[:])
		body.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	for _, want := range []string{"PublicKeyCredential", "passkeysEnabled", "/api/auth/passkeys/login/begin", "/api/auth/passkeys/login/finish"} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("login.js lacks %q", want)
		}
	}
}
