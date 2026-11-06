// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Backup key in the administration, against the real handlers and the real
// backup scripts of deploy/: the systemd example as it is and, if Ansible is
// installed, the template of the role.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/Midas1989/Mailclient/internal/auth"
)

const (
	backupGenerate = "/api/admin/backup/key/generate"
	backupConfirm  = "/api/admin/backup/key/confirm"
	backupImport   = "/api/admin/backup/key/import"
)

// rawDo is browser.do for tests that look at the exact bytes of the answer.
func rawDo(t *testing.T, b *browser, path string, body any) (*http.Response, []byte) {
	t.Helper()
	js, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", b.e.ts.URL+path, bytes.NewReader(js))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, b.csrf)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func backupAdmin(t *testing.T, opts Options) (*env, *browser) {
	t.Helper()
	e := newEnv(t, opts)
	e.user("root", auth.RoleAdmin)
	admin := e.browser()
	admin.login("root")
	return e, admin
}

// generateKey creates a key and confirms it, as the dialog does.
func generateKey(t *testing.T, admin *browser, replace bool) (privateKey, publicKey string) {
	t.Helper()
	resp, m := admin.do("POST", backupGenerate, map[string]any{"password": pw, "replace": replace})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, m)
	}
	resp, c := admin.do("POST", backupConfirm, map[string]any{"fingerprint": m["fingerprint"], "replace": replace})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status, c)
	}
	return m["privateKey"].(string), m["publicKey"].(string)
}

func TestBackupKeyOnlyAdminWithConfirmation(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
	_, tp := e.totpUser("secure", auth.RoleAdmin)
	e.user("anna", auth.RoleUser)

	stored := func() bool {
		_, err := os.Stat(e.srv.backupRecipientsPath())
		return err == nil
	}
	anon := e.browser()
	anon.fetchCSRF()
	if resp, _ := anon.do("GET", "/api/admin/backup", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status)
	}
	if resp, _ := anon.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status)
	}
	user := e.browser()
	user.login("anna")
	if resp, _ := user.do("GET", "/api/admin/backup", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	if resp, _ := user.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	// No confirmation, wrong confirmation, no CSRF token.
	for _, body := range []map[string]any{{}, {"password": "wrong password!"}} {
		if resp, m := admin.do("POST", backupGenerate, body); resp.StatusCode < 400 || m["privateKey"] != nil {
			t.Fatal(body, resp.Status, m)
		}
	}
	noCSRF := *admin
	noCSRF.csrf = ""
	if resp, _ := noCSRF.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	// Second factor on: the password alone is not enough, also not for an import.
	sec := e.browser()
	sec.fetchCSRF()
	_, m := sec.do("POST", "/api/auth/login", map[string]string{"username": "secure", "password": pw})
	if resp, m := sec.do("POST", "/api/auth/login/totp", map[string]string{"challenge": m["challenge"].(string), "code": tp.Code(time.Now())}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	sec.fetchCSRF()
	if resp, m := sec.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode < 400 || m["privateKey"] != nil {
		t.Fatal(resp.Status, m)
	}
	id, _ := age.GenerateX25519Identity()
	if resp, _ := sec.do("POST", backupImport, map[string]any{"password": pw, "publicKey": id.Recipient().String()}); resp.StatusCode < 400 {
		t.Fatal(resp.Status)
	}
	if stored() {
		t.Fatal("a rejected request stored a key")
	}
}

func TestBackupKeyPrivateKeyShownOnceAndNeverStored(t *testing.T) {
	var logs bytes.Buffer
	e, admin := backupAdmin(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})

	resp, raw := rawDo(t, admin, backupGenerate, map[string]any{"password": pw})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, string(raw))
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: %q", got)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	private := m["privateKey"]
	if !strings.HasPrefix(private, "AGE-SECRET-KEY-") || bytes.Count(raw, []byte(private)) != 1 {
		t.Fatalf("answer: %s", raw)
	}
	id, err := age.ParseX25519Identity(private)
	if err != nil {
		t.Fatal(err)
	}
	if id.Recipient().String() != m["publicKey"] || m["fingerprint"] != backupFingerprint(m["publicKey"]) {
		t.Fatal(m)
	}

	// The public key is what is left, for the job to read after the
	// confirmation, not writable by others.
	if resp, c := admin.do("POST", backupConfirm, map[string]any{"fingerprint": m["fingerprint"]}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, c)
	}
	fi, err := os.Stat(e.srv.backupRecipientsPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o137 != 0 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	onDisk, _ := os.ReadFile(e.srv.backupRecipientsPath())
	if string(onDisk) != m["publicKey"]+"\n" {
		t.Fatalf("file: %q", onDisk)
	}

	// Not retrievable afterwards, and the status shows only the fingerprint and the public key.
	_, st := admin.do("GET", "/api/admin/backup", nil)
	if js, _ := json.Marshal(st); bytes.Contains(js, []byte("AGE-SECRET-KEY")) {
		t.Fatalf("status: %s", js)
	}
	if st["keys"].([]any)[0].(map[string]any)["fingerprint"] != m["fingerprint"] {
		t.Fatal(st)
	}

	// Byte search through data directory (database, WAL, everything), log and audit trail.
	secret := []byte(private)
	lower := []byte(strings.ToLower(private))
	filepath.WalkDir(e.cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, secret) || bytes.Contains(content, lower) {
			t.Errorf("private key found in %s", p)
		}
		return nil
	})
	if bytes.Contains(logs.Bytes(), secret) {
		t.Error("private key in the log")
	}
	entries := backupAudit(t, admin)
	if len(entries) != 2 || entries[0]["action"] != "backup.key.confirm" || entries[1]["action"] != "backup.key.generate" || entries[0]["actor"] != "root" {
		t.Fatal(entries)
	}
	for _, entry := range entries {
		if d, _ := entry["details"].(string); !strings.Contains(d, m["fingerprint"]) || strings.Contains(d, m["publicKey"]) || strings.Contains(d, "SECRET") {
			t.Fatalf("audit details: %q", d)
		}
	}
}

