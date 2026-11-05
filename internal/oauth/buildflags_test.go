// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package oauth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NC-08 / ND-16: the Google client secret of the desktop registration is
// linked only when a desktop build asks for it (TUXMAIL_LINK_CLIENT_SECRET=1)
// and never into server/Docker builds.
func TestServerBuildFlagsOmitGoogleSecret(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(root, "build", "lib.sh")
	if _, err := os.Stat(lib); err != nil {
		t.Skip("build/lib.sh not found")
	}
	script := `. "$1" >/dev/null 2>&1
printf 'PLAIN:%s\n' "$(go_ldflags)"
printf 'DESKTOP:%s\n' "$(TUXMAIL_LINK_CLIENT_SECRET=1 go_ldflags)"
printf 'SERVER:%s\n' "$(TUXMAIL_LINK_CLIENT_SECRET=1 go_ldflags_server)"`
	cmd := exec.Command(sh, "-c", script, "sh", lib)
	cmd.Dir = filepath.Join(root, "build")
	cmd.Env = append(os.Environ(), "TUXMAIL_VERSION=0.0.0-test", "TUXMAIL_BUILD_GOOGLE_CLIENT_ID=gid", "TUXMAIL_BUILD_GOOGLE_CLIENT_SECRET=S3CRET-value", "SOURCE_DATE_EPOCH=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("lib.sh: %v", err)
	}
	var plain, desktop, server string
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "PLAIN:"):
			plain = l
		case strings.HasPrefix(l, "DESKTOP:"):
			desktop = l
		case strings.HasPrefix(l, "SERVER:"):
			server = l
		}
	}
	if plain == "" || strings.Contains(plain, "S3CRET-value") {
		t.Errorf("a build that does not ask for the secret must not link it: %q", plain)
	}
	if !strings.Contains(desktop, "builtinGoogleClientSecret=S3CRET-value") {
		t.Errorf("desktop flags lost the secret: %q", desktop)
	}
	if server == "" || strings.Contains(server, "S3CRET-value") || strings.Contains(server, "builtinGoogleClientSecret") {
		t.Errorf("server flags: %q", server)
	}
	if !strings.Contains(server, "builtinGoogleClientID=gid") {
		t.Errorf("server flags lost the public client id: %q", server)
	}
	pkg, err := os.ReadFile(filepath.Join(root, "build", "package-server.sh"))
	if err != nil || !strings.Contains(string(pkg), "go_ldflags_server") || strings.Contains(string(pkg), "$(go_ldflags)") {
		t.Errorf("package-server.sh must use go_ldflags_server (err %v)", err)
	}
}
