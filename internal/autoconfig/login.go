// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// ErrAuthFailed is returned by ProbeLogin when the server rejected the
// credentials (the server itself works).
var ErrAuthFailed = errors.New("autoconfig: login rejected")

// ProbeLogin connects like ProbeConnect and then logs in with username and
// password (IMAP LOGIN, POP3 USER/PASS, SMTP AUTH PLAIN). Nothing is
// stored; the connection is closed right away. A rejected login returns an
// error wrapping ErrAuthFailed.
func (d *Discoverer) ProbeLogin(ctx context.Context, kind domain.AccountKind, cfg domain.ServerConfig, username, password string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	dial := d.DialContext
	if dial == nil {
		dial = netguard.Dialer(&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if d.TLSConfig != nil {
		tlsCfg = d.TLSConfig.Clone()
	}
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = cfg.Host
	}
	var c net.Conn = conn
	if cfg.TLS == domain.TLSImplicit {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("autoconfig: TLS handshake with %s: %w", cfg.Host, err)
		}
		c = tc
	}
	wrap := func(err error) error {
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	switch kind {
	case domain.KindSMTP:
		return wrap(smtpLogin(c, cfg, tlsCfg, username, password))
	case domain.KindPOP3:
		return wrap(pop3Login(c, cfg, tlsCfg, username, password))
	default:
		return wrap(imapLogin(c, cfg, tlsCfg, username, password))
	}
}

func imapLogin(conn net.Conn, cfg domain.ServerConfig, tlsCfg *tls.Config, user, pass string) error {
	opts := &imapclient.Options{TLSConfig: tlsCfg}
	var (
		c   *imapclient.Client
		err error
	)
	if cfg.TLS == domain.TLSStartTLS {
		if c, err = imapclient.NewStartTLS(conn, opts); err != nil {
			return fmt.Errorf("autoconfig: STARTTLS: %w", err)
		}
	} else {
		c = imapclient.New(conn, opts)
		if err := c.WaitGreeting(); err != nil {
			return err
		}
	}
	defer c.Close()
	if err := c.Login(user, pass).Wait(); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	_ = c.Logout().Wait()
	return nil
}

func smtpLogin(conn net.Conn, cfg domain.ServerConfig, tlsCfg *tls.Config, user, pass string) error {
	var (
		c   *gosmtp.Client
		err error
	)
	if cfg.TLS == domain.TLSStartTLS {
		if c, err = gosmtp.NewClientStartTLS(conn, tlsCfg); err != nil {
			return fmt.Errorf("autoconfig: STARTTLS: %w", err)
		}
	} else {
		c = gosmtp.NewClient(conn)
		if err := c.Hello("localhost"); err != nil {
			return err
		}
	}
	defer c.Close()
	if ok, _ := c.Extension("AUTH"); !ok {
		return fmt.Errorf("%w: server offers no AUTH", ErrAuthFailed)
	}
	if err := c.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	_ = c.Quit()
	return nil
}

func pop3Login(conn net.Conn, cfg domain.ServerConfig, tlsCfg *tls.Config, user, pass string) error {
	br := bufio.NewReader(conn)
	if err := readGreeting(br, domain.KindPOP3); err != nil {
		return err
	}
	if cfg.TLS == domain.TLSStartTLS {
		if err := startTLS(conn, br, domain.KindPOP3); err != nil {
			return err
		}
		tc := tls.Client(conn, tlsCfg)
		if err := tc.Handshake(); err != nil {
			return err
		}
		conn, br = tc, bufio.NewReader(tc)
	}
	cmd := func(line string) (string, error) {
		if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
			return "", err
		}
		return readLine(br)
	}
	if r, err := cmd("USER " + user); err != nil || !strings.HasPrefix(r, "+OK") {
		return fmt.Errorf("%w: USER: %s %v", ErrAuthFailed, r, err)
	}
	if r, err := cmd("PASS " + pass); err != nil || !strings.HasPrefix(r, "+OK") {
		return fmt.Errorf("%w: PASS: %s %v", ErrAuthFailed, r, err)
	}
	_, _ = cmd("QUIT")
	return nil
}
