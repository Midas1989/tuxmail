// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fake is an in-memory messenger plugin for tests and the demo
// mode. It implements every call of the protocol without network access.
package fake

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Plugin is the fake plugin.
type Plugin struct {
	sdk.Unsupported
	Name    string
	Channel string

	mu       sync.Mutex
	env      *sdk.Env
	accounts map[string]*account
	// Sent records every SendRequest.
	Sent []wire.SendRequest
	// Calls records the method names of state changing calls
	// (react, edit, delete, mark_read, typing).
	Calls []string
	// MediaName is the file name DownloadMedia reports (tests of the host's
	// handling of plugin-supplied names).
	MediaName string
	// CrashOn makes the process exit when the named call arrives
	// (only meaningful when running as a separate process).
	CrashOn string
	now     func() time.Time
	seq     int
}

type account struct {
	wire.Account
	chats    map[string]*wire.Chat
	messages map[string][]wire.Message // chat -> oldest first
	media    map[string][]byte
}

// New creates a fake plugin with the given name and channel.
func New(name, channel string) *Plugin {
	return &Plugin{Name: name, Channel: channel, accounts: map[string]*account{}, now: time.Now}
}

// Info implements sdk.Plugin.
func (p *Plugin) Info() wire.HelloResult {
	return wire.HelloResult{Name: p.Name, DisplayName: "Fake " + p.Channel, Version: "1.0.0-test", Channel: p.Channel,
		Risk: wire.RiskLow, PairingMethods: []string{wire.PairQR, wire.PairPhone},
		Capabilities: []string{wire.CapMedia, wire.CapReactions, wire.CapReplies, wire.CapEdit, wire.CapDelete,
			wire.CapReadReceipts, wire.CapTyping, wire.CapHistory}}
}

// Init implements sdk.Plugin.
func (p *Plugin) Init(_ context.Context, env *sdk.Env) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = env
	if v := env.Config["crash_on"]; v != "" {
		p.CrashOn = v
	}
	if p.CrashOn == "hello" {
		os.Exit(3)
	}
	// Accounts paired in an earlier process survive a restart.
	dirs, _ := env.AccountDirs()
	for _, d := range dirs {
		if _, ok := p.accounts[d]; !ok {
			if b, err := os.ReadFile(env.DataDir + "/" + d + "/session"); err == nil {
				p.accounts[d] = newAccount(d, string(b), p.Channel)
			}
		}
	}
	return nil
}

func newAccount(id, phone, channel string) *account {
	return &account{Account: wire.Account{ID: id, Self: wire.Contact{ID: phone, Phone: phone, Name: "Me"}, State: wire.StateDisconnected,
		Channel: channel}, chats: map[string]*wire.Chat{}, messages: map[string][]wire.Message{}, media: map[string][]byte{}}
}

func (p *Plugin) crash(call string) {
	if p.CrashOn == call {
		os.Exit(3)
	}
}

func (p *Plugin) acc(id string) (*account, error) {
	a := p.accounts[id]
	if a == nil {
		return nil, wire.Errorf(wire.CodeNotLinked, "account %q is not linked", id)
	}
	return a, nil
}

