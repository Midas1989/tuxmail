// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"slices"
	"testing"
)

func testEnv(m map[string]string) Env { return func(k string) string { return m[k] } }

var winEnv = testEnv(map[string]string{
	"LOCALAPPDATA":      `C:\Users\anna\AppData\Local`,
	"APPDATA":           `C:\Users\anna\AppData\Roaming`,
	"USERPROFILE":       `C:\Users\anna`,
	"ProgramFiles":      `C:\Program Files`,
	"ProgramW6432":      `C:\Program Files`,
	"ProgramFiles(x86)": `C:\Program Files (x86)`,
	"ProgramData":       `C:\ProgramData`,
	"SystemRoot":        `C:\Windows`,
	"SystemDrive":       `C:`,
})

func TestWinPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\Users\x\..\y\`, `C:\Users\y`},
		{`c:/a//b/./c`, `C:\a\b\c`},
		{`C:\..\..`, `C:\`},
		{`C:\`, `C:\`},
		{`\\srv\share\a\..\b`, `\\srv\share\b`},
		{`\\srv\share`, `\\srv\share\`},
		{`rel\x`, `rel\x`},
	} {
		if got := WinClean(tc.in); got != tc.want {
			t.Errorf("WinClean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := WinJoin(`C:\Program Files`, "TuxMail"); got != `C:\Program Files\TuxMail` {
		t.Errorf("WinJoin = %q", got)
	}
	for p, want := range map[string]bool{`C:\x`: true, `C:x`: false, `\x`: false, `\\s\h\x`: true, `x`: false} {
		if WinIsAbs(p) != want {
			t.Errorf("WinIsAbs(%q) != %v", p, want)
		}
	}
	if !WinIsRoot(`D:\`) || WinIsRoot(`D:\x`) || !WinIsRoot(`\\s\h`) {
		t.Error("WinIsRoot")
	}
	if WinBase(`C:\a\TuxMail\`) != "TuxMail" || WinDir(`C:\a\b`) != `C:\a` || WinDir(`C:\a`) != `C:\` {
		t.Error("WinBase/WinDir")
	}
	if !WinWithin(`C:\Windows`, `c:\windows\system32`) || WinWithin(`C:\Win`, `C:\Windows`) || !WinWithin(`C:\`, `C:\x`) {
		t.Error("WinWithin")
	}
}

func TestDefaultDir(t *testing.T) {
	for _, tc := range []struct {
		scope    Scope
		setupDir string
		want     string
	}{
		{ScopeUser, "", `C:\Users\anna\AppData\Local\Programs\TuxMail`},
		{ScopeMachine, "", `C:\Program Files\TuxMail`},
		{ScopePortable, `E:\`, `E:\TuxMail`},
		{ScopePortable, `C:\Users\anna\Downloads`, `C:\Users\anna\TuxMail Portable`}, // protected: not next to the setup
		{ScopePortable, `D:\Tools`, `D:\Tools\TuxMail`},
	} {
		if got := DefaultDir(tc.scope, winEnv, tc.setupDir); got != tc.want {
			t.Errorf("DefaultDir(%s, %q) = %q, want %q", tc.scope, tc.setupDir, got, tc.want)
		}
	}
	// 32-bit environment: ProgramW6432 wins over ProgramFiles (x86).
	env := testEnv(map[string]string{"ProgramFiles": `C:\Program Files (x86)`, "ProgramW6432": `C:\Program Files`})
	if got := DefaultDir(ScopeMachine, env, ""); got != `C:\Program Files\TuxMail` {
		t.Errorf("machine dir = %q", got)
	}
	if got := UserDataDir(winEnv); got != `C:\Users\anna\AppData\Roaming\TuxMail` {
		t.Errorf("UserDataDir = %q", got)
	}
}

func TestSuggestDir(t *testing.T) {
	for in, want := range map[string]string{
		`D:\Apps`:          `D:\Apps\TuxMail`,
		`D:\Apps\tuxmail\`: `D:\Apps\tuxmail`,
		`E:\`:              `E:\TuxMail`,
		``:                 ``,
	} {
		if got := SuggestDir(in); got != want {
			t.Errorf("SuggestDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckDir(t *testing.T) {
	states := map[string]DirState{
		`D:\full`:          {Exists: true, IsDir: true},
		`D:\file`:          {Exists: true},
		`D:\old\TuxMail`:   {Exists: true, IsDir: true, HasApp: true},
		`D:\empty`:         {Exists: true, IsDir: true, Empty: true},
		`C:\Program Files`: {Exists: true, IsDir: true},
	}
	stat := func(p string) DirState { return states[p] }
	for _, tc := range []struct {
		scope     Scope
		dir       string
		err, warn DirIssue
		existing  bool
	}{
		{ScopeUser, ``, DirEmpty, "", false},
		{ScopeUser, `TuxMail`, DirRelative, "", false},
		{ScopeUser, `C:TuxMail`, DirRelative, "", false},
		{ScopeUser, `D:\a|b`, DirInvalidChars, "", false},
		{ScopeUser, `D:\a:b`, DirInvalidChars, "", false},
		{ScopeUser, `D:\trailing.\x`, DirInvalidChars, "", false},
		{ScopeUser, `D:\`, DirRoot, "", false},
		{ScopeUser, `\\nas\share\TuxMail`, DirNetwork, "", false},
		{ScopePortable, `\\nas\share\TuxMail`, "", "", false},
		{ScopeMachine, `C:\Windows\System32\TuxMail`, DirSystem, "", false},
		{ScopeMachine, `C:\Program Files`, DirProtected, "", false},
		{ScopeUser, `C:\Users\anna\AppData\Local\Programs`, DirProtected, "", false},
		{ScopeUser, `C:\Users\anna\Desktop`, DirProtected, "", false},
		{ScopeUser, `D:\file`, DirIsFile, "", false},
		{ScopeUser, `D:\full`, "", DirNotEmpty, false},
		{ScopeUser, `D:\empty`, "", "", false},
		{ScopeUser, `D:\old\TuxMail`, "", "", true},
		{ScopeMachine, `C:\Users\anna\TuxMail`, "", DirOtherUserArea, false},
		{ScopeUser, `C:\Users\anna\AppData\Local\Programs\TuxMail`, "", "", false},
	} {
		got := CheckDir(tc.scope, tc.dir, winEnv, stat)
		if got.Error != tc.err || got.Warning != tc.warn || got.Existing != tc.existing {
			t.Errorf("CheckDir(%s, %q) = %+v, want error=%q warning=%q existing=%v", tc.scope, tc.dir, got, tc.err, tc.warn, tc.existing)
		}
	}
}

func TestParseScope(t *testing.T) {
	for in, want := range map[string]Scope{"user": ScopeUser, "Machine": ScopeMachine, "allusers": ScopeMachine, "portable": ScopePortable} {
		if got, err := ParseScope(in); err != nil || got != want {
			t.Errorf("ParseScope(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseScope("everyone"); err == nil {
		t.Error("ParseScope accepted an unknown scope")
	}
	if !slices.Contains(protectedDirs(winEnv), `C:\Users`) {
		t.Error(`C:\Users must be protected`)
	}
}
