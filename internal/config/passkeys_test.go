// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

func TestPasskeyRPDerivation(t *testing.T) {
	cases := []struct {
		url, id, origin string
		ok, enabled     bool
	}{
		{"https://mail.example.org", "mail.example.org", "https://mail.example.org", true, true},
		{"https://Mail.Example.org/sub/path", "mail.example.org", "https://mail.example.org", true, true},
		{"https://mail.example.org:8443", "mail.example.org", "https://mail.example.org:8443", true, true},
		{"https://mail.example.org:443", "mail.example.org", "https://mail.example.org", true, true},
		{"http://localhost:8080", "localhost", "http://localhost:8080", true, true},
		{"https://localhost", "localhost", "https://localhost", true, true},
		{"http://mail.example.org", "", "", false, false},      // not a secure context
		{"http://127.0.0.1:8080", "", "", false, false},        // IP addresses are not RP IDs
		{"https://192.0.2.7", "", "", false, false},            // not even over https
		{"https://[2001:db8::1]:8443", "", "", false, false},   // IPv6 literal
		{"http://localhost.example.org", "", "", false, false}, // only the literal name localhost
	}
	for _, c := range cases {
		cfg := Default()
		cfg.PublicURL = c.url
		id, origin, ok := cfg.PasskeyRP()
		if id != c.id || origin != c.origin || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.url, id, origin, ok, c.id, c.origin, c.ok)
		}
		if cfg.PasskeysEnabled() != c.enabled {
			t.Errorf("%s: PasskeysEnabled = %v, want %v (default)", c.url, cfg.PasskeysEnabled(), c.enabled)
		}
	}
}

func TestPasskeysDefaultsAndExplicit(t *testing.T) {
	c, err := Parse([]byte("public_url: https://mail.example.org\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.PasskeysEnabled() || c.Auth.Passkeys.Enabled != nil || c.PasskeyRPName() != DefaultPasskeyRPName {
		t.Fatalf("defaults: %+v", c.Auth.Passkeys)
	}
	c, err = Parse([]byte("public_url: https://mail.example.org\nauth: {passkeys: {enabled: false, rp_name: Acme Mail}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.PasskeysEnabled() || c.PasskeyRPName() != "Acme Mail" {
		t.Fatalf("explicit off: %+v", c.Auth.Passkeys)
	}
	// Off by default for a plain-http public_url, and not an error.
	c, err = Parse([]byte("public_url: http://mail.example.org\n"))
	if err != nil || c.PasskeysEnabled() {
		t.Fatalf("http: enabled=%v err=%v", c != nil && c.PasskeysEnabled(), err)
	}
	// The environment override of public_url is validated as well.
	t.Setenv(EnvPublic, "https://other.example.org:8443")
	c, err = Parse([]byte("public_url: https://mail.example.org\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, origin, _ := c.PasskeyRP(); origin != "https://other.example.org:8443" {
		t.Fatal(origin)
	}
}

func TestPasskeysValidation(t *testing.T) {
	cases := map[string]string{
		"explicit on, http":      "public_url: http://mail.example.org\nauth: {passkeys: {enabled: true}}\n",
		"explicit on, ip":        "public_url: https://192.0.2.7\nauth: {passkeys: {enabled: true}}\n",
		"explicit on, loopback":  "public_url: http://127.0.0.1:8080\nauth: {passkeys: {enabled: true}}\n",
		"rp_name too long":       "auth: {passkeys: {rp_name: " + strings.Repeat("x", 65) + "}}\n",
		"rp_name control":        "auth: {passkeys: {rp_name: \"a\\nb\"}}\n",
		"rp_name surrounding ws": "auth: {passkeys: {rp_name: \" x\"}}\n",
		"unknown passkeys key":   "auth: {passkeys: {enable: true}}\n",
		"enabled is not a bool":  "auth: {passkeys: {enabled: maybe}}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	for _, y := range []string{
		"public_url: http://localhost:8080\nauth: {passkeys: {enabled: true}}\n",
		"public_url: https://mail.example.org\nauth: {passkeys: {enabled: true, rp_name: \"Müller Mail\"}}\n",
		"public_url: http://127.0.0.1:8080\nauth: {passkeys: {enabled: false}}\n",
	} {
		if _, err := Parse([]byte(y)); err != nil {
			t.Errorf("%q: %v", y, err)
		}
	}
}
