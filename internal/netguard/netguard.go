// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netguard restricts outbound connections of the server mode
// (SSRF protection, docs/security/AUDIT-A-server.md finding A-02).
//
// In server mode, users configure many remote endpoints themselves: mail
// servers (IMAP, SMTP, POP3, ManageSieve, EAS, EWS, JMAP), CalDAV/CardDAV,
// cloud and password-manager servers, ICS subscriptions, workflow
// webhooks, AI provider base URLs, Web Push endpoints, autoconfig and
// WKD/VKS lookups. Without restrictions any logged-in user could make the
// server connect to loopback services, the internal network or cloud
// metadata endpoints. A Policy installed with SetPolicy is enforced
//
//   - for every dial of http.DefaultTransport (and therefore
//     http.DefaultClient, every http.Client without its own Transport and
//     every clone of the default transport) and
//   - for every net.Dialer that was passed through Dialer (mail protocols).
//
// The check runs in net.Dialer.Control, i.e. after DNS resolution and for
// every address actually dialed, which also defeats DNS rebinding.
//
// Without a policy (desktop mode, the default) nothing is restricted.
// Endpoints configured by the administrator (OIDC issuer, Gotenberg) use
// TrustedClient or Trusted and are not restricted.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// ErrBlocked is returned (wrapped) when a destination is not allowed.
var ErrBlocked = errors.New("netguard: destination address not allowed in server mode (see security.allow_private_networks / security.allowed_networks)")

// Rule allows a network prefix, optionally limited to one port.
type Rule struct {
	Prefix netip.Prefix
	Port   uint16 // 0 = any port
}

func (r Rule) String() string {
	if r.Port == 0 {
		return r.Prefix.String()
	}
	return "[" + r.Prefix.String() + "]:" + strconv.Itoa(int(r.Port))
}

func (r Rule) match(ap netip.AddrPort) bool {
	return r.Prefix.Contains(ap.Addr()) && (r.Port == 0 || r.Port == ap.Port())
}

// ParseRule parses an allowlist entry: an address ("192.168.1.10",
// "fd00::5"), a prefix ("10.0.0.0/8"), each optionally with a port
// ("127.0.0.1:8008", "10.0.0.0/8:993", "[::1]:8008", "[fd00::/8]:993").
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	host, port := s, ""
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.LastIndex(s, "]")
		if end < 0 {
			return Rule{}, fmt.Errorf("invalid network %q", s)
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest != "" {
			p, ok := strings.CutPrefix(rest, ":")
			if !ok {
				return Rule{}, fmt.Errorf("invalid network %q", s)
			}
			port = p
		}
	case strings.Count(s, ":") == 1: // IPv4 (or IPv4 prefix) with port
		host, port, _ = strings.Cut(s, ":")
	}
	var r Rule
	if p, err := netip.ParsePrefix(host); err == nil {
		r.Prefix = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmapBits(p.Addr())).Masked()
	} else if a, err := netip.ParseAddr(host); err == nil {
		a = a.Unmap()
		r.Prefix = netip.PrefixFrom(a, a.BitLen())
	} else {
		return Rule{}, fmt.Errorf("invalid network %q (expected an IP address or CIDR prefix, optionally with :port)", s)
	}
	if !r.Prefix.IsValid() {
		return Rule{}, fmt.Errorf("invalid network %q", s)
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return Rule{}, fmt.Errorf("invalid port in network %q", s)
		}
		r.Port = uint16(n)
	}
	return r, nil
}

func unmapBits(a netip.Addr) int {
	if a.Is4In6() {
		return 96
	}
	return 0
}

// Policy decides which destinations may be contacted.
type Policy struct {
	// AllowPrivate permits private networks (RFC 1918, unique local IPv6,
	// carrier-grade NAT) and loopback, e.g. for mail servers in the same
	// LAN or on the same host. Link-local addresses (including the cloud
	// metadata service 169.254.169.254) stay blocked unless listed in Allowed.
	AllowPrivate bool
	// Allowed are always permitted (also link-local or loopback).
	Allowed []Rule
}

