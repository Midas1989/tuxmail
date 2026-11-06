// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

func pgpTestApp(t *testing.T) *App {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, Options{Store: st, Blobs: blobs, DataDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

type countingLookup struct {
	calls int
	keys  []*pgp.Key
	err   error
}

func (c *countingLookup) LookupByEmail(context.Context, string) ([]*pgp.Key, error) {
	c.calls++
	return c.keys, c.err
}
func (c *countingLookup) Source() pgp.KeySource { return pgp.SourceWKD }

// Key directories are only asked on an explicit request: reading a signed mail
// from an unknown sender and planning a send stay offline.
func TestPGPLookupOnlyOnRequest(t *testing.T) {
	a := pgpTestApp(t)
	ctx := context.Background()
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "bob@example.org"}}})
	if err != nil {
		t.Fatal(err)
	}
	l := &countingLookup{keys: []*pgp.Key{k}}
	a.comfort.pgp.lookup = func(string) pgp.KeyLookup { return l }

	ent, err := pgp.SignMIME([]byte("Content-Type: text/plain\r\n\r\nx\r\n"), k, pgp.SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("From: bob@example.org\r\nSubject: s\r\nMIME-Version: 1.0\r\n"), ent.Bytes()...)
	if _, err := a.render.Render(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PGPStatusFor(ctx, "me@example.org", []string{"bob@example.org"}); err != nil {
		t.Fatal(err)
	}
	if l.calls != 0 {
		t.Fatalf("%d lookups without a click", l.calls)
	}

	info, err := a.LookupPGPKey(ctx, PGPLookupInput{Email: "bob@example.org", Source: "wkd"})
	if err != nil || l.calls != 1 || info.Own || info.Source != "wkd" || info.Trust != "tofu" {
		t.Fatalf("lookup: %+v calls=%d %v", info, l.calls, err)
	}
	stored, err := a.st.GetPGPKeyByFingerprint(ctx, k.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.PublicKey), "PRIVATE") || stored.SecretRef != "" {
		t.Fatalf("a looked up key must stay public: %+v", stored.SecretRef)
	}

	var appErr *Error
	l.err = pgp.ErrKeyNotFound
	if _, err := a.LookupPGPKey(ctx, PGPLookupInput{Email: "nobody@example.org", Source: "wkd"}); !errors.As(err, &appErr) || appErr.Code != "pgp_key_not_found" {
		t.Fatalf("not found: %v", err)
	}
	a.comfort.pgp.lookup = pgpDirectory
	if _, err := a.LookupPGPKey(ctx, PGPLookupInput{Email: "x@example.org", Source: "evil"}); !errors.As(err, &appErr) || appErr.Code != CodeInvalidInput {
		t.Fatalf("unknown source: %v", err)
	}
}

// WKD talks to the host in the address: internal targets are refused.
func TestPGPDirectoryRefusesInternalHosts(t *testing.T) {
	_, err := pgpDirectory("wkd").LookupByEmail(context.Background(), "x@127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("expected a refused connection, got %v", err)
	}
}

