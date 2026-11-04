// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imap implements domain.MailProvider on top of go-imap v2.
//
// Supported: implicit TLS / STARTTLS / plain connections, password
// (AUTHENTICATE PLAIN or LOGIN) and OAuth2 (XOAUTH2 / OAUTHBEARER)
// authentication, SPECIAL-USE folder roles with name-based fallback,
// incremental sync based on UIDVALIDITY/UIDNEXT plus CONDSTORE (or a flag
// diff over the known UID set), OBJECTID and Gmail X-GM-MSGID/X-GM-THRID,
// MOVE with UIDPLUS mapping and IDLE-based watching with NOOP fallback.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// TokenSource returns a currently valid OAuth2 access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token implements TokenSource.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// Config configures a Provider.
type Config struct {
	// Server is the incoming server. Server.Auth selects the login method.
	Server domain.ServerConfig
	// Password for domain.AuthPassword.
	Password string
	// TokenSource for domain.AuthOAuth2.
	TokenSource TokenSource
	// OAuthMechanism forces "XOAUTH2" or "OAUTHBEARER"; empty selects
	// automatically from the server capabilities (XOAUTH2 preferred).
	OAuthMechanism string
	// TLSConfig is cloned per connection; ServerName defaults to Server.Host.
	TLSConfig *tls.Config
	// DialContext overrides the TCP dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// Logger receives diagnostics; nil discards them.
	Logger *slog.Logger
	// PollInterval is the NOOP interval used by Watch when the server lacks
	// IDLE. Default 2 minutes.
	PollInterval time.Duration
	// DisableIdle makes Watch poll with NOOP even if IDLE is advertised
	// (for servers with broken IDLE implementations).
	DisableIdle bool
	// MinBackoff / MaxBackoff bound the reconnect delay of Watch.
	// Defaults 1 s / 5 min.
	MinBackoff, MaxBackoff time.Duration
	// FetchBatchSize is the number of UIDs per header FETCH. Default 250.
	FetchBatchSize int
	// FirstSyncChunk and SyncChunk bound the number of new messages one
	// Sync call returns (newest first; SyncDelta.More reports the rest):
	// FirstSyncChunk for a folder without known messages (fast first rows),
	// SyncChunk afterwards. Defaults 50 / 500.
	FirstSyncChunk, SyncChunk int
	// DebugWriter receives the raw protocol trace (contains credentials!).
	DebugWriter io.Writer
}

// ErrMessageNotFound is returned when a referenced message no longer exists.
var ErrMessageNotFound = errors.New("imap: message not found")

// Provider is an IMAP implementation of domain.MailProvider.
//
// All methods except Watch share one connection and are serialized by a
// mutex. Watch opens a dedicated connection per call.
type Provider struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	c        *imapclient.Client
	caps     imap.CapSet
	delim    rune
	lastUsed time.Time
	ids      *idConn
	closed   bool
}

var _ domain.MailProvider = (*Provider)(nil)

