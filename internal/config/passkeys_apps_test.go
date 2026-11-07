// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"strings"
	"testing"
)

func TestPasskeyAndroidApps(t *testing.T) {
	// SHA-256 fingerprint of a signing certificate, as keytool prints it.
	const fp = "14:6D:E9:83:C5:73:06:50:D8:EE:B9:95:2F:34:FC:64:16:A0:83:42:E6:1D:BE:A8:8A:04:96:B2:3F:CF:44:E5"
	c, err := Parse([]byte("public_url: https://mail.example.org\nauth:\n  passkeys:\n    android_apps:\n" +
		"      - package_name: org.tuxmail.app\n        sha256_cert_fingerprints: [\"" + fp + "\", \"" + strings.ToLower(strings.ReplaceAll(fp, ":", "")) + "\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Both spellings are the same certificate: one origin, two equal fingerprints in the statement.
	if o := c.PasskeyAppOrigins(); len(o) != 1 || o[0] != "android:apk-key-hash:FG3pg8VzBlDY7rmVLzT8ZBagg0LmHb6oigSWsj_PROU" {
		t.Fatalf("origins: %v", o)
	}
	st := c.AndroidAssetLinks()
	if len(st) != 1 || st[0].Target.PackageName != "org.tuxmail.app" || st[0].Target.Fingerprints[0] != fp || st[0].Target.Fingerprints[1] != fp {
		t.Fatalf("statements: %+v", st)
	}
	// Without apps, or with passkeys off: nothing published.
	c, _ = Parse([]byte("public_url: https://mail.example.org\n"))
	if c.AndroidAssetLinks() != nil || c.PasskeyAppOrigins() != nil {
		t.Fatal("default must publish nothing")
	}
	c, _ = Parse([]byte("public_url: https://mail.example.org\nauth: {passkeys: {enabled: false, android_apps: [{package_name: a.b, sha256_cert_fingerprints: [\"" + fp + "\"]}]}}\n"))
	if c.AndroidAssetLinks() != nil {
		t.Fatal("published although passkeys are off")
	}
}

func TestPasskeyAndroidAppsValidation(t *testing.T) {
	const fp = "14:6D:E9:83:C5:73:06:50:D8:EE:B9:95:2F:34:FC:64:16:A0:83:42:E6:1D:BE:A8:8A:04:96:B2:3F:CF:44:E5"
	for name, apps := range map[string]string{
		"package without dot":  `[{package_name: tuxmail, sha256_cert_fingerprints: ["` + fp + `"]}]`,
		"package with dash":    `[{package_name: org.tux-mail.app, sha256_cert_fingerprints: ["` + fp + `"]}]`,
		"no fingerprint":       `[{package_name: org.tuxmail.app}]`,
		"short fingerprint":    `[{package_name: org.tuxmail.app, sha256_cert_fingerprints: ["14:6D:E9"]}]`,
		"sha1 fingerprint":     `[{package_name: org.tuxmail.app, sha256_cert_fingerprints: ["14:6D:E9:83:C5:73:06:50:D8:EE:B9:95:2F:34:FC:64:16:A0:83:42"]}]`,
		"not hex":              `[{package_name: org.tuxmail.app, sha256_cert_fingerprints: ["` + strings.Repeat("ZZ:", 31) + `ZZ"]}]`,
		"unknown key":          `[{package: org.tuxmail.app}]`,
		"too many fingerprint": `[{package_name: org.tuxmail.app, sha256_cert_fingerprints: ["` + strings.Repeat(fp+`","`, 5) + fp + `"]}]`,
	} {
		if _, err := Parse([]byte("public_url: https://mail.example.org\nauth: {passkeys: {android_apps: " + apps + "}}\n")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
