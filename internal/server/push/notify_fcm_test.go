// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/server/push/fcm"
)

type fakeFCM struct {
	mu   sync.Mutex
	sent []fcm.Message
	gone map[string]bool
}

func (f *fakeFCM) Send(_ context.Context, m fcm.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone[m.Token] {
		return fcm.ErrUnregistered
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeFCM) payloads(t *testing.T) []Payload {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Payload
	for _, m := range f.sent {
		var p Payload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestNotifyFCMDedupeQuietHoursPrivacy(t *testing.T) {
	svc, s, _, _, _, _ := setup(t)
	ctx := context.Background()
	u, _ := svc.CreateUser(ctx, auth.NewUser{Username: "fiona"})
	f := &fakeFCM{gone: map[string]bool{}}
	s.FCM = f
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	tok := "fcm-token-abcdefghijklmnopqrstuvwxyz"
	if _, err := s.Store.Save(ctx, Subscription{UserID: u.ID, Endpoint: FCMEndpoint(tok), Kind: KindFCM, DeviceName: "Pixel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Save(ctx, Subscription{UserID: u.ID, Endpoint: "fcm:short", Kind: KindFCM}); err == nil {
		t.Fatal("invalid token accepted")
	}
	n := Notification{Payload: Payload{Type: "chat", Title: "Anna", Body: "Hallo", Sender: "Anna", Tag: "chat-1", ConversationID: "1"}, Urgency: UrgencyHigh}
	if err := s.Notify(ctx, u.ID, n); err != nil {
		t.Fatal(err)
	}
	// Same tag again within the window: suppressed.
	if err := s.Notify(ctx, u.ID, n); !errors.Is(err, ErrDuplicate) {
		t.Fatal("duplicate delivered:", err)
	}
	if len(f.sent) != 1 || !f.sent[0].High || f.sent[0].Token != tok || f.sent[0].CollapseKey != "chat-1" {
		t.Fatalf("%+v", f.sent)
	}
	// Quiet hours suppress.
	if err := s.Store.SetPrefs(ctx, u.ID, Prefs{Enabled: true, ShowContent: true, QuietHours: &QuietHours{Start: "11:00", End: "13:00"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "mail", Title: "x", Tag: "m2"}}); !errors.Is(err, ErrSuppressed) {
		t.Fatal("quiet hours:", err)
	}
	// Privacy mode: no title/body/sender leaves the server.
	_ = s.Store.SetPrefs(ctx, u.ID, Prefs{Enabled: true, ShowContent: false})
	if err := s.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "mail", Title: "Anna", Body: "Gehalt", Sender: "Anna", Tag: "m3"}}); err != nil {
		t.Fatal(err)
	}
	p := f.payloads(t)[1]
	if p.Title != PrivacyTitle || p.Body != PrivacyBody || p.Sender != "" {
		t.Fatalf("privacy leak: %+v", p)
	}
	// Channel toggle.
	_ = s.Store.SetPrefs(ctx, u.ID, Prefs{Enabled: true, ShowContent: true, Channels: map[string]bool{"team": false}})
	if err := s.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "team", Title: "@you", Tag: "t1"}}); !errors.Is(err, ErrSuppressed) {
		t.Fatal(err)
	}
	// An unregistered token is removed.
	f.gone[tok] = true
	if err := s.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "mail", Title: "y", Tag: "m4"}}); err != nil {
		t.Fatal(err)
	}
	if subs, _ := s.Store.List(ctx, u.ID); len(subs) != 0 {
		t.Fatal("unregistered FCM token kept", subs)
	}
	// Without FCM client the registration fails clearly.
	s.FCM = nil
	if err := s.sendFCM(ctx, Subscription{Kind: KindFCM, Endpoint: FCMEndpoint(tok)}, n); !errors.Is(err, ErrFCMNotConfigured) {
		t.Fatal(err)
	}
	// After the window the same tag is delivered again.
	var d deduper
	if !d.first(1, "x", now) || d.first(1, "x", now.Add(time.Minute)) || !d.first(1, "x", now.Add(DedupeWindow+time.Second)) || !d.first(2, "x", now) {
		t.Fatal("dedupe window")
	}
}

func TestOnePerSession(t *testing.T) {
	subs := []Subscription{{ID: 1, SessionID: 7}, {ID: 2}, {ID: 3, SessionID: 7}, {ID: 4, SessionID: 8}}
	got := OnePerSession(subs)
	if len(got) != 3 || got[0].ID != 2 || got[1].ID != 3 || got[2].ID != 4 {
		t.Fatalf("%+v", got)
	}
}
