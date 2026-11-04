// SPDX-License-Identifier: AGPL-3.0-or-later

package matrix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// fakeHS is an in-process homeserver with just enough of the client API.
type fakeHS struct {
	mu      sync.Mutex
	pending []map[string]any // room -> events for the next /sync
	sent    []map[string]any
	seq     int
	wake    chan struct{}
}

const (
	self      = "@anna:hs.test"
	bridged   = "!wa:hs.test"
	team      = "!team:hs.test"
	botDM     = "!bot:hs.test"
	waGhost   = "@whatsapp_4915112345678:hs.test"
	waBot     = "@whatsappbot:hs.test"
	stateJSON = `[
	 {"type":"m.room.member","state_key":"@anna:hs.test","sender":"@anna:hs.test","event_id":"$m1","content":{"membership":"join"}},
	 {"type":"m.room.member","state_key":"@whatsapp_4915112345678:hs.test","sender":"@whatsappbot:hs.test","event_id":"$m2","content":{"membership":"join","displayname":"Maria (WA)"}},
	 {"type":"m.room.member","state_key":"@whatsappbot:hs.test","sender":"@whatsappbot:hs.test","event_id":"$m3","content":{"membership":"join"}},
	 {"type":"m.bridge","state_key":"wa","sender":"@whatsappbot:hs.test","event_id":"$b1","content":{"bridgebot":"@whatsappbot:hs.test","protocol":{"id":"whatsapp"},"channel":{"id":"4915112345678@s.whatsapp.net"}}}
	]`
	teamState = `[
	 {"type":"m.room.member","state_key":"@anna:hs.test","sender":"@anna:hs.test","event_id":"$t1","content":{"membership":"join"}},
	 {"type":"m.room.member","state_key":"@bob:hs.test","sender":"@bob:hs.test","event_id":"$t2","content":{"membership":"join","displayname":"Bob"}},
	 {"type":"m.room.member","state_key":"@carl:hs.test","sender":"@carl:hs.test","event_id":"$t3","content":{"membership":"join"}},
	 {"type":"m.room.name","state_key":"","sender":"@bob:hs.test","event_id":"$t4","content":{"name":"Team"}}
	]`
)

