// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/messenger"
	"github.com/Midas1989/Mailclient/internal/messenger/host"
	"github.com/Midas1989/Mailclient/internal/messenger/signalcli"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
)

// MessengerOptions enable the messenger plugins (internal/messenger).
type MessengerOptions struct {
	// Dirs are the plugin directories (default host.DefaultDirs()).
	Dirs []string
	// Static registers in-process plugins (tests, demo).
	Static []host.Manifest
	// Launcher replaces the process launcher (tests).
	Launcher host.Launcher
	// Configure may adjust the service options.
	Configure func(*messenger.Options)
}

// Messenger error codes.
const (
	CodeMessengerDisabled  = "messenger_disabled"
	CodeRiskNotAccepted    = "risk_not_accepted"
	CodePluginUnavailable  = "plugin_unavailable"
	CodeMessengerNotLinked = "messenger_not_linked"
	CodePluginOutdated     = "plugin_outdated"
	CodeRateLimited        = "rate_limited"
	CodeComponentMissing   = "component_missing"
)

// Messenger ApiEvent types.
const (
	EventMessengerPlugin  = "messenger.plugin"
	EventMessengerPairing = "messenger.pairing"
	EventMessengerTyping  = "messenger.typing"
)

func (a *App) initMessenger() error {
	o := a.opts.Messenger
	if o == nil {
		return nil
	}
	mo := messenger.Options{Store: a.st, Blobs: a.blobs, Bus: a.bus, DataDir: a.opts.DataDir, Dirs: o.Dirs, Static: o.Static,
		Launcher: o.Launcher, HostVersion: a.opts.Version, Env: messenger.EnvConfig, Notifier: appNotifier{a: a}, Logger: a.log,
		Now: a.opts.Now, RestrictHostSettings: a.opts.Mode == ModeServer}
	if o.Configure != nil {
		o.Configure(&mo)
	}
	svc, err := messenger.New(mo)
	if err != nil {
		return err
	}
	a.msgr = svc
	return nil
}

// Messenger returns the messenger service (nil if not enabled).
func (a *App) Messenger() *messenger.Service { return a.msgr }

func (a *App) messengerOrError() (*messenger.Service, error) {
	if a.msgr == nil {
		return nil, notSupported("messenger integration")
	}
	return a.msgr, nil
}

// msgrErr maps messenger errors to API errors.
func msgrErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return err
	}
	switch {
	case errors.Is(err, messenger.ErrDisabled):
		return &Error{Code: CodeMessengerDisabled, Status: http.StatusConflict, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrRiskNotAcked):
		return &Error{Code: CodeRiskNotAccepted, Status: http.StatusConflict, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrUnknownPair), errors.Is(err, messenger.ErrNotMessenger):
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrHostSetting), errors.Is(err, messenger.ErrInstallRestricted):
		return &Error{Code: "forbidden", Status: http.StatusForbidden, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrNoChat):
		return &Error{Code: CodeInvalidInput, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrNoComponent):
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: err.Error(), Err: err}
	case errors.Is(err, messenger.ErrInstallRunning):
		return &Error{Code: CodeConflict, Status: http.StatusConflict, Message: err.Error(), Err: err}
	case errors.Is(err, signalcli.ErrUnsupported), errors.Is(err, signalcli.ErrNotPinned):
		return &Error{Code: CodeNotSupported, Status: http.StatusNotImplemented, Message: err.Error(), Err: err}
	case errors.Is(err, host.ErrUnavailable):
		return &Error{Code: CodePluginUnavailable, Status: http.StatusServiceUnavailable, Message: err.Error(), Err: err}
	}
	switch wire.ErrorCode(err) {
	case wire.CodeNotSupported, wire.CodeMethodNotFound:
		return &Error{Code: CodeNotSupported, Status: http.StatusNotImplemented, Message: err.Error(), Err: err}
	case wire.CodeNotLinked:
		return &Error{Code: CodeMessengerNotLinked, Status: http.StatusConflict, Message: err.Error(), Err: err}
	case wire.CodeInvalid, wire.CodeInvalidParams:
		return &Error{Code: CodeInvalidInput, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	case wire.CodeRateLimited:
		return &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests, Message: err.Error(), Err: err}
	case wire.CodeNotFound:
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: err.Error(), Err: err}
	case wire.CodeOutdated:
		return &Error{Code: CodePluginOutdated, Status: http.StatusServiceUnavailable, Message: err.Error(), Err: err}
	case wire.CodeComponentMissing:
		return &Error{Code: CodeComponentMissing, Status: http.StatusServiceUnavailable, Message: err.Error(), Err: err}
	case wire.CodeUnavailable:
		return &Error{Code: CodePluginUnavailable, Status: http.StatusServiceUnavailable, Message: err.Error(), Err: err}
	}
	return err
}

// appNotifier forwards messenger changes to the UI hub.
type appNotifier struct{ a *App }

func (n appNotifier) ConversationChanged(conversationID int64, itemID string) {
	n.a.invalidateViews()
	if conversationID != 0 {
		n.a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: conversationID, ItemID: itemID})
	}
}
func (n appNotifier) ViewsChanged() { n.a.invalidateViews() }
func (n appNotifier) PluginChanged(name string) {
	n.a.hub.Publish(Event{Type: EventMessengerPlugin, Extra: map[string]any{"name": name}})
}
func (n appNotifier) AccountChanged(accountID int64) {
	n.a.invalidateViews()
	n.a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: accountID})
}
func (n appNotifier) PairingChanged(p messenger.Pairing) {
	n.a.hub.Publish(Event{Type: EventMessengerPairing, Extra: map[string]any{"pairing": pairingDTO(p)}})
}
func (n appNotifier) Typing(t messenger.TypingInfo) {
	n.a.hub.Publish(Event{Type: EventMessengerTyping, ConversationID: t.ConversationID,
		Extra: map[string]any{"accountId": t.AccountID, "personId": t.PersonID, "name": t.Name, "active": t.Active}})
}

