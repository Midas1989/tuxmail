// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smtp implements domain.Transport for SMTP submission servers
// (implicit TLS on 465, STARTTLS on 587, or plain text after explicit user
// consent) with PLAIN, LOGIN, XOAUTH2 and OAUTHBEARER authentication.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// TokenSource returns a currently valid OAuth2 access token.
// Implementations are expected to refresh tokens on their own.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to the TokenSource interface.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token implements TokenSource.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// Config configures a Transport.
type Config struct {
	// Server is the outgoing server. Server.Auth selects password or OAuth2.
	Server domain.ServerConfig
	// Password is used for domain.AuthPassword.
	Password string
	// TokenSource is used for domain.AuthOAuth2.
	TokenSource TokenSource
	// TLSConfig is cloned for every connection. ServerName defaults to Server.Host.
	TLSConfig *tls.Config
	// LocalName is the name sent with EHLO. Defaults to "localhost".
	LocalName string
	// DialContext overrides the TCP dialer (tests, proxies).
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout bounds a single SMTP session step when ctx has no deadline.
	// Defaults to 2 minutes.
	Timeout time.Duration
}

// ErrMessageTooLarge is returned when a message exceeds the server's SIZE
// limit (RFC 1870), either detected up front from the EHLO response or from a
// 552 / 5.3.4 rejection. The core uses Limit to suggest cloud links instead.
type ErrMessageTooLarge struct {
	// Size is the size of the rejected message in bytes.
	Size int64
	// Limit is the server limit in bytes, 0 if the server did not announce one.
	Limit int64
}

func (e *ErrMessageTooLarge) Error() string {
	if e.Limit > 0 {
		return fmt.Sprintf("smtp: message too large (%d bytes, server limit %d bytes)", e.Size, e.Limit)
	}
	return fmt.Sprintf("smtp: message too large (%d bytes)", e.Size)
}

// Transport is an SMTP implementation of domain.Transport. It keeps one
// connection open between sends and transparently reconnects. It is safe for
// concurrent use; sends are serialized.
type Transport struct {
	cfg Config

	mu     sync.Mutex
	client *gosmtp.Client
	conn   net.Conn
	closed bool
}

var _ domain.Transport = (*Transport)(nil)

// New validates cfg and returns a Transport. No connection is made until the
// first Send (use Check to verify settings eagerly).
func New(cfg Config) (*Transport, error) {
	if cfg.Server.Host == "" {
		return nil, errors.New("smtp: missing host")
	}
	if cfg.Server.Port == 0 {
		switch cfg.Server.TLS {
		case domain.TLSImplicit:
			cfg.Server.Port = 465
		default:
			cfg.Server.Port = 587
		}
	}
	if cfg.Server.TLS == "" {
		cfg.Server.TLS = domain.TLSImplicit
	}
	if cfg.Server.Auth == domain.AuthOAuth2 && cfg.TokenSource == nil {
		return nil, errors.New("smtp: OAuth2 authentication requires a token source")
	}
	if cfg.LocalName == "" {
		cfg.LocalName = "localhost"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	return &Transport{cfg: cfg}, nil
}

// Check connects and authenticates without sending a message.
func (t *Transport) Check(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return net.ErrClosed
	}
	t.dropLocked()
	return t.withConn(ctx, func(c *gosmtp.Client) error { return nil })
}

// MaxMessageSize connects if necessary and returns the server SIZE limit
// (0, false if unknown or unlimited).
func (t *Transport) MaxMessageSize(ctx context.Context) (int64, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var (
		limit int
		ok    bool
	)
	err := t.withConn(ctx, func(c *gosmtp.Client) error {
		limit, ok = c.MaxMessageSize()
		return nil
	})
	return int64(limit), ok && limit > 0, err
}

