// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"context"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// M-11: even with content enabled nothing readable goes to Google; the ids for
// opening and replying stay.
func TestNotifyFCMNeverCarriesContent(t *testing.T) {
	svc, s, _, _, _, _ := setup(t)
	ctx := context.Background()
	u, _ := svc.CreateUser(ctx, auth.NewUser{Username: "gina"})
	f := &fakeFCM{gone: map[string]bool{}}
	s.FCM = f
	if _, err := s.Store.Save(ctx, Subscription{UserID: u.ID, Endpoint: FCMEndpoint("fcm-token-abcdefghijklmnopqrstuvwxyz"), Kind: KindFCM}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SetPrefs(ctx, u.ID, Prefs{Enabled: true, ShowContent: true}); err != nil {
		t.Fatal(err)
	}
	n := Notification{Payload: Payload{Type: "chat", Title: "Anna", Body: "Streng geheim", Sender: "Anna", Tag: "chat-9", ConversationID: "9", ItemID: "m:5"}}
	if err := s.Notify(ctx, u.ID, n); err != nil {
		t.Fatal(err)
	}
	raw := string(f.sent[0].Payload)
	if strings.Contains(raw, "Anna") || strings.Contains(raw, "geheim") {
		t.Fatalf("content in FCM payload: %s", raw)
	}
	p := f.payloads(t)[0]
	if p.Title != PrivacyTitle || p.Body != PrivacyBody || p.ConversationID != "9" || p.ItemID != "m:5" || p.Type != "chat" {
		t.Fatalf("%+v", p)
	}
}
