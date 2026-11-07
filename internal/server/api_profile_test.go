// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func auditEntries(t *testing.T, e *env, action string) []auth.AuditEntry {
	t.Helper()
	list, err := e.svc.QueryAudit(context.Background(), auth.AuditFilter{Limit: 50, Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestUpdateMeChangesOwnDisplayNameAndAudits(t *testing.T) {
	e := newEnv(t, Options{})
	me := e.user("dora", auth.RoleUser)
	other := e.user("erik", auth.RoleUser)
	b := e.browser()
	b.login("dora")

	resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "  Dora Bauer  "})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if got := m["user"].(map[string]any)["displayName"]; got != "Dora Bauer" {
		t.Fatalf("response name %v", got)
	}
	if _, m = b.do("GET", "/api/auth/me", nil); m["user"].(map[string]any)["displayName"] != "Dora Bauer" {
		t.Fatalf("name not stored: %v", m)
	}
	if u, _ := e.svc.GetUser(context.Background(), other.ID); u.DisplayName != "" {
		t.Fatalf("other user changed: %q", u.DisplayName)
	}

	list := auditEntries(t, e, "user.update")
	if len(list) != 1 {
		t.Fatalf("audit entries: %+v", list)
	}
	a := list[0]
	if a.ActorID != me.ID || a.Target != "dora" || !a.Success {
		t.Fatalf("audit entry: %+v", a)
	}
	if strings.Contains(a.Details, "Bauer") {
		t.Fatalf("audit entry contains the value: %+v", a)
	}
}

