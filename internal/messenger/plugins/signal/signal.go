// SPDX-License-Identifier: AGPL-3.0-or-later

// Package signal is the Signal messenger plugin. It drives signal-cli
// (https://github.com/AsamK/signal-cli, GPL-3.0, external binary) in JSON-RPC
// mode, one signal-cli process per account with its data in the account
// directory, and links TuxMail as a secondary device (QR code with the
// sgnl://linkdevice URI).
//
// signal-cli is used instead of signalmeow (mautrix-signal) because
// signalmeow needs libsignal-ffi (Rust, linked through CGO), which would
// break the CGO-free build of the plugins. The path of signal-cli is taken
// from the setting signal_cli_path (or TUXMAIL_SIGNAL_SIGNAL_CLI_PATH),
// otherwise "signal-cli" is looked up in PATH.
package signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Version of the plugin.
const Version = "0.1.0"

// Spawner starts signal-cli in JSON-RPC mode with its data in configDir.
type Spawner func(ctx context.Context, configDir string) (CLI, error)

// CLI is a running signal-cli process.
type CLI interface {
	io.Reader
	io.WriteCloser
	// Kill stops the process.
	Kill() error
}

// Plugin implements sdk.Plugin.
type Plugin struct {
	sdk.Unsupported
	Spawn Spawner

	mu       sync.Mutex
	env      *sdk.Env
	accounts map[string]*session
}

type session struct {
	id     string
	number string
	uuid   string
	cli    CLI
	conn   *wire.Conn
	state  string
	err    string
	dir    string
	groups map[string]string // id -> name
}

// New creates the plugin.
func New() *Plugin { return &Plugin{accounts: map[string]*session{}} }

// Info implements sdk.Plugin.
func (p *Plugin) Info() wire.HelloResult {
	return wire.HelloResult{Name: "signal", DisplayName: "Signal (signal-cli)", Version: Version, Channel: "signal", Risk: wire.RiskLow,
		PairingMethods: []string{wire.PairQR},
		Capabilities: []string{wire.CapMedia, wire.CapReactions, wire.CapReplies, wire.CapDelete, wire.CapReadReceipts, wire.CapTyping,
			wire.CapEdit, wire.CapE2EE}}
}

// Init implements sdk.Plugin.
func (p *Plugin) Init(_ context.Context, env *sdk.Env) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = env
	if p.Spawn == nil {
		path := env.Config["signal_cli_path"]
		if path == "" {
			path = os.Getenv("TUXMAIL_SIGNAL_SIGNAL_CLI_PATH")
		}
		if path == "" {
			path = "signal-cli"
		}
		p.Spawn = ExecSpawner(path)
	}
	dirs, err := env.AccountDirs()
	if err != nil {
		return err
	}
	for _, d := range dirs {
		var meta accountMeta
		b, err := os.ReadFile(filepath.Join(env.DataDir, d, "account.json"))
		if err != nil || json.Unmarshal(b, &meta) != nil || meta.Number == "" {
			continue
		}
		p.accounts[d] = &session{id: d, number: meta.Number, uuid: meta.UUID, state: wire.StateDisconnected,
			dir: filepath.Join(env.DataDir, d), groups: map[string]string{}}
	}
	return nil
}

type accountMeta struct {
	Number string `json:"number"`
	UUID   string `json:"uuid,omitempty"`
}

// ExecSpawner runs the signal-cli executable at path.
func ExecSpawner(path string) Spawner {
	return func(ctx context.Context, dir string) (CLI, error) {
		cmd := exec.Command(path, "--config", filepath.Join(dir, "signal-cli"), "--output=json", "jsonRpc", "--receive-mode=on-start")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, wire.Errorf(wire.CodeUnavailable, "signal-cli not found (%s): install it and set signal_cli_path", path)
			}
			return nil, err
		}
		return &execCLI{Reader: stdout, WriteCloser: stdin, cmd: cmd}, nil
	}
}

type execCLI struct {
	io.Reader
	io.WriteCloser
	cmd *exec.Cmd
}