// Check returns an error wrapping ErrBlocked if ap must not be contacted.
func (p *Policy) Check(ap netip.AddrPort) error {
	if p == nil {
		return nil
	}
	a := ap.Addr().Unmap()
	ap = netip.AddrPortFrom(a, ap.Port())
	for _, r := range p.Allowed {
		if r.match(ap) {
			return nil
		}
	}
	switch classify(a) {
	case classPublic:
		return nil
	case classPrivate:
		if p.AllowPrivate {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrBlocked, ap)
}

type class int

const (
	classPublic  class = iota
	classPrivate       // private networks and loopback
	classBlocked       // link-local, metadata, multicast, reserved ...
)

var (
	privatePrefixes = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7", "127.0.0.0/8", "::1/128")
	// blockedPrefixes are never contacted unless explicitly allowed.
	blockedPrefixes = mustPrefixes(
		"0.0.0.0/8", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/3", // multicast, reserved (240/4) and broadcast
		"::/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"fe80::/10", "fec0::/10", "ff00::/8",
		"fd00:ec2::254/128",  // AWS IMDS over IPv6 (inside fc00::/7)
		"168.63.129.16/32",   // Azure WireServer / IMDS helper (public range)
		"100.100.100.200/32", // Alibaba Cloud metadata (inside 100.64.0.0/10)
		"::/96",              // IPv4-compatible (deprecated); ::1 is handled in classify
		"::ffff:0:0:0/96",    // IPv4-translated (SIIT)
	)
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	loopback6 = netip.MustParseAddr("::1")
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func classify(a netip.Addr) class {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" {
		return classBlocked
	}
	if nat64.Contains(a) { // NAT64: judge the embedded IPv4 address
		b := a.As16()
		return classify(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if a == loopback6 {
		return classPrivate
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return classBlocked
		}
	}
	for _, p := range privatePrefixes {
		if p.Contains(a) {
			return classPrivate
		}
	}
	if a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return classBlocked
	}
	if a.IsLoopback() || a.IsPrivate() {
		return classPrivate
	}
	return classPublic
}

// IsPublic reports whether a is a public unicast address (no policy needed).
func IsPublic(a netip.Addr) bool { return classify(a) == classPublic }

var current atomic.Pointer[Policy]

// SetPolicy installs (or with nil removes) the process-wide policy. The
// server calls it once at startup.
func SetPolicy(p *Policy) { current.Store(p) }

// Current returns the installed policy (nil: unrestricted).
func Current() *Policy { return current.Load() }

var messenger atomic.Pointer[Policy]

// SetMessengerPolicy installs the policy handed to messenger plugins (the
// general policy plus allowances that apply to the plugins only, e.g. the
// local Matrix homeserver). nil removes it.
func SetMessengerPolicy(p *Policy) { messenger.Store(p) }

// MessengerPolicy returns the policy for messenger plugins and the
// homeserver pre-check; without a dedicated one it is the general policy.
func MessengerPolicy() *Policy {
	if p := messenger.Load(); p != nil {
		return p
	}
	return current.Load()
}

// Control is a net.Dialer Control function enforcing the current policy.
func Control(network, address string, _ syscall.RawConn) error {
	p := current.Load()
	if p == nil {
		return nil
	}
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return nil // unix sockets etc. are not network destinations
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	return p.Check(ap)
}

// Dialer adds the policy check to d (keeping an existing Control) and
// returns d.
func Dialer(d *net.Dialer) *net.Dialer {
	prev := d.Control
	d.Control = func(network, address string, c syscall.RawConn) error {
		if err := Control(network, address, c); err != nil {
			return err
		}
		if prev != nil {
			return prev(network, address, c)
		}
		return nil
	}
	return d
}

// DialContext is a guarded replacement for net.Dialer{}.DialContext.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return Dialer(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
}

type trustedKey struct{}

// Trusted marks ctx for connections to administrator-configured endpoints;
// http.DefaultTransport skips the policy for requests with this context.
func Trusted(ctx context.Context) context.Context { return context.WithValue(ctx, trustedKey{}, true) }

