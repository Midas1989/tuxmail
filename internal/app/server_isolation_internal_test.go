// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Regression tests of the server-mode isolation fixes (re-audit 2026-11-05,
// NH-01, NH-02, NH-04, NH-05, NH-06): nothing a user does may change or keep
// alive state that belongs to another user's App.

type nameRenderer struct{ name string }

func (r nameRenderer) Render(context.Context, []byte) (RenderedMessage, error) {
	return RenderedMessage{Text: "text", HTML: `<img src="/api/imageproxy?u=` + r.name + `">`}, nil
}

func (nameRenderer) Attachment(context.Context, []byte, int) (RenderedAttachment, []byte, error) {
	return RenderedAttachment{}, nil, errors.New("none")
}

func newIsolationApp(t *testing.T, opts Options) *App {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bl, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	opts.Store, opts.Blobs = st, bl
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	a, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// NH-01: a server user cannot force the desktop loopback flow (listener and
// browser opener on the server); the opener is never invoked.
func TestStartOAuthLoopbackRefusedInServerMode(t *testing.T) {
	var opened atomic.Int32
	opener := func(context.Context, string) error { opened.Add(1); return nil }
	a := newIsolationApp(t, Options{Mode: ModeServer, OAuth: &OAuthOptions{
		Clients: oauth.Clients{"microsoft": {ID: "cid", Source: "admin"}}, Opener: opener, RedirectBase: "https://mail.example.org"}})
	_, err := a.StartOAuth(context.Background(), OAuthStartInput{Provider: "microsoft", Email: "u@contoso.example", Kind: "graph", Mode: "loopback"})
	if e := AsError(err); e == nil || e.Status != 400 {
		t.Fatalf("loopback in server mode: err = %v, want a 400", err)
	}
	if opened.Load() != 0 {
		t.Fatal("the browser opener ran on the server")
	}
	a.oauth.mu.Lock()
	n := len(a.oauth.flows)
	a.oauth.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d flows left behind", n)
	}
	// The default mode of the server is redirect and still works.
	fl, err := a.StartOAuth(context.Background(), OAuthStartInput{Provider: "microsoft", Email: "u@contoso.example", Kind: "graph"})
	if err != nil || fl.Mode != "redirect" {
		t.Fatalf("default server mode: %+v, %v", fl, err)
	}
	// Neither a configured nor the default opener can be reached in server mode.
	a.SetOAuthOpener(opener)
	if err := a.oauth.opt.Opener(context.Background(), "https://example.org/"); err == nil {
		t.Fatal("server-mode opener must refuse")
	}
	if opened.Load() != 0 {
		t.Fatal("the browser opener ran on the server")
	}
}

// NH-09: a server user cannot make the server download and install programs.
func TestInstallMessengerComponentRefusedInServerMode(t *testing.T) {
	a := newIsolationApp(t, Options{Mode: ModeServer})
	err := a.InstallMessengerComponent(context.Background(), "signal")
	if e := AsError(err); e == nil || e.Status != 403 {
		t.Fatalf("InstallMessengerComponent in server mode: %v, want 403", err)
	}
}

