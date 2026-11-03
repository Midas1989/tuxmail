// SPDX-License-Identifier: AGPL-3.0-or-later

package wkd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

func TestHashLocalPartVector(t *testing.T) {
	// Test vector from draft-koch-openpgp-webkey-service.
	if got := HashLocalPart("Joe.Doe"); got != "iy9q119eutrkn8s1mk4r39qejnbu3n5q" {
		t.Fatalf("hash = %q", got)
	}
	u, err := AdvancedURL("Joe.Doe@Example.ORG")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://openpgpkey.example.org/.well-known/openpgpkey/example.org/hu/iy9q119eutrkn8s1mk4r39qejnbu3n5q?l=Joe.Doe"
	if u != want {
		t.Fatalf("advanced = %q", u)
	}
	u, _ = DirectURL("Joe.Doe@Example.ORG")
	want = "https://example.org/.well-known/openpgpkey/hu/iy9q119eutrkn8s1mk4r39qejnbu3n5q?l=Joe.Doe"
	if u != want {
		t.Fatalf("direct = %q", u)
	}
	u, _ = DirectURL("a+b c@example.org")
	if want := "https://example.org/.well-known/openpgpkey/hu/" + HashLocalPart("a+b c") + "?l=a%2Bb+c"; u != want {
		t.Fatalf("escaped = %q", u)
	}
	for _, bad := range []string{"", "nodomain", "@example.org", "x@", "x@exa/mple.org"} {
		if _, err := AdvancedURL(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestZBase32(t *testing.T) {
	if got := ZBase32(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	// 0xF0 -> bits 11110 (30 -> "6") 000(00) (0 -> "y")
	if got := ZBase32([]byte{0xF0}); got != "6y" {
		t.Errorf("0xF0 = %q", got)
	}
}

// rewriteTransport sends every request to the test server and records the
// original URLs.
type rewriteTransport struct {
	target *url.URL
	mu     sync.Mutex
	seen   []string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.seen = append(rt.seen, req.URL.String())
	rt.mu.Unlock()
	r := req.Clone(req.Context())
	r.URL.Scheme = rt.target.Scheme
	r.URL.Host = rt.target.Host
	r.Host = req.URL.Host // keep the original host for routing in the handler
	return http.DefaultTransport.RoundTrip(r)
}

func newKey(t *testing.T, emails ...string) *pgp.Key {
	t.Helper()
	var uids []pgp.UserIDSpec
	for _, e := range emails {
		uids = append(uids, pgp.UserIDSpec{Email: e})
	}
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: uids})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestLookupAdvancedAndDirect(t *testing.T) {
	joe := newKey(t, "Joe.Doe@example.org")
	other := newKey(t, "someone@example.org")
	joeBin, _ := joe.PublicBytes()
	otherBin, _ := other.PublicBytes()

	var advancedEnabled = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash := HashLocalPart("joe.doe")
		switch {
		case r.Host == "openpgpkey.example.org" && r.URL.Path == "/.well-known/openpgpkey/example.org/hu/"+hash && advancedEnabled:
			if r.URL.Query().Get("l") != "Joe.Doe" {
				t.Errorf("l = %q", r.URL.Query().Get("l"))
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(append(otherBin, joeBin...)) // extra key must be filtered
		case r.Host == "example.org" && r.URL.Path == "/.well-known/openpgpkey/hu/"+hash:
			_, _ = w.Write(joeBin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	rt := &rewriteTransport{target: target}
	c := &Client{HTTPClient: &http.Client{Transport: rt, Timeout: 10 * time.Second}}

	keys, err := c.LookupByEmail(context.Background(), "Joe.Doe@Example.ORG")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Fingerprint() != joe.Fingerprint() || keys[0].IsSecret() {
		t.Fatalf("keys = %v", keys)
	}
	if len(rt.seen) != 1 {
		t.Fatalf("requests = %v", rt.seen)
	}

	// Advanced method unavailable -> direct method.
	advancedEnabled = false
	rt.seen = nil
	keys, err = c.LookupByEmail(context.Background(), "Joe.Doe@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || len(rt.seen) != 2 {
		t.Fatalf("fallback: keys=%d requests=%v", len(keys), rt.seen)
	}

	// Unknown user.
	if _, err := c.LookupByEmail(context.Background(), "nobody@example.org"); !errors.Is(err, pgp.ErrKeyNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// Cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.LookupByEmail(ctx, "Joe.Doe@example.org"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if c.Source() != pgp.SourceWKD {
		t.Fatal("source")
	}
}
