// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package app_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
)

// NC-04: exports to user-chosen paths hold complete mail, contacts and
// calendars; they must be created 0600 whatever the umask is.
func TestExportFilesArePrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	ctx := context.Background()
	e := newIOEnv(t)
	imp := func(format, file string, target app.ImportTarget) {
		t.Helper()
		j := e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: format, Path: fixtures + file}, Target: target,
			Options: app.ImportOptions{Dedupe: true}}))
		if j.Status != "done" {
			t.Fatalf("import %s: %+v", file, j)
		}
	}
	imp("mbox", "sample.mbox", app.ImportTarget{Kind: "local"})
	imp("vcf", "contacts3.vcf", app.ImportTarget{})
	imp("ics", "calendar.ics", app.ImportTarget{})
	var cal int64
	if err := e.st.DB().QueryRow(`SELECT id FROM calendars`).Scan(&cal); err != nil {
		t.Fatal(err)
	}

	var folders []int64
	rows, err := e.st.DB().Query(`SELECT f.id FROM folders f JOIN accounts a ON a.id = f.account_id WHERE a.kind = 'local'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		folders = append(folders, id)
	}
	rows.Close()
	sel := app.MailSelection{FolderIDs: folders}

	dir := t.TempDir()
	for _, tc := range []struct {
		format string
		req    app.ExportRequest
	}{
		{"mbox", app.ExportRequest{Selection: sel}},
		{"eml", app.ExportRequest{Selection: sel}},
		{"maildir", app.ExportRequest{Selection: sel}},
		{"vcf", app.ExportRequest{VCardVersion: "3.0"}},
		{"csv", app.ExportRequest{}},
		{"ldif", app.ExportRequest{}},
		{"ics", app.ExportRequest{CalendarIDs: []int64{cal}}},
	} {
		out := filepath.Join(dir, "export."+tc.format)
		// A previous, world-readable file at the same path is tightened too.
		if tc.format == "mbox" {
			if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		req := tc.req
		req.Format, req.Path = tc.format, out
		j := e.wait(t)(e.a.StartExport(ctx, req))
		if j.Status != "done" {
			t.Fatalf("%s export: %+v", tc.format, j)
		}
		fi, err := os.Stat(out)
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		if fi.Size() == 0 {
			t.Errorf("%s export is empty", tc.format)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s export mode = %v, want -rw-------", tc.format, fi.Mode().Perm())
		}
	}
}
