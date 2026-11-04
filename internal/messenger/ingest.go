// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/store"
)

// account is a messenger account resolved for an event.
type account struct {
	store.MessengerAccount
	Channel string
	Name    string
}

func (s *Service) lookupAccount(ctx context.Context, plugin, pluginAccount string) (account, error) {
	ma, err := s.st.MessengerAccountByPlugin(ctx, plugin, pluginAccount)
	if err != nil {
		return account{}, err
	}
	return s.withAccount(ctx, ma)
}

func (s *Service) withAccount(ctx context.Context, ma store.MessengerAccount) (account, error) {
	a := account{MessengerAccount: ma, Channel: SpecFor(ma.Plugin).Channel}
	if da, err := s.st.GetAccount(ctx, ma.AccountID); err == nil {
		a.Name = da.DisplayName
		if ch := da.Extra["channel"]; ch != "" {
			a.Channel = ch
		}
	}
	return a, nil
}

// handleEvent is the host's event callback (one goroutine per plugin, in
// order).
func (s *Service) handleEvent(plugin string, ev wire.Event) {
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	if ev.Kind == wire.EventPairing {
		if ev.Pairing != nil {
			s.handlePairing(ctx, plugin, ev.Pairing)
		}
		return
	}
	acc, err := s.lookupAccount(ctx, plugin, ev.Account)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("messenger: event for unknown account", "plugin", plugin, "err", err)
		}
		return
	}
	if err := s.apply(ctx, acc, ev); err != nil {
		s.log.Warn("messenger: apply event", "plugin", plugin, "kind", ev.Kind, "err", err)
	}
}

