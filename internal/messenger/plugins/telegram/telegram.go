// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telegram is the Telegram messenger plugin on github.com/gotd/td
// (MTProto, the official API that explicitly allows third-party clients).
// Login by phone number + code (+ 2FA password) or QR code; chats, groups
// and channels, text and media, replies, reactions, edits, deletes and read
// state.
//
// The API credentials (api_id/api_hash of the TuxMail project) come from
// the plugin settings api_id/api_hash or the environment variables
// TUXMAIL_TELEGRAM_API_ID / TUXMAIL_TELEGRAM_API_HASH.
package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Version of the plugin.
const Version = "0.1.0"

// Plugin implements sdk.Plugin.
type Plugin struct {
	mu       sync.Mutex
	env      *sdk.Env
	appID    int
	appHash  string
	accounts map[string]*session_
}

type session_ struct {
	id     string
	dir    string
	peers  *peers
	client *telegram.Client
	api    *tg.Client
	cancel context.CancelFunc
	ready  chan struct{}
	state  string
	err    string
	self   wire.Contact
	// msgChat remembers the chat of recent non-channel message ids
	// (updateDeleteMessages carries ids only).
	msgChat map[int]string
}

// New creates the plugin.
func New() *Plugin { return &Plugin{accounts: map[string]*session_{}} }

// Info implements sdk.Plugin.
func (p *Plugin) Info() wire.HelloResult {
	info := wire.HelloResult{Name: "telegram", DisplayName: "Telegram", Version: Version, Channel: "telegram", Risk: wire.RiskLow,
		PairingMethods: []string{wire.PairQR, wire.PairPhone},
		Capabilities: []string{wire.CapMedia, wire.CapReactions, wire.CapReplies, wire.CapEdit, wire.CapDelete, wire.CapReadReceipts,
			wire.CapTyping, wire.CapHistory, wire.CapThreads}}
	if p.appID == 0 {
		info.UpdateHint = "Telegram API credentials (api_id/api_hash) are not configured"
	}
	return info
}

// Credentials parses api_id/api_hash from config or environment.
func Credentials(cfg map[string]string) (int, string, error) {
	id, hash := cfg["api_id"], cfg["api_hash"]
	if id == "" {
		id = os.Getenv("TUXMAIL_TELEGRAM_API_ID")
	}
	if hash == "" {
		hash = os.Getenv("TUXMAIL_TELEGRAM_API_HASH")
	}
	if id == "" || hash == "" {
		return 0, "", wire.Errorf(wire.CodeUnavailable, "Telegram API credentials are not configured (TUXMAIL_TELEGRAM_API_ID/HASH)")
	}
	n, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || n <= 0 {
		return 0, "", wire.Errorf(wire.CodeInvalid, "invalid Telegram api_id")
	}
	return n, strings.TrimSpace(hash), nil
}

// Init implements sdk.Plugin.
func (p *Plugin) Init(_ context.Context, env *sdk.Env) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = env
	p.appID, p.appHash, _ = Credentials(env.Config)
	dirs, err := env.AccountDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		dir := filepath.Join(env.DataDir, d)
		if _, err := os.Stat(filepath.Join(dir, "session.json")); err != nil {
			continue
		}
		pc := loadPeers(filepath.Join(dir, "peers.json"))
		p.accounts[d] = &session_{id: d, dir: dir, peers: pc, state: wire.StateDisconnected, msgChat: map[int]string{},
			self: wire.Contact{ID: "u" + strconv.FormatInt(pc.self(), 10)}}
	}
	return nil
}

func (p *Plugin) creds() (int, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.appID == 0 {
		return 0, "", wire.Errorf(wire.CodeUnavailable, "Telegram API credentials are not configured (TUXMAIL_TELEGRAM_API_ID/HASH)")
	}
	return p.appID, p.appHash, nil
}

