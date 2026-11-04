// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package host

import (
	"os"
	"path/filepath"
	"testing"
)

// writePlugin creates an executable plugin with exact permissions (no umask).
func writePlugin(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch \"$0.ran\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// AUDIT-D D-06: plugins that other users can plant or replace are not run.
func TestDiscoverRejectsUntrustedPlugins(t *testing.T) {
	// World-writable directory without sticky bit (e.g. a shared folder).
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, shared, "tuxmail-plugin-evil", 0o755)

	// World-writable plugin file in a private directory.
	private := t.TempDir()
	writePlugin(t, private, "tuxmail-plugin-loose", 0o757)
	writePlugin(t, private, "tuxmail-plugin-good", 0o755)

	// Sticky world-writable directory (like /tmp): our own file is fine.
	sticky := t.TempDir()
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, sticky, "tuxmail-plugin-sticky", 0o755)

	got := map[string]bool{}
	for _, m := range Discover(shared, private, sticky) {
		got[m.Name] = true
	}
	if got["evil"] || got["loose"] || !got["good"] || !got["sticky"] {
		t.Fatalf("discover: %v", got)
	}
}

func TestDiscoverRejectsForeignOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to chown")
	}
	dir := t.TempDir()
	p := writePlugin(t, dir, "tuxmail-plugin-foreign", 0o755)
	if err := os.Chown(p, 4242, 4242); err != nil {
		t.Skip(err)
	}
	if ms := Discover(dir); len(ms) != 0 {
		t.Fatalf("plugin of another user discovered: %+v", ms)
	}
}

func TestIsGoRunBinary(t *testing.T) {
	tmp := os.TempDir()
	for p, want := range map[string]bool{
		filepath.Join(tmp, "go-build123", "b001", "exe", "tuxmail"): true,
		"/usr/bin/tuxmail":                                       false,
		filepath.Join(tmp, "TuxMail", "tuxmail"):                 false,
		filepath.Join(filepath.Dir(tmp), "go-build1", "tuxmail"): false,
	} {
		if got := isGoRunBinary(p); got != want {
			t.Errorf("isGoRunBinary(%s) = %v, want %v", p, got, want)
		}
	}
}

// An installed binary does not look into ./bin/plugins of the working
// directory (e.g. a cloned repository or a download folder).
func TestDefaultDirsNoWorkingDirectory(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("TUXMAIL_PLUGIN_DIR", "")
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	for _, d := range DefaultDirs() {
		if d == filepath.Join(wd, "bin", "plugins") && !isGoRunBinary(exe) {
			t.Fatalf("working directory searched by %s: %v", exe, DefaultDirs())
		}
	}
}