func (c *execCLI) Kill() error {
	_ = c.WriteCloser.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		return c.cmd.Process.Kill()
	}
}

func (p *Plugin) session(id string) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.accounts[id]
	if s == nil {
		return nil, wire.Errorf(wire.CodeNotLinked, "signal account %q is not linked", id)
	}
	return s, nil
}

// connected returns the session with a running signal-cli.
func (p *Plugin) connected(ctx context.Context, id string) (*session, error) {
	s, err := p.session(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	conn := s.conn
	p.mu.Unlock()
	if conn != nil {
		return s, nil
	}
	if err := p.Connect(ctx, id); err != nil {
		return nil, err
	}
	return s, nil
}

// Accounts implements sdk.Plugin.
func (p *Plugin) Accounts(context.Context) ([]wire.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]wire.Account, 0, len(p.accounts))
	for _, s := range p.accounts {
		out = append(out, wire.Account{ID: s.id, Self: wire.Contact{ID: s.selfID(), Phone: s.number}, State: s.state, Channel: "signal", Error: s.err})
	}
	return out, nil
}

// selfID is the own author id in message ids (the number, as signal-cli
// reports authors by number first).
func (s *session) selfID() string {
	if s.number != "" {
		return s.number
	}
	return s.uuid
}

func (p *Plugin) start(ctx context.Context, dir string, handler wire.Handler) (CLI, *wire.Conn, error) {
	cli, err := p.Spawn(ctx, dir)
	if err != nil {
		return nil, nil, err
	}
	return cli, wire.NewConn(cli, cli, handler), nil
}

// Connect implements sdk.Plugin: it starts signal-cli for the account.
func (p *Plugin) Connect(ctx context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if s.conn != nil {
		p.mu.Unlock()
		return nil
	}
	s.state = wire.StateConnecting
	p.mu.Unlock()
	cli, conn, err := p.start(ctx, s.dir, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "receive" {
			p.onReceive(id, params)
		}
		return nil, nil
	})
	if err != nil {
		p.setState(s, wire.StateError, err.Error())
		return err
	}
	p.mu.Lock()
	s.cli, s.conn = cli, conn
	p.mu.Unlock()
	go func() {
		<-conn.Done()
		p.mu.Lock()
		stillCurrent := s.conn == conn
		if stillCurrent {
			s.conn, s.cli = nil, nil
		}
		p.mu.Unlock()
		if stillCurrent {
			p.setState(s, wire.StateDisconnected, "signal-cli exited")
		}
	}()
	// Groups for titles.
	var groups []cliGroup
	if err := conn.Call(ctx, "listGroups", map[string]any{"account": s.number}, &groups); err == nil {
		p.mu.Lock()
		for _, g := range groups {
			s.groups[g.ID] = g.Name
		}
		p.mu.Unlock()
	}
	p.setState(s, wire.StateConnected, "")
	return nil
}

func (p *Plugin) setState(s *session, state, msg string) {
	p.mu.Lock()
	s.state, s.err = state, msg
	env := p.env
	p.mu.Unlock()
	env.Emit(wire.Event{Kind: wire.EventStatus, Account: s.id, Status: &wire.AccountStatus{State: state, Error: msg}})
}

// Disconnect implements sdk.Plugin.
func (p *Plugin) Disconnect(_ context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return err
	}
	p.stopCLI(s)
	p.setState(s, wire.StateDisconnected, "")
	return nil
}

func (p *Plugin) stopCLI(s *session) {
	p.mu.Lock()
	cli, conn := s.cli, s.conn
	s.cli, s.conn = nil, nil
	p.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if cli != nil {
		_ = cli.Kill()
	}
}

// Remove implements sdk.Plugin: the linked device is removed locally (the
// phone shows it until the user unlinks it there).
func (p *Plugin) Remove(ctx context.Context, id string) error {
	s, err := p.session(id)
	if err != nil {
		return nil
	}
	if conn := s.conn; conn != nil {
		_ = conn.Call(ctx, "unregister", map[string]any{"account": s.number}, nil)
	}
	p.stopCLI(s)
	p.mu.Lock()
	delete(p.accounts, id)
	p.mu.Unlock()
	return os.RemoveAll(s.dir)
}

