// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/devserver"
)

// devserverCmd runs the development mail server (internal/devserver). It is
// a hidden command for developers and the e2e tests (docs/ENTWICKLUNG.md):
//
//	tuxmail devserver [--imap ADDR] [--smtp ADDR] [--http ADDR] [--user ADDR:PASSWORD[:NAME]]... [--no-seed]
//	tuxmail devserver --external-imap 127.0.0.1:993 --external-ca /run/tuxmail-dovecot/ca.pem   (Dovecot/Postfix)
func (c *cli) devserverCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devserver", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	imapAddr := fs.String("imap", "127.0.0.1:1143", "IMAP listen address (plaintext)")
	smtpAddr := fs.String("smtp", "127.0.0.1:1587", "SMTP listen address (plaintext, AUTH PLAIN/LOGIN)")
	httpAddr := fs.String("http", "127.0.0.1:1080", "HTTP control/autoconfig listen address")
	domain := fs.String("domain", "", "mail domain of the autoconfig document (default: domain of the first user)")
	noSeed := fs.Bool("no-seed", false, "do not add sample mail")
	verbose := fs.Bool("verbose", false, "log protocol errors and deliveries")
	profile := fs.String("profile", "", `folder profile: "" (English folders) or "german" (Dovecot-like German folders with SPECIAL-USE, "Archive" plus "Archiv/2024")`)
	bulk := fs.Int("bulk", 0, "add this many generated mails to the INBOX of every user (plus a share in the other folders) for large-mailbox tests")
	extIMAP := fs.String("external-imap", "", "use a real IMAP server (implicit TLS host:port, e.g. Dovecot 127.0.0.1:993) instead of the in-memory one")
	extSMTP := fs.String("external-smtp", "127.0.0.1:25", "with --external-imap: inbound SMTP (MX) used by /api/send")
	extSub := fs.String("external-submission", "127.0.0.1:587", "with --external-imap: submission server (STARTTLS) for the autoconfig document")
	extCA := fs.String("external-ca", "", "with --external-imap: PEM file with the CA of the self-signed certificate")
	var users userFlags
	fs.Var(&users, "user", "user as ADDRESS:PASSWORD[:NAME] (repeatable; default anna@ and ben@"+devserver.DefaultDomain+" with password "+devserver.DefaultPassword+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: devserver takes no arguments", errUsage)
	}
	if len(users) == 0 {
		users = devserver.DefaultUsers()
	}
	lvl := slog.LevelWarn
	if *verbose {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: lvl}))
	cfg := devserver.Config{IMAPAddr: *imapAddr, SMTPAddr: *smtpAddr, HTTPAddr: *httpAddr, Domain: *domain, Users: users, Logger: log}
	switch *profile {
	case "":
	case "german":
		cfg.Folders, cfg.SpecialUse = devserver.GermanFolders, devserver.GermanSpecialUse
	default:
		return fmt.Errorf("%w: unknown --profile %q", errUsage, *profile)
	}
	if *extIMAP != "" {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if *extCA != "" {
			pem, err := os.ReadFile(*extCA)
			if err != nil {
				return err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return fmt.Errorf("%s: no PEM certificates", *extCA)
			}
			tc.RootCAs = pool
		}
		cfg.External = &devserver.External{IMAP: *extIMAP, SMTP: *extSMTP, Submission: *extSub, TLSConfig: tc}
	}
	s, err := devserver.Start(cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	if !*noSeed {
		switch {
		case cfg.External != nil:
			err = devserver.SeedIMAP(cfg.External.IMAP, cfg.External.TLSConfig, true, users, time.Now())
		case *profile == "":
			err = s.Seed(time.Now())
		}
		if err != nil {
			return err
		}
	}
	if *bulk > 0 && cfg.External == nil {
		start := time.Now()
		if err := seedBulk(s, cfg, *bulk); err != nil {
			return err
		}
		fmt.Fprintf(c.stderr, "devserver: %d generated mails per user added in %s\n", *bulk, time.Since(start).Round(time.Millisecond))
	}
	info := map[string]any{"imap": s.IMAPAddr(), "smtp": s.SMTPAddr(), "http": s.HTTPAddr(), "domain": s.Domain(),
		"ispdb": "http://" + s.HTTPAddr() + "/v1.1/"}
	var names []string
	for _, u := range s.Users() {
		names = append(names, u.Email)
	}
	info["users"] = names
	b, _ := json.Marshal(info)
	fmt.Fprintln(c.stdout, string(b))
	fmt.Fprintf(c.stderr, "TuxMail devserver running (IMAP %s, SMTP %s, HTTP %s). Not for production use. Ctrl+C stops.\n",
		s.IMAPAddr(), s.SMTPAddr(), s.HTTPAddr())
	<-ctx.Done()
	return nil
}

type userFlags []devserver.User

func (u *userFlags) String() string { return fmt.Sprint(len(*u)) }

func (u *userFlags) Set(v string) error {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) < 2 || !strings.Contains(parts[0], "@") || parts[1] == "" {
		return fmt.Errorf("expected ADDRESS:PASSWORD[:NAME], got %q", v)
	}
	usr := devserver.User{Email: parts[0], Password: parts[1]}
	if len(parts) == 3 {
		usr.Name = parts[2]
	}
	*u = append(*u, usr)
	return nil
}

// seedBulk fills every user's INBOX with n mails and the other folders with
// a share of it (Trash and the archives larger, the rest smaller).
func seedBulk(s *devserver.Server, cfg devserver.Config, n int) error {
	folders := cfg.Folders
	if len(folders) == 0 {
		folders = devserver.DefaultFolders
	}
	now := time.Now()
	for _, u := range s.Users() {
		for i, f := range folders {
			k := n / 20
			lf := strings.ToLower(f)
			switch {
			case f == "INBOX":
				k = n
			case lf == "trash" || lf == "papierkorb" || strings.HasPrefix(lf, "archiv"):
				k = n / 4
			}
			if k == 0 {
				continue
			}
			if err := s.SeedBulk(u.Email, f, k, now, uint64(i+1)); err != nil {
				return err
			}
		}
	}
	return nil
}
