// SPDX-License-Identifier: AGPL-3.0-or-later

// Package safehttp holds the rules the protocol clients (DAV, EAS, EWS,
// JMAP, Graph/Gmail) share for where credentials may be sent:
//
//   - an endpoint must use https; plain http is accepted for loopback hosts
//     (dev server, tests) and when the caller explicitly confirmed it,
//   - credentials follow a redirect or a server-supplied URL only to the same
//     host, or - over https - to another host of the same registrable domain
//     (Exchange datacenter redirects, iCloud home sets),
//   - a redirect never downgrades https to http.
//
// Audit 2026-11-05 NF-06/NF-07/NF-08/NF-10.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ExtraAllowInsecure is the Account.Extra key ("true") that records the
// user's explicit confirmation to use unencrypted http:// endpoints (EAS,
// JMAP, CalDAV/CardDAV). It is set by the account create/update API when the
// request carries allowInsecure.
const ExtraAllowInsecure = "allowInsecure"

// ErrInsecure is returned for plain-http endpoints that were not confirmed.
var ErrInsecure = errors.New("insecure http:// endpoint refused: credentials would be sent unencrypted")

// ErrForeignHost is returned when a URL leaves the host credentials are bound to.
var ErrForeignHost = errors.New("URL points to a different host than the configured server")

// IsLoopbackHost reports whether host (optionally with port) is localhost or
// a loopback IP literal.
func IsLoopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackURL reports whether u points to a loopback host.
func IsLoopbackURL(u *url.URL) bool { return u != nil && IsLoopbackHost(u.Hostname()) }

// IsInsecure reports whether rawURL is a plain-http URL to a non-loopback host.
func IsInsecure(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") && !IsLoopbackURL(u)
}

// RequireSecure checks that rawURL is an http(s) URL with a host and that it
// is not a plain-http URL to a non-loopback host unless allowInsecure.
func RequireSecure(rawURL string, allowInsecure bool) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL %q", rawURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if allowInsecure || IsLoopbackURL(u) {
			return nil
		}
		return ErrInsecure
	}
	return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
}

var multiLabelSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true,
	"co.at": true, "or.at": true, "ac.at": true,
	"com.au": true, "net.au": true, "org.au": true,
	"co.nz": true, "co.jp": true, "ne.jp": true, "or.jp": true,
	"com.br": true, "com.cn": true, "com.tr": true, "co.za": true, "co.in": true,
	"com.mx": true, "com.ar": true, "com.pl": true,
}

// RegistrableDomain returns the registrable domain of host ("" for IP
// literals, single labels and unknown shapes). It uses a small list of
// two-label public suffixes instead of the full public suffix list; errors of
// that list only make the check stricter or equal to "same host" for IPs.
func RegistrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	n := 2
	if len(labels) >= 3 && multiLabelSuffixes[strings.Join(labels[len(labels)-2:], ".")] {
		n = 3
	}
	if len(labels) < n {
		return ""
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// SameHost reports whether a and b have the same host name and port.
func SameHost(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// CredentialsAllowed reports whether credentials bound to origin may be sent
// to target: same host (and port), or - when both are https - a host of the
// same registrable domain. Scheme downgrades are never allowed (https origin
// to http target), except between loopback hosts.
func CredentialsAllowed(origin, target *url.URL) bool {
	if origin == nil || target == nil {
		return false
	}
	os, ts := strings.ToLower(origin.Scheme), strings.ToLower(target.Scheme)
	if ts != "http" && ts != "https" {
		return false
	}
	if os == "https" && ts == "http" && !IsLoopbackURL(target) {
		return false
	}
	if SameHost(origin, target) {
		return true
	}
	if os != "https" || ts != "https" {
		return false
	}
	od, td := RegistrableDomain(origin.Hostname()), RegistrableDomain(target.Hostname())
	return od != "" && od == td
}

// CheckRedirect returns a http.Client.CheckRedirect that follows at most max
// redirects (default 10) and refuses redirects to targets credentials bound
// to origin may not follow (see CredentialsAllowed) with ErrForeignHost or
// ErrInsecure. If origin is nil the host of the first request is the origin.
func CheckRedirect(origin *url.URL, max int) func(req *http.Request, via []*http.Request) error {
	if max <= 0 {
		max = 10
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("stopped after %d redirects", max)
		}
		o := origin
		if o == nil && len(via) > 0 {
			o = via[0].URL
		}
		if o != nil && !CredentialsAllowed(o, req.URL) {
			if strings.EqualFold(o.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
				return fmt.Errorf("redirect to %s: %w", req.URL.Redacted(), ErrInsecure)
			}
			return fmt.Errorf("redirect to %s: %w", req.URL.Redacted(), ErrForeignHost)
		}
		return nil
	}
}

// ClientWithRedirectPolicy returns a shallow copy of base (http.DefaultClient
// if nil) whose redirects are limited by CheckRedirect(origin, 10).
func ClientWithRedirectPolicy(base *http.Client, origin *url.URL) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	cp := *base
	cp.CheckRedirect = CheckRedirect(origin, 10)
	return &cp
}
