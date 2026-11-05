// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Audit NF-01: the wizard's test login sends the real password.
func TestLoginAllowed(t *testing.T) {
	doms := []string{"example.org", "provider.net"}
	tls := domain.ServerConfig{Host: "imap.example.org", Port: 993, TLS: domain.TLSImplicit}
	plain := domain.ServerConfig{Host: "imap.example.org", Port: 143, TLS: domain.TLSNone}
	foreign := domain.ServerConfig{Host: "mx.attacker.test", Port: 993, TLS: domain.TLSImplicit}
	loop := domain.ServerConfig{Host: "127.0.0.1", Port: 1143, TLS: domain.TLSNone}
	cases := []struct {
		name string
		c    Candidate
		s    domain.ServerConfig
		want bool
	}{
		{"https autoconfig, tls", Candidate{Source: SourceAutoconfig}, tls, true},
		{"ispdb, foreign host", Candidate{Source: SourceISPDB}, foreign, true},
		{"known provider", Candidate{Source: SourceKnown}, foreign, true},
		{"plaintext server never", Candidate{Source: SourceAutoconfig, Insecure: true}, plain, false},
		{"plaintext even from known table", Candidate{Source: SourceKnown}, plain, false},
		{"config fetched over http never", Candidate{Source: SourceAutoconfig, Insecure: true}, tls, false},
		{"srv host outside the domain", Candidate{Source: SourceSRV}, foreign, false},
		{"srv host inside the domain", Candidate{Source: SourceSRV}, tls, true},
		{"mx host outside the domains", Candidate{Source: SourceMX}, foreign, false},
		{"mx host at the mx provider", Candidate{Source: SourceMX}, domain.ServerConfig{Host: "imap.provider.net", TLS: domain.TLSStartTLS}, true},
		{"heuristic guess inside the domain", Candidate{Source: SourceHeuristic}, tls, true},
		{"loopback dev server may be plaintext", Candidate{Source: SourceISPDB, Insecure: true}, loop, true},
	}
	for _, tc := range cases {
		if got := loginAllowed(tc.c, tc.s, doms); got != tc.want {
			t.Errorf("%s: loginAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Audit NF-02: Autodiscover Basic credentials stay within the mail domain.
func TestAdCredentialHost(t *testing.T) {
	cases := []struct {
		url, dom string
		want     bool
	}{
		{"https://autodiscover.example.org/autodiscover/autodiscover.xml", "example.org", true},
		{"https://example.org/autodiscover/autodiscover.xml", "example.org", true},
		{"https://autodiscover-s.outlook.com/autodiscover/autodiscover.xml", "example.org", true},
		{"https://outlook.office365.com/autodiscover/autodiscover.xml", "example.org", true},
		{"https://probe.test/autodiscover/autodiscover.xml", "example.org", false},
		{"https://example.org.attacker.test/autodiscover/autodiscover.xml", "example.org", false},
		{"https://notexample.org/autodiscover/autodiscover.xml", "example.org", false},
		{"https://evil-outlook.com/autodiscover/autodiscover.xml", "example.org", false},
		{"http://autodiscover.example.org/autodiscover/autodiscover.xml", "example.org", false},
		{"https://autodiscover.example.org/x", "", false},
	}
	for _, tc := range cases {
		if got := adCredentialHost(tc.url, tc.dom); got != tc.want {
			t.Errorf("adCredentialHost(%q, %q) = %v, want %v", tc.url, tc.dom, got, tc.want)
		}
	}
}
