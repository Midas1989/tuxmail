// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import "github.com/Midas1989/Mailclient/internal/netguard"

// Security configures the outbound connection policy of the server mode
// (SSRF protection, internal/netguard). By default users can only make the
// server connect to public addresses: loopback, private networks,
// link-local addresses and cloud metadata endpoints are refused for every
// user-configured destination (mail, calendar, cloud and messenger
// servers, ICS subscriptions, webhooks, AI providers, Web Push endpoints,
// autoconfig, WKD/VKS).
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
