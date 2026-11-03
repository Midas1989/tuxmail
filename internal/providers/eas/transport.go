// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Transport submits mail with EAS SendMail. The server stores the message
// in Sent Items itself (SaveInSentItems), so the send pipeline must not
// append it (send.providerSavesSent covers domain.KindEAS).
type Transport struct {
	c *Client
	// NoSaveInSent disables SaveInSentItems.
	NoSaveInSent bool
}

var _ domain.Transport = (*Transport)(nil)

// NewTransport returns a transport using c.
func NewTransport(c *Client) *Transport { return &Transport{c: c} }

// Send implements domain.Transport. Envelope recipients that do not appear
// in To/Cc/Bcc are added as a Bcc header, because EAS takes the recipients
// from the MIME header (the server removes Bcc before delivery).
func (t *Transport) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	raw := withBcc(msg.Raw, msg.Recipients)
	return t.c.SendMIME(ctx, raw, SendOptions{SaveInSent: !t.NoSaveInSent})
}

// Reply sends a reply with SmartReply; the MIME must already contain the
// quoted text (ReplaceMime), the server only links it to the original.
func (t *Transport) Reply(ctx context.Context, msg domain.OutgoingMessage, original domain.RemoteRef) error {
	return t.c.SendMIME(ctx, withBcc(msg.Raw, msg.Recipients), SendOptions{SaveInSent: !t.NoSaveInSent,
		Smart: "SmartReply", SourceFolder: CollectionID(original.FolderRemoteID), SourceItem: original.ID, ReplaceMime: true})
}

// Forward sends a forward with SmartForward. Unless replaceMime is set the
// server appends the original message including its attachments.
func (t *Transport) Forward(ctx context.Context, msg domain.OutgoingMessage, original domain.RemoteRef, replaceMime bool) error {
	return t.c.SendMIME(ctx, withBcc(msg.Raw, msg.Recipients), SendOptions{SaveInSent: !t.NoSaveInSent,
		Smart: "SmartForward", SourceFolder: CollectionID(original.FolderRemoteID), SourceItem: original.ID, ReplaceMime: replaceMime})
}

// Close implements domain.Transport.
func (t *Transport) Close() error { return nil }

// withBcc prepends a Bcc header with the recipients missing from the
// address headers of raw.
func withBcc(raw []byte, recipients []string) []byte {
	if len(recipients) == 0 {
		return raw
	}
	h := mimeHeader(raw)
	present := map[string]bool{}
	for _, key := range []string{"To", "Cc", "Bcc"} {
		for _, v := range h.Values(key) {
			for _, a := range parseAddressList(v) {
				present[strings.ToLower(a.Email)] = true
			}
		}
	}
	var missing []string
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r != "" && !present[strings.ToLower(r)] {
			missing = append(missing, r)
			present[strings.ToLower(r)] = true
		}
	}
	if len(missing) == 0 {
		return raw
	}
	out := make([]byte, 0, len(raw)+64)
	out = append(out, "Bcc: "+strings.Join(missing, ", ")+"\r\n"...)
	return append(out, raw...)
}