func (p *Plugin) session(id string) (*session_, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.accounts[id]
	if s == nil {
		return nil, wire.Errorf(wire.CodeNotLinked, "telegram account %q is not linked", id)
	}
	return s, nil
}

// Accounts implements sdk.Plugin.
func (p *Plugin) Accounts(context.Context) ([]wire.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wire.Account, 0, len(p.accounts))
	for _, s := range p.accounts {
		out = append(out, wire.Account{ID: s.id, Self: s.self, State: s.state, Error: s.err, Channel: "telegram"})
	}
	return out, nil
}

func (p *Plugin) emitStatus(s *session_, state, msg string) {
	p.mu.Lock()
	s.state, s.err = state, msg
	env := p.env
	p.mu.Unlock()
	env.Emit(wire.Event{Kind: wire.EventStatus, Account: s.id, Status: &wire.AccountStatus{State: state, Error: msg}})
}

func newClient(appID int, appHash, dir string, h telegram.UpdateHandler) *telegram.Client {
	return telegram.NewClient(appID, appHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(dir, "session.json")},
		UpdateHandler:  h,
		Device: telegram.DeviceConfig{DeviceModel: "TuxMail", SystemVersion: "TuxMail", AppVersion: Version,
			SystemLangCode: "en", LangCode: "en"},
	})
}

// Connect implements sdk.Plugin: it runs the MTProto client of the account
// until Disconnect.
func (p *Plugin) Connect(ctx context.Context, id string) error {
	appID, appHash, err := p.creds()
	if err != nil {
		return err
	}
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if s.client != nil {
		ready := s.ready
		p.mu.Unlock()
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d := tg.NewUpdateDispatcher()
	p.handleUpdates(s, &d)
	client := newClient(appID, appHash, s.dir, d)
	runCtx, cancel := context.WithCancel(context.Background())
	s.client, s.api, s.cancel, s.ready = client, client.API(), cancel, make(chan struct{})
	ready := s.ready
	p.mu.Unlock()
	errc := make(chan error, 1)
	go func() {
		err := client.Run(runCtx, func(ctx context.Context) error {
			st, err := client.Auth().Status(ctx)
			if err != nil {
				return err
			}
			if !st.Authorized {
				return wire.Errorf(wire.CodeNotLinked, "telegram session is no longer authorized")
			}
			if st.User != nil {
				s.peers.learn([]tg.UserClass{st.User}, nil)
				p.mu.Lock()
				s.self = userContact(st.User)
				p.mu.Unlock()
			}
			close(ready)
			p.emitStatus(s, wire.StateConnected, "")
			<-ctx.Done()
			return nil
		})
		_ = s.peers.save()
		p.mu.Lock()
		if s.client == client {
			s.client, s.api = nil, nil
		}
		p.mu.Unlock()
		select {
		case <-ready:
		default:
			errc <- err
		}
		if runCtx.Err() == nil {
			state := wire.StateDisconnected
			if wire.ErrorCode(err) == wire.CodeNotLinked {
				state = wire.StateLoggedOut
			}
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			p.emitStatus(s, state, msg)
		}
	}()
	select {
	case <-ready:
		return nil
	case err := <-errc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Plugin) api(ctx context.Context, id string) (*session_, *tg.Client, error) {
	s, err := p.session(id)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	api := s.api
	p.mu.Unlock()
	if api == nil {
		if err := p.Connect(ctx, id); err != nil {
			return nil, nil, err
		}
		p.mu.Lock()
		api = s.api
		p.mu.Unlock()
	}
	if api == nil {
		return nil, nil, wire.Errorf(wire.CodeUnavailable, "telegram not connected")
	}
	return s, api, nil
}

// Disconnect implements sdk.Plugin.
func (p *Plugin) Disconnect(_ context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.emitStatus(s, wire.StateDisconnected, "")
	return nil
}

// Remove implements sdk.Plugin: logs out and deletes the session.
func (p *Plugin) Remove(ctx context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return nil
	}
	if _, api, err := p.api(ctx, id); err == nil {
		_, _ = api.AuthLogOut(ctx)
	}
	_ = p.Disconnect(ctx, id)
	p.mu.Lock()
	delete(p.accounts, id)
	p.mu.Unlock()
	return os.RemoveAll(s.dir)
}

// Shutdown implements sdk.Plugin.
func (p *Plugin) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	ids := make([]string, 0, len(p.accounts))
	for id := range p.accounts {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		_ = p.Disconnect(ctx, id)
	}
	return nil
}

