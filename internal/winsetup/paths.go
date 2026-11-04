// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"fmt"
	"strings"
)

// AppName is the product and folder name.
const AppName = "TuxMail"

// File names inside an installation.
const (
	AppExe        = "TuxMail.exe"
	SetupExe      = "TuxMail-Setup.exe" // copy of the setup, used for uninstall/repair
	RecordFile    = "tuxmail-install.json"
	PortableMark  = "portable"
	PortableData  = "data"
	legacyUninst  = "Uninstall.exe" // NSIS installer (classic)
	newSuffix     = ".tuxmail-new"
	oldSuffix     = ".tuxmail-old"
	PluginsSubdir = "plugins"
)

// Scope is the installation type.
type Scope string

const (
	// ScopeUser installs for the current user (%LOCALAPPDATA%\Programs, HKCU, no admin).
	ScopeUser Scope = "user"
	// ScopeMachine installs for all users (Program Files, HKLM, elevated).
	ScopeMachine Scope = "machine"
	// ScopePortable only extracts the program with a "portable" marker and data\.
	ScopePortable Scope = "portable"
)

// ParseScope accepts user|machine|portable (and a few aliases).
func ParseScope(s string) (Scope, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "user", "currentuser", "me", "peruser":
		return ScopeUser, nil
	case "machine", "allusers", "all", "permachine":
		return ScopeMachine, nil
	case "portable":
		return ScopePortable, nil
	}
	return "", fmt.Errorf("unknown scope %q (user, machine or portable)", s)
}

// Env looks up environment variables (os.Getenv in production).
type Env func(string) string

// DefaultDir returns the proposed destination folder of a scope. setupDir
// is the folder of the running setup program (portable default: next to it,
// e.g. on a USB stick).
func DefaultDir(scope Scope, env Env, setupDir string) string {
	switch scope {
	case ScopeMachine:
		pf := env("ProgramW6432") // 64-bit Program Files, also from 32-bit processes
		if pf == "" {
			pf = env("ProgramFiles")
		}
		if pf == "" {
			pf = WinJoin(sysDrive(env), "Program Files")
		}
		return WinJoin(pf, AppName)
	case ScopePortable:
		if setupDir != "" && WinIsAbs(setupDir) && !isProtected(setupDir, env) {
			return WinJoin(setupDir, AppName)
		}
		if p := env("USERPROFILE"); p != "" {
			return WinJoin(p, AppName+" Portable")
		}
		return WinJoin(sysDrive(env), AppName+" Portable")
	default:
		la := env("LOCALAPPDATA")
		if la == "" {
			if p := env("USERPROFILE"); p != "" {
				la = WinJoin(p, "AppData", "Local")
			}
		}
		return WinJoin(la, "Programs", AppName)
	}
}

