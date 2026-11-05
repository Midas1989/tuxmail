// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/server/push"
)

// Re-audit 2026-11-05 (NH-03): a manager of a shared mailbox cannot install a
// forwarding workflow without explicit confirmation; the change is attributed
// (author), audited with its external target and the other managers are told.
func TestTeamWorkflowExternalEffectConfirmedAttributedAudited(t *testing.T) {
	te := newTeamEnv(t)
	ctx := context.Background()
	for n, r := range map[string]string{"anna": "manage", "dora": "manage"} {
		if resp, m := te.b["eve"].do("PUT", te.path("/grants"), map[string]any{"userId": te.users[n].ID, "role": r}); resp.StatusCode != 200 {
			t.Fatal(resp.Status, m)
		}
	}
	if resp, m := te.b["dora"].do("POST", "/api/push/subscribe", map[string]any{"type": "fcm", "token": "fcm-dora-0123456789abcdefghij",
		"deviceName": "Pixel", "appId": "org.tuxmail.app"}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	body := func(confirm bool) map[string]any {
		return map[string]any{"name": "Mirror", "enabled": true, "confirmExternal": confirm,
			"trigger": map[string]any{"event": "mail.received"},
			"actions": []any{map[string]any{"type": "forward", "params": map[string]any{"to": []any{"drop@evil.example"}}}}}
	}

	// Without the confirmation flag nothing is stored.
	resp, m := te.b["anna"].do("POST", te.path("/workflows"), body(false))
	if resp.StatusCode != 409 || !strings.Contains(strings.ToLower(fmt.Sprint(m)), "confirmation_required") {
		t.Fatalf("unconfirmed forwarding workflow: %s %v", resp.Status, m)
	}
	var list []map[string]any
	getJSON(t, te.b["anna"], te.path("/workflows"), &list)
	if len(list) != 0 {
		t.Fatalf("workflow stored without confirmation: %v", list)
	}

	// Confirmed: stored, attributed to anna.
	resp, m = te.b["anna"].do("POST", te.path("/workflows"), body(true))
	if resp.StatusCode != 201 || m["externalEffects"] != true {
		t.Fatalf("confirmed forwarding workflow: %s %v", resp.Status, m)
	}
	if m["author"] != te.users["anna"].Username {
		t.Fatalf("author = %v, want %q", m["author"], te.users["anna"].Username)
	}
	if m["confirmExternal"] != nil {
		t.Fatalf("input flag echoed: %v", m)
	}

	// Audit entry with actor, workflow id and the external target.
	entries, _ := te.e.svc.ListAudit(ctx, 50, 0)
	found := false
	for _, e := range entries {
		if e.Action == "team.workflow.save" {
			found = true
			if e.Actor != te.users["anna"].Username || !strings.Contains(e.Details, "workflow=mirror") || !strings.Contains(e.Details, "drop@evil.example") {
				t.Errorf("audit entry: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("no team.workflow.save audit entry")
	}

	// The other manager is told (push), the actor is not.
	p := te.fcm.wait(t, func(p push.Payload) bool { return p.Type == "team" && strings.Contains(p.Body, "drop@evil.example") })
	if p.TeamID == "" {
		t.Fatalf("%+v", p)
	}

	// Re-enabling after a disable needs the confirmation again; deleting is audited.
	if resp, m := te.b["anna"].do("PUT", te.path("/workflows/mirror/enabled"), map[string]any{"enabled": false}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if resp, m := te.b["anna"].do("PUT", te.path("/workflows/mirror/enabled"), map[string]any{"enabled": true}); resp.StatusCode != 409 {
		t.Fatalf("enabling without confirmation: %s %v", resp.Status, m)
	}
	if resp, m := te.b["anna"].do("PUT", te.path("/workflows/mirror/enabled"), map[string]any{"enabled": true, "confirmExternal": true}); resp.StatusCode != 200 {
		t.Fatalf("enabling with confirmation: %s %v", resp.Status, m)
	}
	if resp, _ := te.b["anna"].do("DELETE", te.path("/workflows/mirror"), nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	entries, _ = te.e.svc.ListAudit(ctx, 50, 0)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	for _, a := range []string{"team.workflow.save", "team.workflow.disable", "team.workflow.enable", "team.workflow.delete"} {
		if !seen[a] {
			t.Errorf("audit entry %s missing", a)
		}
	}

	// The personal API has the same rule (no implicit confirmation).
	if resp, m := te.b["anna"].do("POST", "/api/workflows", body(false)); resp.StatusCode != 409 {
		t.Fatalf("personal unconfirmed forwarding workflow: %s %v", resp.Status, m)
	}
	if resp, m := te.b["anna"].do("POST", "/api/workflows", body(true)); resp.StatusCode != 201 {
		t.Fatalf("personal confirmed forwarding workflow: %s %v", resp.Status, m)
	}
}