func (s *Service) apply(ctx context.Context, acc account, ev wire.Event) error {
	switch ev.Kind {
	case wire.EventStatus:
		if ev.Status == nil {
			return nil
		}
		s.mu.Lock()
		s.states[acc.AccountID] = *ev.Status
		s.mu.Unlock()
		if err := s.st.SetMessengerAccountState(ctx, acc.AccountID, ev.Status.State, ev.Status.Error); err != nil {
			return err
		}
		s.notify.AccountChanged(acc.AccountID)
		if ev.Status.UpdateHint != "" {
			s.notify.PluginChanged(acc.Plugin)
		}
	case wire.EventChat:
		if ev.Chat == nil {
			return nil
		}
		c, err := s.ensureChat(ctx, acc, *ev.Chat)
		if err != nil {
			return err
		}
		s.notify.ConversationChanged(c.ConversationID, "")
	case wire.EventMessage, wire.EventEdit:
		if ev.Message == nil {
			return nil
		}
		c, err := s.chatFor(ctx, acc, ev.Message.ChatID)
		if err != nil {
			return err
		}
		id, created, err := s.ingestMessage(ctx, acc, c, *ev.Message)
		if err != nil {
			return err
		}
		if ev.Kind == wire.EventEdit && !created {
			if err := s.st.EditChatMessage(ctx, id, ev.Message.Text, ev.Message.EditedAt); err != nil {
				return err
			}
			_ = s.st.IndexChatMessage(ctx, id, searchDoc(*ev.Message))
		}
		if created && !ev.Message.FromMe && !ev.Message.Service && !ev.Message.Historical {
			s.publishReceived(acc, c, *ev.Message)
		}
		s.notify.ConversationChanged(c.ConversationID, chatItemID(id))
	case wire.EventDelete:
		c, err := s.st.ChatByRemoteID(ctx, acc.AccountID, ev.ChatID)
		if err != nil {
			return ignoreNotFound(err)
		}
		m, err := s.st.ChatMessageByRemoteID(ctx, c.ID, ev.MessageID)
		if err != nil {
			return ignoreNotFound(err)
		}
		if err := s.st.MarkChatMessageDeleted(ctx, m.ID); err != nil {
			return err
		}
		s.notify.ConversationChanged(c.ConversationID, chatItemID(m.ID))
	case wire.EventReaction:
		if ev.Reaction == nil {
			return nil
		}
		return s.applyReaction(ctx, acc, *ev.Reaction)
	case wire.EventReceipt:
		if ev.Receipt == nil {
			return nil
		}
		return s.applyReceipt(ctx, acc, *ev.Receipt)
	case wire.EventTyping:
		if ev.Typing == nil {
			return nil
		}
		c, err := s.st.ChatByRemoteID(ctx, acc.AccountID, ev.Typing.ChatID)
		if err != nil {
			return ignoreNotFound(err)
		}
		pid, _ := s.resolvePerson(ctx, s.chatChannel(acc, c), ev.Typing.Sender)
		s.notify.Typing(TypingInfo{ConversationID: c.ConversationID, ChatID: c.ID, AccountID: acc.AccountID, PersonID: pid,
			Name: contactName(ev.Typing.Sender), Active: ev.Typing.Active})
	}
	return nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func chatItemID(id int64) string { return fmt.Sprintf("c:%d", id) }

func (s *Service) chatChannel(acc account, c store.Chat) string {
	if c.Channel != "" {
		return c.Channel
	}
	return acc.Channel
}

func wireChannel(acc account, c wire.Chat) string {
	switch {
	case c.Channel != "":
		return c.Channel
	case c.Bridge != nil && c.Bridge.Network != "":
		return c.Bridge.Network
	}
	return acc.Channel
}

// ensureChat creates or updates a chat with its conversation and
// counterpart person.
func (s *Service) ensureChat(ctx context.Context, acc account, wc wire.Chat) (store.Chat, error) {
	if wc.ID == "" {
		return store.Chat{}, errors.New("chat id missing")
	}
	kind := wc.Kind
	switch kind {
	case wire.ChatDirect, wire.ChatGroup, wire.ChatChannel:
	default:
		kind = wire.ChatGroup
	}
	channel := wireChannel(acc, wc)
	title := strings.TrimSpace(wc.Title)
	if title == "" && wc.Peer != nil {
		title = contactName(*wc.Peer)
	}
	existing, err := s.st.ChatByRemoteID(ctx, acc.AccountID, wc.ID)
	conv := existing.ConversationID
	switch {
	case errors.Is(err, store.ErrNotFound):
		if conv, err = s.st.CreateConversation(ctx, store.Conversation{Kind: store.ConvChat, Title: title}); err != nil {
			return store.Chat{}, err
		}
	case err != nil:
		return store.Chat{}, err
	default:
		if title == "" {
			title = existing.Title
		}
	}
	id, err := s.st.UpsertChat(ctx, store.Chat{AccountID: acc.AccountID, RemoteID: wc.ID, Kind: kind, Title: title, ConversationID: conv,
		Channel: channel})
	if err != nil {
		return store.Chat{}, err
	}
	if conv == 0 {
		conv, err = s.st.CreateConversation(ctx, store.Conversation{Kind: store.ConvChat, Title: title})
		if err != nil {
			return store.Chat{}, err
		}
		if err := s.st.AssignChatConversation(ctx, id, conv); err != nil {
			return store.Chat{}, err
		}
	}
	if kind == wire.ChatDirect && wc.Peer != nil {
		pid, err := s.resolvePerson(ctx, channel, *wc.Peer)
		if err != nil {
			return store.Chat{}, err
		}
		if pid != 0 {
			if err := s.st.SetChatCounterpart(ctx, id, pid); err != nil {
				return store.Chat{}, err
			}
			p, _ := s.st.GetPerson(ctx, pid)
			if _, err := s.st.PersonConversation(ctx, pid, p.DisplayName); err != nil {
				return store.Chat{}, err
			}
		}
	} else if kind != wire.ChatDirect {
		_ = s.st.SetChatCounterpart(ctx, id, 0)
	}
	return s.st.GetChat(ctx, id)
}

// chatFor returns the chat of a message, creating a placeholder if the
// plugin did not announce it.
func (s *Service) chatFor(ctx context.Context, acc account, remoteID string) (store.Chat, error) {
	c, err := s.st.ChatByRemoteID(ctx, acc.AccountID, remoteID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return c, err
	}
	return s.ensureChat(ctx, acc, wire.Chat{ID: remoteID, Kind: wire.ChatDirect})
}

func searchDoc(m wire.Message) store.SearchDoc {
	d := store.SearchDoc{Body: m.Text}
	if m.Media != nil {
		d.Attachments = m.Media.Name
	}
	return d
}

func mapStatus(s string) string {
	switch s {
	case wire.StatusSent, wire.StatusDelivered, wire.StatusRead, wire.StatusFailed:
		return s
	}
	return ""
}

// ingestMessage stores a message and its sender.
func (s *Service) ingestMessage(ctx context.Context, acc account, c store.Chat, m wire.Message) (int64, bool, error) {
	if m.ID == "" {
		return 0, false, errors.New("message id missing")
	}
	channel := s.chatChannel(acc, c)
	cm := store.ChatMessage{ChatID: c.ID, RemoteID: m.ID, FromMe: m.FromMe, SentAt: m.Time, Body: m.Text, ReplyToRemoteID: m.ReplyTo,
		EditedAt: m.EditedAt, Deleted: m.Deleted, Status: mapStatus(m.Status)}
	if cm.SentAt.IsZero() {
		cm.SentAt = s.now()
	}
	if m.FromMe && cm.Status == "" {
		cm.Status = store.ChatStatusSent
	}
	if !m.FromMe && m.Sender.ID != "" {
		pid, err := s.resolvePerson(ctx, channel, m.Sender)
		if err != nil {
			return 0, false, err
		}
		cm.SenderPersonID = pid
		if c.Kind == store.ChatDirect {
			if cp, _ := s.st.ChatCounterpart(ctx, c.ID); cp == 0 && pid != 0 {
				_ = s.st.SetChatCounterpart(ctx, c.ID, pid)
				p, _ := s.st.GetPerson(ctx, pid)
				_, _ = s.st.PersonConversation(ctx, pid, p.DisplayName)
			}
		}
	}
	if m.Media != nil {
		// Names come from the remote peer: no paths, no bidi spoofing.
		cm.MediaMime, cm.MediaName = m.Media.MIMEType, parse.CleanFilename(m.Media.Name)
		if cm.MediaName == "" {
			cm.MediaName = defaultMediaName(m.Media.MIMEType)
		}
	}
	for _, r := range m.Reactions {
		cm.Reactions = append(cm.Reactions, reaction(r.Emoji, r.SenderID, r.FromMe))
	}
	id, created, err := s.st.UpsertChatMessage(ctx, cm)
	if err != nil {
		return 0, false, err
	}
	if m.Media != nil && m.Media.Ref != "" {
		if err := s.st.PutChatMedia(ctx, id, m.Media.Ref, m.Media.Size); err != nil {
			return 0, false, err
		}
	}
	if !m.Deleted {
		if err := s.st.IndexChatMessage(ctx, id, searchDoc(m)); err != nil {
			return 0, false, err
		}
	}
	if m.FromMe && created {
		s.applyPending(ctx, acc, c, m.ID)
	}
	if m.FromMe && !m.Time.IsZero() {
		// Sent from another device: everything before it was read there.
		_ = s.st.MarkChatRead(ctx, c.ID, m.Time)
	}
	return id, created, nil
}

func defaultMediaName(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "voice"
	}
	return "file"
}