// New validates cfg. No connection is made until the first call (or Connect).
func New(cfg Config) (*Provider, error) {
	if cfg.Server.Host == "" {
		return nil, errors.New("imap: missing host")
	}
	if cfg.Server.TLS == "" {
		cfg.Server.TLS = domain.TLSImplicit
	}
	if cfg.Server.Port == 0 {
		if cfg.Server.TLS == domain.TLSImplicit {
			cfg.Server.Port = 993
		} else {
			cfg.Server.Port = 143
		}
	}
	if cfg.Server.Auth == domain.AuthOAuth2 && cfg.TokenSource == nil {
		return nil, errors.New("imap: OAuth2 authentication requires a token source")
	}
	switch strings.ToUpper(cfg.OAuthMechanism) {
	case "", "XOAUTH2", "OAUTHBEARER":
		cfg.OAuthMechanism = strings.ToUpper(cfg.OAuthMechanism)
	default:
		return nil, fmt.Errorf("imap: unsupported OAuth mechanism %q", cfg.OAuthMechanism)
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Minute
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.FetchBatchSize <= 0 {
		cfg.FetchBatchSize = 250
	}
	if cfg.FirstSyncChunk <= 0 {
		cfg.FirstSyncChunk = 50
	}
	if cfg.SyncChunk <= 0 {
		cfg.SyncChunk = 500
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Provider{cfg: cfg, log: log}, nil
}

// Connect establishes and authenticates the shared connection. Calling it is
// optional; it is useful to verify credentials and to populate Capabilities.
func (p *Provider) Connect(ctx context.Context) error {
	return p.do(ctx, func(*imapclient.Client) error { return nil })
}

// Capabilities reports the capabilities of the last established connection
// (zero value before the first successful connection).
func (p *Provider) Capabilities() domain.MailCaps {
	p.mu.Lock()
	caps := p.caps
	p.mu.Unlock()
	return mailCaps(caps)
}

func mailCaps(caps imap.CapSet) domain.MailCaps {
	if caps == nil {
		return domain.MailCaps{}
	}
	mc := domain.MailCaps{
		Move:      caps.Has(imap.CapMove),
		Idle:      caps.Has(imap.CapIdle),
		ObjectID:  caps.Has(imap.CapObjectID) || caps.Has(capGmail),
		CondStore: caps.Has(imap.CapCondStore),
	}
	for c := range caps {
		if strings.HasPrefix(strings.ToUpper(string(c)), "THREAD=") {
			mc.Thread = true
		}
	}
	return mc
}

const capGmail imap.Cap = "X-GM-EXT-1"

func (p *Provider) addr() string {
	return net.JoinHostPort(p.cfg.Server.Host, strconv.Itoa(p.cfg.Server.Port))
}

func (p *Provider) tlsConfig() *tls.Config {
	var cfg *tls.Config
	if p.cfg.TLSConfig != nil {
		cfg = p.cfg.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.ServerName == "" {
		cfg.ServerName = p.cfg.Server.Host
	}
	return cfg
}

// dialRaw opens the TCP connection and, for implicit TLS, completes the
// handshake. STARTTLS is left to the caller.
func (p *Provider) dialRaw(ctx context.Context) (net.Conn, error) {
	dial := p.cfg.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	conn, err := dial(ctx, "tcp", p.addr())
	if err != nil {
		return nil, fmt.Errorf("imap: dial %s: %w", p.addr(), err)
	}
	if p.cfg.Server.TLS == domain.TLSImplicit {
		tc := tls.Client(conn, p.tlsConfig())
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap: TLS handshake: %w", err)
		}
		return tc, nil
	}
	return conn, nil
}

// dial returns a connected, authenticated client.
func (p *Provider) dial(ctx context.Context, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	conn, err := p.dialRaw(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	opts := &imapclient.Options{
		TLSConfig:             p.tlsConfig(),
		UnilateralDataHandler: handler,
		DebugWriter:           p.cfg.DebugWriter,
	}
	var c *imapclient.Client
	switch p.cfg.Server.TLS {
	case domain.TLSStartTLS:
		c, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			return nil, fmt.Errorf("imap: STARTTLS: %w", ctxErr(ctx, err))
		}
	case domain.TLSImplicit, domain.TLSNone:
		c = imapclient.New(conn, opts)
	default:
		conn.Close()
		return nil, fmt.Errorf("imap: unknown TLS mode %q", p.cfg.Server.TLS)
	}
	if err := c.WaitGreeting(); err != nil {
		c.Close()
		return nil, fmt.Errorf("imap: greeting: %w", ctxErr(ctx, err))
	}
	if c.State() == imap.ConnStateNotAuthenticated {
		if err := p.authenticate(ctx, c); err != nil {
			c.Close()
			return nil, ctxErr(ctx, err)
		}
	}
	if ctx.Err() != nil {
		c.Close()
		return nil, ctx.Err()
	}
	return c, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func isClosed(c *imapclient.Client) bool {
	select {
	case <-c.Closed():
		return true
	default:
		return false
	}
}

// do runs fn with the shared connection. A cancelled ctx closes the
// connection (go-imap has no per-command cancellation); it is re-established
// on the next call.
func (p *Provider) do(ctx context.Context, fn func(c *imapclient.Client) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	if p.c != nil && (isClosed(p.c) || time.Since(p.lastUsed) > time.Minute) {
		// Verify long-idle connections before running possibly
		// non-idempotent commands on them.
		c := p.c
		stop := context.AfterFunc(ctx, func() { c.Close() })
		if isClosed(c) || c.Noop().Wait() != nil {
			p.dropLocked()
		}
		if !stop() {
			p.dropLocked()
			return ctx.Err()
		}
	}
	if p.c == nil {
		c, err := p.dial(ctx, nil)
		if err != nil {
			return err
		}
		p.c, p.caps, p.delim = c, c.Caps(), 0
		p.lastUsed = time.Now()
	}
	c := p.c
	stop := context.AfterFunc(ctx, func() { c.Close() })
	err := fn(c)
	if !stop() {
		p.dropLocked()
		return ctx.Err()
	}
	p.lastUsed = time.Now()
	if isClosed(c) {
		p.dropLocked()
	}
	return err
}

func (p *Provider) dropLocked() {
	if p.c != nil {
		p.c.Close()
		p.c = nil
	}
}

// Close logs out and closes all connections. Running Watch loops are not
// affected; they stop when their context ends.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.ids != nil {
		p.ids.close()
		p.ids = nil
	}
	if p.c == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		_ = p.c.Logout().Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	err := p.c.Close()
	p.c = nil
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// selectMailbox selects name read-write unless it is already selected.
func selectMailbox(c *imapclient.Client, name string, condstore bool) (*imap.SelectData, error) {
	data, err := c.Select(name, &imap.SelectOptions{CondStore: condstore}).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: select %q: %w", name, err)
	}
	return data, nil
}

func ensureSelected(c *imapclient.Client, name string) error {
	if mb := c.Mailbox(); mb != nil && mb.Name == name {
		return nil
	}
	_, err := selectMailbox(c, name, false)
	return err
}
