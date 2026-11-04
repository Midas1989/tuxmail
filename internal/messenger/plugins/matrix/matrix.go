// SPDX-License-Identifier: AGPL-3.0-or-later

// Package matrix is the Matrix messenger plugin on maunium.net/go/mautrix:
// login by password or SSO login token, rooms and direct chats, media,
// reactions, replies, edits, redactions, read receipts, typing and minimal
// threads. End-to-end encryption uses mautrix's crypto helper with the
// pure-Go goolm implementation; it is compiled in with the build tag
// "goolm" (build/plugins.sh), without the tag encrypted rooms show a
// placeholder.
//
// The plugin is also the base of the bridge mode (KONZEPT 11.3): rooms
// with an m.bridge / uk.half-shot.bridge state event (or ghost users of a
// known bridge) are reported with the channel of the bridged network
// (whatsapp, signal, telegram ...), and pairing a network sends the login
// command to the bridge bot and turns its QR code into a pairing event.
package matrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Version of the plugin.
const Version = "0.1.0"

// Plugin implements sdk.Plugin.
type Plugin struct {
	sdk.Unsupported
	// HTTPClient is used for all homeserver requests (tests).
	HTTPClient *http.Client

	mu       sync.Mutex
	env      *sdk.Env
	accounts map[string]*session
}

type sessionFile struct {
	Homeserver  string      `json:"homeserver"`
	UserID      id.UserID   `json:"userId"`
	DeviceID    id.DeviceID `json:"deviceId"`
	AccessToken string      `json:"accessToken"`
}

type room struct {
	name      string
	encrypted bool
	members   map[id.UserID]string // joined members -> display name
	bridge    *wire.BridgeInfo
	typing    map[id.UserID]bool
}

type session struct {
	id   string
	dir  string
	file sessionFile

	mu      sync.Mutex
	cli     *mautrix.Client
	cancel  context.CancelFunc
	state   string
	err     string
	rooms   map[id.RoomID]*room
	direct  map[id.RoomID]id.UserID
	e2ee    e2ee
	waiters map[id.RoomID]chan *event.Event // bridge pairing: bot messages
	// reactions remembers own reaction event ids for removal.
	reactions map[string]id.EventID
}

// New creates the plugin.
func New() *Plugin { return &Plugin{accounts: map[string]*session{}} }

// Info implements sdk.Plugin.
func (p *Plugin) Info() wire.HelloResult {
	caps := []string{wire.CapMedia, wire.CapReactions, wire.CapReplies, wire.CapEdit, wire.CapDelete, wire.CapReadReceipts, wire.CapTyping,
		wire.CapHistory, wire.CapThreads, wire.CapBridge}
	if e2eeAvailable {
		caps = append(caps, wire.CapE2EE)
	}
	return wire.HelloResult{Name: "matrix", DisplayName: "Matrix", Version: Version, Channel: "matrix", Risk: wire.RiskLow,
		PairingMethods: []string{wire.PairPassword, wire.PairToken, wire.PairBridge}, Capabilities: caps}
}

// Init implements sdk.Plugin.
func (p *Plugin) Init(_ context.Context, env *sdk.Env) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = env
	dirs, err := env.AccountDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		dir := filepath.Join(env.DataDir, d)
		var sf sessionFile
		b, err := os.ReadFile(filepath.Join(dir, "session.json"))
		if err != nil || json.Unmarshal(b, &sf) != nil || sf.AccessToken == "" {
			continue
		}
		p.accounts[d] = newSession(d, dir, sf)
	}
	return nil
}

func newSession(accID, dir string, sf sessionFile) *session {
	return &session{id: accID, dir: dir, file: sf, state: wire.StateDisconnected, rooms: map[id.RoomID]*room{}, direct: map[id.RoomID]id.UserID{},
		waiters: map[id.RoomID]chan *event.Event{}, reactions: map[string]id.EventID{}}
}

func (p *Plugin) session(id string) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.accounts[id]
	if s == nil {
		return nil, wire.Errorf(wire.CodeNotLinked, "matrix account %q is not linked", id)
	}
	return s, nil
}

func (p *Plugin) emit(ev wire.Event) {
	p.mu.Lock()
	env := p.env
	p.mu.Unlock()
	env.Emit(ev)
}

// Accounts implements sdk.Plugin.
func (p *Plugin) Accounts(context.Context) ([]wire.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wire.Account, 0, len(p.accounts))
	for _, s := range p.accounts {
		s.mu.Lock()
		out = append(out, wire.Account{ID: s.id, Self: wire.Contact{ID: string(s.file.UserID)}, State: s.state, Error: s.err, Channel: "matrix"})
		s.mu.Unlock()
	}
	return out, nil
}

