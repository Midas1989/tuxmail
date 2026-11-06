// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"bytes"
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/crypto/smime"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
)

// The "prefer" setting decides when both S/MIME and OpenPGP would act; the
// preferences reach the send at run time.
func TestPreferAndPrefs(t *testing.T) {
	jonas := testIdentity(t, "jonas@kanzlei.example")
	anna := testIdentity(t, "anna@example.net")
	keys := fakeSMIME{ids: map[string]*smime.Identity{"jonas@kanzlei.example": jonas},
		certs: map[string]*x509.Certificate{"anna@example.net": anna.Cert}}
	prefs := PGPPrefs{SignAlways: true}
	e := newEnv(t, func(c *Config) {
		c.SMIME = keys
		c.PGPPrefs = func() PGPPrefs { return prefs }
	}, nil, 0)
	ctx := context.Background()
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "jonas@kanzlei.example"}}, Now: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ring.ImportKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	draft := func() compose.Draft {
		return compose.Draft{To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Vertrag", TextBody: "Inhalt"}
	}

	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, PreferPGP: true, Draft: draft()})
	if err != nil {
		t.Fatal(err)
	}
	raw := e.smtp.messages()[0].data
	if res.SMIME || !res.Signed || !bytes.Contains(raw, []byte("application/pgp-signature")) || !bytes.Contains(raw, []byte("\r\nAutocrypt: ")) {
		t.Fatalf("prefer OpenPGP: %+v\n%s", res, raw)
	}

	// Prefer S/MIME: it wins as before.
	res, err = e.svc.Send(ctx, Request{IdentityID: e.ident.ID, Draft: draft()})
	if err != nil || !res.SMIME {
		t.Fatalf("prefer S/MIME: %+v %v", res, err)
	}

	// Autocrypt header switched off in the settings.
	prefs.NoAutocrypt, prefs.SignAlways = true, false
	if _, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, SMIME: SMIMEOptions{Sign: PGPOff, Encrypt: PGPOff}, Draft: draft()}); err != nil {
		t.Fatal(err)
	}
	ms := e.smtp.messages()
	if bytes.Contains(ms[len(ms)-1].data, []byte("Autocrypt:")) {
		t.Fatal("Autocrypt header despite the setting")
	}
}
