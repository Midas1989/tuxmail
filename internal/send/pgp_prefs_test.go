// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
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

// A scheduled send that cannot work is refused when it is planned.
func TestScheduleChecksKeys(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	_, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, SendAt: now.Add(time.Hour), PGP: PGPOptions{Encrypt: PGPOn},
		Draft: compose.Draft{To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Vertrag", TextBody: "Inhalt"}})
	if !errors.Is(err, ErrCannotEncrypt) {
		t.Fatalf("got %v", err)
	}
}

const encryptedOriginal = "From: anna@example.net\r\nSubject: s\r\nMessage-ID: <o@example.net>\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/encrypted; protocol=\"application/pgp-encrypted\"; boundary=b\r\n\r\n--b\r\nContent-Type: application/pgp-encrypted\r\n\r\nVersion: 1\r\n--b\r\n" +
	"Content-Type: application/octet-stream\r\n\r\nx\r\n--b--\r\n"

func replyEnv(t *testing.T, mod func(*Config)) (*env, *pgp.Key) {
	t.Helper()
	e := newEnv(t, mod, nil, 0)
	ctx := context.Background()
	own, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "jonas@kanzlei.example"}}, Now: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "anna@example.net"}}, Now: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []*pgp.Key{own, peer.Public()} {
		if _, err := e.ring.ImportKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	return e, peer
}

func replyDraft() compose.Draft {
	return compose.Draft{Kind: compose.KindReply, OriginalRaw: []byte(encryptedOriginal), To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Re: s", TextBody: "Zitat GEHEIM"}
}

// S/MIME signing switches OpenPGP off in deliver: the check has to see that.
func TestDecryptedInPlainFollowsDeliver(t *testing.T) {
	jonas := testIdentity(t, "jonas@kanzlei.example")
	e, _ := replyEnv(t, func(c *Config) {
		c.SMIME = fakeSMIME{ids: map[string]*smime.Identity{"jonas@kanzlei.example": jonas}, certs: map[string]*x509.Certificate{}}
	})
	_, err := e.svc.Send(context.Background(), Request{IdentityID: e.ident.ID, SMIME: SMIMEOptions{Sign: PGPOn}, Draft: replyDraft()})
	if !errors.Is(err, ErrBlockedByChecks) {
		t.Fatalf("got %v", err)
	}
	if n := len(e.smtp.messages()); n != 0 {
		t.Fatalf("%d mails left in plain text", n)
	}
	// OpenPGP alone encrypts, nothing stops.
	res, err := e.svc.Send(context.Background(), Request{IdentityID: e.ident.ID, SMIME: SMIMEOptions{Sign: PGPOff, Encrypt: PGPOff}, Draft: replyDraft()})
	if err != nil || !res.Encrypted || res.SMIME {
		t.Fatalf("encrypted reply: %+v %v", res, err)
	}
}

// The recipient's key is gone when a scheduled reply falls due.
func TestDecryptedInPlainAtDueTime(t *testing.T) {
	e, peer := replyEnv(t, nil)
	ctx := context.Background()
	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, SendAt: now.Add(time.Hour), Draft: replyDraft()})
	if err != nil || !res.Scheduled {
		t.Fatalf("schedule: %+v %v", res, err)
	}
	op, err := e.st.GetOp(ctx, res.OpID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ring.Delete(ctx, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.HandleOp(ctx, op.Kind, op.Payload); !errors.Is(err, ErrBlockedByChecks) {
		t.Fatalf("got %v", err)
	}
	if n := len(e.smtp.messages()); n != 0 {
		t.Fatalf("%d mails left in plain text", n)
	}
}
