package server

import (
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// M-2: privileged administrator actions need a fresh step-up; resetting the own
// password or second factor is refused outright.
func TestAdminActionsNeedStepUp(t *testing.T) {
	e := newEnv(t, Options{})
	root := e.user("root", auth.RoleAdmin)
	vic := e.user("victim", auth.RoleUser)
	adm2 := e.user("second", auth.RoleAdmin)
	admin := e.browser()
	admin.login("root")
	re := map[string]any{"reauth": map[string]string{"password": pw}}
	base := "/api/admin/users/" + itoa(int(vic.ID))

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/admin/users", map[string]any{"username": "neu", "role": "user"}},
		{"POST", base + "/reset-password", nil},
		{"POST", base + "/reset-2fa", nil},
		{"PATCH", base, map[string]any{"role": "admin"}},
		{"DELETE", "/api/admin/users/" + itoa(int(adm2.ID)), nil},
	} {
		resp, m := admin.do(c.method, c.path, c.body)
		if resp.StatusCode != http.StatusForbidden || m["error"] != "reauth_required" {
			t.Errorf("%s %s without step-up: %d %v", c.method, c.path, resp.StatusCode, m)
		}
	}
	if resp, m := admin.do("PATCH", base, map[string]any{"displayName": "V"}); resp.StatusCode != http.StatusOK {
		t.Errorf("non-privileged edit needs no step-up: %d %v", resp.StatusCode, m)
	}
	if resp, m := admin.do("POST", base+"/reset-password", re); resp.StatusCode != http.StatusOK {
		t.Errorf("reset-password: %d %v", resp.StatusCode, m)
	}
	if resp, m := admin.do("POST", base+"/reset-2fa", re); resp.StatusCode != http.StatusNoContent {
		t.Errorf("reset-2fa: %d %v", resp.StatusCode, m)
	}
	if resp, m := admin.do("PATCH", base, map[string]any{"role": "admin", "reauth": map[string]string{"password": pw}}); resp.StatusCode != http.StatusOK {
		t.Errorf("promote: %d %v", resp.StatusCode, m)
	}
	if resp, m := admin.do("DELETE", "/api/admin/users/"+itoa(int(adm2.ID)), re); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete admin: %d %v", resp.StatusCode, m)
	}
	// Never the own account, even with proof.
	own := "/api/admin/users/" + itoa(int(root.ID))
	for _, p := range []string{own + "/reset-password", own + "/reset-2fa"} {
		if resp, m := admin.do("POST", p, re); resp.StatusCode != http.StatusForbidden || m["error"] != "forbidden" {
			t.Errorf("%s: %d %v", p, resp.StatusCode, m)
		}
	}
}
