// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/auth"
)

// appEnv is a server with a logged-in user whose store holds the demo data.
func appEnv(t *testing.T) (*env, *browser, *auth.User, demo.Result) {
	t.Helper()
	e := newEnv(t, Options{AppOptions: func(_ int64, o *app.Options) { o.Sync = nil }})
	u := e.user("anna", auth.RoleUser)
	a, err := e.srv.App(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := demo.Seed(context.Background(), a.Store(), a.Blobs(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.login("anna")
	return e, b, u, d
}

// documentedRoutes parses the REST route list at the top of http.ts.
func documentedRoutes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "lib", "api", "http.ts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	start := strings.Index(text, "REST routes")
	end := strings.Index(text, "Device flow")
	if start < 0 || end < start {
		t.Fatal("route list not found in http.ts")
	}
	re := regexp.MustCompile(`\b((?:GET|POST|PUT|PATCH|DELETE|WS)(?:/(?:GET|POST|PUT|PATCH|DELETE))*)\s+(/api/[^\s;,]*)`)
	var out []string
	for _, line := range strings.Split(text[start:end], "\n") {
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			path := m[2]
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			for _, method := range strings.Split(m[1], "/") {
				if method == "WS" {
					method = "GET"
				}
				p := path
				if base, ok := strings.CutSuffix(p, "[/{id}]"); ok {
					p = base // GET lists, the others address one entry
					if method != "GET" && method != "POST" {
						p = base + "/{id}"
					}
				}
				dir, last := p[:strings.LastIndexByte(p, '/')+1], p[strings.LastIndexByte(p, '/')+1:]
				for _, alt := range strings.Split(last, "|") {
					out = append(out, method+" "+dir+alt)
				}
			}
		}
	}
	// "…/items/{itemId}/translate" continues the AI line.
	out = append(out, "POST /api/ai/conversations/{id}/items/{itemId}/translate")
	return out
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestContractRoutesMounted checks that every route documented in http.ts
// is served by a handler (not the /api/ catch-all or the SPA).
func TestContractRoutesMounted(t *testing.T) {
	e := newEnv(t, Options{})
	routes := documentedRoutes(t)
	if len(routes) < 100 {
		t.Fatalf("parsed only %d routes from http.ts", len(routes))
	}
	for _, rt := range routes {
		method, path, _ := strings.Cut(rt, " ")
		req := httptest.NewRequest(method, pathParam.ReplaceAllString(path, "1"), nil)
		_, pattern := e.srv.mux.Handler(req)
		if pattern == "" || pattern == "/api/" || pattern == "/" {
			t.Errorf("%s is not mounted (matched %q)", rt, pattern)
		}
	}
}

func getJSON(t *testing.T, b *browser, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", b.e.ts.URL+path, nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v: %s", path, err, raw)
		}
	}
	return resp.StatusCode
}

func TestAPIHandlers(t *testing.T) {
	e, b, _, d := appEnv(t)
	var accs []app.Account
	if code := getJSON(t, b, "/api/accounts", &accs); code != 200 || len(accs) != 3 {
		t.Fatal(code, accs)
	}
	var page app.Page[app.MessageSummary]
	if code := getJSON(t, b, "/api/messages?scope=unified&role=inbox&offset=0&limit=50", &page); code != 200 || page.Total != 5 {
		t.Fatal(code, page.Total)
	}
	var convs app.Page[app.Conversation]
	if code := getJSON(t, b, "/api/conversations?scope=all&grouping=person&limit=50", &convs); code != 200 || convs.Total == 0 {
		t.Fatal(code, convs)
	}
	var items []app.ConversationItem
	path := fmt.Sprintf("/api/conversations/%d/items?scope=folder&folderId=%d", d.Threads["relaunch"], d.WorkInbox)
	if code := getJSON(t, b, path, &items); code != 200 || len(items) != 3 || items[0].InScope == nil {
		t.Fatal(code, items)
	}
	// Attachment download and error format.
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/messages/%d/attachments/0", e.ts.URL, d.Messages["invoice"]), nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(body), "%PDF") {
		t.Fatal(resp.Status, resp.Header)
	}
	r, m := b.do("GET", "/api/messages/9999", nil)
	if r.StatusCode != 404 || m["error"] != "not_found" {
		t.Fatal(r.Status, m)
	}
	r, m = b.do("POST", "/api/search", map[string]any{"text": "Angebot", "mode": "simple", "scopes": []string{}, "accountIds": []int{},
		"folderIds": []int{}, "location": "local", "sort": "relevance"})
	if r.StatusCode != 200 || m["total"].(float64) < 2 {
		t.Fatal(r.Status, m)
	}
	r, m = b.do("POST", fmt.Sprintf("/api/ai/conversations/%d/summarize", d.ChatConversation), nil)
	if r.StatusCode != http.StatusConflict || m["error"] != "ai_not_configured" {
		t.Fatal(r.Status, m)
	}
	r, _ = b.do("PUT", "/api/chat-folders/new", map[string]any{"name": "Signal", "rules": map[string]any{"channels": []string{"signal"}}, "order": 0})
	if r.StatusCode != 200 {
		t.Fatal(r.Status)
	}
	// CSRF: state changes without token are rejected.
	nb := e.browser()
	nb.c.Jar = b.c.Jar
	if r, _ := nb.do("POST", "/api/messages/flags", map[string]any{"ids": []int64{1}, "add": []string{`\Seen`}, "remove": []string{}}); r.StatusCode != 403 {
		t.Fatal("csrf not enforced", r.Status)
	}
	// Per-user isolation: another user sees an empty store.
	e.user("bert", auth.RoleUser)
	ob := e.browser()
	ob.login("bert")
	if code := getJSON(t, ob, "/api/accounts", &accs); code != 200 || len(accs) != 0 {
		t.Fatal("isolation", code, accs)
	}
	if code := getJSON(t, ob, fmt.Sprintf("/api/messages/%d", d.Messages["invoice"]), nil); code != 404 {
		t.Fatal("other user's message visible", code)
	}
	// Unauthenticated.
	if code := getJSON(t, e.browser(), "/api/accounts", nil); code != 401 {
		t.Fatal(code)
	}
}

