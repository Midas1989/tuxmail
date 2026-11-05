// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

// NA-02 (PoC of the audit): an admin whose account was linked to an OIDC
// identity is still forced to enrol TOTP when logging in with the password,
// and an OIDC-only account cannot give itself a local password from a
// stolen session.
func TestReauditNA02OIDCLinkedAdminAndFirstPassword(t *testing.T) {
	e := newEnv(t, Options{}, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	e.user("root", auth.RoleAdmin)
	db := e.svc.DB()

	admin := e.browser()
	admin.login("root")
	if resp, m := admin.do("GET", "/api/admin/users", nil); resp.StatusCode != http.StatusForbidden || m["error"] != "mfa_setup_required" {
		t.Fatalf("plain admin without TOTP: %s %v", resp.Status, m)
	}
	// What a verified-e-mail OIDC login does to the account.
	if _, err := db.Exec(`UPDATE users SET oidc_issuer = 'https://idp.example', oidc_subject = 'sub-1' WHERE username = 'root'`); err != nil {
		t.Fatal(err)
	}
	pwLogin := e.browser()
	pwLogin.login("root")
	if resp, m := pwLogin.do("GET", "/api/admin/users", nil); resp.StatusCode != http.StatusForbidden || m["error"] != "mfa_setup_required" {
		t.Fatalf("NA-02: password login of an OIDC-linked admin reaches /api/admin: %s %v", resp.Status, m)
	}
	if _, m := pwLogin.do("GET", "/api/auth/me", nil); m["mfaSetupRequired"] != true {
		t.Fatalf("me does not report the enrolment duty: %v", m)
	}
	// A session created by the OIDC callback is exempt: simulate it by
	// marking the session row (the callback itself is covered in internal/auth).
	if _, err := db.Exec(`UPDATE sessions SET login_method = 'oidc' WHERE id = (SELECT MAX(id) FROM sessions)`); err != nil {
		t.Fatal(err)
	}
	if resp, m := pwLogin.do("GET", "/api/admin/users", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("OIDC session of the linked admin lost access: %s %v", resp.Status, m)
	}

	// An OIDC-only account (no local password) with a stolen password session.
	e.user("sso", auth.RoleUser)
	victim := e.browser()
	victim.login("sso")
	if _, err := db.Exec(`UPDATE users SET password_hash = '', oidc_issuer = 'https://idp.example', oidc_subject = 'sub-2' WHERE username = 'sso'`); err != nil {
		t.Fatal(err)
	}
	resp, m := victim.do("POST", "/api/auth/password", map[string]string{"currentPassword": "", "newPassword": "a-brand-new-password-123"})
	if resp.StatusCode != http.StatusForbidden || m["error"] != "reauth_required" {
		t.Fatalf("NA-02: session without re-authentication set a local password: %s %v", resp.Status, m)
	}
	attempt := e.browser()
	attempt.fetchCSRF()
	if resp, _ := attempt.do("POST", "/api/auth/login", map[string]string{"username": "sso", "password": "a-brand-new-password-123"}); resp.StatusCode == http.StatusOK {
		t.Fatal("backdoor password login works")
	}
}