// ownSender is the reaction sender id of the user (store.Reaction "me").
const ownSender = "me"

func reaction(emoji, sender string, fromMe bool) store.Reaction {
	if fromMe {
		sender = ownSender
	}
	return store.Reaction{Emoji: emoji, Sender: sender}
}

func (s *Service) applyReaction(ctx context.Context, acc account, r wire.ReactionEvent) error {
	c, err := s.st.ChatByRemoteID(ctx, acc.AccountID, r.ChatID)
	if err != nil {
		return ignoreNotFound(err)
	}
	m, err := s.st.ChatMessageByRemoteID(ctx, c.ID, r.MessageID)
	if err != nil {
		return ignoreNotFound(err)
	}
	sender := r.Sender.ID
	if r.FromMe {
		sender = ownSender
	}
	rs := setReaction(m.Reactions, sender, r.Emoji)
	if err := s.st.SetChatMessageReactions(ctx, m.ID, rs); err != nil {
		return err
	}
	s.notify.ConversationChanged(c.ConversationID, chatItemID(m.ID))
	return nil
}

// setReaction replaces the reaction of sender (one per sender, as on
// WhatsApp/Signal/Telegram without premium); an empty emoji removes it.
func setReaction(rs []store.Reaction, sender, emoji string) []store.Reaction {
	rs = slices.DeleteFunc(slices.Clone(rs), func(r store.Reaction) bool { return r.Sender == sender })
	if emoji != "" {
		rs = append(rs, store.Reaction{Emoji: emoji, Sender: sender})
	}
	return rs
}

