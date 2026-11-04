// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// AUDIT-C: imported RSA keys below 2048 bits must not be used for
// encryption (go-crypto v2 default MinRSABits = 2047, nil config).
func TestWeakRSAKeyNotUsedForEncryption(t *testing.T) {
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 1024, MinRSABits: 1024}
	e, err := openpgp.NewEntity("Weak", "", "weak@example.org", cfg)
	if err != nil {
		t.Fatal(err)
	}
	k := NewKeyFromEntity(e)
	if k.CanEncrypt(time.Now()) {
		t.Fatal("RSA-1024 key is accepted for encryption")
	}
}
