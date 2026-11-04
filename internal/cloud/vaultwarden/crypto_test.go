// SPDX-License-Identifier: AGPL-3.0-or-later

package vaultwarden

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// Known answers computed independently with Node.js crypto (pbkdf2Sync,
// hkdfSync, createCipheriv/createHmac) following the Bitwarden client
// algorithms.
const (
	katMasterKey = "f0604f8492a55ec503deb11fec73119a4325dafa93d737ed8ec5837f57e8d3e9"
	katHash      = "xfhcWVbbpzNGYAAN1KziRm0+y4B9Dd11uXDgzDHBYLE="
	katStretched = "5fde6d3bacab07f28b43a93d94d8d177c486cde21af391c16e6f018511717861" +
		"333a28c460fbb8aac95545aeee3ac5e0ed58a734b743da5e1a30149d3987ebf2"
	katSendKey = "063a955a1c01b4e1aacb7cd359c919b2cb61041c0c05c94c0eb76fb9a9e144b3" +
		"fbeca074de1437a1a2330ad2833138abf3be2561a3a30ae1f33e15e1a2f7ca04"
	katSendPassword = "nBr2FSqwZ4Isht7GtfyrzSYod/Vs1hc5n+cyDRF7gCo="
	katEncString    = "2.EBESExQVFhcYGRobHB0eHw==|Rfsh4QOjsk6Gkrd7Krbi1w==|Y+Ln7+LdhEuhqZwVX/UJhJcEXjjp57oIOIxVbTEIjNI="
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMasterKeyKAT(t *testing.T) {
	mk, err := MasterKey("p4ssw0rd", "  User@Example.com ", KDFParams{Type: KDFPBKDF2, Iterations: 5000})
	if err != nil || hex.EncodeToString(mk) != katMasterKey {
		t.Fatalf("master key %x %v", mk, err)
	}
	if h := MasterPasswordHash(mk, "p4ssw0rd"); h != katHash {
		t.Errorf("hash %s", h)
	}
	if s := StretchMasterKey(mk); hex.EncodeToString(s) != katStretched {
		t.Errorf("stretched %x", []byte(s))
	}
}

func TestArgon2idMasterKey(t *testing.T) {
	k := KDFParams{Type: KDFArgon2id, Iterations: 3, Memory: 16, Parallelism: 1}
	mk, err := MasterKey("p4ssw0rd", "User@example.com", k)
	if err != nil {
		t.Fatal(err)
	}
	salt := sha256.Sum256([]byte("user@example.com"))
	if want := argon2.IDKey([]byte("p4ssw0rd"), salt[:], 3, 16*1024, 1, 32); !bytes.Equal(mk, want) {
		t.Errorf("argon2id master key %x", mk)
	}
	if _, err := MasterKey("x", "y", KDFParams{Type: KDFArgon2id}); err == nil {
		t.Error("invalid argon2 params accepted")
	}
	if _, err := MasterKey("x", "y", KDFParams{Type: 9, Iterations: 1}); err == nil {
		t.Error("unknown kdf accepted")
	}
}

// AUDIT-C: a malicious server must not downgrade the KDF (offline
// brute force of the master password hash) or make the client allocate
// huge amounts of memory.
func TestKDFDowngradeRejected(t *testing.T) {
	for _, k := range []KDFParams{
		{Type: KDFPBKDF2, Iterations: 1},
		{Type: KDFPBKDF2, Iterations: 4999},
		{Type: KDFPBKDF2, Iterations: 1 << 30},
		{Type: KDFArgon2id, Iterations: 1, Memory: 64, Parallelism: 4},
		{Type: KDFArgon2id, Iterations: 3, Memory: 1, Parallelism: 4},
		{Type: KDFArgon2id, Iterations: 3, Memory: 1 << 20, Parallelism: 4},
		{Type: KDFArgon2id, Iterations: 3, Memory: 64, Parallelism: 200},
		{Type: KDFArgon2id, Iterations: 1000, Memory: 64, Parallelism: 4},
	} {
		if _, err := MasterKey("pw", "a@b", k); !errors.Is(err, ErrWeakKDF) {
			t.Errorf("%+v: err = %v, want ErrWeakKDF", k, err)
		}
	}
	for _, k := range []KDFParams{{Type: KDFPBKDF2, Iterations: 600000}, {Type: KDFArgon2id, Iterations: 3, Memory: 64, Parallelism: 4}} {
		if err := k.Validate(); err != nil {
			t.Errorf("%+v: %v", k, err)
		}
	}
}

func TestSendKeyKAT(t *testing.T) {
	material := unhex("000102030405060708090a0b0c0d0e0f")
	key := SendKey(material)
	if hex.EncodeToString(key) != katSendKey {
		t.Fatalf("send key %x", []byte(key))
	}
	if h := SendPasswordHash("geheim", material); h != katSendPassword {
		t.Errorf("send password hash %s", h)
	}
	e, err := key.encryptIV([]byte("Angebot.pdf"), unhex("101112131415161718191a1b1c1d1e1f"))
	if err != nil || e.String() != katEncString {
		t.Fatalf("enc string %s %v", e, err)
	}
	if got := base64.RawURLEncoding.EncodeToString(material); got != "AAECAwQFBgcICQoLDA0ODw" {
		t.Errorf("url key %s", got)
	}
	parsed, err := ParseEncString(katEncString)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := key.Decrypt(parsed)
	if err != nil || string(plain) != "Angebot.pdf" {
		t.Fatalf("decrypt %q %v", plain, err)
	}
	parsed.MAC[0] ^= 1
	if _, err := key.Decrypt(parsed); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Errorf("tampered MAC accepted: %v", err)
	}
}

func TestEncBufferRoundTrip(t *testing.T) {
	key := SendKey([]byte("0123456789abcdef"))
	data := bytes.Repeat([]byte("PDF"), 1000)
	buf, err := key.EncryptBuffer(data)
	if err != nil || buf[0] != 2 || len(buf) != 1+16+32+3008 {
		t.Fatalf("buffer len %d %v", len(buf), err)
	}
	got, err := key.DecryptBuffer(buf)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip %v", err)
	}
	for _, bad := range []string{"", "2.a|b", "3.x|y|z", "2.AAAA|AAAA|AAAA"} {
		if _, err := ParseEncString(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
