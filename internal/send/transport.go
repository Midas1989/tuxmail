// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/smtp"
)

// ErrNoOutgoingServer is returned for accounts without outgoing server.
var ErrNoOutgoingServer = errors.New("send: account has no outgoing server")

// SMTPFactory is the default TransportFactory: it builds an SMTP transport
// from the account's outgoing server and the secret store.
type SMTPFactory struct {
	Secrets domain.SecretStore
	// TokenSource returns the OAuth2 token source of an account (required
	// for accounts with domain.AuthOAuth2).
	TokenSource func(ctx context.Context, account domain.Account) (smtp.TokenSource, error)
	TLSConfig   *tls.Config
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Timeout     time.Duration
	LocalName   string
}

// ExtraSMTPSecretRef is the account extra holding a dedicated secret
// reference for the outgoing server (default: Account.SecretRef).
const ExtraSMTPSecretRef = "smtp_secret_ref"

// Transport implements TransportFactory.
func (f SMTPFactory) Transport(ctx context.Context, account domain.Account) (domain.Transport, error) {
	if account.Outgoing == nil || account.Outgoing.Host == "" {
		return nil, fmt.Errorf("%w (account %d)", ErrNoOutgoingServer, account.ID)
	}
	cfg := smtp.Config{
		Server: *account.Outgoing, TLSConfig: f.TLSConfig, DialContext: f.DialContext,
		Timeout: f.Timeout, LocalName: f.LocalName,
	}
	switch account.Outgoing.Auth {
	case domain.AuthOAuth2:
		if f.TokenSource == nil {
			return nil, errors.New("send: OAuth2 account without token source")
		}
		ts, err := f.TokenSource(ctx, account)
		if err != nil {
			return nil, err
		}
		cfg.TokenSource = ts
	case domain.AuthPassword, "":
		ref := account.SecretRef
		if r := account.Extra[ExtraSMTPSecretRef]; r != "" {
			ref = r
		}
		if ref != "" && f.Secrets != nil && account.Outgoing.Username != "" {
			pw, err := f.Secrets.Get(ref)
			if err != nil {
				return nil, fmt.Errorf("send: read SMTP password: %w", err)
			}
			cfg.Password = string(pw)
		}
	}
	return smtp.New(cfg)
}
