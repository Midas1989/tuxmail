// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
)

// MaxMediaSize bounds downloads and uploads of media.
const MaxMediaSize = 200 << 20

// OutgoingMedia is an attachment to send.
type OutgoingMedia struct {
	Open        func() (io.ReadCloser, error)
	MIMEType    string
	Name        string
	DurationSec float64
	Voice       bool
}

// SendInput is a message to send. ChatID (store id) addresses an existing
// chat; otherwise AccountID and To address a direct chat.
type SendInput struct {
	ChatID    int64
	AccountID int64
	To        *wire.Contact
	Text      string
	// ReplyTo is the store id of the chat message being answered.
	ReplyTo int64
	Media   []OutgoingMedia
}

// SendOutput identifies the stored message (the last one when several
// attachments were sent as separate messages).
type SendOutput struct {
	ChatID         int64
	ChatMessageID  int64
	ConversationID int64
}

func (s *Service) chatAccount(ctx context.Context, chatID int64) (store.Chat, account, error) {
	c, err := s.st.GetChat(ctx, chatID)
	if err != nil {
		return c, account{}, err
	}
	ma, err := s.st.GetMessengerAccount(ctx, c.AccountID)
	if err != nil {
		return c, account{}, ErrNotMessenger
	}
	acc, err := s.withAccount(ctx, ma)
	return c, acc, err
}

func (s *Service) messengerAccount(ctx context.Context, accountID int64) (account, error) {
	ma, err := s.st.GetMessengerAccount(ctx, accountID)
	if err != nil {
		return account{}, ErrNotMessenger
	}
	return s.withAccount(ctx, ma)
}

// IsMessengerAccount reports whether the account belongs to a plugin.
func (s *Service) IsMessengerAccount(ctx context.Context, accountID int64) bool {
	_, err := s.st.GetMessengerAccount(ctx, accountID)
	return err == nil
}

