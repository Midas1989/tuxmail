// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/host"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

type recNotifier struct {
	NopNotifier
	mu       sync.Mutex
	pairings []Pairing
	typing   []TypingInfo
}

func (r *recNotifier) PairingChanged(p Pairing) {
	r.mu.Lock()
	r.pairings = append(r.pairings, p)
	r.mu.Unlock()
}

func (r *recNotifier) Typing(t TypingInfo) {
	r.mu.Lock()
	r.typing = append(r.typing, t)
	r.mu.Unlock()
}

type env struct {
	t   *testing.T
	st  *store.Store
	bus *events.Bus
	svc *Service
	fp  *fake.Plugin
	n   *recNotifier
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	t.Cleanup(bus.Close)
	fp := fake.New("whatsapp", "whatsapp")
	n := &recNotifier{}
	svc, err := New(Options{Store: st, Blobs: blobs, Bus: bus, DataDir: dir, Dirs: []string{},
		Static: []host.Manifest{{Name: "whatsapp", New: func() sdk.Plugin { return fp }}}, Notifier: n})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &env{t: t, st: st, bus: bus, svc: svc, fp: fp, n: n, ctx: ctx}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
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

// pair links an account through the QR flow and returns its id.
func (e *env) pair() int64 {
	t := e.t
	tr := true
	if err := e.svc.UpdatePlugin(e.ctx, "whatsapp", PluginPatch{Enabled: &tr}); !errors.Is(err, ErrRiskNotAcked) {
		t.Fatalf("enable without risk ack: %v", err)
	}
	if err := e.svc.UpdatePlugin(e.ctx, "whatsapp", PluginPatch{Enabled: &tr, AcceptRisk: true}); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.StartPairing(e.ctx, PairingInput{Plugin: "whatsapp", Method: wire.PairQR, Phone: "+4917099887766"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "input request", func() bool { cur, _ := e.svc.GetPairing(p.ID); return cur.State == PairNeedInput })
	e.n.mu.Lock()
	sawQR := false
	for _, x := range e.n.pairings {
		sawQR = sawQR || (x.State == PairShowQR && strings.HasPrefix(x.QR, "fake://pair/"))
	}
	e.n.mu.Unlock()
	if !sawQR {
		t.Fatal("no QR state")
	}
	if err := e.svc.SubmitPairingInput(e.ctx, p.ID, "confirm", "yes"); err != nil {
		t.Fatal(err)
	}
	var cur Pairing
	waitUntil(t, "success", func() bool { cur, _ = e.svc.GetPairing(p.ID); return cur.State == PairSuccess })
	acc, err := e.st.GetAccount(e.ctx, cur.AccountID)
	if err != nil || acc.Kind != domain.KindWhatsApp || acc.Extra["address"] != "+4917099887766" {
		t.Fatalf("account: %+v %v", acc, err)
	}
	waitUntil(t, "connected", func() bool { st, ok := e.svc.AccountState(cur.AccountID); return ok && st.State == wire.StateConnected })
	waitUntil(t, "initial sync", func() bool {
		ma, _ := e.st.GetMessengerAccount(e.ctx, cur.AccountID)
		return !ma.SyncedAt.IsZero()
	})
	return cur.AccountID
}

func (e *env) pluginAccount(accountID int64) string {
	ma, err := e.st.GetMessengerAccount(e.ctx, accountID)
	if err != nil {
		e.t.Fatal(err)
	}
	return ma.PluginAccount
}

func TestE164(t *testing.T) {
	for in, want := range map[string]string{
		"+49 170 1234567": "+491701234567", "0049 170 1234567": "+491701234567", "491701234567@s.whatsapp.net": "+491701234567",
		"491701234567:12@s.whatsapp.net": "+491701234567", "0170 1234567": "", "@anna:matrix.org": "", "": "",
	} {
		if got := E164(in); got != want {
			t.Errorf("E164(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPairingReceiveMergeSend(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	// Maria is known from mail with her phone number on the contact card.
	maria, err := e.st.CreatePerson(ctx, store.Person{DisplayName: "Maria Sommer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddHandle(ctx, store.Handle{PersonID: maria, Kind: store.HandleEmail, Value: "maria@example.org"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddHandle(ctx, store.Handle{PersonID: maria, Kind: store.HandlePhone, Value: "+49 151 12345678"}); err != nil {
		t.Fatal(err)
	}
	sub := e.bus.Subscribe(ctx, events.SubscribeOptions{Name: "t", Types: []events.Type{events.ChatReceived}})
	defer sub.Unsubscribe()

	accID := e.pair()
	pa := e.pluginAccount(accID)
	peer := wire.Contact{ID: "4915112345678@s.whatsapp.net", Name: "Maria"}
	chat := wire.Chat{ID: "4915112345678@s.whatsapp.net", Kind: wire.ChatDirect, Peer: &peer}
	now := time.Now().UTC().Truncate(time.Second)
	e.fp.Receive(pa, chat, wire.Message{ID: "m1", Sender: peer, Time: now, Text: "Hallo aus WhatsApp"}, nil)
	e.fp.Receive(pa, chat, wire.Message{ID: "m2", Sender: peer, Time: now.Add(time.Second),
		Media: &wire.Media{Ref: "r1", MIMEType: "image/jpeg", Name: "foto.jpg", Size: 3}}, []byte("JPG"))

	var c store.Chat
	waitUntil(t, "chat stored", func() bool {
		c, err = e.st.ChatByRemoteID(ctx, accID, chat.ID)
		if err != nil {
			return false
		}
		ms, _ := e.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, 10)
		return len(ms) == 2
	})
	if c.Channel != "whatsapp" || c.ConversationID == 0 || c.Title != "Maria" {
		t.Fatalf("chat: %+v", c)
	}
	// Person mapping: the WhatsApp contact is Maria (merged by phone).
	if cp, _ := e.st.ChatCounterpart(ctx, c.ID); cp != maria {
		t.Fatalf("counterpart %d, want %d", cp, maria)
	}
	if p, err := e.st.FindPersonByHandle(ctx, store.HandleWhatsApp, "+4915112345678"); err != nil || p.ID != maria {
		t.Fatalf("whatsapp handle: %+v %v", p, err)
	}
	select {
	case ev := <-sub.C:
		if ev.Chat == nil || ev.Chat.Text != "Hallo aus WhatsApp" || ev.Chat.Channel != "whatsapp" {
			t.Fatalf("event: %+v", ev.Chat)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no chat.received")
	}

	ms, _ := e.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, 10)
	media := ms[0]
	if media.RemoteID != "m2" || media.MediaBlob != "" {
		t.Fatalf("media message: %+v", media)
	}
	mc, err := e.svc.Media(ctx, media.ID)
	if err != nil || string(mc.Data) != "JPG" || mc.MIMEType != "image/jpeg" {
		t.Fatalf("media: %+v %v", mc, err)
	}
	if m, _ := e.st.GetChatMessage(ctx, media.ID); m.MediaBlob == "" {
		t.Fatal("media not cached in blob store")
	}

	// Mark read sends receipts for both incoming messages.
	if err := e.svc.MarkRead(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	// Reply with text and an attachment.
	out, err := e.svc.Send(ctx, SendInput{ChatID: c.ID, Text: "Danke!", ReplyTo: ms[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	sent, _ := e.st.GetChatMessage(ctx, out.ChatMessageID)
	if !sent.FromMe || sent.Body != "Danke!" || sent.Status == "" || sent.ReplyToRemoteID != "m1" {
		t.Fatalf("sent: %+v", sent)
	}
	waitUntil(t, "delivered", func() bool {
		m, _ := e.st.GetChatMessage(ctx, out.ChatMessageID)
		return m.Status == store.ChatStatusDelivered
	})
	_, err = e.svc.Send(ctx, SendInput{ChatID: c.ID, Media: []OutgoingMedia{{Name: "../plan.pdf", MIMEType: "application/pdf",
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("PDF")), nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	if s := e.fp.SentLog(); len(s) != 2 || s[0].ReplyTo != "m1" || s[1].Media == nil || s[1].Media.Name != "../plan.pdf" {
		t.Fatalf("plugin sends: %+v", s)
	}

	// Remote read receipt, reaction from the peer, own reaction toggle.
	e.fp.Emit(wire.Event{Kind: wire.EventReceipt, Account: pa, Receipt: &wire.Receipt{ChatID: chat.ID, MessageIDs: []string{sent.RemoteID},
		Kind: wire.ReceiptRead, Time: time.Now()}})
	e.fp.Emit(wire.Event{Kind: wire.EventReaction, Account: pa, Reaction: &wire.ReactionEvent{ChatID: chat.ID, MessageID: sent.RemoteID,
		Sender: peer, Emoji: "❤️"}})
	waitUntil(t, "receipt+reaction", func() bool {
		m, _ := e.st.GetChatMessage(ctx, out.ChatMessageID)
		return m.Status == store.ChatStatusRead && len(m.Reactions) == 1
	})
	m, err := e.svc.React(ctx, ms[1].ID, "👍")
	if err != nil || len(m.Reactions) != 1 || m.Reactions[0].Sender != "me" {
		t.Fatal(m.Reactions, err)
	}
	if m, err = e.svc.React(ctx, ms[1].ID, "👍"); err != nil || len(m.Reactions) != 0 {
		t.Fatal("toggle off", m.Reactions, err)
	}
	// Edit and delete from the network.
	e.fp.Emit(wire.Event{Kind: wire.EventEdit, Account: pa, Message: &wire.Message{ChatID: chat.ID, ID: "m1", Sender: peer, Text: "Hallo!",
		EditedAt: time.Now()}})
	e.fp.Emit(wire.Event{Kind: wire.EventDelete, Account: pa, ChatID: chat.ID, MessageID: "m2"})
	e.fp.Emit(wire.Event{Kind: wire.EventTyping, Account: pa, Typing: &wire.Typing{ChatID: chat.ID, Sender: peer, Active: true}})
	waitUntil(t, "edit+delete", func() bool {
		a, _ := e.st.ChatMessageByRemoteID(ctx, c.ID, "m1")
		b, _ := e.st.ChatMessageByRemoteID(ctx, c.ID, "m2")
		return a.Body == "Hallo!" && !a.EditedAt.IsZero() && b.Deleted
	})
	waitUntil(t, "typing", func() bool {
		e.n.mu.Lock()
		defer e.n.mu.Unlock()
		return len(e.n.typing) == 1 && e.n.typing[0].PersonID == maria && e.n.typing[0].ConversationID == c.ConversationID
	})
	calls := strings.Join(e.fp.CallLog(), "\n")
	for _, want := range []string{"mark_read " + chat.ID + " m1,m2", "react " + chat.ID + " m1 👍 remove=false", "react " + chat.ID + " m1 👍 remove=true"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q in\n%s", want, calls)
		}
	}

	// Password delivery: Maria is addressed by her e-mail address.
	if err := e.svc.SendPassword(ctx, accID, domain.Address{Email: "Maria@example.org"}, "Passwort: geheim"); err != nil {
		t.Fatal(err)
	}
	if s := e.fp.SentLog(); s[len(s)-1].ChatID != chat.ID || s[len(s)-1].Text != "Passwort: geheim" {
		t.Fatalf("password: %+v", s[len(s)-1])
	}
	if err := e.svc.SendPassword(ctx, accID, domain.Address{Email: "nobody@example.org"}, "x"); err == nil {
		t.Fatal("password to unknown recipient")
	}

	// Disabling the plugin blocks sending; removing the account deletes chats.
	f := false
	if err := e.svc.UpdatePlugin(ctx, "whatsapp", PluginPatch{Enabled: &f}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Send(ctx, SendInput{ChatID: c.ID, Text: "x"}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := e.svc.RemoveAccount(ctx, accID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetChat(ctx, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestHistorySyncAndRestart(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	accID := e.pair()
	pa := e.pluginAccount(accID)
	group := wire.Chat{ID: "g1@g.us", Kind: wire.ChatGroup, Title: "Familie", Unread: 1, LastActivity: time.Now()}
	a := wire.Contact{ID: "491701111111@s.whatsapp.net", Name: "Tom"}
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	for i, txt := range []string{"eins", "zwei", "drei"} {
		e.fp.Receive(pa, group, wire.Message{ID: "h" + txt, Sender: a, Time: base.Add(time.Duration(i) * time.Minute), Text: txt}, nil)
	}
	waitUntil(t, "live messages", func() bool {
		c, err := e.st.ChatByRemoteID(ctx, accID, "g1@g.us")
		if err != nil {
			return false
		}
		ms, _ := e.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, 10)
		return len(ms) == 3
	})
	// Simulate a fresh install of the same session: delete the chat, reset
	// the sync marker and restart the plugin -> history sync restores it.
	c, _ := e.st.ChatByRemoteID(ctx, accID, "g1@g.us")
	if err := e.st.DeleteChat(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Host().Restart("whatsapp"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "history synced", func() bool {
		c, err := e.st.ChatByRemoteID(ctx, accID, "g1@g.us")
		if err != nil {
			return false
		}
		ms, _ := e.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, 10)
		return len(ms) == 3
	})
	c, _ = e.st.ChatByRemoteID(ctx, accID, "g1@g.us")
	if c.Kind != store.ChatGroup || c.Title != "Familie" {
		t.Fatalf("group: %+v", c)
	}
	if cp, _ := e.st.ChatCounterpart(ctx, c.ID); cp != 0 {
		t.Fatalf("group counterpart %d", cp)
	}
}

func TestPairingError(t *testing.T) {
	e := newEnv(t)
	tr := true
	if err := e.svc.UpdatePlugin(e.ctx, "whatsapp", PluginPatch{Enabled: &tr, AcceptRisk: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartPairing(e.ctx, PairingInput{Plugin: "whatsapp", Method: wire.PairPassword}); err == nil {
		t.Fatal("unsupported method accepted")
	}
	p, err := e.svc.StartPairing(e.ctx, PairingInput{Plugin: "whatsapp", Method: wire.PairPhone, Phone: "+491700000001"})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "code", func() bool { c, _ := e.svc.GetPairing(p.ID); return c.State == PairNeedInput })
	if err := e.svc.SubmitPairingInput(e.ctx, p.ID, "confirm", "fail"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "error", func() bool {
		c, _ := e.svc.GetPairing(p.ID)
		return c.State == PairError && c.Message == "pairing rejected"
	})
	accs, _ := e.st.ListMessengerAccounts(e.ctx, "")
	if len(accs) != 0 {
		t.Fatal(accs)
	}
	ps, err := e.svc.Plugins(e.ctx)
	if err != nil || len(ps) < 4 || ps[0].Spec.Name != "whatsapp" || !ps[0].Installed || !ps[0].Enabled || ps[0].RiskAcceptedAt.IsZero() {
		t.Fatalf("plugins: %+v %v", ps, err)
	}
}

// The desktop app stops its plugins while it works through a server and starts
// them again without a restart when it disconnects.
func TestStopPluginsAndStartAgain(t *testing.T) {
	e := newEnv(t)
	e.pair()
	waitUntil(t, "plugin running", func() bool { return e.svc.Host().Running("whatsapp") })
	e.svc.StopPlugins()
	waitUntil(t, "plugin stopped", func() bool { return !e.svc.Host().Running("whatsapp") })
	e.svc.Start()
	waitUntil(t, "plugin running again", func() bool { return e.svc.Host().Running("whatsapp") })
}