func dialEvents(t *testing.T, e *env, origin string, header http.Header, protocols ...string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig(strings.Replace(e.ts.URL, "http", "ws", 1)+"/api/events", origin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = header
	cfg.Protocol = append(protocols, wsProtocols(header)...)
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func readEvent(t *testing.T, ws *websocket.Conn, want string) map[string]any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("waiting for %s: %v", want, err)
		}
		var ev map[string]any
		_ = json.Unmarshal([]byte(msg), &ev)
		if ev["type"] == want {
			return ev
		}
	}
}

func TestEventsWebSocket(t *testing.T) {
	e, b, _, d := appEnv(t)
	h := cookieHeader(t, e, b)
	ws := dialEvents(t, e, e.ts.URL, h)
	defer ws.Close()
	readEvent(t, ws, "connection")
	if r, _ := b.do("POST", "/api/messages/flags", map[string]any{"ids": []int64{d.Messages["invoice"]}, "add": []string{`\Seen`}, "remove": []string{}}); r.StatusCode != 204 {
		t.Fatal(r.Status)
	}
	ev := readEvent(t, ws, "message.updated")
	if int64(ev["messageId"].(float64)) != d.Messages["invoice"] {
		t.Fatal(ev)
	}

	// Foreign origins are rejected for cookie sessions.
	cfg, _ := websocket.NewConfig(strings.Replace(e.ts.URL, "http", "ws", 1)+"/api/events", "https://evil.example")
	cfg.Header = h
	cfg.Protocol = wsProtocols(h)
	if _, err := websocket.DialConfig(cfg); err == nil {
		t.Fatal("cross-site WebSocket accepted")
	}

	// Native app: device token as subprotocol, WebView origin.
	resp, m := e.browser().do("POST", "/api/device/login", map[string]string{"username": "anna", "password": pw, "device_name": "Pixel"})
	tok, _ := m["access_token"].(string)
	if resp.StatusCode != 200 || tok == "" {
		t.Fatal(resp.Status, m)
	}
	ws2 := dialEvents(t, e, "https://localhost", nil, "tuxmail.v1", "tuxmail.bearer."+tok)
	defer ws2.Close()
	if p := ws2.Config().Protocol; len(p) != 1 || p[0] != "tuxmail.v1" {
		t.Fatalf("subprotocol answer: %v", p)
	}
	readEvent(t, ws2, "connection")
}

func TestConversationReplyNeedsSender(t *testing.T) {
	_, b, _, d := appEnv(t)
	r, m := b.do("POST", fmt.Sprintf("/api/conversations/%d/reply", d.Threads["relaunch"]), map[string]string{"text": "Danke!"})
	// The demo accounts point at *.invalid hosts: sending fails, but the
	// request reaches the send pipeline (identity resolved, recipients set).
	if r.StatusCode == 404 || r.StatusCode == 400 {
		t.Fatal(r.Status, m)
	}
}
