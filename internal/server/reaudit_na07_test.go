// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

// NA-07: anonymous endpoints do not reveal the version unless
// security.expose_version is set; the product marker stays so that clients
// still recognise the server; authenticated users still see the version.
func TestReauditNA07VersionNotDisclosedToAnonymous(t *testing.T) {
	for _, expose := range []bool{false, true} {
		e := newEnv(t, Options{Version: "9.8.7-test"}, func(c *config.Config) { c.Security.ExposeVersion = expose })
		anon := e.browser()
		for _, path := range []string{"/healthz", "/api/auth/config"} {
			resp, m := anon.do("GET", path, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatal(path, resp.Status)
			}
			if m["product"] != "tuxmail" {
				t.Errorf("expose=%v %s: no product marker: %v", expose, path, m)
			}
			_, has := m["version"]
			if has != expose {
				t.Errorf("expose=%v %s: version present=%v: %v", expose, path, has, m)
			}
			if !expose {
				raw := resp.Header.Get("Server") + resp.Header.Get("X-Powered-By")
				if strings.Contains(raw, "9.8.7") {
					t.Errorf("%s: version in headers", path)
				}
			}
		}
		// Authenticated clients keep getting it (provisioning profile of a user session).
		e.user("anna", auth.RoleUser)
		b := e.browser()
		b.login("anna")
		if _, m := b.do("GET", "/api/provisioning/profile", nil); m["server"].(map[string]any)["version"] != "9.8.7-test" {
			t.Errorf("expose=%v: authenticated profile lost the version: %v", expose, m["server"])
		}
	}
}
