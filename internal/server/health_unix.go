// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package server

import "syscall"

// diskFreePercent is the share of the file system of dir an unprivileged process can use.
func diskFreePercent(dir string) (int, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil || st.Blocks == 0 {
		return 0, false
	}
	return int(st.Bavail * 100 / st.Blocks), true
}
