// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// xoauth2Client implements the XOAUTH2 SASL mechanism used by Google and
// Microsoft. go-sasl no longer ships a client for it.
type xoauth2Client struct {
	username, token string
}

func newXOAuth2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: username, token: token}
}

func xoauth2IR(username, token string) []byte {
	return []byte("user=" + username + "\x01auth=Bearer " + token + "\x01\x01")
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	return "XOAUTH2", xoauth2IR(c.username, c.token), nil
}

// Next answers the JSON error challenge with an empty response, after which
// the server fails the command.
func (c *xoauth2Client) Next([]byte) ([]byte, error) { return []byte{}, nil }

// authenticate logs c in according to the configured method.
func (p *Provider) authenticate(ctx context.Context, c *imapclient.Client) error {
	caps := c.Caps()
	user := p.cfg.Server.Username
	switch p.cfg.Server.Auth {
	case domain.AuthOAuth2:
		token, err := p.cfg.TokenSource.Token(ctx)
		if err != nil {
			return fmt.Errorf("imap: obtain OAuth2 token: %w", err)
		}
		mech := p.cfg.OAuthMechanism
		if mech == "" {
			switch {
			case caps.Has("AUTH=XOAUTH2"):
				mech = "XOAUTH2"
			case caps.Has("AUTH=OAUTHBEARER"):
				mech = "OAUTHBEARER"
			default:
				return fmt.Errorf("imap: server offers neither XOAUTH2 nor OAUTHBEARER")
			}
		}
		var sc sasl.Client
		if mech == "OAUTHBEARER" {
			sc = sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
				Username: user, Token: token, Host: p.cfg.Server.Host, Port: p.cfg.Server.Port,
			})
		} else {
			sc = newXOAuth2Client(user, token)
		}
		if err := c.Authenticate(sc); err != nil {
			return fmt.Errorf("imap: authenticate %s: %w", mech, err)
		}
	case domain.AuthClientCert:
		if caps.Has("AUTH=EXTERNAL") {
			if err := c.Authenticate(sasl.NewExternalClient("")); err != nil {
				return fmt.Errorf("imap: authenticate EXTERNAL: %w", err)
			}
		}
	default:
		if caps.Has("AUTH=PLAIN") {
			if err := c.Authenticate(sasl.NewPlainClient("", user, p.cfg.Password)); err != nil {
				return fmt.Errorf("imap: authenticate PLAIN: %w", err)
			}
			return nil
		}
		if caps.Has("LOGINDISABLED") {
			return fmt.Errorf("imap: server disabled LOGIN and offers no AUTH=PLAIN")
		}
		if err := c.Login(user, p.cfg.Password).Wait(); err != nil {
			return fmt.Errorf("imap: login: %w", err)
		}
	}
	return nil
}