func (f *fakeHS) push(room string, evs ...map[string]any) {
	f.mu.Lock()
	for _, e := range evs {
		e["room"] = room
		f.pending = append(f.pending, e)
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func msg(id, sender, msgtype, body string) map[string]any {
	return map[string]any{"type": "m.room.message", "event_id": id, "sender": sender, "origin_server_ts": time.Now().UnixMilli(),
		"content": map[string]any{"msgtype": msgtype, "body": body}}
}

func (f *fakeHS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case strings.HasSuffix(p, "/login"):
		write(map[string]any{"user_id": self, "access_token": "tok", "device_id": "DEV"})
	case strings.HasSuffix(p, "/joined_rooms"):
		write(map[string]any{"joined_rooms": []string{bridged, team}})
	case strings.Contains(p, "/account_data/"):
		w.WriteHeader(404)
		write(map[string]any{"errcode": "M_NOT_FOUND"})
	case strings.HasSuffix(p, "/state") && strings.Contains(p, "wa"):
		_, _ = io.WriteString(w, stateJSON)
	case strings.HasSuffix(p, "/state"):
		_, _ = io.WriteString(w, teamState)
	case strings.HasSuffix(p, "/filter"):
		write(map[string]any{"filter_id": "1"})
	case strings.HasSuffix(p, "/createRoom"):
		write(map[string]any{"room_id": botDM})
	case strings.Contains(p, "/send/"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		parts := strings.Split(p, "/")
		room := parts[len(parts)-4]
		f.mu.Lock()
		f.seq++
		eid := "$sent" + string(rune('a'+f.seq))
		body["_room"], body["_type"] = room, parts[len(parts)-2]
		f.sent = append(f.sent, body)
		f.mu.Unlock()
		write(map[string]any{"event_id": eid})
		if body["body"] == "login qr" {
			go func() {
				img := msg("$qr", waBot, "m.image", "2@QRDATA,abc")
				f.push(botDM, msg("$n1", waBot, "m.notice", "Scan the QR code with WhatsApp"), img)
				time.Sleep(50 * time.Millisecond)
				f.push(botDM, msg("$n2", waBot, "m.notice", "Successfully logged in as +4917011111111"))
			}()
		}
	case strings.HasSuffix(p, "/sync"):
		if r.URL.Query().Get("since") != "" {
			select {
			case <-f.wake:
			case <-time.After(200 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		f.mu.Lock()
		evs := f.pending
		f.pending = nil
		f.mu.Unlock()
		join := map[string]any{}
		for _, e := range evs {
			room := e["room"].(string)
			delete(e, "room")
			rj, _ := join[room].(map[string]any)
			if rj == nil {
				rj = map[string]any{"timeline": map[string]any{"events": []any{}}}
				join[room] = rj
			}
			tl := rj["timeline"].(map[string]any)
			tl["events"] = append(tl["events"].([]any), e)
		}
		write(map[string]any{"next_batch": "s" + time.Now().Format("150405.000000"), "rooms": map[string]any{"join": join}})
	default:
		write(map[string]any{})
	}
}

func TestMatrixLoginRoomsBridge(t *testing.T) {
	hs := &fakeHS{wake: make(chan struct{}, 1)}
	srv := httptest.NewServer(hs)
	defer srv.Close()
	ctx := context.Background()
	evs := make(chan wire.Event, 256)
	p := New()
	p.HTTPClient = srv.Client()
	env := sdk.NewTestEnv(t.TempDir(), nil, func(ev wire.Event) { evs <- ev })
	if err := p.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	acc, err := p.Pair(ctx, wire.PairRequest{Account: "m1", Method: wire.PairPassword, Homeserver: srv.URL, Username: "anna", Password: "pw"},
		sdk.NewTestPairing("m1", func(wire.PairEvent) {}))
	if err != nil || acc.Self.ID != self {
		t.Fatal(acc, err)
	}
	// The session survives a restart of the plugin.
	p2 := New()
	if err := p2.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	if accs, _ := p2.Accounts(ctx); len(accs) != 1 || accs[0].Self.ID != self {
		t.Fatal(accs)
	}
	if err := p.Connect(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(ctx)
	chats, err := p.ListChats(ctx, "m1")
	if err != nil || len(chats) != 2 {
		t.Fatal(chats, err)
	}
	wa, tm := chats[1], chats[0]
	if wa.ID != bridged || wa.Channel != "whatsapp" || wa.Kind != wire.ChatDirect || wa.Peer == nil || wa.Peer.Phone != "+4915112345678" ||
		wa.Title != "Maria (WA)" {
		t.Fatalf("bridged room: %+v %+v", wa, wa.Peer)
	}
	if tm.Kind != wire.ChatGroup || tm.Title != "Team" || tm.Channel != "" {
		t.Fatalf("team: %+v", tm)
	}
	// Live message from the WhatsApp ghost.
	hs.push(bridged, msg("$in1", waGhost, "m.text", "Hallo über die Bridge"))
	var got *wire.Message
	deadline := time.After(5 * time.Second)
	for got == nil {
		select {
		case ev := <-evs:
			if ev.Kind == wire.EventMessage && ev.Message.ID == "$in1" {
				got = ev.Message
			}
		case <-deadline:
			t.Fatal("no message from sync")
		}
	}
	if got.Sender.Phone != "+4915112345678" || got.Text != "Hallo über die Bridge" {
		t.Fatalf("%+v", got)
	}
	res, err := p.Send(ctx, wire.SendRequest{Account: "m1", ChatID: bridged, Text: "Antwort", ReplyTo: "$in1"})
	if err != nil || res.ID == "" {
		t.Fatal(res, err)
	}
	if err := p.React(ctx, wire.ReactRequest{Account: "m1", ChatID: bridged, MessageID: "$in1", Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	hs.mu.Lock()
	reply := hs.sent[0]
	react := hs.sent[1]
	hs.mu.Unlock()
	rel, _ := reply["m.relates_to"].(map[string]any)
	if reply["body"] != "Antwort" || rel == nil || rel["m.in_reply_to"] == nil {
		t.Fatalf("reply: %+v", reply)
	}
	if react["_type"] != "m.reaction" {
		t.Fatalf("reaction: %+v", react)
	}

	// Bridge pairing: the bot's QR image becomes a pairing QR.
	var mu sync.Mutex
	var pes []wire.PairEvent
	acc, err = p.Pair(ctx, wire.PairRequest{Account: "x", Method: wire.PairBridge, Network: "whatsapp", BridgeAccount: "m1"},
		sdk.NewTestPairing("x", func(pe wire.PairEvent) { mu.Lock(); pes = append(pes, pe); mu.Unlock() }))
	if err != nil || acc.ID != "m1" || acc.Channel != "whatsapp" {
		t.Fatal(acc, err)
	}
	mu.Lock()
	defer mu.Unlock()
	sawQR := false
	for _, pe := range pes {
		sawQR = sawQR || (pe.Kind == wire.PairEventQR && pe.QR == "2@QRDATA,abc")
	}
	if !sawQR {
		t.Fatalf("pairing events: %+v", pes)
	}
}

func TestBotReplyAndConvert(t *testing.T) {
	cases := map[string]string{
		"Successfully logged in as +49170": wire.PairEventSuccess, "Login failed: timeout": wire.PairEventError,
		"Your pairing code is ABCD-1234": wire.PairEventCode, "Please send your phone number": wire.PairEventInput, "Starting login": wire.PairEventInfo,
	}
	for body, kind := range cases {
		if pe := BotReply(&event.MessageEventContent{MsgType: event.MsgNotice, Body: body}); pe.Kind != kind {
			t.Errorf("%q -> %s, want %s", body, pe.Kind, kind)
		}
	}
	if pe := BotReply(&event.MessageEventContent{MsgType: event.MsgImage, Body: "2@abc"}); pe.Kind != wire.PairEventQR || pe.QR != "2@abc" {
		t.Fatal(pe)
	}
	if got := stripReplyFallback("> <@a:b> quoted\n> more\n\nreal answer"); got != "real answer" {
		t.Fatal(got)
	}
	if c := contactOf("@signal_+4915112345678:hs", ""); c.Phone != "+4915112345678" {
		t.Fatal(c)
	}
	if c := contactOf("@signal_3f2a-uuid:hs", ""); c.Phone != "" {
		t.Fatal(c)
	}
}
