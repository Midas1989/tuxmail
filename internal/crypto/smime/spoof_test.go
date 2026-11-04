// SPDX-License-Identifier: AGPL-3.0-or-later

package smime

import (
	"crypto/x509"
	"testing"

	"github.com/smallstep/pkcs7"
)

// AUDIT-C (High): with more than one SignerInfo the first certificate of the
// bag was taken as "the signer". An attacker could sign twice with their
// own key, put the victim's (public, CA-issued) certificate first and get a
// "trusted signature by victim".
func TestMultipleSignersCannotSpoofSigner(t *testing.T) {
	ca := newCA(t)
	victim := ca.issue(t, "chef@firma.de", 2)
	attacker := newCA(t).issue(t, "chef@firma.de", 3) // own CA, untrusted
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)

	sd, err := pkcs7.NewSignedData([]byte(entity))
	if err != nil {
		t.Fatal(err)
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	sd.AddCertificate(victim.Cert)
	for range 2 {
		if err := sd.AddSigner(attacker.Cert, attacker.Key, pkcs7.SignerInfoConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	der, err := sd.Finish()
	if err != nil {
		t.Fatal(err)
	}
	raw := header(append([]byte("Content-Type: application/pkcs7-mime; smime-type=signed-data; name=smime.p7m\r\nContent-Transfer-Encoding: base64\r\n\r\n"), base64Lines(der)...))
	res, err := Read(raw, ReadOptions{Roots: roots, From: "chef@firma.de", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature == SigTrusted || (res.Signer != nil && res.Signer.Equal(victim.Cert)) {
		t.Fatalf("signature spoofed: %s signer %v", res.Signature, res.SignerFingerprint)
	}
}
