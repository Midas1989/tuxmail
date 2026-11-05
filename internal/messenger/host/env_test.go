// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"slices"
	"testing"
)

// Audit ND-13: plugins get an allow-listed environment, not the secrets of
// the server process.
func TestPluginEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/tux", "LANG=de_DE.UTF-8", "LC_ALL=C", "HTTPS_PROXY=http://proxy:3128", "http_proxy=http://p:1",
		"TMPDIR=/tmp", "SystemRoot=C:\\Windows", "LocalAppData=C:\\Users\\x\\AppData\\Local",
		"TUXMAIL_MATRIX_DEFAULT_HOMESERVER=https://matrix.example.org", "TUXMAIL_TELEGRAM_API_ID=1",
		"TUXMAIL_BACKUP_PASSPHRASE=hunter2", "TUXMAIL_DESKTOP_TEST_TOKEN=0123456789abcdef", "TUXMAIL_OIDC_CLIENT_SECRET=s",
		"AWS_SECRET_ACCESS_KEY=k", "GITHUB_TOKEN=t", "SSH_AUTH_SOCK=/run/agent", "=C:=C:\\dir", "NOEQUALS",
	}
	got := PluginEnv(in, "matrix")
	want := []string{
		"PATH=/usr/bin", "HOME=/home/tux", "LANG=de_DE.UTF-8", "LC_ALL=C", "HTTPS_PROXY=http://proxy:3128", "http_proxy=http://p:1",
		"TMPDIR=/tmp", "SystemRoot=C:\\Windows", "LocalAppData=C:\\Users\\x\\AppData\\Local",
		"TUXMAIL_MATRIX_DEFAULT_HOMESERVER=https://matrix.example.org",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PluginEnv =\n%q\nwant\n%q", got, want)
	}
	if got := PluginEnv([]string{"TUXMAIL_TELEGRAM_API_ID=1", "TUXMAIL_MATRIX_X=2"}, "telegram"); !slices.Equal(got, []string{"TUXMAIL_TELEGRAM_API_ID=1"}) {
		t.Fatalf("other plugin's settings leaked: %q", got)
	}
	if got := PluginEnv([]string{"TUXMAIL_X=1"}, ""); len(got) != 0 {
		t.Fatalf("empty plugin name must not match TUXMAIL_*: %q", got)
	}
}
