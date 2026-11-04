// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
)

// TeamDir returns data_dir/teams/<id>, the data directory of a shared team
// mailbox (KONZEPT 8.14, 15.6: shared mailboxes live in their own area).
func (s *Service) TeamDir(id int64) string {
	return filepath.Join(s.dataDir, "teams", fmt.Sprint(id))
}

// TeamSecrets returns the secret store of a team mailbox (its mail password
// or OAuth token): an internal/secrets Vault at data_dir/teams/<id>/secrets.vault
// whose password is derived from the server master key (HMAC-SHA256 over
// "team-vault:<id>"), so it is protected by the KEK like the user DEKs.
// Vaults are cached next to the user vaults (under the negated id).
func (s *Service) TeamSecrets(teamID int64) (domain.SecretStore, error) {
	if teamID <= 0 {
		return nil, ErrInvalidInput
	}
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	if v, ok := s.vaults[-teamID]; ok {
		return v, nil
	}
	mac := hmac.New(sha256.New, s.master)
	fmt.Fprintf(mac, "team-vault:%d", teamID)
	key := mac.Sum(nil)
	defer clear(key)
	pw := []byte(base64.StdEncoding.EncodeToString(key))
	defer clear(pw)
	dir := s.TeamDir(teamID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "secrets.vault")
	v, err := secrets.OpenVault(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v, err = secrets.CreateVault(path, pw, vaultKDF)
	case err == nil:
		err = v.Unlock(pw)
	}
	if err != nil {
		return nil, fmt.Errorf("auth: team secrets: %w", err)
	}
	s.vaults[-teamID] = v
	return v, nil
}

// DropTeamSecrets locks and forgets the cached vault of a deleted team mailbox.
func (s *Service) DropTeamSecrets(teamID int64) { s.dropVault(-teamID) }
