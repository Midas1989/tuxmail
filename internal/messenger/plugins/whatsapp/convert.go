// SPDX-License-Identifier: AGPL-3.0-or-later

package whatsapp

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Chat ids are JIDs without device ("4917012345@s.whatsapp.net",
// "1234-5678@g.us"); message ids are WhatsApp message ids. Media refs are a
// base64 JSON of the download coordinates (mediaRef).

// mediaRef holds what Client.DownloadMediaWithPath needs.
type mediaRef struct {
	DirectPath string `json:"p"`
	EncSHA256  []byte `json:"e"`
	SHA256     []byte `json:"s"`
	MediaKey   []byte `json:"k"`
	Length     int    `json:"l"`
	Type       string `json:"t"` // whatsmeow.MediaType
	MMSType    string `json:"m,omitempty"`
}

func (r mediaRef) encode() string {
	b, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeMediaRef(s string) (mediaRef, error) {
	var r mediaRef
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return r, wire.Errorf(wire.CodeInvalid, "invalid media ref")
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, wire.Errorf(wire.CodeInvalid, "invalid media ref")
	}
	return r, nil
}

// phoneOf returns the E.164 number of a phone-number JID ("" for LIDs).
func phoneOf(j types.JID) string {
	if j.Server == types.DefaultUserServer && j.User != "" {
		return "+" + j.User
	}
	return ""
}

func contactOf(j, alt types.JID, name string) wire.Contact {
	pn := j
	if j.Server == types.HiddenUserServer && !alt.IsEmpty() {
		pn = alt
	}
	pn = pn.ToNonAD()
	return wire.Contact{ID: pn.String(), Name: name, Phone: phoneOf(pn)}
}

// chatJID returns the chat of a message with phone-number addressing for
// direct chats where WhatsApp reports a LID.
func chatJID(info types.MessageInfo) types.JID {
	chat := info.Chat.ToNonAD()
	if chat.Server == types.HiddenUserServer && !info.IsGroup {
		if info.IsFromMe && !info.RecipientAlt.IsEmpty() {
			return info.RecipientAlt.ToNonAD()
		}
		if !info.IsFromMe && !info.SenderAlt.IsEmpty() {
			return info.SenderAlt.ToNonAD()
		}
	}
	return chat
}

func contextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	}
	return nil
}

// textOf returns the text or caption of a message.
func textOf(m *waE2E.Message) string {
	switch {
	case m.GetConversation() != "":
		return m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetCaption()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetCaption()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetCaption()
	case m.GetLocationMessage() != nil:
		l := m.GetLocationMessage()
		return strings.TrimSpace(l.GetName() + " https://maps.google.com/?q=" + ftoa(l.GetDegreesLatitude()) + "," + ftoa(l.GetDegreesLongitude()))
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetDisplayName()
	}
	return ""
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// mediaOf returns the media of a message.
func mediaOf(m *waE2E.Message) *wire.Media {
	ref := func(dm whatsmeow.DownloadableMessage, t whatsmeow.MediaType, length uint64) string {
		return mediaRef{DirectPath: dm.GetDirectPath(), EncSHA256: dm.GetFileEncSHA256(), SHA256: dm.GetFileSHA256(), MediaKey: dm.GetMediaKey(),
			Length: int(length), Type: string(t), MMSType: mmsType(t)}.encode()
	}
	switch {
	case m.GetImageMessage() != nil:
		v := m.GetImageMessage()
		return &wire.Media{Ref: ref(v, whatsmeow.MediaImage, v.GetFileLength()), MIMEType: v.GetMimetype(), Name: "image" + ext(v.GetMimetype()),
			Size: int64(v.GetFileLength())}
	case m.GetVideoMessage() != nil:
		v := m.GetVideoMessage()
		return &wire.Media{Ref: ref(v, whatsmeow.MediaVideo, v.GetFileLength()), MIMEType: v.GetMimetype(), Name: "video" + ext(v.GetMimetype()),
			Size: int64(v.GetFileLength()), DurationSec: float64(v.GetSeconds())}
	case m.GetAudioMessage() != nil:
		v := m.GetAudioMessage()
		name := "audio" + ext(v.GetMimetype())
		if v.GetPTT() {
			name = "voice" + ext(v.GetMimetype())
		}
		return &wire.Media{Ref: ref(v, whatsmeow.MediaAudio, v.GetFileLength()), MIMEType: v.GetMimetype(), Name: name,
			Size: int64(v.GetFileLength()), DurationSec: float64(v.GetSeconds())}
	case m.GetDocumentMessage() != nil:
		v := m.GetDocumentMessage()
		name := v.GetFileName()
		if name == "" {
			name = v.GetTitle()
		}
		return &wire.Media{Ref: ref(v, whatsmeow.MediaDocument, v.GetFileLength()), MIMEType: v.GetMimetype(), Name: name, Size: int64(v.GetFileLength())}
	case m.GetStickerMessage() != nil:
		v := m.GetStickerMessage()
		return &wire.Media{Ref: ref(v, whatsmeow.MediaImage, v.GetFileLength()), MIMEType: v.GetMimetype(), Name: "sticker.webp",
			Size: int64(v.GetFileLength())}
	}
	return nil
}

func mmsType(t whatsmeow.MediaType) string {
	switch t {
	case whatsmeow.MediaImage:
		return "image"
	case whatsmeow.MediaVideo:
		return "video"
	case whatsmeow.MediaAudio:
		return "audio"
	}
	return "document"
}

func ext(mime string) string {
	mime, _, _ = strings.Cut(mime, ";")
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	}
	return ""
}