// NH-02: Close leaves nothing behind that keeps the App reachable.
func TestClosedAppIsGarbageCollected(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bl, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	wp := func() weak.Pointer[App] {
		a, err := New(ctx, Options{Store: st, Blobs: bl, Mode: ModeServer, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		a.Start()
		_ = a.io()
		a.RunDueBackups(ctx) // what the first timer tick does
		a.kickTimers()
		time.Sleep(100 * time.Millisecond)
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		return weak.Make(a)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for wp.Value() != nil && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	if wp.Value() != nil {
		t.Fatal("a closed App is still referenced (package-level registry?)")
	}
}

// NH-04: the prefetch bookkeeping is per App; message ids collide between users.
func TestBodyPrefetchIsPerApp(t *testing.T) {
	a, b := &App{}, &App{}
	if !a.prefetch.claim(7) {
		t.Fatal("first claim refused")
	}
	if a.prefetch.claim(7) {
		t.Fatal("the same App may not prefetch a message twice at once")
	}
	if !b.prefetch.claim(7) {
		t.Fatal("another user's message 7 was treated as busy")
	}
	a.prefetch.release(7)
	if !a.prefetch.claim(7) {
		t.Fatal("release did not free the id")
	}
}

// NH-05: a rendered mail (with the image-proxy URLs signed for one user) is
// never served from another user's App, even with identical blob hash and id.
func TestRenderCacheIsPerApp(t *testing.T) {
	ctx := context.Background()
	alice := newIsolationApp(t, Options{Mode: ModeServer, Renderer: nameRenderer{"alice"}})
	bob := newIsolationApp(t, Options{Mode: ModeServer, Renderer: nameRenderer{"bob"}})
	hash, err := alice.blobs.Put(strings.NewReader("Subject: x\r\n\r\nsame bytes\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if h2, err := bob.blobs.Put(strings.NewReader("Subject: x\r\n\r\nsame bytes\r\n")); err != nil || h2 != hash {
		t.Fatal(h2, err)
	}
	m := store.Message{ID: 1, BlobHash: hash}
	ra, err := alice.renderCached(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := bob.renderCached(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ra.HTML, "u=alice") || strings.Contains(rb.HTML, "alice") || !strings.Contains(rb.HTML, "u=bob") {
		t.Fatalf("cross-user render cache hit:\nalice %s\nbob %s", ra.HTML, rb.HTML)
	}
	if ba, bb := alice.bubble(ctx, m), bob.bubble(ctx, m); ba.visible == "" || bb.visible == "" {
		t.Fatal("bubble")
	}
	if _, ok := bob.bubbles.get(hash); !ok {
		t.Fatal("bubble cache of bob is empty")
	}
	fresh := &App{}
	if _, ok := fresh.bubbles.get(hash); ok {
		t.Fatal("bubble cache is shared between Apps")
	}
}

// NH-05: the bubble and render caches have a byte budget.
func TestCachesHaveByteBudget(t *testing.T) {
	var c bodyCache
	chunk := strings.Repeat("x", maxBodyCacheBytes/16)
	for i := 0; i < 100; i++ {
		c.put(string(rune('a'+i%26))+strings.Repeat("k", i), bodyParts{visible: chunk})
	}
	if c.bytes > maxBodyCacheBytes || len(c.m) == 0 {
		t.Fatalf("bubble cache holds %d bytes in %d entries (budget %d)", c.bytes, len(c.m), maxBodyCacheBytes)
	}
	c.put("huge", bodyParts{visible: strings.Repeat("x", maxBodyCacheBytes)})
	if _, ok := c.get("huge"); ok {
		t.Fatal("a single oversized entry was cached")
	}
	var r renderCache
	h := strings.Repeat("y", maxRenderCacheBytes/8)
	for i := 0; i < 100; i++ {
		r.put(strings.Repeat("k", i+1), RenderedMessage{HTML: h})
	}
	if r.bytes > maxRenderCacheBytes || len(r.m) == 0 {
		t.Fatalf("render cache holds %d bytes (budget %d)", r.bytes, maxRenderCacheBytes)
	}
}

// NH-06: a failed Chromium run only affects the App it happened in.
func TestChromiumBrokenIsPerApp(t *testing.T) {
	a, b := &App{opts: Options{Mode: ModeDesktop}}, &App{opts: Options{Mode: ModeDesktop}}
	prog := filepath.Join(t.TempDir(), "my-browser")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var s ComfortSettings
	s.PDF.ChromiumPath = prog
	a.chromiumBroken.Store(true)
	if _, err := a.chromium(s); err == nil {
		t.Fatal("broken browser still used by the same App")
	}
	if got, err := b.chromium(s); err != nil || got != prog {
		t.Fatalf("another App is affected: %q, %v", got, err)
	}
}
