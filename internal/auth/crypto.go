// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const masterKeyLen = 32

// LoadOrCreateMasterKey reads the base64-encoded 256-bit master key at path,
// creating it with mode 0600 if it does not exist. A key file that is readable
// by group or others is rejected.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, masterKeyLen)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(key) + "\n"
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("auth: create master key: %w", err)
		}
		if _, err := f.WriteString(enc); err != nil {
			f.Close()
			os.Remove(path)
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		if d, err := os.Open(filepath.Dir(path)); err == nil {
			d.Sync()
			d.Close()
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth: read master key: %w", err)
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("auth: master key %s must not be accessible by group or others (mode %v); run chmod 600", path, st.Mode().Perm())
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != masterKeyLen {
		return nil, fmt.Errorf("auth: master key %s is malformed", path)
	}
	return key, nil
}

// seal encrypts plain with the master key; ad binds the ciphertext to its use.
func (s *Service) seal(plain []byte, ad string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(s.master)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, []byte(ad)), nil
}

func (s *Service) open(ct []byte, ad string) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(s.master)
	if err != nil {
		return nil, err
	}
	if len(ct) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("auth: ciphertext too short")
	}
	plain, err := aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], []byte(ad))
	if err != nil {
		return nil, errors.New("auth: cannot decrypt (wrong master key?)")
	}
	return plain, nil
}

// ArgonParams are Argon2id password hashing parameters.
type ArgonParams struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
	KeyLen    uint32
}

// DefaultArgonParams follow RFC 9106's second recommendation (t=3, m=64 MiB, p=4).
var DefaultArgonParams = ArgonParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4, KeyLen: 32}

// argonSem bounds concurrent Argon2id computations: each needs MemoryKiB of
// RAM, so unbounded parallel logins could exhaust memory.
var argonSem = make(chan struct{}, max(2, runtime.NumCPU()))

func argonKey(password, salt []byte, p ArgonParams, keyLen uint32) []byte {
	argonSem <- struct{}{}
	defer func() { <-argonSem }()
	return argon2.IDKey(password, salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
}

// HashPassword returns an Argon2id hash in PHC string format:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash> (unpadded standard base64).
func HashPassword(password string, p ArgonParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argonKey([]byte(password), salt, p, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(h)), nil
}

// VerifyPassword checks password against a PHC Argon2id hash in constant time.
// needsRehash reports whether the hash uses parameters other than want.
func VerifyPassword(password, phc string, want ArgonParams) (ok, needsRehash bool, err error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false, errors.New("auth: unsupported password hash")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, false, errors.New("auth: unsupported argon2 version")
	}
	var p ArgonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return false, false, fmt.Errorf("auth: bad argon2 parameters: %w", err)
	}
	if p.Time < 1 || p.Time > 100 || p.Threads < 1 || p.MemoryKiB < 8*uint32(p.Threads) || p.MemoryKiB > 4<<20 {
		return false, false, errors.New("auth: argon2 parameters out of range")
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, false, err
	}
	want2, err := b64.DecodeString(parts[5])
	if err != nil || len(want2) < 16 || len(want2) > 128 {
		return false, false, errors.New("auth: bad argon2 hash")
	}
	got := argonKey([]byte(password), salt, p, uint32(len(want2)))
	ok = subtle.ConstantTimeCompare(got, want2) == 1
	needsRehash = p.Time != want.Time || p.MemoryKiB != want.MemoryKiB || p.Threads != want.Threads || uint32(len(want2)) != want.KeyLen
	return ok, needsRehash, nil
}

// GeneratePassword returns a random password of the given length from an
// alphabet without ambiguous characters.
func GeneratePassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	return randomString(alphabet, n)
}

func randomString(alphabet string, n int) string {
	out := make([]byte, n)
	buf := make([]byte, 1)
	limit := 256 - 256%len(alphabet)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if int(buf[0]) >= limit {
			continue
		}
		out[i] = alphabet[int(buf[0])%len(alphabet)]
		i++
	}
	return string(out)
}
