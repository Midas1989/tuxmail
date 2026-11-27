// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Backup key in the administration, against the real handlers and the real
// backup scripts of deploy/: the systemd example as it is and, if Ansible is
// installed, the template of the role.

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
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
	req.Header.Set("Origin", b.e.ts.URL)
	hadSession := b.sessionCookieValue()
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if now := b.sessionCookieValue(); hadSession != "" && now != hadSession && now != "" {
		b.csrf = csrfTokenFor(now) // a step-up gave the session a new id
	}
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
	if health(t, admin) != "fail" {
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

func health(t *testing.T, admin *browser) string {
	t.Helper()
	resp, m := admin.do("GET", "/api/admin/backup", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	v, _ := m["health"].(string)
	return v
}

// The backup state is for administrators: the public /healthz says nothing about
// it, in any state, and an ordinary user does not get it either.
func TestBackupHealthIsNotPublic(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
	e.user("anna", auth.RoleUser)
	check := func() {
		t.Helper()
		req, _ := http.NewRequest("GET", e.ts.URL+"/healthz", nil)
		resp, err := e.browser().c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || bytes.Contains(bytes.ToLower(raw), []byte("backup")) {
			t.Fatalf("%s %s", resp.Status, raw)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m["status"] != "ok" {
			t.Fatal(string(raw), err)
		}
	}
	check()
	if got := health(t, admin); got != "fail" {
		t.Fatal(got)
	}
	generateKey(t, admin, false)
	check()
	user := e.browser()
	user.login("anna")
	if resp, _ := user.do("GET", "/api/admin/backup", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
}

func TestBackupHealthStates(t *testing.T) {
	e, admin := backupAdmin(t, Options{})
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
	if got := health(t, admin); got != "fail" {
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
		if got := health(t, admin); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
	os.WriteFile(e.srv.backupRecipientsPath(), []byte("kaputt\n"), 0o640)
	if got := health(t, admin); got != "fail" {
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
	return b.runArgs(t, env, "scheduled", "2")
}

func (b backupScript) runArgs(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{b.path}, args...)...)
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
		if !strings.HasPrefix(f.Name(), ".") { // the lock file of the job
			names = append(names, f.Name())
		}
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

// buildTuxmail builds the real command, which the job calls for the settings copy.
func buildTuxmail(t *testing.T) string {
	t.Helper()
	needTools(t, "go")
	bin := filepath.Join(t.TempDir(), "tuxmail")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/tuxmail").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// systemdScript is deploy/systemd/tuxmail-backup.sh with other directories.
// currentUser is the login name of the user running the tests.
func currentUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

func systemdScript(t *testing.T, dataDir string) backupScript {
	t.Helper()
	bin := buildTuxmail(t)
	raw, err := os.ReadFile("../../deploy/systemd/tuxmail-backup.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "archives")
	s := string(raw)
	for old, repl := range map[string]string{
		"DATA_DIR=/var/lib/tuxmail\n":                "DATA_DIR=" + dataDir + "\n",
		"BACKUP_DIR=/var/backups/tuxmail\n":          "BACKUP_DIR=" + backupDir + "\n",
		"KEY_DIR=/var/lib/tuxmail/backup\n":          "KEY_DIR=" + filepath.Join(dataDir, backupKeyDirName) + "\n",
		"TUXMAIL_BIN=/opt/tuxmail/current/tuxmail\n": "TUXMAIL_BIN=" + bin + "\n",
		// Run as root (containers, CI) the script writes its status as the
		// service user; test machines have no "tuxmail" account.
		"SERVICE_USER=tuxmail\n": "SERVICE_USER=" + currentUser(t) + "\n",
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
		"tuxmail_user": currentUser(t), "tuxmail_backup_allow_plaintext": false, "tuxmail_binary": buildTuxmail(t),
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
		marker, err := os.ReadFile(filepath.Join(e.cfg.DataDir, "master.key"))
		if err != nil {
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
			if strings.HasSuffix(h.Name, "/master.key") {
				got, _ := io.ReadAll(tr)
				found = bytes.Equal(got, marker)
			}
		}
		if !found {
			t.Fatal("master.key not in the decrypted archive")
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
	needTools(t, "tar", "zstd") // the plaintext run below compresses
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

// --- what the daily archive holds ------------------------------------------------

const (
	markerMail       = "MARKER-MAILTEXT-7f3a"
	markerSubject    = "MARKER-BETREFF-7f3a"
	markerAttachment = "MARKER-ANHANG-7f3a"
	markerChat       = "MARKER-CHAT-7f3a"
	markerEvent      = "MARKER-KALENDER-7f3a"
	markerContact    = "MARKER-KONTAKT-7f3a"
	markerPlugin     = "MARKER-SITZUNG-7f3a"
	markerComment    = "MARKER-TEAMKOMMENTAR-7f3a"
	markerAccount    = "MARKER-KONTO-7f3a"
	markerRule       = "MARKER-REGEL-7f3a"
	markerCert       = "MARKER-SMIME-7f3a"
	markerPGPKey     = "MARKER-PGPKEY-7f3a"
	markerPGPPeer    = "marker-pgppeer-7f3a@example.de"
	markerSignature  = "MARKER-SIGNATUR-7f3a"
	secretPayload    = "-----BEGIN PGP PRIVATE KEY BLOCK----- geheim"
	markerSigImage   = "MARKER-SIGNATURBILD-7f3a"
)

// seedUserData fills a user's data directory with settings and with everything
// that must stay out of the archive, in every place it can reach the disk.
func seedUserData(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	bs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := bs.Put(strings.NewReader(markerAttachment + " Inhalt der Datei"))
	if err != nil {
		t.Fatal(err)
	}
	sigImage, err := bs.Put(strings.NewReader(markerSigImage))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tuxmail.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		// Settings.
		`INSERT INTO accounts(id, kind, display_name, config_json, enabled) VALUES (1, 'imap', '` + markerAccount + `', '{}', 0)`,
		`INSERT INTO folders(id, account_id, remote_id, name, role, sync_state, unread, total) VALUES (1, 1, 'INBOX', 'Inbox', 'inbox', 'uidvalidity=1;uid=99', 3, 9)`,
		`INSERT INTO workflows(id, name, enabled, yaml, created_at, updated_at) VALUES ('r1', '` + markerRule + `', 1, 'name: x', 1, 1)`,
		`INSERT INTO tags(id, name, path, path_norm, created_at, updated_at) VALUES (77, 'wichtig', 'wichtig', 'wichtig', 1, 1)`,
		`INSERT INTO signatures(id, name, html, plain) VALUES (1, '` + markerSignature + `', '', '')`,
		`INSERT INTO signature_images(signature_id, content_id, blob_hash, mime_type) VALUES (1, 'c1', '` + sigImage + `', 'image/png')`,
		`INSERT INTO smime_certs(fingerprint, email, subject, der, created_at) VALUES ('fp', 'a@example.de', '` + markerCert + `', x'00', 1)`,
		`INSERT INTO pgp_keys(fingerprint, email, user_id, public_key, secret_ref, created_at, updated_at) VALUES ('AB', 'a@example.de', '` + markerPGPKey + `', x'00', 'pgp:AB', 1, 1)`,
		`INSERT INTO pgp_trust(address, fingerprint, level) VALUES ('a@example.de', 'AB', 'verified')`,
		`INSERT INTO pgp_peers(addr, public_key) VALUES ('` + markerPGPPeer + `', x'00')`,
		`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('search.history', '["` + markerMail + `"]', 1), ('prefs', '{"a":1}', 1)`,
		`INSERT INTO calendars(id, name, sync_token) VALUES (1, 'Privat', 'tok')`,
		`INSERT INTO addressbooks(id, name) VALUES (1, 'Adressen')`,
		// Data.
		`INSERT INTO conversations(id, kind, title) VALUES (1, 'mail', '` + markerSubject + `')`,
		`INSERT INTO messages(id, account_id, fingerprint, subject, snippet, conversation_id, blob_hash, created_at) VALUES (1, 1, 'fp1', '` + markerSubject + `', '` + markerMail + `', 1, '` + attachment + `', 1)`,
		`INSERT INTO message_addresses(message_id, role, email) VALUES (1, 'from', 'marker-adresse-7f3a@example.de')`,
		`INSERT INTO attachments(message_id, filename, blob_hash) VALUES (1, '` + markerAttachment + `.pdf', '` + attachment + `')`,
		`INSERT INTO fts_docs(kind, message_id, subject, body) VALUES ('mail', 1, '` + markerSubject + `', '` + markerMail + ` ` + markerAttachment + `')`,
		`INSERT INTO item_tags(item_kind, item_id, tag_id, created_at) VALUES ('message', 1, 77, 1)`,
		`INSERT INTO chats(id, account_id, remote_id, kind, title) VALUES (1, 1, 'c1', 'dm', '` + markerChat + `')`,
		`INSERT INTO chat_messages(id, chat_id, remote_id, body) VALUES (1, 1, 'm1', '` + markerChat + `')`,
		`INSERT INTO fts_docs(kind, chat_message_id, subject, body) VALUES ('chat', 1, '', '` + markerChat + `')`,
		`INSERT INTO cal_objects(calendar_id, uid, kind, ical, summary) VALUES (1, 'u1', 'event', 'SUMMARY:` + markerEvent + `', '` + markerEvent + `')`,
		`INSERT INTO persons(id, display_name) VALUES (1, '` + markerContact + `')`,
		`INSERT INTO contacts(addressbook_id, uid, vcard, display_name, person_id) VALUES (1, 'u1', 'FN:` + markerContact + `', '` + markerContact + `', 1)`,
		`INSERT INTO category_tokens(category, token, n) VALUES ('work', '` + strings.ToLower(markerMail) + `', 1)`,
		`INSERT INTO scheduled_messages(id, channel, send_at, text, message_json, created_at) VALUES ('s1', 'mail', 1, '` + markerMail + `', '{}', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for path, content := range map[string]string{
		"plugins/whatsapp/a/session.db": markerPlugin,
		"io/jobs/export.json":           markerMail,
		"archive/mail.eml":              markerMail,
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// decryptArchive returns the decompressed tar of an encrypted archive.
func decryptArchive(t *testing.T, path, privateKey string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.ParseX25519Identity(privateKey)
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
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Acceptance: the job runs while the service runs and writes, the archive holds
// the settings and no data, and restoring it gives a working server.
func TestSettingsArchiveHoldsNoDataAndRestores(t *testing.T) {
	needTools(t, "age", "zstd", "tar")
	e, admin := backupAdmin(t, Options{})
	u := e.user("mailnutzer", auth.RoleUser)
	dir := e.svc.UserDir(u.ID)
	seedUserData(t, dir)
	// Secrets (mail passwords, private key material) live in the user's vault.
	secretStore, err := e.svc.UserSecrets(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Set("account:1", []byte(secretPayload)); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Set("pgp:AB", []byte(secretPayload)); err != nil {
		t.Fatal(err)
	}
	master, err := os.ReadFile(filepath.Join(e.cfg.DataDir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	serverDB, err := sql.Open("sqlite", e.cfg.ServerDB()+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer serverDB.Close()
	if _, err := serverDB.Exec(`INSERT INTO team_mailboxes(id, name, created_at, updated_at) VALUES (5, 'Team', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := serverDB.Exec(`INSERT INTO team_comments(mailbox_id, conversation_id, body, created_at) VALUES (5, 1, ?, 1)`, markerComment); err != nil {
		t.Fatal(err)
	}
	seedUserData(t, filepath.Join(e.cfg.DataDir, "teams", "5"))
	private, _ := generateKey(t, admin, false)

	// The service keeps running and being written to.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var requests, writes atomic.Int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if resp, m := admin.do("POST", "/api/admin/groups", map[string]string{"name": fmt.Sprintf("last-%d", i)}); resp.StatusCode != http.StatusCreated {
				t.Errorf("service write during the job: %s %v", resp.Status, m)
				return
			}
			requests.Add(1)
		}
	}()
	go func() {
		defer wg.Done()
		db, err := sql.Open("sqlite", filepath.Join(dir, "tuxmail.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Error(err)
			return
		}
		defer db.Close()
		for i := 2; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO messages(id, account_id, fingerprint, subject, created_at) VALUES (?, 1, ?, ?, 1)`, i, fmt.Sprintf("fp%d", i), markerSubject); err != nil {
				t.Errorf("mail write during the job: %v", err)
				return
			}
			writes.Add(1)
		}
	}()

	job := systemdScript(t, e.cfg.DataDir)
	rc, out := job.run(t)
	close(stop)
	wg.Wait()
	if rc != 0 {
		t.Fatal(rc, out)
	}
	if requests.Load() == 0 || writes.Load() == 0 {
		t.Fatalf("no load: %d requests, %d writes", requests.Load(), writes.Load())
	}
	names := job.archives(t)
	if len(names) != 1 {
		t.Fatal(names)
	}

	// No plaintext rest.
	leftovers, _ := filepath.Glob(filepath.Join(e.cfg.DataDir, backupKeyDirName, ".staging.*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
	partial, _ := filepath.Glob(filepath.Join(job.backupDir, ".*.partial"))
	if len(partial) != 0 {
		t.Fatal(partial)
	}

	// Byte search through everything the archive holds.
	tarball := decryptArchive(t, filepath.Join(job.backupDir, names[0]), private)
	for _, absent := range []string{markerMail, markerSubject, markerAttachment, markerChat, markerEvent, markerContact, markerPlugin, markerComment, markerPGPPeer, "marker-adresse-7f3a", "uidvalidity=1"} {
		if bytes.Contains(tarball, []byte(absent)) || bytes.Contains(bytes.ToLower(tarball), []byte(strings.ToLower(absent))) {
			t.Errorf("%s is in the archive", absent)
		}
	}
	for _, present := range []string{markerAccount, markerRule, markerCert, markerPGPKey, markerSignature, markerSigImage, "mailnutzer"} {
		if !bytes.Contains(tarball, []byte(present)) {
			t.Errorf("%s is missing from the archive", present)
		}
	}
	if !bytes.Contains(tarball, master) {
		t.Error("master.key is missing from the archive")
	}
	ring, err := os.ReadFile(filepath.Join(e.cfg.DataDir, "secretbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(tarball, ring) {
		t.Error("the key ring is missing from the archive")
	}

	// Restore: unpack as a data directory, start the service, migrations run.
	restoreDir := t.TempDir()
	tarPath := filepath.Join(t.TempDir(), "a.tar")
	if err := os.WriteFile(tarPath, tarball, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-xpf", tarPath, "-C", restoreDir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	restored := filepath.Join(restoreDir, filepath.Base(e.cfg.DataDir))
	r := newEnv(t, Options{}, func(c *config.Config) { c.DataDir = restored })
	nb := r.browser()
	nb.login("mailnutzer")
	if _, m := nb.do("GET", "/api/auth/me", nil); m["user"].(map[string]any)["username"] != "mailnutzer" {
		t.Fatal(m)
	}
	rootAdmin := r.browser()
	rootAdmin.login("root")
	if _, m := rootAdmin.do("GET", "/api/admin/backup", nil); len(m["keys"].([]any)) != 1 {
		t.Fatal("backup key was not restored", m)
	}
	// The restored master.key opens the restored vault.
	restoredSecrets, err := r.svc.UserSecrets(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restoredSecrets.Get("account:1"); err != nil || string(got) != secretPayload {
		t.Fatalf("vault after restore: %q %v", got, err)
	}
	if got, err := restoredSecrets.Get("pgp:AB"); err != nil || string(got) != secretPayload {
		t.Fatalf("private OpenPGP key after restore: %q %v", got, err)
	}
	// The running service opens the store of the user, with its migrations.
	if resp, raw := func() (*http.Response, string) {
		resp, m := nb.do("GET", "/api/accounts", nil)
		js, _ := json.Marshal(m)
		return resp, string(js)
	}(); resp.StatusCode != 200 {
		t.Fatal(resp.Status, raw)
	}
	r.srv.apps.drop(u.ID)
	ctx := context.Background()
	for _, storeDir := range []string{r.svc.UserDir(u.ID), filepath.Join(restored, "teams", "5")} {
		st, err := store.Open(ctx, filepath.Join(storeDir, "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		accounts, err := st.ListAccounts(ctx)
		st.Close()
		if err != nil || len(accounts) != 1 || accounts[0].DisplayName != markerAccount {
			t.Fatalf("account: %v %v", accounts, err)
		}
		db, err := sql.Open("sqlite", filepath.Join(storeDir, "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		var rule, syncState sql.NullString
		var messages, indexed, fkErrors int
		var sigBlob string
		for q, dst := range map[string]any{
			`SELECT name FROM workflows WHERE id = 'r1'`:                     &rule,
			`SELECT sync_state FROM folders WHERE id = 1`:                    &syncState,
			`SELECT COUNT(*) FROM messages`:                                  &messages,
			`SELECT COUNT(*) FROM fts_items WHERE fts_items MATCH 'marker*'`: &indexed,
			`SELECT COUNT(*) FROM pragma_foreign_key_check`:                  &fkErrors,
			`SELECT blob_hash FROM signature_images`:                         &sigBlob,
		} {
			if err := db.QueryRow(q).Scan(dst); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if rule.String != markerRule || syncState.Valid || messages != 0 || indexed != 0 || fkErrors != 0 {
			t.Fatalf("rule %q, sync state %v, %d messages, %d indexed, %d fk errors", rule.String, syncState, messages, indexed, fkErrors)
		}
		// The index still works for mail that comes in again.
		if _, err := db.Exec(`INSERT INTO messages(id, account_id, fingerprint, created_at) VALUES (1, 1, 'neu', 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO fts_docs(kind, message_id, subject) VALUES ('mail', 1, 'neuermailtext')`); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM fts_items WHERE fts_items MATCH 'neuermailtext'`).Scan(&indexed); err != nil || indexed != 1 {
			t.Fatalf("index after restore: %d %v", indexed, err)
		}
		db.Close()
		bs, err := blob.New(filepath.Join(storeDir, "blobs"))
		if err != nil {
			t.Fatal(err)
		}
		rc, err := bs.Open(sigBlob)
		if err != nil {
			t.Fatal("signature image:", err)
		}
		rc.Close()
	}

	// Without a key: exit 3, no file, service untouched (the job never stops it here).
	os.Remove(e.srv.backupRecipientsPath())
	before := len(job.archives(t))
	if rc, out := job.run(t); rc != 3 || len(job.archives(t)) != before {
		t.Fatal(rc, out)
	}
}

// --full stays what the way back of a version change needs: the whole directory.
func TestBackupFullKeepsEverything(t *testing.T) {
	needTools(t, "age", "zstd", "tar")
	e, admin := backupAdmin(t, Options{})
	u := e.user("mailnutzer", auth.RoleUser)
	seedUserData(t, e.svc.UserDir(u.ID))
	private, _ := generateKey(t, admin, false)
	job := systemdScript(t, e.cfg.DataDir)
	if rc, out := job.runArgs(t, nil, "--full", "pre-upgrade", "2", "1.0-to-1.1"); rc != 0 {
		t.Fatal(rc, out)
	}
	tarball := decryptArchive(t, filepath.Join(job.backupDir, job.archives(t)[0]), private)
	if !bytes.Contains(tarball, []byte(markerAttachment)) || !bytes.Contains(tarball, []byte(markerPlugin)) {
		t.Fatal("the full backup lost data")
	}
}
