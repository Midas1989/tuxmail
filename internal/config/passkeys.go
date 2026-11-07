// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Passkeys configures passkey (WebAuthn / FIDO2) sign-in of the web UI.
//
// Passkeys are passwordless logins with user verification: the browser asks
// for the device PIN, fingerprint or face, which makes a passkey login two
// factors in one. A passkey session therefore satisfies
// auth.totp_required_for_admins, a password session of the same account does
// not.
//
// The relying party is derived from public_url: the RP ID is its host name
// (no port, no path) and the only accepted origin is scheme://host[:port].
// Changing the host name of public_url makes all registered passkeys unusable
// (they are bound to the RP ID by the authenticator); users must register new
// ones. Browsers only offer WebAuthn in secure contexts, so https is required
// (http://localhost is allowed for development) and IP addresses are never
// valid RP IDs.
type Passkeys struct {
	// Enabled switches passkeys on or off. Unset (the default) means: on when
	// public_url is https with a host name, or http(s)://localhost; off
	// otherwise. Setting it to true for a public_url that cannot work is a
	// configuration error.
	Enabled *bool `yaml:"enabled"`
	// RPName is the name of the service that authenticators show next to the
	// passkey (default "TuxMail"), at most 64 characters.
	RPName string `yaml:"rp_name"`
	// AndroidApps are the Android apps that may sign in with the passkeys of
	// this server directly (Credential Manager, passkeys_apps.go). The server
	// publishes them in /.well-known/assetlinks.json and accepts their
	// android:apk-key-hash origin for the passkey device login. Empty (the
	// default): no app can use the passkeys directly, the app signs in through
	// the browser (device approval) instead.
	AndroidApps []AndroidApp `yaml:"android_apps"`
}

// DefaultPasskeyRPName is shown by authenticators when rp_name is unset.
const DefaultPasskeyRPName = "TuxMail"

const maxPasskeyRPNameRunes = 64

// PasskeyRP returns the WebAuthn relying party derived from public_url: the RP
// ID (host name), the origin (scheme://host[:port], default ports removed as
// browsers serialise them) and whether public_url can host passkeys at all.
func (c *Config) PasskeyRP() (rpID, origin string, ok bool) {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return "", "", false // IP addresses and absolute (trailing-dot) names are not valid RP IDs
	}
	if scheme != "http" && scheme != "https" {
		return "", "", false
	}
	if scheme != "https" && host != "localhost" {
		return "", "", false // secure context required
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	origin = scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	return host, origin, true
}

// PasskeysEnabled reports whether passkey sign-in is available: the explicit
// auth.passkeys.enabled if set, otherwise whether public_url can host passkeys.
func (c *Config) PasskeysEnabled() bool {
	if _, _, ok := c.PasskeyRP(); !ok {
		return false
	}
	if c.Auth.Passkeys.Enabled != nil {
		return *c.Auth.Passkeys.Enabled
	}
	return true
}

// PasskeyRPName returns the configured relying party name or the default.
func (c *Config) PasskeyRPName() string {
	if n := strings.TrimSpace(c.Auth.Passkeys.RPName); n != "" {
		return n
	}
	return DefaultPasskeyRPName
}

// validatePasskeys returns the problems of the auth.passkeys section.
func (c *Config) validatePasskeys() []error {
	var errs []error
	p := c.Auth.Passkeys
	if p.Enabled != nil && *p.Enabled {
		if _, _, ok := c.PasskeyRP(); !ok {
			errs = append(errs, fmt.Errorf("auth.passkeys.enabled: passkeys need public_url with an https host name (or http://localhost for development), got %q", c.PublicURL))
		}
	}
	if n := strings.TrimSpace(p.RPName); n != p.RPName && p.RPName != "" {
		errs = append(errs, fmt.Errorf("auth.passkeys.rp_name: must not start or end with whitespace"))
	} else if utf8.RuneCountInString(p.RPName) > maxPasskeyRPNameRunes {
		errs = append(errs, fmt.Errorf("auth.passkeys.rp_name: at most %d characters", maxPasskeyRPNameRunes))
	} else if strings.IndexFunc(p.RPName, unicode.IsControl) >= 0 {
		errs = append(errs, fmt.Errorf("auth.passkeys.rp_name: must not contain control characters"))
	}
	errs = append(errs, validateAndroidApps(p.AndroidApps)...)
	return errs
}
