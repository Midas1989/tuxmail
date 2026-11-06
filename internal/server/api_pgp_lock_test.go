// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// Logging out forgets the passphrase; responses never echo it.
func TestLogoutLocksOpenPGPKeys(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("pgpnutzer", auth.RoleUser)
	b := e.browser()
	b.login("pgpnutzer")
	const pass = "logout-passphrase-lang"
	resp, key := b.do("POST", "/api/comfort/pgp/keys/generate", map[string]string{"email": "p@example.org", "passphrase": pass})
	if resp.StatusCode != http.StatusOK || key["locked"] != true {
		t.Fatalf("generate: %s %v", resp.Status, key)
	}
	id := key["id"].(float64)
	url := "/api/comfort/pgp/keys/" + itoa(int(id))
	if resp, m := b.do("POST", url+"/unlock", map[string]string{"passphrase": "falsch"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong: %s %v", resp.Status, m)
	}
	resp, m := b.do("POST", url+"/unlock", map[string]string{"passphrase": pass})
	if resp.StatusCode != http.StatusOK || m["locked"] != false {
		t.Fatalf("unlock: %s %v", resp.Status, m)
	}
	if resp, _ := b.do("POST", "/api/comfort/pgp/lock", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lock: %s", resp.Status)
	}
	if resp, m := b.do("POST", url+"/unlock", map[string]string{"passphrase": pass}); resp.StatusCode != http.StatusOK || m["locked"] != false {
		t.Fatalf("unlock again: %s %v", resp.Status, m)
	}
	if resp, _ := b.do("POST", "/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %s", resp.Status)
	}
	userApp := e.srv.apps.m
	found := false
	e.srv.apps.mu.Lock()
	for _, entry := range userApp {
		found = true
		if entry.app.PGPUnlockedCount() != 0 {
			t.Error("passphrase survives the logout")
		}
	}
	e.srv.apps.mu.Unlock()
	if !found {
		t.Fatal("no App was open")
	}
}
