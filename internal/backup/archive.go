// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backup creates and restores TuxMail backups (KONZEPT 14).
//
// An archive ("tuxmail-backup-….txmb") is a tar stream compressed with zstd
// and, when a passphrase is given, encrypted with age (scrypt recipient).
// The first entry is manifest.json (format version, schema version, scope,
// accounts, SHA-256 of every file); then follow db/tuxmail.db (a VACUUM INTO
// snapshot pruned to the chosen scope), secrets.json (only on request and
// only encrypted), and blobs/ab/cd/<hash> (the zstd files of the blob
// store, copied as-is). Blobs are verified by their content hash, the other
// files by the manifest checksums; age authenticates the whole stream.
//
// Incremental archives contain only blobs that are not listed in the
// manifest of their base archive (the blob store is content-addressed).
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

// FormatVersion is the archive format written by this program.
const FormatVersion = 1

// Extension is the file extension of backup archives.
const Extension = ".txmb"

// Errors.
var (
	ErrWrongPassphrase = errors.New("backup: wrong passphrase")
	ErrNeedPassphrase  = errors.New("backup: the archive is encrypted; a passphrase is required")
	ErrTampered        = errors.New("backup: archive is damaged or was modified")
	ErrNotArchive      = errors.New("backup: not a TuxMail backup archive")
	ErrMissingBase     = errors.New("backup: incremental archive needs its base archive")
	ErrTooNew          = errors.New("backup: archive was written by a newer TuxMail version")
	// ErrNotEncrypted is returned when a passphrase is given but the archive
	// is not encrypted: an attacker who can write to the backup target could
	// otherwise replace an encrypted archive by an unauthenticated one
	// (downgrade, AUDIT-C).
	ErrNotEncrypted = errors.New("backup: a passphrase was given but the archive is not encrypted")
)

// scryptWorkFactor overrides the age default (tests only).
var scryptWorkFactor int

// Archive entry names.
const (
	manifestName = "manifest.json"
	dbName       = "db/tuxmail.db"
	secretsName  = "secrets.json"
	blobPrefix   = "blobs/"
	serverPrefix = "server/"
)

// FileEntry is a checksummed file of an archive.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// AccountInfo describes an account contained in a backup.
type AccountInfo struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Address   string `json:"address,omitempty"`
	Messenger bool   `json:"messenger,omitempty"`
	Messages  int    `json:"messages"`
}

