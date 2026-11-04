// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package host

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkTrusted refuses plugin executables that another user could have
// planted or could replace (AUDIT-D D-06): the file and its directory must
// belong to root or to the user running TuxMail and must not be writable
// by others (group-writable only for the user's own primary group, the
// "user private group" umask 002 of many distributions; a world-writable
// directory only with the sticky bit).
func checkTrusted(path string) error {
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	check := func(p string, isDir bool) error {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Uid != 0 && st.Uid != uid {
			return fmt.Errorf("%s is owned by uid %d", p, st.Uid)
		}
		mode := fi.Mode()
		if mode.Perm()&0o002 != 0 && !(isDir && mode&os.ModeSticky != 0) {
			return fmt.Errorf("%s is writable by every user", p)
		}
		if mode.Perm()&0o020 != 0 && st.Gid != gid && !(isDir && mode&os.ModeSticky != 0) {
			return fmt.Errorf("%s is writable by group %d", p, st.Gid)
		}
		return nil
	}
	if err := check(path, false); err != nil {
		return err
	}
	return check(filepath.Dir(path), true)
}
