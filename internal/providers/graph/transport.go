// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/mimehdr"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
)

// Send modes of shared mailboxes (KONZEPT 7.3a).
const (
	SendAs       = "send_as"
	SendOnBehalf = "send_on_behalf"
)

// Transport sends mail through POST …/sendMail with the MIME message
// (base64, text/plain). Exchange stores the message in Sent: for Send-As
// in the Sent folder of the shared mailbox, for Send-on-Behalf in the own one.
type Transport struct{ c *Client }

var _ domain.Transport = (*Transport)(nil)

// NewTransport creates a transport.
func NewTransport(cfg Config) (*Transport, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Transport{c: c}, nil
}

// Close implements domain.Transport.
func (t *Transport) Close() error { return nil }

// Send implements domain.Transport.
func (t *Transport) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	raw := msg.Raw
	path := t.c.user() + "/sendMail"
	if t.c.cfg.Mailbox != "" && t.c.cfg.SendMode == SendOnBehalf {
		path = "/me/sendMail"
		if t.c.cfg.Self != "" {
			raw = mimehdr.Ensure(raw, "Sender", t.c.cfg.Self)
		}
	}
	// sendMail with MIME takes the recipients from the headers: envelope
	// recipients missing there (Bcc) are added as Bcc header, which
	// Exchange removes before delivery.
	if bcc := mimehdr.MissingRecipients(raw, msg.Recipients); len(bcc) > 0 {
		raw = mimehdr.Ensure(raw, "Bcc", strings.Join(bcc, ", "))
	}
	return t.c.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: path,
		Body: []byte(base64.StdEncoding.EncodeToString(raw)), ContentType: "text/plain"}, nil)
}
