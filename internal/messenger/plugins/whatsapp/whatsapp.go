// SPDX-License-Identifier: AGPL-3.0-or-later

// Package whatsapp is the WhatsApp messenger plugin on go.mau.fi/whatsmeow
// (MPL-2.0): TuxMail is linked as a companion device by QR code or phone
// pairing code. Every account keeps its whatsmeow sqlstore in
// <account>/whatsmeow.db, opened with the pure-Go modernc.org/sqlite driver
// (no CGO).
//
// Risk (KONZEPT 18.1): WhatsApp's terms forbid unofficial clients; the
// plugin is disabled by default and behaves conservatively (no bulk
// sending, rate limited, realistic client name).
package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Version of the plugin.
const Version = "0.1.0"

// minSendInterval spaces outgoing messages (conservative behaviour).
const minSendInterval = 700 * time.Millisecond

// Plugin implements sdk.Plugin.
type Plugin struct {
	sdk.Unsupported

	mu       sync.Mutex
	env      *sdk.Env
	accounts map[string]*session
	outdated string
	lastSend time.Time
}

type session struct {
	id     string
	dir    string
	db     *sql.DB
	client *whatsmeow.Client
	state  string
	err    string
}

// New creates the plugin.
func New() *Plugin { return &Plugin{accounts: map[string]*session{}} }

// Info implements sdk.Plugin.
func (p *Plugin) Info() wire.HelloResult {
	return wire.HelloResult{Name: "whatsapp", DisplayName: "WhatsApp", Version: Version, Channel: "whatsapp", Risk: wire.RiskMedium,
		PairingMethods: []string{wire.PairQR, wire.PairPhone},
		Capabilities: []string{wire.CapMedia, wire.CapReactions, wire.CapReplies, wire.CapEdit, wire.CapDelete, wire.CapReadReceipts,
			wire.CapTyping, wire.CapE2EE},
		UpdateHint: p.outdated}
}

// Init implements sdk.Plugin.
func (p *Plugin) Init(ctx context.Context, env *sdk.Env) error {
	p.mu.Lock()
	p.env = env
	p.mu.Unlock()
	store.DeviceProps.Os = ptr("TuxMail")
	// WhatsApp rejects outdated web versions: fetch the current one.
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if v, err := whatsmeow.GetLatestVersion(vctx, &http.Client{Timeout: 10 * time.Second}); err == nil && v != nil {
		store.SetWAVersion(*v)
	}
	cancel()
	dirs, err := env.AccountDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		dir := filepath.Join(env.DataDir, d)
		if _, err := os.Stat(filepath.Join(dir, "whatsmeow.db")); err != nil {
			continue
		}
		p.mu.Lock()
		p.accounts[d] = &session{id: d, dir: dir, state: wire.StateDisconnected}
		p.mu.Unlock()
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// openDevice opens the whatsmeow store of an account directory.
func openDevice(ctx context.Context, dir string) (*sql.DB, *store.Device, error) {
	dsn := "file:" + filepath.Join(dir, "whatsmeow.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	c := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := c.Upgrade(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("whatsmeow store: %w", err)
	}
	dev, err := c.GetFirstDevice(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, dev, nil
}

func (p *Plugin) session(id string) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.accounts[id]
	if s == nil {
		return nil, wire.Errorf(wire.CodeNotLinked, "whatsapp account %q is not linked", id)
	}
	return s, nil
}

func selfContact(dev *store.Device) wire.Contact {
	if dev == nil || dev.ID == nil {
		return wire.Contact{}
	}
	j := dev.ID.ToNonAD()
	return wire.Contact{ID: j.String(), Phone: phoneOf(j), Name: dev.PushName}
}

// Accounts implements sdk.Plugin.
func (p *Plugin) Accounts(context.Context) ([]wire.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wire.Account, 0, len(p.accounts))
	for _, s := range p.accounts {
		a := wire.Account{ID: s.id, State: s.state, Error: s.err, Channel: "whatsapp"}
		if s.client != nil {
			a.Self = selfContact(s.client.Store)
		}
		out = append(out, a)
	}
	return out, nil
}

func (p *Plugin) emit(ev wire.Event) {
	p.mu.Lock()
	env := p.env
	p.mu.Unlock()
	env.Emit(ev)
}

