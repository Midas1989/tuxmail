// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Server side of the administration page in the web UI (Settings ->
// Administration): lock state in the user DTO, unlock, audit filters.

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func adminUsers(t *testing.T, b *browser) map[string]map[string]any {
	t.Helper()
	resp, m := b.do("GET", "/api/admin/users", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	out := map[string]map[string]any{}
	for _, u := range m["users"].([]any) {
		um := u.(map[string]any)
		out[um["username"].(string)] = um
	}
	return out
}

func TestAdminUserListShowsLockStateAndUnlock(t *testing.T) {
	e := newEnv(t, Options{})
	e.svc.PressureDelay = 0
	e.user("root", auth.RoleAdmin)
	victim := e.user("victim", auth.RoleUser)
	e.user("quiet", auth.RoleUser)

	admin := e.browser()
	admin.login("root")

	for name, u := range adminUsers(t, admin) {
		if u["locked"] != false || u["underPressure"] != false {
			t.Fatalf("%s: fresh account reported as locked: %v", name, u)
		}
		if _, ok := u["lockedUntil"]; ok {
			t.Fatalf("%s: lockedUntil must be omitted while not locked: %v", name, u)
		}
	}

	// Five wrong passwords from one network lock that network for the victim.
	from := func(ip string) *http.Response {
		b := e.browser()
		b.header = http.Header{"X-Forwarded-For": {ip}}
		b.fetchCSRF()
		resp, _ := b.do("POST", "/api/auth/login", map[string]string{"username": "victim", "password": "wrong password!"})
		return resp
	}
	for range 5 {
		from("203.0.113.5")
	}
	us := adminUsers(t, admin)
	v := us["victim"]
	if v["locked"] != true || v["lockedUntil"] == nil || v["underPressure"] != false {
		t.Fatalf("victim must be reported locked: %v", v)
	}
	if us["quiet"]["locked"] != false || us["root"]["locked"] != false {
		t.Fatalf("other accounts must not be locked: %v", us)
	}
	// The detail and the update answer carry the same flags.
	path := fmt.Sprintf("/api/admin/users/%d", victim.ID)
	if _, m := admin.do("GET", path, nil); m["user"].(map[string]any)["locked"] != true {
		t.Fatalf("GET user: %v", m)
	}
	if _, m := admin.do("PATCH", path, map[string]any{"displayName": "Vic"}); m["user"].(map[string]any)["locked"] != true || m["user"].(map[string]any)["displayName"] != "Vic" {
		t.Fatalf("PATCH user: %v", m)
	}

	// Unlock clears it again.
	if resp, _ := admin.do("POST", path+"/unlock", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
	if v := adminUsers(t, admin)["victim"]; v["locked"] != false || v["underPressure"] != false {
		t.Fatalf("unlock did not clear the lock: %v", v)
	}

	// Distributed guessing: the soft signal is reported as underPressure.
	for n := 1; n <= 11; n++ {
		for range 5 {
			from(fmt.Sprintf("203.0.114.%d", n))
		}
	}
	if v := adminUsers(t, admin)["victim"]; v["underPressure"] != true || v["pressureUntil"] == nil {
		t.Fatalf("victim must be reported under pressure: %v", v)
	}
	admin.do("POST", path+"/unlock", nil)
	if v := adminUsers(t, admin)["victim"]; v["underPressure"] != false {
		t.Fatalf("unlock did not clear the pressure: %v", v)
	}
}

func TestAdminCreateUserReturnsLockFieldsAndPasswordOnce(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("root", auth.RoleAdmin)
	admin := e.browser()
	admin.login("root")
	resp, m := admin.do("POST", "/api/admin/users", map[string]any{"username": "neu", "email": "neu@example.de", "role": "readonly"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, m)
	}
	pw, _ := m["password"].(string)
	u := m["user"].(map[string]any)
	if len(pw) < 20 || u["locked"] != false || u["role"] != "readonly" {
		t.Fatalf("create: %v", m)
	}
	// The generated password works and is never returned again.
	nb := e.browser()
	nb.fetchCSRF()
	if resp, m := nb.do("POST", "/api/auth/login", map[string]string{"username": "neu", "password": pw}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if _, m := admin.do("GET", fmt.Sprintf("/api/admin/users/%d", int64(u["id"].(float64))), nil); m["password"] != nil {
		t.Fatalf("password leaked: %v", m)
	}
}

func TestAdminEndpointsNeedAdminRole(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("root", auth.RoleAdmin)
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	for _, p := range []string{"/api/admin/users", "/api/admin/invites", "/api/admin/groups", "/api/admin/audit"} {
		if resp, _ := b.do("GET", p, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %s", p, resp.Status)
		}
	}
	// /api/auth/me tells the UI the role without calling an admin route.
	if _, m := b.do("GET", "/api/auth/me", nil); m["user"].(map[string]any)["role"] != "user" {
		t.Fatalf("me: %v", m)
	}
}

func TestAdminAuditFilterAndPaging(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("root", auth.RoleAdmin)
	e.user("anna", auth.RoleUser)
	admin := e.browser()
	admin.login("root")
	// A failed login of someone else plus admin actions.
	other := e.browser()
	other.fetchCSRF()
	other.do("POST", "/api/auth/login", map[string]string{"username": "anna", "password": "wrong password!"})
	admin.do("POST", "/api/admin/groups", map[string]string{"name": "g1"})
	admin.do("POST", "/api/admin/groups", map[string]string{"name": "g2"})

	list := func(q url.Values) []any {
		t.Helper()
		resp, m := admin.do("GET", "/api/admin/audit?"+q.Encode(), nil)
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status, m)
		}
		return m["entries"].([]any)
	}
	if n := len(list(url.Values{"action": {"group."}})); n != 2 {
		t.Fatalf("action filter: %d", n)
	}
	failed := list(url.Values{"result": {"failed"}})
	if len(failed) != 1 || failed[0].(map[string]any)["action"] != "login" {
		t.Fatalf("result=failed: %v", failed)
	}
	if n := len(list(url.Values{"actor": {"root"}, "action": {"group.create"}})); n != 2 {
		t.Fatalf("actor filter: %d", n)
	}
	// Paging backwards by entry id.
	page1 := list(url.Values{"limit": {"2"}})
	if len(page1) != 2 {
		t.Fatalf("limit: %d", len(page1))
	}
	last := int64(page1[1].(map[string]any)["id"].(float64))
	page2 := list(url.Values{"limit": {"2"}, "before": {fmt.Sprint(last)}})
	if len(page2) == 0 || int64(page2[0].(map[string]any)["id"].(float64)) >= last {
		t.Fatalf("before: %v", page2)
	}
}