// authenticator answers the phone login flow through the pairing UI.
type authenticator struct {
	phone string
	pr    *sdk.Pairing
}

func (a authenticator) Phone(ctx context.Context) (string, error) {
	if a.phone != "" {
		return a.phone, nil
	}
	return a.pr.Input(ctx, "phone", "Telegram phone number")
}
func (a authenticator) Password(ctx context.Context) (string, error) {
	return a.pr.Input(ctx, "password", "Two-step verification password")
}
func (a authenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.pr.Input(ctx, "code", "Login code sent by Telegram")
}
func (a authenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("sign up with the Telegram app first")
}
func (a authenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("sign up with the Telegram app first")
}

// Pair implements sdk.Plugin.
func (p *Plugin) Pair(ctx context.Context, req wire.PairRequest, pr *sdk.Pairing) (wire.Account, error) {
	appID, appHash, err := p.creds()
	if err != nil {
		return wire.Account{}, err
	}
	dir, err := p.env.AccountDir(req.Account)
	if err != nil {
		return wire.Account{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d)
	client := newClient(appID, appHash, dir, d)
	var self *tg.User
	err = client.Run(ctx, func(ctx context.Context) error {
		switch req.Method {
		case wire.PairPhone:
			if err := auth.NewFlow(authenticator{phone: req.Phone, pr: pr}, auth.SendCodeOptions{}).Run(ctx, client.Auth()); err != nil {
				return err
			}
		default:
			_, err := client.QR().Auth(ctx, loggedIn, func(_ context.Context, t qrlogin.Token) error {
				pr.Emit(wire.PairEvent{Kind: wire.PairEventQR, QR: t.URL(), ExpiresAt: t.Expires(),
					Message: "Telegram > Settings > Devices > Link Desktop Device"})
				return nil
			})
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") || errors.Is(err, auth.ErrPasswordAuthNeeded) {
				pw, ierr := pr.Input(ctx, "password", "Two-step verification password")
				if ierr != nil {
					return ierr
				}
				_, err = client.Auth().Password(ctx, pw)
			}
			if err != nil {
				return err
			}
		}
		u, err := client.Self(ctx)
		self = u
		return err
	})
	if err != nil {
		return wire.Account{}, err
	}
	pc := loadPeers(filepath.Join(dir, "peers.json"))
	pc.learn([]tg.UserClass{self}, nil)
	if err := pc.save(); err != nil {
		return wire.Account{}, err
	}
	s := &session_{id: req.Account, dir: dir, peers: pc, state: wire.StateDisconnected, self: userContact(self), msgChat: map[int]string{}}
	p.mu.Lock()
	p.accounts[req.Account] = s
	p.mu.Unlock()
	ok = true
	return wire.Account{ID: req.Account, Self: s.self, State: wire.StateDisconnected, Channel: "telegram"}, nil
}

// ==== updates ==================================================================

