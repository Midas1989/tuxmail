// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package dllsafe

import "syscall"

// loadLibrarySearchSystem32 is LOAD_LIBRARY_SEARCH_SYSTEM32.
const loadLibrarySearchSystem32 = 0x00000800

// Err is the error of SetDefaultDllDirectories (nil on success).
var Err error

func init() {
	// kernel32.dll is a KnownDLL: loading it by name is safe.
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetDefaultDllDirectories")
	if err := proc.Find(); err != nil {
		Err = err
		return
	}
	if r, _, err := proc.Call(loadLibrarySearchSystem32); r == 0 {
		Err = err
	}
}
