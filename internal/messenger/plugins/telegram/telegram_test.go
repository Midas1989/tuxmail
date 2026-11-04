// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

func TestCredentials(t *testing.T) {
	t.Setenv("TUXMAIL_TELEGRAM_API_ID", "")
	t.Setenv("TUXMAIL_TELEGRAM_API_HASH", "")
	if _, _, err := Credentials(nil); wire.ErrorCode(err) != wire.CodeUnavailable {
		t.Fatal(err)
	}
	t.Setenv("TUXMAIL_TELEGRAM_API_ID", "12345")
	t.Setenv("TUXMAIL_TELEGRAM_API_HASH", "abc")
	if id, h, err := Credentials(nil); err != nil || id != 12345 || h != "abc" {
		t.Fatal(id, h, err)
	}
	if _, _, err := Credentials(map[string]string{"api_id": "x", "api_hash": "y"}); wire.ErrorCode(err) != wire.CodeInvalid {
		t.Fatal(err)
	}
	// Without credentials the plugin starts but reports an update hint and
	// refuses to pair.
	t.Setenv("TUXMAIL_TELEGRAM_API_ID", "")
	p := New()
	if err := p.Init(context.Background(), sdk.NewTestEnv(t.TempDir(), nil, func(wire.Event) {})); err != nil {
		t.Fatal(err)
	}
	if p.Info().UpdateHint == "" {
		t.Fatal("no hint")
	}
	_, err := p.Pair(context.Background(), wire.PairRequest{Account: "a1", Method: wire.PairQR}, sdk.NewTestPairing("a1", func(wire.PairEvent) {}))
	if wire.ErrorCode(err) != wire.CodeUnavailable {
		t.Fatal(err)
	}
}

func TestPeersAndConvert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	pc := loadPeers(path)
	me := &tg.User{ID: 1, Self: true, FirstName: "Ich", AccessHash: 11}
	anna := &tg.User{ID: 2, FirstName: "Anna", LastName: "Muster", Username: "anna", Phone: "4915112345678", AccessHash: 22}
	pc.learn([]tg.UserClass{me, anna}, []tg.ChatClass{&tg.Channel{ID: 7, Title: "News", AccessHash: 77}, &tg.Channel{ID: 8, Title: "Team",
		Megagroup: true, AccessHash: 88}, &tg.Chat{ID: 9, Title: "Familie"}})
	if err := pc.save(); err != nil {
		t.Fatal(err)
	}
	pc = loadPeers(path)
	if ip, err := pc.inputPeer("u2"); err != nil || ip.(*tg.InputPeerUser).AccessHash != 22 {
		t.Fatal(ip, err)
	}
	if ip, _ := pc.inputPeer("u1"); ip == nil {
		t.Fatal("self")
	} else if _, ok := ip.(*tg.InputPeerSelf); !ok {
		t.Fatalf("%T", ip)
	}
	if ip, err := pc.inputPeer("ch7"); err != nil || ip.(*tg.InputPeerChannel).AccessHash != 77 {
		t.Fatal(ip, err)
	}
	if _, err := pc.inputPeer("u99"); wire.ErrorCode(err) != wire.CodeNotFound {
		t.Fatal(err)
	}
	if pc.kind("ch7") != wire.ChatChannel || pc.kind("ch8") != wire.ChatGroup || pc.kind("c9") != wire.ChatGroup || pc.kind("u2") != wire.ChatDirect {
		t.Fatal("kinds")
	}
	users := map[int64]*tg.User{2: anna}
	m := &tg.Message{ID: 42, PeerID: &tg.PeerUser{UserID: 2}, Date: 1700000000, Message: "Hallo",
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 41},
		Media: &tg.MessageMediaDocument{Document: &tg.Document{MimeType: "application/pdf", Size: 1234,
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "plan.pdf"}}}},
		Reactions: tg.MessageReactions{RecentReactions: []tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 1}, My: true,
			Reaction: &tg.ReactionEmoji{Emoticon: "👍"}}}}}
	wm := ConvertMessage(pc, m, users)
	if wm.ChatID != "u2" || wm.ID != "42" || wm.FromMe || wm.Sender.Phone != "+4915112345678" || wm.Sender.Name != "Anna Muster" ||
		wm.ReplyTo != "41" || wm.Media.Ref != "u2/42" || wm.Media.Name != "plan.pdf" || len(wm.Reactions) != 1 || !wm.Reactions[0].FromMe {
		t.Fatalf("%+v %+v", wm, wm.Media)
	}
	out := &tg.Message{ID: 43, Out: true, PeerID: &tg.PeerChannel{ChannelID: 8}, FromID: &tg.PeerUser{UserID: 1}, Date: 1700000001, Message: "x"}
	if wm := ConvertMessage(pc, out, nil); !wm.FromMe || wm.ChatID != "ch8" {
		t.Fatalf("%+v", wm)
	}
	if ConvertMessage(pc, &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 2}}, nil) != nil {
		t.Fatal("empty message kept")
	}
	chat := chatOf(pc, &tg.PeerUser{UserID: 2}, users)
	if chat.Kind != wire.ChatDirect || chat.Peer == nil || chat.Peer.Username != "anna" {
		t.Fatalf("%+v", chat)
	}
}

func TestSentID(t *testing.T) {
	id, _, err := sentID(&tg.UpdateShortSentMessage{ID: 5, Date: 1}, 0)
	if err != nil || id != 5 {
		t.Fatal(id, err)
	}
	id, _, err = sentID(&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 9, RandomID: 77},
		&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 9, Date: 1700000000}}}}, 77)
	if err != nil || id != 9 {
		t.Fatal(id, err)
	}
	if _, _, err := sentID(&tg.Updates{}, 1); err == nil {
		t.Fatal("no error")
	}
}
