// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/zalando/go-keyring"
)

// testParams keep Argon2id cheap in tests.
var testParams = KDFParams{Time: 1, MemoryKiB: 64, Threads: 1}

// exerciseStore runs the generic domain.SecretStore contract.
func exerciseStore(t *testing.T, s domain.SecretStore) {
	t.Helper()
	ref := "tuxmail-test/account/1"
	t.Cleanup(func() { s.Delete(ref) })

	if _, err := s.Get(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}
	val := []byte{0, 1, 2, 'p', 'w', 0xff}
	if err := s.Set(ref, val); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ref)
	if err != nil || !bytes.Equal(got, val) {
		t.Fatalf("Get = %v, %v", got, err)
	}
	got[0] = 42 // returned slice must be a copy
	if again, _ := s.Get(ref); again[0] != 0 {
		t.Fatal("Get returned internal storage")
	}
	if err := s.Set(ref, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ref); string(got) != "new" {
		t.Fatalf("overwrite: %q", got)
	}
	if err := s.Set(ref, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ref); err != nil || len(got) != 0 {
		t.Fatalf("empty value: %q %v", got, err)
	}
	if err := s.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete(ref); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestMemory(t *testing.T) {
	exerciseStore(t, NewMemory())
	var zero Memory
	exerciseStore(t, &zero)
}

func TestKeyringReal(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// Must run before TestKeyringMock: MockInit replaces the provider globally.
	k := NewKeyring()
	if !k.Available() {
		t.Skip("no OS keyring available")
	}
	exerciseStore(t, k)
}

func TestKeyringMock(t *testing.T) {
	keyring.MockInit()
	k := NewKeyring()
	if !k.Available() {
		t.Fatal("mock keyring should be available")
	}
	exerciseStore(t, k)
}

func TestVaultContract(t *testing.T) {
	v, err := CreateVault(filepath.Join(t.TempDir(), "secrets.vault"), []byte("master"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	exerciseStore(t, v)
}

func TestVaultPersistLockUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := CreateVault(path, []byte("master"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateVault(path, []byte("x"), testParams); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("create over existing vault: %v", err)
	}
	if err := v.Set("a", []byte("secret-a")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("b", []byte("secret-b")); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("secret-a")) || bytes.Contains(raw, []byte(`"a"`)) {
		t.Fatal("vault file contains plaintext")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("vault mode %v", fi.Mode().Perm())
		}
	}

	v.Lock()
	if !v.Locked() {
		t.Fatal("expected locked")
	}
	if _, err := v.Get("a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("Get while locked: %v", err)
	}
	if err := v.Set("c", nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("Set while locked: %v", err)
	}
	if err := v.Delete("a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("Delete while locked: %v", err)
	}

	w, err := OpenVault(path)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Locked() || w.Path() != path {
		t.Fatal("opened vault must be locked")
	}
	if err := w.Unlock([]byte("wrong")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := w.Unlock([]byte("master")); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("b"); string(got) != "secret-b" {
		t.Fatalf("persisted value: %q", got)
	}
	if err := w.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock([]byte("master")); err != nil { // re-reads the file
		t.Fatal(err)
	}
	if _, err := v.Get("b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete not persisted: %v", err)
	}
}

func TestVaultChangePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.vault")
	v, err := CreateVault(path, []byte("old"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	v.Set("k", []byte("value"))
	before, _ := os.ReadFile(path)

	if err := v.ChangePassword([]byte("wrong"), []byte("new")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("change with wrong old password: %v", err)
	}
	if err := v.ChangePassword([]byte("old"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if bytes.Equal(before[19:35], after[19:35]) {
		t.Fatal("salt not rotated")
	}
	if got, _ := v.Get("k"); string(got) != "value" {
		t.Fatal("secrets lost in memory")
	}

	w, _ := OpenVault(path)
	if err := w.Unlock([]byte("old")); !errors.Is(err, ErrWrongPassword) {
		t.Fatal("old password must no longer work")
	}
	// ChangePassword also works on a locked vault.
	if err := w.ChangePassword([]byte("new"), []byte("newer")); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("k"); string(got) != "value" {
		t.Fatal("secret lost")
	}
	x, _ := OpenVault(path)
	if err := x.Unlock([]byte("newer")); err != nil {
		t.Fatal(err)
	}
	if err := x.ChangePassword([]byte("bad"), []byte("z")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("locked vault with wrong old password: %v", err)
	}
}

func TestVaultTamperAndFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.vault")
	v, err := CreateVault(path, []byte("pw"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	v.Set("k", []byte("v"))
	good, _ := os.ReadFile(path)

	// Flipping a ciphertext byte or the (authenticated) header is detected.
	for _, off := range []int{len(good) - 1, headerLen, 20 /* salt */, 45 /* nonce */} {
		bad := bytes.Clone(good)
		bad[off] ^= 1
		os.WriteFile(path, bad, 0o600)
		w, err := OpenVault(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Unlock([]byte("pw")); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("tamper at %d: %v", off, err)
		}
	}

	cases := map[string][]byte{
		"short":     good[:10],
		"magic":     append([]byte("NOTVAULT"), good[8:]...),
		"version":   func() []byte { b := bytes.Clone(good); b[8] = 99; return b }(),
		"kdf":       func() []byte { b := bytes.Clone(good); b[9] = 7; return b }(),
		"huge mem":  func() []byte { b := bytes.Clone(good); b[14], b[15] = 0xff, 0xff; return b }(),
		"zero time": func() []byte { b := bytes.Clone(good); copy(b[10:14], []byte{0, 0, 0, 0}); return b }(),
	}
	for name, data := range cases {
		os.WriteFile(path, data, 0o600)
		if _, err := OpenVault(path); !errors.Is(err, ErrVaultFormat) {
			t.Errorf("%s: want ErrVaultFormat, got %v", name, err)
		}
	}
	if _, err := OpenVault(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := CreateVault(filepath.Join(dir, "x"), []byte("pw"), KDFParams{}); err == nil {
		t.Fatal("invalid params must be rejected")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "secrets.vault" {
			t.Fatalf("unexpected file %s", e.Name())
		}
	}
}

func TestVaultSaveFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.vault")
	v, err := CreateVault(path, []byte("pw"), testParams)
	if err != nil {
		t.Fatal(err)
	}
	v.Set("k", []byte("v1"))
	// Make the final rename fail by putting a non-empty directory at path.
	os.Remove(path)
	os.MkdirAll(filepath.Join(path, "blocker"), 0o700)
	if err := v.Set("k", []byte("v2")); err == nil {
		t.Fatal("expected save error")
	}
	if got, _ := v.Get("k"); string(got) != "v1" {
		t.Fatalf("in-memory state changed despite failed save: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}
