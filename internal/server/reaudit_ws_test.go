// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

// rawStream is a hand-made WebSocket client for /api/events that exposes the
// close code the server sends (golang.org/x/net/websocket hides it).
type rawStream struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialRaw(t *testing.T, e *env, header http.Header) *rawStream {
	t.Helper()
	u, _ := url.Parse(e.ts.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	key := make([]byte, 16)
	rand.Read(key)
	var sb strings.Builder
	sb.WriteString("GET /api/events HTTP/1.1\r\nHost: " + u.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n")
	sb.WriteString("Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n")
	if header.Get("Authorization") == "" {
		sb.WriteString("Origin: " + e.ts.URL + "\r\n")
	}
	for k, vs := range header {
		for _, v := range vs {
			sb.WriteString(k + ": " + v + "\r\n")
		}
	}
	sb.WriteString("\r\n")
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("handshake: %s %s", resp.Status, b)
	}
	return &rawStream{conn: conn, br: br}
}

// next reads one frame; it returns the opcode and the payload.
func (s *rawStream) next(t *testing.T, within time.Duration) (byte, []byte, error) {
	t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(within))
	var h [2]byte
	if _, err := io.ReadFull(s.br, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var l [2]byte
		io.ReadFull(s.br, l[:])
		n = int(binary.BigEndian.Uint16(l[:]))
	case 127:
		var l [8]byte
		io.ReadFull(s.br, l[:])
		n = int(binary.BigEndian.Uint64(l[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(s.br, p); err != nil {
		return 0, nil, err
	}
	return h[0] & 0x0f, p, nil
}

// waitClose waits for the server's close frame and returns its status code.
func (s *rawStream) waitClose(t *testing.T, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		op, p, err := s.next(t, time.Until(deadline))
		if err != nil {
			t.Fatalf("stream was not closed within %v: %v", within, err)
		}
		if op == 0x8 {
			if len(p) < 2 {
				return 0
			}
			return int(binary.BigEndian.Uint16(p))
		}
	}
}

// stillOpen reports that no close frame arrives within d.
func (s *rawStream) stillOpen(t *testing.T, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		op, _, err := s.next(t, time.Until(deadline))
		if err != nil {
			return true // read timeout: nothing happened
		}
		if op == 0x8 {
			return false
		}
	}
	return true
}

func cookieHeader(t *testing.T, e *env, b *browser) http.Header {
	t.Helper()
	u, _ := url.Parse(e.ts.URL)
	h := http.Header{}
	var parts []string
	for _, c := range b.c.Jar.Cookies(u) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	h.Set("Cookie", strings.Join(parts, "; "))
	// Like the web client: the CSRF token of the session as subprotocol.
	b.fetchCSRF()
	h.Set("Sec-WebSocket-Protocol", "tuxmail.v1, "+wsCSRFProtocol+b.csrf)
	return h
}

// wsProtocols returns the subprotocols of a cookieHeader for x/net/websocket,
// which ignores that header in Config.Header.
func wsProtocols(h http.Header) []string {
	if p := h.Get("Sec-WebSocket-Protocol"); p != "" {
		return strings.Split(p, ", ")
	}
	return nil
}

func streamEnv(t *testing.T, revalidate time.Duration) *env {
	return newEnv(t, Options{StreamRevalidateEvery: revalidate, AppOptions: func(_ int64, o *app.Options) { o.Sync = nil }})
}

// NH-08: an open event stream ends when its session is revoked, expires or
// the user is disabled - not when the process restarts.
func TestReauditNH08StreamClosedWhenSessionEnds(t *testing.T) {
	const policyViolation = 1008
	t.Run("revoked in the database (periodic check)", func(t *testing.T) {
		e := streamEnv(t, 100*time.Millisecond)
		u := e.user("anna", auth.RoleUser)
		b := e.browser()
		b.login("anna")
		st := dialRaw(t, e, cookieHeader(t, e, b))
		if !st.stillOpen(t, 350*time.Millisecond) {
			t.Fatal("valid session was closed")
		}
		// Not through the HTTP API: only the periodic check can notice.
		if err := e.svc.RevokeAllSessions(context.Background(), u.ID, 0); err != nil {
			t.Fatal(err)
		}
		if code := st.waitClose(t, 3*time.Second); code != policyViolation {
			t.Fatalf("close code %d, want 1008", code)
		}
	})
	t.Run("logout closes at once", func(t *testing.T) {
		e := streamEnv(t, time.Hour) // the periodic check cannot be the reason
		e.user("anna", auth.RoleUser)
		b := e.browser()
		b.login("anna")
		st := dialRaw(t, e, cookieHeader(t, e, b))
		if resp, _ := b.do("POST", "/api/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
			t.Fatal(resp.Status)
		}
		if code := st.waitClose(t, 3*time.Second); code != policyViolation {
			t.Fatalf("close code %d, want 1008", code)
		}
	})
	t.Run("revoked device token (bearer)", func(t *testing.T) {
		e := streamEnv(t, time.Hour)
		e.user("anna", auth.RoleUser)
		web := e.browser()
		web.login("anna")
		dev := e.browser()
		_, m := dev.do("POST", "/api/device/login", map[string]string{"username": "anna", "password": pw, "device_name": "Pixel"})
		tok, _ := m["access_token"].(string)
		sid := int64(m["session_id"].(float64))
		st := dialRaw(t, e, http.Header{"Authorization": {"Bearer " + tok}})
		if !st.stillOpen(t, 200*time.Millisecond) {
			t.Fatal("valid device token closed")
		}
		if resp, _ := web.do("DELETE", fmt.Sprintf("/api/auth/sessions/%d", sid), map[string]string{"password": pw}); resp.StatusCode != http.StatusNoContent {
			t.Fatal(resp.Status)
		}
		if code := st.waitClose(t, 3*time.Second); code != policyViolation {
			t.Fatalf("close code %d, want 1008", code)
		}
	})
	t.Run("expired session", func(t *testing.T) {
		e := streamEnv(t, 100*time.Millisecond)
		e.user("anna", auth.RoleUser)
		b := e.browser()
		b.login("anna")
		st := dialRaw(t, e, cookieHeader(t, e, b))
		if _, err := e.svc.DB().Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		if code := st.waitClose(t, 3*time.Second); code != policyViolation {
			t.Fatalf("close code %d, want 1008", code)
		}
	})
	t.Run("user disabled", func(t *testing.T) {
		e := streamEnv(t, 100*time.Millisecond)
		e.user("root", auth.RoleAdmin)
		u := e.user("anna", auth.RoleUser)
		b := e.browser()
		b.login("anna")
		st := dialRaw(t, e, cookieHeader(t, e, b))
		if err := e.svc.SetDisabled(context.Background(), u.ID, true); err != nil {
			t.Fatal(err)
		}
		if code := st.waitClose(t, 3*time.Second); code != policyViolation {
			t.Fatalf("close code %d, want 1008", code)
		}
	})
}

// NA-08: one user cannot hold an unbounded number of event streams; the
// limit is per user and frees up when a stream ends.
func TestReauditNA08StreamsPerUserCapped(t *testing.T) {
	e := streamEnv(t, 0)
	e.srv.streams.max = 3
	e.user("anna", auth.RoleUser)
	e.user("ben", auth.RoleUser)
	a, other := e.browser(), e.browser()
	a.login("anna")
	other.login("ben")

	var open []*websocket.Conn
	for i := 0; i < 3; i++ {
		open = append(open, dialEvents(t, e, e.ts.URL, cookieHeader(t, e, a)))
	}
	// The fourth is refused with 429 before the upgrade.
	cfg, _ := websocket.NewConfig(strings.Replace(e.ts.URL, "http", "ws", 1)+"/api/events", e.ts.URL)
	cfg.Header = cookieHeader(t, e, a)
	cfg.Protocol = wsProtocols(cfg.Header)
	if ws, err := websocket.DialConfig(cfg); err == nil {
		ws.Close()
		t.Fatal("fourth stream of one user accepted")
	}
	preq, _ := http.NewRequest("GET", e.ts.URL+"/api/events", nil)
	preq.Header = cookieHeader(t, e, a)
	preq.Header.Set("Origin", e.ts.URL)
	preq.Header.Set("Upgrade", "websocket")
	preq.Header.Set("Connection", "Upgrade")
	preq.Header.Set("Sec-WebSocket-Version", "13")
	preq.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if resp, err := http.DefaultClient.Do(preq); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("refused with %s, want 429", resp.Status)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/events", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Authorization", "Bearer nope")
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	// Other users are unaffected.
	ws := dialEvents(t, e, e.ts.URL, cookieHeader(t, e, other))
	ws.Close()
	// A closed stream frees its slot.
	open[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ws, err := websocket.DialConfig(cfg)
		if err == nil {
			ws.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not freed after close: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range open {
		c.Close()
	}
}

// NA-08: /api/device/token polling is rate limited per device code and per
// network.
func TestReauditNA08DeviceTokenPollingLimited(t *testing.T) {
	e := newEnv(t, Options{})
	poll := func(ip, code string) (*http.Response, map[string]any) {
		c := e.browser()
		c.header = http.Header{"X-Forwarded-For": {ip}}
		return c.do("POST", "/api/device/token", map[string]string{"device_code": code})
	}
	// One (here unknown) code polled in a loop: invalid_grant until its
	// bucket is empty, then slow_down (RFC 8628).
	var invalid, slow int
	for i := 0; i < 40; i++ {
		_, m := poll("203.0.113.1", "same-code")
		switch m["error"] {
		case "invalid_grant":
			invalid++
		case "slow_down":
			slow++
		}
	}
	if slow == 0 || invalid > deviceTokenCodeBurst+2 {
		t.Fatalf("no per-code limit: %d invalid_grant, %d slow_down", invalid, slow)
	}
	// Many codes from one network: 429 once the network budget is used.
	limited := 0
	for i := 0; i < deviceTokenIPBurst+30; i++ {
		resp, m := poll("203.0.113.2", fmt.Sprintf("code-%d", i))
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
			if m["error"] != "rate_limited" || resp.Header.Get("Retry-After") == "" {
				t.Fatal(m)
			}
		}
	}
	if limited == 0 {
		t.Fatal("no per-network limit")
	}
	// Another network is not affected.
	if resp, _ := poll("198.51.100.9", "fresh-code"); resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("limit leaks across networks")
	}
	// A regular client (5 s interval) is never limited: a few polls pass.
	_, m := e.browser().do("POST", "/api/device/authorize", map[string]string{"client_name": "x"})
	code := m["device_code"].(string)
	for i := 0; i < 3; i++ {
		if _, m := poll("198.51.100.10", code); m["error"] == "rate_limited" {
			t.Fatal("regular polling limited")
		}
	}
}

// NA-08: JSON decoding errors do not leak Go decoder text.
func TestReauditNA08ReadJSONGenericError(t *testing.T) {
	e := newEnv(t, Options{})
	b := e.browser()
	b.fetchCSRF()
	for _, body := range []string{`{"username": 5}`, `{"username": "a"`, `[1,2`, `{"username":{"a":1}}`} {
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrfHeader, b.csrf)
		req.Header.Set("Origin", e.ts.URL)
		resp, err := b.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %s", body, resp.Status)
		}
		for _, leak := range []string{"unmarshal", "Go struct", "Go value", "json:", "unexpected", "cannot", "server.loginReq", "EOF", "character"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("body %q: response leaks decoder text %q: %s", body, leak, raw)
			}
		}
	}
}

// HSTS: two years (Basis spec/PARAMETER.md), includeSubDomains can be switched off, never preload.
func TestReauditNA08HSTSOption(t *testing.T) {
	for _, sub := range []bool{false, true} {
		e := newEnv(t, Options{}, func(c *config.Config) {
			c.PublicURL = "https://mail.example.de"
			c.Security.HSTSIncludeSubdomains = sub
		})
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/csrf", nil))
		got := rec.Header().Get("Strict-Transport-Security")
		want := "max-age=63072000"
		if sub {
			want += "; includeSubDomains"
		}
		if got != want {
			t.Errorf("includeSubdomains=%v: HSTS %q, want %q", sub, got, want)
		}
		if strings.Contains(strings.ToLower(got), "preload") {
			t.Error("HSTS preload must never be sent")
		}
	}
}
