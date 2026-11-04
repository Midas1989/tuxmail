// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
)

// Settings → Info & Diagnose: the archive holds info, redacted config and
// the logs, without passwords or full e-mail addresses.
func TestDiagnosticsExport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "tuxmail.log")
	if err := os.WriteFile(logFile, []byte("level=INFO msg=login user=anna.berger@example.org password=hunter2 token: abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logFile+".1", []byte("older\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.a.SetDiagnostics(app.DiagnosticsOptions{LogFile: logFile, Config: func() any { return map[string]string{"mode": "test"} }})

	info := must[app.DiagnosticsInfo](t)(f.a.Diagnostics(ctx))
	if info.LogFile != logFile || len(info.Accounts) == 0 || info.OS == "" {
		t.Fatalf("info %+v", info)
	}
	z := must[app.DiagnosticsArchive](t)(f.a.ExportDiagnostics(ctx))
	if !strings.HasSuffix(z.Filename, ".zip") || z.MIMEType != "application/zip" {
		t.Fatalf("archive %q %q", z.Filename, z.MIMEType)
	}
	zr, err := zip.NewReader(bytes.NewReader(z.Data), int64(len(z.Data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, zf := range zr.File {
		r, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		files[zf.Name] = string(b)
	}
	for _, name := range []string{"info.json", "config.json", "logs/tuxmail.log", "logs/tuxmail.log.1"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing %s (have %v)", name, files)
		}
	}
	all := strings.Join([]string{files["info.json"], files["config.json"], files["logs/tuxmail.log"]}, "\n")
	for _, secret := range []string{"hunter2", "abc123", "anna.berger@example.org"} {
		if strings.Contains(all, secret) {
			t.Errorf("archive leaks %q", secret)
		}
	}
	if !strings.Contains(files["logs/tuxmail.log"], "a***@example.org") || !strings.Contains(files["config.json"], `"mode": "test"`) {
		t.Errorf("redaction/config: %s / %s", files["logs/tuxmail.log"], files["config.json"])
	}
	sts := must[[]app.AccountStatus](t)(f.a.SyncStatus(ctx))
	if len(sts) == 0 {
		t.Error("no sync status")
	}
}