func backupAudit(t *testing.T, admin *browser) []map[string]any {
	t.Helper()
	resp, m := admin.do("GET", "/api/admin/audit?action=backup.", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	var out []map[string]any
	for _, e := range m["entries"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestBackupKeyImportValidates(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
	id, _ := age.GenerateX25519Identity()
	for key, code := range map[string]string{
		id.String():                            "backup_key_private",
		"  " + id.String() + "\n":              "backup_key_private",
		strings.ToLower(id.String()):           "backup_key_private",
		"":                                     "backup_key_invalid",
		"Unsinn":                               "backup_key_invalid",
		"age1":                                 "backup_key_invalid",
		"age1qqqqqqqq":                         "backup_key_invalid",
		id.Recipient().String() + "\nage1evil": "backup_key_invalid",
	} {
		resp, raw := rawDo(t, admin, backupImport, map[string]any{"password": pw, "publicKey": key})
		if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(raw, []byte(code)) {
			t.Fatalf("%q: %s %s", key, resp.Status, raw)
		}
		// The refused input is not repeated in the answer.
		if len(strings.TrimSpace(key)) > len("age1") && bytes.Contains(raw, []byte(strings.TrimSpace(key))) {
			t.Fatalf("%q echoed: %s", key, raw)
		}
	}
	if _, err := os.Stat(e.srv.backupRecipientsPath()); err == nil {
		t.Fatal("an invalid key was stored")
	}
	public := id.Recipient().String()
	resp, m := admin.do("POST", backupImport, map[string]any{"password": pw, "publicKey": " " + public + "\n"})
	if resp.StatusCode != 200 || m["publicKey"] != public || m["privateKey"] != nil {
		t.Fatal(resp.Status, m)
	}
	if onDisk, _ := os.ReadFile(e.srv.backupRecipientsPath()); string(onDisk) != public+"\n" {
		t.Fatalf("%q", onDisk)
	}
	if a := backupAudit(t, admin); len(a) != 1 || a[0]["action"] != "backup.key.import" {
		t.Fatal(a)
	}
}

func TestBackupKeyReplaceNeedsConfirmationAndAudits(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
	_, first := generateKey(t, admin, false)
	if resp, m := admin.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode != http.StatusConflict || m["error"] != "backup_key_exists" {
		t.Fatal(resp.Status, m)
	}
	id, _ := age.GenerateX25519Identity()
	if resp, m := admin.do("POST", backupImport, map[string]any{"password": pw, "publicKey": id.Recipient().String()}); resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status, m)
	}
	if onDisk, _ := os.ReadFile(e.srv.backupRecipientsPath()); string(onDisk) != first+"\n" {
		t.Fatalf("the refused call changed the key: %q", onDisk)
	}
	_, second := generateKey(t, admin, true)
	if second == first {
		t.Fatal("same key")
	}
	entries := backupAudit(t, admin)
	if d, _ := entries[0]["details"].(string); !strings.Contains(d, "fingerprint="+backupFingerprint(second)) || !strings.Contains(d, "replaced="+backupFingerprint(first)) {
		t.Fatalf("audit: %v", entries[0])
	}
	// A damaged file counts as an existing key, and the status says so.
	os.WriteFile(e.srv.backupRecipientsPath(), []byte("kaputt\n"), 0o640)
	if _, m := admin.do("GET", "/api/admin/backup", nil); m["invalid"] != true || len(m["keys"].([]any)) != 0 {
		t.Fatal(m)
	}
	if resp, _ := admin.do("POST", backupGenerate, map[string]any{"password": pw}); resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status)
	}
}

