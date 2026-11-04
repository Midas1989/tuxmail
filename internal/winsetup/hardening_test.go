// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsProtectedACL(t *testing.T) {
	env := testEnv(map[string]string{
		"ProgramFiles": `C:\Program Files`, "ProgramW6432": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`,
	})
	for _, c := range []struct {
		scope Scope
		dir   string
		want  bool
	}{
		{ScopeMachine, `C:\Program Files\TuxMail`, false},
		{ScopeMachine, `c:\program files (x86)\TuxMail`, false},
		{ScopeMachine, `C:\TuxMail`, true}, // C:\ lets Authenticated Users modify new subfolders
		{ScopeMachine, `D:\Apps\TuxMail`, true},
		{ScopeMachine, `C:\Program Files Evil\TuxMail`, true},
		{ScopeUser, `C:\TuxMail`, false},
		{ScopePortable, `E:\TuxMail`, false},
	} {
		if got := NeedsProtectedACL(c.scope, c.dir, env); got != c.want {
			t.Errorf("NeedsProtectedACL(%s, %s) = %v, want %v", c.scope, c.dir, got, c.want)
		}
	}
}

func TestElevateBeforeTempCopy(t *testing.T) {
	if !ElevateBeforeTempCopy(ScopeMachine, false) {
		t.Error("machine uninstall from a non-elevated setup must elevate before copying to the temp folder")
	}
	if ElevateBeforeTempCopy(ScopeMachine, true) || ElevateBeforeTempCopy(ScopeUser, false) {
		t.Error("no extra elevation for elevated or per-user uninstalls")
	}
}

func TestSafeForCmd(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\Users\a\AppData\Local\Programs\TuxMail\TuxMail.exe`: true,
		`C:\Apps & Tools\TuxMail`:                               true, // quoted: & is literal
		`C:\x\%USERPROFILE%\TuxMail.exe`:                        false,
		`C:\x"\y`:                                               false,
		``:                                                      false,
	} {
		if got := SafeForCmd(p); got != want {
			t.Errorf("SafeForCmd(%q) = %v, want %v", p, got, want)
		}
	}
}

// AUDIT-D D-05: an all-users installation outside Program Files gets the
// protected ACL before any file is written; others are left alone.
func TestInstallProtectsMachineDirOutsideProgramFiles(t *testing.T) {
	r := newRig(t, payloadV(t, "0.1.0", "telegram"), "0.1.0")
	pf := filepath.Join(r.root, "Program Files")
	var protected []string
	r.in.Env = testEnv(map[string]string{"ProgramFiles": pf, "ProgramW6432": pf})
	r.in.Protect = func(dir string) error {
		if _, err := os.Stat(filepath.Join(dir, AppExe)); err == nil {
			t.Errorf("ACL applied after %s was written", AppExe)
		}
		protected = append(protected, dir)
		return nil
	}
	custom := filepath.Join(r.root, "Apps", "TuxMail")
	if err := r.in.Install(context.Background(), Request{Scope: ScopeMachine, Dir: custom, Options: DefaultOptions()}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.in.Install(context.Background(), Request{Scope: ScopeMachine, Dir: filepath.Join(pf, "TuxMail"), Options: DefaultOptions()}, nil); err != nil {
		t.Fatal(err)
	}
	if len(protected) != 1 || protected[0] != custom {
		t.Fatalf("protected %v, want only %s", protected, custom)
	}

	// A failing ACL aborts a fresh installation (no unprotected program files).
	r.in.Protect = func(string) error { return errors.New("access denied") }
	other := filepath.Join(r.root, "Other", "TuxMail")
	if err := r.in.Install(context.Background(), Request{Scope: ScopeMachine, Dir: other, Options: DefaultOptions()}, nil); err == nil {
		t.Fatal("install succeeded although the folder could not be protected")
	}
	if _, err := os.Stat(filepath.Join(other, AppExe)); !os.IsNotExist(err) {
		t.Fatalf("%s written into an unprotected folder", AppExe)
	}
}
