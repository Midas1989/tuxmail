// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "github.com/Midas1989/Mailclient/internal/netguard"

// Security configures the outbound connection policy of the server mode
// (SSRF protection, internal/netguard) and two server hardening switches
// (ExposeVersion, HSTSIncludeSubdomains). By default users can only make the
// server connect to public addresses: loopback, private networks,
// link-local addresses and cloud metadata endpoints are refused for every
// user-configured destination (mail, calendar, cloud and messenger
// servers, ICS subscriptions, webhooks, AI providers, Web Push endpoints,
// autoconfig, WKD/VKS).
//
// Messenger plugins are separate processes: the server hands this policy to
// them in the hello handshake (wire.HelloParams.NetPolicy) and the plugin
// SDK installs it for the plugin's http.DefaultTransport, which covers the
// user-chosen Matrix homeserver; the server additionally requires https and
// checks the homeserver host before pairing. Not covered by the policy are
// connections that plugins make with their own dialers to fixed vendor
// endpoints (WhatsApp, Telegram) and the signal-cli process.
type Security struct {
	// AllowPrivateNetworks permits private networks (RFC 1918, fc00::/7,
	// 100.64.0.0/10) and loopback, e.g. for a mail server in the same LAN.
	// Every user of the server can then reach these networks. Link-local
	// addresses (169.254.0.0/16, fe80::/10, metadata services) stay blocked.
	AllowPrivateNetworks bool `yaml:"allow_private_networks"`
	// AllowedNetworks are always reachable: addresses or CIDR prefixes,
	// optionally with a port, e.g. "192.168.10.5", "10.20.0.0/16:993",
	// "127.0.0.1:8008" (local Matrix homeserver), "[fd00::/8]:443".
	AllowedNetworks []string `yaml:"allowed_networks"`

	// ExposeVersion lets the unauthenticated endpoints /healthz and
	// /api/auth/config report the exact program version (default off: it
	// eases targeted attacks with known vulnerabilities). Both always carry
	// the product marker "product":"tuxmail" so clients (the app's "connect
	// to server" flow) still recognise the server. Authenticated users
	// (diagnostics, provisioning profile) always see the version.
	ExposeVersion bool `yaml:"expose_version"`
	// HSTSIncludeSubdomains adds includeSubDomains to the
	// Strict-Transport-Security header sent for HTTPS deployments (default
	// off). Enable it only when EVERY subdomain of the host name is served
	// over HTTPS: browsers then refuse plain HTTP for all of them for a year.
	// "preload" is never sent: it cannot be undone quickly.
	HSTSIncludeSubdomains bool `yaml:"hsts_include_subdomains"`
}

// NetPolicy returns the outbound connection policy for netguard.SetPolicy.
func (c *Config) NetPolicy() (*netguard.Policy, error) {
	p := &netguard.Policy{AllowPrivate: c.Security.AllowPrivateNetworks}
	for _, s := range c.Security.AllowedNetworks {
		r, err := netguard.ParseRule(s)
		if err != nil {
			return nil, err
		}
		p.Allowed = append(p.Allowed, r)
	}
	return p, nil
}
