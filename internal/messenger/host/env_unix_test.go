// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package host

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The started process really gets the filtered environment.
func TestExecLauncherSanitizesEnvironment(t *testing.T) {
	t.Setenv("TUXMAIL_BACKUP_PASSPHRASE", "hunter2")
	t.Setenv("TUXMAIL_FAKE_SETTING", "ok")
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := writePlugin(t, dir, "tuxmail-plugin-fake", 0o755)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n/usr/bin/env > \"$0.env\" 2>/dev/null || env > \"$0.env\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := ExecLauncher{}.Launch(context.Background(), Manifest{Name: "fake", Path: p})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.Wait()
	b, err := os.ReadFile(p + ".env")
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	if strings.Contains(env, "hunter2") || strings.Contains(env, "TUXMAIL_BACKUP_PASSPHRASE") {
		t.Fatalf("the plugin inherited a server secret:\n%s", env)
	}
	if !strings.Contains(env, "TUXMAIL_FAKE_SETTING=ok") || !strings.Contains(env, "PATH=/usr/bin:/bin") {
		t.Fatalf("the plugin lacks its own settings or PATH:\n%s", env)
	}
}
