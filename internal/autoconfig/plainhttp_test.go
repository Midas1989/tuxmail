// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// NF-05: New() must not fetch plain-HTTP autoconfig documents.
func TestNewDefaultsToNoPlainHTTP(t *testing.T) {
	if New().AllowHTTP {
		t.Fatal("AllowHTTP defaults to true")
	}
	f := newWebFixture(t)
	f.set("http://autoconfig.example.org/mail/config-v1.1.xml", simpleXML("evil.example.net"))
	f.set("http://example.org/.well-known/autoconfig/mail/config-v1.1.xml", simpleXML("evil.example.net"))
	d := newTestDiscoverer(f, &fakeResolver{})
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Incoming.Host == "evil.example.net" {
			t.Fatalf("plain-HTTP document used by default: %+v", c)
		}
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "http://") {
			t.Errorf("plain-HTTP request made: %s", r)
		}
	}
}

// NF-05: with AllowHTTP the address stays out of the plain-HTTP query string
// (and in the https one, as before).
func TestPlainHTTPRequestsOmitEmailAddress(t *testing.T) {
	f := newWebFixture(t)
	f.set("http://autoconfig.example.org/mail/config-v1.1.xml", simpleXML("plain.example.org"))
	d := newTestDiscoverer(f, &fakeResolver{})
	d.AllowHTTP = true
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Incoming.Host != "plain.example.org" || !cands[0].Insecure {
		t.Fatalf("got %+v", cands[0])
	}
	sawHTTPS := false
	for _, r := range f.requests {
		switch {
		case strings.HasPrefix(r, "http://"):
			if strings.Contains(r, "bob") || strings.Contains(r, "emailaddress") {
				t.Errorf("e-mail address leaked over plain HTTP: %s", r)
			}
		case strings.HasPrefix(r, "https://autoconfig.example.org") && strings.Contains(r, "emailaddress=bob%40example.org"):
			sawHTTPS = true
		}
	}
	if !sawHTTPS {
		t.Errorf("https request lost its emailaddress: %v", f.requests)
	}
}

// NF-05: an https -> http redirect is refused by default and, when plain HTTP
// is allowed, the result is marked insecure.
func TestHTTPSToHTTPRedirect(t *testing.T) {
	var plainHits atomic.Int32
	// "downgrader" speaks https (TLS test server) and redirects to a
	// non-loopback-looking plain URL which the fixture client maps to plain.
	f := newWebFixture(t)
	f.tlsSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://downgrade.example.org/cfg.xml", http.StatusFound)
	})
	f.plainSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, simpleXML("redirected.example.org"))
	})

	d := newTestDiscoverer(f, &fakeResolver{})
	if _, err := d.FetchConfig(context.Background(), "https://autoconfig.example.org/mail/config-v1.1.xml", "bob@example.org"); err == nil {
		t.Fatal("https->http redirect followed by default")
	}
	if plainHits.Load() != 0 {
		t.Fatalf("plain HTTP server was contacted %d times", plainHits.Load())
	}
	cands := d.fetchFirst(context.Background(), "bob@example.org", SourceAutoconfig, "https://autoconfig.example.org/mail/config-v1.1.xml")
	if len(cands) != 0 {
		t.Fatalf("candidates from downgraded document: %+v", cands)
	}

	d.AllowHTTP = true
	cands = d.fetchFirst(context.Background(), "bob@example.org", SourceAutoconfig, "https://autoconfig.example.org/mail/config-v1.1.xml")
	if len(cands) == 0 || !cands[0].Insecure {
		t.Fatalf("redirected document must be marked insecure: %+v", cands)
	}
}

// NF-06/NF-07: the JMAP probe sends Basic credentials but never follows a
// redirect to plain HTTP.
func TestProbeJMAPDoesNotFollowRedirectToHTTP(t *testing.T) {
	f := newWebFixture(t)
	var plainAuth atomic.Value
	f.tlsSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://jmap.example.org/session", http.StatusFound)
	})
	f.plainSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	})
	d := newTestDiscoverer(f, &fakeResolver{})
	if c, err := d.DiscoverJMAP(context.Background(), "bob@example.org", "Sup3rSecret!"); err == nil {
		t.Fatalf("candidate from downgraded redirect: %+v", c)
	}
	if v := plainAuth.Load(); v != nil {
		t.Fatalf("plain HTTP server was contacted (Authorization %q)", v)
	}
}

// Loopback HTTP (dev server, TUXMAIL_ISPDB_URL=http://127.0.0.1:...) keeps
// working without AllowHTTP.
func TestLoopbackHTTPAllowedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, simpleXML("dev.example.org"))
	}))
	defer srv.Close()
	d := New()
	d.Resolver = &fakeResolver{}
	d.HTTPClient = srv.Client()
	d.ISPDBURL = srv.URL + "/v1.1/"
	d.AlwaysHeuristics = false
	cands, err := d.Discover(context.Background(), "anna@devmail.test")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Source != SourceISPDB || cands[0].Incoming.Host != "dev.example.org" {
		t.Fatalf("got %+v", cands[0])
	}
}
