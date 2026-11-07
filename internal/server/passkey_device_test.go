// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Passkeys for connecting apps (api_device_passkey.go): device approval in the
// browser with a passkey, the direct passkey sign-in of the Android app and
// its Digital Asset Links. Driven with the software authenticator of
// internal/auth/passkeytest, including the mandatory negative cases of Basis
// spec/AUTH.md §6 (wrong origin and RP ID, UV=0, old and reused challenge,
// counter going backwards) plus foreign user, disabled user and a ceremony of
// another purpose.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/auth/passkeytest"
	"github.com/Midas1989/Mailclient/internal/config"
)

// The test app's signing certificate (any 32 bytes stand for its SHA-256).
var (
	appCertSum   = sha256.Sum256([]byte("TuxMail test signing certificate"))
	appOrigin    = "android:apk-key-hash:" + base64.RawURLEncoding.EncodeToString(appCertSum[:])
	appFingerHex = func() string {
		var p []string
		for _, b := range appCertSum {
			p = append(p, strings.ToUpper(hexByte(b)))
		}
		return strings.Join(p, ":")
	}()
)

func hexByte(b byte) string {
	const d = "0123456789abcdef"
	return string([]byte{d[b>>4], d[b&15]})
}

func withAndroidApp(c *config.Config) {
	c.Auth.Passkeys.AndroidApps = []config.AndroidApp{{PackageName: "org.tuxmail.app", Fingerprints: []string{appFingerHex}}}
}

func fastLogin(c *config.Config) { c.Limits.LoginRatePerMinute = 1000 }

