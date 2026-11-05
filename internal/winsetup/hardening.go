// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"fmt"
	"strings"
)

// Hardening decisions of the setup (docs/security/AUDIT-D-endpoint.md).
// The decisions are plain functions so they can be tested on any OS; the
// Windows calls that apply them live in hardening_windows.go.

// ProtectedDACL is the DACL given to an all-users installation folder that
// is not below Program Files (D-05): full control for SYSTEM and
// Administrators, read & execute for Users and ALL APPLICATION PACKAGES,
// not inherited from the parent (C:\ grants Authenticated Users "modify"
// on new subfolders). The owner becomes Administrators so that a folder a
// user created beforehand cannot be opened up again by that user.
const ProtectedDACL = "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;0x1200a9;;;AC)"

// PrivateTempDACL protects the temporary folder of an elevated setup
// (D-03): only SYSTEM and Administrators may create or replace files in it,
// so a process of the (non-elevated) user cannot swap the WebView2
// bootstrapper or the uninstaller copy before they are executed.
const PrivateTempDACL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// NeedsProtectedACL reports whether the setup must replace the ACL of an
// installation folder: all-users installations outside the Program Files
// folders (which already carry a safe ACL).
func NeedsProtectedACL(scope Scope, dir string, env Env) bool {
	if scope != ScopeMachine {
		return false
	}
	for _, k := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if pf := env(k); pf != "" && WinWithin(pf, dir) {
			return false
		}
	}
	return true
}

// ElevateBeforeTempCopy reports whether an uninstallation must first be
// elevated from its (protected) installation folder before the setup copies
// itself to a temporary folder (D-04). Otherwise the copy would sit in the
// user's %TEMP%, where another process of the user could replace it between
// the copy and the UAC prompt.
func ElevateBeforeTempCopy(scope Scope, elevated bool) bool {
	return scope == ScopeMachine && !elevated
}

// SafeForCmd reports whether a path can be put into a cmd.exe command line
// in double quotes (ScheduleDelete): '%' would expand environment variables
// even inside quotes, '"' would end the quoting (D-07).
func SafeForCmd(p string) bool {
	return p != "" && !strings.ContainsAny(p, "%\"\r\n")
}

// DeleteCommandLine builds the command line of the hidden cmd.exe that
// ScheduleDelete starts (re-audit ND-03). Every program is addressed by its
// full path below the system directory sysDir (C:\Windows\System32): cmd.exe
// looks up a command without a path in its current directory first, so a
// planted ping.exe/ping.cmd in the user's %TEMP% would otherwise run with
// the administrator rights of an all-users uninstall. del and rmdir are
// built-in commands of cmd.exe. Paths that cannot be quoted safely are
// skipped (SafeForCmd, D-07); an unsafe sysDir is an error.
func DeleteCommandLine(sysDir string, files, dirs []string) (string, error) {
	if !SafeForCmd(sysDir) || !strings.Contains(sysDir, `\`) {
		return "", fmt.Errorf("unsafe system directory %q", sysDir)
	}
	sysDir = strings.TrimRight(sysDir, `\`)
	var b strings.Builder
	fmt.Fprintf(&b, `"%s\ping.exe" -n 4 127.0.0.1 >nul`, sysDir)
	for _, f := range files {
		if SafeForCmd(f) {
			fmt.Fprintf(&b, ` & del /f /q "%s"`, f)
		}
	}
	for _, d := range dirs {
		if SafeForCmd(d) {
			fmt.Fprintf(&b, ` & rmdir "%s"`, d)
		}
	}
	return `"` + sysDir + `\cmd.exe" /d /s /c "` + b.String() + `"`, nil
}
