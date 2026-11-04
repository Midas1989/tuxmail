// SPDX-License-Identifier: AGPL-3.0-or-later

package pop3

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ErrServer is wrapped by every negative (-ERR) server response.
var ErrServer = errors.New("pop3: server error")

// maxLine bounds a single response line (RFC 1939 allows 512 octets; real
// servers send longer lines in message bodies).
const maxLine = 1 << 20

// conn is one POP3 session (RFC 1939). It is not safe for concurrent use.
type conn struct {
	nc   net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	caps map[string]string // upper-case capability -> arguments
	tls  bool
	// timeout bounds every command round trip.
	timeout time.Duration
}

// dialConfig are the connection parameters of a session.
type dialConfig struct {
	server  domain.ServerConfig
	tls     *tls.Config
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	timeout time.Duration
}

func dial(ctx context.Context, dc dialConfig) (*conn, error) {
	addr := net.JoinHostPort(dc.server.Host, strconv.Itoa(dc.server.Port))
	d := dc.dial
	if d == nil {
		nd := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		d = nd.DialContext
	}
	nc, err := d(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tcfg := tlsConfig(dc)
	isTLS := false
	if dc.server.TLS == domain.TLSImplicit {
		tc := tls.Client(nc, tcfg)
		if err := handshake(ctx, tc); err != nil {
			nc.Close()
			return nil, err
		}
		nc, isTLS = tc, true
	}
	c := newConn(nc, dc.timeout)
	c.tls = isTLS
	// Abort the greeting / STLS handshake when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := c.response(); err != nil {
		c.nc.Close()
		return nil, fmt.Errorf("pop3: greeting: %w", err)
	}
	_ = c.capa() // CAPA is optional (RFC 2449)
	if dc.server.TLS == domain.TLSStartTLS {
		if _, ok := c.caps["STLS"]; !ok && len(c.caps) > 0 {
			c.nc.Close()
			return nil, errors.New("pop3: server does not offer STLS")
		}
		if _, err := c.cmd("STLS"); err != nil {
			c.nc.Close()
			return nil, fmt.Errorf("pop3: STLS: %w", err)
		}
		tc := tls.Client(c.nc, tcfg)
		if err := handshake(ctx, tc); err != nil {
			c.nc.Close()
			return nil, err
		}
		c.nc, c.r, c.w, c.tls = tc, bufio.NewReader(tc), bufio.NewWriter(tc), true
		_ = c.capa() // capabilities may change after STLS (RFC 2595)
	}
	return c, nil
}

func tlsConfig(dc dialConfig) *tls.Config {
	var cfg *tls.Config
	if dc.tls != nil {
		cfg = dc.tls.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.ServerName == "" {
		cfg.ServerName = dc.server.Host
	}
	return cfg
}

func handshake(ctx context.Context, tc *tls.Conn) error {
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		return fmt.Errorf("pop3: TLS handshake: %w", err)
	}
	return nil
}

func newConn(nc net.Conn, timeout time.Duration) *conn {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &conn{nc: nc, r: bufio.NewReader(nc), w: bufio.NewWriter(nc), caps: map[string]string{}, timeout: timeout}
}

func (c *conn) close() error { return c.nc.Close() }

func (c *conn) readLine() (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return "", err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return "", errors.New("pop3: response line too long")
		}
		if !isPrefix {
			return string(buf), nil
		}
	}
}

// response reads a status line and returns the text after +OK.
func (c *conn) response() (string, error) {
	_ = c.nc.SetReadDeadline(time.Now().Add(c.timeout))
	line, err := c.readLine()
	if err != nil {
		return "", err
	}
	switch {
	case strings.HasPrefix(line, "+OK"):
		return strings.TrimSpace(strings.TrimPrefix(line, "+OK")), nil
	case strings.HasPrefix(line, "-ERR"):
		return "", fmt.Errorf("%w: %s", ErrServer, strings.TrimSpace(strings.TrimPrefix(line, "-ERR")))
	case strings.HasPrefix(line, "+"):
		// SASL continuation ("+ <base64>").
		return line, errContinue
	}
	return "", fmt.Errorf("pop3: unexpected response %q", truncate(line, 80))
}

var errContinue = errors.New("pop3: continuation")

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (c *conn) send(line string) error {
	_ = c.nc.SetWriteDeadline(time.Now().Add(c.timeout))
	if _, err := c.w.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return c.w.Flush()
}

// cmd sends a command and reads its single-line response.
func (c *conn) cmd(format string, args ...any) (string, error) {
	if err := c.send(fmt.Sprintf(format, args...)); err != nil {
		return "", err
	}
	return c.response()
}