// Private keys live in the secret store, are part of a backup with credentials,
// and are gone with the key. The database only has public material.
func TestPGPSecretRefsAndStorage(t *testing.T) {
	a := pgpTestApp(t)
	ctx := context.Background()
	info, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Name: "Me", Email: "me@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	ref := "pgp:" + info.Fingerprint
	if !slices.Contains(a.secretRefs(), ref) {
		t.Fatalf("backup does not carry the private key: %v", a.secretRefs())
	}
	if _, err := a.opts.Secrets.Get(ref); err != nil {
		t.Fatal(err)
	}
	row, err := a.st.GetPGPKey(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	k, err := pgp.ReadKey(row.PublicKey)
	if err != nil || k.IsSecret() {
		t.Fatalf("database holds private material: %v", err)
	}
	// A fresh ring on the same stores still has the private part.
	if k, err := pgp.NewKeyRing(a.comfort.pgp.keys, nil).Get(ctx, info.Fingerprint); err != nil || !k.IsSecret() {
		t.Fatalf("reload: %v", err)
	}
	if err := a.DeletePGPKey(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.opts.Secrets.Get(ref); err == nil {
		t.Fatal("private key survives deleting the key")
	}
}

// The search index and the insights get nothing from an encrypted mail. S/MIME
// is the template for the index; analysis is stricter here (see comfort_pgp.go).
func TestPGPNoPlaintextInIndexOrInsights(t *testing.T) {
	a := pgpTestApp(t)
	ctx := context.Background()
	info, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "me@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := a.pgpRing().Get(ctx, info.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	ent, err := pgp.EncryptMIME([]byte("Content-Type: text/plain\r\n\r\nGEHEIMWORT Rechnung\r\n"), []*pgp.Key{k.Public()}, pgp.EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("From: bob@example.org\r\nSubject: s\r\nMessage-ID: <a@b>\r\nMIME-Version: 1.0\r\n"), ent.Bytes()...)

	c, err := ParseExtractor{}.Extract(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Text+c.Snippet, "GEHEIMWORT") {
		t.Fatalf("index input has plain text: %+v", c)
	}
	an, err := a.analyze(ctx, store.Message{ID: 1}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(an.msg.Text, "GEHEIMWORT") {
		t.Fatal("the analysis decrypted the mail")
	}
	b, _ := json.Marshal(an.insight)
	if strings.Contains(string(b), "GEHEIMWORT") {
		t.Fatal("plain text in the insight")
	}
	// The renderer of the reading pane does decrypt, and flags the mail for
	// the features that must not see it (AI, rules).
	r, err := a.render.Render(ctx, raw)
	if err != nil || !strings.Contains(r.Text, "GEHEIMWORT") || !r.Encrypted {
		t.Fatalf("render: %+v %v", r, err)
	}
	if pm, err := parse.Parse(raw, parse.Options{}); err != nil || !pm.PGP.Encrypted || pm.PGP.Decrypted {
		t.Fatalf("detection: %+v %v", pm, err)
	}
}

// A user backup with credentials brings the private key back, encrypted in the
// archive; one without them does not contain it and says so through Own=false.
func TestPGPUserBackupRoundTrip(t *testing.T) {
	a := pgpTestApp(t)
	ctx := context.Background()
	info, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Name: "Me", Email: "me@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := a.opts.Secrets.Get("pgp:" + info.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetPGPKeyVerified(ctx, info.ID, true); err != nil {
		t.Fatal(err)
	}

	create := func(sc backup.Scope, pass string) []byte {
		t.Helper()
		var buf bytes.Buffer
		if _, err := backup.Create(ctx, &buf, backup.Options{Store: a.st, Blobs: a.blobs, Secrets: a.opts.Secrets, SecretRefs: a.secretRefs(),
			Scope: sc, Passphrase: pass, TempDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	src := func(b []byte) backup.Source {
		return backup.Source{Name: "a", Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
	}

	arc := create(backup.Scope{Mode: backup.ModeFull, Secrets: true}, "correct horse")
	if bytes.Contains(arc, stored) {
		t.Fatal("private key readable in the archive")
	}
	ex, err := backup.Extract(ctx, src(arc), "correct horse", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()

	// Replace: a new data directory.
	dir := t.TempDir()
	sec := newMemorySecrets()
	if _, err := backup.Restore(ctx, ex, backup.RestoreOptions{Mode: backup.Replace, DataDir: dir, Secrets: true, SecretStore: sec}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(ctx, Options{Store: st, Blobs: blobs, DataDir: dir, Secrets: sec, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	keys, err := restored.ListPGPKeys(ctx)
	if err != nil || len(keys) != 1 || !keys[0].Own || !keys[0].CanSign || keys[0].Fingerprint != info.Fingerprint {
		t.Fatalf("replace: %+v %v", keys, err)
	}

	// Merge: into a running installation.
	other := pgpTestApp(t)
	if _, err := backup.Restore(ctx, ex, backup.RestoreOptions{Mode: backup.Merge, Store: other.st, Blobs: other.blobs, Secrets: true, SecretStore: other.opts.Secrets}); err != nil {
		t.Fatal(err)
	}
	if keys, err := other.ListPGPKeys(ctx); err != nil || len(keys) != 1 || !keys[0].Own {
		t.Fatalf("merge: %+v %v", keys, err)
	}

	// Without credentials: the public key only.
	plain := create(backup.Scope{Mode: backup.ModeFull}, "correct horse")
	ex2, err := backup.Extract(ctx, src(plain), "correct horse", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex2.Close()
	if sec, err := ex2.Secrets(); err != nil || len(sec) != 0 {
		t.Fatalf("private key without the option: %v %v", sec, err)
	}
}

type pgpClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *pgpClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *pgpClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func pgpMail(t *testing.T, body string, to ...*pgp.Key) []byte {
	t.Helper()
	ent, err := pgp.EncryptMIME([]byte("Content-Type: text/plain\r\n\r\n"+body+"\r\n"), to, pgp.EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("From: bob@example.org\r\nSubject: s\r\nMessage-ID: <a@b>\r\nMIME-Version: 1.0\r\n"), ent.Bytes()...)
}

func pgpCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestPGPPassphraseLifecycle(t *testing.T) {
	const pass, pass2 = "erste-passphrase-lang", "zweite-passphrase-lang"
	a := pgpTestApp(t)
	clock := &pgpClock{t: time.Now()}
	a.opts.Now = clock.now
	ctx := context.Background()

	if _, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "me@example.org", Passphrase: "kurz"}); pgpCode(err) != "pgp_passphrase_short" {
		t.Fatalf("short: %v", err)
	}
	info, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "me@example.org", Passphrase: pass})
	if err != nil || !info.Protected || !info.Locked || !info.Own || !info.CanSign {
		t.Fatalf("generate: %+v %v", info, err)
	}
	k, err := a.pgpRing().Get(ctx, info.Fingerprint)
	if err != nil || !k.IsLocked() {
		t.Fatalf("not locked: %v", err)
	}
	raw := pgpMail(t, "GEHEIMWORT", k.Public())

	// Locked: reading neither decrypts nor waits, and says which key is missing.
	done := make(chan RenderedMessage, 1)
	go func() {
		r, err := a.render.Render(ctx, raw)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	select {
	case r := <-done:
		if strings.Contains(r.Text, "GEHEIMWORT") || !r.Encrypted {
			t.Fatalf("locked render: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading with a locked key hangs")
	}
	if pm, err := parse.Parse(raw, parse.Options{KeyRing: a.pgpRing()}); err != nil || pm.PGP.Decrypted || !slices.Contains(pm.PGP.LockedKeys, info.Fingerprint) {
		t.Fatalf("locked state: %+v %v", pm.PGP, err)
	}
	// Background analysis does not decrypt, locked or not.
	an, err := a.analyze(ctx, store.Message{ID: 1}, raw)
	if err != nil || strings.Contains(an.msg.Text, "GEHEIMWORT") {
		t.Fatalf("analysis: %v", err)
	}

	// Guessing is limited; then even the right one has to wait.
	for i := 0; i < pgpMaxFailures; i++ {
		if _, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: "falsch"}); pgpCode(err) != "pgp_wrong_passphrase" {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: pass}); pgpCode(err) != "pgp_unlock_throttled" {
		t.Fatalf("throttle: %v", err)
	}
	if _, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: pass, New: pass2}); pgpCode(err) != "pgp_unlock_throttled" {
		t.Fatalf("throttle on change: %v", err)
	}
	clock.advance(pgpFailureWindow)

	got, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: pass})
	if err != nil || got.Locked || !got.Protected || got.UnlockedUntil == "" {
		t.Fatalf("unlock: %+v %v", got, err)
	}
	if r, err := a.render.Render(ctx, raw); err != nil || !strings.Contains(r.Text, "GEHEIMWORT") {
		t.Fatalf("unlocked render: %+v %v", r, err)
	}

	// The time limit also ends what was decrypted in the caches.
	a.rendered.put("x", RenderedMessage{Text: "GEHEIMWORT"})
	clock.advance(pgpUnlockTTL)
	a.pgpSweep()
	if _, ok := a.rendered.get("x"); ok {
		t.Fatal("decrypted text survives the passphrase")
	}
	if r, _ := a.render.Render(ctx, raw); strings.Contains(r.Text, "GEHEIMWORT") {
		t.Fatal("still unlocked after the time limit")
	}
	// So does "lock".
	if _, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	a.rendered.put("x", RenderedMessage{Text: "GEHEIMWORT"})
	a.LockPGPKeys()
	if _, ok := a.rendered.get("x"); ok {
		t.Fatal("decrypted text survives locking")
	}
	if r, _ := a.render.Render(ctx, raw); strings.Contains(r.Text, "GEHEIMWORT") {
		t.Fatal("still unlocked after locking")
	}

	// Export needs the current passphrase and stays protected.
	if _, err := a.ExportPGPSecretKey(ctx, info.ID, PGPExportInput{Passphrase: pass2, Current: "falsch"}); pgpCode(err) != "pgp_wrong_passphrase" {
		t.Fatalf("export: %v", err)
	}
	armored, err := a.ExportPGPSecretKey(ctx, info.ID, PGPExportInput{Passphrase: pass2, Current: pass})
	if err != nil {
		t.Fatal(err)
	}
	if e, err := pgp.ReadKey([]byte(armored)); err != nil || !e.IsLocked() {
		t.Fatalf("export not locked: %v", err)
	}

	// Change: old one checked, then only the new one works.
	if _, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: "falsch", New: pass2}); pgpCode(err) != "pgp_wrong_passphrase" {
		t.Fatalf("change: %v", err)
	}
	if _, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: pass, New: "kurz"}); pgpCode(err) != "pgp_passphrase_short" {
		t.Fatalf("change short: %v", err)
	}
	if got, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: pass, New: pass2}); err != nil || !got.Locked {
		t.Fatalf("change: %+v %v", got, err)
	}
	if _, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: pass}); pgpCode(err) != "pgp_wrong_passphrase" {
		t.Fatalf("old passphrase: %v", err)
	}
	if _, err := a.UnlockPGPKey(ctx, info.ID, PGPUnlockInput{Passphrase: pass2}); err != nil {
		t.Fatal(err)
	}
	// Remove: needs the current one as well, also while unlocked.
	if _, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: pass}); pgpCode(err) != "pgp_wrong_passphrase" {
		t.Fatalf("remove: %v", err)
	}
	got, err = a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{Current: pass2})
	if err != nil || got.Protected || got.Locked {
		t.Fatalf("remove: %+v %v", got, err)
	}
	if r, err := a.render.Render(ctx, raw); err != nil || !strings.Contains(r.Text, "GEHEIMWORT") {
		t.Fatalf("after removing: %+v %v", r, err)
	}
	// Set on an unprotected key.
	if got, err := a.ChangePGPPassphrase(ctx, info.ID, PGPPassphraseInput{New: pass}); err != nil || !got.Locked {
		t.Fatalf("set: %+v %v", got, err)
	}
	if !func() bool { k, err := a.pgpRing().Get(ctx, info.Fingerprint); return err == nil && k.IsLocked() }() {
		t.Fatal("not protected after setting")
	}
}