func (p *Plugin) newClient(hs string, user id.UserID, token string) (*mautrix.Client, error) {
	cli, err := mautrix.NewClient(hs, user, token)
	if err != nil {
		return nil, wire.Errorf(wire.CodeInvalid, "invalid homeserver: %v", err)
	}
	if p.HTTPClient != nil {
		cli.Client = p.HTTPClient
	}
	cli.DefaultHTTPRetries = 2
	return cli, nil
}

// Pair implements sdk.Plugin: password or SSO token login; PairBridge
// links a network through a bridge bot of an existing account.
func (p *Plugin) Pair(ctx context.Context, req wire.PairRequest, pr *sdk.Pairing) (wire.Account, error) {
	if req.Method == wire.PairBridge {
		return p.pairBridge(ctx, req, pr)
	}
	hs := strings.TrimSpace(req.Homeserver)
	if hs == "" {
		hs = p.env.Config["default_homeserver"]
	}
	if hs == "" {
		v, err := pr.Input(ctx, "homeserver", "Homeserver URL")
		if err != nil {
			return wire.Account{}, err
		}
		hs = v
	}
	if !strings.Contains(hs, "://") {
		hs = "https://" + hs
	}
	cli, err := p.newClient(hs, "", "")
	if err != nil {
		return wire.Account{}, err
	}
	lr := &mautrix.ReqLogin{InitialDeviceDisplayName: "TuxMail"}
	switch req.Method {
	case wire.PairToken:
		token := req.Token
		if token == "" {
			// SSO: the UI opens this URL and passes the loginToken back.
			pr.Emit(wire.PairEvent{Kind: wire.PairEventInfo, Message: cli.BuildClientURL("v3", "login", "sso", "redirect") + "?redirectUrl=tuxmail://matrix-sso"})
			if token, err = pr.Input(ctx, "token", "SSO login token"); err != nil {
				return wire.Account{}, err
			}
		}
		lr.Type, lr.Token = mautrix.AuthTypeToken, token
	default:
		user, pw := req.Username, req.Password
		if user == "" {
			if user, err = pr.Input(ctx, "username", "Matrix user"); err != nil {
				return wire.Account{}, err
			}
		}
		if pw == "" {
			if pw, err = pr.Input(ctx, "password", "Password"); err != nil {
				return wire.Account{}, err
			}
		}
		lr.Type, lr.Password = mautrix.AuthTypePassword, pw
		lr.Identifier = mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user}
	}
	resp, err := cli.Login(ctx, lr)
	if err != nil {
		return wire.Account{}, wire.Errorf(wire.CodeInvalid, "login failed: %v", err)
	}
	dir, err := p.env.AccountDir(req.Account)
	if err != nil {
		return wire.Account{}, err
	}
	sf := sessionFile{Homeserver: hs, UserID: resp.UserID, DeviceID: resp.DeviceID, AccessToken: resp.AccessToken}
	b, _ := json.Marshal(sf)
	if err := os.WriteFile(filepath.Join(dir, "session.json"), b, 0o600); err != nil {
		return wire.Account{}, err
	}
	p.mu.Lock()
	p.accounts[req.Account] = newSession(req.Account, dir, sf)
	p.mu.Unlock()
	return wire.Account{ID: req.Account, Self: wire.Contact{ID: string(resp.UserID)}, State: wire.StateDisconnected, Channel: "matrix"}, nil
}

func (p *Plugin) setState(s *session, state, msg string) {
	s.mu.Lock()
	s.state, s.err = state, msg
	s.mu.Unlock()
	p.emit(wire.Event{Kind: wire.EventStatus, Account: s.id, Status: &wire.AccountStatus{State: state, Error: msg}})
}