// Until "saved in the vault" arrives at the server nothing changes for the job.
func TestBackupKeyPendingUntilConfirmed(t *testing.T) {
	needTools(t, "age", "zstd", "tar")
	e, admin := backupAdmin(t, Options{})
	job := systemdScript(t, e.cfg.DataDir)
	generate := func(replace bool) map[string]any {
		t.Helper()
		resp, m := admin.do("POST", backupGenerate, map[string]any{"password": pw, "replace": replace})
		if resp.StatusCode != http.StatusCreated {
			t.Fatal(resp.Status, m)
		}
		return m
	}
	confirm := func(fingerprint any, replace bool) (*http.Response, map[string]any) {
		t.Helper()
		return admin.do("POST", backupConfirm, map[string]any{"fingerprint": fingerprint, "replace": replace})
	}

	// First key, answer lost or tab closed: no active key, the job stays red.
	first := generate(false)
	if _, err := os.Stat(e.srv.backupRecipientsPath()); err == nil {
		t.Fatal("key active without confirmation")
	}
	if _, st := admin.do("GET", "/api/admin/backup", nil); len(st["keys"].([]any)) != 0 || st["pending"].(map[string]any)["fingerprint"] != first["fingerprint"] || st["privateKey"] != nil {
		t.Fatal(st)
	}
	if rc, out := job.run(t); rc != 3 || len(job.archives(t)) != 0 {
		t.Fatal(rc, out)
	}
	if health(t, e) != "fail" {
		t.Fatal("no active key must be fail")
	}

	// Double click: only the pending key changes, the first one is no longer valid.
	second := generate(false)
	if resp, m := confirm(first["fingerprint"], false); resp.StatusCode != http.StatusConflict || m["error"] != "backup_key_not_pending" {
		t.Fatal(resp.Status, m)
	}
	// Another session of the same administrator never saw the private key, nor does a guess help.
	other := e.browser()
	other.login("root")
	if resp, m := other.do("POST", backupConfirm, map[string]any{"fingerprint": second["fingerprint"]}); resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := confirm("SHA256:unsinn", false); resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status)
	}
	if _, err := os.Stat(e.srv.backupRecipientsPath()); err == nil {
		t.Fatal("key active after a refused confirmation")
	}
	if resp, m := confirm(second["fingerprint"], false); resp.StatusCode != 200 || m["publicKey"] != second["publicKey"] {
		t.Fatal(resp.Status, m)
	}
	// Lost answer: repeating is fine and audited once.
	if resp, m := confirm(second["fingerprint"], false); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if _, err := os.Stat(e.srv.backupPendingPath()); err == nil {
		t.Fatal("pending file left behind")
	}
	if n := len(backupAudit(t, admin)); n != 3 { // two generate, one confirm
		t.Fatal(n)
	}

	// Replacing, not confirmed: the old key stays in force and old archives stay.
	secondID, err := age.ParseX25519Identity(second["privateKey"].(string))
	if err != nil {
		t.Fatal(err)
	}
	third := generate(true)
	if resp, m := confirm(third["fingerprint"], false); resp.StatusCode != http.StatusConflict || m["error"] != "backup_key_exists" {
		t.Fatal(resp.Status, m)
	}
	if rc, out := job.run(t); rc != 0 {
		t.Fatal(rc, out)
	}
	raw, _ := os.ReadFile(filepath.Join(job.backupDir, job.archives(t)[0]))
	if _, err := age.Decrypt(bytes.NewReader(raw), secondID); err != nil {
		t.Fatal("job did not keep using the old key:", err)
	}
	if resp, m := confirm(third["fingerprint"], true); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if d, _ := backupAudit(t, admin)[0]["details"].(string); !strings.Contains(d, "replaced="+second["fingerprint"].(string)) {
		t.Fatal(d)
	}

	// An imported key replaces a pending one, which cannot be activated afterwards.
	fourth := generate(true)
	id, _ := age.GenerateX25519Identity()
	if resp, m := admin.do("POST", backupImport, map[string]any{"password": pw, "publicKey": id.Recipient().String(), "replace": true}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	if resp, _ := confirm(fourth["fingerprint"], true); resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status)
	}
}