// enrolPasskey creates user name with a passkey registered in the web client.
func enrolPasskey(t *testing.T, e *env, name string) (*auth.User, *passkeytest.Authenticator) {
	t.Helper()
	u := e.user(name, auth.RoleUser)
	b := e.browser()
	b.login(name)
	a := passkeytest.New(pkOrigin, pkRPID)
	if resp, m := b.registerPasskeyHTTP(a, "phone", map[string]string{"password": pw}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	return u, a
}

// native is a client without cookies, CSRF token or Origin (CapacitorHttp).
func (e *env) native(ip string) *browser {
	b := e.browser()
	b.noOrigin = true
	if ip != "" {
		b.header = http.Header{"X-Forwarded-For": {ip}}
	}
	return b
}

func startGrant(t *testing.T, e *env) (deviceCode, userCode string) {
	t.Helper()
	resp, m := e.native("").do("POST", "/api/device/authorize", map[string]string{"client_name": "TuxMail Android (Pixel)", "scope": "data"})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	return m["device_code"].(string), m["user_code"].(string)
}

func pollToken(t *testing.T, e *env, deviceCode string) (*http.Response, map[string]any) {
	t.Helper()
	return e.native("").do("POST", "/api/device/token", map[string]string{"device_code": deviceCode})
}

// approvalProofOf runs the passkey confirmation of request uc on the approval page of b with a.
func approvalProofOf(t *testing.T, b *browser, a *passkeytest.Authenticator, uc string) map[string]any {
	t.Helper()
	resp, m := b.do("POST", "/api/device/approve/passkey", map[string]string{"user_code": uc})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	return map[string]any{"challengeId": m["challengeId"], "credential": json.RawMessage(a.Get(js(t, m["publicKey"])))}
}

// Device code phishing: a link /device?user_code=<attacker's code>, the victim
// signs in with the passkey and comes back. That sign-in must not approve
// anything: the proof comes after the request was shown and is bound to it.
func TestDeviceApprovalNeedsProofAfterPasskeySignIn(t *testing.T) {
	e := passkeyEnv(t, fastLogin)
	_, a := enrolPasskey(t, e, "anna")
	dc, uc := startGrant(t, e)
	_, uc2 := startGrant(t, e)

	pb, resp, m := e.passkeyLoginHTTP(a, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	resp, m = pb.do("GET", "/api/device/request?user_code="+uc, nil)
	if resp.StatusCode != 200 || m["passkey"] != true || m["passkeyFresh"] != nil {
		t.Fatalf("request: %s %v", resp.Status, m)
	}
	for _, code := range []string{uc, uc2} {
		if resp, m := pb.do("POST", "/api/device/approve", map[string]any{"user_code": code, "approve": true}); resp.StatusCode != 403 || m["error"] != "reauth_required" {
			t.Fatalf("fresh passkey session approved %s without proof: %s %v", code, resp.Status, m)
		}
	}
	if resp, m := pollToken(t, e, dc); resp.StatusCode == 200 {
		t.Fatalf("token without proof: %v", m)
	}
	// A proof for one request does not approve another.
	proof := approvalProofOf(t, pb, a, uc)
	if resp, m := pb.do("POST", "/api/device/approve", map[string]any{"user_code": uc2, "passkey": proof}); resp.StatusCode != 400 || m["error"] != "passkey_invalid" {
		t.Fatalf("proof of another request: %s %v", resp.Status, m)
	}
	// The proof given for the shown request works, once; the session id rotates.
	before := pb.sessionCookieValue()
	proof = approvalProofOf(t, pb, a, uc)
	if resp, m := pb.do("POST", "/api/device/approve", map[string]any{"user_code": uc, "passkey": proof}); resp.StatusCode != 204 {
		t.Fatalf("approve with passkey: %s %v", resp.Status, m)
	}
	if pb.sessionCookieValue() == before {
		t.Error("session not rotated after the passkey step-up")
	}
	resp, m = pollToken(t, e, dc)
	if resp.StatusCode != 200 || m["access_token"] == "" || m["scope"] != "data" {
		t.Fatalf("token: %s %v", resp.Status, m)
	}
	assertAudit(t, e, "device.passkey", "anna", true)
	// Begin needs a pending request.
	if resp, m := pb.do("POST", "/api/device/approve/passkey", map[string]string{"user_code": "BCDF-GHJK"}); resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("unknown code: %s %v", resp.Status, m)
	}
}

func TestDeviceApprovalPasskeyForPasswordSession(t *testing.T) {
	e := passkeyEnv(t, fastLogin)
	_, a := enrolPasskey(t, e, "anna")
	_, uc := startGrant(t, e)
	b := e.browser()
	b.login("anna")
	if _, m := b.do("GET", "/api/device/request?user_code="+uc, nil); m["passkey"] != true {
		t.Fatalf("request: %v", m)
	}
	if resp, m := b.do("POST", "/api/device/approve", map[string]any{"user_code": uc}); resp.StatusCode != 403 || m["error"] != "reauth_required" {
		t.Fatalf("password session without proof: %s %v", resp.Status, m)
	}
	if resp, m := b.do("POST", "/api/device/approve", map[string]any{"user_code": uc, "passkey": approvalProofOf(t, b, a, uc)}); resp.StatusCode != 204 {
		t.Fatalf("passkey proof: %s %v", resp.Status, m)
	}
}

// Refused passkey proofs count in the step-up lock of the account (s:<id>).
func TestDeviceApprovalPasskeyStepUpLock(t *testing.T) {
	e := passkeyEnv(t, fastLogin)
	_, a := enrolPasskey(t, e, "anna")
	_, uc := startGrant(t, e)
	b := e.browser()
	b.login("anna")
	noUV := *a
	noUV.UV = false
	for range 5 {
		b.do("POST", "/api/device/approve", map[string]any{"user_code": uc, "passkey": approvalProofOf(t, b, &noUV, uc)})
	}
	if resp, m := b.do("POST", "/api/device/approve/passkey", map[string]string{"user_code": uc}); resp.StatusCode != 429 || m["error"] != "throttled" {
		t.Fatalf("locked begin: %s %v", resp.Status, m)
	}
	if resp, m := b.do("POST", "/api/device/approve", map[string]any{"user_code": uc, "password": pw}); resp.StatusCode != 429 {
		t.Fatalf("password during the lock: %s %v", resp.Status, m)
	}
}

func TestDeviceApprovalPasskeyNegative(t *testing.T) {
	e := passkeyEnv(t, fastLogin)
	_, a := enrolPasskey(t, e, "anna")
	_, bob := enrolPasskey(t, e, "bob")
	_, uc := startGrant(t, e)
	b := e.browser()
	b.login("anna")
	approve := func(proof map[string]any) (*http.Response, map[string]any) {
		return b.do("POST", "/api/device/approve", map[string]any{"user_code": uc, "passkey": proof})
	}
	refused := func(what string, proof map[string]any) {
		t.Helper()
		if resp, m := approve(proof); resp.StatusCode != 400 || m["error"] != "passkey_invalid" {
			t.Errorf("%s: %s %v", what, resp.Status, m)
		}
		e.svc.ClearStepUp(context.Background(), 1) // each case on its own (the lock has its own test)
	}

	// Foreign user: bob's passkey for anna's approval.
	refused("passkey of another user", approvalProofOf(t, b, bob, uc))
	// Wrong origin (phishing site) and wrong RP ID.
	phish := *a
	phish.Origin = "https://evil.example"
	refused("wrong origin", approvalProofOf(t, b, &phish, uc))
	wrongRP := *a
	wrongRP.RPID = "evil.example"
	refused("wrong RP ID", approvalProofOf(t, b, &wrongRP, uc))
	// The app origin is never valid in the browser ceremony.
	appA := *a
	appA.Origin = appOrigin
	refused("app origin", approvalProofOf(t, b, &appA, uc))
	// No user verification.
	noUV := *a
	noUV.UV = false
	refused("UV=0", approvalProofOf(t, b, &noUV, uc))
	// Expired challenge.
	proof := approvalProofOf(t, b, a, uc)
	e.svc.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	refused("expired challenge", proof)
	e.svc.Now = time.Now
	// Reused challenge: the first use succeeds, the second is refused.
	proof = approvalProofOf(t, b, a, uc)
	if resp, m := approve(proof); resp.StatusCode != 204 {
		t.Fatalf("valid proof: %s %v", resp.Status, m)
	}
	_, uc = startGrant(t, e)
	refused("reused challenge", proof)
	// Counter going backwards (the valid proof above stored a counter > 0).
	back := *a
	back.SignCount, back.CountStep = 0, 0
	refused("counter backwards", approvalProofOf(t, b, &back, uc))
	// A challenge of the web sign-in (other purpose) is no approval.
	x := e.browser()
	x.fetchCSRF()
	_, m := x.do("POST", "/api/auth/passkeys/login/begin", nil)
	refused("web login ceremony", map[string]any{"challengeId": m["challengeId"], "credential": json.RawMessage(a.Get(js(t, m["publicKey"])))})
	// Another user's approval ceremony cannot be used (user bound).
	bb := e.browser()
	bb.login("bob")
	refused("bob's ceremony", approvalProofOf(t, bb, a, uc))
	assertAudit(t, e, "device.passkey", "anna", false)

	// Device tokens and anonymous callers cannot start the confirmation.
	if resp, _ := e.browser().do("POST", "/api/device/approve/passkey", map[string]string{"user_code": uc}); resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Errorf("anonymous: %s", resp.Status)
	}
	d := deviceTok(t, e, "anna", "data")
	if resp, m := d.do("POST", "/api/device/approve/passkey", map[string]string{"user_code": uc}); resp.StatusCode != 403 || m["error"] != "device_token_forbidden" {
		t.Errorf("device token: %s %v", resp.Status, m)
	}
	// An account without passkey gets a clear answer.
	e.user("carl", auth.RoleUser)
	cb := e.browser()
	cb.login("carl")
	if resp, m := cb.do("POST", "/api/device/approve/passkey", map[string]string{"user_code": uc}); resp.StatusCode != 404 {
		t.Errorf("no passkey: %s %v", resp.Status, m)
	}
	if _, m := cb.do("GET", "/api/device/request?user_code="+uc, nil); m["passkey"] != false {
		t.Errorf("request without passkey: %v", m)
	}
}

