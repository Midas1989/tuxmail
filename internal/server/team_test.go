// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/server/push"
	"github.com/Midas1989/Mailclient/internal/server/push/fcm"
	"github.com/Midas1989/Mailclient/internal/team"
)

type recFCM struct {
	mu   sync.Mutex
	msgs []fcm.Message
}

func (f *recFCM) Send(_ context.Context, m fcm.Message) error {
	f.mu.Lock()
	f.msgs = append(f.msgs, m)
	f.mu.Unlock()
	return nil
}

func (f *recFCM) wait(t *testing.T, pred func(push.Payload) bool) push.Payload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, m := range f.msgs {
			var p push.Payload
			_ = json.Unmarshal(m.Payload, &p)
			if pred(p) {
				f.mu.Unlock()
				return p
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("notification not sent")
	return push.Payload{}
}

type teamEnv struct {
	e     *env
	fcm   *recFCM
	id    int64
	d     demo.Result
	b     map[string]*browser
	users map[string]*auth.User
}

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	f := &recFCM{}
	noSync := func(o *app.Options) { o.Sync = nil }
	e := newEnv(t, Options{FCM: f, AppOptions: func(_ int64, o *app.Options) { noSync(o) },
		TeamAppOptions: func(_ int64, o *app.Options) { noSync(o) }})
	t.Cleanup(e.srv.stopTeam)
	te := &teamEnv{e: e, fcm: f, b: map[string]*browser{}, users: map[string]*auth.User{}}
	for _, n := range []string{"eve", "anna", "ben", "dora"} {
		role := auth.RoleUser
		if n == "eve" {
			role = auth.RoleAdmin
		}
		te.users[n] = e.user(n, role)
		b := e.browser()
		b.login(n)
		te.b[n] = b
	}
	resp, m := te.b["eve"].do("POST", "/api/admin/team/mailboxes", map[string]any{"mailbox": map[string]any{"name": "Support", "address": "support@example.de"}})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	te.id = int64(m["mailbox"].(map[string]any)["id"].(float64))
	ta, err := e.srv.TeamApp(context.Background(), te.id)
	if err != nil {
		t.Fatal(err)
	}
	if te.d, err = demo.Seed(context.Background(), ta.Store(), ta.Blobs(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for n, r := range map[string]string{"anna": "reply", "ben": "read"} {
		if resp, m := te.b["eve"].do("PUT", te.path("/grants"), map[string]any{"userId": te.users[n].ID, "role": r}); resp.StatusCode != 200 {
			t.Fatal(resp.Status, m)
		}
	}
	return te
}

func (te *teamEnv) path(p string) string { return fmt.Sprintf("/api/team/%d%s", te.id, p) }

func TestTeamAuthorizationHTTP(t *testing.T) {
	te := newTeamEnv(t)
	conv := te.d.Threads["relaunch"]
	type req struct {
		user, method, path string
		body               any
		want               int
	}
	cases := []req{
		// Reading (read role and above; no access = 404).
		{"ben", "GET", "/accounts", nil, 200},
		{"dora", "GET", "/accounts", nil, 404},
		{"dora", "GET", "/conversations", nil, 404},
		{"ben", "GET", fmt.Sprintf("/conversations/%d", conv), nil, 200},
		{"ben", "GET", fmt.Sprintf("/conversations/%d/items", conv), nil, 200},
		{"ben", "GET", fmt.Sprintf("/conversations/%d/comments", conv), nil, 200},
		{"ben", "GET", "/members", nil, 200},
		{"ben", "GET", "/stats", nil, 200},
		// Acting needs reply.
		{"ben", "POST", fmt.Sprintf("/conversations/%d/comments", conv), map[string]string{"body": "x"}, 403},
		{"ben", "PATCH", fmt.Sprintf("/conversations/%d/assignment", conv), map[string]any{"assigneeId": te.users["ben"].ID}, 403},
		{"ben", "POST", "/messages/flags", map[string]any{"ids": []int64{te.d.Messages["offer1"]}, "add": []string{"\\Flagged"}}, 403},
		{"ben", "POST", "/send", map[string]any{"channel": "email", "to": []any{}}, 403},
		{"anna", "POST", "/messages/flags", map[string]any{"ids": []int64{te.d.Messages["offer1"]}, "add": []string{"\\Flagged"}}, 204},
		// Managing needs manage.
		{"anna", "PUT", "/snippets/new", map[string]any{"trigger": "/danke", "title": "Danke", "text": "Vielen Dank!"}, 403},
		{"anna", "GET", "/grants", nil, 403},
		{"eve", "PUT", "/snippets/new", map[string]any{"trigger": "/danke", "title": "Danke", "text": "Vielen Dank!"}, 200},
		{"ben", "GET", "/snippets", nil, 200},
	}
	for _, c := range cases {
		resp, m := te.b[c.user].do(c.method, te.path(c.path), c.body)
		if resp.StatusCode != c.want {
			t.Errorf("%s %s %s: %s, want %d (%v)", c.user, c.method, c.path, resp.Status, c.want, m)
		}
	}
	// Mailbox list per user.
	var acc []team.Access
	getJSON(t, te.b["ben"], "/api/team/mailboxes", &acc)
	if len(acc) != 1 || acc[0].Role != team.RoleRead {
		t.Fatal(acc)
	}
	getJSON(t, te.b["dora"], "/api/team/mailboxes", &acc)
	if len(acc) != 0 {
		t.Fatal("dora sees", acc)
	}
	// The personal API is unaffected: anna's own store is empty.
	var own []app.Account
	getJSON(t, te.b["anna"], "/api/accounts", &own)
	var teamAccs []app.Account
	getJSON(t, te.b["anna"], te.path("/accounts"), &teamAccs)
	if len(own) != 0 || len(teamAccs) == 0 {
		t.Fatal("team data leaked into or missing from the views", len(own), len(teamAccs))
	}
}

func TestTeamCollaborationFlow(t *testing.T) {
	te := newTeamEnv(t)
	ctx := context.Background()
	conv := te.d.Threads["relaunch"]
	// ben registers an FCM device.
	if resp, m := te.b["ben"].do("POST", "/api/push/subscribe", map[string]any{"type": "fcm", "token": "fcm-ben-0123456789abcdefghij",
		"deviceName": "Pixel", "appId": "org.tuxmail.app"}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	subs, _ := te.e.srv.pushStore.List(ctx, te.users["ben"].ID)
	if len(subs) != 1 || subs[0].Kind != push.KindFCM {
		t.Fatal(subs)
	}

	// List with team state; unassigned view contains the conversation.
	var page app.Page[app.TeamConversation]
	if code := getJSON(t, te.b["anna"], te.path("/conversations?scope=all&view=unassigned&limit=200"), &page); code != 200 || page.Total == 0 {
		t.Fatal(code, page.Total)
	}
	found := false
	for _, c := range page.Items {
		found = found || c.ID == conv
		if c.Team.Status != team.StatusOpen {
			t.Fatal("unassigned view with state", c.Team)
		}
	}
	if !found {
		t.Fatal("conversation missing from unassigned view")
	}

	// anna takes it ("Mir zuweisen").
	resp, m := te.b["anna"].do("PATCH", te.path(fmt.Sprintf("/conversations/%d/assignment", conv)), map[string]any{"assigneeId": te.users["anna"].ID})
	if resp.StatusCode != 200 || m["status"] != "in_progress" {
		t.Fatal(resp.Status, m)
	}
	getJSON(t, te.b["anna"], te.path("/conversations?view=mine"), &page)
	if page.Total != 1 || page.Items[0].ID != conv || page.Items[0].Team.AssigneeID != te.users["anna"].ID {
		t.Fatal("mine view", page)
	}
	getJSON(t, te.b["ben"], te.path("/conversations?view=mine"), &page)
	if page.Total != 0 {
		t.Fatal("ben's mine view", page.Total)
	}

	// Comment with @mention -> push to ben (FCM) with the team id.
	resp, m = te.b["anna"].do("POST", te.path(fmt.Sprintf("/conversations/%d/comments", conv)), map[string]string{"body": "@ben kannst du das Angebot prüfen?"})
	if resp.StatusCode != 201 {
		t.Fatal(resp.Status, m)
	}
	p := te.fcm.wait(t, func(p push.Payload) bool { return p.Type == "team" })
	if p.TeamID != fmt.Sprint(te.id) || p.ConversationID != fmt.Sprint(conv) || !strings.Contains(p.Body, "Angebot") {
		t.Fatalf("%+v", p)
	}
	var ms []team.Mention
	getJSON(t, te.b["ben"], "/api/team/mentions?unread=1", &ms)
	if len(ms) != 1 {
		t.Fatal(ms)
	}

	// Presence: anna is replying; ben sees it (and reading marks the mention read).
	if resp, _ := te.b["anna"].do("POST", te.path(fmt.Sprintf("/conversations/%d/presence", conv)), map[string]string{"state": "replying"}); resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if resp, _ := te.b["ben"].do("POST", te.path(fmt.Sprintf("/conversations/%d/presence", conv)), map[string]string{"state": "replying"}); resp.StatusCode != 403 {
		t.Fatal("reader may not claim replying:", resp.Status)
	}
	var det app.TeamConversationDetail
	getJSON(t, te.b["ben"], te.path(fmt.Sprintf("/conversations/%d", conv)), &det)
	if len(det.Presence) != 1 || det.Presence[0].State != "replying" || det.Role != team.RoleRead || det.Team.Comments != 1 {
		t.Fatalf("%+v", det)
	}
	getJSON(t, te.b["ben"], "/api/team/mentions?unread=1", &ms)
	if len(ms) != 0 {
		t.Fatal("mention not marked read after opening")
	}

	// Per-user team drafts: anna's draft is a lock hint for ben.
	if resp, _ := te.b["anna"].do("PUT", te.path(fmt.Sprintf("/conversations/%d/draft", conv)), map[string]string{"text": "Hallo Herr Weber"}); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	var ds []team.Draft
	getJSON(t, te.b["ben"], te.path(fmt.Sprintf("/conversations/%d/drafts", conv)), &ds)
	if len(ds) != 1 || ds[0].Body != nil {
		t.Fatal(ds)
	}
	var own app.ChatDraft
	getJSON(t, te.b["anna"], te.path(fmt.Sprintf("/conversations/%d/draft", conv)), &own)
	if own.Text != "Hallo Herr Weber" {
		t.Fatal(own)
	}

	// Messages of the team store link attachments under the team prefix.
	var msg map[string]any
	getJSON(t, te.b["ben"], te.path(fmt.Sprintf("/messages/%d", te.d.Messages["offer1"])), &msg)
	raw, _ := json.Marshal(msg)
	if strings.Contains(string(raw), `"/api/messages/`) {
		t.Fatalf("unrewritten URL in %s", raw)
	}

	// Audit log: read and assigned entries.
	entries, _ := te.e.svc.ListAudit(ctx, 200, 0)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	for _, a := range []string{"team.read", "team.assigned", "team.comment", "team.grant", "team.mailbox.create"} {
		if !seen[a] {
			t.Errorf("audit entry %s missing", a)
		}
	}

	// Deleting the mailbox stops it and removes access.
	if resp, _ := te.b["eve"].do("DELETE", fmt.Sprintf("/api/admin/team/mailboxes/%d", te.id), nil); resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	if resp, _ := te.b["anna"].do("GET", te.path("/accounts"), nil); resp.StatusCode != 404 {
		t.Fatal("deleted mailbox still readable", resp.Status)
	}
}
