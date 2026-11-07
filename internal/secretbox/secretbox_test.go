// SPDX-License-Identifier: AGPL-3.0-or-later

package secretbox

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type vectors struct {
	Secretbox struct {
		Keyring   json.RawMessage `json:"keyring"`
		Context   string          `json:"context"`
		Token     string          `json:"token"`
		Plaintext string          `json:"plaintext"`
	} `json:"secretbox"`
}

func loadVectors(t *testing.T) (*KeyRing, vectors) {
	t.Helper()
	raw, err := os.ReadFile("testdata/testvektoren.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	kr, err := Parse(v.Secretbox.Keyring)
	if err != nil {
		t.Fatal(err)
	}
	return kr, v
}

// Basis spec/testvektoren.json: every implementation must read this token.
func TestBasisVector(t *testing.T) {
	kr, v := loadVectors(t)
	got, err := kr.Decrypt(v.Secretbox.Token, v.Secretbox.Context)
	if err != nil || string(got) != v.Secretbox.Plaintext {
		t.Fatalf("vector: %q %v", got, err)
	}
	if kr.NeedsReencrypt(v.Secretbox.Token) {
		t.Fatal("vector token is under the primary key")
	}
}

func TestContextAndManipulation(t *testing.T) {
	kr, v := loadVectors(t)
	if _, err := kr.Decrypt(v.Secretbox.Token, "users.totp_secret:2"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other context: %v", err)
	}
	tok := v.Secretbox.Token
	last := "A"
	if strings.HasSuffix(tok, "A") {
		last = "B"
	}
	if _, err := kr.Decrypt(tok[:len(tok)-1]+last, v.Secretbox.Context); err == nil {
		t.Fatal("manipulated token accepted")
	}
	for _, bad := range []string{"", "bx1", "bx1.x", "bx1.unknown.AAAA", "bx2.4db444c9.AAAA", "klartext", "bx1.4db444c9.AAAA"} {
		if _, err := kr.Decrypt(bad, "c"); !errors.Is(err, ErrFormat) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := kr.Encrypt([]byte("x"), ""); err == nil {
		t.Fatal("empty context accepted")
	}
}

func TestRotateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secretbox.json")
	kr, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := kr.SaveNew(path); err != nil {
		t.Fatal(err)
	}
	if err := kr.SaveNew(path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second SaveNew: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	old, _ := kr.Encrypt([]byte("secret"), "c")
	oldKid := kr.Primary()
	newKid, err := kr.Rotate()
	if err != nil || newKid == oldKid {
		t.Fatal(newKid, err)
	}
	if err := kr.Save(path); err != nil {
		t.Fatal(err)
	}
	re, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if re.Primary() != newKid || !re.NeedsReencrypt(old) {
		t.Fatal("primary not persisted")
	}
	if b, err := re.Decrypt(old, "c"); err != nil || string(b) != "secret" {
		t.Fatal(b, err)
	}
	if err := re.Remove(newKid); err == nil {
		t.Fatal("primary removed")
	}
	if err := re.Remove(oldKid); err != nil {
		t.Fatal(err)
	}
	if _, err := re.Decrypt(old, "c"); !errors.Is(err, ErrFormat) {
		t.Fatalf("removed key: %v", err)
	}
	os.Chmod(path, 0o640)
	if _, err := Load(path); err == nil {
		t.Fatal("group-readable key file accepted")
	}
}