// Send submits msg. If the cached connection turns out to be dead, Send
// reconnects once.
func (t *Transport) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	if len(msg.Recipients) == 0 {
		return errors.New("smtp: no recipients")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return net.ErrClosed
	}

	if t.client != nil {
		// Probe the cached connection; servers drop idle sessions.
		err := t.withConn(ctx, func(c *gosmtp.Client) error { return c.Reset() })
		if err != nil {
			t.dropLocked()
		}
	}
	return t.withConn(ctx, func(c *gosmtp.Client) error { return t.sendOn(c, msg) })
}

func (t *Transport) sendOn(c *gosmtp.Client, msg domain.OutgoingMessage) error {
	size := int64(len(msg.Raw))
	limit, hasLimit := c.MaxMessageSize()
	if hasLimit && limit > 0 && size > int64(limit) {
		return &ErrMessageTooLarge{Size: size, Limit: int64(limit)}
	}

	opts := &gosmtp.MailOptions{Size: size}
	if needsSMTPUTF8(msg) {
		if ok, _ := c.Extension("SMTPUTF8"); ok {
			opts.UTF8 = true
		}
	}
	if err := c.Mail(msg.From.Email, opts); err != nil {
		return t.mapErr(c, err, size)
	}
	for _, rcpt := range msg.Recipients {
		if err := c.Rcpt(rcpt, nil); err != nil {
			_ = c.Reset()
			return fmt.Errorf("smtp: recipient %q rejected: %w", rcpt, t.mapErr(c, err, size))
		}
	}
	w, err := c.Data()
	if err != nil {
		_ = c.Reset()
		return t.mapErr(c, err, size)
	}
	if _, err := w.Write(msg.Raw); err != nil {
		_ = w.Close()
		return t.mapErr(c, err, size)
	}
	if err := w.Close(); err != nil {
		return t.mapErr(c, err, size)
	}
	return nil
}

func (t *Transport) mapErr(c *gosmtp.Client, err error, size int64) error {
	var se *gosmtp.SMTPError
	if errors.As(err, &se) {
		if se.Code == 552 || se.EnhancedCode == (gosmtp.EnhancedCode{5, 3, 4}) || se.EnhancedCode == (gosmtp.EnhancedCode{5, 2, 3}) {
			limit, _ := c.MaxMessageSize()
			return &ErrMessageTooLarge{Size: size, Limit: int64(limit)}
		}
	}
	return err
}

