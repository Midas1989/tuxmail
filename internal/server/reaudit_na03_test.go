// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// NA-03: PUT /api/settings/{key} answers 413 quota_exceeded beyond 64 keys.
func TestReauditNA03SettingsQuotaOverHTTP(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("eve", auth.RoleUser)
	b := e.browser()
	b.login("eve")
	for i := 0; i < auth.MaxSettingKeys; i++ {
		if resp, m := b.do("PUT", fmt.Sprintf("/api/settings/k%d", i), map[string]int{"a": i}); resp.StatusCode != http.StatusNoContent {
			t.Fatal(i, resp.Status, m)
		}
	}
	resp, m := b.do("PUT", "/api/settings/overflow", map[string]int{"a": 1})
	if resp.StatusCode != http.StatusRequestEntityTooLarge || m["error"] != "quota_exceeded" || m["message"] == "" {
		t.Fatalf("settings flood not refused: %s %v", resp.Status, m)
	}
}
