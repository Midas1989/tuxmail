// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PrivateTempDir returns a fresh temporary folder for files the setup
// executes or writes. An elevated setup creates it with PrivateTempDACL,
// so non-elevated processes of the user cannot plant or replace files in
// it (D-03); CreateDirectory fails if the name exists, so the folder cannot
// be prepared in advance either. A non-elevated setup gets a normal
// temporary folder.
func PrivateTempDir(prefix string) (string, error) {
	if !IsElevated() {
		return os.MkdirTemp("", prefix)
	}
	sd, err := windows.SecurityDescriptorFromString(PrivateTempDACL)
	if err != nil {
		return "", err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	for i := 0; i < 10; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		dir := filepath.Join(os.TempDir(), prefix+hex.EncodeToString(b))
		p, err := windows.UTF16PtrFromString(dir)
		if err != nil {
			return "", err
		}
		err = windows.CreateDirectory(p, sa)
		if err == nil {
			return dir, nil
		}
		if err != windows.ERROR_ALREADY_EXISTS {
			return "", fmt.Errorf("create private temp folder: %w", err)
		}
	}
	return "", fmt.Errorf("create private temp folder: no free name")
}

// ProtectInstallDir applies ProtectedDACL to an all-users installation
// folder outside Program Files (D-05). The inheritable ACEs propagate to
// the files the setup writes into it.
func ProtectInstallDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(ProtectedDACL)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
}
