// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/store"
)

func testCert(t *testing.T, email string, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: email}, EmailAddresses: []string{email},
		NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Audit NC-01: a certificate that was merely collected from incoming mail
// never out-ranks one the user imported, even with a later expiry date.
func TestSMIMEEncryptionPrefersImportedOverCollected(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &App{st: st, opts: Options{Now: time.Now}}
	const addr = "alice@bank.example"

	imported := testCert(t, addr, time.Now().Add(365*24*time.Hour))
	attacker := testCert(t, addr, time.Now().Add(10*365*24*time.Hour))
	ic := storeCert(imported)
	ic.Source, ic.Trusted = "imported", true
	if _, err := st.PutSMIMECert(ctx, ic); err != nil {
		t.Fatal(err)
	}
	ac := storeCert(attacker)
	ac.Source = "collected"
	if _, err := st.PutSMIMECert(ctx, ac); err != nil {
		t.Fatal(err)
	}
	got, err := smimeKeys{a: a}.SMIMECertificates(ctx, []string{addr})
	if err != nil {
		t.Fatal(err)
	}
	if c := got[addr]; c == nil || !c.Equal(imported) {
		t.Fatalf("encryption certificate = %v, want the imported one", c)
	}

	// Without an imported certificate a collected one is still usable.
	other := testCert(t, "carol@example.org", time.Now().Add(365*24*time.Hour))
	oc := storeCert(other)
	oc.Source = "collected"
	if _, err := st.PutSMIMECert(ctx, oc); err != nil {
		t.Fatal(err)
	}
	got, _ = smimeKeys{a: a}.SMIMECertificates(ctx, []string{"carol@example.org"})
	if c := got["carol@example.org"]; c == nil || !c.Equal(other) {
		t.Fatalf("collected certificate not used when it is the only one: %v", c)
	}
}

// Audit ND-01: in server mode the browser used for PDF export is chosen by
// the administrator, never by a per-user setting.
func TestChromiumPathSettingIgnoredInServerMode(t *testing.T) {
	prog := filepath.Join(t.TempDir(), "my-browser")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var s ComfortSettings
	s.PDF.ChromiumPath = prog

	server := &App{opts: Options{Mode: ModeServer}}
	if got, _ := server.chromium(s); got == prog {
		t.Fatal("server mode executes the program named in the user's settings")
	}
	desktop := &App{opts: Options{Mode: ModeDesktop}}
	if got, err := desktop.chromium(s); err != nil || got != prog {
		t.Fatalf("desktop mode must honour the setting: %q, %v", got, err)
	}
}

// Audit NB-04: an invitation whose sender is unknown is always flagged.
func TestOrganizerMismatchUnknownSender(t *testing.T) {
	org := &ical.Attendee{Email: "boss@example.org"}
	for _, m := range []string{"REQUEST", "CANCEL", "REPLY", "COUNTER"} {
		if !organizerMismatch(m, "", org, nil) {
			t.Errorf("%s without a sender address is not flagged", m)
		}
	}
	if organizerMismatch("REQUEST", "boss@example.org", org, nil) {
		t.Error("matching sender flagged")
	}
}
