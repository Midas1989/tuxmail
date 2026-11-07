// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Regression tests (proofs of concept) of security audit A, see
// docs/security/AUDIT-A-server.md. Each test is named after its finding.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/netguard"
	"github.com/Midas1989/Mailclient/internal/team"
)

func noSync(_ int64, o *app.Options) { o.Sync = nil }

func (b *browser) tags() string {
	b.e.t.Helper()
	resp, err := b.c.Do(b.req("GET", "/api/tags"))
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return buf.String()
}

func (b *browser) req(method, path string) *http.Request {
	req, _ := http.NewRequest(method, b.e.ts.URL+path, nil)
	if b.csrf != "" {
		req.Header.Set(csrfHeader, b.csrf)
	}
	return req
}

// A-01: a new user must never get the id (and thereby the data directory
// or the still cached App) of a deleted user.
func TestAuditA01DeletedUserDataNotInherited(t *testing.T) {
	e := newEnv(t, Options{AppOptions: noSync})
	e.user("root", auth.RoleAdmin)
	for _, keep := range []bool{false, true} {
		victim := e.user(fmt.Sprintf("victim%v", keep), auth.RoleUser)
		vb := e.browser()
		vb.login(victim.Username)
		if resp, m := vb.do("POST", "/api/tags", map[string]string{"path": "secret-of-victim"}); resp.StatusCode != http.StatusCreated {
			t.Fatal(resp.Status, m)
		}
		admin := e.browser()
		admin.login("root")
		q := ""
		if keep {
			q = "?keepData=true"
		}
		if resp, m := admin.do("DELETE", fmt.Sprintf("/api/admin/users/%d%s", victim.ID, q), nil); resp.StatusCode != http.StatusNoContent {
			t.Fatal(resp.Status, m)
		}
		resp, m := admin.do("POST", "/api/admin/users", map[string]string{"username": fmt.Sprintf("next%v", keep), "password": pw})
		if resp.StatusCode != http.StatusCreated {
			t.Fatal(resp.Status, m)
		}
		id := int64(m["user"].(map[string]any)["id"].(float64))
		if id == victim.ID {
			t.Errorf("keepData=%v: new user got the id %d of the deleted user", keep, id)
		}
		nb := e.browser()
		nb.login(fmt.Sprintf("next%v", keep))
		if tags := nb.tags(); strings.Contains(tags, "secret-of-victim") {
			t.Fatalf("keepData=%v: new user sees the deleted user's data: %s", keep, tags)
		}
	}
}

// A-02: in server mode users cannot make the server connect to internal
// addresses (here: a loopback "intranet" service via a CalDAV account).
func TestAuditA02SSRFBlocked(t *testing.T) {
	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "internal", http.StatusUnauthorized)
	}))
	defer internal.Close()
	t.Cleanup(func() { netguard.SetPolicy(nil) })

	for _, allow := range []bool{false, true} {
		hits.Store(0)
		e := newEnv(t, Options{AppOptions: noSync}, func(c *config.Config) { c.Security.AllowPrivateNetworks = allow })
		e.user(fmt.Sprintf("anna%v", allow), auth.RoleUser)
		b := e.browser()
		b.login(fmt.Sprintf("anna%v", allow))
		resp, m := b.do("POST", "/api/dav-accounts", map[string]string{"url": internal.URL + "/dav/", "username": "u", "password": "p"})
		t.Logf("allow_private_networks=%v: %s %v (hits %d)", allow, resp.Status, m, hits.Load())
		if !allow && hits.Load() != 0 {
			t.Fatalf("server connected to an internal address on behalf of a user (%d requests)", hits.Load())
		}
		if allow && hits.Load() == 0 {
			t.Fatal("allow_private_networks: the internal server was not contacted (test does not exercise the path)")
		}
	}
}