// Send sends a message (text and/or media).
func (s *Service) Send(ctx context.Context, in SendInput) (SendOutput, error) {
	var (
		c   store.Chat
		acc account
		err error
	)
	if in.ChatID != 0 {
		c, acc, err = s.chatAccount(ctx, in.ChatID)
	} else {
		if in.To == nil {
			return SendOutput{}, ErrNoChat
		}
		acc, err = s.messengerAccount(ctx, in.AccountID)
	}
	if err != nil {
		return SendOutput{}, err
	}
	if err := s.requireEnabled(ctx, acc.Plugin); err != nil {
		return SendOutput{}, err
	}
	if c.ID != 0 {
		// Answering reads the chat: send the receipts before the marker moves.
		if err := s.MarkRead(ctx, c.ID); err != nil {
			s.log.Warn("messenger: read receipts", "chat", c.ID, "err", err)
		}
	}
	req := wire.SendRequest{Account: acc.PluginAccount, ChatID: c.RemoteID, To: in.To}
	if in.ReplyTo != 0 {
		if m, err := s.st.GetChatMessage(ctx, in.ReplyTo); err == nil && m.ChatID == c.ID {
			req.ReplyTo = m.RemoteID
		}
	}
	var out SendOutput
	sendOne := func(r wire.SendRequest, media *OutgoingMedia) error {
		var res wire.SendResult
		if err := s.host.Call(ctx, acc.Plugin, wire.MethodSend, r, &res); err != nil {
			return err
		}
		if c.ID == 0 {
			chatRemote := res.ChatID
			if chatRemote == "" {
				return errors.New("messenger: plugin did not report the chat of the sent message")
			}
			nc, err := s.ensureChat(ctx, acc, wire.Chat{ID: chatRemote, Kind: wire.ChatDirect, Peer: in.To})
			if err != nil {
				return err
			}
			c = nc
			req.ChatID = chatRemote
		}
		if res.Time.IsZero() {
			res.Time = s.now()
		}
		wm := wire.Message{ChatID: c.RemoteID, ID: res.ID, FromMe: true, Time: res.Time, Text: r.Text, ReplyTo: r.ReplyTo, Status: wire.StatusSent}
		if media != nil && r.Media != nil {
			wm.Media = &wire.Media{MIMEType: media.MIMEType, Name: media.Name}
		}
		id, _, err := s.ingestMessage(ctx, acc, c, wm)
		if err != nil {
			return err
		}
		if media != nil {
			// Keep the sent file: no need to download it again.
			if rc, err := media.Open(); err == nil && s.blobs != nil {
				if h, err := s.blobs.Put(io.LimitReader(rc, MaxMediaSize)); err == nil {
					_ = s.st.SetChatMessageMedia(ctx, id, h, media.MIMEType, media.Name)
				}
				rc.Close()
			}
		}
		out = SendOutput{ChatID: c.ID, ChatMessageID: id, ConversationID: c.ConversationID}
		s.notify.ConversationChanged(c.ConversationID, chatItemID(id))
		return nil
	}
	if len(in.Media) == 0 {
		req.Text = in.Text
		if strings.TrimSpace(req.Text) == "" {
			return SendOutput{}, errors.New("messenger: empty message")
		}
		return out, sendOne(req, nil)
	}
	for i, m := range in.Media {
		path, err := s.stageMedia(acc, m)
		if err != nil {
			return out, err
		}
		r := req
		r.Media = &wire.OutMedia{Path: path, MIMEType: m.MIMEType, Name: m.Name, DurationSec: m.DurationSec, Voice: m.Voice}
		if i == 0 {
			r.Text = in.Text // caption
		} else {
			r.ReplyTo = ""
		}
		err = sendOne(r, &in.Media[i])
		os.Remove(path)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// stageMedia copies an attachment into the plugin's account directory.
func (s *Service) stageMedia(acc account, m OutgoingMedia) (string, error) {
	dir := filepath.Join(s.host.PluginDataDir(acc.Plugin), acc.PluginAccount, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	rc, err := m.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	name := unsafeName.ReplaceAllString(m.Name, "_")
	if name == "" || strings.HasPrefix(name, ".") {
		name = "file" + name
	}
	f, err := os.CreateTemp(dir, "*-"+name)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(rc, MaxMediaSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxMediaSize {
		err = fmt.Errorf("messenger: attachment larger than %d MB", MaxMediaSize>>20)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// React sets (emoji != "") or removes the user's reaction on a chat
// message, remotely and in the store.
func (s *Service) React(ctx context.Context, chatMessageID int64, emoji string) (store.ChatMessage, error) {
	m, err := s.st.GetChatMessage(ctx, chatMessageID)
	if err != nil {
		return m, err
	}
	c, acc, err := s.chatAccount(ctx, m.ChatID)
	if err != nil {
		return m, err
	}
	if err := s.requireEnabled(ctx, acc.Plugin); err != nil {
		return m, err
	}
	var current string
	for _, r := range m.Reactions {
		if r.Sender == ownSender {
			current = r.Emoji
		}
	}
	remove := emoji == "" || emoji == current
	req := wire.ReactRequest{Account: acc.PluginAccount, ChatID: c.RemoteID, MessageID: m.RemoteID, Emoji: emoji, Remove: remove, FromMe: m.FromMe}
	if remove {
		req.Emoji = current
	}
	if !m.FromMe {
		req.SenderID = s.remoteSender(ctx, acc, c, m)
	}
	if err := s.host.Call(ctx, acc.Plugin, wire.MethodReact, req, nil); err != nil {
		return m, err
	}
	next := emoji
	if remove {
		next = ""
	}
	m.Reactions = setReaction(m.Reactions, ownSender, next)
	if err := s.st.SetChatMessageReactions(ctx, m.ID, m.Reactions); err != nil {
		return m, err
	}
	s.notify.ConversationChanged(c.ConversationID, chatItemID(m.ID))
	return m, nil
}

// remoteSender returns the network id of the sender of an incoming message.
func (s *Service) remoteSender(ctx context.Context, acc account, c store.Chat, m store.ChatMessage) string {
	if m.SenderPersonID == 0 {
		return ""
	}
	hs, err := s.st.Handles(ctx, m.SenderPersonID)
	if err != nil {
		return ""
	}
	ch := s.chatChannel(acc, c)
	for _, h := range hs {
		if h.Kind == ch {
			return h.Value
		}
	}
	return ""
}

// Delete deletes a chat message; own messages are deleted for everyone.
func (s *Service) Delete(ctx context.Context, chatMessageID int64) error {
	m, err := s.st.GetChatMessage(ctx, chatMessageID)
	if err != nil {
		return err
	}
	c, acc, err := s.chatAccount(ctx, m.ChatID)
	if err != nil {
		return err
	}
	if err := s.requireEnabled(ctx, acc.Plugin); err == nil {
		req := wire.DeleteRequest{Account: acc.PluginAccount, ChatID: c.RemoteID, MessageID: m.RemoteID, ForEveryone: m.FromMe, FromMe: m.FromMe}
		if !m.FromMe {
			req.SenderID = s.remoteSender(ctx, acc, c, m)
		}
		err := s.host.Call(ctx, acc.Plugin, wire.MethodDelete, req, nil)
		if err != nil && wire.ErrorCode(err) != wire.CodeNotSupported {
			return err
		}
	}
	return s.st.MarkChatMessageDeleted(ctx, m.ID)
}

// Edit replaces the text of an own message.
func (s *Service) Edit(ctx context.Context, chatMessageID int64, text string) error {
	m, err := s.st.GetChatMessage(ctx, chatMessageID)
	if err != nil {
		return err
	}
	if !m.FromMe {
		return errors.New("messenger: only own messages can be edited")
	}
	c, acc, err := s.chatAccount(ctx, m.ChatID)
	if err != nil {
		return err
	}
	if err := s.host.Call(ctx, acc.Plugin, wire.MethodEdit, wire.EditRequest{Account: acc.PluginAccount, ChatID: c.RemoteID,
		MessageID: m.RemoteID, Text: text}, nil); err != nil {
		return err
	}
	if err := s.st.EditChatMessage(ctx, m.ID, text, s.now()); err != nil {
		return err
	}
	_ = s.st.IndexChatMessage(ctx, m.ID, store.SearchDoc{Body: text, Attachments: m.MediaName})
	s.notify.ConversationChanged(c.ConversationID, chatItemID(m.ID))
	return nil
}

// MarkRead sends read receipts for the unread incoming messages of a chat
// (call before moving the local read marker).
func (s *Service) MarkRead(ctx context.Context, chatID int64) error {
	c, acc, err := s.chatAccount(ctx, chatID)
	if err != nil {
		return err
	}
	if s.requireEnabled(ctx, acc.Plugin) != nil {
		return nil
	}
	ids, err := s.st.UnreadIncomingRemoteIDs(ctx, c.ID, 200)
	if err != nil || len(ids) == 0 {
		return err
	}
	last, err := s.st.LatestIncomingChatMessage(ctx, c.ID)
	if err != nil {
		return ignoreNotFound(err)
	}
	req := wire.MarkReadRequest{Account: acc.PluginAccount, ChatID: c.RemoteID, MessageIDs: ids, SenderID: s.remoteSender(ctx, acc, c, last),
		Time: last.SentAt}
	err = s.host.Call(ctx, acc.Plugin, wire.MethodMarkRead, req, nil)
	if wire.ErrorCode(err) == wire.CodeNotSupported {
		return nil
	}
	return err
}

// Typing reports the user's typing state in a chat (best effort).
func (s *Service) Typing(ctx context.Context, chatID int64, active bool) error {
	c, acc, err := s.chatAccount(ctx, chatID)
	if err != nil {
		return err
	}
	if !s.host.Running(acc.Plugin) {
		return nil
	}
	return s.host.Call(ctx, acc.Plugin, wire.MethodTyping, wire.TypingRequest{Account: acc.PluginAccount, ChatID: c.RemoteID, Active: active}, nil)
}

// MediaContent is downloaded media.
type MediaContent struct {
	Data     []byte
	MIMEType string
	Name     string
}

// Media returns the media of a chat message, downloading it through the
// plugin into the blob store on first access.
func (s *Service) Media(ctx context.Context, chatMessageID int64) (MediaContent, error) {
	m, err := s.st.GetChatMessage(ctx, chatMessageID)
	if err != nil {
		return MediaContent{}, err
	}
	out := MediaContent{MIMEType: m.MediaMime, Name: m.MediaName}
	if s.blobs == nil {
		return out, errors.New("messenger: no blob store")
	}
	if m.MediaBlob != "" {
		rc, err := s.blobs.Open(m.MediaBlob)
		if err == nil {
			defer rc.Close()
			out.Data, err = io.ReadAll(io.LimitReader(rc, MaxMediaSize))
			return out, err
		}
	}
	ref, _, err := s.st.ChatMedia(ctx, m.ID)
	if err != nil {
		return out, err
	}
	c, acc, err := s.chatAccount(ctx, m.ChatID)
	if err != nil {
		return out, err
	}
	dir := filepath.Join(s.host.PluginDataDir(acc.Plugin), acc.PluginAccount, "inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, err
	}
	path := filepath.Join(dir, randomID(8))
	defer os.Remove(path)
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var res wire.MediaResult
	if err := s.host.Call(dctx, acc.Plugin, wire.MethodDownloadMedia, wire.MediaRequest{Account: acc.PluginAccount, ChatID: c.RemoteID,
		Ref: ref, Path: path}, &res); err != nil {
		return out, err
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	out.Data, err = io.ReadAll(io.LimitReader(f, MaxMediaSize))
	if err != nil {
		return out, err
	}
	if res.MIMEType != "" && out.MIMEType == "" {
		out.MIMEType = res.MIMEType
	}
	if res.Name != "" && (out.Name == "" || out.Name == defaultMediaName(out.MIMEType)) {
		out.Name = res.Name
	}
	h, err := s.blobs.Put(bytes.NewReader(out.Data))
	if err != nil {
		return out, err
	}
	if err := s.st.SetChatMessageMedia(ctx, m.ID, h, out.MIMEType, out.Name); err != nil {
		return out, err
	}
	return out, nil
}

// SendPassword implements send.MessengerSender: the share password goes to
// the recipient's direct chat on the messenger account (KONZEPT 12.2).
func (s *Service) SendPassword(ctx context.Context, accountID int64, to domain.Address, text string) error {
	acc, err := s.messengerAccount(ctx, accountID)
	if err != nil {
		return err
	}
	pid, err := s.personByAddress(ctx, strings.ToLower(strings.TrimSpace(to.Email)))
	if err != nil {
		return err
	}
	if pid == 0 {
		return fmt.Errorf("messenger: %s has no %s contact", to.Email, acc.Channel)
	}
	if chats, err := s.st.DirectChatsOfPerson(ctx, pid); err == nil {
		for _, c := range chats {
			if c.AccountID == accountID {
				_, err := s.Send(ctx, SendInput{ChatID: c.ID, Text: text})
				return err
			}
		}
	}
	hs, err := s.st.Handles(ctx, pid)
	if err != nil {
		return err
	}
	var target *wire.Contact
	for _, h := range hs {
		switch {
		case h.Kind == acc.Channel:
			target = &wire.Contact{ID: h.Value, Phone: E164(h.Value)}
		case h.Kind == store.HandlePhone && target == nil && acc.Channel != store.HandleMatrix:
			target = &wire.Contact{Phone: h.Value}
		}
	}
	if target == nil {
		return fmt.Errorf("messenger: %s has no %s contact", to.Email, acc.Channel)
	}
	_, err = s.Send(ctx, SendInput{AccountID: accountID, To: target, Text: text})
	return err
}