// Accounts implements sdk.Plugin.
func (p *Plugin) Accounts(context.Context) ([]wire.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wire.Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, a.Account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Connect implements sdk.Plugin.
func (p *Plugin) Connect(_ context.Context, id string) error {
	p.mu.Lock()
	a, err := p.acc(id)
	if err == nil {
		a.State = wire.StateConnected
	}
	env := p.env
	p.mu.Unlock()
	if err == nil {
		env.Emit(wire.Event{Kind: wire.EventStatus, Account: id, Status: &wire.AccountStatus{State: wire.StateConnected}})
	}
	return err
}

// Disconnect implements sdk.Plugin.
func (p *Plugin) Disconnect(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, err := p.acc(id)
	if err == nil {
		a.State = wire.StateDisconnected
	}
	return err
}

// Remove implements sdk.Plugin.
func (p *Plugin) Remove(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.accounts, id)
	if dir, err := p.env.AccountDir(id); err == nil {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// Pair implements sdk.Plugin: it shows a QR code (or a pairing code for
// PairPhone) and waits for the input "confirm" (any value), then links an
// account whose own number is req.Phone (default +4900000000).
func (p *Plugin) Pair(ctx context.Context, req wire.PairRequest, pr *sdk.Pairing) (wire.Account, error) {
	switch req.Method {
	case wire.PairPhone:
		if req.Phone == "" {
			return wire.Account{}, wire.Errorf(wire.CodeInvalid, "phone number required")
		}
		pr.Emit(wire.PairEvent{Kind: wire.PairEventCode, Code: "ABCD-EFGH"})
	default:
		pr.QR("fake://pair/" + req.Account)
	}
	v, err := pr.Input(ctx, "confirm", "confirm on the phone")
	if err != nil {
		return wire.Account{}, err
	}
	if v == "fail" {
		return wire.Account{}, fmt.Errorf("pairing rejected")
	}
	phone := req.Phone
	if phone == "" {
		phone = "+4900000000"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	a := newAccount(req.Account, phone, p.Channel)
	p.accounts[req.Account] = a
	if dir, err := p.env.AccountDir(req.Account); err == nil {
		_ = os.WriteFile(dir+"/session", []byte(phone), 0o600)
	}
	return a.Account, nil
}

// ListChats implements sdk.Plugin.
func (p *Plugin) ListChats(_ context.Context, id string) ([]wire.Chat, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, err := p.acc(id)
	if err != nil {
		return nil, err
	}
	var out []wire.Chat
	for _, c := range a.chats {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// History implements sdk.Plugin.
func (p *Plugin) History(_ context.Context, req wire.HistoryRequest) ([]wire.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, err := p.acc(req.Account)
	if err != nil {
		return nil, err
	}
	msgs := a.messages[req.ChatID]
	end := len(msgs)
	if req.Before != "" {
		for i, m := range msgs {
			if m.ID == req.Before {
				end = i
			}
		}
	}
	var out []wire.Message
	for i := end - 1; i >= 0 && len(out) < req.Limit; i-- {
		out = append(out, msgs[i])
	}
	return out, nil
}

// Send implements sdk.Plugin.
func (p *Plugin) Send(_ context.Context, req wire.SendRequest) (wire.SendResult, error) {
	p.crash("send")
	p.mu.Lock()
	a, err := p.acc(req.Account)
	if err != nil {
		p.mu.Unlock()
		return wire.SendResult{}, err
	}
	chat := req.ChatID
	if chat == "" {
		if req.To == nil || req.To.Phone == "" {
			p.mu.Unlock()
			return wire.SendResult{}, wire.Errorf(wire.CodeInvalid, "no recipient")
		}
		chat = req.To.Phone
		if a.chats[chat] == nil {
			a.chats[chat] = &wire.Chat{ID: chat, Kind: wire.ChatDirect, Peer: req.To}
		}
	}
	p.seq++
	res := wire.SendResult{ChatID: chat, ID: fmt.Sprintf("out-%d", p.seq), Time: p.now().UTC().Truncate(time.Second)}
	m := wire.Message{ChatID: chat, ID: res.ID, Sender: a.Self, FromMe: true, Time: res.Time, Text: req.Text, ReplyTo: req.ReplyTo,
		Status: wire.StatusSent}
	if req.Media != nil {
		b, _ := os.ReadFile(req.Media.Path)
		ref := "media-" + res.ID
		a.media[ref] = b
		m.Media = &wire.Media{Ref: ref, MIMEType: req.Media.MIMEType, Name: req.Media.Name, Size: int64(len(b))}
	}
	a.messages[chat] = append(a.messages[chat], m)
	p.Sent = append(p.Sent, req)
	env := p.env
	p.mu.Unlock()
	// The network acknowledges delivery.
	env.Emit(wire.Event{Kind: wire.EventReceipt, Account: req.Account, Receipt: &wire.Receipt{ChatID: chat, MessageIDs: []string{res.ID},
		Kind: wire.ReceiptDelivered, Time: res.Time}})
	return res, nil
}

func (p *Plugin) record(call string) {
	p.crash(call)
	p.mu.Lock()
	p.Calls = append(p.Calls, call)
	p.mu.Unlock()
}

// React implements sdk.Plugin.
func (p *Plugin) React(_ context.Context, req wire.ReactRequest) error {
	p.record(fmt.Sprintf("react %s %s %s remove=%v", req.ChatID, req.MessageID, req.Emoji, req.Remove))
	return nil
}

// Edit implements sdk.Plugin.
func (p *Plugin) Edit(_ context.Context, req wire.EditRequest) error {
	p.record(fmt.Sprintf("edit %s %s %s", req.ChatID, req.MessageID, req.Text))
	return nil
}

// Delete implements sdk.Plugin.
func (p *Plugin) Delete(_ context.Context, req wire.DeleteRequest) error {
	p.record(fmt.Sprintf("delete %s %s everyone=%v", req.ChatID, req.MessageID, req.ForEveryone))
	return nil
}

// MarkRead implements sdk.Plugin.
func (p *Plugin) MarkRead(_ context.Context, req wire.MarkReadRequest) error {
	p.record(fmt.Sprintf("mark_read %s %s", req.ChatID, strings.Join(req.MessageIDs, ",")))
	return nil
}

// Typing implements sdk.Plugin.
func (p *Plugin) Typing(_ context.Context, req wire.TypingRequest) error {
	p.record(fmt.Sprintf("typing %s %v", req.ChatID, req.Active))
	return nil
}

// DownloadMedia implements sdk.Plugin.
func (p *Plugin) DownloadMedia(_ context.Context, req wire.MediaRequest) (wire.MediaResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, err := p.acc(req.Account)
	if err != nil {
		return wire.MediaResult{}, err
	}
	b, ok := a.media[req.Ref]
	if !ok {
		return wire.MediaResult{}, wire.Errorf(wire.CodeNotFound, "unknown media %q", req.Ref)
	}
	if err := os.WriteFile(req.Path, b, 0o600); err != nil {
		return wire.MediaResult{}, err
	}
	return wire.MediaResult{Size: int64(len(b)), Name: p.MediaName}, nil
}

// Shutdown implements sdk.Plugin.
func (p *Plugin) Shutdown(context.Context) error { return nil }

// Receive simulates an incoming message: it is stored (for History) and
// emitted as event. Media data is kept for DownloadMedia.
func (p *Plugin) Receive(accountID string, chat wire.Chat, m wire.Message, media []byte) {
	p.mu.Lock()
	a := p.accounts[accountID]
	if a == nil {
		p.mu.Unlock()
		return
	}
	if a.chats[chat.ID] == nil {
		c := chat
		a.chats[chat.ID] = &c
	}
	m.ChatID = chat.ID
	if m.Media != nil && media != nil {
		a.media[m.Media.Ref] = media
	}
	a.messages[chat.ID] = append(a.messages[chat.ID], m)
	env := p.env
	p.mu.Unlock()
	env.Emit(wire.Event{Kind: wire.EventChat, Account: accountID, Chat: &chat})
	env.Emit(wire.Event{Kind: wire.EventMessage, Account: accountID, Message: &m})
}

// Emit sends an arbitrary event (reactions, receipts, typing ...).
func (p *Plugin) Emit(ev wire.Event) {
	p.mu.Lock()
	env := p.env
	p.mu.Unlock()
	env.Emit(ev)
}

// SetNow replaces the clock.
func (p *Plugin) SetNow(now func() time.Time) { p.now = now }

// CallLog returns a copy of Calls.
func (p *Plugin) CallLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.Calls...)
}

// SentLog returns a copy of Sent.
func (p *Plugin) SentLog() []wire.SendRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]wire.SendRequest(nil), p.Sent...)
}
