// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingFile is the desktop log file <data_dir>/logs/tuxmail.log: when it
// exceeds maxSize it is renamed to tuxmail.log.1 (older files shift up to
// tuxmail.log.<keep>, the oldest is dropped). The Windows GUI build has no
// console, so this file is the only log (Settings → Info & Diagnose
// exports it).
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	f       *os.File
	size    int64
}

const (
	logMaxSize = 5 << 20
	logKeep    = 3
)

// openLogFile opens (appends to) dir/tuxmail.log.
func openLogFile(dir string, maxSize int64, keep int) (*rotatingFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &rotatingFile{path: filepath.Join(dir, "tuxmail.log"), maxSize: maxSize, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Path is the current log file.
func (r *rotatingFile) Path() string { return r.path }

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if r.keep > 0 {
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			return err
		}
	} else {
		_ = os.Remove(r.path)
	}
	return r.open()
}

// Close closes the file.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