// multi reads a dot-terminated multi-line body (with dot unstuffing). limit
// bounds the total size (0 = unlimited); the rest is drained to keep the
// session in sync.
func (c *conn) multi(limit int64) ([]byte, error) {
	var buf bytes.Buffer
	over := false
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(c.timeout))
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if line == "." {
			break
		}
		if strings.HasPrefix(line, ".") {
			line = line[1:]
		}
		if over {
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\r\n")
		if limit > 0 && int64(buf.Len()) > limit {
			over = true
		}
	}
	if over {
		return nil, fmt.Errorf("pop3: response larger than %d bytes", limit)
	}
	return buf.Bytes(), nil
}

// multiCmd sends a command with a multi-line answer.
func (c *conn) multiCmd(limit int64, format string, args ...any) ([]byte, error) {
	if _, err := c.cmd(format, args...); err != nil {
		return nil, err
	}
	return c.multi(limit)
}

// capa reads the capability list (RFC 2449).
func (c *conn) capa() error {
	body, err := c.multiCmd(64<<10, "CAPA")
	if err != nil {
		return err
	}
	c.caps = map[string]string{}
	for _, l := range strings.Split(string(body), "\r\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		name, args, _ := strings.Cut(l, " ")
		c.caps[strings.ToUpper(name)] = args
	}
	return nil
}

func (c *conn) hasCap(name string) bool {
	_, ok := c.caps[name]
	return ok
}

// saslMechs returns the SASL mechanisms (upper case) the server offers.
func (c *conn) saslMechs() []string {
	return strings.Fields(strings.ToUpper(c.caps["SASL"]))
}

func (c *conn) hasMech(m string) bool {
	for _, x := range c.saslMechs() {
		if x == m {
			return true
		}
	}
	return false
}

// userPass logs in with USER/PASS.
func (c *conn) userPass(user, pass string) error {
	if _, err := c.cmd("USER %s", user); err != nil {
		return fmt.Errorf("pop3: USER: %w", err)
	}
	if _, err := c.cmd("PASS %s", pass); err != nil {
		return fmt.Errorf("pop3: login failed: %w", err)
	}
	return nil
}

// authenticate runs a single-step SASL exchange with initial response
// (RFC 5034). On a continuation (error details of XOAUTH2) an empty line is
// sent and the final error returned.
func (c *conn) authenticate(mech string, ir []byte) error {
	enc := base64.StdEncoding.EncodeToString(ir)
	if len(ir) == 0 {
		enc = "="
	}
	_, err := c.cmd("AUTH %s %s", mech, enc)
	if errors.Is(err, errContinue) {
		if _, err2 := c.cmd(""); err2 != nil {
			return fmt.Errorf("pop3: AUTH %s: %w", mech, err2)
		}
		return fmt.Errorf("pop3: AUTH %s: unexpected challenge", mech)
	}
	if err != nil {
		return fmt.Errorf("pop3: AUTH %s: %w", mech, err)
	}
	return nil
}

func plainIR(user, pass string) []byte { return []byte("\x00" + user + "\x00" + pass) }

func xoauth2IR(user, token string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
}

// listing is one entry of LIST/UIDL.
type listing struct {
	num  int
	size int64
	uidl string
}

// uidlList returns the unique ids of all messages by message number.
func (c *conn) uidlList() (map[int]string, error) {
	body, err := c.multiCmd(0, "UIDL")
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, l := range strings.Split(string(body), "\r\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		out[n] = f[1]
	}
	return out, nil
}

// list returns the sizes of all messages by message number.
func (c *conn) list() (map[int]int64, error) {
	body, err := c.multiCmd(0, "LIST")
	if err != nil {
		return nil, err
	}
	out := map[int]int64{}
	for _, l := range strings.Split(string(body), "\r\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, err1 := strconv.Atoi(f[0])
		s, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[n] = s
	}
	return out, nil
}

// top returns the header (and n body lines) of a message.
func (c *conn) top(num, lines int) ([]byte, error) {
	return c.multiCmd(4<<20, "TOP %d %d", num, lines)
}

// retr downloads a message.
func (c *conn) retr(num int, limit int64) ([]byte, error) {
	return c.multiCmd(limit, "RETR %d", num)
}

func (c *conn) dele(num int) error {
	_, err := c.cmd("DELE %d", num)
	return err
}

func (c *conn) noop() error {
	_, err := c.cmd("NOOP")
	return err
}

// quit ends the session; deletions are committed (UPDATE state).
func (c *conn) quit() error {
	_, err := c.cmd("QUIT")
	c.nc.Close()
	return err
}

var _ io.Closer = (*conn)(nil)

// Close implements io.Closer.
func (c *conn) Close() error { return c.close() }