// ==== chat mode hooks ==========================================================

// messengerChat picks the chat of a conversation to send to: the chat of
// the answered item, else the most recent chat on the channel/account.
func (a *App) messengerChat(ctx context.Context, msg OutgoingMessage) (store.Chat, error) {
	if kind, id, err := parseItemID(msg.ReplyToItemID); err == nil && kind == "c" {
		if cid, err := a.st.ChatOfMessage(ctx, id); err == nil {
			if c, err := a.st.GetChat(ctx, cid); err == nil && a.msgr.IsMessengerAccount(ctx, c.AccountID) {
				return c, nil
			}
		}
	}
	conv, err := a.st.GetConversation(ctx, msg.ConversationID)
	if err != nil {
		return store.Chat{}, err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return store.Chat{}, err
	}
	for _, c := range src.chats { // most recently active first
		ch := c.Channel
		if ch == "" {
			if acc, err := a.st.GetAccount(ctx, c.AccountID); err == nil {
				ch = string(acc.Kind)
			}
		}
		if ch != msg.Channel || (msg.AccountID != 0 && c.AccountID != msg.AccountID) {
			continue
		}
		if a.msgr.IsMessengerAccount(ctx, c.AccountID) {
			return c, nil
		}
	}
	return store.Chat{}, msgrErr(messenger.ErrNoChat)
}

// sendMessenger sends a chat-mode message through the plugin.
func (a *App) sendMessenger(ctx context.Context, msg OutgoingMessage) (SendResult, error) {
	in := messenger.SendInput{Text: msg.Text}
	if msg.ConversationID != 0 {
		c, err := a.messengerChat(ctx, msg)
		if err != nil {
			return SendResult{}, err
		}
		in.ChatID = c.ID
	} else {
		return SendResult{}, invalid("conversationId is required for %s messages", msg.Channel)
	}
	if kind, id, err := parseItemID(msg.ReplyToItemID); err == nil && kind == "c" {
		in.ReplyTo = id
	}
	for _, at := range msg.Attachments {
		u, err := a.st.GetUpload(ctx, at.UploadToken)
		if err != nil {
			return SendResult{}, invalid("unknown upload token")
		}
		hash := u.BlobHash
		in.Media = append(in.Media, messenger.OutgoingMedia{Name: at.Filename, MIMEType: at.MIMEType, DurationSec: at.DurationSec,
			Voice: at.DurationSec > 0 && strings.HasPrefix(at.MIMEType, "audio/"),
			Open:  func() (io.ReadCloser, error) { return a.blobs.Open(hash) }})
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Media) == 0 {
		return SendResult{}, invalid("empty message")
	}
	out, err := a.msgr.Send(ctx, in)
	if err != nil {
		return SendResult{}, msgrErr(err)
	}
	conv := msg.ConversationID
	if conv == 0 {
		conv = out.ConversationID
	}
	_ = a.st.DeleteChatDraft(ctx, msg.ConversationID)
	a.invalidateViews()
	res := SendResult{ConversationID: conv, ItemID: chatItemID(out.ChatMessageID)}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: conv, ItemID: res.ItemID})
	return res, nil
}

// usesMessenger reports whether a chat item belongs to a plugin account.
func (a *App) messengerItem(ctx context.Context, chatMessageID int64) bool {
	if a.msgr == nil {
		return false
	}
	cid, err := a.st.ChatOfMessage(ctx, chatMessageID)
	if err != nil {
		return false
	}
	c, err := a.st.GetChat(ctx, cid)
	return err == nil && a.msgr.IsMessengerAccount(ctx, c.AccountID)
}

// reactMessenger sends the reaction of a chat item through the plugin.
// handled is false for items that are no messenger messages.
func (a *App) reactMessenger(ctx context.Context, itemID, emoji string) (handled bool, err error) {
	kind, id, err := parseItemID(itemID)
	if err != nil || kind != "c" || !a.messengerItem(ctx, id) {
		return false, nil
	}
	_, err = a.msgr.React(ctx, id, emoji)
	return true, msgrErr(err)
}

// deleteMessenger deletes a messenger message remotely (own: for everyone).
func (a *App) deleteMessenger(ctx context.Context, chatMessageID int64) (handled bool, err error) {
	if !a.messengerItem(ctx, chatMessageID) {
		return false, nil
	}
	return true, msgrErr(a.msgr.Delete(ctx, chatMessageID))
}

// markReadMessenger sends read receipts for the chats of a conversation.
func (a *App) markReadMessenger(ctx context.Context, chats []store.Chat) {
	if a.msgr == nil {
		return
	}
	for _, c := range chats {
		if a.msgr.IsMessengerAccount(ctx, c.AccountID) {
			if err := a.msgr.MarkRead(ctx, c.ID); err != nil {
				a.log.Warn("messenger: read receipts", "chat", c.ID, "err", err)
			}
		}
	}
}

// chatMediaURL is the download URL of chat message media.
func chatMediaURL(chatMessageID int64) string {
	return fmt.Sprintf("/api/messenger/media/%d", chatMessageID)
}

// messengerPasswords implements send.MessengerSender on top of the service.
type messengerPasswords struct{ a *App }

func (m messengerPasswords) SendPassword(ctx context.Context, accountID int64, to domain.Address, text string) error {
	if m.a.msgr == nil {
		return errors.New("messenger integration not enabled")
	}
	return m.a.msgr.SendPassword(ctx, accountID, to, text)
}

func readAllLimited(r io.Reader, n int64) ([]byte, error) { return io.ReadAll(io.LimitReader(r, n)) }
