// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Midas1989/Mailclient/internal/server/push/fcm"
)

// FCM registrations are stored like Web Push subscriptions with the
// pseudo endpoint "fcm:<token>" and no keys (mobile/SERVER_TODO.md 1).

// FCMEndpointPrefix prefixes the registration token in Subscription.Endpoint.
const FCMEndpointPrefix = "fcm:"

// FCMClient sends one data message (implemented by *fcm.Client).
type FCMClient interface {
	Send(ctx context.Context, m fcm.Message) error
}

// FCMEndpoint returns the stored endpoint of a registration token.
func FCMEndpoint(token string) string { return FCMEndpointPrefix + token }

// FCMToken returns the token of an FCM subscription ("" for other kinds).
func FCMToken(sub Subscription) string {
	if sub.Kind != KindFCM {
		return ""
	}
	return strings.TrimPrefix(sub.Endpoint, FCMEndpointPrefix)
}

func validateFCM(sub Subscription) error {
	tok, ok := strings.CutPrefix(sub.Endpoint, FCMEndpointPrefix)
	if !ok || len(tok) < 20 || len(tok) > 4096 || strings.ContainsAny(tok, " \t\r\n") {
		return errors.New("push: invalid FCM registration token")
	}
	return nil
}

// ErrFCMNotConfigured is returned for FCM registrations when the server has
// no service account (push.fcm.service_account_file).
var ErrFCMNotConfigured = errors.New("push: Firebase Cloud Messaging is not configured")

func (s *Sender) sendFCM(ctx context.Context, sub Subscription, n Notification) error {
	if s.FCM == nil {
		return ErrFCMNotConfigured
	}
	// FCM passes through Google's servers: sender, subject and chat text never
	// travel there in plaintext (M-11). Only the generic text and the ids the
	// app needs to open the conversation or reply (conversationId, itemId, tag,
	// type, accountId, teamId, url) are sent; the app builds the notification
	// from those and loads the content from the server itself.
	pl := n.Payload
	pl.Title, pl.Body, pl.Sender = PrivacyTitle, PrivacyBody, ""
	body, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	err = s.FCM.Send(ctx, fcm.Message{Token: FCMToken(sub), Payload: body, High: n.Urgency == UrgencyHigh,
		TTL: n.TTL, CollapseKey: n.Tag})
	switch {
	case errors.Is(err, fcm.ErrUnregistered):
		if s.Log != nil {
			s.Log.Info("FCM registration expired, removing", "user", sub.UserID, "err", err)
		}
		return s.Store.DeleteEndpoint(context.WithoutCancel(ctx), sub.Endpoint)
	case err != nil:
		return err
	}
	return s.Store.MarkSuccess(ctx, sub.ID)
}