// Import: protected keys stay protected, with or without the passphrase; an
// unprotected key takes the passphrase as protection.
func TestPGPImportKeepsProtection(t *testing.T) {
	const pass = "import-passphrase-lang"
	a := pgpTestApp(t)
	ctx := context.Background()
	b64 := func(k *pgp.Key) string {
		s, err := k.ArmoredSecret()
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString([]byte(s))
	}
	locked, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "a@example.org"}}, Passphrase: []byte(pass)})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := a.ImportPGPKeys(ctx, PGPImportInput{Data: b64(locked)})
	if err != nil || !keys[0].Protected || !keys[0].Locked {
		t.Fatalf("without passphrase: %+v %v", keys, err)
	}
	if _, err := a.ImportPGPKeys(ctx, PGPImportInput{Data: b64(locked), Passphrase: "falsch"}); pgpCode(err) != "pgp_wrong_passphrase" {
		t.Fatalf("wrong: %v", err)
	}
	keys, err = a.ImportPGPKeys(ctx, PGPImportInput{Data: b64(locked), Passphrase: pass})
	if err != nil || !keys[0].Protected || keys[0].Locked {
		t.Fatalf("with passphrase: %+v %v", keys, err)
	}
	if k, err := a.pgpRing().Get(ctx, locked.Fingerprint()); err != nil || !k.IsLocked() {
		t.Fatalf("protection removed: %v", err)
	}

	open, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "b@example.org"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ImportPGPKeys(ctx, PGPImportInput{Data: b64(open), Passphrase: "kurz"}); pgpCode(err) != "pgp_passphrase_short" {
		t.Fatalf("short: %v", err)
	}
	keys, err = a.ImportPGPKeys(ctx, PGPImportInput{Data: b64(open), Passphrase: pass})
	if err != nil || !keys[0].Protected || !keys[0].Locked {
		t.Fatalf("protect on import: %+v %v", keys, err)
	}
}