func (p *Plugin) handleUpdates(s *session_, d *tg.UpdateDispatcher) {
	emit := func(ev wire.Event) {
		ev.Account = s.id
		p.mu.Lock()
		env := p.env
		p.mu.Unlock()
		env.Emit(ev)
	}
	onMsg := func(e tg.Entities, mc tg.MessageClass, edit bool) {
		s.peers.learnEntities(e)
		m, ok := mc.(*tg.Message)
		if !ok {
			return
		}
		wm := ConvertMessage(s.peers, m, e.Users)
		if wm == nil {
			return
		}
		p.mu.Lock()
		if _, isChan := m.PeerID.(*tg.PeerChannel); !isChan {
			if len(s.msgChat) > 5000 {
				clear(s.msgChat)
			}
			s.msgChat[m.ID] = wm.ChatID
		}
		p.mu.Unlock()
		chat := chatOf(s.peers, m.PeerID, e.Users)
		emit(wire.Event{Kind: wire.EventChat, Chat: &chat})
		kind := wire.EventMessage
		if edit {
			kind = wire.EventEdit
		}
		emit(wire.Event{Kind: kind, Message: wm})
	}
	d.OnNewMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		onMsg(e, u.Message, false)
		return nil
	})
	d.OnNewChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		onMsg(e, u.Message, false)
		return nil
	})
	d.OnEditMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		onMsg(e, u.Message, true)
		return nil
	})
	d.OnEditChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		onMsg(e, u.Message, true)
		return nil
	})
	d.OnDeleteMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteMessages) error {
		for _, id := range u.Messages {
			p.mu.Lock()
			chat := s.msgChat[id]
			p.mu.Unlock()
			if chat != "" {
				emit(wire.Event{Kind: wire.EventDelete, ChatID: chat, MessageID: strconv.Itoa(id)})
			}
		}
		return nil
	})
	d.OnDeleteChannelMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		chat := "ch" + strconv.FormatInt(u.ChannelID, 10)
		for _, id := range u.Messages {
			emit(wire.Event{Kind: wire.EventDelete, ChatID: chat, MessageID: strconv.Itoa(id)})
		}
		return nil
	})
	d.OnUserTyping(func(_ context.Context, e tg.Entities, u *tg.UpdateUserTyping) error {
		s.peers.learnEntities(e)
		id := "u" + strconv.FormatInt(u.UserID, 10)
		_, cancel := u.Action.(*tg.SendMessageCancelAction)
		emit(wire.Event{Kind: wire.EventTyping, Typing: &wire.Typing{ChatID: id, Sender: wire.Contact{ID: id, Name: s.peers.name(id)}, Active: !cancel}})
		return nil
	})
	d.OnChatUserTyping(func(_ context.Context, e tg.Entities, u *tg.UpdateChatUserTyping) error {
		s.peers.learnEntities(e)
		sid := peerID(u.FromID)
		_, cancel := u.Action.(*tg.SendMessageCancelAction)
		emit(wire.Event{Kind: wire.EventTyping, Typing: &wire.Typing{ChatID: "c" + strconv.FormatInt(u.ChatID, 10),
			Sender: wire.Contact{ID: sid, Name: s.peers.name(sid)}, Active: !cancel}})
		return nil
	})
	d.OnReadHistoryOutbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadHistoryOutbox) error {
		emit(wire.Event{Kind: wire.EventReceipt, Receipt: &wire.Receipt{ChatID: peerID(u.Peer), Kind: wire.ReceiptRead, Time: time.Now().UTC()}})
		return nil
	})
	d.OnReadChannelOutbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadChannelOutbox) error {
		emit(wire.Event{Kind: wire.EventReceipt, Receipt: &wire.Receipt{ChatID: "ch" + strconv.FormatInt(u.ChannelID, 10), Kind: wire.ReceiptRead,
			Time: time.Now().UTC()}})
		return nil
	})
	d.OnReadHistoryInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		emit(wire.Event{Kind: wire.EventReceipt, Receipt: &wire.Receipt{ChatID: peerID(u.Peer), MessageIDs: []string{strconv.Itoa(u.MaxID)},
			Kind: wire.ReceiptRead, FromMe: true}})
		return nil
	})
	d.OnReadChannelInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadChannelInbox) error {
		emit(wire.Event{Kind: wire.EventReceipt, Receipt: &wire.Receipt{ChatID: "ch" + strconv.FormatInt(u.ChannelID, 10),
			MessageIDs: []string{strconv.Itoa(u.MaxID)}, Kind: wire.ReceiptRead, FromMe: true}})
		return nil
	})
}

