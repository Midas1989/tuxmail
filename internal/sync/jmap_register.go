// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	jmapprov "github.com/Midas1989/Mailclient/internal/providers/jmap"
)

func init() { registerBuiltin(domain.KindJMAP, JMAPFactory) }

// JMAPConfig builds the JMAP provider configuration of an account (session
// URL from Extra["jmapUrl"] or the incoming host's /.well-known/jmap).
func JMAPConfig(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (jmapprov.Config, error) {
	u := jmapprov.SessionURLFor(acc)
	if u == "" {
		return jmapprov.Config{}, Permanent(fmt.Errorf("sync: account %d has no JMAP URL", acc.ID))
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if opts.TLSConfig != nil {
		tr.TLSClientConfig = opts.TLSConfig.Clone()
	}
	if opts.DialContext != nil {
		tr.DialContext = opts.DialContext
	}
	cfg := jmapprov.Config{SessionURL: u, HTTPClient: &http.Client{Transport: tr, Timeout: 2 * time.Minute}, Logger: opts.Logger,
		PollInterval: opts.PollInterval, MinBackoff: opts.WatchMinBackoff, MaxBackoff: opts.WatchMaxBackoff}
	jmapprov.ApplyAccountOptions(&cfg, acc) // confirmed http / extra hosts
	user := ""
	auth := domain.AuthPassword
	if acc.Incoming != nil {
		user, auth = acc.Incoming.Username, acc.Incoming.Auth
	}
	if user == "" && acc.Extra != nil {
		user = acc.Extra["address"]
	}
	switch auth {
	case domain.AuthOAuth2:
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return cfg, err
		}
		cfg.Auth = jmapprov.BearerAuth(ts.Token)
	default:
		pw, err := creds.Password(ctx, acc)
		if err != nil {
			return cfg, err
		}
		cfg.Auth = jmapprov.BasicAuth(user, pw)
	}
	return cfg, nil
}

// JMAPFactory builds a JMAP mail provider.
func JMAPFactory(ctx context.Context, acc domain.Account, creds Credentials, opts ProviderOptions) (domain.MailProvider, error) {
	cfg, err := JMAPConfig(ctx, acc, creds, opts)
	if err != nil {
		return nil, err
	}
	p, err := jmapprov.New(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	return permanentErrors{MailProvider: p, permanent: func(err error) bool {
		var se *jmapprov.SetError
		return errors.Is(err, jmapprov.ErrNotFound) || errors.As(err, &se)
	}}, nil
}