// Shutdown implements sdk.Plugin.
func (p *Plugin) Shutdown(context.Context) error {
	p.mu.Lock()
	sessions := make([]*session, 0, len(p.accounts))
	for _, s := range p.accounts {
		sessions = append(sessions, s)
	}
	p.mu.Unlock()
	for _, s := range sessions {
		p.stopCLI(s)
	}
	return nil
}

// Pair implements sdk.Plugin: startLink returns the sgnl:// URI shown as QR
// code; finishLink waits until the phone scanned it.
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
	cli, conn, err := p.start(ctx, dir, nil)
	if err != nil {
		return wire.Account{}, err
	}
	defer func() {
		conn.Close()
		_ = cli.Kill()
	}()
	var link struct {
		DeviceLinkURI string `json:"deviceLinkUri"`
	}
	if err := conn.Call(ctx, "startLink", map[string]any{}, &link); err != nil {
		return wire.Account{}, err
	}
	if link.DeviceLinkURI == "" {
		return wire.Account{}, errors.New("signal-cli returned no link URI")
	}
	pr.Emit(wire.PairEvent{Kind: wire.PairEventQR, QR: link.DeviceLinkURI, ExpiresAt: time.Now().Add(90 * time.Second),
		Message: "Signal > Settings > Linked devices > Link new device"})
	var fin struct {
		Number string `json:"number"`
		UUID   string `json:"uuid"`
	}
	if err := conn.Call(ctx, "finishLink", map[string]any{"deviceLinkUri": link.DeviceLinkURI, "deviceName": "TuxMail"}, &fin); err != nil {
		return wire.Account{}, err
	}
	if fin.Number == "" {
		var accs []struct {
			Number string `json:"number"`
		}
		if err := conn.Call(ctx, "listAccounts", nil, &accs); err == nil && len(accs) > 0 {
			fin.Number = accs[0].Number
		}
	}
	if fin.Number == "" {
		return wire.Account{}, errors.New("linking finished without account number")
	}
	b, _ := json.Marshal(accountMeta{Number: fin.Number, UUID: fin.UUID})
	if err := os.WriteFile(filepath.Join(dir, "account.json"), b, 0o600); err != nil {
		return wire.Account{}, err
	}
	s := &session{id: req.Account, number: fin.Number, uuid: fin.UUID, state: wire.StateDisconnected, dir: dir, groups: map[string]string{}}
	p.mu.Lock()
	p.accounts[req.Account] = s
	p.mu.Unlock()
	ok = true
	return wire.Account{ID: req.Account, Self: wire.Contact{ID: s.selfID(), Phone: fin.Number}, State: wire.StateDisconnected, Channel: "signal"}, nil
}

// ==== ids ======================================================================

// Chat ids: a direct chat is the peer's number (or "u:<uuid>"), a group is
// "g:<groupId>". Message ids are "<timestamp>@<author>" because Signal
// addresses messages by author and timestamp.

func groupChat(id string) string { return "g:" + id }

func directChat(number, uuid string) string {
	if number != "" {
		return number
	}
	return "u:" + uuid
}

// recipient returns the signal-cli target params of a chat.
func recipient(chatID string) map[string]any {
	switch {
	case strings.HasPrefix(chatID, "g:"):
		return map[string]any{"groupId": strings.TrimPrefix(chatID, "g:")}
	case strings.HasPrefix(chatID, "u:"):
		return map[string]any{"recipient": []string{strings.TrimPrefix(chatID, "u:")}}
	}
	return map[string]any{"recipient": []string{chatID}}
}

func messageID(ts int64, author string) string { return strconv.FormatInt(ts, 10) + "@" + author }

func parseMessageID(id string) (ts int64, author string, err error) {
	t, a, _ := strings.Cut(id, "@")
	ts, err = strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, "", wire.Errorf(wire.CodeInvalid, "invalid signal message id %q", id)
	}
	return ts, a, nil
}

func msTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// ==== calls ===================================================================

func withAccount(s *session, m map[string]any) map[string]any {
	m["account"] = s.number
	return m
}

type cliGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cliContact struct {
	Number      string `json:"number"`
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	ProfileName string `json:"profileName"`
	Profile     *struct {
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"profile"`
}

func (c cliContact) displayName() string {
	if c.Name != "" {
		return c.Name
	}
	if c.Profile != nil {
		if n := strings.TrimSpace(c.Profile.GivenName + " " + c.Profile.FamilyName); n != "" {
			return n
		}
	}
	return c.ProfileName
}

// ListChats implements sdk.Plugin (contacts with a number and groups).
func (p *Plugin) ListChats(ctx context.Context, id string) ([]wire.Chat, error) {
	s, err := p.connected(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []wire.Chat
	var groups []cliGroup
	if err := s.conn.Call(ctx, "listGroups", withAccount(s, map[string]any{}), &groups); err != nil {
		return nil, err
	}
	for _, g := range groups {
		out = append(out, wire.Chat{ID: groupChat(g.ID), Kind: wire.ChatGroup, Title: g.Name})
	}
	var contacts []cliContact
	if err := s.conn.Call(ctx, "listContacts", withAccount(s, map[string]any{}), &contacts); err != nil {
		return nil, err
	}
	for _, c := range contacts {
		if c.Number == "" && c.UUID == "" {
			continue
		}
		peer := wire.Contact{ID: directChat(c.Number, c.UUID), Name: c.displayName(), Phone: c.Number}
		out = append(out, wire.Chat{ID: directChat(c.Number, c.UUID), Kind: wire.ChatDirect, Title: peer.Name, Peer: &peer})
	}
	return out, nil
}

// Send implements sdk.Plugin.
func (p *Plugin) Send(ctx context.Context, req wire.SendRequest) (wire.SendResult, error) {
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return wire.SendResult{}, err
	}
	chat := req.ChatID
	if chat == "" {
		if req.To == nil || (req.To.Phone == "" && req.To.ID == "") {
			return wire.SendResult{}, wire.Errorf(wire.CodeInvalid, "no recipient")
		}
		chat = req.To.Phone
		if chat == "" {
			chat = req.To.ID
		}
	}
	params := withAccount(s, recipient(chat))
	params["message"] = req.Text
	if req.Media != nil {
		params["attachments"] = []string{req.Media.Path}
	}
	if req.ReplyTo != "" {
		ts, author, err := parseMessageID(req.ReplyTo)
		if err == nil {
			params["quoteTimestamp"], params["quoteAuthor"] = ts, author
		}
	}
	var res struct {
		Timestamp int64 `json:"timestamp"`
	}
	if err := s.conn.Call(ctx, "send", params, &res); err != nil {
		return wire.SendResult{}, err
	}
	return wire.SendResult{ChatID: chat, ID: messageID(res.Timestamp, s.selfID()), Time: msTime(res.Timestamp)}, nil
}

// React implements sdk.Plugin.
func (p *Plugin) React(ctx context.Context, req wire.ReactRequest) error {
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return err
	}
	ts, author, err := parseMessageID(req.MessageID)
	if err != nil {
		return err
	}
	params := withAccount(s, recipient(req.ChatID))
	params["emoji"], params["targetAuthor"], params["targetTimestamp"], params["remove"] = req.Emoji, author, ts, req.Remove
	return s.conn.Call(ctx, "sendReaction", params, nil)
}

// Edit implements sdk.Plugin.
func (p *Plugin) Edit(ctx context.Context, req wire.EditRequest) error {
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return err
	}
	ts, _, err := parseMessageID(req.MessageID)
	if err != nil {
		return err
	}
	params := withAccount(s, recipient(req.ChatID))
	params["message"], params["editTimestamp"] = req.Text, ts
	return s.conn.Call(ctx, "send", params, nil)
}

// Delete implements sdk.Plugin (only own messages can be deleted remotely).
func (p *Plugin) Delete(ctx context.Context, req wire.DeleteRequest) error {
	if !req.ForEveryone {
		return nil // local deletion only
	}
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return err
	}
	ts, _, err := parseMessageID(req.MessageID)
	if err != nil {
		return err
	}
	params := withAccount(s, recipient(req.ChatID))
	params["targetTimestamp"] = ts
	return s.conn.Call(ctx, "remoteDelete", params, nil)
}

// MarkRead implements sdk.Plugin (read receipts per author).
func (p *Plugin) MarkRead(ctx context.Context, req wire.MarkReadRequest) error {
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return err
	}
	byAuthor := map[string][]int64{}
	for _, id := range req.MessageIDs {
		ts, author, err := parseMessageID(id)
		if err == nil && author != "" {
			byAuthor[author] = append(byAuthor[author], ts)
		}
	}
	for author, tss := range byAuthor {
		params := withAccount(s, map[string]any{"recipient": author, "targetTimestamp": tss, "type": "read"})
		if err := s.conn.Call(ctx, "sendReceipt", params, nil); err != nil {
			return err
		}
	}
	return nil
}

// Typing implements sdk.Plugin.
func (p *Plugin) Typing(ctx context.Context, req wire.TypingRequest) error {
	s, err := p.connected(ctx, req.Account)
	if err != nil {
		return err
	}
	params := withAccount(s, recipient(req.ChatID))
	params["stop"] = !req.Active
	return s.conn.Call(ctx, "sendTyping", params, nil)
}

// DownloadMedia implements sdk.Plugin: signal-cli stores received
// attachments in its data directory (attachments/<id>).
func (p *Plugin) DownloadMedia(_ context.Context, req wire.MediaRequest) (wire.MediaResult, error) {
	s, err := p.session(req.Account)
	if err != nil {
		return wire.MediaResult{}, err
	}
	if req.Ref == "" || filepath.Base(req.Ref) != req.Ref || strings.HasPrefix(req.Ref, ".") {
		return wire.MediaResult{}, wire.Errorf(wire.CodeInvalid, "invalid attachment id")
	}
	src := filepath.Join(s.dir, "signal-cli", "attachments", req.Ref)
	in, err := os.Open(src)
	if err != nil {
		return wire.MediaResult{}, wire.Errorf(wire.CodeNotFound, "attachment %s not found", req.Ref)
	}
	defer in.Close()
	out, err := os.Create(req.Path)
	if err != nil {
		return wire.MediaResult{}, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return wire.MediaResult{Size: n}, err
}

// ==== receive ==================================================================

type envelope struct {
	Source       string `json:"source"`
	SourceNumber string `json:"sourceNumber"`
	SourceUUID   string `json:"sourceUuid"`
	SourceName   string `json:"sourceName"`
	Timestamp    int64  `json:"timestamp"`
	DataMessage  *data  `json:"dataMessage"`
	EditMessage  *struct {
		TargetSentTimestamp int64 `json:"targetSentTimestamp"`
		DataMessage         data  `json:"dataMessage"`
	} `json:"editMessage"`
	ReceiptMessage *struct {
		When       int64   `json:"when"`
		IsDelivery bool    `json:"isDelivery"`
		IsRead     bool    `json:"isRead"`
		Timestamps []int64 `json:"timestamps"`
	} `json:"receiptMessage"`
	TypingMessage *struct {
		Action  string `json:"action"`
		GroupID string `json:"groupId"`
	} `json:"typingMessage"`
	SyncMessage *struct {
		SentMessage *struct {
			Destination       string `json:"destination"`
			DestinationNumber string `json:"destinationNumber"`
			DestinationUUID   string `json:"destinationUuid"`
			data
		} `json:"sentMessage"`
		ReadMessages []struct {
			Sender       string `json:"sender"`
			SenderNumber string `json:"senderNumber"`
			SenderUUID   string `json:"senderUuid"`
			Timestamp    int64  `json:"timestamp"`
		} `json:"readMessages"`
	} `json:"syncMessage"`
}

type data struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
	GroupInfo *struct {
		GroupID string `json:"groupId"`
	} `json:"groupInfo"`
	Attachments []struct {
		ContentType string `json:"contentType"`
		Filename    string `json:"filename"`
		ID          string `json:"id"`
		Size        int64  `json:"size"`
	} `json:"attachments"`
	Quote *struct {
		ID           int64  `json:"id"`
		Author       string `json:"author"`
		AuthorNumber string `json:"authorNumber"`
		AuthorUUID   string `json:"authorUuid"`
	} `json:"quote"`
	Reaction *struct {
		Emoji               string `json:"emoji"`
		TargetAuthor        string `json:"targetAuthor"`
		TargetAuthorNumber  string `json:"targetAuthorNumber"`
		TargetAuthorUUID    string `json:"targetAuthorUuid"`
		TargetSentTimestamp int64  `json:"targetSentTimestamp"`
		IsRemove            bool   `json:"isRemove"`
	} `json:"reaction"`
	RemoteDelete *struct {
		Timestamp int64 `json:"timestamp"`
	} `json:"remoteDelete"`
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (p *Plugin) onReceive(account string, params json.RawMessage) {
	var r struct {
		Envelope envelope `json:"envelope"`
		Account  string   `json:"account"`
	}
	if err := json.Unmarshal(params, &r); err != nil {
		return
	}
	s, err := p.session(account)
	if err != nil {
		return
	}
	p.mu.Lock()
	groups := s.groups
	self := s.selfID()
	env := p.env
	p.mu.Unlock()
	for _, ev := range Convert(account, self, r.Envelope, func(id string) string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return groups[id]
	}) {
		env.Emit(ev)
	}
}

// Convert maps a signal-cli envelope to plugin events (exported for tests).
func Convert(account, self string, e envelope, groupName func(string) string) []wire.Event {
	sender := wire.Contact{ID: firstNonEmpty(e.SourceUUID, e.SourceNumber, e.Source), Name: e.SourceName, Phone: e.SourceNumber}
	author := firstNonEmpty(e.SourceNumber, e.SourceUUID, e.Source)
	chatOf := func(d *data, peerNumber, peerUUID string) wire.Chat {
		if d != nil && d.GroupInfo != nil && d.GroupInfo.GroupID != "" {
			return wire.Chat{ID: groupChat(d.GroupInfo.GroupID), Kind: wire.ChatGroup, Title: groupName(d.GroupInfo.GroupID)}
		}
		peer := wire.Contact{ID: firstNonEmpty(peerUUID, peerNumber), Phone: peerNumber}
		if peerNumber == e.SourceNumber && peerUUID == e.SourceUUID {
			peer.Name = e.SourceName
		}
		return wire.Chat{ID: directChat(peerNumber, peerUUID), Kind: wire.ChatDirect, Peer: &peer, Title: peer.Name}
	}
	var out []wire.Event
	msgEvent := func(kind string, chat wire.Chat, d data, fromMe bool, msgAuthor string, ts int64) {
		m := wire.Message{ChatID: chat.ID, ID: messageID(ts, msgAuthor), Sender: sender, FromMe: fromMe, Time: msTime(d.Timestamp), Text: d.Message}
		if fromMe {
			m.Sender = wire.Contact{ID: self}
		}
		if d.Quote != nil {
			m.ReplyTo = messageID(d.Quote.ID, firstNonEmpty(d.Quote.AuthorNumber, d.Quote.AuthorUUID, d.Quote.Author))
		}
		if len(d.Attachments) > 0 {
			at := d.Attachments[0]
			m.Media = &wire.Media{Ref: at.ID, MIMEType: at.ContentType, Name: at.Filename, Size: at.Size}
		}
		if kind == wire.EventEdit {
			m.EditedAt = msTime(d.Timestamp)
		}
		out = append(out, wire.Event{Kind: wire.EventChat, Account: account, Chat: &chat},
			wire.Event{Kind: kind, Account: account, Message: &m})
		// Further attachments become their own messages.
		for i, at := range d.Attachments[min(1, len(d.Attachments)):] {
			mm := m
			mm.ID, mm.Text = fmt.Sprintf("%s#%d", m.ID, i+1), ""
			mm.Media = &wire.Media{Ref: at.ID, MIMEType: at.ContentType, Name: at.Filename, Size: at.Size}
			out = append(out, wire.Event{Kind: kind, Account: account, Message: &mm})
		}
	}
	switch {
	case e.DataMessage != nil:
		d := *e.DataMessage
		chat := chatOf(&d, e.SourceNumber, e.SourceUUID)
		switch {
		case d.Reaction != nil:
			r := d.Reaction
			emoji := r.Emoji
			if r.IsRemove {
				emoji = ""
			}
			out = append(out, wire.Event{Kind: wire.EventReaction, Account: account, Reaction: &wire.ReactionEvent{ChatID: chat.ID,
				MessageID: messageID(r.TargetSentTimestamp, firstNonEmpty(r.TargetAuthorNumber, r.TargetAuthorUUID, r.TargetAuthor)),
				Sender:    sender, Emoji: emoji}})
		case d.RemoteDelete != nil:
			out = append(out, wire.Event{Kind: wire.EventDelete, Account: account, ChatID: chat.ID, MessageID: messageID(d.RemoteDelete.Timestamp, author)})
		case d.Message != "" || len(d.Attachments) > 0:
			msgEvent(wire.EventMessage, chat, d, false, author, d.Timestamp)
		}
	case e.EditMessage != nil:
		d := e.EditMessage.DataMessage
		msgEvent(wire.EventEdit, chatOf(&d, e.SourceNumber, e.SourceUUID), d, false, author, e.EditMessage.TargetSentTimestamp)
	case e.ReceiptMessage != nil:
		r := e.ReceiptMessage
		kind := wire.ReceiptDelivered
		if r.IsRead {
			kind = wire.ReceiptRead
		}
		ids := make([]string, 0, len(r.Timestamps))
		for _, ts := range r.Timestamps {
			ids = append(ids, messageID(ts, self))
		}
		chat := directChat(e.SourceNumber, e.SourceUUID)
		out = append(out, wire.Event{Kind: wire.EventReceipt, Account: account, Receipt: &wire.Receipt{ChatID: chat, MessageIDs: ids, Kind: kind,
			Time: msTime(r.When)}})
	case e.TypingMessage != nil:
		chat := directChat(e.SourceNumber, e.SourceUUID)
		if e.TypingMessage.GroupID != "" {
			chat = groupChat(e.TypingMessage.GroupID)
		}
		out = append(out, wire.Event{Kind: wire.EventTyping, Account: account, Typing: &wire.Typing{ChatID: chat, Sender: sender,
			Active: e.TypingMessage.Action == "STARTED"}})
	case e.SyncMessage != nil && e.SyncMessage.SentMessage != nil:
		sm := e.SyncMessage.SentMessage
		chat := chatOf(&sm.data, firstNonEmpty(sm.DestinationNumber, sm.Destination), sm.DestinationUUID)
		if chat.Peer != nil {
			chat.Peer.Name, chat.Title = "", ""
		}
		if sm.Reaction == nil && sm.RemoteDelete == nil {
			msgEvent(wire.EventMessage, chat, sm.data, true, self, sm.Timestamp)
		}
	case e.SyncMessage != nil && len(e.SyncMessage.ReadMessages) > 0:
		for _, rm := range e.SyncMessage.ReadMessages {
			who := firstNonEmpty(rm.SenderNumber, rm.SenderUUID, rm.Sender)
			out = append(out, wire.Event{Kind: wire.EventReceipt, Account: account, Receipt: &wire.Receipt{
				ChatID: directChat(rm.SenderNumber, rm.SenderUUID), MessageIDs: []string{messageID(rm.Timestamp, who)}, Kind: wire.ReceiptRead,
				Time: msTime(rm.Timestamp), FromMe: true}})
		}
	}
	return out
}
