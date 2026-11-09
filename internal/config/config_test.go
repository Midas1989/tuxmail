// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const contractYAML = `
listen: "127.0.0.1:8080"
public_url: "https://mail.example.de"
data_dir: "/var/lib/tuxmail"
log: { level: info, format: json, file: "" }
tls: { mode: none, cert_file: "", key_file: "" }
trusted_proxies: ["127.0.0.1/32", "::1/128"]
auth:
  session_lifetime: 720h
  password_min_length: 12
  registration: disabled
  totp_required_for_admins: false
  oidc: { enabled: false, issuer: "", client_id: "", client_secret_file: "", admin_group: "", user_group: "", groups_claim: groups }
limits: { max_upload_mb: 200, login_rate_per_minute: 10 }
security: { allow_private_networks: false, allowed_networks: ["127.0.0.1:8008"] }
`

func TestParseContract(t *testing.T) {
	c, err := Parse([]byte(contractYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" || c.PublicURL != "https://mail.example.de" || c.DataDir != "/var/lib/tuxmail" {
		t.Fatalf("unexpected: %+v", c)
	}
	if c.Auth.SessionLifetime.D() != 720*time.Hour || c.Auth.PasswordMinLength != 12 {
		t.Fatalf("auth: %+v", c.Auth)
	}
	if !c.SecureCookies() || c.PublicOrigin() != "https://mail.example.de" {
		t.Fatal("public url helpers")
	}
	ps, err := c.TrustedPrefixes()
	if err != nil || len(ps) != 2 {
		t.Fatal(ps, err)
	}
	if c.Limits.MaxUploadMB != 200 || c.Limits.LoginRatePerMinute != 10 {
		t.Fatal(c.Limits)
	}
}

func TestDefaultsAndEnv(t *testing.T) {
	t.Setenv(EnvListen, "unix:/run/tuxmail/tuxmail.sock")
	t.Setenv(EnvDataDir, "/srv/tm")
	c, err := Parse([]byte("public_url: http://localhost:8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	if sock, ok := c.UnixSocket(); !ok || sock != "/run/tuxmail/tuxmail.sock" {
		t.Fatal(c.Listen)
	}
	if c.DataDir != "/srv/tm" || c.ServerDB() != "/srv/tm/server.db" {
		t.Fatal(c.DataDir)
	}
	if c.Auth.Registration != "disabled" || c.Auth.OIDC.GroupsClaim != "groups" {
		t.Fatal(c.Auth)
	}
	if c.SecureCookies() {
		t.Fatal("http must not set secure cookies")
	}
}

func TestBackgroundSyncLimits(t *testing.T) {
	c, err := Parse([]byte(contractYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Limits.BackgroundSync || c.Limits.BackgroundSyncMaxUsers != 500 {
		t.Fatalf("defaults: %+v", c.Limits)
	}
	c, err = Parse([]byte("limits: {background_sync: false, background_sync_max_users: 20}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.BackgroundSync || c.Limits.BackgroundSyncMaxUsers != 20 {
		t.Fatalf("explicit: %+v", c.Limits)
	}
}

func TestEmptyFile(t *testing.T) {
	if _, err := Parse(nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"unknown key":   "lisen: x\n",
		"bad listen":    "listen: nope\n",
		"relative sock": "listen: unix:rel.sock\n",
		"bad url":       "public_url: ftp://x\n",
		"rel data":      "data_dir: data\n",
		"bad level":     "log: {level: loud}\n",
		"tls file":      "tls: {mode: file}\n",
		"bad proxy":     "trusted_proxies: [nonsense]\n",
		"short life":    "auth: {session_lifetime: 1s}\n",
		"bad duration":  "auth: {session_lifetime: forever}\n",
		"min len":       "auth: {password_min_length: 4}\n",
		"registration":  "auth: {registration: open}\n",
		"oidc":          "auth: {oidc: {enabled: true}}\n",
		"limits":        "limits: {max_upload_mb: 0}\n",
		"bg max users":  "limits: {background_sync_max_users: 0}\n",
		"bg sync type":  "limits: {background_sync: maybe}\n",
		"bad network":   "security: {allowed_networks: [intranet.example]}\n",
		"ai max level":  "ai: {max_level: everything}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadFileAndSecret(t *testing.T) {
	dir := t.TempDir()
	sec := filepath.Join(dir, "secret")
	os.WriteFile(sec, []byte("s3cret\n"), 0o600)
	p := filepath.Join(dir, "config.yaml")
	y := strings.Replace(contractYAML, `oidc: { enabled: false, issuer: "", client_id: "", client_secret_file: ""`,
		`oidc: { enabled: true, issuer: "https://id.example.de", client_id: "tuxmail", client_secret_file: "`+sec+`"`, 1)
	os.WriteFile(p, []byte(y), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.OIDCClientSecret()
	if err != nil || s != "s3cret" {
		t.Fatal(s, err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("expected error")
	}
}

func TestSecurityPolicy(t *testing.T) {
	c, err := Parse([]byte("security: {allow_private_networks: true, allowed_networks: [\"127.0.0.1:8008\", \"10.0.0.0/8\"]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.NetPolicy()
	if err != nil || !p.AllowPrivate || len(p.Allowed) != 2 {
		t.Fatal(p, err)
	}
	d := Default()
	if p, _ := d.NetPolicy(); p.AllowPrivate || len(p.Allowed) != 0 {
		t.Fatal("default policy must block private networks", p)
	}
}