// Convert maps a whatsmeow message event to plugin events (chat first).
// self is the own phone-number JID.
func Convert(account string, self types.JID, ev *events.Message, chatName func(types.JID) string) []wire.Event {
	info, m := ev.Info, ev.Message
	if m == nil {
		return nil
	}
	chat := chatJID(info)
	sender := contactOf(info.Sender, info.SenderAlt, info.PushName)
	if info.IsFromMe {
		sender = wire.Contact{ID: self.ToNonAD().String(), Phone: phoneOf(self.ToNonAD())}
	}
	wc := wire.Chat{ID: chat.String(), Kind: wire.ChatDirect}
	if info.IsGroup {
		wc.Kind = wire.ChatGroup
		if chat.Server == types.NewsletterServer {
			wc.Kind = wire.ChatChannel
		}
		wc.Title = chatName(chat)
	} else {
		peer := wire.Contact{ID: chat.String(), Phone: phoneOf(chat)}
		if !info.IsFromMe {
			peer.Name = info.PushName
		}
		if n := chatName(chat); n != "" {
			peer.Name = n
		}
		wc.Peer, wc.Title = &peer, peer.Name
	}
	chatEv := wire.Event{Kind: wire.EventChat, Account: account, Chat: &wc}
	if r := m.GetReactionMessage(); r != nil {
		key := r.GetKey()
		return []wire.Event{{Kind: wire.EventReaction, Account: account, Reaction: &wire.ReactionEvent{ChatID: wc.ID, MessageID: key.GetID(),
			Sender: sender, FromMe: info.IsFromMe, Emoji: r.GetText()}}}
	}
	if pm := m.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return []wire.Event{{Kind: wire.EventDelete, Account: account, ChatID: wc.ID, MessageID: pm.GetKey().GetID()}}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited := pm.GetEditedMessage()
			if edited == nil {
				return nil
			}
			wm := wire.Message{ChatID: wc.ID, ID: pm.GetKey().GetID(), Sender: sender, FromMe: info.IsFromMe, Time: info.Timestamp,
				Text: textOf(edited), EditedAt: info.Timestamp}
			return []wire.Event{{Kind: wire.EventEdit, Account: account, Message: &wm}}
		}
		return nil
	}
	wm := wire.Message{ChatID: wc.ID, ID: info.ID, Sender: sender, FromMe: info.IsFromMe, Time: info.Timestamp, Text: textOf(m), Media: mediaOf(m)}
	if ci := contextInfo(m); ci != nil && ci.GetStanzaID() != "" {
		wm.ReplyTo = ci.GetStanzaID()
	}
	if ev.IsEdit {
		wm.EditedAt = info.Timestamp
	}
	if wm.Text == "" && wm.Media == nil {
		return []wire.Event{chatEv}
	}
	kind := wire.EventMessage
	if ev.IsEdit {
		kind = wire.EventEdit
	}
	return []wire.Event{chatEv, {Kind: kind, Account: account, Message: &wm}}
}

// ConvertReceipt maps a receipt.
func ConvertReceipt(account string, r *events.Receipt) *wire.Event {
	kind := ""
	switch r.Type {
	case types.ReceiptTypeDelivered:
		kind = wire.ReceiptDelivered
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		kind = wire.ReceiptRead
	default:
		return nil
	}
	chat := r.Chat.ToNonAD()
	if chat.Server == types.HiddenUserServer && !r.IsGroup && !r.SenderAlt.IsEmpty() {
		chat = r.SenderAlt.ToNonAD()
	}
	ids := make([]string, len(r.MessageIDs))
	copy(ids, r.MessageIDs)
	return &wire.Event{Kind: wire.EventReceipt, Account: account, Receipt: &wire.Receipt{ChatID: chat.String(), MessageIDs: ids, Kind: kind,
		Time: r.Timestamp, FromMe: r.IsFromMe}}
}