// pendingKey identifies a receipt that arrived before its message was
// stored (plugins may report delivery before Send returned).
type pendingKey struct {
	account int64
	chat    string
	msg     string
}

type pendingReceipt struct {
	status string
	at     time.Time
}

func (s *Service) keepPending(acc account, chat string, ids []string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[pendingKey]pendingReceipt{}
	}
	now := s.now()
	for k, v := range s.pending {
		if now.Sub(v.at) > 2*time.Minute {
			delete(s.pending, k)
		}
	}
	for _, id := range ids {
		k := pendingKey{acc.AccountID, chat, id}
		if cur, ok := s.pending[k]; !ok || cur.status != store.ChatStatusRead {
			s.pending[k] = pendingReceipt{status: status, at: now}
		}
	}
}

// applyPending applies a receipt that arrived before the message.
func (s *Service) applyPending(ctx context.Context, acc account, c store.Chat, remoteID string) {
	k := pendingKey{acc.AccountID, c.RemoteID, remoteID}
	s.mu.Lock()
	pr, ok := s.pending[k]
	delete(s.pending, k)
	s.mu.Unlock()
	if ok {
		_, _ = s.st.UpgradeChatStatus(ctx, c.ID, []string{remoteID}, time.Time{}, pr.status)
	}
}

func (s *Service) applyReceipt(ctx context.Context, acc account, r wire.Receipt) error {
	status := store.ChatStatusDelivered
	if r.Kind == wire.ReceiptRead {
		status = store.ChatStatusRead
	}
	c, err := s.st.ChatByRemoteID(ctx, acc.AccountID, r.ChatID)
	if errors.Is(err, store.ErrNotFound) && !r.FromMe {
		s.keepPending(acc, r.ChatID, r.MessageIDs, status)
		return nil
	}
	if err != nil {
		return err
	}
	if r.FromMe {
		// Read on another device of the user.
		t := r.Time
		for _, id := range r.MessageIDs {
			if m, err := s.st.ChatMessageByRemoteID(ctx, c.ID, id); err == nil && m.SentAt.After(t) {
				t = m.SentAt
			}
		}
		if !t.IsZero() {
			if err := s.st.MarkChatRead(ctx, c.ID, t); err != nil {
				return err
			}
		}
		s.notify.ConversationChanged(c.ConversationID, "")
		return nil
	}
	var missing []string
	for _, id := range r.MessageIDs {
		if _, err := s.st.ChatMessageByRemoteID(ctx, c.ID, id); errors.Is(err, store.ErrNotFound) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		s.keepPending(acc, r.ChatID, missing, status)
	}
	n, err := s.st.UpgradeChatStatus(ctx, c.ID, r.MessageIDs, r.Time, status)
	if err != nil {
		return err
	}
	if n > 0 {
		s.notify.ConversationChanged(c.ConversationID, "")
	}
	return nil
}