// A-03: the role readonly is enforced: no writes through the API and at
// most the read role on team mailboxes.
func TestAuditA03ReadonlyRole(t *testing.T) {
	e := newEnv(t, Options{AppOptions: noSync})
	admin := e.user("root", auth.RoleAdmin)
	guest := e.user("guest", auth.RoleReadonly)
	b := e.browser()
	b.login("guest")
	if resp, _ := b.do("POST", "/api/tags", map[string]string{"path": "x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("readonly user created a tag: %s", resp.Status)
	}
	if resp, _ := b.do("DELETE", "/api/signatures/1", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("readonly user may delete: %s", resp.Status)
	}
	if resp, _ := b.do("GET", "/api/tags", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readonly user cannot read: %s", resp.Status)
	}
	if resp, _ := b.do("POST", "/api/search", map[string]any{"query": "x"}); resp.StatusCode == http.StatusForbidden {
		t.Fatal("search refused for readonly user")
	}
	// Self-service stays available.
	if resp, _ := b.do("GET", "/api/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	ctx := context.Background()
	ts := e.srv.TeamService()
	mb, err := ts.CreateMailbox(ctx, team.Actor{UserID: admin.ID}, team.MailboxInput{Name: "Support", Address: "support@example.de"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.SetGrant(ctx, team.Actor{UserID: admin.ID}, mb.ID, guest.ID, 0, team.RoleManage); err != nil {
		t.Fatal(err)
	}
	if r, _ := ts.RoleOf(ctx, guest.ID, mb.ID); r != team.RoleRead {
		t.Fatalf("readonly user has team role %q, want read", r)
	}
	if _, err := ts.Require(ctx, guest.ID, mb.ID, team.RoleReply); !errors.Is(err, team.ErrForbidden) {
		t.Fatalf("readonly user may reply in a team mailbox: %v", err)
	}
}

// A-04: logging out (or revoking a session) ends its push subscription, so
// a signed-out browser no longer receives mail previews.
func TestAuditA04PushEndsWithSession(t *testing.T) {
	e := newEnv(t, Options{})
	u := e.user("anna", auth.RoleUser)
	p256 := append([]byte{4}, bytes.Repeat([]byte{1}, 64)...)
	sub := map[string]any{"endpoint": "https://push.example.org/abc", "deviceName": "Kiosk",
		"keys": map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(p256), "auth": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 16))}}
	for _, how := range []string{"logout", "revoke"} {
		b := e.browser()
		b.login("anna")
		if resp, m := b.do("POST", "/api/push/subscribe", sub); resp.StatusCode/100 != 2 {
			t.Fatal(resp.Status, m)
		}
		if l, _ := e.srv.pushStore.List(context.Background(), u.ID); len(l) != 1 {
			t.Fatalf("subscriptions: %d", len(l))
		}
		switch how {
		case "logout":
			b.do("POST", "/api/auth/logout", nil)
		case "revoke":
			other := e.browser()
			other.login("anna")
			other.do("POST", "/api/auth/sessions/revoke-others", map[string]string{"password": pw})
		}
		if l, _ := e.srv.pushStore.List(context.Background(), u.ID); len(l) != 0 {
			t.Fatalf("%s: push subscription survived the session", how)
		}
	}
}

// A-06: IPv6 clients are rate limited per /64.
func TestAuditA06IPv6RateLimitPrefix(t *testing.T) {
	if limiterKey("2001:db8:1:2::1") != limiterKey("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Fatal("addresses of one /64 get separate login buckets")
	}
	if limiterKey("2001:db8:1:2::1") == limiterKey("2001:db8:1:3::1") {
		t.Fatal("different /64 share a bucket")
	}
	if limiterKey("198.51.100.7") != "198.51.100.7" || limiterKey("local") != "local" {
		t.Fatal("IPv4 keys changed")
	}
	e := newEnv(t, Options{}, func(c *config.Config) { c.Limits.LoginRatePerMinute = 1 })
	b := e.browser()
	b.fetchCSRF()
	blocked := false
	for i := range 10 {
		b.header = http.Header{"X-Forwarded-For": {fmt.Sprintf("2001:db8::%x", i+1)}}
		// Password spraying: a new username each time (no per-user lockout).
		if resp, m := b.do("POST", "/api/auth/login", map[string]string{"username": fmt.Sprintf("user%d", i), "password": "y"}); resp.StatusCode == http.StatusTooManyRequests && m["error"] == "rate_limited" {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("rotating IPv6 addresses inside one /64 bypasses the login rate limit")
	}
}

// A-09: limits.max_upload_mb is enforced by the server itself.
func TestAuditA09BodyLimit(t *testing.T) {
	e := newEnv(t, Options{AppOptions: noSync}, func(c *config.Config) { c.Limits.MaxUploadMB = 1 })
	e.user("anna", auth.RoleUser)
	b := e.browser()
	b.login("anna")
	for _, chunked := range []bool{false, true} {
		var body = bytes.NewReader(bytes.Repeat([]byte("x"), 2<<20))
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/io/uploads", body)
		if chunked {
			req.ContentLength = -1
			req.Body = io.NopCloser(io.MultiReader(body))
		}
		req.Header.Set(csrfHeader, b.csrf)
		req.Header.Set("Origin", e.ts.URL)
		req.Header.Set("X-Filename", "big.mbox")
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked=%v: upload larger than max_upload_mb accepted: %s", chunked, resp.Status)
		}
	}
}

// A-11: the login page only redirects to paths of this origin.
func TestAuditA11LoginNextRedirect(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	start := strings.Index(loginJS, "function next(){")
	end := strings.Index(loginJS[start:], "\nf.addEventListener")
	fn := loginJS[start : start+end]
	script := `const location={origin:'https://mail.example.de',search:''};` + fn + `
const cases={'/mail/inbox?x=1':'/mail/inbox?x=1','//evil.example':'/','/\\evil.example':'/','/\t/evil.example':'/',
'https://evil.example/':'/','javascript:alert(1)':'/','':'/','/%5Cevil.example':'/%5Cevil.example'};
let bad=0;for(const [n,want] of Object.entries(cases)){location.search='?next='+encodeURIComponent(n);const got=next();
if(got!==want){console.log('next('+JSON.stringify(n)+') = '+JSON.stringify(got)+', want '+JSON.stringify(want));bad++}}
process.exit(bad)`
	dir := t.TempDir()
	f := filepath.Join(dir, "next.js")
	os.WriteFile(f, []byte(script), 0o600)
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("open redirect in /login: %v\n%s", err, out)
	}
}