// appLogin runs the direct passkey sign-in of the app with a from ip.
func appLogin(t *testing.T, e *env, a *passkeytest.Authenticator, ip string, extra map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	n := e.native(ip)
	resp, m := n.do("POST", "/api/device/passkey/begin", nil)
	if resp.StatusCode != 200 {
		return resp, m
	}
	body := map[string]any{"challengeId": m["challengeId"], "credential": json.RawMessage(a.Get(js(t, m["publicKey"]))),
		"device_name": "TuxMail Android (Pixel)", "scope": "data"}
	for k, v := range extra {
		body[k] = v
	}
	return n.do("POST", "/api/device/passkey/login", body)
}

func TestAppPasskeyLogin(t *testing.T) {
	e := passkeyEnv(t, fastLogin, withAndroidApp)
	_, a := enrolPasskey(t, e, "anna")
	if _, m := e.browser().do("GET", "/api/auth/config", nil); m["passkeyAppLogin"] != true {
		t.Fatalf("config: %v", m)
	}
	// Credential Manager in the app: same credential, the app's origin.
	a.Origin = appOrigin
	resp, m := appLogin(t, e, a, "192.0.2.10", nil)
	if resp.StatusCode != 200 || m["token_type"] != "Bearer" || m["scope"] != "data" || m["user"].(map[string]any)["username"] != "anna" {
		t.Fatalf("app login: %s %v", resp.Status, m)
	}
	for _, c := range resp.Cookies() {
		t.Errorf("the native login set a cookie: %v", c.Name)
	}
	d := e.browser()
	d.noOrigin = true
	d.bearer = m["access_token"].(string)
	resp, me := d.do("GET", "/api/auth/me", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, me)
	}
	sess := me["session"].(map[string]any)
	if sess["kind"] != "device" || sess["loginMethod"] != "passkey" || sess["mfa"] != true || sess["deviceName"] != "TuxMail Android (Pixel)" {
		t.Fatalf("session: %v", sess)
	}
	// The scopes work as with /api/device/login: data, no provisioning secrets.
	if resp, _ := d.do("GET", "/api/provisioning/secrets", nil); resp.StatusCode != 403 {
		t.Fatalf("provisioning with a data token: %s", resp.Status)
	}
	assertAuditDetails(t, e, "login", "anna", "kind=device method=passkey mfa=true")
	// The web origin gets no device token here: app origins only.
	a.Origin = pkOrigin
	if resp, m := appLogin(t, e, a, "192.0.2.11", nil); resp.StatusCode != 401 || m["access_token"] != nil {
		t.Fatalf("web origin at the app endpoint: %s %v", resp.Status, m)
	}
	// Invalid client key and scope are refused before anything else.
	a.Origin = appOrigin
	if resp, m := appLogin(t, e, a, "192.0.2.12", map[string]any{"client_public_key": "kurz"}); resp.StatusCode != 400 || m["error"] != "invalid_request" {
		t.Fatalf("bad key: %s %v", resp.Status, m)
	}
	if resp, m := appLogin(t, e, a, "192.0.2.12", map[string]any{"scope": "admin"}); resp.StatusCode != 400 || m["error"] != "invalid_scope" {
		t.Fatalf("bad scope: %s %v", resp.Status, m)
	}
}

