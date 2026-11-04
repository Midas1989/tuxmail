// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ioCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLIImportBackupRestore(t *testing.T) {
	t.Setenv("TUXMAIL_BACKUP_PASSPHRASE", "")
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	src := filepath.Join("..", "..", "internal", "importexport", "testdata", "sample.mbox")
	if code, out, errs := ioCLI(t, "", "import", "--data-dir", data, "--format", "mbox", "--source", src, "--quiet"); code != 0 || !strings.Contains(out, "messages: 2") {
		t.Fatalf("import: %d %s %s", code, out, errs)
	}
	arc := filepath.Join(dir, "b.txmb")
	if code, _, errs := ioCLI(t, "secret\n", "backup", "--data-dir", data, "--out", arc, "--passphrase-stdin"); code != 0 {
		t.Fatalf("backup: %s", errs)
	}
	if code, out, _ := ioCLI(t, "secret\n", "restore", "--in", arc, "--inspect", "--passphrase-stdin"); code != 0 || !strings.Contains(out, "Local Folders") {
		t.Fatalf("inspect: %s", out)
	}
	if code, _, errs := ioCLI(t, "wrong\n", "restore", "--in", arc, "--verify", "--passphrase-stdin"); code == 0 || !strings.Contains(errs, "wrong passphrase") {
		t.Fatalf("wrong passphrase: %d %s", code, errs)
	}
	restored := filepath.Join(dir, "restored")
	if code, out, errs := ioCLI(t, "secret\n", "restore", "--in", arc, "--data-dir", restored, "--passphrase-stdin"); code != 0 || !strings.Contains(out, "2 messages") {
		t.Fatalf("restore: %s %s", out, errs)
	}
	out := filepath.Join(dir, "export.mbox")
	if code, _, errs := ioCLI(t, "", "export", "--data-dir", restored, "--format", "mbox", "--query", "First", "--out", out, "--quiet"); code != 0 {
		t.Fatalf("export: %s", errs)
	}
	if b, _ := os.ReadFile(out); !bytes.Contains(b, []byte("Subject: First")) {
		t.Fatalf("export content: %s", b)
	}
	if code, _, _ := ioCLI(t, "", "import", "--data-dir", data); code != 2 {
		t.Fatalf("missing flags must be a usage error, got %d", code)
	}
}
