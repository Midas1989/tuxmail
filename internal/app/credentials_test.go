// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/send"
)

// evilServer counts who connects: the stored password must not get on the way there.
func evilServer(t *testing.T) (domain.ServerConfig, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return domain.ServerConfig{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"}, &n
}

func TestStoredPasswordDoesNotFollowAMovedAccount(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	evil, hits := evilServer(t)
	other := func(mod func(*domain.ServerConfig)) *domain.ServerConfig {
		c := e.srv
		mod(&c)
		return &c
	}
	for name, u := range map[string]AccountUpdate{
		"other server":   {Incoming: &evil},
		"other port":     {Incoming: other(func(c *domain.ServerConfig) { c.Port = evil.Port })},
		"other user":     {Incoming: other(func(c *domain.ServerConfig) { c.Username = "bob" })},
		"plain text":     {Incoming: other(func(c *domain.ServerConfig) { c.TLS = domain.TLSNone; c.Host = "mail.example.org" })},
		"outgoing":       {Outgoing: &evil},
		"sieve server":   {Sieve: &SieveConfig{Host: "127.0.0.1", Port: evil.Port, TLS: "none"}},
		"sieve port":     {Sieve: &SieveConfig{Port: evil.Port}},
		"verify as well": {Incoming: &evil, Verify: true},
	} {
		u := u
		if _, err := e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Update: &u}); errCode(err) != CodePasswordRequired {
			t.Errorf("test, %s: %v", name, err)
		}
		if _, err := e.a.UpdateAccount(ctx, e.acc, u); errCode(err) != CodePasswordRequired {
			t.Errorf("update, %s: %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d connections to the other server", hits.Load())
	}
	before, _ := e.st.GetAccount(ctx, e.acc)
	if before.Incoming.Host != e.srv.Host || before.Incoming.Port != e.srv.Port || before.Extra["sieveHost"] != "" {
		t.Fatalf("refused change was saved: %+v", before)
	}
	if _, err := e.a.SetSieveConfig(ctx, e.acc, SieveConfig{Host: "127.0.0.1", Port: evil.Port}); errCode(err) != CodePasswordRequired {
		t.Fatalf("sieve route: %v", err)
	}
	// What does not move the credentials needs nothing.
	name, same := "Neu", e.srv
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{DisplayName: &name, Incoming: &same}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Kinds: []string{"imap"}, Update: &AccountUpdate{Incoming: &same}}); err != nil {
		t.Fatal(err)
	}
	// With a new password the normal way stays open, and it is the one that is sent.
	res, err := e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{Kinds: []string{"imap"}, Password: "neu", Update: &AccountUpdate{Incoming: &evil}})
	if err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if hits.Load() == 0 {
		t.Fatal("the test with a new password did not run")
	}
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Incoming: &evil, Password: "neues-passwort"}); err != nil {
		t.Fatal(err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "neues-passwort" {
		t.Fatalf("password %q", pw)
	}
}

func TestSMTPAloneGetsItsOwnSecret(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	smtp := domain.ServerConfig{Host: "smtp.example.org", Port: 465, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: "alice"}
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Outgoing: &smtp}); errCode(err) != CodePasswordRequired {
		t.Fatal(err)
	}
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Outgoing: &smtp, Password: "smtp-pw"}); err != nil {
		t.Fatal(err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "secret" {
		t.Fatalf("incoming password overwritten: %q", pw)
	}
	if pw, _ := e.sec.Get(acc.Extra[send.ExtraSMTPSecretRef]); string(pw) != "smtp-pw" {
		t.Fatalf("SMTP password %q", pw)
	}
}

func TestSievePasswordOfItsOwn(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	cfg := SieveConfig{Host: "sieve.example.org", Port: 4190, Password: "sieve-pw"}
	if _, err := e.a.SetSieveConfig(ctx, e.acc, cfg); err != nil {
		t.Fatal(err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	ref := acc.Extra[extraSieveSecretRef]
	if pw, _ := e.sec.Get(ref); string(pw) != "sieve-pw" {
		t.Fatalf("%q", pw)
	}
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "secret" {
		t.Fatalf("account password overwritten: %q", pw)
	}
	if pw, err := e.a.sievePassword(ctx, acc, e.a.easCredentials()); err != nil || pw != "sieve-pw" {
		t.Fatal(pw, err)
	}
	if got, _ := e.a.GetSieveConfig(ctx, e.acc); got.Password != "" {
		t.Fatal("password returned")
	}
	// The stored one stays with that server.
	cfg.Password = ""
	if _, err := e.a.SetSieveConfig(ctx, e.acc, cfg); err != nil {
		t.Fatal("same server:", err)
	}
	cfg.Host = "evil.example.org"
	if _, err := e.a.SetSieveConfig(ctx, e.acc, cfg); errCode(err) != CodePasswordRequired {
		t.Fatal(err)
	}
	// Back to the default: nothing to bring, the own password goes.
	if _, err := e.a.SetSieveConfig(ctx, e.acc, SieveConfig{}); err != nil {
		t.Fatal(err)
	}
	acc, _ = e.st.GetAccount(ctx, e.acc)
	if acc.Extra[extraSieveSecretRef] != "" {
		t.Fatal("reference left")
	}
	if _, err := e.sec.Get(ref); err == nil {
		t.Fatal("password left")
	}
}

func TestSieveServerThroughTheAccountDialog(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	if _, err := e.a.UpdateAccount(ctx, e.acc, AccountUpdate{Sieve: &SieveConfig{Host: "sieve.example.org", Port: 4190}, Password: "sieve-pw"}); err != nil {
		t.Fatal(err)
	}
	acc, _ := e.st.GetAccount(ctx, e.acc)
	if pw, _ := e.sec.Get(acc.Extra[extraSieveSecretRef]); string(pw) != "sieve-pw" {
		t.Fatalf("%q", pw)
	}
	if pw, _ := e.sec.Get(acc.SecretRef); string(pw) != "secret" {
		t.Fatalf("account password overwritten: %q", pw)
	}
}

func TestOAuthAccountCannotMove(t *testing.T) {
	acc := domain.Account{Kind: domain.KindIMAP, SecretRef: "x", Incoming: &domain.ServerConfig{Host: "a", Port: 993, Auth: domain.AuthOAuth2},
		Extra: map[string]string{oauth.ExtraProvider: "google"}}
	next := acc
	next.Incoming = &domain.ServerConfig{Host: "evil", Port: 993, Auth: domain.AuthOAuth2}
	err := requireNewCredential(acc, next, "egal")
	if errCode(err) != CodePasswordRequired || !strings.Contains(err.Error(), "OAuth") {
		t.Fatal(err)
	}
	if err := requireNewCredential(acc, acc, ""); err != nil {
		t.Fatal(err)
	}
	// Nothing stored: nothing to protect.
	free := domain.Account{Kind: domain.KindIMAP, Incoming: &domain.ServerConfig{Host: "a", Port: 993, Auth: domain.AuthPassword}}
	moved := free
	moved.Incoming = &domain.ServerConfig{Host: "b", Port: 993, Auth: domain.AuthPassword}
	if err := requireNewCredential(free, moved, ""); err != nil {
		t.Fatal(err)
	}
}

func TestWebDAVTargetAndAIKeyStayWhereTheyWere(t *testing.T) {
	ctx := context.Background()
	e := newEditEnv(t)
	in := BackupScheduleInput{Passphrase: "eine lange passphrase", TargetPassword: "dav-pw"}
	in.Name, in.Frequency, in.Hour = "x", "daily", 2
	in.Scope.Mode = "config"
	in.Target.Kind, in.Target.URL, in.Target.Username = "webdav", "https://dav.example.org/x", "alice"
	s, err := e.a.SaveBackupSchedule(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	up := BackupScheduleInput{Schedule: s}
	if _, err := e.a.SaveBackupSchedule(ctx, up); err != nil {
		t.Fatal("unchanged:", err)
	}
	for name, mod := range map[string]func(*BackupScheduleInput){
		"url":  func(i *BackupScheduleInput) { i.Target.URL = "https://evil.example/x" },
		"user": func(i *BackupScheduleInput) { i.Target.Username = "bob" },
	} {
		x := BackupScheduleInput{Schedule: s}
		mod(&x)
		if _, err := e.a.SaveBackupSchedule(ctx, x); errCode(err) != CodePasswordRequired {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, _ := e.a.getSchedule(ctx, s.ID)
	if got.Target.URL != "https://dav.example.org/x" {
		t.Fatal(got.Target.URL)
	}
	moved := BackupScheduleInput{Schedule: s, TargetPassword: "neu"}
	moved.Target.URL = "https://new.example/x"
	if _, err := e.a.SaveBackupSchedule(ctx, moved); err != nil {
		t.Fatal(err)
	}

	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "openai", BaseURL: "https://api.openai.com", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "openai", BaseURL: "https://api.openai.com", Model: "m"}); err != nil {
		t.Fatal("same service:", err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "openai", BaseURL: "https://evil.example"}); errCode(err) != CodePasswordRequired {
		t.Fatal(err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "custom", BaseURL: "https://api.openai.com"}); errCode(err) != CodePasswordRequired {
		t.Fatal(err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "custom", BaseURL: "https://other.example", APIKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.SaveAiSettings(ctx, AiSettings{Provider: "custom", BaseURL: "https://evil.example"}); errCode(err) != CodePasswordRequired {
		t.Fatal("over none:", err)
	}
}
