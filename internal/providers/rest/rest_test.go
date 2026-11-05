// SPDX-License-Identifier: AGPL-3.0-or-later

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// NF-10: the bearer token must not follow an absolute nextLink to another host.
func TestForeignAbsoluteLinkRefused(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer evil.Close()
	var auth atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer api.Close()

	c := &Client{BaseURL: api.URL + "/v1.0", Tokens: StaticToken("tok")}
	var out map[string]any
	if err := c.Get(context.Background(), evil.URL+"/v1.0/me/messages?$skiptoken=x", &out); !errors.Is(err, ErrForeignURL) {
		t.Fatalf("foreign link: err = %v, want ErrForeignURL", err)
	}
	if evilHits.Load() != 0 {
		t.Fatal("foreign host was contacted")
	}
	// A scheme change on the same host is refused as well.
	if err := c.Get(context.Background(), strings.Replace(api.URL, "http://", "https://", 1)+"/v1.0/x", &out); !errors.Is(err, ErrForeignURL) {
		t.Fatalf("scheme change: err = %v", err)
	}
	// Absolute links on the API host still work (delta/next links).
	if err := c.Get(context.Background(), api.URL+"/v1.0/me/messages/delta?$deltatoken=1", &out); err != nil {
		t.Fatalf("same-host link: %v", err)
	}
	if auth.Load() != "Bearer tok" {
		t.Errorf("Authorization = %v", auth.Load())
	}
}

// NF-10: a redirect to another host is not followed (the token would go along).
func TestRedirectToForeignHostRefused(t *testing.T) {
	var evilAuth atomic.Value
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer evil.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	defer api.Close()
	c := &Client{BaseURL: api.URL, Tokens: StaticToken("tok")}
	if err := c.Get(context.Background(), "/me", &struct{}{}); err == nil {
		t.Fatal("redirect to a foreign host followed")
	}
	if evilAuth.Load() != nil {
		t.Fatalf("foreign host contacted with %v", evilAuth.Load())
	}
}

// NF-10: JSON responses are bounded.
func TestResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"v":"` + strings.Repeat("a", 5000) + `"}`))
	}))
	defer srv.Close()
	var out map[string]string

	c := &Client{BaseURL: srv.URL, MaxResponse: 1000}
	if err := c.Get(context.Background(), "/x", &out); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	// Per-request override.
	if err := c.Do(context.Background(), Request{Method: "GET", Path: "/x", MaxBytes: 1 << 20}, &out); err != nil || len(out["v"]) != 5000 {
		t.Fatalf("override: %v", err)
	}
	// Default limit leaves normal responses alone.
	if err := (&Client{BaseURL: srv.URL}).Get(context.Background(), "/x", &out); err != nil {
		t.Fatal(err)
	}
}
