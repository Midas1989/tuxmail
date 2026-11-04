// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/Midas1989/Mailclient/internal/domain"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Vault errors.
var (
	ErrLocked        = errors.New("secrets: vault is locked")
	ErrWrongPassword = errors.New("secrets: wrong master password or corrupted vault")
	ErrVaultFormat   = errors.New("secrets: not a valid vault file")
)

// Vault file format, version 1 (all integers big-endian):
//
//	offset size
//	0      8    magic "TUXVAULT"
//	8      1    format version (1)
//	9      1    KDF id (1 = Argon2id)
//	10     4    Argon2id time (iterations)
//	14     4    Argon2id memory in KiB
//	18     1    Argon2id threads
//	19     16   salt
//	35     24   XChaCha20-Poly1305 nonce
//	59     ...  ciphertext + 16 byte tag of the JSON payload
//
// The complete header (bytes 0..58) is authenticated as associated data. A new
// nonce is generated on every save; the salt changes with the password.
const (
	vaultMagic     = "TUXVAULT"
	vaultVersion   = 1
	kdfArgon2id    = 1
	saltLen        = 16
	headerLen      = 8 + 1 + 1 + 4 + 4 + 1 + saltLen + chacha20poly1305.NonceSizeX
	maxArgonMemKiB = 1 << 20 // 1 GiB; protects against crafted headers (AUDIT-C)
	maxArgonTime   = 20
)

// KDFParams are the Argon2id parameters used to derive the vault key.
type KDFParams struct {
	Time      uint32 // iterations
	MemoryKiB uint32
	Threads   uint8
}

// DefaultKDFParams follow the second recommendation of RFC 9106
// (t=3, m=64 MiB, p=4).
var DefaultKDFParams = KDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

func (p KDFParams) valid() bool {
	return p.Time >= 1 && p.Time <= maxArgonTime && p.MemoryKiB >= 8*uint32(p.Threads) &&
		p.MemoryKiB <= maxArgonMemKiB && p.Threads >= 1
}

func (p KDFParams) derive(password, salt []byte) []byte {
	return argon2.IDKey(password, salt, p.Time, p.MemoryKiB, p.Threads, chacha20poly1305.KeySize)
}

type payload struct {
	Secrets map[string][]byte `json:"secrets"`
}

// Vault is a password-protected secret file for portable mode (KONZEPT 15.2,
// 17.2). It implements domain.SecretStore; all methods except Unlock,
// ChangePassword and Locked return ErrLocked while the vault is locked.
// A Vault is safe for concurrent use within one process; it must not be
// opened by several processes at the same time.
type Vault struct {
	path string

	mu      sync.Mutex
	params  KDFParams
	salt    []byte
	key     []byte // nil while locked
	secrets map[string][]byte
}

var _ domain.SecretStore = (*Vault)(nil)

// CreateVault creates a new, empty vault file at path protected by password
// and returns it unlocked. It fails if the file already exists.
func CreateVault(path string, password []byte, params KDFParams) (*Vault, error) {
	if !params.valid() {
		return nil, fmt.Errorf("secrets: invalid KDF parameters %+v", params)
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("secrets: create vault %s: %w", path, fs.ErrExist)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	v := &Vault{path: path, params: params, salt: salt, key: params.derive(password, salt), secrets: map[string][]byte{}}
	if err := v.save(v.secrets); err != nil {
		return nil, err
	}
	return v, nil
}

// OpenVault opens an existing vault file in the locked state. Only the header
// is validated; call Unlock with the master password before use.
func OpenVault(path string) (*Vault, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: open vault: %w", err)
	}
	params, salt, err := parseHeader(data)
	if err != nil {
		return nil, err
	}
	return &Vault{path: path, params: params, salt: salt}, nil
}

func parseHeader(data []byte) (KDFParams, []byte, error) {
	if len(data) < headerLen+chacha20poly1305.Overhead || string(data[:8]) != vaultMagic {
		return KDFParams{}, nil, ErrVaultFormat
	}
	if data[8] != vaultVersion {
		return KDFParams{}, nil, fmt.Errorf("%w: unsupported version %d", ErrVaultFormat, data[8])
	}
	if data[9] != kdfArgon2id {
		return KDFParams{}, nil, fmt.Errorf("%w: unsupported KDF %d", ErrVaultFormat, data[9])
	}
	p := KDFParams{
		Time:      binary.BigEndian.Uint32(data[10:14]),
		MemoryKiB: binary.BigEndian.Uint32(data[14:18]),
		Threads:   data[18],
	}
	if !p.valid() {
		return KDFParams{}, nil, fmt.Errorf("%w: bad KDF parameters %+v", ErrVaultFormat, p)
	}
	return p, bytes.Clone(data[19 : 19+saltLen]), nil
}

