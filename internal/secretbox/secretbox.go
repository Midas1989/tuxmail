// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secretbox encrypts secrets at rest in the Basis format (Basis
// spec/KRYPTO.md, ADR 0004):
//
//	token = "bx1" "." kid "." base64url(nonce[12] || ciphertext || tag[16])
//	AAD   = context, e.g. "users.totp_secret:42"
//	file  = {"primary": "<kid>", "keys": {"<kid>": "<b64url 32 B>", ...}}  (mode 0600)
//
// AES-256-GCM from the standard library. The context is mandatory: a value
// copied to another row or column cannot be decrypted. It follows the Basis Go
// module (go/secretbox) byte for byte; the Basis module is not imported because
// its repository is private and tuxmail is built on public CI. Rotate,
// NeedsReencrypt and Save mirror python/basis_sec/secretbox.py.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Prefix is the format marker of a token.
const Prefix = "bx1"

const (
	keyLen   = 32
	nonceLen = 12
	tagLen   = 16
)

var (
	b64 = base64.RawURLEncoding.Strict() // non-canonical encodings of a token are refused
	// ErrFormat reports a malformed token or key file, or an unknown key id.
	ErrFormat = errors.New("secretbox: invalid format or unknown key")
	// ErrDecrypt reports a wrong key, a wrong context or a manipulated value.
	ErrDecrypt = errors.New("secretbox: decryption failed")
)

// KeyRing holds several keys; the primary one encrypts, all of them decrypt.
type KeyRing struct {
	primary string
	keys    map[string][]byte
}

type fileFormat struct {
	Primary string            `json:"primary"`
	Keys    map[string]string `json:"keys"`
}

func newKid() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Generate returns a ring with one new key.
func Generate() (*KeyRing, error) {
	kr := &KeyRing{keys: map[string][]byte{}}
	if _, err := kr.Rotate(); err != nil {
		return nil, err
	}
	return kr, nil
}

// Parse reads the JSON form of a ring.
func Parse(raw []byte) (*KeyRing, error) {
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, ErrFormat
	}
	kr := &KeyRing{primary: f.Primary, keys: map[string][]byte{}}
	for kid, v := range f.Keys {
		k, err := b64.DecodeString(strings.TrimRight(v, "="))
		if err != nil || len(k) != keyLen || kid == "" || strings.Contains(kid, ".") {
			return nil, ErrFormat
		}
		kr.keys[kid] = k
	}
	if _, ok := kr.keys[kr.primary]; !ok {
		return nil, ErrFormat
	}
	return kr, nil
}

// Load reads a key file. A file readable by group or others is rejected.
func Load(path string) (*KeyRing, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secretbox: key file %s must not be accessible by group or others (mode %v); run chmod 600", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	kr, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("secretbox: key file %s: %w", path, err)
	}
	return kr, nil
}

func (kr *KeyRing) marshal() ([]byte, error) {
	f := fileFormat{Primary: kr.primary, Keys: map[string]string{}}
	for kid, k := range kr.keys {
		f.Keys[kid] = b64.EncodeToString(k)
	}
	return json.MarshalIndent(f, "", "  ")
}

// writeTemp writes the ring to a synced temporary file next to path.
func (kr *KeyRing) writeTemp(path string) (string, error) {
	data, err := kr.marshal()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".secretbox-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func syncDir(path string) {
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
}

// Save atomically replaces the key file (mode 0600): a crash leaves the old or
// the new ring, never a partial file.
func (kr *KeyRing) Save(path string) error {
	tmp, err := kr.writeTemp(path)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(path)
	return nil
}

// SaveNew creates the key file and fails with an error matching os.ErrExist if
// it already exists, so that two starting processes cannot overwrite each other.
func (kr *KeyRing) SaveNew(path string) error {
	tmp, err := kr.writeTemp(path)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	syncDir(path)
	return nil
}

// Primary returns the id of the key that encrypts.
func (kr *KeyRing) Primary() string { return kr.primary }

// Kids returns all key ids, sorted.
func (kr *KeyRing) Kids() []string {
	out := make([]string, 0, len(kr.keys))
	for kid := range kr.keys {
		out = append(out, kid)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the ring holds kid.
func (kr *KeyRing) Has(kid string) bool {
	_, ok := kr.keys[kid]
	return ok
}

// Rotate adds a new key, makes it primary and returns its id. Old keys stay
// and keep decrypting.
func (kr *KeyRing) Rotate() (string, error) {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	for {
		kid, err := newKid()
		if err != nil {
			return "", err
		}
		if !kr.Has(kid) {
			kr.keys[kid] = key
			kr.primary = kid
			return kid, nil
		}
	}
}

// Remove deletes a key that is not primary. The caller has checked that no
// value uses it any more.
func (kr *KeyRing) Remove(kid string) error {
	if kid == kr.primary {
		return errors.New("secretbox: the primary key cannot be removed")
	}
	if !kr.Has(kid) {
		return ErrFormat
	}
	delete(kr.keys, kid)
	return nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Encrypt returns the token of plain, bound to context, under the primary key.
func (kr *KeyRing) Encrypt(plain []byte, context string) (string, error) {
	if context == "" {
		return "", errors.New("secretbox: context is mandatory")
	}
	a, err := gcm(kr.keys[kr.primary])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen, nonceLen+len(plain)+tagLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return Prefix + "." + kr.primary + "." + b64.EncodeToString(a.Seal(nonce, nonce, plain, []byte(context))), nil
}

// Decrypt opens a token with the context it was encrypted for.
func (kr *KeyRing) Decrypt(token, context string) ([]byte, error) {
	kid, body, ok := split(token)
	if !ok {
		return nil, ErrFormat
	}
	key, ok := kr.keys[kid]
	if !ok {
		return nil, ErrFormat
	}
	raw, err := b64.DecodeString(body)
	if err != nil || len(raw) < nonceLen+tagLen {
		return nil, ErrFormat
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out, err := a.Open(nil, raw[:nonceLen], raw[nonceLen:], []byte(context))
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

// NeedsReencrypt reports whether token is not under the primary key.
func (kr *KeyRing) NeedsReencrypt(token string) bool {
	kid, _, ok := split(token)
	return !ok || kid != kr.primary
}

func split(token string) (kid, body string, ok bool) {
	p := strings.SplitN(token, ".", 3)
	if len(p) != 3 || p[0] != Prefix || p[1] == "" {
		return "", "", false
	}
	return p[1], p[2], true
}

// IsToken reports whether v has the outer form of a token: the prefix, a key id
// and nothing but base64url characters. It says nothing about whether it decrypts.
func IsToken(v []byte) bool {
	kid, body, ok := split(string(v))
	if !ok || body == "" {
		return false
	}
	for _, r := range kid + body {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// KidOf returns the key id of a token.
func KidOf(token string) (string, bool) {
	kid, _, ok := split(token)
	return kid, ok
}
