// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pgp implements the OpenPGP support of TuxMail on top of
// github.com/ProtonMail/go-crypto/openpgp/v2 (RFC 4880 v4 keys and
// RFC 9580 v6 keys).
//
// The package is organised in layers:
//
//   - Key handling (Key, GenerateKey, ReadKeys): parsing, export,
//     passphrase changes, revocation and introspection (fingerprints,
//     subkeys, expiry, capabilities).
//   - KeyRing: a thread-safe collection of keys backed by a pluggable
//     KeyStore. Secret key passphrases are requested on demand through a
//     PassphraseFunc so that the UI or the secret store can supply them.
//   - Trust: a small per-address/per-key trust model (unknown, TOFU,
//     verified, revoked, expired) with first-seen/last-seen bookkeeping.
//   - PGP/MIME (RFC 3156): building multipart/signed and
//     multipart/encrypted entities, protected headers, and decrypting and
//     verifying incoming PGP/MIME and inline PGP messages.
//   - Autocrypt Level 1: header generation/parsing, gossip, peer state
//     updates, the recommendation algorithm and Autocrypt Setup Messages.
//   - Composer helper (PlanEncryption): decides whether a message to a set
//     of recipients can and should be encrypted.
//
// Network key discovery lives in the subpackages wkd (Web Key Directory)
// and vks (keys.openpgp.org Verifying Keyserver); both implement
// KeyLookup.
package pgp
