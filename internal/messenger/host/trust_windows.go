// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package host

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// checkTrusted refuses plugin executables that another user of the machine
// could have planted or could replace (audit ND-13, the equivalent of
// trust_unix.go; AUDIT-D D-06): the file and its folder must belong to an
// administrator, SYSTEM, TrustedInstaller or the user running TuxMail, and
// no other principal (Everyone, Users, Authenticated Users, other users)
// may be granted write, delete or change-permission rights on them. The
// decision is made by checkSDDL on the security descriptors.
//
// Installations by the setup (Program Files, per-user folder below
// %LOCALAPPDATA%\Programs or the protected all-users folder) pass. A
// portable copy in a folder that every user can write to (a shared drive,
// C:\Tools with default ACLs) or on a file system without ACLs (FAT/exFAT
// USB sticks) does not load plugins: move it to a private folder.
func checkTrusted(path string) error {
	self := ""
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		self = u.User.Sid.String()
	}
	for _, p := range []string{path, filepath.Dir(path)} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("%s: cannot read the access rights: %w", p, err)
		}
		if err := checkSDDL(sd.String(), self); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}