func (p *Plugin) setState(s *session, state, msg, hint string) {
	p.mu.Lock()
	s.state, s.err = state, msg
	if hint != "" {
		p.outdated = hint
	}
	p.mu.Unlock()
	p.emit(wire.Event{Kind: wire.EventStatus, Account: s.id, Status: &wire.AccountStatus{State: state, Error: msg, UpdateHint: hint}})
}

// Connect implements sdk.Plugin.
func (p *Plugin) Connect(ctx context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if s.client != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	db, dev, err := openDevice(ctx, s.dir)
	if err != nil {
		return err
	}
	if dev.ID == nil {
		db.Close()
		return wire.Errorf(wire.CodeNotLinked, "whatsapp session is not paired")
	}
	cli := whatsmeow.NewClient(dev, waLog.Noop)
	cli.AddEventHandler(func(evt any) { p.onEvent(s, cli, evt) })
	p.mu.Lock()
	s.db, s.client = db, cli
	p.mu.Unlock()
	p.setState(s, wire.StateConnecting, "", "")
	if err := cli.Connect(); err != nil {
		p.closeSession(s)
		return wire.Errorf(wire.CodeUnavailable, "connect: %v", err)
	}
	return nil
}

func (p *Plugin) closeSession(s *session) {
	p.mu.Lock()
	cli, db := s.client, s.db
	s.client, s.db = nil, nil
	p.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	if db != nil {
		db.Close()
	}
}

func (p *Plugin) client(ctx context.Context, id string) (*whatsmeow.Client, error) {
	s, err := p.session(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	cli := s.client
	p.mu.Unlock()
	if cli == nil {
		if err := p.Connect(ctx, id); err != nil {
			return nil, err
		}
		p.mu.Lock()
		cli = s.client
		p.mu.Unlock()
	}
	if cli == nil || !cli.IsConnected() {
		return cli, wire.Errorf(wire.CodeUnavailable, "whatsapp is not connected")
	}
	return cli, nil
}

// Disconnect implements sdk.Plugin.
func (p *Plugin) Disconnect(_ context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.closeSession(s)
	p.setState(s, wire.StateDisconnected, "", "")
	return nil
}

// Remove implements sdk.Plugin: logs out (removes the linked device on
// the phone) and deletes the session.
func (p *Plugin) Remove(ctx context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return nil
	}
	if cli, err := p.client(ctx, id); err == nil {
		_ = cli.Logout(ctx)
	}
	p.closeSession(s)
	p.mu.Lock()
	delete(p.accounts, id)
	p.mu.Unlock()
	return os.RemoveAll(s.dir)
}

// Shutdown implements sdk.Plugin.
func (p *Plugin) Shutdown(context.Context) error {
	p.mu.Lock()
	ss := make([]*session, 0, len(p.accounts))
	for _, s := range p.accounts {
		ss = append(ss, s)
	}
	p.mu.Unlock()
	for _, s := range ss {
		p.closeSession(s)
	}
	return nil
}

// Pair implements sdk.Plugin: QR code (refreshed by WhatsApp) or phone
// pairing code ("Link with phone number" on the phone).
func (p *Plugin) Pair(ctx context.Context, req wire.PairRequest, pr *sdk.Pairing) (wire.Account, error) {
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
	db, dev, err := openDevice(ctx, dir)
	if err != nil {
		return wire.Account{}, err
	}
	cli := whatsmeow.NewClient(dev, waLog.Noop)
	defer func() {
		cli.Disconnect()
		db.Close()
	}()
	qr, err := cli.GetQRChannel(ctx)
	if err != nil {
		return wire.Account{}, err
	}
	if err := cli.Connect(); err != nil {
		return wire.Account{}, err
	}
	codeSent := false
	for item := range qr {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if req.Method == wire.PairPhone {
				if !codeSent {
					phone := strings.TrimPrefix(strings.ReplaceAll(req.Phone, " ", ""), "+")
					code, err := cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (TuxMail)")
					if err != nil {
						return wire.Account{}, err
					}
					codeSent = true
					pr.Emit(wire.PairEvent{Kind: wire.PairEventCode, Code: code, ExpiresAt: time.Now().Add(3 * time.Minute),
						Message: "WhatsApp > Linked devices > Link with phone number"})
				}
				continue
			}
			pr.Emit(wire.PairEvent{Kind: wire.PairEventQR, QR: item.Code, ExpiresAt: time.Now().Add(item.Timeout),
				Message: "WhatsApp > Settings > Linked devices > Link a device"})
		case "success":
			ok = true
			s := &session{id: req.Account, dir: dir, state: wire.StateDisconnected}
			self := selfContact(cli.Store)
			p.mu.Lock()
			p.accounts[req.Account] = s
			p.mu.Unlock()
			return wire.Account{ID: req.Account, Self: self, State: wire.StateDisconnected, Channel: "whatsapp"}, nil
		case "timeout":
			return wire.Account{}, errors.New("QR code expired")
		case "error":
			return wire.Account{}, fmt.Errorf("pairing failed: %v", item.Error)
		default:
			if item.Error != nil {
				return wire.Account{}, item.Error
			}
			return wire.Account{}, fmt.Errorf("pairing ended: %s", item.Event)
		}
	}
	return wire.Account{}, ctx.Err()
}

