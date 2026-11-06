// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/devserver"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

const (
	pgpMarker     = "KLARTEXTMARKER7f3a im Inhalt"
	pgpMarkerWord = "KLARTEXTMARKER7f3a"
	pgpPassphrase = "PASSPHRASEMARKER-7f3a-lang"
	pgpSubject    = "OpenPGP Rundlauf"
)

// pgpLive is an App with a real secret vault and a log file on disk, against
// the in-memory dev server, so that the byte search below sees what a real
// installation leaves behind.
type pgpLive struct {
	dir string
	srv *devserver.Server
	st  *store.Store
	a   *app.App
	log *os.File
}

func (l *pgpLive) open(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(l.dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.New(filepath.Join(l.dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(l.dir, "secrets.vault")
	var vault *secrets.Vault
	if _, err := os.Stat(vaultPath); err != nil {
		vault, err = secrets.CreateVault(vaultPath, []byte("master-passwort-test"), secrets.DefaultKDFParams)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if vault, err = secrets.OpenVault(vaultPath); err != nil {
			t.Fatal(err)
		}
		if err := vault.Unlock([]byte("master-passwort-test")); err != nil {
			t.Fatal(err)
		}
	}
	l.log, err = os.OpenFile(filepath.Join(l.dir, "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: l.dir, Mode: app.ModeServer, Secrets: vault,
		Logger: slog.New(slog.NewTextHandler(l.log, &slog.HandlerOptions{Level: slog.LevelDebug})), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	l.a, l.st = a, st
}

func (l *pgpLive) close() {
	if l.a != nil {
		l.a.Close()
		l.st.Close()
		l.log.Close()
		l.a = nil
	}
}

func newPGPLive(t *testing.T) *pgpLive {
	t.Helper()
	srv, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if err := srv.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}
	l := &pgpLive{dir: t.TempDir(), srv: srv}
	l.open(t)
	t.Cleanup(l.close)
	server := func(addr string) *app.ServerConfig {
		h, p, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(p)
		return &app.ServerConfig{Host: h, Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: devserver.AnnaAddr}
	}
	if _, err := l.a.CreateAccount(context.Background(), app.NewAccountInput{Kind: "imap", Email: devserver.AnnaAddr, Name: "Anna Berger",
		Password: devserver.DefaultPassword, Incoming: server(srv.IMAPAddr()), Outgoing: server(srv.SMTPAddr())}); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *pgpLive) wait(t *testing.T, subject string) app.MessageSummary {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		page, err := l.a.ListMessages(context.Background(), app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if m.Subject == subject {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %q not synced", subject)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func generate(t *testing.T, email string, passphrase string) *pgp.Key {
	t.Helper()
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: "Test", Email: email}}, Passphrase: []byte(passphrase), Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func armoredBase64(t *testing.T, k *pgp.Key, secret bool) string {
	t.Helper()
	var s string
	var err error
	if secret {
		s, err = k.ArmoredSecret()
	} else {
		s, err = k.ArmoredPublic()
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// Acceptance: create and import keys, a mail to oneself encrypted and signed
// over real SMTP/IMAP, readable after a restart, nothing readable on disk.
func TestPGPRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := newPGPLive(t)
	l.wait(t, "Mittagessen morgen?")

	own, err := l.a.GeneratePGPKey(ctx, app.PGPGenerateInput{Name: "Anna Berger", Email: devserver.AnnaAddr})
	if err != nil {
		t.Fatal(err)
	}
	if !own.Own || !own.CanSign || !own.CanEncrypt || own.Trust != "verified" || !strings.Contains(own.Algorithm, "25519") && own.Algorithm == "" {
		t.Fatalf("own key: %+v", own)
	}

	// Import of a locked private key: wrong and missing passphrase, then right.
	second := generate(t, "anna-zweit@example.org", pgpPassphrase)
	secondSecret, err := second.SecretBytes()
	if err != nil {
		t.Fatal(err)
	}
	in := app.PGPImportInput{Data: armoredBase64(t, second, true)}
	var appErr *app.Error
	in.Passphrase = "falsch"
	if _, err := l.a.ImportPGPKeys(ctx, in); !errors.As(err, &appErr) || appErr.Code != "pgp_wrong_passphrase" {
		t.Fatalf("wrong passphrase: %v", err)
	}
	in.Passphrase = pgpPassphrase
	imported, err := l.a.ImportPGPKeys(ctx, in)
	if err != nil || len(imported) != 1 || !imported[0].Own || !imported[0].Protected || imported[0].Locked {
		t.Fatalf("import: %+v %v", imported, err)
	}

	// The private key must be exportable, always protected.
	if _, err := l.a.ExportPGPSecretKey(ctx, own.ID, app.PGPExportInput{Passphrase: "kurz"}); !errors.As(err, &appErr) || appErr.Code != "pgp_passphrase_short" {
		t.Fatalf("short export passphrase: %v", err)
	}
	exportPass := "EXPORTMARKER-7f3a-lang"
	armored, err := l.a.ExportPGPSecretKey(ctx, own.ID, app.PGPExportInput{Passphrase: exportPass})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := pgp.ReadKey([]byte(armored))
	if err != nil || !exp.IsSecret() || !exp.IsLocked() {
		t.Fatalf("export is not locked: %v", err)
	}
	if _, err := exp.Unlock([]byte(exportPass)); err != nil {
		t.Fatal(err)
	}
	ownSecret, err := exp.Unlock([]byte(exportPass))
	if err != nil {
		t.Fatal(err)
	}
	ownSecretBytes, err := ownSecret.SecretBytes()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := l.a.ExportPGPPublicKey(ctx, own.ID)
	if err != nil || !strings.Contains(pub, "PUBLIC KEY") || strings.Contains(pub, "PRIVATE") {
		t.Fatalf("public export: %v", err)
	}

	st, err := l.a.PGPStatusFor(ctx, devserver.AnnaAddr, []string{devserver.AnnaAddr})
	if err != nil || !st.CanSign || !st.CanEncrypt {
		t.Fatalf("status: %+v %v", st, err)
	}

	// Recipient without a key: refused, nothing leaves in plain text.
	ids := must[[]app.Identity](t)(l.a.ListIdentities(ctx))
	before := len(l.srv.Deliveries())
	_, err = l.a.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: ids[0].ID, To: []app.Address{{Email: devserver.BenAddr}},
		Subject: "Darf nicht raus", Text: pgpMarker, Crypto: &app.OutgoingCrypto{Method: "pgp", Sign: true, Encrypt: true}})
	if !errors.As(err, &appErr) || appErr.Code != "pgp_missing_key" || !strings.Contains(appErr.Message, devserver.BenAddr) {
		t.Fatalf("missing key: %v", err)
	}
	if n := len(l.srv.Deliveries()); n != before {
		t.Fatalf("%d mails left in plain text", n-before)
	}

	// Encrypted and signed to oneself.
	res, err := l.a.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: ids[0].ID, To: []app.Address{{Email: devserver.AnnaAddr}},
		Subject: pgpSubject, Text: pgpMarker, Crypto: &app.OutgoingCrypto{Method: "pgp", Sign: true, Encrypt: true}})
	if err != nil || res.Blocked {
		t.Fatalf("send: %+v %v", res, err)
	}
	m := l.wait(t, pgpSubject)
	check := func() {
		t.Helper()
		d, err := l.a.GetMessage(ctx, m.ID)
		if err != nil || !strings.Contains(d.Text, pgpMarkerWord) {
			t.Fatalf("not readable: %q %v", d.Text, err)
		}
		ins, err := l.a.MessageInsights(ctx, m.ID)
		if err != nil || ins.PGP == nil || !ins.PGP.Encrypted || !ins.PGP.Decrypted || !ins.PGP.Signed || ins.PGP.Signature != "valid" || ins.PGP.SignerTrust != "verified" {
			t.Fatalf("status: %+v %v", ins.PGP, err)
		}
	}
	check()

	// The message on the wire and in the blob store is ciphertext with an
	// Autocrypt header.
	raw, err := l.a.MessageSource(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, pgpMarkerWord) || !strings.Contains(raw, "multipart/encrypted") || !strings.Contains(raw, "Autocrypt:") {
		t.Fatalf("raw message:\n%s", raw)
	}

	// Restart: everything is read back from disk.
	l.close()
	l.open(t)
	check()
	keys, err := l.a.ListPGPKeys(ctx)
	if err != nil || len(keys) != 2 || !keys[0].Own || !keys[1].Own {
		t.Fatalf("keys after restart: %+v %v", keys, err)
	}
	// Deleting the key takes reading with it, also from the render caches.
	second2, err := l.a.GeneratePGPKey(ctx, app.PGPGenerateInput{Email: "dritter@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.a.DeletePGPKey(ctx, second2.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.a.DeletePGPKey(ctx, own.ID); err != nil {
		t.Fatal(err)
	}
	if d, err := l.a.GetMessage(ctx, m.ID); err != nil || strings.Contains(d.Text, pgpMarkerWord) {
		t.Fatalf("still readable without the key: %q %v", d.Text, err)
	}
	if ins, err := l.a.MessageInsights(ctx, m.ID); err != nil || ins.PGP == nil || ins.PGP.Decrypted {
		t.Fatalf("status without the key: %+v %v", ins.PGP, err)
	}
	var indexed int
	if err := l.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM fts_docs WHERE body LIKE ?`, "%"+pgpMarkerWord+"%").Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("plain text in the search index: %d %v", indexed, err)
	}
	l.close()

	// The form the private key is really stored in, read back from the vault.
	vault, err := secrets.OpenVault(filepath.Join(l.dir, "secrets.vault"))
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock([]byte("master-passwort-test")); err != nil {
		t.Fatal(err)
	}
	enc, err := vault.Get("pgp:" + imported[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	storedSecret, err := base64.StdEncoding.DecodeString(string(enc))
	if err != nil || len(storedSecret) == 0 {
		t.Fatalf("stored private key: %v", err)
	}
	if k, err := pgp.ReadKey(storedSecret); err != nil || !k.IsLocked() {
		t.Fatalf("the passphrase protection did not survive the import: %v", err)
	}
	vault.Lock()
	if _, err := vault.Get("pgp:" + own.Fingerprint); err == nil {
		t.Fatal("locked vault hands out keys")
	}

	// Byte search through everything on disk: database, WAL, blobs, vault, log.
	needles := map[string][]byte{
		"stored private key":     storedSecret,
		"stored private key b64": enc,
		"passphrase":             []byte(pgpPassphrase),
		"export passphrase":      []byte(exportPass),
		"plain text":             []byte(pgpMarkerWord),
		"private key":            secondSecret,
		"own private key":        ownSecretBytes,
		"private key b64":        []byte(base64.StdEncoding.EncodeToString(ownSecretBytes)),
		"armored private":        []byte("PGP PRIVATE KEY"),
	}
	files := 0
	err = filepath.WalkDir(l.dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for what, needle := range needles {
			if bytes.Contains(data, needle) {
				t.Errorf("%s found in %s", what, p)
			}
		}
		return nil
	})
	if err != nil || files < 3 {
		t.Fatalf("byte search: %d files, %v", files, err)
	}
}

func signedMail(t *testing.T, from string, signer *pgp.Key, body string) string {
	t.Helper()
	ent, err := pgp.SignMIME([]byte("Content-Type: text/plain; charset=utf-8\r\n\r\n"+body+"\r\n"), signer, pgp.SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return "From: " + from + "\r\nTo: alex@berger-kanzlei.example\r\nSubject: signiert\r\nDate: Tue, 03 Nov 2026 10:00:00 +0000\r\nMessage-ID: <" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@test.example>\r\nMIME-Version: 1.0\r\n" + string(ent.Bytes())
}

// Signature states: unknown key, valid, manipulated, wrong sender; and a mail
// for another key stays closed. No key is fetched for any of these.
func TestPGPReadingStates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const sender = "bob@example.org"
	signer := generate(t, sender, "")

	status := func(name, raw string) *app.PGPMessageStatus {
		t.Helper()
		id := addRaw(t, f, name, raw)
		ins, err := f.a.MessageInsights(ctx, id)
		if err != nil || ins.PGP == nil {
			t.Fatalf("%s: %+v %v", name, ins.PGP, err)
		}
		return ins.PGP
	}

	mail := signedMail(t, sender, signer, "Vertrag anbei")
	if s := status("unknown", mail); !s.Signed || s.Signature != "unknown_key" {
		t.Fatalf("unknown key: %+v", s)
	}
	imported, err := f.a.ImportPGPKeys(ctx, app.PGPImportInput{Data: armoredBase64(t, signer, false)})
	if err != nil || imported[0].Own || imported[0].Trust != "unknown" {
		t.Fatalf("import: %+v %v", imported, err)
	}
	if s := status("valid", signedMail(t, sender, signer, "Vertrag anbei")); s.Signature != "valid" || s.SignerTrust != "unknown" {
		t.Fatalf("valid: %+v", s)
	}
	// The user compares the fingerprint.
	if err := f.a.SetPGPKeyVerified(ctx, imported[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if s := status("verified", signedMail(t, sender, signer, "Vertrag anbei")); s.Signature != "valid" || s.SignerTrust != "verified" {
		t.Fatalf("verified: %+v", s)
	}
	if err := f.a.SetPGPKeyVerified(ctx, imported[0].ID, false); err != nil {
		t.Fatal(err)
	}
	// Manipulated after signing.
	tampered := strings.Replace(signedMail(t, sender, signer, "Betrag 100 Euro"), "100 Euro", "900 Euro", 1)
	if s := status("tampered", tampered); s.Signature != "invalid" {
		t.Fatalf("tampered: %+v", s)
	}
	// A real signature, but by a key that does not belong to the sender.
	if s := status("mismatch", signedMail(t, "chef@example.org", signer, "Bitte zahlen")); s.Signature != "mismatch" {
		t.Fatalf("mismatch: %+v", s)
	}

	// Wrong key: the mail stays closed, with a clear state and no text.
	other := generate(t, "fremd@example.org", "")
	if _, err := f.a.GeneratePGPKey(ctx, app.PGPGenerateInput{Email: "alex@berger-kanzlei.example"}); err != nil {
		t.Fatal(err)
	}
	ent, err := pgp.EncryptMIME([]byte("Content-Type: text/plain\r\n\r\n"+pgpMarker+"\r\n"), []*pgp.Key{other.Public()}, pgp.EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closed := "From: " + sender + "\r\nSubject: zu\r\nMessage-ID: <zu@test.example>\r\nMIME-Version: 1.0\r\n" + string(ent.Bytes())
	id := addRaw(t, f, "closed", closed)
	ins, err := f.a.MessageInsights(ctx, id)
	if err != nil || ins.PGP == nil || !ins.PGP.Encrypted || ins.PGP.Decrypted {
		t.Fatalf("wrong key: %+v %v", ins.PGP, err)
	}
	if d, err := f.a.GetMessage(ctx, id); err != nil || strings.Contains(d.Text, pgpMarkerWord) {
		t.Fatalf("wrong key: %q %v", d.Text, err)
	}
}

// Incoming Autocrypt headers are collected per specification, count as "seen
// in mail" and make a recipient reachable; only comparing makes them verified.
func TestPGPAutocryptCollect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const peer = "dora@example.org"
	k := generate(t, peer, "")
	mail := func(date string, key *pgp.Key, addr string) string {
		h, err := pgp.NewAutocryptHeader(addr, key, pgp.PreferMutual, now)
		if err != nil {
			t.Fatal(err)
		}
		return "From: " + peer + "\r\nSubject: hallo\r\nDate: " + date + "\r\nMessage-ID: <" + date[:2] + addr + ">\r\nAutocrypt: " + h.Value() + "\r\nContent-Type: text/plain\r\n\r\nHallo\r\n"
	}
	id := addRaw(t, f, "ac1", mail("Mon, 02 Nov 2026 10:00:00 +0000", k, peer))
	if _, err := f.a.MessageInsights(ctx, id); err != nil {
		t.Fatal(err)
	}
	peers, err := f.a.ListPGPPeers(ctx)
	if err != nil || len(peers) != 1 || peers[0].Address != peer || peers[0].Trust != "tofu" || peers[0].Fingerprint != k.Fingerprint() || !peers[0].PreferEncrypt {
		t.Fatalf("peers: %+v %v", peers, err)
	}
	st, err := f.a.PGPStatusFor(ctx, "alex@berger-kanzlei.example", []string{peer})
	if err != nil || len(st.Missing) != 0 || st.Recipients[0].Source != "autocrypt" || st.Recipients[0].Trust != "tofu" {
		t.Fatalf("status: %+v %v", st, err)
	}

	// A header for another address than From is ignored.
	addRawInsights := func(name, raw string) {
		t.Helper()
		if _, err := f.a.MessageInsights(ctx, addRaw(t, f, name, raw)); err != nil {
			t.Fatal(err)
		}
	}
	addRawInsights("ac2", mail("Tue, 03 Nov 2026 09:00:00 +0000", generate(t, "x@example.org", ""), "x@example.org"))
	peers, _ = f.a.ListPGPPeers(ctx)
	if peers[0].Fingerprint != k.Fingerprint() {
		t.Fatalf("foreign address accepted: %+v", peers)
	}
	// A new key for the address replaces the old one and is flagged.
	newer := generate(t, peer, "")
	addRawInsights("ac3", mail("Tue, 03 Nov 2026 10:00:00 +0000", newer, peer))
	peers, _ = f.a.ListPGPPeers(ctx)
	if peers[0].Fingerprint != newer.Fingerprint() || !peers[0].KeyChanged {
		t.Fatalf("key change: %+v", peers)
	}
	// Verification is bound to the fingerprint the user saw.
	var appErr *app.Error
	if _, err := f.a.VerifyPGPPeer(ctx, app.PGPPeerInput{Address: peer, Fingerprint: k.Fingerprint()}); !errors.As(err, &appErr) || appErr.Code != "pgp_key_changed" {
		t.Fatalf("stale fingerprint: %v", err)
	}
	info, err := f.a.VerifyPGPPeer(ctx, app.PGPPeerInput{Address: peer, Fingerprint: newer.Fingerprint()})
	if err != nil || info.Trust != "verified" || info.Own {
		t.Fatalf("verify: %+v %v", info, err)
	}
	if err := f.a.DeletePGPPeer(ctx, peer); err != nil {
		t.Fatal(err)
	}
	if peers, _ := f.a.ListPGPPeers(ctx); len(peers) != 0 {
		t.Fatalf("after delete: %+v", peers)
	}

	// Mail from a sender without a header leaves no trace.
	addRawInsights("ac4", "From: fremd@example.org\r\nSubject: x\r\nMessage-ID: <x@y>\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	if _, err := f.st.GetPGPPeer(ctx, "fremd@example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sender without header stored: %v", err)
	}
}

// A locked key: chosen signing or encryption does not leave without it, plain
// mail still does (Autocrypt needs no passphrase), reading works after
// unlocking and again after a restart, the passphrase never reaches the disk.
func TestPGPLockedKeySendAndRestart(t *testing.T) {
	const pass = "PASSPHRASEMARKER2-7f3a-lang"
	ctx := context.Background()
	l := newPGPLive(t)
	l.wait(t, "Mittagessen morgen?")
	own, err := l.a.GeneratePGPKey(ctx, app.PGPGenerateInput{Name: "Anna Berger", Email: devserver.AnnaAddr, Passphrase: pass})
	if err != nil || !own.Locked {
		t.Fatalf("generate: %+v %v", own, err)
	}
	st, err := l.a.PGPStatusFor(ctx, devserver.AnnaAddr, []string{devserver.AnnaAddr})
	if err != nil || !st.OwnLocked || st.OwnKeyID != own.ID {
		t.Fatalf("status: %+v %v", st, err)
	}
	ids := must[[]app.Identity](t)(l.a.ListIdentities(ctx))
	mail := func(subject string, c *app.OutgoingCrypto) app.OutgoingMessage {
		return app.OutgoingMessage{Channel: "email", IdentityID: ids[0].ID, To: []app.Address{{Email: devserver.AnnaAddr}}, Subject: subject, Text: pgpMarker, Crypto: c}
	}
	var appErr *app.Error
	before := len(l.srv.Deliveries())
	for name, c := range map[string]*app.OutgoingCrypto{
		"sign":    {Method: "pgp", Sign: true},
		"encrypt": {Method: "pgp", Encrypt: true},
		"both":    {Method: "pgp", Sign: true, Encrypt: true},
	} {
		if _, err := l.a.Send(ctx, mail("Darf nicht raus", c)); !errors.As(err, &appErr) || appErr.Code != "pgp_key_locked" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// "Sign every mail" in the settings counts as chosen.
	s, err := l.a.ComfortSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.PGP.SignByDefault = true
	if _, err := l.a.SaveComfortSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := l.a.Send(ctx, mail("Darf nicht raus", nil)); !errors.As(err, &appErr) || appErr.Code != "pgp_key_locked" {
		t.Fatalf("sign by default: %v", err)
	}
	if n := len(l.srv.Deliveries()); n != before {
		t.Fatalf("%d mails left without the key", n-before)
	}
	s.PGP.SignByDefault = false
	if _, err := l.a.SaveComfortSettings(ctx, s); err != nil {
		t.Fatal(err)
	}

	// Plain mail: no signature was asked for, the header needs no passphrase.
	unprotected := mail("Ohne Schutz", &app.OutgoingCrypto{Method: "none"})
	unprotected.Text = "Unkritischer Text"
	if _, err := l.a.Send(ctx, unprotected); err != nil {
		t.Fatal(err)
	}
	plain := must[string](t)(l.a.MessageSource(ctx, l.wait(t, "Ohne Schutz").ID))
	if !strings.Contains(plain, "Autocrypt:") || strings.Contains(plain, "pgp-signature") {
		t.Fatalf("plain mail:\n%s", plain)
	}

	if _, err := l.a.UnlockPGPKey(ctx, own.ID, app.PGPUnlockInput{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	if res, err := l.a.Send(ctx, mail(pgpSubject, &app.OutgoingCrypto{Method: "pgp", Sign: true, Encrypt: true})); err != nil || res.Blocked {
		t.Fatalf("send: %+v %v", res, err)
	}
	m := l.wait(t, pgpSubject)
	read := func() {
		t.Helper()
		d, err := l.a.GetMessage(ctx, m.ID)
		if err != nil || !strings.Contains(d.Text, pgpMarkerWord) {
			t.Fatalf("not readable: %q %v", d.Text, err)
		}
		ins, err := l.a.MessageInsights(ctx, m.ID)
		if err != nil || ins.PGP == nil || !ins.PGP.Decrypted || ins.PGP.Signature != "valid" || len(ins.PGP.LockedKeys) != 0 {
			t.Fatalf("status: %+v %v", ins.PGP, err)
		}
	}
	closed := func() {
		t.Helper()
		d, err := l.a.GetMessage(ctx, m.ID)
		if err != nil || strings.Contains(d.Text, pgpMarkerWord) {
			t.Fatalf("readable while locked: %q %v", d.Text, err)
		}
		ins, err := l.a.MessageInsights(ctx, m.ID)
		if err != nil || ins.PGP == nil || ins.PGP.Decrypted || len(ins.PGP.LockedKeys) != 1 || ins.PGP.LockedKeys[0].ID != own.ID {
			t.Fatalf("status while locked: %+v %v", ins.PGP, err)
		}
	}
	read()
	l.a.LockPGPKeys()
	closed()

	// Restart: nothing was remembered.
	l.close()
	l.open(t)
	closed()
	if _, err := l.a.UnlockPGPKey(ctx, own.ID, app.PGPUnlockInput{Passphrase: "falsch"}); !errors.As(err, &appErr) || appErr.Code != "pgp_wrong_passphrase" {
		t.Fatalf("wrong: %v", err)
	}
	if _, err := l.a.UnlockPGPKey(ctx, own.ID, app.PGPUnlockInput{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	read()
	l.close()

	files := 0
	if err := filepath.WalkDir(l.dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for _, needle := range []string{pass, pgpMarkerWord, "PGP PRIVATE KEY"} {
			if bytes.Contains(data, []byte(needle)) {
				t.Errorf("%q found in %s", needle, p)
			}
		}
		return nil
	}); err != nil || files < 3 {
		t.Fatalf("byte search: %d files, %v", files, err)
	}
}
