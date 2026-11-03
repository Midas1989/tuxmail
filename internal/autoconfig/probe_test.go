// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

type probeFixture struct {
	serverTLS *tls.Config
	clientTLS *tls.Config
}

func newProbeFixture(t *testing.T) *probeFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"localhost", "*.nothing.test", "probe.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &probeFixture{
		serverTLS: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		clientTLS: &tls.Config{RootCAs: pool},
	}
}

func serve(t *testing.T, ln net.Listener, handle func(net.Conn)) string {
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				handle(c)
			}()
		}
	}()
	return ln.Addr().String()
}

// implicitTLS serves greeting over implicit TLS.
func (p *probeFixture) implicitTLS(t *testing.T, greeting string) string {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", p.serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, ln, func(c net.Conn) {
		c.Write([]byte(greeting))
		_, _ = bufio.NewReader(c).ReadString('\n')
	})
}

// startTLSServer emulates the STARTTLS dance of the given protocol.
func (p *probeFixture) startTLSServer(t *testing.T, kind domain.AccountKind) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, ln, func(c net.Conn) {
		br := bufio.NewReader(c)
		switch kind {
		case domain.KindSMTP:
			c.Write([]byte("220 hi\r\n"))
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				switch {
				case strings.HasPrefix(line, "EHLO"):
					c.Write([]byte("250-test\r\n250-SIZE 1000\r\n250 STARTTLS\r\n"))
				case strings.HasPrefix(line, "STARTTLS"):
					c.Write([]byte("220 go ahead\r\n"))
					tls.Server(c, p.serverTLS).Handshake()
					return
				}
			}
		case domain.KindPOP3:
			c.Write([]byte("+OK POP3 ready\r\n"))
			if line, _ := br.ReadString('\n'); strings.HasPrefix(line, "STLS") {
				c.Write([]byte("+OK begin TLS\r\n"))
				tls.Server(c, p.serverTLS).Handshake()
			}
		default:
			c.Write([]byte("* OK [CAPABILITY IMAP4rev1 STARTTLS] ready\r\n"))
			line, _ := br.ReadString('\n')
			tag, _, _ := strings.Cut(line, " ")
			c.Write([]byte("* BYE not really\r\n" + tag + " OK begin TLS\r\n"))
			tls.Server(c, p.serverTLS).Handshake()
		}
	})
}

func hostPort(t *testing.T, addr string) (string, int) {
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(ps)
	return h, port
}

func TestProbeConnect(t *testing.T) {
	p := newProbeFixture(t)
	d := &Discoverer{TLSConfig: p.clientTLS, Timeout: 3 * time.Second}
	ctx := context.Background()

	cases := []struct {
		name string
		kind domain.AccountKind
		mode domain.TLSMode
		addr string
	}{
		{"imaps", domain.KindIMAP, domain.TLSImplicit, p.implicitTLS(t, "* OK ready\r\n")},
		{"smtps", domain.KindSMTP, domain.TLSImplicit, p.implicitTLS(t, "220-multi\r\n220 line\r\n")},
		{"pop3s", domain.KindPOP3, domain.TLSImplicit, p.implicitTLS(t, "+OK hi\r\n")},
		{"imap starttls", domain.KindIMAP, domain.TLSStartTLS, p.startTLSServer(t, domain.KindIMAP)},
		{"smtp starttls", domain.KindSMTP, domain.TLSStartTLS, p.startTLSServer(t, domain.KindSMTP)},
		{"pop3 stls", domain.KindPOP3, domain.TLSStartTLS, p.startTLSServer(t, domain.KindPOP3)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port := hostPort(t, tc.addr)
			cfg := domain.ServerConfig{Host: host, Port: port, TLS: tc.mode}
			if err := d.ProbeConnect(ctx, tc.kind, cfg); err != nil {
				t.Fatalf("probe: %v", err)
			}
		})
	}

	// wrong protocol greeting
	host, port := hostPort(t, p.implicitTLS(t, "220 smtp\r\n"))
	if err := d.ProbeConnect(ctx, domain.KindIMAP, domain.ServerConfig{Host: host, Port: port, TLS: domain.TLSImplicit}); err == nil {
		t.Error("expected greeting mismatch error")
	}
	// untrusted certificate
	d2 := &Discoverer{Timeout: 3 * time.Second}
	if err := d2.ProbeConnect(ctx, domain.KindIMAP, domain.ServerConfig{Host: host, Port: port, TLS: domain.TLSImplicit}); err == nil {
		t.Error("expected certificate error")
	}
	// TLS expected but plain server
	ph, pp := hostPort(t, p.startTLSServer(t, domain.KindIMAP))
	if err := d.ProbeConnect(ctx, domain.KindIMAP, domain.ServerConfig{Host: ph, Port: pp, TLS: domain.TLSImplicit}); err == nil {
		t.Error("expected handshake failure")
	}
	// plain mode
	if err := d.ProbeConnect(ctx, domain.KindIMAP, domain.ServerConfig{Host: ph, Port: pp, TLS: domain.TLSNone}); err != nil {
		t.Errorf("plain probe: %v", err)
	}
}
