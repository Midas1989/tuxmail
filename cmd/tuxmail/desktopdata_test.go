// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Midas1989/Mailclient/internal/secrets"
)

func TestResolveDataDir(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tuxmail")
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))

	loc, err := resolveDataDir("", false, exe)
	if err != nil || loc.Portable || loc.Dir != filepath.Join(dir, "xdg", "tuxmail") {
		t.Fatalf("installed: %+v %v", loc, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "portable"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	loc, _ = resolveDataDir("", false, exe)
	if !loc.Portable || loc.Dir != filepath.Join(dir, "data") {
		t.Fatalf("portable file: %+v", loc)
	}
	os.Remove(filepath.Join(dir, "portable"))
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if loc, _ = resolveDataDir("", false, exe); !loc.Portable {
		t.Fatalf("data dir: %+v", loc)
	}
	if loc, _ = resolveDataDir(filepath.Join(dir, "x"), false, exe); !loc.Portable || loc.Dir != filepath.Join(dir, "x") {
		t.Fatalf("--data-dir: %+v", loc)
	}
}

func TestLazyVault(t *testing.T) {
	v := &lazyVault{path: filepath.Join(t.TempDir(), "secrets.vault")}
	if st := v.Status(); !st.Locked || st.Initialized {
		t.Fatal(st)
	}
	if err := v.Set("a", []byte("b")); !errors.Is(err, secrets.ErrLocked) {
		t.Fatal(err)
	}
	if err := v.Unlock([]byte("master")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("a", []byte("b")); err != nil {
		t.Fatal(err)
	}
	v2 := &lazyVault{path: v.path}
	if err := v2.Unlock([]byte("wrong")); !errors.Is(err, secrets.ErrWrongPassword) {
		t.Fatal(err)
	}
	if err := v2.Unlock([]byte("master")); err != nil {
		t.Fatal(err)
	}
	if b, err := v2.Get("a"); err != nil || string(b) != "b" {
		t.Fatal(b, err)
	}
}
