// SPDX-License-Identifier: AGPL-3.0-or-later

// Package blob implements a content-addressed file store (KONZEPT 5.1).
//
// Each blob is identified by the lowercase hex SHA-256 of its uncompressed
// content and stored zstd-compressed at <root>/ab/cd/<hash>. Writes are atomic
// (temporary file + rename), so a blob is either complete or absent.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob: not found")

// ErrInvalidHash is returned for strings that are not a lowercase hex SHA-256.
var ErrInvalidHash = errors.New("blob: invalid hash")

const tmpDir = "tmp"

// Store is a content-addressed blob store rooted at a directory. It is safe
// for concurrent use, also by several processes.
type Store struct {
	root string
}

// New opens (and creates if necessary) a blob store at root.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, tmpDir), 0o700); err != nil {
		return nil, fmt.Errorf("blob: %w", err)
	}
	return &Store{root: root}, nil
}

// ValidHash reports whether h is a lowercase hex SHA-256.
func ValidHash(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) path(h string) (string, error) {
	if !ValidHash(h) {
		return "", fmt.Errorf("%w: %q", ErrInvalidHash, h)
	}
	return filepath.Join(s.root, h[0:2], h[2:4], h), nil
}

// Put stores the content read from r and returns its hash. Storing content
// that already exists is cheap and returns the same hash.
func (s *Store) Put(r io.Reader) (hash string, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, tmpDir), "put-*")
	if err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	defer func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	enc, err := zstd.NewWriter(tmp, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(enc, io.TeeReader(r, h)); err != nil {
		enc.Close()
		return "", fmt.Errorf("blob: put: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	hash = hex.EncodeToString(h.Sum(nil))

	dst, _ := s.path(hash)
	if _, err := os.Stat(dst); err == nil {
		return hash, nil // deduplicated; the deferred cleanup removes tmp
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", fmt.Errorf("blob: put: %w", err)
	}
	tmp = nil
	return hash, nil
}

// Open returns a reader for the decompressed content of a blob.
func (s *Store) Open(hash string) (io.ReadCloser, error) {
	p, err := s.path(hash)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open: %w", err)
	}
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("blob: open %s: %w", hash, err)
	}
	return &reader{dec: dec, f: f}, nil
}

type reader struct {
	dec *zstd.Decoder
	f   *os.File
}

func (r *reader) Read(p []byte) (int, error) { return r.dec.Read(p) }

func (r *reader) Close() error {
	r.dec.Close()
	return r.f.Close()
}

// Has reports whether a blob exists.
func (s *Store) Has(hash string) (bool, error) {
	p, err := s.path(hash)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes a blob. Deleting a missing blob is not an error.
func (s *Store) Delete(hash string) error {
	p, err := s.path(hash)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: delete: %w", err)
	}
	return nil
}

// Walk calls fn for the hash of every stored blob (e.g. for garbage
// collection or incremental backups). Iteration stops at the first error.
func (s *Store) Walk(fn func(hash string) error) error {
	return filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != s.root && d.Name() == tmpDir && filepath.Dir(p) == s.root {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if ValidHash(name) && strings.HasSuffix(filepath.ToSlash(filepath.Dir(p)), "/"+name[0:2]+"/"+name[2:4]) {
			return fn(name)
		}
		return nil
	})
}