func needsSMTPUTF8(msg domain.OutgoingMessage) bool {
	if !isASCII(msg.From.Email) {
		return true
	}
	for _, r := range msg.Recipients {
		if !isASCII(r) {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// withConn runs fn on a connected, authenticated client while honouring ctx:
// if ctx is cancelled mid-command the connection is closed to unblock I/O.
// On any network-level failure the connection is dropped. t.mu must be held.
func (t *Transport) withConn(ctx context.Context, fn func(c *gosmtp.Client) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.client == nil {
		if err := t.connectLocked(ctx); err != nil {
			return err
		}
	}
	conn := t.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	err := fn(t.client)
	if !stop() {
		t.dropLocked()
		return ctx.Err()
	}
	if err != nil && isConnErr(err) {
		t.dropLocked()
	}
	return err
}

func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	var se *gosmtp.SMTPError
	if errors.As(err, &se) {
		return se.Code == 421
	}
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.As(err, &ne) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func (t *Transport) tlsConfig() *tls.Config {
	var cfg *tls.Config
	if t.cfg.TLSConfig != nil {
		cfg = t.cfg.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.ServerName == "" {
		cfg.ServerName = t.cfg.Server.Host
	}
	return cfg
}

func (t *Transport) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(t.cfg.Server.Host, strconv.Itoa(t.cfg.Server.Port))
	dial := t.cfg.DialContext
	if dial == nil {
		d := netguard.Dialer(&net.Dialer{Timeout: 30 * time.Second})
		dial = d.DialContext
	}
	return dial(ctx, "tcp", addr)
}

func (t *Transport) connectLocked(ctx context.Context) error {
	raw, err := t.dial(ctx)
	if err != nil {
		return fmt.Errorf("smtp: dial: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()

	var c *gosmtp.Client
	switch t.cfg.Server.TLS {
	case domain.TLSImplicit:
		tc := tls.Client(raw, t.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return fmt.Errorf("smtp: TLS handshake: %w", err)
		}
		c = gosmtp.NewClient(tc)
	case domain.TLSStartTLS:
		c, err = gosmtp.NewClientStartTLS(raw, t.tlsConfig())
		if err != nil {
			raw.Close()
			return fmt.Errorf("smtp: STARTTLS: %w", ctxErr(ctx, err))
		}
	case domain.TLSNone:
		c = gosmtp.NewClient(raw)
	default:
		raw.Close()
		return fmt.Errorf("smtp: unknown TLS mode %q", t.cfg.Server.TLS)
	}
	c.CommandTimeout = t.cfg.Timeout
	if err := c.Hello(t.cfg.LocalName); err != nil {
		c.Close()
		return fmt.Errorf("smtp: EHLO: %w", ctxErr(ctx, err))
	}
	if err := t.auth(ctx, c); err != nil {
		c.Close()
		return ctxErr(ctx, err)
	}
	if ctx.Err() != nil {
		c.Close()
		return ctx.Err()
	}
	t.client, t.conn = c, raw
	return nil
}

// ctxErr prefers the context error when the failure was caused by cancellation.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (t *Transport) auth(ctx context.Context, c *gosmtp.Client) error {
	username := t.cfg.Server.Username
	ok, offered := c.Extension("AUTH")
	switch t.cfg.Server.Auth {
	case domain.AuthOAuth2:
		token, err := t.cfg.TokenSource.Token(ctx)
		if err != nil {
			return fmt.Errorf("smtp: obtain OAuth2 token: %w", err)
		}
		var mech sasl.Client
		switch {
		case !ok:
			return errNoAuthMechanism("OAuth2", "")
		case c.SupportsAuth("XOAUTH2"):
			mech = newXOAuth2Client(username, token)
		case c.SupportsAuth("OAUTHBEARER"):
			mech = sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
				Username: username, Token: token, Host: t.cfg.Server.Host, Port: t.cfg.Server.Port,
			})
		default:
			return errNoAuthMechanism("OAuth2", offered)
		}
		if err := c.Auth(mech); err != nil {
			return fmt.Errorf("smtp: authenticate: %w", err)
		}
	case domain.AuthPassword, "":
		if username == "" && t.cfg.Password == "" {
			return nil // anonymous relay (e.g. local MTA)
		}
		if !ok {
			return errNoAuthMechanism("password", "")
		}
		var mech sasl.Client
		switch {
		case c.SupportsAuth("PLAIN"):
			mech = sasl.NewPlainClient("", username, t.cfg.Password)
		case c.SupportsAuth("LOGIN"):
			mech = newLoginClient(username, t.cfg.Password)
		default:
			return errNoAuthMechanism("password", offered)
		}
		if err := c.Auth(mech); err != nil {
			return fmt.Errorf("smtp: authenticate: %w", err)
		}
	case domain.AuthClientCert:
		// The certificate is presented during the TLS handshake via TLSConfig;
		// try SASL EXTERNAL when offered, otherwise rely on the TLS session.
		if ok && strings.Contains(strings.ToUpper(offered), "EXTERNAL") {
			if err := c.Auth(sasl.NewExternalClient("")); err != nil {
				return fmt.Errorf("smtp: authenticate: %w", err)
			}
		}
	default:
		return fmt.Errorf("smtp: unsupported auth method %q", t.cfg.Server.Auth)
	}
	return nil
}

func (t *Transport) dropLocked() {
	if t.client != nil {
		_ = t.client.Close()
	}
	t.client, t.conn = nil, nil
}

// Close sends QUIT (best effort) and closes the connection.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.client == nil {
		return nil
	}
	_ = t.conn.SetDeadline(time.Now().Add(5 * time.Second))
	err := t.client.Quit()
	t.dropLocked()
	if err != nil && !isConnErr(err) {
		return err
	}
	return nil
}
