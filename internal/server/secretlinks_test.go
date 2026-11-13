// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func secretCT() string { return base64.RawURLEncoding.EncodeToString(make([]byte, 40)) }

func TestSecretLinkLifecycle(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("dora", auth.RoleUser)
	e.user("erik", auth.RoleUser)
	dora := e.browser()
	dora.login("dora")

	resp, m := dora.do("POST", "/api/secrets", map[string]any{"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 1})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, m)
	}
	id, _ := m["id"].(string)
	if len(id) != 22 {
		t.Fatalf("id %q", id)
	}
	if u, _ := m["url"].(string); !strings.HasSuffix(u, "/secret/"+id) || !strings.HasPrefix(u, e.cfg.PublicURL) {
		t.Fatalf("url %q", u)
	}
	if _, m = dora.do("GET", "/api/secrets", nil); false {
		t.Log(m)
	}

	// Somebody without an account opens it: a GET (link preview) does nothing, the POST uses it up.
	guest := e.browser()
	guest.fetchCSRF()
	if resp, _ := guest.do("GET", "/api/secrets/"+id+"/open", nil); resp.StatusCode == http.StatusOK {
		t.Fatalf("GET opened the link: %s", resp.Status)
	}
	resp, m = guest.do("POST", "/api/secrets/"+id+"/open", map[string]any{})
	if resp.StatusCode != http.StatusOK || m["ciphertext"] != secretCT() {
		t.Fatal(resp.Status, m)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
	resp, m = guest.do("POST", "/api/secrets/"+id+"/open", map[string]any{})
	if resp.StatusCode != http.StatusNotFound || m["error"] != "gone" {
		t.Fatalf("second open: %s %v", resp.Status, m)
	}
	// Same answer for an id that never existed.
	resp, m = guest.do("POST", "/api/secrets/AAAAAAAAAAAAAAAAAAAAAA/open", map[string]any{})
	if resp.StatusCode != http.StatusNotFound || m["error"] != "gone" {
		t.Fatalf("unknown id: %s %v", resp.Status, m)
	}
}

func TestSecretLinkProtections(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("dora", auth.RoleUser)
	e.user("erik", auth.RoleUser)
	dora := e.browser()
	dora.login("dora")
	erik := e.browser()
	erik.login("erik")

	// Creating needs a login.
	anon := e.browser()
	anon.fetchCSRF()
	if resp, m := anon.do("POST", "/api/secrets", map[string]any{"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 1}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status, m)
	}
	// Limits and format.
	for name, body := range map[string]map[string]any{
		"base64":   {"ciphertext": "***", "ttlSeconds": 3600, "maxViews": 1},
		"short":    {"ciphertext": base64.RawURLEncoding.EncodeToString([]byte("x")), "ttlSeconds": 3600, "maxViews": 1},
		"ttl":      {"ciphertext": secretCT(), "ttlSeconds": 30 * 24 * 3600, "maxViews": 1},
		"views":    {"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 99},
		"no views": {"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 0},
	} {
		if resp, m := dora.do("POST", "/api/secrets", body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %s %v", name, resp.Status, m)
		}
	}
	// Another user can neither delete nor see it.
	_, m := dora.do("POST", "/api/secrets", map[string]any{"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 2})
	id := m["id"].(string)
	if resp, _ := erik.do("DELETE", "/api/secrets/"+id, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign delete: %s", resp.Status)
	}
	if resp, _ := dora.do("DELETE", "/api/secrets/"+id, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("own delete: %s", resp.Status)
	}
	if resp, _ := anon.do("POST", "/api/secrets/"+id+"/open", map[string]any{}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("opened after revoke: %s", resp.Status)
	}
	// The audit log names the action but not the id.
	for _, a := range auditEntries(t, e, "secretlink.create") {
		if strings.Contains(a.Details+a.Target, id) {
			t.Fatalf("audit contains the id: %+v", a)
		}
	}
}

func TestSecretLinkPage(t *testing.T) {
	e := newEnv(t, Options{})
	resp, err := http.Get(e.ts.URL + "/secret/AAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/secret.js") {
		t.Fatal(resp.Status, string(body))
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers: %v", resp.Header)
	}
	js, err := http.Get(e.ts.URL + "/secret.js")
	if err != nil {
		t.Fatal(err)
	}
	src, _ := io.ReadAll(js.Body)
	js.Body.Close()
	if !strings.Contains(string(src), "/open") || strings.Contains(string(src), "localStorage") {
		t.Fatal("script does not look right")
	}
}

func TestSecretLinkAuditFixes(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("gast", auth.RoleReadonly)
	e.user("dora", auth.RoleUser)
	guest := e.browser()
	guest.login("gast")
	if resp, m := guest.do("POST", "/api/secrets", map[string]any{"ciphertext": secretCT(), "ttlSeconds": 3600, "maxViews": 1}); resp.StatusCode != http.StatusForbidden || m["error"] != "read_only" {
		t.Fatalf("read-only account created a link: %s %v", resp.Status, m)
	}
	dora := e.browser()
	dora.login("dora")
	// A lifetime that would overflow time.Duration is refused, not wrapped into the allowed range.
	for _, ttl := range []int64{-1, 1 << 62, 9223372036} {
		if resp, m := dora.do("POST", "/api/secrets", map[string]any{"ciphertext": secretCT(), "ttlSeconds": ttl, "maxViews": 1}); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("ttl %d: %s %v", ttl, resp.Status, m)
		}
	}
	// Link ids never reach the request log.
	for _, p := range []string{"/api/secrets/AAAAAAAAAAAAAAAAAAAAAA/open", "/api/secrets/AAAAAAAAAAAAAAAAAAAAAA", "/secret/AAAAAAAAAAAAAAAAAAAAAA"} {
		if strings.Contains(logPath(p), "AAAAAAAAAAAAAAAAAAAAAA") {
			t.Fatalf("logPath(%q) = %q", p, logPath(p))
		}
	}
}
