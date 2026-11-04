// SPDX-License-Identifier: AGPL-3.0-or-later

package thunderbird

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AUDIT-C: an uploaded profile must not make the server read files outside
// the upload (sig_file = master.key, absolute mail directories,
// profiles.ini with IsRelative=0).
func TestReadConfinedToRoot(t *testing.T) {
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "master.key")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-MASTER-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secretDir, "Mail"), 0o700); err != nil {
		t.Fatal(err)
	}
	up := t.TempDir()
	prefs := strings.Join([]string{
		`user_pref("mail.accountmanager.accounts", "account1,account2");`,
		`user_pref("mail.account.account1.server", "server1");`,
		`user_pref("mail.account.account1.identities", "id1");`,
		`user_pref("mail.server.server1.type", "none");`,
		`user_pref("mail.server.server1.directory", "` + filepath.ToSlash(filepath.Join(secretDir, "Mail")) + `");`,
		`user_pref("mail.identity.id1.useremail", "a@example.org");`,
		`user_pref("mail.identity.id1.attach_signature", true);`,
		`user_pref("mail.identity.id1.sig_file", "` + filepath.ToSlash(secret) + `");`,
		`user_pref("mail.account.account2.server", "server2");`,
		`user_pref("mail.account.account2.identities", "id2");`,
		`user_pref("mail.server.server2.type", "none");`,
		`user_pref("mail.server.server2.directory-rel", "[ProfD]../../../../../../../../tmp");`,
		`user_pref("mail.identity.id2.useremail", "b@example.org");`,
		`user_pref("mail.identity.id2.attach_signature", true);`,
		`user_pref("mail.identity.id2.sig_file-rel", "[ProfD]../` + filepath.Base(secretDir) + `/master.key");`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(up, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	pr, err := Read(context.Background(), up, Options{Root: up, SkipAddressBooks: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pr.Accounts {
		if a.MailDir != "" {
			t.Errorf("account %s: mail dir outside root kept: %s", a.Key, a.MailDir)
		}
		for _, id := range a.Identities {
			if strings.Contains(id.SignatureText+id.SignatureHTML, "TOP-SECRET") {
				t.Errorf("identity %s: signature read from outside the upload", id.Email)
			}
		}
	}
	// Without Root (desktop: the user's own profile) absolute paths work.
	pr, err = Read(context.Background(), up, Options{SkipAddressBooks: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pr.Accounts[0].Identities[0].SignatureText, "TOP-SECRET") {
		t.Errorf("unconfined read should follow sig_file")
	}

	// profiles.ini pointing outside the upload.
	up2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretDir, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	ini := "[Profile0]\nName=x\nIsRelative=0\nPath=" + secretDir + "\nDefault=1\n"
	if err := os.WriteFile(filepath.Join(up2, "profiles.ini"), []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), up2, Options{Root: up2}); err == nil {
		t.Errorf("profile outside the root accepted")
	}
}
