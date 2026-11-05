// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"testing"

	"github.com/Midas1989/Mailclient/internal/secrets"
)

// NC-07: the store chosen on the first start is remembered; an unavailable
// keyring at a later start must not switch to a (new) vault.
func TestChooseSecretStore(t *testing.T) {
	up, down := func() bool { return true }, func() bool { return false }
	for _, tc := range []struct {
		name     string
		portable bool
		saved    string
		avail    func() bool
		kind     string
		persist  bool
	}{
		{"first run, keyring up", false, "", up, "keyring", true},
		{"first run, keyring down", false, "", down, "vault", true},
		{"keyring chosen, now down (login race)", false, "keyring", down, "keyring", false},
		{"keyring chosen, up", false, "keyring", up, "keyring", false},
		{"vault chosen, keyring appears", false, "vault", up, "vault", false},
		{"portable ignores keyring", true, "", up, "vault", false},
		{"portable ignores saved keyring", true, "keyring", up, "vault", false},
		{"unknown value is decided again", false, "bogus", up, "keyring", true},
	} {
		kind, persist := chooseSecretStore(tc.portable, tc.saved, tc.avail)
		if kind != tc.kind || persist != tc.persist {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", tc.name, kind, persist, tc.kind, tc.persist)
		}
	}
}

func TestSecretStorePersistedInDesktopJSON(t *testing.T) {
	dir := t.TempDir()
	d := &Desktop{dataDir: dir}
	p := loadDesktopPrefs(dir)
	p.SecretStore = "keyring"
	if err := saveDesktopPrefs(dir, p); err != nil {
		t.Fatal(err)
	}
	// Saving an unrelated preference keeps the secret store.
	if err := d.SetNativeTitleBar(true); err != nil {
		t.Fatal(err)
	}
	if got := loadDesktopPrefs(dir).SecretStore; got != "keyring" {
		t.Fatalf("secretStore = %q", got)
	}
}

type failingStore struct{ err error }

func (f failingStore) Get(string) ([]byte, error) { return nil, f.err }
func (f failingStore) Set(string, []byte) error   { return f.err }
func (f failingStore) Delete(string) error        { return f.err }

func TestRequiredKeyringReportsUnavailable(t *testing.T) {
	cause := errors.New("dbus: no secret service")
	down := &requiredKeyring{inner: failingStore{cause}, available: func() bool { return false }}
	if _, err := down.Get("x"); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Get: %v", err)
	}
	if err := down.Set("x", []byte("v")); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Set: %v", err)
	}
	if err := down.Delete("x"); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Delete: %v", err)
	}
	// A missing secret in a working keyring is a plain not-found, and
	// errors of a working keyring are passed through unchanged.
	up := &requiredKeyring{inner: failingStore{secrets.ErrNotFound}, available: func() bool { return true }}
	if _, err := up.Get("x"); !errors.Is(err, secrets.ErrNotFound) || errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("not found: %v", err)
	}
	if err := (&requiredKeyring{inner: failingStore{cause}, available: func() bool { return true }}).Set("x", nil); !errors.Is(err, cause) || errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("working keyring: %v", err)
	}
	// Not found stays not-found even while the probe fails.
	if _, err := (&requiredKeyring{inner: failingStore{secrets.ErrNotFound}, available: func() bool { return false }}).Get("x"); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("not found while down: %v", err)
	}
}
