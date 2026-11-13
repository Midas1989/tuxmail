// SPDX-License-Identifier: AGPL-3.0-or-later

package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[string]class{
		"8.8.8.8":            classPublic,
		"2a00:1450::1":       classPublic,
		"10.1.2.3":           classPrivate,
		"172.20.0.1":         classPrivate,
		"192.168.178.1":      classPrivate,
		"100.64.0.1":         classPrivate,
		"127.0.0.1":          classPrivate,
		"::1":                classPrivate,
		"fd12::1":            classPrivate,
		"::ffff:127.0.0.1":   classPrivate,
		"169.254.169.254":    classBlocked,
		"fd00:ec2::254":      classBlocked,
		"fe80::1":            classBlocked,
		"0.0.0.0":            classBlocked,
		"::":                 classBlocked,
		"224.0.0.1":          classBlocked,
		"255.255.255.255":    classBlocked,
		"64:ff9b::a9fe:a9fe": classBlocked, // NAT64 of 169.254.169.254
		"64:ff9b::808:808":   classPublic,  // NAT64 of 8.8.8.8
		"2002:7f00:1::":      classBlocked, // 6to4 of 127.0.0.1
		"168.63.129.16":      classBlocked, // Azure WireServer
		"100.100.100.200":    classBlocked, // Alibaba metadata
		"100.100.100.201":    classPrivate,
		"::7f00:1":           classBlocked, // IPv4-compatible 127.0.0.1
		"::a00:1":            classBlocked, // IPv4-compatible 10.0.0.1
		"::ffff:0:7f00:1":    classBlocked, // IPv4-translated 127.0.0.1
	}
	for s, want := range cases {
		if got := classify(netip.MustParseAddr(s)); got != want {
			t.Errorf("classify(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestPolicyCheck(t *testing.T) {
	r1, _ := ParseRule("127.0.0.1:8008")
	r2, _ := ParseRule("10.0.0.0/8")
	r3, _ := ParseRule("[fd00::/8]:993")
	r4, _ := ParseRule("169.254.10.5")
	p := &Policy{Allowed: []Rule{r1, r2, r3, r4}}
	allow := []string{"1.1.1.1:443", "127.0.0.1:8008", "10.9.9.9:25", "[fd00::5]:993", "169.254.10.5:80", "[::ffff:10.0.0.1]:1"}
	deny := []string{"127.0.0.1:8080", "192.168.1.1:993", "[fd00::5]:80", "169.254.169.254:80", "[::1]:8008"}
	for _, s := range allow {
		if err := p.Check(netip.MustParseAddrPort(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range deny {
		if err := p.Check(netip.MustParseAddrPort(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: allowed", s)
		}
	}
	priv := &Policy{AllowPrivate: true}
	if err := priv.Check(netip.MustParseAddrPort("192.168.1.1:993")); err != nil {
		t.Error(err)
	}
	if err := priv.Check(netip.MustParseAddrPort("127.0.0.1:993")); err != nil {
		t.Error(err)
	}
	if err := priv.Check(netip.MustParseAddrPort("169.254.169.254:80")); err == nil {
		t.Error("metadata endpoint allowed with allow_private_networks")
	}
	for _, s := range []string{"168.63.129.16:80", "100.100.100.200:80", "[::7f00:1]:80", "[::ffff:0:7f00:1]:80"} {
		if err := priv.Check(netip.MustParseAddrPort(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s allowed with allow_private_networks", s)
		}
	}
}

func TestParseRule(t *testing.T) {
	for _, s := range []string{"10.0.0.0/8", "10.0.0.0/8:993", "1.2.3.4", "1.2.3.4:5", "fd00::/8", "fd00::1", "[fd00::1]:5", "[fd00::/8]:5"} {
		if _, err := ParseRule(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "example.org", "1.2.3.4:0", "1.2.3.4:x", "[fd00::1", "10.0.0.0/33"} {
		if _, err := ParseRule(s); err == nil {
			t.Errorf("%s: accepted", s)
		}
	}
}

// TestDefaultTransportGuarded is the SSRF PoC: with a policy installed,
// http.DefaultClient (used by CalDAV, webhooks, AI, Web Push ...) and
// guarded dialers (IMAP, SMTP ...) cannot reach loopback services.
func TestDefaultTransportGuarded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("internal")) }))
	defer ts.Close()
	t.Cleanup(func() { SetPolicy(nil) })

	SetPolicy(nil)
	if resp, err := http.Get(ts.URL); err != nil {
		t.Fatal("unrestricted without policy:", err)
	} else {
		resp.Body.Close()
	}

	SetPolicy(&Policy{})
	if _, err := http.Get(ts.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback reachable through the default transport: %v", err)
	}
	if _, err := (&http.Client{}).Get(ts.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback reachable through a client without transport: %v", err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if _, err := (&http.Client{Transport: tr}).Get(ts.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback reachable through a cloned default transport: %v", err)
	}
	if _, err := DialContext(context.Background(), "tcp", ts.Listener.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Fatalf("guarded dialer reached loopback: %v", err)
	}
	// Administrator-configured endpoints.
	if resp, err := TrustedClient(0).Get(ts.URL); err != nil {
		t.Fatal("trusted client blocked:", err)
	} else {
		resp.Body.Close()
	}
	req, _ := http.NewRequestWithContext(Trusted(context.Background()), "GET", ts.URL, nil)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal("trusted context blocked:", err)
	} else {
		resp.Body.Close()
	}
	// Allowlisted host:port.
	r, err := ParseRule(ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	SetPolicy(&Policy{Allowed: []Rule{r}})
	if resp, err := http.Get(ts.URL); err != nil {
		t.Fatal("allowlisted address blocked:", err)
	} else {
		resp.Body.Close()
	}
	// DNS names are checked after resolution.
	SetPolicy(&Policy{})
	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	if _, err := http.Get("http://localhost:" + port); err == nil {
		t.Fatal("localhost reachable by name")
	}
}

func TestMessengerPolicy(t *testing.T) {
	t.Cleanup(func() { SetPolicy(nil); SetMessengerPolicy(nil) })
	gp := &Policy{}
	SetPolicy(gp)
	if MessengerPolicy() != gp {
		t.Fatal("messenger policy must fall back to the general one")
	}
	r, _ := ParseRule("127.0.0.1:8008")
	SetMessengerPolicy(&Policy{Allowed: []Rule{r}})
	if err := MessengerPolicy().Check(netip.MustParseAddrPort("127.0.0.1:8008")); err != nil {
		t.Fatal(err)
	}
	if err := Current().Check(netip.MustParseAddrPort("127.0.0.1:8008")); err == nil {
		t.Fatal("general policy must not allow the messenger-only entry")
	}
}

// TestProxyAddressNotDialedDirectly: a user URL pointing at the environment
// proxy (e.g. matched by NO_PROXY) must not skip the policy check.
func TestProxyAddressNotDialedDirectly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("internal")) }))
	defer ts.Close()
	addr := ts.Listener.Addr().String()
	t.Setenv("HTTP_PROXY", "http://"+addr)
	t.Setenv("NO_PROXY", "127.0.0.1")
	t.Setenv("http_proxy", "http://"+addr)
	t.Setenv("no_proxy", "127.0.0.1")
	proxyOnce = sync.Once{}
	t.Cleanup(func() { SetPolicy(nil); proxyOnce = sync.Once{} })
	SetPolicy(&Policy{})

	req, _ := http.NewRequest("GET", ts.URL, nil)
	if _, err := proxyDefault(req); !errors.Is(err, ErrBlocked) {
		t.Fatalf("direct dial of the proxy address allowed: %v", err)
	}
	if _, err := http.Get(ts.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("proxy address reachable as target: %v", err)
	}
	// The connection to the proxy itself stays unchecked.
	c, err := dialDefault(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
