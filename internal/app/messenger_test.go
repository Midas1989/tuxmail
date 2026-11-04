// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/host"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestMessengerEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := demo.Seed(ctx, st, blobs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp := fake.New("whatsapp", "whatsapp")
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC,
		Messenger: &app.MessengerOptions{Dirs: []string{}, Static: []host.Manifest{{Name: "whatsapp", New: func() sdk.Plugin { return fp }}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Start()
	events, unsub := a.Events().Subscribe(ctx, 256)
	defer unsub()

	ps := must[[]app.MessengerPlugin](t)(a.ListMessengerPlugins(ctx))
	if len(ps) < 4 || ps[0].Name != "whatsapp" || ps[0].Risk != "medium" || !ps[0].NeedsRiskAck || ps[0].Enabled || !ps[0].Installed {
		t.Fatalf("plugins: %+v", ps)
	}
	tr := true
	if _, err := a.UpdateMessengerPlugin(ctx, "whatsapp", app.MessengerPluginPatch{Enabled: &tr}); code(err) != app.CodeRiskNotAccepted {
		t.Fatalf("risk: %v", err)
	}
	if _, err := a.StartMessengerPairing(ctx, app.MessengerPairingInput{Plugin: "whatsapp", Method: "qr"}); code(err) != app.CodeMessengerDisabled {
		t.Fatalf("pair disabled: %v", err)
	}
	p := must[app.MessengerPlugin](t)(a.UpdateMessengerPlugin(ctx, "whatsapp", app.MessengerPluginPatch{Enabled: &tr, AcceptRisk: true}))
	if !p.Enabled || p.RiskAcceptedAt == "" {
		t.Fatal(p)
	}
	pr := must[app.MessengerPairing](t)(a.StartMessengerPairing(ctx, app.MessengerPairingInput{Plugin: "whatsapp", Method: "qr", Phone: "+4917099887766"}))
	eventually(t, "qr event", func() bool {
		for {
			select {
			case ev := <-events:
				b, _ := json.Marshal(ev)
				if ev.Type == app.EventMessengerPairing && strings.Contains(string(b), `"qr":"fake://pair/`) {
					return true
				}
			default:
				return false
			}
		}
	})
	eventually(t, "input", func() bool { p, _ := a.GetMessengerPairing(ctx, pr.ID); return p.State == "input" })
	if err := a.SubmitMessengerPairingInput(ctx, pr.ID, "confirm", "ok"); err != nil {
		t.Fatal(err)
	}
	var accID int64
	eventually(t, "linked", func() bool {
		p, _ := a.GetMessengerPairing(ctx, pr.ID)
		accID = p.AccountID
		return p.State == "success"
	})
	eventually(t, "synced", func() bool {
		l, _ := a.ListMessengerAccounts(ctx)
		return len(l) == 1 && l[0].SyncedAt != "" && l[0].State == "connected"
	})
	ma, _ := st.GetMessengerAccount(ctx, accID)

	// Maria writes on WhatsApp (same number as her Signal handle).
	peer := wire.Contact{ID: "4915112345678@s.whatsapp.net", Name: "Maria"}
	chat := wire.Chat{ID: peer.ID, Kind: wire.ChatDirect, Peer: &peer}
	fp.Receive(ma.PluginAccount, chat, wire.Message{ID: "w1", Sender: peer, Time: time.Now().UTC().Truncate(time.Second), Text: "Hallo per WhatsApp",
		Media: &wire.Media{Ref: "r1", MIMEType: "image/png", Name: "logo.png"}}, []byte("PNG"))
	var conv app.Conversation
	eventually(t, "conversation", func() bool {
		page, err := a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Limit: 50})
		if err != nil {
			return false
		}
		for _, c := range page.Items {
			if slices.Contains(c.Channels, "whatsapp") {
				conv = c
				return true
			}
		}
		return false
	})
	if conv.UnreadCount != 1 || conv.Snippet == "" || !slices.Contains(conv.AccountIDs, accID) {
		t.Fatalf("conversation: %+v", conv)
	}
	// Person grouping: Maria's person view now contains mails, Signal and WhatsApp.
	page := must[app.Page[app.Conversation]](t)(a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"},
		Grouping: "person", Limit: 50}))
	found := false
	for _, c := range page.Items {
		if slices.Contains(c.ParticipantIDs, d.Maria) && slices.Contains(c.Channels, "whatsapp") && slices.Contains(c.Channels, "signal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("person view without whatsapp: %+v", page.Items)
	}
	items := must[[]app.ConversationItem](t)(a.ListConversationItems(ctx, conv.ID, app.ItemsQuery{}))
	if len(items) != 1 || items[0].Channel != "whatsapp" || len(items[0].Attachments) != 1 || !strings.HasPrefix(items[0].Attachments[0].URL, "/api/messenger/media/") {
		t.Fatalf("items: %+v", items)
	}
	_, mid, _ := strings.Cut(items[0].ID, ":")
	var chatMsgID int64
	_, _ = jsonNumber(mid, &chatMsgID)
	media := must[app.AttachmentContent](t)(a.MessengerMedia(ctx, chatMsgID))
	if string(media.Data) != "PNG" || media.MIMEType != "image/png" {
		t.Fatal(media)
	}

	// Reply, react, read.
	res := must[app.SendResult](t)(a.Send(ctx, app.OutgoingMessage{Channel: "whatsapp", ConversationID: conv.ID, Text: "Gern!", ReplyToItemID: items[0].ID}))
	if !strings.HasPrefix(res.ItemID, "c:") {
		t.Fatal(res)
	}
	if s := fp.SentLog(); len(s) != 1 || s[0].Text != "Gern!" || s[0].ReplyTo != "w1" {
		t.Fatalf("sent: %+v", s)
	}
	rr := must[app.ReactionResult](t)(a.ReactToItem(ctx, conv.ID, items[0].ID, "🎉"))
	if len(rr.Reactions) != 1 || !rr.Reactions[0].Mine {
		t.Fatal(rr)
	}
	if err := a.MarkConversationRead(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(fp.CallLog(), "\n")
	if !strings.Contains(calls, "react "+chat.ID+" w1 🎉") || !strings.Contains(calls, "mark_read "+chat.ID+" w1") {
		t.Fatalf("calls: %s", calls)
	}
	c2 := must[app.Conversation](t)(a.GetConversation(ctx, conv.ID))
	if c2.UnreadCount != 0 {
		t.Fatal(c2)
	}
	// Delete own message: revoked for everyone.
	if err := a.DeleteItem(ctx, conv.ID, res.ItemID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fp.CallLog(), "\n"), "everyone=true") {
		t.Fatal(fp.CallLog())
	}
	if err := a.SetMessengerTyping(ctx, conv.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveMessengerAccount(ctx, accID); err != nil {
		t.Fatal(err)
	}
	if l := must[[]app.MessengerAccount](t)(a.ListMessengerAccounts(ctx)); len(l) != 0 {
		t.Fatal(l)
	}
}

func jsonNumber(s string, out *int64) (bool, error) {
	err := json.Unmarshal([]byte(s), out)
	return err == nil, err
}