// Path returns the vault file path.
func (v *Vault) Path() string { return v.path }

// Locked reports whether the vault is locked.
func (v *Vault) Locked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.key == nil
}

// Unlock reads the vault file and decrypts it with password. It returns
// ErrWrongPassword if the password is wrong or the file was tampered with.
func (v *Vault) Unlock(password []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.unlock(password)
}

func (v *Vault) unlock(password []byte) error {
	data, err := os.ReadFile(v.path)
	if err != nil {
		return fmt.Errorf("secrets: unlock: %w", err)
	}
	params, salt, err := parseHeader(data)
	if err != nil {
		return err
	}
	key := params.derive(password, salt)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	header := data[:headerLen]
	plain, err := aead.Open(nil, header[headerLen-chacha20poly1305.NonceSizeX:], data[headerLen:], header)
	if err != nil {
		return ErrWrongPassword
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return fmt.Errorf("%w: bad payload: %v", ErrVaultFormat, err)
	}
	if p.Secrets == nil {
		p.Secrets = map[string][]byte{}
	}
	v.params, v.salt, v.key, v.secrets = params, salt, key, p.Secrets
	return nil
}

// Lock forgets the key and all decrypted secrets.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.key)
	for _, s := range v.secrets {
		clear(s)
	}
	v.key, v.secrets = nil, nil
}

// ChangePassword re-encrypts the vault with a key derived from newPassword and
// a fresh salt. oldPassword must be the current master password. The vault is
// unlocked afterwards.
func (v *Vault) ChangePassword(oldPassword, newPassword []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		if err := v.unlock(oldPassword); err != nil {
			return err
		}
	} else if subtle.ConstantTimeCompare(v.params.derive(oldPassword, v.salt), v.key) != 1 {
		return ErrWrongPassword
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	oldSalt, oldKey := v.salt, v.key
	v.salt, v.key = salt, v.params.derive(newPassword, salt)
	if err := v.save(v.secrets); err != nil {
		v.salt, v.key = oldSalt, oldKey
		return err
	}
	clear(oldKey)
	return nil
}

// Get returns a copy of the secret stored under ref.
func (v *Vault) Get(ref string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return nil, ErrLocked
	}
	s, ok := v.secrets[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(s), nil
}

// Set stores value under ref and saves the vault.
func (v *Vault) Set(ref string, value []byte) error {
	return v.update(func(m map[string][]byte) { m[ref] = clone(value) })
}

// Delete removes ref and saves the vault; a missing ref is not an error.
func (v *Vault) Delete(ref string) error {
	return v.update(func(m map[string][]byte) { delete(m, ref) })
}

// update applies fn to a copy of the secrets, saves it and only then makes it current.
func (v *Vault) update(fn func(map[string][]byte)) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	next := maps.Clone(v.secrets)
	fn(next)
	if err := v.save(next); err != nil {
		return err
	}
	v.secrets = next
	return nil
}

// save encrypts secrets with the current key and atomically replaces the file.
func (v *Vault) save(secrets map[string][]byte) error {
	plain, err := json.Marshal(payload{Secrets: secrets})
	if err != nil {
		return err
	}
	defer clear(plain)
	aead, err := chacha20poly1305.NewX(v.key)
	if err != nil {
		return err
	}
	header := make([]byte, headerLen, headerLen+len(plain)+aead.Overhead())
	copy(header, vaultMagic)
	header[8], header[9] = vaultVersion, kdfArgon2id
	binary.BigEndian.PutUint32(header[10:14], v.params.Time)
	binary.BigEndian.PutUint32(header[14:18], v.params.MemoryKiB)
	header[18] = v.params.Threads
	copy(header[19:], v.salt)
	nonce := header[19+saltLen:]
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := aead.Seal(header, nonce, plain, header)
	if err := writeFileAtomic(v.path, data); err != nil {
		return fmt.Errorf("secrets: save vault: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temporary file next to path, syncs it and
// renames it over path, so readers see either the old or the new content.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// os.CreateTemp creates the file with mode 0600.
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	// Persist the rename; not supported on every platform, hence best effort.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
