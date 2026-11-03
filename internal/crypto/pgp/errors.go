// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import "errors"

var (
	// ErrKeyNotFound is returned when a key is not present in a store or
	// could not be found by a lookup.
	ErrKeyNotFound = errors.New("pgp: key not found")
	// ErrNoKeyData is returned when the input does not contain any OpenPGP key.
	ErrNoKeyData = errors.New("pgp: no OpenPGP key data found")
	// ErrNotSecretKey is returned when an operation needs secret key material
	// but only a public key is available.
	ErrNotSecretKey = errors.New("pgp: key has no secret key material")
	// ErrKeyLocked is returned when an operation needs an unlocked
	// (decrypted) secret key.
	ErrKeyLocked = errors.New("pgp: secret key is locked")
	// ErrWrongPassphrase is returned when a passphrase does not unlock a key.
	ErrWrongPassphrase = errors.New("pgp: wrong passphrase")
	// ErrNoPassphrase is returned when a key needs a passphrase but no
	// PassphraseFunc is configured.
	ErrNoPassphrase = errors.New("pgp: passphrase required but no passphrase callback configured")
	// ErrNoEncryptionKey is returned when a key has no usable encryption
	// (sub)key, e.g. because it is expired or revoked.
	ErrNoEncryptionKey = errors.New("pgp: no usable encryption key")
	// ErrNoSigningKey is returned when a key has no usable signing (sub)key.
	ErrNoSigningKey = errors.New("pgp: no usable signing key")
	// ErrNoRecipients is returned when encrypting without recipients.
	ErrNoRecipients = errors.New("pgp: no recipients")
	// ErrNoDecryptionKey is returned when none of the available secret keys
	// can decrypt a message.
	ErrNoDecryptionKey = errors.New("pgp: no matching secret key to decrypt the message")
	// ErrMalformedMIME is returned for structurally invalid PGP/MIME input.
	ErrMalformedMIME = errors.New("pgp: malformed PGP/MIME message")
	// ErrInvalidAutocrypt is returned for unparsable or invalid Autocrypt headers.
	ErrInvalidAutocrypt = errors.New("pgp: invalid Autocrypt header")
)
