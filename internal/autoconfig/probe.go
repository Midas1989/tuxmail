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

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// ProbeConnect verifies that cfg is reachable: it connects, performs the TLS
// handshake (implicit or after STARTTLS/STLS) and checks the protocol
// greeting. kind selects the protocol (domain.KindIMAP, domain.KindPOP3 or
// domain.KindSMTP). It does not authenticate.
func (d *Discoverer) ProbeConnect(ctx context.Context, kind domain.AccountKind, cfg domain.ServerConfig) error {
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

	wrap := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}

	switch cfg.TLS {
	case domain.TLSImplicit:
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return wrap(fmt.Errorf("autoconfig: TLS handshake with %s: %w", cfg.Host, err))
		}
		return wrap(readGreeting(bufio.NewReader(tc), kind))
	case domain.TLSStartTLS:
		br := bufio.NewReader(conn)
		if err := readGreeting(br, kind); err != nil {
			return wrap(err)
		}
		if err := startTLS(conn, br, kind); err != nil {
			return wrap(err)
		}
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return wrap(fmt.Errorf("autoconfig: TLS handshake with %s: %w", cfg.Host, err))
		}
		return nil
	case domain.TLSNone:
		return wrap(readGreeting(bufio.NewReader(conn), kind))
	}
	return fmt.Errorf("autoconfig: unknown TLS mode %q", cfg.TLS)
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > 4096 {
		return "", errors.New("autoconfig: line too long")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readSMTPReply reads a (possibly multi-line) SMTP reply and returns its code.
func readSMTPReply(br *bufio.Reader) (int, error) {
	for i := 0; i < 100; i++ {
		line, err := readLine(br)
		if err != nil {
			return 0, err
		}
		if len(line) < 3 {
			return 0, fmt.Errorf("autoconfig: malformed SMTP reply %q", line)
		}
		code, err := strconv.Atoi(line[:3])
		if err != nil {
			return 0, fmt.Errorf("autoconfig: malformed SMTP reply %q", line)
		}
		if len(line) == 3 || line[3] == ' ' {
			return code, nil
		}
	}
	return 0, errors.New("autoconfig: SMTP reply too long")
}

func readGreeting(br *bufio.Reader, kind domain.AccountKind) error {
	switch kind {
	case domain.KindSMTP:
		code, err := readSMTPReply(br)
		if err != nil {
			return err
		}
		if code != 220 {
			return fmt.Errorf("autoconfig: unexpected SMTP greeting %d", code)
		}
	case domain.KindPOP3:
		line, err := readLine(br)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "+OK") {
			return fmt.Errorf("autoconfig: unexpected POP3 greeting %q", line)
		}
	default:
		line, err := readLine(br)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "* OK") && !strings.HasPrefix(line, "* PREAUTH") {
			return fmt.Errorf("autoconfig: unexpected IMAP greeting %q", line)
		}
	}
	return nil
}

func startTLS(conn net.Conn, br *bufio.Reader, kind domain.AccountKind) error {
	switch kind {
	case domain.KindSMTP:
		if _, err := conn.Write([]byte("EHLO localhost\r\n")); err != nil {
			return err
		}
		if code, err := readSMTPReply(br); err != nil || code != 250 {
			return fmt.Errorf("autoconfig: EHLO failed (%d): %v", code, err)
		}
		if _, err := conn.Write([]byte("STARTTLS\r\n")); err != nil {
			return err
		}
		if code, err := readSMTPReply(br); err != nil || code != 220 {
			return fmt.Errorf("autoconfig: STARTTLS refused (%d): %v", code, err)
		}
	case domain.KindPOP3:
		if _, err := conn.Write([]byte("STLS\r\n")); err != nil {
			return err
		}
		line, err := readLine(br)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "+OK") {
			return fmt.Errorf("autoconfig: STLS refused: %q", line)
		}
	default:
		if _, err := conn.Write([]byte("P1 STARTTLS\r\n")); err != nil {
			return err
		}
		var line string
		for i := 0; !strings.HasPrefix(line, "P1 "); i++ {
			if i == 100 {
				return errors.New("autoconfig: no STARTTLS response")
			}
			var err error
			if line, err = readLine(br); err != nil {
				return err
			}
		}
		if !strings.HasPrefix(strings.ToUpper(line[3:]), "OK") {
			return fmt.Errorf("autoconfig: STARTTLS refused: %q", line)
		}
	}
	if br.Buffered() > 0 {
		return errors.New("autoconfig: unexpected data before TLS handshake")
	}
	return nil
}
