// SPDX-License-Identifier: AGPL-3.0-or-later

package vaultwarden

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

// KDF types of the Bitwarden prelogin response.
const (
	KDFPBKDF2   = 0
	KDFArgon2id = 1
)

// SendKDFIterations is the PBKDF2 iteration count of Send passwords
// (Bitwarden SEND_KDF_ITERATIONS).
const SendKDFIterations = 100000

// KDFParams are the account's key derivation parameters.
type KDFParams struct {
	Type        int `json:"kdf"`
	Iterations  int `json:"kdfIterations"`
	Memory      int `json:"kdfMemory,omitempty"`      // MiB (Argon2id)
	Parallelism int `json:"kdfParallelism,omitempty"` // Argon2id
}

// MasterKey derives the 32-byte master key from the master password. The
// salt is the lower-cased, trimmed e-mail address (Argon2id: its SHA-256).
func MasterKey(password, email string, k KDFParams) ([]byte, error) {
	salt := []byte(strings.ToLower(strings.TrimSpace(email)))
	switch k.Type {
	case KDFPBKDF2:
		if k.Iterations < 1 {
			return nil, errors.New("vaultwarden: invalid PBKDF2 iteration count")
		}
		return pbkdf2.Key([]byte(password), salt, k.Iterations, 32, sha256.New), nil
	case KDFArgon2id:
		if k.Iterations < 1 || k.Memory < 1 || k.Parallelism < 1 || k.Parallelism > 255 {
			return nil, errors.New("vaultwarden: invalid Argon2id parameters")
		}
		h := sha256.Sum256(salt)
		return argon2.IDKey([]byte(password), h[:], uint32(k.Iterations), uint32(k.Memory)*1024, uint8(k.Parallelism), 32), nil
	}
	return nil, fmt.Errorf("vaultwarden: unsupported KDF type %d", k.Type)
}

// MasterPasswordHash is the server authentication hash:
// base64(PBKDF2-SHA256(masterKey, password, 1)).
func MasterPasswordHash(masterKey []byte, password string) string {
	return base64.StdEncoding.EncodeToString(pbkdf2.Key(masterKey, []byte(password), 1, 32, sha256.New))
}

// hkdfExpand is HKDF-Expand (RFC 5869) with prk used directly.
func hkdfExpand(prk []byte, info string, n int) []byte {
	out := make([]byte, n)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, prk, []byte(info)), out); err != nil {
		panic(err)
	}
	return out
}

// StretchMasterKey expands the master key into a 64-byte enc+mac key.
func StretchMasterKey(masterKey []byte) SymmetricKey {
	return SymmetricKey(append(hkdfExpand(masterKey, "enc", 32), hkdfExpand(masterKey, "mac", 32)...))
}

// SendKey derives the Send encryption key from the 16 random key bytes:
// HKDF-SHA256(ikm=material, salt="bitwarden-send", info="send", 64).
func SendKey(material []byte) SymmetricKey {
	out := make([]byte, 64)
	if _, err := io.ReadFull(hkdf.New(sha256.New, material, []byte("bitwarden-send"), []byte("send")), out); err != nil {
		panic(err)
	}
	return SymmetricKey(out)
}

// SendPasswordHash hashes a Send password as the clients do:
// base64(PBKDF2-SHA256(password, salt=key material, 100000, 32)).
func SendPasswordHash(password string, material []byte) string {
	return base64.StdEncoding.EncodeToString(pbkdf2.Key([]byte(password), material, SendKDFIterations, 32, sha256.New))
}

// SymmetricKey is a 64-byte AES-256 key followed by a HMAC-SHA256 key.
type SymmetricKey []byte

func (k SymmetricKey) enc() []byte { return k[:32] }
func (k SymmetricKey) mac() []byte { return k[32:64] }

func (k SymmetricKey) valid() error {
	if len(k) != 64 {
		return fmt.Errorf("vaultwarden: key has %d bytes, want 64", len(k))
	}
	return nil
}

// EncString is a Bitwarden cipher string of type 2 (AES-256-CBC, HMAC-SHA256).
type EncString struct {
	IV, CT, MAC []byte
}