// ==== calls ===================================================================

// ListChats implements sdk.Plugin (the dialog list).
func (p *Plugin) ListChats(ctx context.Context, id string) ([]wire.Chat, error) {
	s, api, err := p.api(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100})
	if err != nil {
		return nil, err
	}
	var (
		dialogs []tg.DialogClass
		msgs    []tg.MessageClass
		users   []tg.UserClass
		chats   []tg.ChatClass
	)
	switch v := res.(type) {
	case *tg.MessagesDialogs:
		dialogs, msgs, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	case *tg.MessagesDialogsSlice:
		dialogs, msgs, users, chats = v.Dialogs, v.Messages, v.Users, v.Chats
	default:
		return nil, nil
	}
	s.peers.learn(users, chats)
	_ = s.peers.save()
	byUser := map[int64]*tg.User{}
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok {
			byUser[u.ID] = u
		}
	}
	last := map[string]time.Time{}
	for _, mc := range msgs {
		if m, ok := mc.(*tg.Message); ok {
			last[peerID(m.PeerID)] = time.Unix(int64(m.Date), 0).UTC()
		}
	}
	out := make([]wire.Chat, 0, len(dialogs))
	for _, dc := range dialogs {
		dl, ok := dc.(*tg.Dialog)
		if !ok {
			continue
		}
		c := chatOf(s.peers, dl.Peer, byUser)
		c.Unread = dl.UnreadCount
		c.LastActivity = last[c.ID]
		c.Archived = dl.FolderID == 1
		c.Muted = dl.NotifySettings.MuteUntil > int(time.Now().Unix())
		out = append(out, c)
	}
	return out, nil
}

func messagesOf(res tg.MessagesMessagesClass) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass) {
	switch v := res.(type) {
	case *tg.MessagesMessages:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Users, v.Chats
	}
	return nil, nil, nil
}

// History implements sdk.Plugin.
func (p *Plugin) History(ctx context.Context, req wire.HistoryRequest) ([]wire.Message, error) {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return nil, err
	}
	peer, err := s.peers.inputPeer(req.ChatID)
	if err != nil {
		return nil, err
	}
	offset := 0
	if req.Before != "" {
		if offset, err = parseMsgID(req.Before); err != nil {
			return nil, err
		}
	}
	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, OffsetID: offset, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	msgs, users, chats := messagesOf(res)
	s.peers.learn(users, chats)
	byUser := map[int64]*tg.User{}
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok {
			byUser[u.ID] = u
		}
	}
	var out []wire.Message
	for _, mc := range msgs {
		if m, ok := mc.(*tg.Message); ok {
			if wm := ConvertMessage(s.peers, m, byUser); wm != nil {
				out = append(out, *wm)
			}
		}
	}
	return out, nil
}

