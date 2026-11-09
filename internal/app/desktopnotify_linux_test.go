// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotifySendEscapesAndSeparatesOptions(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + out + "; done\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := osNotify(context.Background(), "-e <b>x</b>", "a & <i>b</i>"); err != nil {
		t.Skip("no sh available: ", err)
	}
	b, _ := os.ReadFile(out)
	got := string(b)
	for _, want := range []string{"--\n", "-e &lt;b&gt;x&lt;/b&gt;\n", "a &amp; &lt;i&gt;b&lt;/i&gt;\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
