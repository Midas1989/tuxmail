// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/locales"
)

type editEnv struct {
	a   *App
	st  *store.Store
	sec domain.SecretStore
	acc int64
	srv domain.ServerConfig
}

func newEditEnv(t *testing.T) *editEnv {
	t.Helper()
	ctx := context.Background()
	srv := imapTestServer(t, "Sent")
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sec := secrets.NewMemory()
	a, err := New(ctx, Options{Store: st, DataDir: dir, Locales: locales.FS, Secrets: sec,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	acc, err := a.CreateAccount(ctx, NewAccountInput{Kind: "imap", DisplayName: "Alice", Email: "alice@example.org", Name: "Alice",
		Password: "secret", Incoming: &srv})
	if err != nil {
		t.Fatal(err)
	}
	return &editEnv{a: a, st: st, sec: sec, acc: acc.ID, srv: srv}
}

func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func TestAccountSettingsUpdate(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	s, err := e.a.GetAccountSettings(ctx, e.acc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Email != "alice@example.org" || !s.HasPassword || s.BodyPolicy != "all" || s.IdleMode != "default" ||
		!s.Index.Content || s.Sieve == nil || !s.Editable || strings.Join(s.Tests, ",") != "imap,sieve" {
		t.Fatalf("settings %+v", s)
	}
	s, err = e.a.UpdateAccount(ctx, e.acc, AccountUpdate{DisplayName: ptr("Arbeit"), Color: ptr("#112233"),
		BodyPolicy: ptr("on_demand"), PollMinutes: ptr(15), IdleMode: ptr("custom"), IdleFolders: []int64{7, 3, 7},
		Index: &IndexOptions{Content: true, Attachments: true}, Email: ptr("Alice@Example.org")})
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if acc.DisplayName != "Arbeit" || acc.Color != "#112233" || acc.Extra[tsync.ExtraBodyPolicy] != "on_demand" ||
		acc.Extra[tsync.ExtraPollMinutes] != "15" || acc.Extra[tsync.ExtraIdleFolders] != "3,7" {
		t.Fatalf("stored %+v", acc)
	}
	if s.IdleMode != "custom" || len(s.IdleFolders) != 2 || !s.Index.Attachments || s.PollMinutes != 15 || s.BodyPolicy != "on_demand" {
		t.Fatalf("settings %+v", s)
	}
	// Back to the defaults removes the overrides.
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{BodyPolicy: ptr("all"), PollMinutes: ptr(0), IdleMode: ptr("default")}); err != nil {
		t.Fatal(err)
	}
	acc, _ = e.st.GetAccount(ctx, e.acc)
	for _, k := range []string{tsync.ExtraBodyPolicy, tsync.ExtraPollMinutes, tsync.ExtraIdleFolders} {
		if _, ok := acc.Extra[k]; ok {
			t.Fatalf("%s kept: %+v", k, acc.Extra)
		}
	}

	bad := map[string]AccountUpdate{
		"empty name": {DisplayName: ptr(" ")},
		"color":      {Color: ptr("red")},
		"port":       {Incoming: &domain.ServerConfig{Host: "x", Port: 0, TLS: "tls"}},
		"tls":        {Incoming: &domain.ServerConfig{Host: "x", Port: 1, TLS: "ssl"}},
		"host":       {Incoming: &domain.ServerConfig{Host: "", Port: 993, TLS: "tls"}},
		"policy":     {BodyPolicy: ptr("sometimes")},
		"poll":       {PollMinutes: ptr(-1)},
		"idle":       {IdleMode: ptr("maybe")},
		"eas url":    {EASURL: ptr("https://x")},
		"email":      {Email: ptr("nope")},
	}
	for name, in := range bad {
		if _, err := e.a.UpdateAccount(ctx, e.acc, in); errCode(err) != CodeInvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func TestAccountConnectionTestAndPassword(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	res, err := e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Kinds: []string{"imap"}})
	if err != nil || !res.OK || len(res.Steps) != 1 || res.Steps[0].Kind != "imap" {
		t.Fatalf("stored settings: %+v %v", res, err)
	}
	res, _ = e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Kinds: []string{"imap"}, Password: "wrong"})
	if res.OK || res.Steps[0].Code != "auth_failed" {
		t.Fatalf("wrong password: %+v", res)
	}
	bad := e.srv
	bad.Port = closedPort(t)
	res, _ = e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Kinds: []string{"imap"}, Password: "secret", Update: &AccountUpdate{Incoming: &bad}})
	if res.OK || res.Steps[0].Code != "unreachable" {
		t.Fatalf("closed port: %+v", res)
	}
	// Sieve is part of the full test (no ManageSieve server here).
	res, _ = e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{})
	if res.OK || len(res.Steps) != 2 || !res.Steps[0].OK || res.Steps[1].Kind != "sieve" || res.Steps[1].OK {
		t.Fatalf("full test: %+v", res)
	}

	// Verified updates do not save broken settings (Sieve is optional).
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Incoming: &bad, Password: "secret", Verify: true}); errCode(err) != CodeConnectionFailed {
		t.Fatalf("verify: %v", err)
	}
	if acc, _ := e.st.GetAccount(ctx, e.acc); acc.Incoming.Port != e.srv.Port {
		t.Fatal("broken settings were saved")
	}
	good := e.srv
	good.Username = "alice"
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Incoming: &good, Password: "secret", Verify: true}); err != nil {
		t.Fatalf("verify good: %v", err)
	}

	// Password change: verified by a test login, stored in the secret store.
	if _, err := e.a.ChangeAccountPassword(ctx, e.acc, "wrong", true); errCode(err) != CodeConnectionFailed {
		t.Fatalf("wrong password: %v", err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "secret" {
		t.Fatalf("password changed to %q", pw)
	}
	if _, err := e.a.ChangeAccountPassword(ctx, e.acc, "new-pass", false); err != nil {
		t.Fatal(err)
	}
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "new-pass" {
		t.Fatalf("password %q", pw)
	}
	if _, err := e.a.ChangeAccountPassword(ctx, e.acc, "", false); errCode(err) != CodeInvalidInput {
		t.Fatal(err)
	}
}

func TestAccountIdentities(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	ids, _ := e.st.ListIdentities(ctx, e.acc)
	if len(ids) != 1 || !ids[0].IsDefault {
		t.Fatal(ids)
	}
	primary := ids[0].ID
	alias, err := e.a.CreateIdentity(ctx, Identity{Identity: domain.Identity{AccountID: e.acc, Name: "Support", Email: "support@example.org",
		ReplyTo: "help@example.org", BccSelf: true}, ChatSignatureMode: "short"})
	if err != nil || alias.ID == 0 || alias.IsDefault || alias.ChatSignatureMode != "short" {
		t.Fatalf("%+v %v", alias, err)
	}
	if _, err := e.a.CreateIdentity(ctx, Identity{Identity: domain.Identity{AccountID: e.acc, Email: "bad"}}); errCode(err) != CodeInvalidInput {
		t.Fatal(err)
	}
	if err := e.a.SetDefaultIdentity(ctx, alias.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.st.GetIdentity(ctx, primary); p.IsDefault {
		t.Fatal("primary still default")
	}
	// Deleting the default promotes the remaining identity; the last one stays.
	if err := e.a.DeleteIdentity(ctx, alias.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.st.GetIdentity(ctx, primary); !p.IsDefault {
		t.Fatal("primary not promoted")
	}
	if err := e.a.DeleteIdentity(ctx, primary); errCode(err) != CodeConflict {
		t.Fatal(err)
	}
}

func TestDeleteAccountAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if _, err := e.st.UpsertFolder(ctx, domain.Folder{AccountID: e.acc, RemoteID: "INBOX", Name: "INBOX", Role: domain.RoleInbox}); err != nil {
		t.Fatal(err)
	}
	d, err := e.a.AccountDiagnostics(ctx, e.acc)
	if err != nil || d.Account.SecretRef != "" || len(d.Folders) != 1 || d.Identities != 1 || d.Settings.Account.SecretRef != "" {
		t.Fatalf("%+v %v", d, err)
	}

	// Keep the local data: the account becomes a local one without server and secret.
	if err := e.a.DeleteAccount(ctx, e.acc, DeleteAccountOptions{KeepData: true}); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.GetAccount(ctx, e.acc)
	if err != nil || got.Kind != kindLocal || got.Incoming != nil || got.SecretRef != "" || got.Extra["archivedKind"] != "imap" {
		t.Fatalf("%+v %v", got, err)
	}
	if fs, _ := e.st.ListFolders(ctx, e.acc); len(fs) != 1 {
		t.Fatal("folders removed")
	}
	if ids, _ := e.st.ListIdentities(ctx, e.acc); len(ids) != 0 {
		t.Fatal("identities kept")
	}
	if _, err := e.sec.Get(acc.SecretRef); err == nil {
		t.Fatal("secret kept")
	}
	// Removing it for good.
	if err := e.a.DeleteAccount(ctx, e.acc, DeleteAccountOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetAccount(ctx, e.acc); err == nil {
		t.Fatal("account still there")
	}
}
