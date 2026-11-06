// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