func sysDrive(env Env) string {
	if d := env("SystemDrive"); d != "" {
		return d + `\`
	}
	return `C:\`
}

// UserDataDir is where TuxMail keeps the data of the current user
// (cmd/tuxmail resolveDataDir: %APPDATA%\TuxMail).
func UserDataDir(env Env) string {
	if a := env("APPDATA"); a != "" {
		return WinJoin(a, AppName)
	}
	return ""
}

// SuggestDir turns a folder picked in the browse dialog into a destination:
// "\TuxMail" is appended unless the folder is already called so.
func SuggestDir(picked string) string {
	picked = WinClean(strings.TrimSpace(picked))
	if picked == "" || strings.EqualFold(WinBase(picked), AppName) {
		return picked
	}
	if WinIsRoot(picked) {
		return WinClean(picked + `\` + AppName)
	}
	return WinJoin(picked, AppName)
}

// protectedDirs are system and profile folders that must not be used as
// the installation folder itself (uninstall would clean them up).
func protectedDirs(env Env) []string {
	var out []string
	for _, k := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramW6432", "ProgramFiles(x86)",
		"ProgramData", "LOCALAPPDATA", "APPDATA", "USERPROFILE", "PUBLIC", "TEMP", "TMP"} {
		if v := env(k); v != "" {
			out = append(out, v)
		}
	}
	if la := env("LOCALAPPDATA"); la != "" {
		out = append(out, WinJoin(la, "Programs"))
	}
	if up := env("USERPROFILE"); up != "" {
		for _, s := range []string{"Desktop", "Documents", "Downloads"} {
			out = append(out, WinJoin(up, s))
		}
		out = append(out, WinDir(up)) // C:\Users
	}
	return out
}

func isProtected(dir string, env Env) bool {
	for _, p := range protectedDirs(env) {
		if WinSame(p, dir) {
			return true
		}
	}
	return false
}

// DirIssue identifies a problem with a destination folder. The values are
// translation keys of the setup UI.
type DirIssue string

const (
	DirOK            DirIssue = ""
	DirEmpty         DirIssue = "dirEmpty"
	DirRelative      DirIssue = "dirRelative"
	DirInvalidChars  DirIssue = "dirInvalidChars"
	DirRoot          DirIssue = "dirRoot"
	DirSystem        DirIssue = "dirSystem"
	DirProtected     DirIssue = "dirProtected"
	DirIsFile        DirIssue = "dirIsFile"
	DirNetwork       DirIssue = "dirNetwork"
	DirNotEmpty      DirIssue = "dirNotEmpty"      // warning only
	DirOtherUserArea DirIssue = "dirOtherUserArea" // warning: machine install in a profile
)

// DirState describes what is at a path (os.Stat in production).
type DirState struct {
	Exists, IsDir, Empty bool
	// HasApp: TuxMail.exe or an install record is already there (update).
	HasApp bool
	// Portable: a "portable" marker is there.
	Portable bool
}

// DirCheck is the result of CheckDir.
type DirCheck struct {
	Dir     string   `json:"dir"`
	Error   DirIssue `json:"error,omitempty"`
	Warning DirIssue `json:"warning,omitempty"`
	// Existing: the folder holds a TuxMail installation that is updated.
	Existing bool `json:"existing"`
}

// CheckDir validates a destination folder for a scope.
func CheckDir(scope Scope, dir string, env Env, stat func(string) DirState) DirCheck {
	dir = strings.TrimSpace(dir)
	res := DirCheck{Dir: WinClean(dir)}
	switch {
	case dir == "":
		res.Error = DirEmpty
		return res
	case !WinIsAbs(dir):
		res.Error = DirRelative
		return res
	}
	_, rest := splitVolume(res.Dir)
	if strings.ContainsAny(rest, `<>:"|?*`) || strings.ContainsFunc(rest, func(r rune) bool { return r < 32 }) {
		res.Error = DirInvalidChars
		return res
	}
	for _, el := range strings.Split(rest, `\`) {
		if el != "" && (strings.HasSuffix(el, ".") || strings.HasSuffix(el, " ")) {
			res.Error = DirInvalidChars
			return res
		}
	}
	if WinIsRoot(res.Dir) {
		res.Error = DirRoot
		return res
	}
	if scope != ScopePortable && WinIsUNC(res.Dir) {
		res.Error = DirNetwork
		return res
	}
	for _, k := range []string{"SystemRoot", "windir"} {
		if w := env(k); w != "" && WinWithin(w, res.Dir) {
			res.Error = DirSystem
			return res
		}
	}
	if isProtected(res.Dir, env) {
		res.Error = DirProtected
		return res
	}
	st := stat(res.Dir)
	switch {
	case st.Exists && !st.IsDir:
		res.Error = DirIsFile
		return res
	case st.HasApp:
		res.Existing = true
	case st.Exists && !st.Empty:
		res.Warning = DirNotEmpty
	}
	if scope == ScopeMachine {
		if up := env("USERPROFILE"); up != "" && WinWithin(WinDir(up), res.Dir) {
			res.Warning = DirOtherUserArea
		}
	}
	return res
}
