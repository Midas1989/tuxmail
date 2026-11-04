// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

const fixtures = "../importexport/testdata/"

type ioEnv struct {
	a   *app.App
	st  *store.Store
	dir string
}

func newIOEnv(t *testing.T) *ioEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS, Now: time.Now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return &ioEnv{a: a, st: st, dir: dir}
}

func (e *ioEnv) wait(t *testing.T) func(app.IOJob, error) app.IOJob {
	return func(job app.IOJob, err error) app.IOJob { t.Helper(); return e.waitJob(t, job, err) }
}

func (e *ioEnv) waitJob(t *testing.T, job app.IOJob, err error) app.IOJob {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j, err := e.a.GetIOJob(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != "running" {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return app.IOJob{}
}

func (e *ioEnv) count(t *testing.T, q string) int {
	var n int
	if err := e.st.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportMboxTreeDedupeAndExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	req := app.ImportRequest{Source: app.IOSource{Format: "mbox", Path: fixtures + "tbprofile/Mail/Local Folders"},
		Target: app.ImportTarget{Kind: "local"}, Options: app.ImportOptions{KeepStructure: true, Dedupe: true, KeepFlags: true, KeepDates: true, MboxVariant: "mboxo"}}
	j := e.wait(t)(e.a.StartImport(ctx, req))
	if j.Status != "done" || j.Summary["messages"] != 4 {
		t.Fatalf("import: %+v", j)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM folders f JOIN accounts a ON a.id = f.account_id WHERE a.kind = 'local'`); n != 2 {
		t.Fatalf("folders: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE flags & 1 = 1`); n != 4 {
		t.Fatalf("seen flags: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE conversation_id IS NOT NULL`); n != 4 {
		t.Fatalf("threaded: %d", n)
	}
	if ids, err := e.st.SearchMessages(ctx, "quoted", 10); err != nil || len(ids) == 0 {
		t.Fatalf("full-text index: %v %v", ids, err)
	}
	// The same import again: everything is a duplicate.
	j = e.wait(t)(e.a.StartImport(ctx, req))
	if j.Summary["messages"] != 0 || j.Skipped != 4 {
		t.Fatalf("dedupe: %+v", j)
	}
	// Export the local account as mbox and import it elsewhere.
	var folders []int64
	rows, _ := e.st.DB().Query(`SELECT f.id FROM folders f JOIN accounts a ON a.id = f.account_id WHERE a.kind = 'local'`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		folders = append(folders, id)
	}
	rows.Close()
	out := filepath.Join(t.TempDir(), "out.mbox")
	j = e.wait(t)(e.a.StartExport(ctx, app.ExportRequest{Format: "mbox", Selection: app.MailSelection{FolderIDs: folders}, Path: out}))
	if j.Status != "done" || j.Summary["messages"] != 4 {
		t.Fatalf("export: %+v", j)
	}
	e2 := newIOEnv(t)
	j = e2.wait(t)(e2.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "mbox", Path: out}, Target: app.ImportTarget{Kind: "local"},
		Options: app.ImportOptions{Dedupe: true, KeepFlags: true}}))
	if j.Summary["messages"] != 4 {
		t.Fatalf("re-import: %+v", j)
	}
	if n := e2.count(t, `SELECT COUNT(*) FROM messages WHERE flags & 1 = 1`); n != 4 {
		t.Fatalf("flags after round trip: %d", n)
	}
	// Bodies are equal.
	b1 := bodies(t, e)
	b2 := bodies(t, e2)
	for k, v := range b1 {
		if strings.TrimRight(b2[k], "\n") != strings.TrimRight(v, "\n") {
			t.Fatalf("message %s differs:\n%q\n%q", k, v, b2[k])
		}
	}
	// EML and Maildir exports produce downloads.
	for _, f := range []string{"eml", "maildir"} {
		j = e.wait(t)(e.a.StartExport(ctx, app.ExportRequest{Format: f, Selection: app.MailSelection{Query: "quoted"}}))
		if j.Status != "done" || j.Download == "" {
			t.Fatalf("%s export: %+v", f, j)
		}
		d, err := e.a.OpenIODownload(ctx, j.Download)
		if err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(d.Path); err != nil || st.Size() == 0 {
			t.Fatalf("%s download empty", f)
		}
	}
}

