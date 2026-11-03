// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
)

// EASFactory builds EAS SendMail transports. The server stores sent mail
// in Sent Items itself (providerSavesSent covers domain.KindEAS). State
// should be the same store the sync engine uses (sync.EASStoreState) so
// that the policy key is shared and no extra provisioning is needed.
type EASFactory struct {
	Secrets domain.SecretStore
	// TokenSource returns the OAuth2 token source of an account (required
	// for accounts with domain.AuthOAuth2, e.g. Exchange Online).
	TokenSource func(ctx context.Context, account domain.Account) (eas.TokenSource, error)
	State       eas.StateStore
	HTTPClient  *http.Client
}

// Transport implements TransportFactory.
func (f EASFactory) Transport(ctx context.Context, account domain.Account) (domain.Transport, error) {
	cc, err := eas.AccountClientConfig(account)
	if err != nil {
		return nil, err
	}
	cc.State, cc.HTTPClient = f.State, f.HTTPClient
	if account.Incoming != nil && account.Incoming.Auth == domain.AuthOAuth2 {
		if f.TokenSource == nil {
			return nil, errors.New("send: OAuth2 EAS account without token source")
		}
		ts, err := f.TokenSource(ctx, account)
		if err != nil {
			return nil, err
		}
		cc.TokenSource = ts
	} else {
		if f.Secrets == nil || account.SecretRef == "" {
			return nil, fmt.Errorf("send: no password for EAS account %d", account.ID)
		}
		pw, err := f.Secrets.Get(account.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("send: read EAS password: %w", err)
		}
		cc.Password = string(pw)
	}
	c, err := eas.NewClient(cc)
	if err != nil {
		return nil, err
	}
	return eas.NewTransport(c), nil
}

// KindFactory is the per-kind transport router (an alias of KindRouter,
// see cloud_transport.go), e.g.
//
//	KindFactory{Default: SMTPFactory{...}, Kinds: map[domain.AccountKind]TransportFactory{domain.KindEAS: EASFactory{...}}}
type KindFactory = KindRouter
