// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sealed encrypts data for a client's X25519 public key (an ECIES
// construction), used to hand account secrets to a desktop client during
// provisioning without them ever appearing in plain text in transit logs,
// proxies or TLS-terminating middleboxes.
//
// Construction (version 1):
//
//	eph        = fresh X25519 key pair
//	shared     = X25519(eph.private, client.public)
//	key        = HKDF-SHA256(ikm=shared, salt=eph.public||client.public, info=Info, 32 bytes)
//	ciphertext = XChaCha20-Poly1305(key, nonce=random 24 bytes, ad=Info)
package sealed

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// Alg names the construction in Box.Alg.
const Alg = "X25519-HKDF-SHA256-XChaCha20Poly1305"

// Info is the HKDF info string and AEAD associated data.
const Info = "TuxMail provisioning secrets v1"

// Box is the JSON form of a sealed message (all fields base64url, unpadded).
type Box struct {
	Alg                string `json:"alg"`
	EphemeralPublicKey string `json:"ephemeralPublicKey"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

var b64 = base64.RawURLEncoding

func deriveKey(shared, ephPub, clientPub []byte) ([]byte, error) {
	salt := append(append([]byte{}, ephPub...), clientPub...)
	key := make([]byte, chacha20poly1305.KeySize)
	_, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte(Info)), key)
	return key, err
}

// Seal encrypts plain for the 32-byte X25519 public key clientPub.
func Seal(clientPub, plain []byte) (*Box, error) {
	pub, err := ecdh.X25519().NewPublicKey(clientPub)
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(shared, eph.PublicKey().Bytes(), clientPub)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plain, []byte(Info))
	return &Box{Alg: Alg, EphemeralPublicKey: b64.EncodeToString(eph.PublicKey().Bytes()),
		Nonce: b64.EncodeToString(nonce), Ciphertext: b64.EncodeToString(ct)}, nil
}

// Open decrypts a Box with the client's private key (client side).
func Open(priv *ecdh.PrivateKey, box *Box) ([]byte, error) {
	if box.Alg != Alg {
		return nil, errors.New("sealed: unsupported algorithm")
	}
	ephPub, err1 := b64.DecodeString(box.EphemeralPublicKey)
	nonce, err2 := b64.DecodeString(box.Nonce)
	ct, err3 := b64.DecodeString(box.Ciphertext)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(ephPub)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	key, err := deriveKey(shared, ephPub, priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("sealed: bad nonce")
	}
	return aead.Open(nil, nonce, ct, []byte(Info))
}
