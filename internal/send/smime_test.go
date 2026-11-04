// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/smime"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
)

type fakeSMIME struct {
	ids   map[string]*smime.Identity
	certs map[string]*x509.Certificate
}

func (f fakeSMIME) SMIMEIdentity(_ context.Context, email string) (*smime.Identity, error) {
	return f.ids[strings.ToLower(email)], nil
}

func (f fakeSMIME) SMIMECertificates(_ context.Context, emails []string) (map[string]*x509.Certificate, error) {
	out := map[string]*x509.Certificate{}
	for _, e := range emails {
		if c := f.certs[strings.ToLower(e)]; c != nil {
			out[strings.ToLower(e)] = c
		}
	}
	return out, nil
}

func testIdentity(t *testing.T, email string) *smime.Identity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: email}, EmailAddresses: []string{email},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &smime.Identity{Cert: c, Key: key}
}

func TestSMIMESend(t *testing.T) {
	jonas := testIdentity(t, "jonas@kanzlei.example")
	anna := testIdentity(t, "anna@example.net")
	keys := fakeSMIME{ids: map[string]*smime.Identity{"jonas@kanzlei.example": jonas},
		certs: map[string]*x509.Certificate{"anna@example.net": anna.Cert}}
	e := newEnv(t, func(c *Config) { c.SMIME = keys }, nil, 0)
	ctx := context.Background()
	// Automatic: all recipients have certificates -> sign + encrypt.
	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Vertrag", TextBody: "Geheimer Inhalt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SMIME || !res.Encrypted || !res.Signed {
		t.Fatalf("result %+v", res)
	}
	raw := e.smtp.messages()[0].data
	if bytes.Contains(raw, []byte("Geheimer Inhalt")) || !bytes.Contains(raw, []byte("application/pkcs7-mime")) {
		t.Fatal("not encrypted")
	}
	r, err := smime.Read(raw, smime.ReadOptions{Identities: []*smime.Identity{anna}, Trusted: []*x509.Certificate{jonas.Cert}, From: "jonas@kanzlei.example"})
	if err != nil || !r.Decrypted || r.Signature != smime.SigTrusted || !bytes.Contains(r.Content, []byte("Geheimer Inhalt")) {
		t.Fatalf("read: %+v %v", r, err)
	}
	// Unknown recipient: auto falls back to plain; forced encryption fails.
	if res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "bob@example.org"}}, Subject: "Hallo", TextBody: "x"}}); err != nil || res.SMIME {
		t.Fatalf("auto plain: %+v %v", res, err)
	}
	if _, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, SMIME: SMIMEOptions{Encrypt: PGPOn},
		Draft: compose.Draft{To: []domain.Address{{Email: "bob@example.org"}}, Subject: "Hallo", TextBody: "x"}}); err == nil {
		t.Fatal("forced encryption without certificate must fail")
	}
	// Sign only.
	res, err = e.svc.Send(ctx, Request{IdentityID: e.ident.ID, SMIME: SMIMEOptions{Sign: PGPOn, Encrypt: PGPOff},
		Draft: compose.Draft{To: []domain.Address{{Email: "bob@example.org"}}, Subject: "Signiert", TextBody: "Klartext"}})
	if err != nil || !res.Signed || res.Encrypted {
		t.Fatalf("sign: %+v %v", res, err)
	}
	msgs := e.smtp.messages()
	raw = msgs[len(msgs)-1].data
	if r, _ := smime.Read(raw, smime.ReadOptions{Trusted: []*x509.Certificate{jonas.Cert}}); r.Signature != smime.SigTrusted {
		t.Fatalf("signature: %+v", r)
	}
}
