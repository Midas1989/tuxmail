// SPDX-License-Identifier: AGPL-3.0-or-later

package sealed

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

func TestRoundTripAndTamper(t *testing.T) {
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	box, err := Seal(priv.PublicKey().Bytes(), []byte(`{"secrets":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(priv, box)
	if err != nil || string(got) != `{"secrets":{}}` {
		t.Fatal(string(got), err)
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if _, err := Open(other, box); err == nil {
		t.Fatal("opened with wrong key")
	}
	bad := *box
	bad.Ciphertext = "A" + bad.Ciphertext[1:]
	if bad.Ciphertext != box.Ciphertext {
		if _, err := Open(priv, &bad); err == nil {
			t.Fatal("tampered ciphertext accepted")
		}
	}
	if _, err := Seal([]byte{1, 2, 3}, nil); err == nil {
		t.Fatal("bad public key accepted")
	}
}
