// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/devserver"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// One walk through the commands against the in-memory development mail server
// (real IMAP and SMTP on loopback, never a real account).
func TestMailLiveAgainstDevserver(t *testing.T) {
	ctx := context.Background()
	mailDrainTimeout = 20 * time.Second
	srv, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := srv.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	passFile := filepath.Join(t.TempDir(), "vault")
	const pass = "correct horse battery staple"
	if err := os.WriteFile(passFile, []byte(pass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, st, err := openDesktopApp(ctx, dataLocation{Dir: dir, Portable: true}, log, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UnlockVault(ctx, pass); err != nil {
		t.Fatal(err)
	}
	a.Start()
	server := func(addr string) *app.ServerConfig {
		h, p, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(p)
		return &app.ServerConfig{Host: h, Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: devserver.AnnaAddr}
	}
	if _, err := a.CreateAccount(ctx, app.NewAccountInput{Kind: "imap", Email: devserver.AnnaAddr, Name: "Anna Berger",
		Password: devserver.DefaultPassword, Incoming: server(srv.IMAPAddr()), Outgoing: server(srv.SMTPAddr())}); err != nil {
		t.Fatal(err)
	}
	var target int64
	for deadline := time.Now().Add(15 * time.Second); target == 0; time.Sleep(100 * time.Millisecond) {
		page, err := a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if m.Subject == "Mittagessen morgen?" {
				target = m.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("seed mail not synced")
		}
	}
	a.Close()
	st.Close()

	base := []string{"--data-dir", dir, "--portable", "--vault-passphrase-file", passFile}
	mail := func(stdin string, args ...string) (int, string, string) {
		return runCLI(t, stdin, append(append([]string{"mail"}, args...), base...)...)
	}
	id := strconv.FormatInt(target, 10)

	if code, out, errs := mail("Hallo Ben,\nTest aus dem Terminal.\n", "send", "--to", devserver.BenAddr, "--subject", "Terminal-Test"); code != 0 || !strings.HasPrefix(out, "sent\t") {
		t.Fatal(code, out, errs)
	}
	got, err := srv.Messages(devserver.BenAddr, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got {
		found = found || m.Subject == "Terminal-Test"
	}
	if !found {
		t.Fatal("mail did not reach the SMTP server")
	}
	if code, out, errs := mail("Gerne!\n", "reply", id, "--json"); code != 0 || !strings.Contains(out, `"blocked":false`) {
		t.Fatal(code, out, errs)
	}
	// The send checks stop a forgotten attachment until it is confirmed.
	if code, out, errs := mail("anbei der Tischplan", "send", "--to", devserver.BenAddr, "--subject", "Plan"); code != 5 || !strings.Contains(out, "forgotten_attachment") {
		t.Fatal(code, out, errs)
	}
	if code, out, errs := mail("anbei der Tischplan", "send", "--to", devserver.BenAddr, "--subject", "Plan", "--confirm", "forgotten_attachment"); code != 0 {
		t.Fatal(code, out, errs)
	}
	if code, out, errs := mail("", "mark", id, "flagged"); code != 0 || errs != "" {
		t.Fatal(code, out, errs)
	}
	if code, out, errs := mail("", "move", id, "archive"); code != 0 || errs != "" {
		t.Fatal(code, out, errs)
	}
	msgs, err := srv.Messages(devserver.AnnaAddr, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	moved := false
	for _, m := range msgs {
		moved = moved || m.Subject == "Mittagessen morgen?"
	}
	if !moved {
		t.Fatal("the move did not reach the IMAP server")
	}
}
