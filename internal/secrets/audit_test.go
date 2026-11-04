// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// AUDIT-C: lowering the KDF parameters in the header (downgrade) must not
// yield a vault that unlocks, oversized parameters are refused before a key
// is derived, and the file is owner-only.
func TestVaultKDFDowngradeAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	if _, err := CreateVault(path, []byte("pw"), testParams); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	if st, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("vault mode %v", st.Mode().Perm())
	}
	bad := bytes.Clone(good)
	copy(bad[10:19], []byte{0, 0, 0, 1, 0, 0, 0, 8, 1}) // t=1, m=8 KiB, p=1
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := OpenVault(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Unlock([]byte("pw")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("downgraded header: %v", err)
	}
	huge := bytes.Clone(good)
	copy(huge[14:18], []byte{0, 0x20, 0, 0}) // 2 GiB
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(path); !errors.Is(err, ErrVaultFormat) {
		t.Fatalf("2 GiB memory parameter: %v", err)
	}
}
