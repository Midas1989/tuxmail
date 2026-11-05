// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// NA-06 (PoC of the audit): ten networks x 5 wrong passwords used to lock the
// account for the owner too. Now the owner keeps logging in from the networks
// he used before, while the attacker's networks get nothing verified.
func TestReauditNA06DistributedLockoutOverHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.svc.PressureDelay = 0
	e.user("victim", auth.RoleUser)
	from := func(ip, password string) (*http.Response, map[string]any) {
		b := e.browser()
		b.header = http.Header{"X-Forwarded-For": {ip}}
		b.fetchCSRF()
		return b.do("POST", "/api/auth/login", map[string]string{"username": "victim", "password": password})
	}
	if resp, m := from("198.51.100.7", pw); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	for n := 1; n <= 10; n++ {
		for range 5 {
			if resp, _ := from(fmt.Sprintf("203.0.113.%d", n), "wrong password!"); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusTooManyRequests {
				t.Fatal(resp.Status)
			}
		}
	}
	if resp, m := from("198.51.100.7", pw); resp.StatusCode != 200 || m["status"] != "ok" {
		t.Fatalf("NA-06: owner locked out by ten attacker networks: %s %v", resp.Status, m)
	}
	// An attacker network that knows the password gets no verification ...
	resp, m := from("203.0.113.200", pw)
	if resp.StatusCode != http.StatusTooManyRequests || m["error"] != "throttled" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("unknown network during the attack: %s %v", resp.Status, m)
	}
}