func isTrusted(ctx context.Context) bool { v, _ := ctx.Value(trustedKey{}).(bool); return v }

var (
	plainDialer   = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	guardedDialer = Dialer(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})

	proxyOnce  sync.Once
	proxyAddrs map[string]bool
	envProxy   func(*url.URL) (*url.URL, error)
)

func loadProxies() {
	cfg := httpproxy.FromEnvironment()
	envProxy = cfg.ProxyFunc()
	proxyAddrs = map[string]bool{}
	for _, raw := range []string{cfg.HTTPProxy, cfg.HTTPSProxy} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			if u, err = url.Parse("http://" + raw); err != nil {
				continue
			}
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"https": "443", "socks5": "1080"}[u.Scheme]
			if port == "" {
				port = "80"
			}
		}
		proxyAddrs[strings.ToLower(net.JoinHostPort(u.Hostname(), port))] = true
	}
}

// dialDefault is the DialContext of http.DefaultTransport: the configured
// environment proxy itself may be internal (it is chosen by the
// administrator), everything else is checked.
func dialDefault(ctx context.Context, network, addr string) (net.Conn, error) {
	proxyOnce.Do(loadProxies)
	// A destination equal to the proxy address is refused by proxyDefault
	// for direct connections, so this only skips the check for dials the
	// transport opens to the proxy itself.
	if isTrusted(ctx) || proxyAddrs[strings.ToLower(addr)] {
		return plainDialer.DialContext(ctx, network, addr)
	}
	return guardedDialer.DialContext(ctx, network, addr)
}

// proxyDefault wraps http.ProxyFromEnvironment: when a request goes
// through the environment proxy the proxy resolves the host, so the
// target is checked here (best effort, see residual risks).
func proxyDefault(req *http.Request) (*url.URL, error) {
	proxyOnce.Do(loadProxies)
	u, err := http.ProxyFromEnvironment(req)
	if err != nil {
		return u, err
	}
	if u == nil {
		// Direct connection: dialDefault does not check dials to the proxy
		// address, so a user URL pointing at the proxy (e.g. matched by
		// NO_PROXY) must not be dialed directly.
		if current.Load() != nil && !isTrusted(req.Context()) &&
			proxyAddrs[strings.ToLower(net.JoinHostPort(req.URL.Hostname(), strconv.Itoa(int(portOf(req.URL)))))] {
			return nil, fmt.Errorf("%w: %s (proxy address)", ErrBlocked, req.URL.Host)
		}
		return nil, nil
	}
	if p := current.Load(); p != nil && !isTrusted(req.Context()) {
		if err := CheckHost(req.Context(), req.URL.Hostname(), portOf(req.URL)); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func portOf(u *url.URL) uint16 {
	if p, err := strconv.ParseUint(u.Port(), 10, 16); err == nil {
		return uint16(p)
	}
	if u.Scheme == "https" || u.Scheme == "wss" {
		return 443
	}
	return 80
}

// CheckHost resolves host and checks all its addresses against the current
// policy (for paths where the dial itself cannot be checked).
func CheckHost(ctx context.Context, host string, port uint16) error {
	return CheckHostPolicy(ctx, current.Load(), host, port)
}

// CheckHostPolicy is CheckHost against an explicit policy (nil: allowed).
func CheckHostPolicy(ctx context.Context, p *Policy, host string, port uint16) error {
	if p == nil {
		return nil
	}
	host = strings.Trim(host, "[]")
	if a, err := netip.ParseAddr(host); err == nil {
		return p.Check(netip.AddrPortFrom(a, port))
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := p.Check(netip.AddrPortFrom(a, port)); err != nil {
			return err
		}
	}
	return nil
}

// TrustedClient returns a client for administrator-configured endpoints
// that is not subject to the policy.
func TrustedClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = plainDialer.DialContext
	tr.Proxy = http.ProxyFromEnvironment
	return &http.Client{Transport: tr, Timeout: timeout}
}

// The default transport is guarded from program start; without a policy
// the check is a no-op, so desktop mode is unaffected.
func init() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.DialContext = dialDefault
		t.Proxy = proxyDefault
	}
}
