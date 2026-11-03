// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
)

// TokenSource returns a currently valid OAuth2 access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Credentials supplies the secrets needed to connect an account.
type Credentials interface {
	// Password returns the password of an account (domain.AuthPassword).
	Password(ctx context.Context, acc domain.Account) (string, error)
	// OAuthToken returns a token source for an account (domain.AuthOAuth2).
	OAuthToken(ctx context.Context, acc domain.Account) (TokenSource, error)
}

// OAuthProvider creates OAuth2 token sources (refreshing access tokens from
// the stored refresh token). It is a placeholder for the OAuth module.
type OAuthProvider interface {
	TokenSource(ctx context.Context, acc domain.Account, secrets domain.SecretStore) (TokenSource, error)
}

// SecretCredentials reads passwords from a SecretStore under the account's
// SecretRef and delegates OAuth to an optional OAuthProvider.
type SecretCredentials struct {
	Secrets domain.SecretStore
	OAuth   OAuthProvider
}

// Password implements Credentials.
func (c SecretCredentials) Password(_ context.Context, acc domain.Account) (string, error) {
	if c.Secrets == nil || acc.SecretRef == "" {
		return "", fmt.Errorf("%w %d", ErrNoCredentials, acc.ID)
	}
	b, err := c.Secrets.Get(acc.SecretRef)
	if err != nil {
		return "", fmt.Errorf("%w %d: %v", ErrNoCredentials, acc.ID, err)
	}
	return string(b), nil
}

// OAuthToken implements Credentials.
func (c SecretCredentials) OAuthToken(ctx context.Context, acc domain.Account) (TokenSource, error) {
	if c.OAuth == nil {
		return nil, fmt.Errorf("%w %d: OAuth2 is not configured", ErrNoCredentials, acc.ID)
	}
	return c.OAuth.TokenSource(ctx, acc, c.Secrets)
}

// ProviderOptions are connection settings shared by all providers.
type ProviderOptions struct {
	Logger *slog.Logger
	// TLSConfig is used for TLS connections (nil = system defaults).
	TLSConfig *tls.Config
	// DialContext overrides the TCP dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// PollInterval is the NOOP interval of watchers without IDLE.
	PollInterval time.Duration
	// DisableIdle forces polling.
	DisableIdle bool
	// WatchMinBackoff / WatchMaxBackoff bound the reconnect delay of watchers.
	WatchMinBackoff, WatchMaxBackoff time.Duration
}

// ProviderFactory builds a mail provider for an account.
type ProviderFactory func(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error)

// Registry maps account kinds to provider factories. IMAP is registered by
// DefaultRegistry; EAS, Graph, JMAP ... plug in through Register.
type Registry struct {
	mu stdsync.RWMutex
	m  map[domain.AccountKind]ProviderFactory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[domain.AccountKind]ProviderFactory{}} }

// DefaultRegistry returns a registry with the built-in providers.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(domain.KindIMAP, IMAPFactory)
	return r
}

// Register sets the factory of a kind (replacing an existing one).
func (r *Registry) Register(kind domain.AccountKind, f ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[kind] = f
}

// Supports reports whether a factory is registered for kind.
func (r *Registry) Supports(kind domain.AccountKind) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.m[kind]
	return ok
}

// Build creates the provider of an account.
func (r *Registry) Build(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	r.mu.RLock()
	f, ok := r.m[acc.Kind]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnsupportedKind, acc.Kind)
	}
	return f(ctx, acc, creds, opts)
}

// IMAPFactory builds an IMAP provider from the account's incoming server.
func IMAPFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	if acc.Incoming == nil {
		return nil, Permanent(fmt.Errorf("sync: account %d has no incoming server", acc.ID))
	}
	cfg := imapprov.Config{
		Server:       *acc.Incoming,
		TLSConfig:    opts.TLSConfig,
		DialContext:  opts.DialContext,
		Logger:       opts.Logger,
		PollInterval: opts.PollInterval,
		DisableIdle:  opts.DisableIdle,
		MinBackoff:   opts.WatchMinBackoff,
		MaxBackoff:   opts.WatchMaxBackoff,
	}
	if acc.Extra != nil {
		cfg.OAuthMechanism = acc.Extra["oauthMechanism"]
	}
	switch acc.Incoming.Auth {
	case domain.AuthOAuth2:
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return nil, err
		}
		cfg.TokenSource = imapprov.TokenSourceFunc(ts.Token)
	default:
		pw, err := creds.Password(ctx, acc)
		if err != nil {
			return nil, err
		}
		cfg.Password = pw
	}
	p, err := imapprov.New(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	return p, nil
}
