// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
)

// vaultKDF is deliberately cheap: the vault password is a random 256-bit DEK,
// so key stretching adds no security.
var vaultKDF = secrets.KDFParams{Time: 1, MemoryKiB: 64, Threads: 1}

// userDEK unwraps the user's data encryption key with the master key.
func (s *Service) userDEK(ctx context.Context, userID int64) ([]byte, error) {
	var wrapped []byte
	if err := s.db.QueryRowContext(ctx, `SELECT dek_wrapped FROM users WHERE id = ?`, userID).Scan(&wrapped); err != nil {
		return nil, ErrNotFound
	}
	return s.open(wrapped, fmt.Sprintf("dek:%d", userID))
}

// UserSecrets returns the user's secret store (mail passwords, OAuth tokens)
// backed by an internal/secrets Vault at data_dir/users/<id>/secrets.vault,
// unlocked with the user's DEK (see package documentation). Vaults are cached
// and shared; the returned store is safe for concurrent use.
func (s *Service) UserSecrets(ctx context.Context, userID int64) (domain.SecretStore, error) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	if v, ok := s.vaults[userID]; ok {
		return v, nil
	}
	dek, err := s.userDEK(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	pw := []byte(base64.StdEncoding.EncodeToString(dek))
	defer clear(pw)
	if err := os.MkdirAll(s.UserDir(userID), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.UserDir(userID), "secrets.vault")
	v, err := secrets.OpenVault(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v, err = secrets.CreateVault(path, pw, vaultKDF)
	case err == nil:
		err = v.Unlock(pw)
	}
	if err != nil {
		return nil, fmt.Errorf("auth: user secrets: %w", err)
	}
	s.vaults[userID] = v
	return v, nil
}

func (s *Service) dropVault(userID int64) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	if v, ok := s.vaults[userID]; ok {
		v.Lock()
		delete(s.vaults, userID)
	}
}