// buildMessage creates the message to send: text (with reply context) or
// an uploaded media message.
func buildMessage(text string, reply *waE2E.ContextInfo, up *whatsmeow.UploadResponse, out *wire.OutMedia) *waE2E.Message {
	str := func(s string) *string { return &s }
	if up == nil {
		if reply == nil {
			return &waE2E.Message{Conversation: str(text)}
		}
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: str(text), ContextInfo: reply}}
	}
	l := up.FileLength
	mime := out.MIMEType
	switch {
	case strings.HasPrefix(mime, "image/"):
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: str(text), Mimetype: str(mime), URL: str(up.URL), DirectPath: str(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &l, ContextInfo: reply}}
	case strings.HasPrefix(mime, "video/"):
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: str(text), Mimetype: str(mime), URL: str(up.URL), DirectPath: str(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &l, ContextInfo: reply}}
	case strings.HasPrefix(mime, "audio/"):
		sec := uint32(out.DurationSec)
		ptt := out.Voice
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: str(mime), URL: str(up.URL), DirectPath: str(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &l, Seconds: &sec, PTT: &ptt, ContextInfo: reply}}
	}
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: str(text), Mimetype: str(mime), FileName: str(out.Name), Title: str(out.Name),
		URL: str(up.URL), DirectPath: str(up.DirectPath), MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: &l, ContextInfo: reply}}
}

func uploadType(mime string) whatsmeow.MediaType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return whatsmeow.MediaImage
	case strings.HasPrefix(mime, "video/"):
		return whatsmeow.MediaVideo
	case strings.HasPrefix(mime, "audio/"):
		return whatsmeow.MediaAudio
	}
	return whatsmeow.MediaDocument
}

// ConvertHistory maps a history sync blob (sent by the phone after linking
// and later in chunks) to chat and message events, so that the linked
// device shows the same chats as the phone. Messages are marked
// Historical; the unread count of each conversation becomes a read marker
// (an own read receipt for the newest message before the unread ones).
func ConvertHistory(account string, self types.JID, hs *events.HistorySync, chatName func(types.JID) string,
	parse func(types.JID, *waWeb.WebMessageInfo) (*events.Message, error)) []wire.Event {
	if hs == nil || hs.Data == nil {
		return nil
	}
	var out []wire.Event
	for _, conv := range hs.Data.GetConversations() {
		jid, err := types.ParseJID(conv.GetID())
		if err != nil || jid.Server == types.BroadcastServer {
			continue
		}
		name := conv.GetName()
		names := func(j types.JID) string {
			if j == jid && name != "" {
				return name
			}
			return chatName(j)
		}
		var msgs []*wire.Message // newest first, as delivered
		var chat *wire.Chat
		for _, hm := range conv.GetMessages() {
			wm := hm.GetMessage()
			if wm == nil {
				continue
			}
			ev, err := parse(jid, wm)
			if err != nil {
				continue
			}
			for _, e := range Convert(account, self, ev, names) {
				switch {
				case e.Chat != nil:
					if chat == nil {
						chat = e.Chat
					}
				case e.Message != nil && e.Kind == wire.EventMessage:
					e.Message.Historical = true
					msgs = append(msgs, e.Message)
				}
			}
		}
		if chat == nil {
			c := wire.Chat{ID: jid.ToNonAD().String(), Kind: wire.ChatDirect, Title: name}
			if jid.Server == types.GroupServer {
				c.Kind = wire.ChatGroup
			} else {
				c.Peer = &wire.Contact{ID: c.ID, Name: name, Phone: phoneOf(jid.ToNonAD())}
			}
			chat = &c
		}
		chat.Unread = int(conv.GetUnreadCount())
		out = append(out, wire.Event{Kind: wire.EventChat, Account: account, Chat: chat})
		for i := len(msgs) - 1; i >= 0; i-- { // oldest first
			out = append(out, wire.Event{Kind: wire.EventMessage, Account: account, Message: msgs[i]})
		}
		// Messages beyond the unread count were read on the phone.
		var incoming []*wire.Message
		for _, m := range msgs {
			if !m.FromMe {
				incoming = append(incoming, m)
			}
		}
		if n := chat.Unread; n < len(incoming) {
			read := incoming[n]
			out = append(out, wire.Event{Kind: wire.EventReceipt, Account: account, Receipt: &wire.Receipt{ChatID: chat.ID,
				MessageIDs: []string{read.ID}, Kind: wire.ReceiptRead, Time: read.Time, FromMe: true}})
		}
	}
	return out
}