// String formats "2.<iv>|<ct>|<mac>".
func (e EncString) String() string {
	b := base64.StdEncoding.EncodeToString
	return "2." + b(e.IV) + "|" + b(e.CT) + "|" + b(e.MAC)
}

// ParseEncString parses a type 2 cipher string.
func ParseEncString(s string) (EncString, error) {
	t, rest, ok := strings.Cut(s, ".")
	if !ok {
		return EncString{}, errors.New("vaultwarden: invalid cipher string")
	}
	if n, err := strconv.Atoi(t); err != nil || n != 2 {
		return EncString{}, fmt.Errorf("vaultwarden: unsupported cipher string type %q", t)
	}
	parts := strings.Split(rest, "|")
	if len(parts) != 3 {
		return EncString{}, errors.New("vaultwarden: invalid type 2 cipher string")
	}
	var e EncString
	for i, dst := range []*[]byte{&e.IV, &e.CT, &e.MAC} {
		b, err := base64.StdEncoding.DecodeString(parts[i])
		if err != nil {
			return EncString{}, fmt.Errorf("vaultwarden: cipher string: %w", err)
		}
		*dst = b
	}
	if len(e.IV) != 16 || len(e.MAC) != 32 || len(e.CT) == 0 || len(e.CT)%16 != 0 {
		return EncString{}, errors.New("vaultwarden: malformed cipher string")
	}
	return e, nil
}

func macOf(key, iv, ct []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(iv)
	m.Write(ct)
	return m.Sum(nil)
}

// encryptIV encrypts with a given IV (tests use fixed IVs).
func (k SymmetricKey) encryptIV(plain, iv []byte) (EncString, error) {
	if err := k.valid(); err != nil {
		return EncString{}, err
	}
	block, err := aes.NewCipher(k.enc())
	if err != nil {
		return EncString{}, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(buf))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, buf)
	return EncString{IV: append([]byte{}, iv...), CT: ct, MAC: macOf(k.mac(), iv, ct)}, nil
}

// Encrypt encrypts plain with a random IV.
func (k SymmetricKey) Encrypt(plain []byte) (EncString, error) {
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return EncString{}, err
	}
	return k.encryptIV(plain, iv)
}

// EncryptString encrypts a UTF-8 string into its cipher string form.
func (k SymmetricKey) EncryptString(s string) (string, error) {
	e, err := k.Encrypt([]byte(s))
	if err != nil {
		return "", err
	}
	return e.String(), nil
}

// Decrypt verifies the MAC and decrypts.
func (k SymmetricKey) Decrypt(e EncString) ([]byte, error) {
	if err := k.valid(); err != nil {
		return nil, err
	}
	if !hmac.Equal(macOf(k.mac(), e.IV, e.CT), e.MAC) {
		return nil, errors.New("vaultwarden: MAC mismatch (wrong key or password)")
	}
	block, err := aes.NewCipher(k.enc())
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(e.CT))
	cipher.NewCBCDecrypter(block, e.IV).CryptBlocks(out, e.CT)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("vaultwarden: invalid padding")
	}
	return out[:len(out)-pad], nil
}

// EncryptBuffer encrypts file data in the EncArrayBuffer layout:
// 0x02 | iv (16) | mac (32) | ciphertext.
func (k SymmetricKey) EncryptBuffer(plain []byte) ([]byte, error) {
	e, err := k.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	return encBuffer(e), nil
}

func encBuffer(e EncString) []byte {
	out := make([]byte, 0, 1+16+32+len(e.CT))
	out = append(out, 2)
	out = append(out, e.IV...)
	out = append(out, e.MAC...)
	return append(out, e.CT...)
}

// DecryptBuffer decrypts an EncArrayBuffer.
func (k SymmetricKey) DecryptBuffer(b []byte) ([]byte, error) {
	if len(b) < 1+16+32+16 || b[0] != 2 {
		return nil, errors.New("vaultwarden: invalid encrypted buffer")
	}
	return k.Decrypt(EncString{IV: b[1:17], MAC: b[17:49], CT: b[49:]})
}