func TestAppPasskeyLoginNegative(t *testing.T) {
	e := passkeyEnv(t, fastLogin, withAndroidApp)
	u, a := enrolPasskey(t, e, "anna")
	a.Origin = appOrigin
	var first map[string]any
	refused := func(what string, resp *http.Response, m map[string]any) {
		t.Helper()
		if resp.StatusCode != 401 || m["error"] != "invalid_credentials" || m["access_token"] != nil {
			t.Errorf("%s: %s %v", what, resp.Status, m)
			return
		}
		if first == nil {
			first = m
		} else if !jsonEqual(first, m) {
			t.Errorf("%s: the answer differs (%v / %v)", what, first, m)
		}
	}
	ip := 20
	next := func() string { ip++; return "192.0.2." + itoa(ip) } // each case from its own network (throttle)

	// Another app (other signing key) and a phishing site.
	other := *a
	sum := sha256.Sum256([]byte("someone else's certificate"))
	other.Origin = "android:apk-key-hash:" + base64.RawURLEncoding.EncodeToString(sum[:])
	r, m := appLogin(t, e, &other, next(), nil)
	refused("foreign app origin", r, m)
	phish := *a
	phish.Origin = "https://evil.example"
	r, m = appLogin(t, e, &phish, next(), nil)
	refused("phishing origin", r, m)
	wrongRP := *a
	wrongRP.RPID = "evil.example"
	r, m = appLogin(t, e, &wrongRP, next(), nil)
	refused("wrong RP ID", r, m)
	// Unknown credential with anna's user handle (foreign user / stranger).
	stranger := passkeytest.New(appOrigin, pkRPID)
	stranger.UserHandle = a.UserHandle
	r, m = appLogin(t, e, stranger, next(), nil)
	refused("unknown credential", r, m)
	// No user verification.
	noUV := *a
	noUV.UV = false
	r, m = appLogin(t, e, &noUV, next(), nil)
	refused("UV=0", r, m)

	// Old (expired) and reused challenges.
	n := e.native(next())
	_, bm := n.do("POST", "/api/device/passkey/begin", nil)
	body := map[string]any{"challengeId": bm["challengeId"], "credential": json.RawMessage(a.Get(js(t, bm["publicKey"])))}
	e.svc.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	r, m = n.do("POST", "/api/device/passkey/login", body)
	e.svc.Now = time.Now
	refused("expired challenge", r, m)
	n = e.native(next())
	_, bm = n.do("POST", "/api/device/passkey/begin", nil)
	body = map[string]any{"challengeId": bm["challengeId"], "credential": json.RawMessage(a.Get(js(t, bm["publicKey"])))}
	if r, m := n.do("POST", "/api/device/passkey/login", body); r.StatusCode != 200 {
		t.Fatalf("valid: %s %v", r.Status, m)
	}
	r, m = e.native(next()).do("POST", "/api/device/passkey/login", body)
	refused("reused challenge", r, m)
	// Counter going backwards (cloned authenticator).
	clone := *a
	clone.SignCount, clone.CountStep = 0, 0
	r, m = appLogin(t, e, &clone, next(), nil)
	refused("counter backwards", r, m)

	// Ceremonies of the other purposes do not fit.
	x := e.browser()
	x.fetchCSRF()
	_, wm := x.do("POST", "/api/auth/passkeys/login/begin", nil)
	r, m = e.native(next()).do("POST", "/api/device/passkey/login", map[string]any{"challengeId": wm["challengeId"], "credential": json.RawMessage(a.Get(js(t, wm["publicKey"])))})
	refused("web login ceremony", r, m)
	n = e.native(next())
	_, bm = n.do("POST", "/api/device/passkey/begin", nil)
	webA := *a
	webA.Origin = pkOrigin
	r, m = x.do("POST", "/api/auth/passkeys/login/finish", map[string]any{"challengeId": bm["challengeId"], "credential": json.RawMessage(webA.Get(js(t, bm["publicKey"])))})
	if r.StatusCode != 401 {
		t.Errorf("app ceremony at the web login: %s %v", r.Status, m)
	}
	// The app origin never signs in to the web.
	x2 := e.browser()
	x2.fetchCSRF()
	_, wm = x2.do("POST", "/api/auth/passkeys/login/begin", nil)
	r, m = x2.do("POST", "/api/auth/passkeys/login/finish", map[string]any{"challengeId": wm["challengeId"], "credential": json.RawMessage(a.Get(js(t, wm["publicKey"])))})
	if r.StatusCode != 401 {
		t.Errorf("app origin at the web login: %s %v", r.Status, m)
	}

	// Disabled user: the same answer.
	if err := e.svc.SetDisabled(context.Background(), u.ID, true); err != nil {
		t.Fatal(err)
	}
	r, m = appLogin(t, e, a, next(), nil)
	refused("disabled user", r, m)
	e.svc.SetDisabled(context.Background(), u.ID, false)
	assertAudit(t, e, "login", "anna", false)

	// Repeated failures lock the network like the web passkey login.
	attacker := "198.51.100.9"
	for range 5 {
		appLogin(t, e, stranger, attacker, nil)
	}
	if r, m := appLogin(t, e, a, attacker, nil); r.StatusCode != 429 || m["error"] != "throttled" {
		t.Errorf("locked network: %s %v", r.Status, m)
	}
	// Malformed bodies are client errors.
	n = e.native(next())
	_, bm = n.do("POST", "/api/device/passkey/begin", nil)
	for _, bad := range []any{map[string]any{"challengeId": bm["challengeId"]}, map[string]any{"challengeId": 5}, "text"} {
		if r, _ := n.do("POST", "/api/device/passkey/login", bad); r.StatusCode < 400 || r.StatusCode >= 500 {
			t.Errorf("malformed %v: %s", bad, r.Status)
		}
	}
}