// Manifest describes an archive.
type Manifest struct {
	Format        int            `json:"format"`
	ID            string         `json:"id"`
	Created       time.Time      `json:"created"`
	AppVersion    string         `json:"appVersion,omitempty"`
	SchemaVersion int            `json:"schemaVersion"`
	Scope         Scope          `json:"scope"`
	Encrypted     bool           `json:"encrypted"`
	Secrets       bool           `json:"secrets"`
	BaseID        string         `json:"baseId,omitempty"` // incremental: manifest id of the base
	Files         []FileEntry    `json:"files"`
	Blobs         []string       `json:"blobs"`         // all blobs the snapshot needs
	BlobsIncluded int            `json:"blobsIncluded"` // blobs stored in this archive
	Accounts      []AccountInfo  `json:"accounts"`
	Counts        map[string]int `json:"counts,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
	// Server marks an admin full-server backup (tuxmail backup --server).
	Server bool `json:"server,omitempty"`
}

// Incremental reports whether the archive depends on a base archive.
func (m *Manifest) Incremental() bool { return m.BaseID != "" }

// HasMessenger reports whether messenger accounts are included.
func (m *Manifest) HasMessenger() bool {
	for _, a := range m.Accounts {
		if a.Messenger {
			return true
		}
	}
	return false
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ==== writing ==================================================================

// writer is an open archive stream.
type writer struct {
	enc io.WriteCloser // age (or nil)
	zw  *zstd.Encoder
	tw  *tar.Writer
}

func newWriter(w io.Writer, passphrase string) (*writer, error) {
	out := &writer{}
	dst := w
	if passphrase != "" {
		rcp, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return nil, err
		}
		if scryptWorkFactor > 0 {
			rcp.SetWorkFactor(scryptWorkFactor)
		}
		enc, err := age.Encrypt(w, rcp)
		if err != nil {
			return nil, err
		}
		out.enc, dst = enc, enc
	}
	zw, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	out.zw = zw
	out.tw = tar.NewWriter(zw)
	return out, nil
}

func (w *writer) file(name string, mod time.Time, size int64, r io.Reader) error {
	if err := w.tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: mod, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	n, err := io.Copy(w.tw, r)
	if err == nil && n != size {
		err = fmt.Errorf("backup: %s changed while it was written", name)
	}
	return err
}

func (w *writer) bytes(name string, mod time.Time, b []byte) error {
	return w.file(name, mod, int64(len(b)), bytes.NewReader(b))
}

func (w *writer) close() error {
	if err := w.tw.Close(); err != nil {
		return err
	}
	if err := w.zw.Close(); err != nil {
		return err
	}
	if w.enc != nil {
		return w.enc.Close()
	}
	return nil
}

// ==== reading ==================================================================

var (
	ageMagic  = []byte("age-encryption.org/")
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// reader is an open archive stream.
type reader struct {
	zr        *zstd.Decoder
	tr        *tar.Reader
	encrypted bool
	in        *countingReader // compressed bytes read from the source
}

// countingReader counts the bytes read from the (compressed) archive; the
// extraction limits compare it with the bytes the archive expands to.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func openReader(r io.Reader, passphrase string) (*reader, error) {
	cr := &countingReader{r: r}
	br := bufio.NewReader(cr)
	head, _ := br.Peek(len(ageMagic))
	src := io.Reader(br)
	encrypted := false
	switch {
	case bytes.HasPrefix(head, ageMagic):
		encrypted = true
		if passphrase == "" {
			return nil, ErrNeedPassphrase
		}
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return nil, err
		}
		dec, err := age.Decrypt(br, id)
		if err != nil {
			var nm *age.NoIdentityMatchError
			if errors.As(err, &nm) || bytes.Contains([]byte(err.Error()), []byte("incorrect")) {
				return nil, ErrWrongPassphrase
			}
			return nil, fmt.Errorf("%w: %v", ErrTampered, err)
		}
		src = dec
	case bytes.HasPrefix(head, zstdMagic):
		if passphrase != "" {
			return nil, ErrNotEncrypted
		}
	default:
		return nil, ErrNotArchive
	}
	zr, err := zstd.NewReader(src, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	return &reader{zr: zr, tr: tar.NewReader(zr), encrypted: encrypted, in: cr}, nil
}

func (r *reader) close() { r.zr.Close() }

// next returns the next entry; stream errors are reported as ErrTampered.
func (r *reader) next() (*tar.Header, error) {
	h, err := r.tr.Next()
	if err == io.EOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	return h, nil
}

func (r *reader) manifest() (*Manifest, error) {
	h, err := r.next()
	if err == io.EOF {
		return nil, ErrNotArchive
	}
	if err != nil {
		return nil, err
	}
	if h.Name != manifestName || h.Size > 256<<20 {
		return nil, ErrNotArchive
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r.tr, h.Size)).Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrTampered, err)
	}
	if m.Format > FormatVersion {
		return nil, fmt.Errorf("%w (format %d)", ErrTooNew, m.Format)
	}
	if m.Format == 0 {
		return nil, ErrNotArchive
	}
	if m.Encrypted != r.encrypted {
		return nil, fmt.Errorf("%w: encryption flag does not match", ErrTampered)
	}
	return &m, nil
}

// Inspect reads only the manifest of an archive.
func Inspect(r io.Reader, passphrase string) (*Manifest, error) {
	ar, err := openReader(r, passphrase)
	if err != nil {
		return nil, err
	}
	defer ar.close()
	return ar.manifest()
}

// IsEncrypted reports whether the archive starts with an age header.
func IsEncrypted(r io.Reader) (bool, error) {
	head := make([]byte, len(ageMagic))
	n, err := io.ReadFull(r, head)
	if err != nil && n == 0 {
		return false, ErrNotArchive
	}
	return bytes.HasPrefix(head[:n], ageMagic), nil
}
