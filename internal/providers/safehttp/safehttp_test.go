// SPDX-License-Identifier: AGPL-3.0-or-later

package safehttp

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestIsLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "127.0.0.1:8080": true, "[::1]:25": true, "::1": true,
		"foo.localhost": true, "example.com": false, "10.0.0.1": false, "127.0.0.1.evil.com": false, "": false,
	} {
		if got := IsLoopbackHost(h); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestRequireSecure(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
		ok       bool
	}{
		{"https://mail.example.com/eas", false, true},
		{"http://mail.example.com/eas", false, false},
		{"http://mail.example.com/eas", true, true},
		{"http://127.0.0.1:1080/eas", false, true},
		{"http://localhost/eas", false, true},
		{"ftp://example.com", true, false},
		{"https://", false, false},
		{"not a url", false, false},
	} {
		err := RequireSecure(tc.url, tc.insecure)
		if (err == nil) != tc.ok {
			t.Errorf("RequireSecure(%q, %v) = %v, want ok=%v", tc.url, tc.insecure, err, tc.ok)
		}
	}
	if err := RequireSecure("http://example.com", false); !errors.Is(err, ErrInsecure) {
		t.Errorf("err = %v", err)
	}
}

func TestCredentialsAllowed(t *testing.T) {
	for _, tc := range []struct {
		origin, target string
		ok             bool
	}{
		{"https://dav.example.com/x", "https://dav.example.com/y", true},
		{"https://dav.example.com/x", "https://DAV.example.com:443/y", true},
		{"https://dav.example.com/x", "https://dav.example.com:8443/y", true}, // same registrable domain
		{"https://127.0.0.1:1/x", "https://127.0.0.1:2/y", false},
		{"https://dav.example.com", "https://p01-dav.example.com/y", true},
		{"https://dav.example.com", "https://evil.test/y", false},
		{"https://dav.example.com", "https://example.com.evil.test/y", false},
		{"https://dav.example.com", "http://dav.example.com/y", false},
		{"https://a.co.uk", "https://b.co.uk", false},
		{"https://x.a.co.uk", "https://y.a.co.uk", true},
		{"http://dav.example.com", "http://dav.example.com/y", true},
		{"http://dav.example.com", "http://other.example.com/y", false},
		{"http://127.0.0.1:1", "http://127.0.0.1:2", false},
		{"https://127.0.0.1:1", "https://127.0.0.1:1/p", true},
		{"https://dav.example.com", "ftp://dav.example.com", false},
	} {
		if got := CredentialsAllowed(mustURL(t, tc.origin), mustURL(t, tc.target)); got != tc.ok {
			t.Errorf("CredentialsAllowed(%s, %s) = %v, want %v", tc.origin, tc.target, got, tc.ok)
		}
	}
}

func TestCheckRedirect(t *testing.T) {
	cr := CheckRedirect(nil, 3)
	first, _ := http.NewRequest("GET", "https://a.example.com/", nil)
	next := func(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }
	if err := cr(next("https://a.example.com/x"), []*http.Request{first}); err != nil {
		t.Errorf("same host: %v", err)
	}
	if err := cr(next("http://a.example.com/x"), []*http.Request{first}); !errors.Is(err, ErrInsecure) {
		t.Errorf("downgrade: %v", err)
	}
	if err := cr(next("https://evil.test/x"), []*http.Request{first}); !errors.Is(err, ErrForeignHost) {
		t.Errorf("foreign: %v", err)
	}
	if err := cr(next("https://a.example.com/x"), []*http.Request{first, first, first}); err == nil {
		t.Error("redirect limit not enforced")
	}
}
