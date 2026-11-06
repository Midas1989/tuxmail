// SPDX-License-Identifier: AGPL-3.0-or-later

// Package passkeytest is a software WebAuthn authenticator for tests: an ES256
// key pair that answers navigator.credentials.create/get option objects with
// "none" attestation and correctly signed assertions, the way a platform
// authenticator with user verification does. It is imported by test files only.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Authenticator is one virtual passkey. The exported fields may be changed
// between calls to simulate misbehaving or malicious clients.
type Authenticator struct {
	Key          *ecdsa.PrivateKey
	CredentialID []byte
	UserHandle   []byte // learnt at Create
	SignCount    uint32
	// CountStep is added to SignCount before every assertion (default 1; set 0
	// to simulate a synced passkey that always reports counter 0 together with
	// SignCount = 0).
	CountStep uint32
	AAGUID    [16]byte

	// Origin and RPID are what the "browser" and "authenticator" use. Tests
	// set them to something else to simulate a phishing site.
	Origin string
	RPID   string

	// UV and UP are the flags set in authenticator data.
	UV, UP bool
	// BackupEligible / BackupState are the BE and BS flags.
	BackupEligible, BackupState bool
	// Transports reported at registration.
	Transports []string
}

// New returns an authenticator for origin / rpID with user verification.
func New(origin, rpID string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	a := &Authenticator{Key: key, CredentialID: id, Origin: origin, RPID: rpID, UV: true, UP: true, CountStep: 1,
		BackupEligible: true, Transports: []string{"internal", "hybrid"}}
	rand.Read(a.AAGUID[:])
	return a
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("passkeytest: bad base64url %q: %v", s, err))
	}
	return b
}

func (a *Authenticator) flags(attested bool) byte {
	var f byte
	if a.UP {
		f |= 0x01
	}
	if a.UV {
		f |= 0x04
	}
	if a.BackupEligible {
		f |= 0x08
	}
	if a.BackupState {
		f |= 0x10
	}
	if attested {
		f |= 0x40
	}
	return f
}

// cbor head: major type and argument.
func head(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	case n < 65536:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	default:
		panic("passkeytest: cbor value too large")
	}
}

func cborInt(v int64) []byte {
	if v >= 0 {
		return head(0, uint64(v))
	}
	return head(1, uint64(-1-v))
}
func cborBytes(b []byte) []byte { return append(head(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(head(3, uint64(len(s))), s...) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (a *Authenticator) coseKey() []byte {
	pub, _ := a.Key.PublicKey.ECDH()
	raw := pub.Bytes() // 0x04 || X || Y
	return cat(head(5, 5),
		cborInt(1), cborInt(2), // kty EC2
		cborInt(3), cborInt(-7), // alg ES256
		cborInt(-1), cborInt(1), // crv P-256
		cborInt(-2), cborBytes(raw[1:33]),
		cborInt(-3), cborBytes(raw[33:65]))
}

func (a *Authenticator) rpHash() []byte {
	h := sha256.Sum256([]byte(a.RPID))
	return h[:]
}

func counter(n uint32) []byte {
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], n)
	return c[:]
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

type creationOptions struct {
	Challenge string `json:"challenge"`
	User      struct {
		ID string `json:"id"`
	} `json:"user"`
}

type requestOptions struct {
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
}

// ParseChallenge returns the challenge of an options object ({"publicKey":{...}} or the inner object).
func ParseChallenge(options []byte) string {
	var w struct {
		PublicKey *requestOptions `json:"publicKey"`
		requestOptions
	}
	if err := json.Unmarshal(options, &w); err != nil {
		panic(err)
	}
	if w.PublicKey != nil {
		return w.PublicKey.Challenge
	}
	return w.Challenge
}

// Create answers navigator.credentials.create(options) and returns the
// credential as PublicKeyCredential.toJSON() would.
func (a *Authenticator) Create(options []byte) []byte {
	var w struct {
		PublicKey *creationOptions `json:"publicKey"`
		creationOptions
	}
	if err := json.Unmarshal(options, &w); err != nil {
		panic(err)
	}
	o := w.creationOptions
	if w.PublicKey != nil {
		o = *w.PublicKey
	}
	a.UserHandle = unb64(o.User.ID)
	cose := a.coseKey()
	authData := cat(a.rpHash(), []byte{a.flags(true)}, counter(a.SignCount), a.AAGUID[:],
		[]byte{byte(len(a.CredentialID) >> 8), byte(len(a.CredentialID))}, a.CredentialID, cose)
	attObj := cat(head(5, 3),
		cborText("fmt"), cborText("none"),
		cborText("attStmt"), head(5, 0),
		cborText("authData"), cborBytes(authData))
	spki, _ := x509.MarshalPKIXPublicKey(&a.Key.PublicKey)
	out := map[string]any{
		"id": b64(a.CredentialID), "rawId": b64(a.CredentialID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":     b64(clientData("webauthn.create", o.Challenge, a.Origin)),
			"attestationObject":  b64(attObj),
			"authenticatorData":  b64(authData),
			"publicKey":          b64(spki),
			"publicKeyAlgorithm": -7,
			"transports":         a.Transports,
		},
	}
	js, _ := json.Marshal(out)
	return js
}

// Get answers navigator.credentials.get(options) with a discoverable
// credential: the response carries the user handle.
func (a *Authenticator) Get(options []byte) []byte {
	return a.GetChallenge(ParseChallenge(options))
}

// GetChallenge signs an assertion for the given challenge (base64url).
func (a *Authenticator) GetChallenge(challenge string) []byte {
	a.SignCount += a.CountStep
	return a.assertion(challenge, a.SignCount)
}

// GetWithCounter signs an assertion that reports exactly counter.
func (a *Authenticator) GetWithCounter(challenge string, counterValue uint32) []byte {
	a.SignCount = counterValue
	return a.assertion(challenge, counterValue)
}

func (a *Authenticator) assertion(challenge string, count uint32) []byte {
	authData := cat(a.rpHash(), []byte{a.flags(false)}, counter(count))
	cd := clientData("webauthn.get", challenge, a.Origin)
	cdh := sha256.Sum256(cd)
	sum := sha256.Sum256(cat(authData, cdh[:]))
	sig, err := ecdsa.SignASN1(rand.Reader, a.Key, sum[:])
	if err != nil {
		panic(err)
	}
	out := map[string]any{
		"id": b64(a.CredentialID), "rawId": b64(a.CredentialID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64(cd),
			"authenticatorData": b64(authData),
			"signature":         b64(sig),
			"userHandle":        b64(a.UserHandle),
		},
	}
	js, _ := json.Marshal(out)
	return js
}
