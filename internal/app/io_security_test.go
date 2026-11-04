// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// AUDIT-C (Critical in server mode): an uploaded Thunderbird profile could
// name any server file as signature file (sig_file) and the import preview
// returned its content, e.g. the server master key.
func TestUploadedThunderbirdProfileCannotReadServerFiles(t *testing.T) {
	e := newIOEnv(t)
	ctx := context.Background()
	secret := filepath.Join(e.dir, "master.key")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-MASTER-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefs := `user_pref("mail.accountmanager.accounts", "account1");
user_pref("mail.account.account1.server", "server1");
user_pref("mail.account.account1.identities", "id1");
user_pref("mail.server.server1.type", "none");
user_pref("mail.server.server1.directory", "` + filepath.ToSlash(e.dir) + `");
user_pref("mail.identity.id1.useremail", "a@example.org");
user_pref("mail.identity.id1.attach_signature", true);
user_pref("mail.identity.id1.sig_file", "` + filepath.ToSlash(secret) + `");
`
	up, err := e.a.SaveIOUpload(ctx, "profile.zip", bytes.NewReader(zipOf(t, map[string]string{"profile/prefs.js": prefs})))
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.a.InspectImport(ctx, app.IOSource{Format: "thunderbird", UploadToken: up.Token})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "TOP-SECRET") {
		t.Fatalf("preview leaks a server file: %s", b)
	}
	if p.Profile == nil || len(p.Profile.Accounts) != 1 || p.Profile.Accounts[0].MailDir != "" {
		t.Fatalf("mail directory outside the upload kept: %s", b)
	}
}

type fakeVault struct {
	initialized bool
	unlocked    []string
}

func (v *fakeVault) Status() app.VaultStatus {
	return app.VaultStatus{Kind: "vault", Portable: true, Locked: true, Initialized: v.initialized}
}
func (v *fakeVault) Unlock(pw []byte) error { v.unlocked = append(v.unlocked, string(pw)); return nil }

// AUDIT-C: a new portable vault needs a master password of minimum length
// (enforced by the backend, not only by the UI).
func TestNewVaultPasswordMinLength(t *testing.T) {
	ctx := context.Background()
	st := newIOEnv(t).st
	v := &fakeVault{}
	a, err := app.New(ctx, app.Options{Store: st, Vault: v, Secrets: nil, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.UnlockVault(ctx, "short"); err == nil || len(v.unlocked) != 0 {
		t.Fatalf("short master password accepted: %v", err)
	}
	if err := a.UnlockVault(ctx, "long enough pw"); err != nil {
		t.Fatal(err)
	}
	v.initialized = true // existing vault: any password is tried
	if err := a.UnlockVault(ctx, "x"); err != nil {
		t.Fatal(err)
	}
}

// AUDIT-C: ZIP uploads of directory formats are unpacked; entries must stay
// inside the temporary directory (zip-slip, also with backslashes).
func TestUploadZipSlip(t *testing.T) {
	for _, name := range []string{"../evil.mbox", `..\evil.mbox`, "a/../../evil.mbox", "/abs/evil.mbox"} {
		e := newIOEnv(t)
		ctx := context.Background()
		up, err := e.a.SaveIOUpload(ctx, "x.zip", bytes.NewReader(zipOf(t, map[string]string{name: "From a@b Mon Jan  1 00:00:00 2024\n\nx\n"})))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.a.InspectImport(ctx, app.IOSource{Format: "mbox", UploadToken: up.Token}); err == nil {
			t.Errorf("%q: unsafe zip entry accepted", name)
		}
		matches, _ := filepath.Glob(filepath.Join(e.dir, "*", "evil.mbox"))
		more, _ := filepath.Glob(filepath.Join(e.dir, "*", "*", "evil.mbox"))
		if len(matches)+len(more) > 0 {
			t.Errorf("%q: file written outside the unzip dir: %v %v", name, matches, more)
		}
	}
}