func TestUpdateMeSameNameWritesNothing(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("dora", auth.RoleUser)
	b := e.browser()
	b.login("dora")
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "Dora"}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if _, err := e.svc.DB().Exec(`UPDATE users SET updated_at = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": " Dora "})
	if resp.StatusCode != 200 || m["user"].(map[string]any)["displayName"] != "Dora" {
		t.Fatal(resp.Status, m)
	}
	var updated int64
	if err := e.svc.DB().QueryRow(`SELECT updated_at FROM users WHERE id = ?`, u.ID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatalf("row written: updated_at = %d", updated)
	}
	if list := auditEntries(t, e, "user.update"); len(list) != 1 {
		t.Fatalf("audit entries: %+v", list)
	}
}

func TestUpdateMeNeedsSession(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("dora", auth.RoleUser)
	b := e.browser()
	b.fetchCSRF() // CSRF passes, the missing session is what fails
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "X"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status, m)
	}
}

func TestUpdateMeNeedsCSRF(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("dora", auth.RoleUser)
	b := e.browser()
	b.login("dora")
	token := b.csrf

	b.csrf = ""
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "No Token"}); resp.StatusCode != http.StatusForbidden || m["error"] != "csrf" {
		t.Fatal(resp.Status, m)
	}
	b.csrf = token
	b.header = http.Header{"Origin": {"https://evil.example"}}
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "Foreign"}); resp.StatusCode != http.StatusForbidden || m["error"] != "csrf" {
		t.Fatal(resp.Status, m)
	}
	if got, _ := e.svc.GetUser(context.Background(), u.ID); got.DisplayName != "" {
		t.Fatalf("name changed without CSRF: %q", got.DisplayName)
	}
}

func TestUpdateMeRejectsInvalidNames(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("dora", auth.RoleUser)
	b := e.browser()
	b.login("dora")
	if _, err := e.svc.UpdateOwnProfile(context.Background(), u.ID, "Dora"); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]map[string]any{
		"missing":          {},
		"empty":            {"displayName": ""},
		"blank":            {"displayName": "   "},
		"too long":         {"displayName": strings.Repeat("ä", auth.MaxDisplayNameLength+1)},
		"newline":          {"displayName": "Dora\nBcc: x@example.de"},
		"control":          {"displayName": "Do\x00ra"},
		"separator":        {"displayName": "Do\u2028ra"},
		"override":         {"displayName": "Do\u202Era"},
		"zero width space": {"displayName": "Do\u200Bra"},
		"soft hyphen":      {"displayName": "Do\u00ADra"},
		"word joiner":      {"displayName": "Do\u2060ra"},
		"hangul filler":    {"displayName": "Do\u3164ra"},
		"tag character":    {"displayName": "Do\U000E0041ra"},
		"only U+200B":      {"displayName": "\u200B"},
		"only joiners":     {"displayName": "\u200C\u200D"},
		"only selector":    {"displayName": "\uFE0F"},
		"only blank":       {"displayName": "\u2800"},
		"only mark":        {"displayName": "\u0301"},
		"wrong type":       {"displayName": 5},
	} {
		if resp, m := b.do("PATCH", "/api/auth/me", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s %v", name, resp.Status, m)
		}
	}
	if got, _ := e.svc.GetUser(context.Background(), u.ID); got.DisplayName != "Dora" {
		t.Fatalf("name changed by a rejected request: %q", got.DisplayName)
	}
	// The limit counts characters, not bytes; names of real people pass.
	for _, ok := range []string{strings.Repeat("ä", auth.MaxDisplayNameLength), "José O'Neill-Müller", "山田 太郎", "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645", "Anna \U0001F469\u200D\U0001F4BB", "\u2764\uFE0F"} {
		if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": ok}); resp.StatusCode != 200 {
			t.Errorf("%q: %s %v", ok, resp.Status, m)
		}
	}
}

// Fields beyond the display name have no effect, and no field of the request
// selects the user: a normal user can neither gain rights nor edit a colleague
// or take over a login name.
func TestUpdateMeIgnoresOtherFieldsAndUsers(t *testing.T) {
	e := newEnv(t, Options{})
	me := e.user("dora", auth.RoleUser)
	other := e.user("erik", auth.RoleUser)
	b := e.browser()
	b.login("dora")

	resp, m := b.do("PATCH", "/api/auth/me", map[string]any{
		"displayName": "Dora",
		"id":          other.ID,
		"userId":      other.ID,
		"username":    "root",
		"email":       "victim@example.de",
		"role":        "admin",
		"isAdmin":     true,
		"disabled":    true,
		"oidc":        true,
	})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	u, err := e.svc.GetUser(context.Background(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "Dora" || u.Role != auth.RoleUser || u.IsAdmin() || u.Disabled || u.OIDC || u.Username != "dora" || u.Email != "dora@example.de" {
		t.Fatalf("own account: %+v", u)
	}
	if resp, m := b.do("GET", "/api/admin/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no admin rights expected: %s %v", resp.Status, m)
	}
	o, err := e.svc.GetUser(context.Background(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.DisplayName != "" || o.Disabled || o.Email != "erik@example.de" {
		t.Fatalf("other account changed: %+v", o)
	}
}

// Accounts of an identity provider: its name returns with every OIDC login, so
// the server does not pretend the change would last.
func TestUpdateMeRefusedForOIDCAccount(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("sso", auth.RoleUser)
	b := e.browser()
	b.login("sso")
	if _, err := e.svc.DB().Exec(`UPDATE users SET oidc_issuer = 'https://idp.example', oidc_subject = 'sub-1' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "Mine"}); resp.StatusCode != http.StatusConflict || m["error"] != "managed_by_idp" {
		t.Fatal(resp.Status, m)
	}
	if got, _ := e.svc.GetUser(context.Background(), u.ID); got.DisplayName != "" {
		t.Fatalf("name changed: %q", got.DisplayName)
	}
}

// Like the other account routes: a device token of the native app works
// without CSRF token, and a read-only account can still edit its own name.
func TestUpdateMeBearerAndReadonly(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("guest", auth.RoleReadonly)
	res, err := e.svc.Login(context.Background(), auth.LoginRequest{Login: "guest", Password: pw, Kind: auth.KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.bearer = res.Token
	if resp, m := b.do("PATCH", "/api/auth/me", map[string]any{"displayName": "Gast"}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if got, _ := e.svc.GetUser(context.Background(), u.ID); got.DisplayName != "Gast" {
		t.Fatalf("name: %q", got.DisplayName)
	}
}
