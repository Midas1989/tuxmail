// SPDX-License-Identifier: AGPL-3.0-or-later

package imageproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

var pngData, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func TestSigner(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := &Signer{Key: bytes.Repeat([]byte{7}, 32), Subject: "42", Now: func() time.Time { return now }, TTL: time.Hour}
	p := s.Sign("https://img.example.org/a.png?x=1&y=2")
	u, _ := url.Parse(p)
	if u.Path != Path {
		t.Fatal(p)
	}
	raw, sub, err := s.Verify(u.Query())
	if err != nil || raw != "https://img.example.org/a.png?x=1&y=2" || sub != "42" {
		t.Fatal(raw, sub, err)
	}
	q := u.Query()
	q.Set("url", "https://evil.example/")
	if _, _, err := s.Verify(q); !errors.Is(err, ErrBadSignature) {
		t.Fatal("tampered url accepted")
	}
	q = u.Query()
	q.Set("u", "43")
	if _, _, err := s.Verify(q); err == nil {
		t.Fatal("other subject accepted")
	}
	now = now.Add(3 * time.Hour)
	if _, _, err := s.Verify(u.Query()); err == nil {
		t.Fatal("expired token accepted")
	}
	other := &Signer{Key: bytes.Repeat([]byte{8}, 32), Subject: "42"}
	if _, _, err := other.Verify(u.Query()); err == nil {
		t.Fatal("other key accepted")
	}
}

func TestBlockedAddresses(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "fe80::1",
		"fc00::1", "0.0.0.0", "100.64.0.1", "::ffff:127.0.0.1", "224.0.0.1", "168.63.129.16", "100.100.100.200", "::7f00:1", "::ffff:0:7f00:1"} {
		if !blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s not blocked", a)
		}
	}
	for _, a := range []string{"93.184.216.34", "2606:4700::1111"} {
		if blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s blocked", a)
		}
	}
	for _, u := range []string{"http://127.0.0.1/x.png", "http://[::1]/x", "http://localhost/x", "ftp://x.org/a", "https://user:pw@x.org/a",
		"http://x.org:22/a", "javascript:alert(1)"} {
		pu, _ := url.Parse(u)
		if checkURL(pu, false) == nil {
			t.Errorf("%s allowed", u)
		}
	}
}

func TestFetchRefusesLoopbackViaDNS(t *testing.T) {
	// The dialer control rejects resolved private addresses.
	if err := control("tcp", "127.0.0.1:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatal(err)
	}
	if err := control("tcp", "[fd00::1]:443", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Fatal(err)
	}
}

func TestProxyServeAndCache(t *testing.T) {
	var hits atomic.Int32
	var gotReferer, gotCookie atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotReferer.Store(r.Referer())
		gotCookie.Store(r.Header.Get("Cookie"))
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Set-Cookie", "track=1")
			w.Write(pngData)
		case "/page.html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/fake.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("<svg xmlns='http://www.w3.org/2000/svg'><script>x</script></svg>"))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(append(append([]byte{}, pngData...), make([]byte, 2048)...))
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1:1/x.png", http.StatusFound)
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bs, _ := blob.New(filepath.Join(dir, "blobs"))
	cache := &StoreCache{Store: st, Blobs: bs}
	p := &Proxy{Signer: &Signer{Key: bytes.Repeat([]byte{1}, 32)}, Fetcher: &Fetcher{AllowPrivate: true, MaxSize: 1024}, Cache: cache}

	get := func(path string, authed bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Cookie", "session=secret")
		p.Serve(rec, req, authed)
		return rec
	}
	signed := p.SignURL(up.URL + "/a.png")
	rec := get(signed, false)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pngData) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(rec.Header())
	}
	if gotReferer.Load() != "" || gotCookie.Load() != "" {
		t.Fatal("referer/cookie leaked", gotReferer.Load(), gotCookie.Load())
	}
	// Cached: no second upstream request.
	if rec := get(signed, false); rec.Code != 200 || hits.Load() != 1 {
		t.Fatalf("cache miss: %d hits=%d", rec.Code, hits.Load())
	}
	// Unsigned only when authenticated.
	plain := Path + "?url=" + url.QueryEscape(up.URL+"/a.png")
	if rec := get(plain, false); rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
	if rec := get(plain, true); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	for path, want := range map[string]int{"/page.html": 415, "/fake.png": 415, "/big.png": 413} {
		if rec := get(p.SignURL(up.URL+path), false); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	// Redirects are re-checked (here: port 1 on loopback with private allowed fails to connect).
	p.Fetcher = &Fetcher{}
	if rec := get(p.SignURL(up.URL+"/redirect"), false); rec.Code == 200 {
		t.Fatal("redirect to loopback served")
	}
	// Purge removes expired entries.
	cache.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if err := cache.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get(context.Background(), Key(up.URL+"/a.png")); ok {
		t.Fatal("expired entry served")
	}
}

func TestAllowlist(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.AddRemoteAllow(ctx, store.AllowSender, "News@Shop.example"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRemoteAllow(ctx, store.AllowDomain, "@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddRemoteAllow(ctx, "x", "y"); err == nil {
		t.Fatal("invalid kind accepted")
	}
	for addr, want := range map[string]bool{"news@shop.example": true, "other@shop.example": false, "a@example.org": true,
		"a@mail.example.org": true, "a@example.org.evil": false} {
		if got, err := st.RemoteAllowed(ctx, addr); err != nil || got != want {
			t.Errorf("%s: %v %v", addr, got, err)
		}
	}
	list, _ := st.ListRemoteAllow(ctx)
	if len(list) != 2 {
		t.Fatal(list)
	}
	_ = st.RemoveRemoteAllow(ctx, store.AllowDomain, "example.org")
	if ok, _ := st.RemoteAllowed(ctx, "a@example.org"); ok {
		t.Fatal("still allowed")
	}
}
