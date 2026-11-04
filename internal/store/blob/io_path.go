// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

// Path returns the file of a blob (zstd-compressed content). It is used by
// backups, which copy the stored files as they are.
func (s *Store) Path(hash string) (string, error) { return s.path(hash) }

// Root returns the directory of the store.
func (s *Store) Root() string { return s.root }