// Connect implements sdk.Plugin: starts the /sync loop.
func (p *Plugin) Connect(ctx context.Context, accID string) error {
	s, err := p.session(accID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.cli != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	cli, err := p.newClient(s.file.Homeserver, s.file.UserID, s.file.AccessToken)
	if err != nil {
		return err
	}
	cli.DeviceID = s.file.DeviceID
	cli.Store = &fileSyncStore{path: filepath.Join(s.dir, "sync.json")}
	cli.StateStore = mautrix.NewMemoryStateStore()
	syncer := cli.Syncer.(*mautrix.DefaultSyncer)
	p.registerHandlers(s, syncer)
	enc, err := setupE2EE(ctx, cli, s.dir, syncer)
	if err != nil {
		p.env.Logger.Warn("matrix: end-to-end encryption unavailable", "err", err)
		cli.StateStore = mautrix.NewMemoryStateStore()
	}
	syncer.OnEvent(cli.StateStoreSyncHandler)
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cli, s.cancel, s.e2ee = cli, cancel, enc
	s.mu.Unlock()
	if err := p.loadRooms(ctx, s); err != nil {
		cancel()
		s.mu.Lock()
		s.cli = nil
		s.mu.Unlock()
		if errors.Is(err, mautrix.MUnknownToken) {
			p.setState(s, wire.StateLoggedOut, "access token revoked")
			return wire.Errorf(wire.CodeNotLinked, "matrix session expired")
		}
		return wire.Errorf(wire.CodeUnavailable, "matrix: %v", err)
	}
	p.setState(s, wire.StateConnected, "")
	go func() {
		for runCtx.Err() == nil {
			err := cli.SyncWithContext(runCtx)
			if runCtx.Err() != nil {
				break
			}
			if errors.Is(err, mautrix.MUnknownToken) {
				p.setState(s, wire.StateLoggedOut, "access token revoked")
				break
			}
			p.setState(s, wire.StateConnecting, fmt.Sprint(err))
			select {
			case <-runCtx.Done():
			case <-time.After(5 * time.Second):
			}
		}
		if enc != nil {
			enc.Close()
		}
	}()
	return nil
}

func (p *Plugin) client(ctx context.Context, accID string) (*session, *mautrix.Client, error) {
	s, err := p.session(accID)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	cli := s.cli
	s.mu.Unlock()
	if cli == nil {
		if err := p.Connect(ctx, accID); err != nil {
			return nil, nil, err
		}
		s.mu.Lock()
		cli = s.cli
		s.mu.Unlock()
	}
	return s, cli, nil
}

// Disconnect implements sdk.Plugin.
func (p *Plugin) Disconnect(_ context.Context, accID string) error {
	s, err := p.session(accID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cancel, cli := s.cancel, s.cli
	s.cancel, s.cli = nil, nil
	s.mu.Unlock()
	if cli != nil {
		cli.StopSync()
	}
	if cancel != nil {
		cancel()
	}
	p.setState(s, wire.StateDisconnected, "")
	return nil
}

// Remove implements sdk.Plugin: logs out the device and deletes the session.
func (p *Plugin) Remove(ctx context.Context, accID string) error {
	s, err := p.session(accID)
	if err != nil {
		return nil
	}
	if _, cli, err := p.client(ctx, accID); err == nil {
		_, _ = cli.Logout(ctx)
	}
	_ = p.Disconnect(ctx, accID)
	p.mu.Lock()
	delete(p.accounts, accID)
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

// ==== room state ===============================================================

func (s *session) room(rid id.RoomID) *room {
	r := s.rooms[rid]
	if r == nil {
		r = &room{members: map[id.UserID]string{}, typing: map[id.UserID]bool{}}
		s.rooms[rid] = r
	}
	return r
}

// applyState records a state event (s.mu held).
func (s *session) applyState(rid id.RoomID, evt *event.Event) {
	r := s.room(rid)
	switch evt.Type {
	case event.StateRoomName:
		if c := evt.Content.AsRoomName(); c != nil {
			r.name = c.Name
		}
	case event.StateEncryption:
		r.encrypted = true
	case event.StateMember:
		if evt.StateKey == nil {
			return
		}
		uid := id.UserID(*evt.StateKey)
		c := evt.Content.AsMember()
		if c.Membership == event.MembershipJoin || c.Membership == event.MembershipInvite {
			r.members[uid] = c.Displayname
		} else {
			delete(r.members, uid)
		}
	case event.StateBridge, event.StateHalfShotBridge:
		if c := evt.Content.AsBridge(); c != nil && c.Protocol.ID != "" {
			r.bridge = &wire.BridgeInfo{Network: networkOf(c.Protocol.ID), Bot: string(c.BridgeBot), Remote: c.Channel.ID}
		}
	}
}

// loadRooms reads the joined rooms with their state (initial connect).
func (p *Plugin) loadRooms(ctx context.Context, s *session) error {
	s.mu.Lock()
	cli := s.cli
	s.mu.Unlock()
	jr, err := cli.JoinedRooms(ctx)
	if err != nil {
		return err
	}
	var direct event.DirectChatsEventContent
	_ = cli.GetAccountData(ctx, event.AccountDataDirectChats.Type, &direct)
	for _, rid := range jr.JoinedRooms {
		state, err := cli.State(ctx, rid)
		if err != nil {
			continue
		}
		s.mu.Lock()
		for _, byKey := range state {
			for _, evt := range byKey {
				s.applyState(rid, evt)
			}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	for user, rooms := range direct {
		for _, rid := range rooms {
			s.direct[rid] = user
		}
	}
	s.mu.Unlock()
	return nil
}

// ghostNetworks maps ghost user prefixes of the mautrix bridges.
var ghostNetworks = []struct{ prefix, network string }{
	{"whatsapp_", "whatsapp"}, {"signal_", "signal"}, {"telegram_", "telegram"}, {"gmessages_", "gmessages"}, {"discord_", "discord"},
}

func networkOf(protocol string) string {
	p := strings.ToLower(protocol)
	switch {
	case strings.Contains(p, "whatsapp"):
		return "whatsapp"
	case strings.Contains(p, "signal"):
		return "signal"
	case strings.Contains(p, "telegram"):
		return "telegram"
	}
	return p
}

// ghostOf reports the network of a bridge ghost or bot user.
func ghostOf(u id.UserID) (network, remote string, bot bool) {
	local := u.Localpart()
	for _, g := range ghostNetworks {
		if strings.HasPrefix(local, g.prefix) {
			return g.network, strings.TrimPrefix(local, g.prefix), false
		}
		if local == strings.TrimSuffix(g.prefix, "_")+"bot" {
			return g.network, "", true
		}
	}
	return "", "", false
}

// contactOf maps a Matrix user; ghosts of the WhatsApp/Signal bridges get
// the phone number from their localpart so that persons merge with the
// direct mode.
func contactOf(u id.UserID, name string) wire.Contact {
	c := wire.Contact{ID: string(u), Name: name}
	if network, remote, _ := ghostOf(u); network == "whatsapp" || network == "signal" {
		digits := strings.TrimPrefix(remote, "+")
		if digits != "" && strings.Trim(digits, "0123456789") == "" {
			c.Phone = "+" + digits
		}
	}
	return c
}

// chatOf describes a room (s.mu held).
func (s *session) chatOf(rid id.RoomID) wire.Chat {
	r := s.room(rid)
	c := wire.Chat{ID: string(rid), Kind: wire.ChatGroup, Title: r.name}
	others := make([]id.UserID, 0, len(r.members))
	for u := range r.members {
		if u == s.file.UserID {
			continue
		}
		if _, _, bot := ghostOf(u); bot {
			continue
		}
		if r.bridge != nil && id.UserID(r.bridge.Bot) == u {
			continue
		}
		others = append(others, u)
	}
	sort.Slice(others, func(i, j int) bool { return others[i] < others[j] })
	bridge := r.bridge
	if bridge == nil {
		// Older bridges without m.bridge: ghosts reveal the network.
		for _, u := range others {
			if network, _, _ := ghostOf(u); network != "" {
				bridge = &wire.BridgeInfo{Network: network}
				break
			}
		}
	}
	if bridge != nil {
		c.Bridge, c.Channel = bridge, bridge.Network
	}
	peer, isDirect := s.direct[rid]
	if !isDirect && len(others) == 1 && (bridge != nil || r.name == "") {
		peer, isDirect = others[0], true
	}
	if isDirect {
		c.Kind = wire.ChatDirect
		pc := contactOf(peer, r.members[peer])
		c.Peer = &pc
		if c.Title == "" {
			c.Title = pc.Name
		}
	}
	if c.Title == "" {
		names := []string{}
		for _, u := range others {
			n := r.members[u]
			if n == "" {
				n = u.Localpart()
			}
			names = append(names, n)
		}
		c.Title = strings.Join(names, ", ")
	}
	return c
}

// ==== events ===================================================================

func (p *Plugin) registerHandlers(s *session, syncer *mautrix.DefaultSyncer) {
	state := func(_ context.Context, evt *event.Event) {
		s.mu.Lock()
		s.applyState(evt.RoomID, evt)
		chat := s.chatOf(evt.RoomID)
		s.mu.Unlock()
		p.emit(wire.Event{Kind: wire.EventChat, Account: s.id, Chat: &chat})
	}
	for _, t := range []event.Type{event.StateRoomName, event.StateMember, event.StateEncryption, event.StateBridge, event.StateHalfShotBridge} {
		syncer.OnEventType(t, state)
	}
	syncer.OnEventType(event.AccountDataDirectChats, func(_ context.Context, evt *event.Event) {
		c := evt.Content.AsDirectChats()
		s.mu.Lock()
		for user, rooms := range *c {
			for _, rid := range rooms {
				s.direct[rid] = user
			}
		}
		s.mu.Unlock()
	})
	syncer.OnEventType(event.EventMessage, func(_ context.Context, evt *event.Event) {
		s.mu.Lock()
		w := s.waiters[evt.RoomID]
		self := s.file.UserID
		chat := s.chatOf(evt.RoomID)
		name := s.room(evt.RoomID).members[evt.Sender]
		s.mu.Unlock()
		if w != nil && evt.Sender != self {
			select {
			case w <- evt:
			default:
			}
		}
		for _, ev := range ConvertMessage(s.id, self, chat, name, evt) {
			p.emit(ev)
		}
	})
	syncer.OnEventType(event.EventEncrypted, func(_ context.Context, evt *event.Event) {
		if e2eeAvailable {
			return // decrypted and dispatched by the crypto helper
		}
		s.mu.Lock()
		chat := s.chatOf(evt.RoomID)
		s.mu.Unlock()
		m := wire.Message{ChatID: string(evt.RoomID), ID: string(evt.ID), Sender: contactOf(evt.Sender, ""), FromMe: evt.Sender == s.file.UserID,
			Time: time.UnixMilli(evt.Timestamp).UTC(), Text: "🔒 Encrypted message (this build has no end-to-end encryption support)"}
		p.emit(wire.Event{Kind: wire.EventChat, Account: s.id, Chat: &chat})
		p.emit(wire.Event{Kind: wire.EventMessage, Account: s.id, Message: &m})
	})
	syncer.OnEventType(event.EventReaction, func(_ context.Context, evt *event.Event) {
		r := evt.Content.AsReaction()
		if r == nil {
			return
		}
		rel := r.GetRelatesTo()
		fromMe := evt.Sender == s.file.UserID
		if fromMe {
			s.mu.Lock()
			s.reactions[reactionKey(evt.RoomID, rel.EventID, rel.Key)] = evt.ID
			s.mu.Unlock()
		}
		p.emit(wire.Event{Kind: wire.EventReaction, Account: s.id, Reaction: &wire.ReactionEvent{ChatID: string(evt.RoomID), MessageID: string(rel.EventID),
			Sender: contactOf(evt.Sender, ""), FromMe: fromMe, Emoji: rel.Key}})
	})
	syncer.OnEventType(event.EventRedaction, func(_ context.Context, evt *event.Event) {
		target := evt.Redacts
		if c := evt.Content.AsRedaction(); target == "" && c != nil {
			target = c.Redacts
		}
		if target != "" {
			p.emit(wire.Event{Kind: wire.EventDelete, Account: s.id, ChatID: string(evt.RoomID), MessageID: string(target)})
		}
	})
	syncer.OnEventType(event.EphemeralEventTyping, func(_ context.Context, evt *event.Event) {
		c := evt.Content.AsTyping()
		s.mu.Lock()
		r := s.room(evt.RoomID)
		now := map[id.UserID]bool{}
		for _, u := range c.UserIDs {
			if u != s.file.UserID {
				now[u] = true
			}
		}
		var changes []wire.Typing
		for u := range now {
			if !r.typing[u] {
				changes = append(changes, wire.Typing{ChatID: string(evt.RoomID), Sender: contactOf(u, r.members[u]), Active: true})
			}
		}
		for u := range r.typing {
			if !now[u] {
				changes = append(changes, wire.Typing{ChatID: string(evt.RoomID), Sender: contactOf(u, r.members[u]), Active: false})
			}
		}
		r.typing = now
		s.mu.Unlock()
		for i := range changes {
			p.emit(wire.Event{Kind: wire.EventTyping, Account: s.id, Typing: &changes[i]})
		}
	})
	syncer.OnEventType(event.EphemeralEventReceipt, func(_ context.Context, evt *event.Event) {
		for _, ev := range ConvertReceipts(s.id, s.file.UserID, evt) {
			p.emit(ev)
		}
	})
}

func reactionKey(rid id.RoomID, target id.EventID, key string) string {
	return string(rid) + "|" + string(target) + "|" + key
}

// mediaRef addresses (possibly encrypted) Matrix media.
type mediaRef struct {
	URL  id.ContentURIString       `json:"u"`
	File *attachment.EncryptedFile `json:"f,omitempty"`
}

func encodeRef(r mediaRef) string {
	b, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ConvertMessage maps an m.room.message event (exported for tests).
func ConvertMessage(account string, self id.UserID, chat wire.Chat, senderName string, evt *event.Event) []wire.Event {
	c := evt.Content.AsMessage()
	if c == nil {
		return nil
	}
	fromMe := evt.Sender == self
	m := wire.Message{ChatID: string(evt.RoomID), ID: string(evt.ID), Sender: contactOf(evt.Sender, senderName), FromMe: fromMe,
		Time: time.UnixMilli(evt.Timestamp).UTC(), Text: c.Body}
	if c.MsgType == event.MsgNotice && chat.Bridge != nil && evt.Sender == id.UserID(chat.Bridge.Bot) {
		m.Service = true
	}
	rel := c.GetRelatesTo()
	if rel.GetReplaceID() != "" && c.NewContent != nil {
		edit := wire.Message{ChatID: m.ChatID, ID: string(rel.GetReplaceID()), Sender: m.Sender, FromMe: fromMe, Time: m.Time,
			Text: c.NewContent.Body, EditedAt: m.Time}
		return []wire.Event{{Kind: wire.EventEdit, Account: account, Message: &edit}}
	}
	if r := c.GetReplyTo(); r != "" {
		m.ReplyTo = string(r)
		m.Text = stripReplyFallback(m.Text)
	}
	if t := rel.GetThreadParent(); t != "" {
		m.ThreadID = string(t)
	}
	switch c.MsgType {
	case event.MsgImage, event.MsgFile, event.MsgAudio, event.MsgVideo:
		ref := mediaRef{URL: c.URL}
		if c.File != nil {
			ref.URL, ref.File = c.File.URL, &c.File.EncryptedFile
		}
		media := &wire.Media{Ref: encodeRef(ref), Name: c.FileName}
		if media.Name == "" {
			media.Name = c.Body
		}
		if c.Info != nil {
			media.MIMEType, media.Size = c.Info.MimeType, int64(c.Info.Size)
			media.DurationSec = float64(c.Info.Duration) / 1000
		}
		m.Media = media
		if c.FileName == "" || c.FileName == c.Body {
			m.Text = "" // body is the file name
		}
	case event.MsgEmote:
		m.Text = "* " + senderName + " " + m.Text
	}
	return []wire.Event{{Kind: wire.EventChat, Account: account, Chat: &chat}, {Kind: wire.EventMessage, Account: account, Message: &m}}
}

// stripReplyFallback removes the "> <@user> quoted" lines of old clients.
func stripReplyFallback(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], "> ") {
		i++
	}
	if i > 0 && i < len(lines) && lines[i] == "" {
		i++
	}
	if i == 0 || i >= len(lines) {
		return body
	}
	return strings.Join(lines[i:], "\n")
}

// ConvertReceipts maps m.receipt: others' read receipts raise own messages
// up to the receipt time, own receipts move the read marker.
func ConvertReceipts(account string, self id.UserID, evt *event.Event) []wire.Event {
	c := evt.Content.AsReceipt()
	var out []wire.Event
	for eid, receipts := range *c {
		for typ, users := range receipts {
			if typ != event.ReceiptTypeRead && typ != event.ReceiptTypeReadPrivate {
				continue
			}
			for u, r := range users {
				ts := r.Timestamp
				if ts.IsZero() {
					ts = time.Now()
				}
				rc := &wire.Receipt{ChatID: string(evt.RoomID), Kind: wire.ReceiptRead, Time: ts.UTC()}
				if u == self {
					rc.FromMe, rc.MessageIDs = true, []string{string(eid)}
				}
				out = append(out, wire.Event{Kind: wire.EventReceipt, Account: account, Receipt: rc})
			}
		}
	}
	return out
}

// ==== calls ===================================================================

// ListChats implements sdk.Plugin.
func (p *Plugin) ListChats(ctx context.Context, accID string) ([]wire.Chat, error) {
	s, _, err := p.client(ctx, accID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]wire.Chat, 0, len(s.rooms))
	for rid := range s.rooms {
		out = append(out, s.chatOf(rid))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// History implements sdk.Plugin.
func (p *Plugin) History(ctx context.Context, req wire.HistoryRequest) ([]wire.Message, error) {
	s, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return nil, err
	}
	rid := id.RoomID(req.ChatID)
	res, err := cli.Messages(ctx, rid, req.Before, "", mautrix.DirectionBackward, nil, req.Limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	chat := s.chatOf(rid)
	names := s.room(rid).members
	s.mu.Unlock()
	var out []wire.Message
	for _, evt := range res.Chunk {
		evt.RoomID = rid
		if evt.Type == event.EventEncrypted {
			s.mu.Lock()
			enc := s.e2ee
			s.mu.Unlock()
			if enc == nil {
				continue
			}
			dec, err := enc.Decrypt(ctx, evt)
			if err != nil {
				continue
			}
			evt = dec
		}
		if evt.Type != event.EventMessage {
			continue
		}
		_ = evt.Content.ParseRaw(evt.Type)
		for _, ev := range ConvertMessage(req.Account, s.file.UserID, chat, names[evt.Sender], evt) {
			if ev.Message != nil && ev.Kind == wire.EventMessage {
				ev.Message.Historical = true
				out = append(out, *ev.Message)
			}
		}
	}
	return out, nil
}

func (p *Plugin) send(ctx context.Context, s *session, cli *mautrix.Client, rid id.RoomID, t event.Type, content any) (*mautrix.RespSendEvent, error) {
	s.mu.Lock()
	encrypted := s.room(rid).encrypted
	enc := s.e2ee
	s.mu.Unlock()
	if encrypted && t != event.EventReaction && enc == nil {
		return nil, wire.Errorf(wire.CodeNotSupported, "this room is end-to-end encrypted; this build has no E2EE support")
	}
	// With the crypto helper set, mautrix encrypts for encrypted rooms.
	return cli.SendMessageEvent(ctx, rid, t, content)
}

// Send implements sdk.Plugin.
func (p *Plugin) Send(ctx context.Context, req wire.SendRequest) (wire.SendResult, error) {
	s, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return wire.SendResult{}, err
	}
	rid := id.RoomID(req.ChatID)
	if rid == "" {
		if req.To == nil || !strings.HasPrefix(req.To.ID, "@") {
			return wire.SendResult{}, wire.Errorf(wire.CodeInvalid, "matrix recipient must be a user id")
		}
		if rid, err = p.directRoom(ctx, s, cli, id.UserID(req.To.ID)); err != nil {
			return wire.SendResult{}, err
		}
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: req.Text}
	if req.Media != nil {
		data, err := os.ReadFile(req.Media.Path)
		if err != nil {
			return wire.SendResult{}, err
		}
		s.mu.Lock()
		encrypted := s.room(rid).encrypted
		s.mu.Unlock()
		var ef *attachment.EncryptedFile
		ctype := req.Media.MIMEType
		if encrypted {
			ef = attachment.NewEncryptedFile()
			data, ctype = ef.Encrypt(data), "application/octet-stream"
		}
		up, err := cli.UploadBytesWithName(ctx, data, ctype, req.Media.Name)
		if err != nil {
			return wire.SendResult{}, err
		}
		content.MsgType = msgTypeOf(req.Media.MIMEType)
		content.Body, content.FileName = req.Media.Name, req.Media.Name
		if req.Text != "" {
			content.Body = req.Text // caption (MSC2530)
		}
		content.Info = &event.FileInfo{MimeType: req.Media.MIMEType, Size: len(data)}
		if ef != nil {
			content.File = &event.EncryptedFileInfo{EncryptedFile: *ef, URL: up.ContentURI.CUString()}
		} else {
			content.URL = up.ContentURI.CUString()
		}
	}
	if req.ReplyTo != "" {
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(id.EventID(req.ReplyTo))
	}
	res, err := p.send(ctx, s, cli, rid, event.EventMessage, content)
	if err != nil {
		return wire.SendResult{}, err
	}
	return wire.SendResult{ChatID: string(rid), ID: string(res.EventID), Time: time.Now().UTC()}, nil
}

func msgTypeOf(mime string) event.MessageType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mime, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mime, "audio/"):
		return event.MsgAudio
	}
	return event.MsgFile
}

// directRoom finds or creates the direct chat with a user.
func (p *Plugin) directRoom(ctx context.Context, s *session, cli *mautrix.Client, user id.UserID) (id.RoomID, error) {
	s.mu.Lock()
	for rid, u := range s.direct {
		if u == user {
			s.mu.Unlock()
			return rid, nil
		}
	}
	s.mu.Unlock()
	res, err := cli.CreateRoom(ctx, &mautrix.ReqCreateRoom{Invite: []id.UserID{user}, IsDirect: true, Preset: "trusted_private_chat"})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.direct[res.RoomID] = user
	s.room(res.RoomID).members[user] = ""
	s.mu.Unlock()
	return res.RoomID, nil
}

// React implements sdk.Plugin (annotation; removal redacts the own one).
func (p *Plugin) React(ctx context.Context, req wire.ReactRequest) error {
	s, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	rid, target := id.RoomID(req.ChatID), id.EventID(req.MessageID)
	key := reactionKey(rid, target, req.Emoji)
	if req.Remove {
		s.mu.Lock()
		eid := s.reactions[key]
		delete(s.reactions, key)
		s.mu.Unlock()
		if eid == "" {
			return nil
		}
		_, err := cli.RedactEvent(ctx, rid, eid)
		return err
	}
	res, err := p.send(ctx, s, cli, rid, event.EventReaction, &event.ReactionEventContent{RelatesTo: event.RelatesTo{
		Type: event.RelAnnotation, EventID: target, Key: req.Emoji}})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.reactions[key] = res.EventID
	s.mu.Unlock()
	return nil
}

// Edit implements sdk.Plugin.
func (p *Plugin) Edit(ctx context.Context, req wire.EditRequest) error {
	s, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "* " + req.Text,
		NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: req.Text}}
	content.RelatesTo = (&event.RelatesTo{}).SetReplace(id.EventID(req.MessageID))
	_, err = p.send(ctx, s, cli, id.RoomID(req.ChatID), event.EventMessage, content)
	return err
}

