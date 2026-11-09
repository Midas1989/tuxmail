// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"sync"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

// ai.max_level reaches the apps of users and of team mailboxes.
func TestAIMaxLevelForUsersAndTeams(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	e := newEnv(t, Options{
		AppOptions: func(_ int64, o *app.Options) { o.Sync = nil; mu.Lock(); got["user"] = o.AIMaxLevel; mu.Unlock() },
		TeamAppOptions: func(_ int64, o *app.Options) {
			o.Sync = nil
			mu.Lock()
			got["team"] = o.AIMaxLevel
			mu.Unlock()
		}}, func(c *config.Config) { c.AI.MaxLevel = "metadata" })
	t.Cleanup(e.srv.stopTeam)
	e.user("eve", auth.RoleAdmin)
	b := e.browser()
	b.login("eve")
	resp, m := b.do("POST", "/api/admin/team/mailboxes", map[string]any{"mailbox": map[string]any{"name": "Support", "address": "support@example.de"}})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	id := int64(m["mailbox"].(map[string]any)["id"].(float64))
	if _, err := e.srv.TeamApp(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	b.do("GET", "/api/ai/settings", nil)
	mu.Lock()
	defer mu.Unlock()
	if got["user"] != "metadata" || got["team"] != "metadata" {
		t.Fatalf("caps: %v", got)
	}
	if (&Server{cfg: &config.Config{}}).aiMaxLevel() != "" {
		t.Fatal("no cap by default")
	}
}
