// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/providers/ews"
	"github.com/Midas1989/Mailclient/internal/providers/gmail"
	"github.com/Midas1989/Mailclient/internal/providers/graph"
)

// Registration of the Microsoft Graph, Gmail API and EWS providers
// (DefaultRegistry) and the OAuth adapter.

// builtinFactories are added to DefaultRegistry (providers register in
// their *_register.go files through init).
var builtinFactories = map[domain.AccountKind]ProviderFactory{}

func init() {
	builtinFactories[domain.KindGraph] = GraphFactory
	builtinFactories[domain.KindGmail] = GmailFactory
	builtinFactories[domain.KindEWS] = EWSFactory
}

// Account extras of cloud accounts.
const (
	ExtraGraphBaseURL = "graphBaseUrl" // tests / national clouds
	ExtraGmailBaseURL = "gmailBaseUrl"
	ExtraMailbox      = "mailbox"  // shared/delegated mailbox address
	ExtraSendMode     = "sendMode" // send_as | send_on_behalf
	ExtraAddress      = "address"  // own address (set by the account wizard)
	ExtraEWSURL       = "ewsUrl"
	ExtraEWSAuth      = "ewsAuth" // ntlm | basic | oauth
)

// GraphNotifications returns the change-notification hub for an account
// (server mode with a public URL); nil = polling.
var GraphNotifications func(acc domain.Account) *graph.Notifier

// OAuthAdapter implements OAuthProvider on *oauth.Manager: the token
// resource follows the account kind (Graph for graph accounts, Exchange
// Online for IMAP/SMTP/EAS/EWS, Google for Google accounts).
type OAuthAdapter struct{ M *oauth.Manager }

// TokenSource implements OAuthProvider.
func (a OAuthAdapter) TokenSource(_ context.Context, acc domain.Account, _ domain.SecretStore) (TokenSource, error) {
	if a.M == nil {
		return nil, fmt.Errorf("%w %d: OAuth2 is not configured", ErrNoCredentials, acc.ID)
	}
	ts, err := a.M.ForAccount(acc)
	if err != nil {
		return nil, Permanent(err)
	}
	return ts, nil
}

func pollInterval(o ProviderOptions) time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}
	return time.Minute
}

// GraphFactory builds a Microsoft Graph provider (OAuth2 only).
func GraphFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	ts, err := creds.OAuthToken(ctx, acc)
	if err != nil {
		return nil, err
	}
	cfg := graph.Config{BaseURL: acc.Extra[ExtraGraphBaseURL], Tokens: ts, Mailbox: acc.Extra[ExtraMailbox],
		Self: acc.Extra[ExtraAddress], SendMode: acc.Extra[ExtraSendMode], PollInterval: pollInterval(opts), Logger: opts.Logger}
	if GraphNotifications != nil {
		cfg.Notifications = GraphNotifications(acc)
	}
	p, err := graph.New(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	return p, nil
}

// GmailFactory builds a Gmail API provider (OAuth2 only).
func GmailFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	ts, err := creds.OAuthToken(ctx, acc)
	if err != nil {
		return nil, err
	}
	p, err := gmail.New(gmail.Config{BaseURL: acc.Extra[ExtraGmailBaseURL], Tokens: ts, PollInterval: pollInterval(opts), Logger: opts.Logger})
	if err != nil {
		return nil, Permanent(err)
	}
	return p, nil
}

// EWSConfig builds the EWS configuration of an account.
func EWSConfig(ctx context.Context, acc domain.Account, creds Credentials) (ews.Config, error) {
	cfg := ews.Config{URL: acc.Extra[ExtraEWSURL], Auth: acc.Extra[ExtraEWSAuth], Mailbox: acc.Extra[ExtraMailbox]}
	if cfg.URL == "" && acc.Incoming != nil && acc.Incoming.Host != "" {
		cfg.URL = "https://" + acc.Incoming.Host + "/EWS/Exchange.asmx"
	}
	if cfg.URL == "" {
		return cfg, Permanent(fmt.Errorf("sync: EWS account %d has no endpoint URL", acc.ID))
	}
	if acc.Incoming != nil {
		cfg.Username = acc.Incoming.Username
	}
	if cfg.Auth == ews.AuthBearer || (acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2) {
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return cfg, err
		}
		cfg.Auth, cfg.Tokens = ews.AuthBearer, ts
		return cfg, nil
	}
	pw, err := creds.Password(ctx, acc)
	if err != nil {
		return cfg, err
	}
	cfg.Password = pw
	return cfg, nil
}

// EWSFactory builds an EWS provider (NTLM/Basic with password or OAuth2).
func EWSFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	cfg, err := EWSConfig(ctx, acc, creds)
	if err != nil {
		return nil, err
	}
	cfg.PollInterval, cfg.Logger = pollInterval(opts), opts.Logger
	p, err := ews.New(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	return p, nil
}