func health(t *testing.T, e *env) string {
	t.Helper()
	resp, m := e.browser().do("GET", "/healthz", nil)
	if resp.StatusCode != 200 || m["status"] != "ok" {
		t.Fatal(resp.Status, m)
	}
	v, _ := m["backup"].(string)
	return v
}

// /healthz reports the backup state in its own field and stays 200/ok for the rollout.
func TestHealthzReportsBackupState(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
	if got := health(t, e); got != "fail" {
		t.Fatal("no key:", got)
	}
	status := func(result string, encrypted bool, at time.Time) {
		t.Helper()
		js, _ := json.Marshal(backupRun{FinishedAt: at, Label: "scheduled", Result: result, Encrypted: encrypted})
		if err := os.MkdirAll(filepath.Dir(e.srv.backupRecipientsPath()), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(e.srv.backupRecipientsPath()), backupStatusName), js, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	status("no_key", false, time.Now())
	if got := health(t, e); got != "fail" {
		t.Fatal("refused run:", got)
	}
	generateKey(t, admin, false)
	for _, c := range []struct {
		result    string
		encrypted bool
		age       time.Duration
		want      string
	}{
		{"no_key", false, 0, "warn"}, // key created, first run still to come
		{"ok", true, time.Hour, "ok"},
		{"ok", true, 72 * time.Hour, "warn"},
		{"ok", false, time.Hour, "warn"},
		{"failed", false, time.Hour, "fail"},
	} {
		status(c.result, c.encrypted, time.Now().Add(-c.age))
		if got := health(t, e); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	os.WriteFile(e.srv.backupRecipientsPath(), []byte("kaputt\n"), 0o640)
	if got := health(t, e); got != "fail" {
		t.Fatal("damaged key:", got)
	}
}

// --- the backup job -----------------------------------------------------------

// backupScript is one variant of the job, adjusted to temporary directories.
type backupScript struct {
	path, backupDir string
}

func (b backupScript) run(t *testing.T, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", b.path, "scheduled", "2")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	t.Fatal(err)
	return -1, ""
}

func (b backupScript) archives(t *testing.T) []string {
	t.Helper()
	list, err := os.ReadDir(b.backupDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range list {
		names = append(names, f.Name())
	}
	return names
}

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// systemdScript is deploy/systemd/tuxmail-backup.sh with other directories.
func systemdScript(t *testing.T, dataDir string) backupScript {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/systemd/tuxmail-backup.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "archives")
	s := string(raw)
	for old, repl := range map[string]string{
		"DATA_DIR=/var/lib/tuxmail\n":       "DATA_DIR=" + dataDir + "\n",
		"BACKUP_DIR=/var/backups/tuxmail\n": "BACKUP_DIR=" + backupDir + "\n",
		"KEY_DIR=/var/lib/tuxmail/backup\n": "KEY_DIR=" + filepath.Join(dataDir, backupKeyDirName) + "\n",
	} {
		if !strings.Contains(s, old) {
			t.Fatalf("script no longer has %q", old)
		}
		s = strings.Replace(s, old, repl, 1)
	}
	path := filepath.Join(dir, "tuxmail-backup")
	if err := os.WriteFile(path, []byte(s), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return backupScript{path: path, backupDir: backupDir}
}

// templateScript renders the template of the Ansible role.
func templateScript(t *testing.T, dataDir string) backupScript {
	t.Helper()
	needTools(t, "ansible")
	src, err := filepath.Abs("../../deploy/ansible/roles/tuxmail/templates/tuxmail-backup.sh.j2")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "archives")
	path := filepath.Join(dir, "tuxmail-backup")
	vars, _ := json.Marshal(map[string]any{
		"tuxmail_data_dir": dataDir, "tuxmail_backup_dir": backupDir, "tuxmail_service_name": "tuxmail",
		"tuxmail_backup_compression": "zstd", "tuxmail_backup_key_dir": filepath.Join(dataDir, backupKeyDirName),
		"tuxmail_user": "tuxmail", "tuxmail_backup_allow_plaintext": false,
	})
	cmd := exec.Command("ansible", "localhost", "-c", "local", "-m", "ansible.builtin.template", "-a", "src="+src+" dest="+path, "-e", string(vars))
	cmd.Env = append(os.Environ(), "ANSIBLE_LOCAL_TEMP="+dir, "ANSIBLE_REMOTE_TEMP="+dir, "ANSIBLE_HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return backupScript{path: path, backupDir: backupDir}
}

func eachScript(t *testing.T, f func(t *testing.T, script func(dataDir string) backupScript)) {
	t.Run("systemd", func(t *testing.T) {
		f(t, func(d string) backupScript { return systemdScript(t, d) })
	})
	t.Run("ansible", func(t *testing.T) {
		needTools(t, "ansible")
		f(t, func(d string) backupScript { return templateScript(t, d) })
	})
}

// A generated private key decrypts what the job writes, and the status arrives in the UI.
func TestBackupKeyRoundTripWithJob(t *testing.T) {
	needTools(t, "age", "zstd", "tar")
	eachScript(t, func(t *testing.T, script func(string) backupScript) {
		e, admin := backupAdmin(t, Options{})
		marker := []byte("inhalt-der-sicherung")
		if err := os.WriteFile(filepath.Join(e.cfg.DataDir, "marker.txt"), marker, 0o600); err != nil {
			t.Fatal(err)
		}
		private, _ := generateKey(t, admin, false)
		job := script(e.cfg.DataDir)
		if rc, out := job.run(t); rc != 0 {
			t.Fatal(rc, out)
		}
		names := job.archives(t)
		if len(names) != 1 || !strings.HasSuffix(names[0], ".tar.zst.age") {
			t.Fatal(names)
		}
		raw, err := os.ReadFile(filepath.Join(job.backupDir, names[0]))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, marker) || !bytes.HasPrefix(raw, []byte("age-encryption.org/")) {
			t.Fatal("the archive is not encrypted")
		}
		id, err := age.ParseX25519Identity(private)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := age.Decrypt(bytes.NewReader(raw), id)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zstd.NewReader(plain)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		found := false
		for tr := tar.NewReader(zr); ; {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(h.Name, "/marker.txt") {
				got, _ := io.ReadAll(tr)
				found = bytes.Equal(got, marker)
			}
		}
		if !found {
			t.Fatal("marker not in the decrypted archive")
		}
		// A different key opens nothing, and the UI shows a successful, encrypted run.
		other, _ := age.GenerateX25519Identity()
		if _, err := age.Decrypt(bytes.NewReader(raw), other); err == nil {
			t.Fatal("decrypted with another key")
		}
		_, st := admin.do("GET", "/api/admin/backup", nil)
		run, _ := st["lastRun"].(map[string]any)
		if run["result"] != "ok" || run["encrypted"] != true || run["label"] != "scheduled" {
			t.Fatal(st)
		}
		if fi, err := os.Stat(filepath.Join(e.cfg.DataDir, backupKeyDirName, backupStatusName)); err != nil || fi.Mode().Perm()&0o137 != 0 {
			t.Fatal(fi, err)
		}
	})
}

func TestBackupJobWithoutKey(t *testing.T) {
	needTools(t, "tar")
	eachScript(t, func(t *testing.T, script func(string) backupScript) {
		e, admin := backupAdmin(t, Options{})
		if err := os.MkdirAll(filepath.Join(e.cfg.DataDir, backupKeyDirName), 0o750); err != nil {
			t.Fatal(err)
		}
		job := script(e.cfg.DataDir)
		rc, out := job.run(t)
		if rc != 3 || !strings.Contains(out, "Backup-Schlüssel") {
			t.Fatal(rc, out)
		}
		if names := job.archives(t); len(names) != 0 {
			t.Fatal("archive without a key:", names)
		}
		_, st := admin.do("GET", "/api/admin/backup", nil)
		run, _ := st["lastRun"].(map[string]any)
		if len(st["keys"].([]any)) != 0 || run["result"] != "no_key" {
			t.Fatal(st)
		}

		// A damaged file stops the run even with plaintext allowed, and its content stays out of the output.
		const leak = "AGE-SECRET-KEY-1NICHTAUSGEBEN"
		os.WriteFile(e.srv.backupRecipientsPath(), []byte(leak+"\n"), 0o640)
		rc, out = job.run(t, "TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1")
		if rc != 3 || strings.Contains(out, leak) || len(job.archives(t)) != 0 {
			t.Fatal(rc, out, job.archives(t))
		}
		_, st = admin.do("GET", "/api/admin/backup", nil)
		if st["lastRun"].(map[string]any)["result"] != "invalid_key" {
			t.Fatal(st)
		}
		// A link is not followed.
		target := filepath.Join(t.TempDir(), "elsewhere")
		id, _ := age.GenerateX25519Identity()
		os.WriteFile(target, []byte(id.Recipient().String()+"\n"), 0o640)
		os.Remove(e.srv.backupRecipientsPath())
		os.Symlink(target, e.srv.backupRecipientsPath())
		if rc, out = job.run(t); rc != 3 || len(job.archives(t)) != 0 {
			t.Fatal(rc, out)
		}

		// The deliberate way stays: plaintext without a key, marked as such.
		os.Remove(e.srv.backupRecipientsPath())
		rc, out = job.run(t, "TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1")
		if rc != 0 {
			t.Fatal(rc, out)
		}
		if names := job.archives(t); len(names) != 1 || !strings.HasSuffix(names[0], ".tar.zst") {
			t.Fatal(names)
		}
		_, st = admin.do("GET", "/api/admin/backup", nil)
		if run := st["lastRun"].(map[string]any); run["result"] != "ok" || run["encrypted"] != false {
			t.Fatal(st)
		}
	})
}

// The retention counts encrypted and plain archives together.
func TestBackupJobRetentionAcrossFormats(t *testing.T) {
	needTools(t, "age", "zstd", "tar")
	e, admin := backupAdmin(t, Options{})
	job := systemdScript(t, e.cfg.DataDir)
	old := filepath.Join(job.backupDir, "tuxmail-scheduled-20200101-000000.tar.zst")
	os.WriteFile(old, []byte("x"), 0o600)
	older := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, older, older)
	generateKey(t, admin, false)
	for range 2 {
		if rc, out := job.run(t); rc != 0 {
			t.Fatal(rc, out)
		}
		time.Sleep(1100 * time.Millisecond) // file names carry seconds
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatalf("old plain archive kept: %v", job.archives(t))
	}
	if names := job.archives(t); len(names) != 2 {
		t.Fatal(names)
	}
}

// The role takes over the former variable once and then leaves the file alone.
func TestBackupRoleTakesOverExistingRecipients(t *testing.T) {
	needTools(t, "ansible-playbook", "age", "zstd", "tar")
	e, admin := backupAdmin(t, Options{})
	dir := t.TempDir()
	tasks, err := filepath.Abs("../../deploy/ansible/roles/tuxmail/tasks/backup_key.yml")
	if err != nil {
		t.Fatal(err)
	}
	playbook := filepath.Join(dir, "play.yml")
	os.WriteFile(playbook, []byte("- hosts: localhost\n  connection: local\n  gather_facts: false\n  tasks:\n    - ansible.builtin.import_tasks: "+tasks+"\n"), 0o600)
	owner := exec.Command("id", "-un")
	group := exec.Command("id", "-gn")
	ownerName, _ := owner.Output()
	groupName, _ := group.Output()
	k1, _ := age.GenerateX25519Identity()
	k2, _ := age.GenerateX25519Identity()
	rollout := func(recipients ...string) {
		t.Helper()
		vars, _ := json.Marshal(map[string]any{
			"tuxmail_backup_key_dir": filepath.Join(e.cfg.DataDir, backupKeyDirName), "tuxmail_backup_age_recipients": append([]string{}, recipients...),
			"tuxmail_user": strings.TrimSpace(string(ownerName)), "tuxmail_group": strings.TrimSpace(string(groupName)),
		})
		cmd := exec.Command("ansible-playbook", "-i", "localhost,", playbook, "-e", string(vars))
		cmd.Env = append(os.Environ(), "ANSIBLE_HOME="+dir, "ANSIBLE_LOCAL_TEMP="+dir, "ANSIBLE_REMOTE_TEMP="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	// Without a value only the directory appears, with its rights.
	rollout()
	fi, err := os.Stat(filepath.Join(e.cfg.DataDir, backupKeyDirName))
	if err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatal(fi, err)
	}
	if _, err := os.Stat(e.srv.backupRecipientsPath()); err == nil {
		t.Fatal("file without a value")
	}
	// Existing installation: both keys arrive, the UI shows them, the job encrypts for them.
	rollout(k1.Recipient().String(), k2.Recipient().String())
	_, st := admin.do("GET", "/api/admin/backup", nil)
	if st["invalid"] != false || len(st["keys"].([]any)) != 2 {
		t.Fatal(st)
	}
	job := systemdScript(t, e.cfg.DataDir)
	if rc, out := job.run(t); rc != 0 {
		t.Fatal(rc, out)
	}
	raw, _ := os.ReadFile(filepath.Join(job.backupDir, job.archives(t)[0]))
	for _, id := range []*age.X25519Identity{k1, k2} {
		if _, err := age.Decrypt(bytes.NewReader(raw), id); err != nil {
			t.Fatal(err)
		}
	}
	// A key replaced in the UI is not turned back by the next rollout.
	_, public := generateKey(t, admin, true)
	rollout(k1.Recipient().String())
	if onDisk, _ := os.ReadFile(e.srv.backupRecipientsPath()); string(onDisk) != public+"\n" {
		t.Fatalf("rollout overwrote the key: %q", onDisk)
	}
}