func randomID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// Send implements sdk.Plugin.
func (p *Plugin) Send(ctx context.Context, req wire.SendRequest) (wire.SendResult, error) {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return wire.SendResult{}, err
	}
	chat := req.ChatID
	if chat == "" {
		if chat, err = p.resolveContact(ctx, s, api, req.To); err != nil {
			return wire.SendResult{}, err
		}
	}
	peer, err := s.peers.inputPeer(chat)
	if err != nil {
		return wire.SendResult{}, err
	}
	var reply tg.InputReplyToClass
	if req.ReplyTo != "" {
		if id, err := parseMsgID(req.ReplyTo); err == nil {
			reply = &tg.InputReplyToMessage{ReplyToMsgID: id}
		}
	}
	rid := randomID()
	var upd tg.UpdatesClass
	if req.Media != nil {
		f, err := uploader.NewUploader(api).FromPath(ctx, req.Media.Path)
		if err != nil {
			return wire.SendResult{}, err
		}
		var media tg.InputMediaClass
		if strings.HasPrefix(req.Media.MIMEType, "image/") && req.Media.MIMEType != "image/gif" {
			media = &tg.InputMediaUploadedPhoto{File: f}
		} else {
			attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: req.Media.Name}}
			if req.Media.Voice {
				attrs = append(attrs, &tg.DocumentAttributeAudio{Voice: true, Duration: int(req.Media.DurationSec)})
			}
			media = &tg.InputMediaUploadedDocument{File: f, MimeType: req.Media.MIMEType, Attributes: attrs}
		}
		upd, err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: peer, ReplyTo: reply, Media: media, Message: req.Text, RandomID: rid})
		if err != nil {
			return wire.SendResult{}, err
		}
	} else {
		upd, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, ReplyTo: reply, Message: req.Text, RandomID: rid})
		if err != nil {
			return wire.SendResult{}, err
		}
	}
	id, date, err := sentID(upd, rid)
	if err != nil {
		return wire.SendResult{}, err
	}
	return wire.SendResult{ChatID: chat, ID: strconv.Itoa(id), Time: date}, nil
}

// resolveContact finds the user of a contact (by username or phone).
func (p *Plugin) resolveContact(ctx context.Context, s *session_, api *tg.Client, c *wire.Contact) (string, error) {
	if c == nil {
		return "", wire.Errorf(wire.CodeInvalid, "no recipient")
	}
	if strings.HasPrefix(c.ID, "u") {
		if _, err := s.peers.inputPeer(c.ID); err == nil {
			return c.ID, nil
		}
	}
	if c.Username != "" {
		res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: strings.TrimPrefix(c.Username, "@")})
		if err != nil {
			return "", err
		}
		s.peers.learn(res.Users, res.Chats)
		return peerID(res.Peer), nil
	}
	if c.Phone != "" {
		u, err := api.ContactsResolvePhone(ctx, strings.TrimPrefix(c.Phone, "+"))
		if err != nil {
			return "", err
		}
		s.peers.learn(u.Users, u.Chats)
		return peerID(u.Peer), nil
	}
	return "", wire.Errorf(wire.CodeInvalid, "telegram recipient needs a username or phone number")
}

// React implements sdk.Plugin.
func (p *Plugin) React(ctx context.Context, req wire.ReactRequest) error {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return err
	}
	peer, err := s.peers.inputPeer(req.ChatID)
	if err != nil {
		return err
	}
	id, err := parseMsgID(req.MessageID)
	if err != nil {
		return err
	}
	r := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: id}
	if !req.Remove {
		r.Reaction = []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: req.Emoji}}
	}
	_, err = api.MessagesSendReaction(ctx, r)
	return err
}

// Edit implements sdk.Plugin.
func (p *Plugin) Edit(ctx context.Context, req wire.EditRequest) error {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return err
	}
	peer, err := s.peers.inputPeer(req.ChatID)
	if err != nil {
		return err
	}
	id, err := parseMsgID(req.MessageID)
	if err != nil {
		return err
	}
	_, err = api.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: peer, ID: id, Message: req.Text})
	return err
}

// Delete implements sdk.Plugin.
func (p *Plugin) Delete(ctx context.Context, req wire.DeleteRequest) error {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return err
	}
	id, err := parseMsgID(req.MessageID)
	if err != nil {
		return err
	}
	if ch, ok := s.peers.inputChannel(req.ChatID); ok {
		_, err = api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: ch, ID: []int{id}})
		return err
	}
	_, err = api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: req.ForEveryone, ID: []int{id}})
	return err
}

