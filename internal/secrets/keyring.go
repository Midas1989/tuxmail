// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/zalando/go-keyring"
)

// KeyringService is the service name under which secrets are stored in the OS keyring.
const KeyringService = "TuxMail"

// Keyring stores secrets in the operating system's keyring (Windows Credential
// Manager, Secret Service/KWallet on Linux, Keychain on macOS). Values are
// base64-encoded because the keyring API only stores strings.
type Keyring struct {
	service string
}

var _ domain.SecretStore = (*Keyring)(nil)

// NewKeyring returns a keyring store using KeyringService.
func NewKeyring() *Keyring { return &Keyring{service: KeyringService} }

// Get returns the secret stored under ref.
func (k *Keyring) Get(ref string) ([]byte, error) {
	s, err := keyring.Get(k.service, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: keyring get: %w", err)
	}
	v, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("secrets: keyring get %q: corrupt value: %w", ref, err)
	}
	return v, nil
}

// Set stores value under ref.
func (k *Keyring) Set(ref string, value []byte) error {
	if err := keyring.Set(k.service, ref, base64.StdEncoding.EncodeToString(value)); err != nil {
		return fmt.Errorf("secrets: keyring set: %w", err)
	}
	return nil
}

// Delete removes the secret stored under ref; a missing ref is not an error.
func (k *Keyring) Delete(ref string) error {
	err := keyring.Delete(k.service, ref)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("secrets: keyring delete: %w", err)
	}
	return nil
}

// Available probes whether an OS keyring is usable (e.g. a Secret Service
// daemon is running). Callers can fall back to the vault if it is not.
func (k *Keyring) Available() bool {
	_, err := keyring.Get(k.service, "__tuxmail_probe__")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
