// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Backup writes a consistent snapshot of the live database to dest using
// VACUUM INTO (KONZEPT 14.2). Other connections may keep reading and writing
// while it runs. dest must not exist; the snapshot is written to a temporary
// file in the same directory and renamed into place on success.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("store: backup: %s: %w", dest, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: backup: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	tmp := f.Name()
	f.Close()
	// VACUUM INTO refuses to write into an existing (even empty) file.
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	if _, err := s.q(ctx).ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: backup: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}