// MarkRead implements sdk.Plugin.
func (p *Plugin) MarkRead(ctx context.Context, req wire.MarkReadRequest) error {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return err
	}
	maxID := 0
	for _, m := range req.MessageIDs {
		if n, err := parseMsgID(m); err == nil && n > maxID {
			maxID = n
		}
	}
	if ch, ok := s.peers.inputChannel(req.ChatID); ok {
		_, err = api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: ch, MaxID: maxID})
		return err
	}
	peer, err := s.peers.inputPeer(req.ChatID)
	if err != nil {
		return err
	}
	_, err = api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: peer, MaxID: maxID})
	return err
}

// Typing implements sdk.Plugin.
func (p *Plugin) Typing(ctx context.Context, req wire.TypingRequest) error {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return err
	}
	peer, err := s.peers.inputPeer(req.ChatID)
	if err != nil {
		return err
	}
	var action tg.SendMessageActionClass = &tg.SendMessageTypingAction{}
	if !req.Active {
		action = &tg.SendMessageCancelAction{}
	}
	_, err = api.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: peer, Action: action})
	return err
}

// DownloadMedia implements sdk.Plugin: Ref is "<chat>/<message id>"; the
// message is fetched again for a fresh file reference.
func (p *Plugin) DownloadMedia(ctx context.Context, req wire.MediaRequest) (wire.MediaResult, error) {
	s, api, err := p.api(ctx, req.Account)
	if err != nil {
		return wire.MediaResult{}, err
	}
	chat, mid, ok := strings.Cut(req.Ref, "/")
	if !ok {
		return wire.MediaResult{}, wire.Errorf(wire.CodeInvalid, "invalid media ref")
	}
	id, err := parseMsgID(mid)
	if err != nil {
		return wire.MediaResult{}, err
	}
	var res tg.MessagesMessagesClass
	if ch, ok := s.peers.inputChannel(chat); ok {
		res, err = api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: ch, ID: []tg.InputMessageClass{&tg.InputMessageID{ID: id}}})
	} else {
		res, err = api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: id}})
	}
	if err != nil {
		return wire.MediaResult{}, err
	}
	msgs, _, _ := messagesOf(res)
	for _, mc := range msgs {
		m, ok := mc.(*tg.Message)
		if !ok || m.ID != id {
			continue
		}
		loc, info := mediaLocation(m.Media)
		if loc == nil {
			return wire.MediaResult{}, wire.Errorf(wire.CodeNotFound, "message has no downloadable media")
		}
		if _, err := downloader.NewDownloader().Download(api, loc).ToPath(ctx, req.Path); err != nil {
			return wire.MediaResult{}, err
		}
		fi, err := os.Stat(req.Path)
		if err != nil {
			return wire.MediaResult{}, err
		}
		info.Size = fi.Size()
		return info, nil
	}
	return wire.MediaResult{}, wire.Errorf(wire.CodeNotFound, "message %d not found", id)
}

func mediaLocation(mc tg.MessageMediaClass) (tg.InputFileLocationClass, wire.MediaResult) {
	switch v := mc.(type) {
	case *tg.MessageMediaPhoto:
		ph, ok := v.Photo.(*tg.Photo)
		if !ok || len(ph.Sizes) == 0 {
			return nil, wire.MediaResult{}
		}
		thumb := ""
		for _, s := range ph.Sizes {
			switch sz := s.(type) {
			case *tg.PhotoSize:
				thumb = sz.Type
			case *tg.PhotoSizeProgressive:
				thumb = sz.Type
			}
		}
		return &tg.InputPhotoFileLocation{ID: ph.ID, AccessHash: ph.AccessHash, FileReference: ph.FileReference, ThumbSize: thumb},
			wire.MediaResult{MIMEType: "image/jpeg", Name: "photo.jpg"}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return nil, wire.MediaResult{}
		}
		m := convertMedia(v)
		return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference},
			wire.MediaResult{MIMEType: d.MimeType, Name: m.Name}
	}
	return nil, wire.MediaResult{}
}
