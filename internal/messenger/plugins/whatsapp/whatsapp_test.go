// SPDX-License-Identifier: AGPL-3.0-or-later

package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// TestStoreWithoutCGO opens the whatsmeow sqlstore on modernc.org/sqlite.
func TestStoreWithoutCGO(t *testing.T) {
	db, dev, err := openDevice(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if dev == nil || dev.ID != nil {
		t.Fatalf("new device: %+v", dev)
	}
}

func s(v string) *string { return &v }

func TestConvert(t *testing.T) {
	self := types.NewJID("4917011111111", types.DefaultUserServer)
	maria := types.NewJID("4915112345678", types.DefaultUserServer)
	group := types.NewJID("12345-678", types.GroupServer)
	names := func(j types.JID) string {
		if j == group {
			return "Familie"
		}
		return ""
	}
	at := time.Unix(1700000000, 0)
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: maria, Sender: maria}, ID: "ABC", PushName: "Maria", Timestamp: at}
	l := uint64(3)
	evs := Convert("a1", self, &events.Message{Info: info, Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: s("Foto"),
		Mimetype: s("image/jpeg"), DirectPath: s("/v/t62/x"), MediaKey: []byte{1}, FileLength: &l,
		ContextInfo: &waE2E.ContextInfo{StanzaID: s("PREV")}}}}, names)
	if len(evs) != 2 || evs[0].Chat.Peer.Phone != "+4915112345678" || evs[0].Chat.Peer.Name != "Maria" {
		t.Fatalf("%+v", evs)
	}
	m := evs[1].Message
	if m.ID != "ABC" || m.Text != "Foto" || m.ReplyTo != "PREV" || m.Media.Name != "image.jpg" || m.Sender.Phone != "+4915112345678" {
		t.Fatalf("%+v", m)
	}
	r, err := decodeMediaRef(m.Media.Ref)
	if err != nil || r.DirectPath != "/v/t62/x" || r.Type != string(whatsmeow.MediaImage) || r.Length != 3 {
		t.Fatal(r, err)
	}
	// LID addressing: the phone number comes from SenderAlt.
	lid := types.NewJID("99999", types.HiddenUserServer)
	info2 := types.MessageInfo{MessageSource: types.MessageSource{Chat: lid, Sender: lid, SenderAlt: maria}, ID: "L1", Timestamp: at}
	evs = Convert("a1", self, &events.Message{Info: info2, Message: &waE2E.Message{Conversation: s("hi")}}, names)
	if evs[0].Chat.ID != maria.String() || evs[1].Message.Sender.ID != maria.String() {
		t.Fatalf("%+v %+v", evs[0].Chat, evs[1].Message.Sender)
	}
	// Group reaction, revoke and edit.
	ginfo := types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: maria, IsGroup: true}, ID: "R1", Timestamp: at}
	evs = Convert("a1", self, &events.Message{Info: ginfo, Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Key: &waCommon.MessageKey{ID: s("ABC")}, Text: s("❤")}}}, names)
	if len(evs) != 1 || evs[0].Reaction.MessageID != "ABC" || evs[0].Reaction.ChatID != group.String() {
		t.Fatalf("%+v", evs)
	}
	revoke := waE2E.ProtocolMessage_REVOKE
	evs = Convert("a1", self, &events.Message{Info: ginfo, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: &revoke,
		Key: &waCommon.MessageKey{ID: s("ABC")}}}}, names)
	if len(evs) != 1 || evs[0].Kind != wire.EventDelete || evs[0].MessageID != "ABC" {
		t.Fatalf("%+v", evs)
	}
	edit := waE2E.ProtocolMessage_MESSAGE_EDIT
	evs = Convert("a1", self, &events.Message{Info: ginfo, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: &edit,
		Key: &waCommon.MessageKey{ID: s("ABC")}, EditedMessage: &waE2E.Message{Conversation: s("neu")}}}}, names)
	if len(evs) != 1 || evs[0].Kind != wire.EventEdit || evs[0].Message.Text != "neu" {
		t.Fatalf("%+v", evs)
	}
	ginfo.IsFromMe, ginfo.ID = true, "OWN"
	evs = Convert("a1", self, &events.Message{Info: ginfo, Message: &waE2E.Message{Conversation: s("von mir")}}, names)
	if evs[0].Chat.Title != "Familie" || evs[0].Chat.Kind != wire.ChatGroup || !evs[1].Message.FromMe || evs[1].Message.Sender.ID != self.String() {
		t.Fatalf("%+v", evs)
	}
	rc := ConvertReceipt("a1", &events.Receipt{MessageSource: types.MessageSource{Chat: maria}, MessageIDs: []string{"X"}, Type: types.ReceiptTypeRead})
	if rc == nil || rc.Receipt.Kind != wire.ReceiptRead || rc.Receipt.ChatID != maria.String() {
		t.Fatalf("%+v", rc)
	}
	if ConvertReceipt("a1", &events.Receipt{Type: types.ReceiptTypeRetry}) != nil {
		t.Fatal("retry receipt converted")
	}
}