// Delete implements sdk.Plugin (redaction).
func (p *Plugin) Delete(ctx context.Context, req wire.DeleteRequest) error {
	if !req.ForEveryone {
		return nil
	}
	_, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	_, err = cli.RedactEvent(ctx, id.RoomID(req.ChatID), id.EventID(req.MessageID))
	return err
}

// MarkRead implements sdk.Plugin.
func (p *Plugin) MarkRead(ctx context.Context, req wire.MarkReadRequest) error {
	_, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	if len(req.MessageIDs) == 0 {
		return nil
	}
	return cli.MarkRead(ctx, id.RoomID(req.ChatID), id.EventID(req.MessageIDs[len(req.MessageIDs)-1]))
}

// Typing implements sdk.Plugin.
func (p *Plugin) Typing(ctx context.Context, req wire.TypingRequest) error {
	_, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	_, err = cli.UserTyping(ctx, id.RoomID(req.ChatID), req.Active, 20*time.Second)
	return err
}

// DownloadMedia implements sdk.Plugin (decrypting encrypted attachments).
func (p *Plugin) DownloadMedia(ctx context.Context, req wire.MediaRequest) (wire.MediaResult, error) {
	_, cli, err := p.client(ctx, req.Account)
	if err != nil {
		return wire.MediaResult{}, err
	}
	b, err := base64.RawURLEncoding.DecodeString(req.Ref)
	var ref mediaRef
	if err != nil || json.Unmarshal(b, &ref) != nil {
		return wire.MediaResult{}, wire.Errorf(wire.CodeInvalid, "invalid media ref")
	}
	uri, err := ref.URL.Parse()
	if err != nil {
		return wire.MediaResult{}, wire.Errorf(wire.CodeInvalid, "invalid mxc url")
	}
	data, err := cli.DownloadBytes(ctx, uri)
	if err != nil {
		return wire.MediaResult{}, err
	}
	if ref.File != nil {
		if err := ref.File.DecryptInPlace(data); err != nil {
			return wire.MediaResult{}, err
		}
	}
	if err := os.WriteFile(req.Path, data, 0o600); err != nil {
		return wire.MediaResult{}, err
	}
	return wire.MediaResult{Size: int64(len(data))}, nil
}

// fileSyncStore persists the sync token so restarts continue the sync.
type fileSyncStore struct {
	mu   sync.Mutex
	path string
}

type syncState struct {
	Filter    string `json:"filter"`
	NextBatch string `json:"nextBatch"`
}

func (f *fileSyncStore) load() syncState {
	var st syncState
	if b, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (f *fileSyncStore) save(fn func(*syncState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.load()
	fn(&st)
	b, _ := json.Marshal(st)
	return os.WriteFile(f.path, b, 0o600)
}

func (f *fileSyncStore) SaveFilterID(_ context.Context, _ id.UserID, filterID string) error {
	return f.save(func(s *syncState) { s.Filter = filterID })
}
func (f *fileSyncStore) LoadFilterID(context.Context, id.UserID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load().Filter, nil
}
func (f *fileSyncStore) SaveNextBatch(_ context.Context, _ id.UserID, token string) error {
	return f.save(func(s *syncState) { s.NextBatch = token })
}
func (f *fileSyncStore) LoadNextBatch(context.Context, id.UserID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load().NextBatch, nil
}
