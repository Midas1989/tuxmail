// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/devserver"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// The wizard must propose the BEST settings, not the first found: the
// ISPDB document lists a dead IMAP server first and the working one second
// (with the right password the test login succeeds) -> the second is chosen,
// the first is listed with its error.
func TestDiscoveryPrefersWorkingCandidate(t *testing.T) {
	srv, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().(*net.TCPAddr)
	dead.Close() // nothing listens there any more
	imapH, imapP, _ := net.SplitHostPort(srv.IMAPAddr())
	smtpH, smtpP, _ := net.SplitHostPort(srv.SMTPAddr())
	doc := fmt.Sprintf(`<?xml version="1.0"?>
<clientConfig version="1.1"><emailProvider id="tuxmail.test"><domain>tuxmail.test</domain><displayName>Dev</displayName>
<incomingServer type="imap"><hostname>%s</hostname><port>%d</port><socketType>plain</socketType><authentication>password-cleartext</authentication><username>%%EMAILADDRESS%%</username></incomingServer>
<incomingServer type="imap"><hostname>%s</hostname><port>%s</port><socketType>plain</socketType><authentication>password-cleartext</authentication><username>%%EMAILADDRESS%%</username></incomingServer>
<outgoingServer type="smtp"><hostname>%s</hostname><port>%s</port><socketType>plain</socketType><authentication>password-cleartext</authentication><username>%%EMAILADDRESS%%</username></outgoingServer>
</emailProvider></clientConfig>`, deadAddr.IP, deadAddr.Port, imapH, imapP, smtpH, smtpP)
	ispdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(doc))
	}))
	defer ispdb.Close()

	d := &autoconfig.Discoverer{HTTPClient: ispdb.Client(), ISPDBURL: ispdb.URL + "/v1.1/", AllowHTTP: true, AlwaysHeuristics: true, Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res := discoverRanked(ctx, d, devserver.AnnaAddr, devserver.DefaultPassword)
	if res.Incoming == nil || net.JoinHostPort(res.Incoming.Host, fmt.Sprint(res.Incoming.Port)) != srv.IMAPAddr() {
		t.Fatalf("chose %+v, want %s (candidates %+v)", res.Incoming, srv.IMAPAddr(), res.Candidates)
	}
	if !res.Verified || len(res.Candidates) < 2 {
		t.Fatalf("result %+v", res)
	}
	best, bad := res.Candidates[0], res.Candidates[len(res.Candidates)-1]
	if !best.In.Reachable || best.In.Login != "ok" || best.Out.Login != "ok" {
		t.Errorf("best not verified with login: %+v", best)
	}
	if bad.In.Reachable || bad.In.Error == "" {
		t.Errorf("dead candidate should report its error: %+v", bad)
	}

	// A wrong password is reported per candidate (login failed), the
	// reachable server is still preferred over the dead one.
	res = discoverRanked(ctx, d, devserver.AnnaAddr, "wrong")
	if res.Incoming == nil || res.Incoming.Port == deadAddr.Port || res.Candidates[0].In.Login != "failed" {
		t.Errorf("wrong password: %+v", res.Candidates)
	}
}

func TestRankPrefersEncryptionAndIMAP(t *testing.T) {
	mk := func(kind domain.AccountKind, tls domain.TLSMode, host string) autoconfig.Ranked {
		return autoconfig.Ranked{Candidate: autoconfig.Candidate{Kind: kind,
			Incoming: domain.ServerConfig{Host: host, TLS: tls}, Outgoing: domain.ServerConfig{Host: host, TLS: tls}},
			In: autoconfig.Check{Reachable: true}, Out: autoconfig.Check{Reachable: true}}
	}
	o := autoconfig.ProbeOptions{Domains: []string{"example.org"}}
	cases := []struct {
		name        string
		better, low autoconfig.Ranked
	}{
		{"implicit TLS over plain", mk(domain.KindIMAP, domain.TLSImplicit, "mail.example.org"), mk(domain.KindIMAP, domain.TLSNone, "mail.example.org")},
		{"STARTTLS over plain", mk(domain.KindIMAP, domain.TLSStartTLS, "mail.example.org"), mk(domain.KindIMAP, domain.TLSNone, "mail.example.org")},
		{"IMAP over POP3", mk(domain.KindIMAP, domain.TLSImplicit, "mail.example.org"), mk(domain.KindPOP3, domain.TLSImplicit, "mail.example.org")},
		{"own domain over foreign host", mk(domain.KindIMAP, domain.TLSImplicit, "imap.example.org"), mk(domain.KindIMAP, domain.TLSImplicit, "imap.hoster.example")},
		{"IMAP over EAS unless chosen", mk(domain.KindIMAP, domain.TLSNone, "mail.example.org"), autoconfig.Ranked{Candidate: autoconfig.Candidate{Kind: domain.KindEAS}}},
	}
	for _, c := range cases {
		if autoconfig.Score(c.better, o) <= autoconfig.Score(c.low, o) {
			t.Errorf("%s: %d <= %d", c.name, autoconfig.Score(c.better, o), autoconfig.Score(c.low, o))
		}
	}
	unreachable := mk(domain.KindIMAP, domain.TLSImplicit, "mail.example.org")
	unreachable.In.Reachable = false
	if autoconfig.Score(unreachable, o) >= autoconfig.Score(mk(domain.KindIMAP, domain.TLSNone, "x.example"), o) {
		t.Error("an unreachable server must rank below a working one")
	}
}