func (s *Service) publishReceived(acc account, c store.Chat, m wire.Message) {
	if s.bus == nil {
		return
	}
	key := fmt.Sprintf("chat:%d:%s:%s", acc.AccountID, c.RemoteID, m.ID)
	s.bus.Publish(events.Event{ID: events.NewID(), Type: events.ChatReceived, Time: s.now(), AccountID: acc.AccountID, Ref: key, Source: "messenger:" + acc.Plugin,
		Chat: &events.Chat{AccountID: acc.AccountID, Key: key, Channel: s.chatChannel(acc, c), ChatID: c.RemoteID, ChatTitle: c.Title,
			Group: c.Kind != store.ChatDirect, Sender: contactName(m.Sender), SenderID: m.Sender.ID, Text: m.Text, HasMedia: m.Media != nil}})
}

// SyncAccount fetches the chat list and recent history of an account in
// the background.
func (s *Service) SyncAccount(accountID int64) {
	s.goBackground(func(ctx context.Context) { s.syncAccount(ctx, accountID) })
}

func (s *Service) syncAccount(ctx context.Context, accountID int64) {
	s.mu.Lock()
	if s.syncing[accountID] {
		s.mu.Unlock()
		return
	}
	s.syncing[accountID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.syncing, accountID)
		s.mu.Unlock()
	}()
	if err := s.doSync(ctx, accountID); err != nil {
		s.log.Warn("messenger: sync", "account", accountID, "err", err)
	}
}

func (s *Service) doSync(ctx context.Context, accountID int64) error {
	ma, err := s.st.GetMessengerAccount(ctx, accountID)
	if err != nil {
		return err
	}
	acc, err := s.withAccount(ctx, ma)
	if err != nil {
		return err
	}
	var cl wire.ChatList
	if err := s.host.Call(ctx, ma.Plugin, wire.MethodListChats, wire.AccountRef{Account: ma.PluginAccount}, &cl); err != nil {
		return err
	}
	slices.SortStableFunc(cl.Chats, func(a, b wire.Chat) int { return b.LastActivity.Compare(a.LastActivity) })
	since := ma.SyncedAt
	for i, wc := range cl.Chats {
		c, err := s.ensureChat(ctx, acc, wc)
		if err != nil {
			return err
		}
		if i >= s.opts.HistoryChats || (!since.IsZero() && !wc.LastActivity.IsZero() && wc.LastActivity.Before(since)) {
			continue
		}
		var ml wire.MessageList
		err = s.host.Call(ctx, ma.Plugin, wire.MethodHistory, wire.HistoryRequest{Account: ma.PluginAccount, ChatID: wc.ID,
			Limit: s.opts.HistoryMessages}, &ml)
		if wire.ErrorCode(err) == wire.CodeNotSupported {
			continue
		}
		if err != nil {
			s.log.Warn("messenger: history", "chat", wc.ID, "err", err)
			continue
		}
		var times []time.Time
		for _, m := range ml.Messages {
			m.ChatID = wc.ID
			if _, _, err := s.ingestMessage(ctx, acc, c, m); err != nil {
				return err
			}
			if !m.FromMe {
				times = append(times, m.Time)
			}
		}
		if since.IsZero() {
			// First sync: messages beyond the unread count are read.
			slices.SortFunc(times, func(a, b time.Time) int { return b.Compare(a) })
			if wc.Unread < len(times) {
				_ = s.st.MarkChatRead(ctx, c.ID, times[wc.Unread])
			}
		}
		s.notify.ConversationChanged(c.ConversationID, "")
	}
	if err := s.st.SetMessengerAccountSynced(ctx, accountID, "", s.now()); err != nil {
		return err
	}
	s.notify.ViewsChanged()
	return nil
}