func bodies(t *testing.T, e *ioEnv) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := e.st.DB().Query(`SELECT message_id_hdr, blob_hash FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bs, _ := blob.New(filepath.Join(e.dir, "blobs"))
	for rows.Next() {
		var id, h string
		rows.Scan(&id, &h)
		rc, err := bs.Open(h)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[id] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	return out
}

func TestImportResume(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	req := app.ImportRequest{Source: app.IOSource{Format: "eml", Path: fixtures + "emldir"}, Target: app.ImportTarget{Kind: "local"},
		Options: app.ImportOptions{KeepStructure: true}}
	// Simulate an import interrupted after the first message.
	cp := map[string]any{"job": app.IOJob{ID: "job_interrupted", Kind: "import", Format: "eml", Status: "running", Done: 1}, "request": req}
	b, _ := json.Marshal(cp)
	os.MkdirAll(filepath.Join(e.dir, "io", "jobs"), 0o700)
	os.WriteFile(filepath.Join(e.dir, "io", "jobs", "job_interrupted.json"), b, 0o600)
	jobs, _ := e.a.ListIOJobs(ctx)
	if len(jobs) != 1 || !jobs[0].Resumable {
		t.Fatalf("resumable job not listed: %+v", jobs)
	}
	j := e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{ResumeJobID: "job_interrupted"}))
	if j.Status != "done" || j.Summary["messages"] != 1 || j.Done != 2 || j.Failed != 1 {
		t.Fatalf("resume: %+v", j)
	}
	if jobs, _ := e.a.ListIOJobs(ctx); len(jobs) != 1 {
		t.Fatalf("checkpoint not removed: %d", len(jobs))
	}
}

func TestImportCorruptAndServerPaths(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	j := e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "mbox", Path: fixtures + "corrupt.mbox"}, Target: app.ImportTarget{Kind: "local"}}))
	if j.Status != "done" || j.Failed != 1 {
		t.Fatalf("corrupt mbox: %+v", j)
	}
	j = e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "ics", Path: fixtures + "corrupt.ics"}}))
	if j.Status != "failed" {
		t.Fatalf("corrupt ics: %+v", j)
	}
	if _, err := e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "mbox", Path: "/does/not/exist"}}); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestPIMImportExport(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	j := e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "ics", Path: fixtures + "calendar.ics"}}))
	if j.Status != "done" || j.Summary["events"] != 3 {
		t.Fatalf("ics: %+v", j)
	}
	var cal int64
	e.st.DB().QueryRow(`SELECT id FROM calendars`).Scan(&cal)
	if n := e.count(t, `SELECT COUNT(*) FROM cal_instances`); n < 6 {
		t.Fatalf("instances: %d", n)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{{"skip", 3}, {"overwrite", 3}, {"copy", 6}} {
		mode, want := tc.mode, tc.want
		j = e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "ics", Path: fixtures + "calendar.ics"},
			Target: app.ImportTarget{CalendarID: cal}, Options: app.ImportOptions{DuplicateMode: mode}}))
		if n := e.count(t, `SELECT COUNT(*) FROM cal_objects WHERE calendar_id = `+i64s(cal)); n != want || j.Summary["duplicates"] != 3 {
			t.Fatalf("%s: %d objects, %+v", mode, n, j.Summary)
		}
	}
	out := filepath.Join(t.TempDir(), "cal.ics")
	j = e.wait(t)(e.a.StartExport(ctx, app.ExportRequest{Format: "ics", CalendarIDs: []int64{cal}, From: "2026-11-20", To: "2026-12-31", Path: out}))
	if j.Summary["events"] != 4 { // 2 recurring (original + copy) + 2 December
		t.Fatalf("range export: %+v", j.Summary)
	}

	for _, f := range []string{"contacts3.vcf", "contacts21.vcf", "outlook.csv", "contacts.ldif"} {
		format := strings.TrimPrefix(filepath.Ext(f), ".")
		j = e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: format, Path: fixtures + f}}))
		if j.Status != "done" || j.Summary["contacts"] == 0 {
			t.Fatalf("%s: %+v", f, j)
		}
	}
	p, err := e.a.InspectImport(ctx, app.IOSource{Format: "csv", Path: fixtures + "google.csv"})
	if err != nil || p.Preset != "google" || len(p.Mapping) != len(p.Header) || p.Items != 1 {
		t.Fatalf("inspect csv: %+v %v", p, err)
	}
	total := e.count(t, `SELECT COUNT(*) FROM contacts`)
	for _, f := range []string{"vcf", "csv", "ldif"} {
		out := filepath.Join(t.TempDir(), "c."+f)
		j = e.wait(t)(e.a.StartExport(ctx, app.ExportRequest{Format: f, Path: out, VCardVersion: "3.0"}))
		if j.Summary["contacts"] != total {
			t.Fatalf("%s export: %+v", f, j.Summary)
		}
		e2 := newIOEnv(t)
		j = e2.wait(t)(e2.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: f, Path: out}}))
		// Contacts with the same address are merged in one address book.
		if got := e2.count(t, `SELECT COUNT(*) FROM contacts`); got < 3 || got > total {
			t.Fatalf("%s round trip: %d != %d", f, got, total)
		}
	}
}

func i64s(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestThunderbirdImport(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	p, err := e.a.InspectImport(ctx, app.IOSource{Format: "thunderbird", Path: fixtures + "tbprofile"})
	if err != nil || p.Profile == nil || len(p.Profile.Accounts) != 2 {
		t.Fatalf("inspect: %+v %v", p, err)
	}
	j := e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "thunderbird", Path: fixtures + "tbprofile"},
		Options: app.ImportOptions{TBAccounts: true, TBMail: true, Dedupe: true, KeepFlags: true}}))
	if j.Status != "done" || j.Summary["accounts"] != 1 || j.Summary["messages"] != 4 {
		t.Fatalf("thunderbird: %+v", j)
	}
	accs, _ := e.st.ListAccounts(ctx)
	if len(accs) != 2 || accs[0].Incoming == nil || accs[0].Incoming.Host != "imap.example.org" {
		t.Fatalf("accounts: %+v", accs)
	}
	ids, _ := e.st.ListIdentities(ctx, accs[0].ID)
	if len(ids) != 1 || ids[0].ReplyTo != "reply@example.org" || ids[0].DefaultSignatureID == 0 {
		t.Fatalf("identity: %+v", ids)
	}
}

func TestBackupRestoreViaApp(t *testing.T) {
	ctx := context.Background()
	e := newIOEnv(t)
	e.wait(t)(e.a.StartImport(ctx, app.ImportRequest{Source: app.IOSource{Format: "mbox", Path: fixtures + "sample.mbox"}, Target: app.ImportTarget{Kind: "local"}}))
	j := e.wait(t)(e.a.CreateBackup(ctx, app.BackupRequest{Scope: backup.Scope{Mode: backup.ModeLocal}, Passphrase: "pw"}))
	if j.Status != "done" || j.Download == "" {
		t.Fatalf("backup: %+v", j)
	}
	d, _ := e.a.OpenIODownload(ctx, j.Download)
	raw, _ := os.ReadFile(d.Path)

	e2 := newIOEnv(t)
	up, err := e2.a.SaveIOUpload(ctx, d.Filename, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	ins, err := e2.a.InspectBackup(ctx, app.BackupSource{UploadToken: up.Token}, "")
	if err != nil || !ins.Encrypted || ins.Manifest != nil {
		t.Fatalf("inspect without passphrase: %+v %v", ins, err)
	}
	if _, err := e2.a.InspectBackup(ctx, app.BackupSource{UploadToken: up.Token}, "bad"); app.AsError(err).Code != app.CodeInvalidPassword {
		t.Fatalf("wrong passphrase: %v", err)
	}
	ins, err = e2.a.InspectBackup(ctx, app.BackupSource{UploadToken: up.Token}, "pw")
	if err != nil || len(ins.Manifest.Accounts) != 1 {
		t.Fatalf("inspect: %+v %v", ins, err)
	}
	j = e2.wait(t)(e2.a.StartRestore(ctx, app.RestoreRequest{Source: app.BackupSource{UploadToken: up.Token}, Passphrase: "pw", Mode: "merge"}))
	if j.Status != "done" || j.Summary["messages"] != 2 {
		t.Fatalf("restore: %+v", j)
	}
	if n := e2.count(t, `SELECT COUNT(*) FROM messages`); n != 2 {
		t.Fatalf("messages after merge: %d", n)
	}
	// Replace stages the database for the next start.
	j = e2.wait(t)(e2.a.StartRestore(ctx, app.RestoreRequest{Source: app.BackupSource{UploadToken: up.Token}, Passphrase: "pw", Mode: "replace"}))
	if j.Status != "done" {
		t.Fatalf("replace: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(e2.dir, backup.PendingDir, "tuxmail.db")); err != nil {
		t.Fatal("replace did not stage the database")
	}

	// Schedules.
	target := t.TempDir()
	s, err := e.a.SaveBackupSchedule(ctx, app.BackupScheduleInput{Schedule: backup.Schedule{Name: "Daily", Enabled: true, Frequency: "daily", Hour: 2,
		Scope: backup.Scope{Mode: backup.ModeFull}, Target: backup.TargetConfig{Kind: "local", Path: target}, Incremental: true}, Passphrase: "pw"})
	if err != nil || s.NextRun.IsZero() || s.PassphraseRef == "" {
		t.Fatalf("schedule: %+v %v", s, err)
	}
	for i := 0; i < 2; i++ {
		j = e.wait(t)(e.a.RunBackupSchedule(ctx, s.ID))
		if j.Status != "done" {
			t.Fatalf("run %d: %+v", i, j)
		}
	}
	arcs, _ := e.a.ListBackupArchives(ctx, s.ID)
	if len(arcs) != 2 || !strings.Contains(arcs[0].Name+arcs[1].Name, ".incr-") {
		t.Fatalf("archives: %+v", arcs)
	}
	list, _ := e.a.ListBackupSchedules(ctx)
	if len(list) != 1 || list[0].LastRun.IsZero() || list[0].LastError != "" {
		t.Fatalf("schedule state: %+v", list)
	}
}