// TestHistorySync: a linked device receives the phone's chats.
func TestHistorySync(t *testing.T) {
	self := types.NewJID("4917011111111", types.DefaultUserServer)
	maria := types.NewJID("4915112345678", types.DefaultUserServer)
	web := func(id string, fromMe bool, ts uint64, text string) *waHistorySync.HistorySyncMsg {
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: s(id), FromMe: &fromMe},
			MessageTimestamp: &ts, Message: &waE2E.Message{Conversation: s(text)}}}
	}
	unread := uint32(1)
	hs := &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: s(maria.String()), Name: s("Maria Sommer"), UnreadCount: &unread,
		Messages: []*waHistorySync.HistorySyncMsg{web("m3", false, 300, "neu"), web("m2", true, 200, "antwort"), web("m1", false, 100, "alt")},
	}}}}
	parse := func(chat types.JID, wm *waWeb.WebMessageInfo) (*events.Message, error) {
		k := wm.GetKey()
		info := types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: maria, IsFromMe: k.GetFromMe()}, ID: k.GetID(),
			Timestamp: time.Unix(int64(wm.GetMessageTimestamp()), 0)}
		return &events.Message{Info: info, Message: wm.GetMessage()}, nil
	}
	evs := ConvertHistory("a1", self, hs, func(types.JID) string { return "" }, parse)
	if len(evs) != 5 || evs[0].Chat.Title != "Maria Sommer" || evs[0].Chat.Unread != 1 {
		t.Fatalf("%+v", evs)
	}
	if evs[1].Message.ID != "m1" || evs[3].Message.ID != "m3" || !evs[1].Message.Historical || !evs[2].Message.FromMe {
		t.Fatalf("order: %+v %+v", evs[1].Message, evs[3].Message)
	}
	if r := evs[4].Receipt; r == nil || !r.FromMe || r.MessageIDs[0] != "m1" {
		t.Fatalf("read marker: %+v", evs[4])
	}
}

func TestBuildMessage(t *testing.T) {
	m := buildMessage("hi", nil, nil, nil)
	if m.GetConversation() != "hi" {
		t.Fatal(m)
	}
	m = buildMessage("re", &waE2E.ContextInfo{StanzaID: s("A")}, nil, nil)
	if m.GetExtendedTextMessage().GetContextInfo().GetStanzaID() != "A" {
		t.Fatal(m)
	}
	up := &whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", FileLength: 9}
	m = buildMessage("", nil, up, &wire.OutMedia{MIMEType: "application/pdf", Name: "plan.pdf"})
	if m.GetDocumentMessage().GetFileName() != "plan.pdf" || m.GetDocumentMessage().GetFileLength() != 9 {
		t.Fatal(m)
	}
	m = buildMessage("", nil, up, &wire.OutMedia{MIMEType: "audio/ogg", Voice: true, DurationSec: 4})
	if !m.GetAudioMessage().GetPTT() || m.GetAudioMessage().GetSeconds() != 4 {
		t.Fatal(m)
	}
	if uploadType("image/png") != whatsmeow.MediaImage || uploadType("text/plain") != whatsmeow.MediaDocument {
		t.Fatal("upload type")
	}
}
