// SPDX-License-Identifier: AGPL-3.0-or-later

package smime

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

var now = time.Now().UTC()

type testCA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func newCA(t *testing.T) testCA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "TuxMail Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{c, key}
}

func (ca testCA) issue(t *testing.T, email string, serial int64) *Identity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: email}, EmailAddresses: []string{email},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &Identity{Cert: c, Key: key, Chain: []*x509.Certificate{ca.cert}}
}

const entity = "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nHallo Bob,\r\nanbei die Unterlagen.\r\n"

func header(body []byte) []byte {
	return append([]byte("From: anna@firma.de\r\nTo: bob@example.org\r\nSubject: Test\r\nMIME-Version: 1.0\r\n"), body...)
}

func TestRoundTrip(t *testing.T) {
	ca := newCA(t)
	anna := ca.issue(t, "anna@firma.de", 2)
	bob := ca.issue(t, "bob@example.org", 3)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)

	// PKCS#12 import.
	p12, err := ExportPKCS12(anna, "geheim")
	if err != nil {
		t.Fatal(err)
	}
	imp, err := ImportPKCS12(p12, "geheim")
	if err != nil || !imp.Cert.Equal(anna.Cert) || len(imp.Chain) != 1 {
		t.Fatalf("import: %v", err)
	}
	if _, err := ImportPKCS12(p12, "falsch"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if !CanEncryptTo(bob.Cert, now) || Emails(bob.Cert)[0] != "bob@example.org" {
		t.Fatal("cert helpers")
	}

	// Sign + verify.
	signed, err := Sign([]byte(entity), imp)
	if err != nil {
		t.Fatal(err)
	}
	raw := header(signed)
	if !IsSMIME(raw) {
		t.Fatal("IsSMIME")
	}
	res, err := Read(raw, ReadOptions{Roots: roots, From: "anna@firma.de", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Signed || res.Signature != SigTrusted || res.SignerEmail != "anna@firma.de" || len(res.Certificates) == 0 ||
		!bytes.Contains(res.Content, []byte("anbei die Unterlagen")) {
		t.Fatalf("verify: %+v", res)
	}
	// Unknown CA, wrong sender, tampered content.
	if res, _ := Read(raw, ReadOptions{Roots: x509.NewCertPool(), Now: now}); res.Signature != SigUntrusted {
		t.Fatalf("untrusted: %+v", res)
	}
	if res, _ := Read(raw, ReadOptions{Roots: x509.NewCertPool(), Trusted: []*x509.Certificate{anna.Cert}, Now: now}); res.Signature != SigTrusted {
		t.Fatalf("user-approved: %+v", res)
	}
	if res, _ := Read(raw, ReadOptions{Roots: roots, From: "mallory@evil.example", Now: now}); res.Signature != SigMismatch {
		t.Fatalf("mismatch: %+v", res)
	}
	tampered := bytes.Replace(raw, []byte("Unterlagen"), []byte("Unterlagex"), 1)
	if res, _ := Read(tampered, ReadOptions{Roots: roots, Now: now}); res.Signature != SigInvalid {
		t.Fatalf("tampered: %+v", res)
	}

	// Encrypt + decrypt; sign + encrypt.
	enc, err := SignAndEncrypt([]byte(entity), anna, []*x509.Certificate{bob.Cert, anna.Cert})
	if err != nil {
		t.Fatal(err)
	}
	raw = header(enc)
	res, err = Read(raw, ReadOptions{Identities: []*Identity{bob}, Roots: roots, From: "anna@firma.de", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted || !res.Decrypted || !res.Signed || res.Signature != SigTrusted || !bytes.Contains(res.Content, []byte("Hallo Bob")) {
		t.Fatalf("decrypt: %+v %q", res, res.Content)
	}
	other := ca.issue(t, "carl@example.org", 4)
	res, _ = Read(raw, ReadOptions{Identities: []*Identity{other}, Now: now})
	if !res.Encrypted || res.Decrypted || res.Error == "" {
		t.Fatalf("foreign key: %+v", res)
	}
	if _, err := Read([]byte("Content-Type: text/plain\r\n\r\nhi"), ReadOptions{}); err != ErrNotSMIME {
		t.Fatalf("plain: %v", err)
	}
}
