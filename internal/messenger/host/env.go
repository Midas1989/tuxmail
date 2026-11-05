// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import "strings"

// pluginEnvExact are the variables (upper case) a plugin gets from the
// host's environment: the search path and home/temp folders, the locale,
// the proxy and TLS-root settings that network access needs and the few
// Windows system variables the Go runtime and Windows DLLs expect. Secrets
// of the server (TUXMAIL_BACKUP_PASSPHRASE, OIDC and push credentials, ...)
// are not passed on (audit ND-13).
var pluginEnvExact = map[string]bool{
	"PATH": true, "HOME": true, "USERPROFILE": true, "TMP": true, "TEMP": true, "TMPDIR": true,
	"LANG": true, "LANGUAGE": true, "TZ": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"JAVA_HOME":       true, // signal-cli
	"XDG_RUNTIME_DIR": true,
	"SYSTEMROOT":      true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true, "USERNAME": true,
}

// PluginEnv filters a process environment (KEY=VALUE entries) for the
// plugin named plugin: the allow-list pluginEnvExact, LC_* locale variables
// and the TUXMAIL_<PLUGIN>_* settings of that plugin only.
func PluginEnv(environ []string, plugin string) []string {
	own := "TUXMAIL_" + strings.ToUpper(plugin) + "_"
	var out []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue // also the "=C:=C:\dir" entries of Windows
		}
		u := strings.ToUpper(k)
		if pluginEnvExact[u] || strings.HasPrefix(u, "LC_") || (plugin != "" && strings.HasPrefix(u, own)) {
			out = append(out, kv)
		}
	}
	return out
}