func (p *Plugin) onEvent(s *session, cli *whatsmeow.Client, evt any) {
	self := types.EmptyJID
	if cli.Store.ID != nil {
		self = cli.Store.ID.ToNonAD()
	}
	names := func(j types.JID) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if j.Server == types.GroupServer {
			if gi, err := cli.GetGroupInfo(ctx, j); err == nil {
				return gi.Name
			}
			return ""
		}
		if c, err := cli.Store.Contacts.GetContact(ctx, j); err == nil && c.Found {
			return firstNonEmpty(c.FullName, c.FirstName, c.PushName, c.BusinessName)
		}
		return ""
	}
	switch v := evt.(type) {
	case *events.Connected:
		p.setState(s, wire.StateConnected, "", "")
	case *events.Disconnected:
		p.setState(s, wire.StateConnecting, "reconnecting", "")
	case *events.LoggedOut:
		p.setState(s, wire.StateLoggedOut, "logged out from the phone", "")
	case *events.StreamReplaced:
		p.setState(s, wire.StateError, "another client connected with this session", "")
	case *events.ClientOutdated:
		p.setState(s, wire.StateError, "WhatsApp rejected the client version", "update the WhatsApp plugin")
	case *events.Message:
		for _, ev := range Convert(s.id, self, v, names) {
			p.emit(ev)
		}
	case *events.HistorySync:
		// The phone sends the existing chats to a newly linked device.
		for _, ev := range ConvertHistory(s.id, self, v, names, func(chat types.JID, wm *waWeb.WebMessageInfo) (*events.Message, error) {
			return cli.ParseWebMessage(chat, wm)
		}) {
			p.emit(ev)
		}
	case *events.Receipt:
		if ev := ConvertReceipt(s.id, v); ev != nil {
			p.emit(*ev)
		}
	case *events.ChatPresence:
		chat := v.Chat.ToNonAD()
		sender := contactOf(v.Sender, v.SenderAlt, "")
		p.emit(wire.Event{Kind: wire.EventTyping, Account: s.id, Typing: &wire.Typing{ChatID: chat.String(), Sender: sender,
			Active: v.State == types.ChatPresenceComposing}})
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func parseChat(id string) (types.JID, error) {
	j, err := types.ParseJID(id)
	if err != nil || j.User == "" {
		return types.JID{}, wire.Errorf(wire.CodeInvalid, "invalid whatsapp chat %q", id)
	}
	return j, nil
}

// ListChats implements sdk.Plugin: groups and contacts (WhatsApp has no
// chat list API for companions; history arrives through history sync).
func (p *Plugin) ListChats(ctx context.Context, id string) ([]wire.Chat, error) {
	cli, err := p.client(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []wire.Chat
	groups, err := cli.GetJoinedGroups(ctx)
	if err == nil {
		for _, g := range groups {
			out = append(out, wire.Chat{ID: g.JID.String(), Kind: wire.ChatGroup, Title: g.Name})
		}
	}
	return out, nil
}

func (p *Plugin) throttle() {
	p.mu.Lock()
	wait := time.Until(p.lastSend.Add(minSendInterval))
	p.lastSend = time.Now().Add(max(wait, 0))
	p.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

// Send implements sdk.Plugin.
func (p *Plugin) Send(ctx context.Context, req wire.SendRequest) (wire.SendResult, error) {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return wire.SendResult{}, err
	}
	chatID := req.ChatID
	if chatID == "" {
		if req.To == nil || req.To.Phone == "" {
			return wire.SendResult{}, wire.Errorf(wire.CodeInvalid, "no recipient")
		}
		chatID = strings.TrimPrefix(req.To.Phone, "+") + "@" + types.DefaultUserServer
	}
	to, err := parseChat(chatID)
	if err != nil {
		return wire.SendResult{}, err
	}
	var reply *waE2E.ContextInfo
	if req.ReplyTo != "" {
		reply = &waE2E.ContextInfo{StanzaID: ptr(req.ReplyTo)}
	}
	var up *whatsmeow.UploadResponse
	if req.Media != nil {
		data, err := os.ReadFile(req.Media.Path)
		if err != nil {
			return wire.SendResult{}, err
		}
		r, err := cli.Upload(ctx, data, uploadType(req.Media.MIMEType))
		if err != nil {
			return wire.SendResult{}, err
		}
		up = &r
	}
	p.throttle()
	res, err := cli.SendMessage(ctx, to, buildMessage(req.Text, reply, up, req.Media))
	if err != nil {
		return wire.SendResult{}, err
	}
	return wire.SendResult{ChatID: to.String(), ID: res.ID, Time: res.Timestamp}, nil
}

func (p *Plugin) senderJID(cli *whatsmeow.Client, senderID string, fromMe bool) types.JID {
	if fromMe && cli.Store.ID != nil {
		return cli.Store.ID.ToNonAD()
	}
	if strings.HasPrefix(senderID, "+") {
		return types.NewJID(strings.TrimPrefix(senderID, "+"), types.DefaultUserServer)
	}
	j, _ := types.ParseJID(senderID)
	return j
}

// React implements sdk.Plugin.
func (p *Plugin) React(ctx context.Context, req wire.ReactRequest) error {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	chat, err := parseChat(req.ChatID)
	if err != nil {
		return err
	}
	emoji := req.Emoji
	if req.Remove {
		emoji = ""
	}
	_, err = cli.SendMessage(ctx, chat, cli.BuildReaction(chat, p.senderJID(cli, req.SenderID, req.FromMe), req.MessageID, emoji))
	return err
}

// Edit implements sdk.Plugin.
func (p *Plugin) Edit(ctx context.Context, req wire.EditRequest) error {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	chat, err := parseChat(req.ChatID)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, chat, cli.BuildEdit(chat, req.MessageID, &waE2E.Message{Conversation: ptr(req.Text)}))
	return err
}

// Delete implements sdk.Plugin (revoke for everyone; local deletes need
// no network call).
func (p *Plugin) Delete(ctx context.Context, req wire.DeleteRequest) error {
	if !req.ForEveryone {
		return nil
	}
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	chat, err := parseChat(req.ChatID)
	if err != nil {
		return err
	}
	_, err = cli.SendMessage(ctx, chat, cli.BuildRevoke(chat, p.senderJID(cli, req.SenderID, req.FromMe), req.MessageID))
	return err
}

// MarkRead implements sdk.Plugin.
func (p *Plugin) MarkRead(ctx context.Context, req wire.MarkReadRequest) error {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	chat, err := parseChat(req.ChatID)
	if err != nil {
		return err
	}
	sender := types.EmptyJID
	if chat.Server == types.GroupServer {
		sender = p.senderJID(cli, req.SenderID, false)
	}
	t := req.Time
	if t.IsZero() {
		t = time.Now()
	}
	return cli.MarkRead(ctx, req.MessageIDs, t, chat, sender)
}

// Typing implements sdk.Plugin.
func (p *Plugin) Typing(ctx context.Context, req wire.TypingRequest) error {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return err
	}
	chat, err := parseChat(req.ChatID)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if req.Active {
		state = types.ChatPresenceComposing
	}
	return cli.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText)
}

// DownloadMedia implements sdk.Plugin.
func (p *Plugin) DownloadMedia(ctx context.Context, req wire.MediaRequest) (wire.MediaResult, error) {
	cli, err := p.client(ctx, req.Account)
	if err != nil {
		return wire.MediaResult{}, err
	}
	r, err := decodeMediaRef(req.Ref)
	if err != nil {
		return wire.MediaResult{}, err
	}
	data, err := cli.DownloadMediaWithPath(ctx, r.DirectPath, r.EncSHA256, r.SHA256, r.MediaKey, r.Length, whatsmeow.MediaType(r.Type), r.MMSType)
	if err != nil {
		return wire.MediaResult{}, err
	}
	if err := os.WriteFile(req.Path, data, 0o600); err != nil {
		return wire.MediaResult{}, err
	}
	return wire.MediaResult{Size: int64(len(data))}, nil
}
