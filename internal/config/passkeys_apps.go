// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// AndroidApp is an Android app allowed to use this server's passkeys
// directly, identified like in Digital Asset Links: the package name and the
// SHA-256 fingerprints of its signing certificates (keytool -list -v, "SHA256:"
// line; Google Play App Signing shows its own one in the Play Console).
//
//	auth:
//	  passkeys:
//	    android_apps:
//	      - package_name: org.tuxmail.app
//	        sha256_cert_fingerprints: ["14:6D:E9:...:2B"]
//
// Android's Credential Manager only hands a passkey of mail.example.org to an
// app that mail.example.org lists in /.well-known/assetlinks.json with the
// relation delegate_permission/common.get_login_creds, and it reports the
// app as origin "android:apk-key-hash:<base64url of the fingerprint>". Both are
// derived from this list.
type AndroidApp struct {
	PackageName  string   `yaml:"package_name"`
	Fingerprints []string `yaml:"sha256_cert_fingerprints"`
}

const (
	maxAndroidApps         = 10
	maxAndroidFingerprints = 5
)

// Java package name with at least two segments (Android applicationId rules).
var androidPackageRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// parseCertFingerprint accepts "AB:CD:..." (any case) or 64 hex digits and
// returns the 32 bytes.
func parseCertFingerprint(s string) ([]byte, error) {
	h := strings.ReplaceAll(strings.TrimSpace(s), ":", "")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("%q is not a SHA-256 certificate fingerprint (32 bytes in hex, e.g. 14:6D:E9:...)", s)
	}
	return b, nil
}

func validateAndroidApps(apps []AndroidApp) []error {
	var errs []error
	if len(apps) > maxAndroidApps {
		errs = append(errs, fmt.Errorf("auth.passkeys.android_apps: at most %d apps", maxAndroidApps))
	}
	for i, a := range apps {
		where := fmt.Sprintf("auth.passkeys.android_apps[%d]", i)
		if !androidPackageRe.MatchString(a.PackageName) || len(a.PackageName) > 255 {
			errs = append(errs, fmt.Errorf("%s.package_name: %q is not an Android package name", where, a.PackageName))
		}
		if len(a.Fingerprints) == 0 || len(a.Fingerprints) > maxAndroidFingerprints {
			errs = append(errs, fmt.Errorf("%s.sha256_cert_fingerprints: 1 to %d fingerprints", where, maxAndroidFingerprints))
		}
		for _, f := range a.Fingerprints {
			if _, err := parseCertFingerprint(f); err != nil {
				errs = append(errs, fmt.Errorf("%s.sha256_cert_fingerprints: %w", where, err))
			}
		}
	}
	return errs
}

// AssetLinkStatement is one entry of /.well-known/assetlinks.json.
type AssetLinkStatement struct {
	Relation []string `json:"relation"`
	Target   struct {
		Namespace    string   `json:"namespace"`
		PackageName  string   `json:"package_name"`
		Fingerprints []string `json:"sha256_cert_fingerprints"`
	} `json:"target"`
}

// AndroidAssetLinks returns the Digital Asset Links statements for the
// configured apps (nil when there are none or passkeys are off). Only the
// relation for sign-in credentials is granted: the server's URLs never open in
// the app (no handle_all_urls).
func (c *Config) AndroidAssetLinks() []AssetLinkStatement {
	if !c.PasskeysEnabled() {
		return nil
	}
	var out []AssetLinkStatement
	for _, a := range c.Auth.Passkeys.AndroidApps {
		var st AssetLinkStatement
		st.Relation = []string{"delegate_permission/common.get_login_creds"}
		st.Target.Namespace = "android_app"
		st.Target.PackageName = a.PackageName
		for _, f := range a.Fingerprints {
			b, err := parseCertFingerprint(f)
			if err != nil {
				continue
			}
			parts := make([]string, len(b))
			for i, x := range b {
				parts[i] = fmt.Sprintf("%02X", x)
			}
			st.Target.Fingerprints = append(st.Target.Fingerprints, strings.Join(parts, ":"))
		}
		out = append(out, st)
	}
	return out
}

// PasskeyAppOrigins returns the WebAuthn origins of the configured Android
// apps: "android:apk-key-hash:" + unpadded base64url of the certificate's
// SHA-256, as Android's Credential Manager puts it into clientDataJSON.
func (c *Config) PasskeyAppOrigins() []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range c.Auth.Passkeys.AndroidApps {
		for _, f := range a.Fingerprints {
			b, err := parseCertFingerprint(f)
			if err != nil {
				continue
			}
			o := "android:apk-key-hash:" + base64.RawURLEncoding.EncodeToString(b)
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}