func TestAppPasskeyLoginWithoutConfiguredApp(t *testing.T) {
	e := passkeyEnv(t, fastLogin)
	_, a := enrolPasskey(t, e, "anna")
	if _, m := e.browser().do("GET", "/api/auth/config", nil); m["passkeyAppLogin"] != false {
		t.Fatalf("config: %v", m)
	}
	// Without a configured app the endpoints do not exist, for any origin.
	for _, o := range []string{appOrigin, pkOrigin} {
		a.Origin = o
		if r, m := appLogin(t, e, a, "192.0.2.50", nil); r.StatusCode != 404 || m["access_token"] != nil {
			t.Fatalf("%s without app: %s %v", o, r.Status, m)
		}
	}
	if r, m := e.native("").do("POST", "/api/device/passkey/login", map[string]any{"challengeId": "x"}); r.StatusCode != 404 {
		t.Fatalf("login without app: %s %v", r.Status, m)
	}
	if r, _ := e.browser().do("GET", "/.well-known/assetlinks.json", nil); r.StatusCode != 404 {
		t.Fatalf("assetlinks without apps: %s", r.Status)
	}
}

func TestAppPasskeyRateLimitAndDisabled(t *testing.T) {
	e := passkeyEnv(t, withAndroidApp) // login_rate_per_minute 10
	n := e.native("192.0.2.77")
	limited := false
	for range 30 {
		if r, m := n.do("POST", "/api/device/passkey/begin", nil); r.StatusCode == 429 && m["error"] == "rate_limited" {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("begin is not rate limited")
	}
	off := passkeyEnv(t, withAndroidApp, func(c *config.Config) { f := false; c.Auth.Passkeys.Enabled = &f })
	if r, m := off.native("").do("POST", "/api/device/passkey/begin", nil); r.StatusCode != 404 || m["error"] != "passkeys_disabled" {
		t.Fatalf("disabled: %s %v", r.Status, m)
	}
	if r, _ := off.browser().do("GET", "/.well-known/assetlinks.json", nil); r.StatusCode != 404 {
		t.Fatalf("assetlinks with passkeys off: %s", r.Status)
	}
}

func TestAssetLinks(t *testing.T) {
	e := passkeyEnv(t, withAndroidApp)
	req, _ := http.NewRequest("GET", e.ts.URL+"/.well-known/assetlinks.json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%s %s %s", resp.Status, resp.Header.Get("Content-Type"), raw)
	}
	var st []config.AssetLinkStatement
	if err := json.Unmarshal(raw, &st); err != nil || len(st) != 1 {
		t.Fatalf("%v %s", err, raw)
	}
	if st[0].Target.Namespace != "android_app" || st[0].Target.PackageName != "org.tuxmail.app" ||
		len(st[0].Target.Fingerprints) != 1 || st[0].Target.Fingerprints[0] != appFingerHex ||
		len(st[0].Relation) != 1 || st[0].Relation[0] != "delegate_permission/common.get_login_creds" {
		t.Fatalf("statement: %s", raw)
	}
}

func assertAudit(t *testing.T, e *env, action, target string, success bool) {
	t.Helper()
	list, err := e.svc.ListAudit(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Action == action && x.Target == target && x.Success == success {
			return
		}
	}
	t.Errorf("no audit entry %s %s success=%v", action, target, success)
}

func assertAuditDetails(t *testing.T, e *env, action, target, details string) {
	t.Helper()
	list, err := e.svc.ListAudit(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Action == action && x.Target == target && strings.Contains(x.Details, details) {
			return
		}
	}
	t.Errorf("no audit entry %s %s %q", action, target, details)
}