// One locked key must not stop a mail that another key can open.
func TestPGPOtherKeyStillDecrypts(t *testing.T) {
	const pass = "andere-passphrase-lang"
	a := pgpTestApp(t)
	ctx := context.Background()
	one, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "one@example.org", Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "two@example.org", Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := a.pgpRing().Get(ctx, one.Fingerprint)
	k2, _ := a.pgpRing().Get(ctx, two.Fingerprint)
	raw := pgpMail(t, "GEHEIMWORT", k1.Public(), k2.Public())
	if _, err := a.UnlockPGPKey(ctx, two.ID, PGPUnlockInput{Passphrase: pass}); err != nil {
		t.Fatal(err)
	}
	if pm, err := parse.Parse(raw, parse.Options{KeyRing: a.pgpRing()}); err != nil || !pm.PGP.Decrypted || len(pm.PGP.LockedKeys) != 0 {
		t.Fatalf("%+v %v", pm.PGP, err)
	}
}

// Backup and restore: the key stays protected and works with its passphrase.
func TestPGPProtectedKeyBackup(t *testing.T) {
	const pass = "sicherung-passphrase-lang"
	a := pgpTestApp(t)
	ctx := context.Background()
	info, err := a.GeneratePGPKey(ctx, PGPGenerateInput{Email: "me@example.org", Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := a.pgpRing().Get(ctx, info.Fingerprint)
	raw := pgpMail(t, "GEHEIMWORT", k.Public())
	var buf bytes.Buffer
	if _, err := backup.Create(ctx, &buf, backup.Options{Store: a.st, Blobs: a.blobs, Secrets: a.opts.Secrets, SecretRefs: a.secretRefs(),
		Scope: backup.Scope{Mode: backup.ModeFull, Secrets: true}, Passphrase: "correct horse", TempDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte(pass)) {
		t.Fatal("passphrase in the archive")
	}
	ex, err := backup.Extract(ctx, backup.Source{Name: "a", Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }}, "correct horse", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	sec, err := ex.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	inArchive, err := base64.StdEncoding.DecodeString(string(sec["pgp:"+info.Fingerprint]))
	if err != nil {
		t.Fatal(err)
	}
	if ak, err := pgp.ReadKey(inArchive); err != nil || !ak.IsLocked() {
		t.Fatalf("archive holds the key unprotected: %v", err)
	}
	if bytes.Contains(inArchive, []byte(pass)) {
		t.Fatal("passphrase next to the key")
	}

	check := func(name string, b *App) {
		t.Helper()
		keys, err := b.ListPGPKeys(ctx)
		if err != nil || len(keys) != 1 || !keys[0].Protected || !keys[0].Locked {
			t.Fatalf("%s: %+v %v", name, keys, err)
		}
		if r, _ := b.render.Render(ctx, raw); strings.Contains(r.Text, "GEHEIMWORT") {
			t.Fatalf("%s: readable without the passphrase", name)
		}
		if _, err := b.UnlockPGPKey(ctx, keys[0].ID, PGPUnlockInput{Passphrase: "falsch"}); pgpCode(err) != "pgp_wrong_passphrase" {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := b.UnlockPGPKey(ctx, keys[0].ID, PGPUnlockInput{Passphrase: pass}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r, err := b.render.Render(ctx, raw); err != nil || !strings.Contains(r.Text, "GEHEIMWORT") {
			t.Fatalf("%s: %+v %v", name, r, err)
		}
	}

	dir := t.TempDir()
	mem := newMemorySecrets()
	if _, err := backup.Restore(ctx, ex, backup.RestoreOptions{Mode: backup.Replace, DataDir: dir, Secrets: true, SecretStore: mem}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(ctx, Options{Store: st, Blobs: blobs, DataDir: dir, Secrets: mem, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	check("replace", restored)

	other := pgpTestApp(t)
	if _, err := backup.Restore(ctx, ex, backup.RestoreOptions{Mode: backup.Merge, Store: other.st, Blobs: other.blobs, Secrets: true, SecretStore: other.opts.Secrets}); err != nil {
		t.Fatal(err)
	}
	check("merge", other)
}
