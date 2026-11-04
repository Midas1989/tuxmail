// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"errors"
	"fmt"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/ews"
	"github.com/Midas1989/Mailclient/internal/providers/gmail"
	"github.com/Midas1989/Mailclient/internal/providers/graph"
	"github.com/Midas1989/Mailclient/internal/providers/smtp"
)

// Transports of the API-based accounts (Microsoft Graph sendMail, Gmail
// messages.send, EWS CreateItem). All of them store the message in Sent
// themselves (see providerSavesSent).

// TokenFunc returns the OAuth2 token source of an account (the resource
// follows the account kind, see oauth.ResourceForKind).
type TokenFunc func(ctx context.Context, account domain.Account) (smtp.TokenSource, error)

// KindRouter dispatches to a factory per account kind and falls back to
// Default (normally SMTPFactory).
type KindRouter struct {
	Default TransportFactory
	Kinds   map[domain.AccountKind]TransportFactory
}

// Transport implements TransportFactory.
func (r KindRouter) Transport(ctx context.Context, account domain.Account) (domain.Transport, error) {
	if f, ok := r.Kinds[account.Kind]; ok && f != nil {
		return f.Transport(ctx, account)
	}
	if r.Default == nil {
		return nil, fmt.Errorf("%w (account %d)", ErrNoOutgoingServer, account.ID)
	}
	return r.Default.Transport(ctx, account)
}

// CloudFactories returns the factories of graph, gmail and ews accounts.
func CloudFactories(tokens TokenFunc, secrets domain.SecretStore) map[domain.AccountKind]TransportFactory {
	return map[domain.AccountKind]TransportFactory{
		domain.KindGraph: GraphFactory{Tokens: tokens},
		domain.KindGmail: GmailFactory{Tokens: tokens},
		domain.KindEWS:   EWSFactory{Tokens: tokens, Secrets: secrets},
	}
}

var errNoTokens = errors.New("send: OAuth2 is not configured")

// GraphFactory builds Graph sendMail transports.
type GraphFactory struct{ Tokens TokenFunc }

// Transport implements TransportFactory.
func (f GraphFactory) Transport(ctx context.Context, a domain.Account) (domain.Transport, error) {
	if f.Tokens == nil {
		return nil, errNoTokens
	}
	ts, err := f.Tokens(ctx, a)
	if err != nil {
		return nil, err
	}
	return graph.NewTransport(graph.Config{BaseURL: a.Extra["graphBaseUrl"], Tokens: ts, Mailbox: a.Extra["mailbox"],
		SendMode: a.Extra["sendMode"], Self: a.Extra["address"]})
}

// GmailFactory builds Gmail messages.send transports.
type GmailFactory struct{ Tokens TokenFunc }

// Transport implements TransportFactory.
func (f GmailFactory) Transport(ctx context.Context, a domain.Account) (domain.Transport, error) {
	if f.Tokens == nil {
		return nil, errNoTokens
	}
	ts, err := f.Tokens(ctx, a)
	if err != nil {
		return nil, err
	}
	return gmail.New(gmail.Config{BaseURL: a.Extra["gmailBaseUrl"], Tokens: ts})
}

// EWSFactory builds EWS transports (password or OAuth2).
type EWSFactory struct {
	Tokens  TokenFunc
	Secrets domain.SecretStore
}

// Transport implements TransportFactory.
func (f EWSFactory) Transport(ctx context.Context, a domain.Account) (domain.Transport, error) {
	cfg := ews.Config{URL: a.Extra["ewsUrl"], Auth: a.Extra["ewsAuth"], Mailbox: a.Extra["mailbox"]}
	if cfg.URL == "" && a.Incoming != nil && a.Incoming.Host != "" {
		cfg.URL = "https://" + a.Incoming.Host + "/EWS/Exchange.asmx"
	}
	if a.Incoming != nil {
		cfg.Username = a.Incoming.Username
	}
	if cfg.Auth == ews.AuthBearer || (a.Incoming != nil && a.Incoming.Auth == domain.AuthOAuth2) {
		if f.Tokens == nil {
			return nil, errNoTokens
		}
		ts, err := f.Tokens(ctx, a)
		if err != nil {
			return nil, err
		}
		cfg.Auth, cfg.Tokens = ews.AuthBearer, ts
	} else if f.Secrets != nil && a.SecretRef != "" {
		pw, err := f.Secrets.Get(a.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("send: read EWS password: %w", err)
		}
		cfg.Password = string(pw)
	}
	return ews.New(cfg)
}
