// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sdk is the plugin side of the messenger protocol (package wire).
// A plugin binary implements Plugin and calls Serve from main:
//
//	func main() { sdk.Main(telegram.New()) }
//
// Serve reads requests from stdin, writes answers and events to stdout and
// logs to stderr (the host forwards stderr to its log). It returns when the
// host closes stdin or sends "shutdown".
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

const (
	shutdownTimeout = 10 * time.Second
	pairingTimeout  = 10 * time.Minute
)

// Env is handed to Plugin.Init.
type Env struct {
	DataDir     string
	Config      map[string]string
	HostVersion string
	Logger      *slog.Logger
	// NetPolicy is the outbound policy the host handed over (nil: none).
	// The sdk has already installed it for the process (ApplyNetPolicy).
	NetPolicy *wire.NetPolicy
	emit      func(wire.Event)
}

// ApplyNetPolicy installs the host's outbound connection policy in this
// process: every dial of http.DefaultTransport (and therefore of every
// http.Client without its own transport, e.g. the Matrix client talking to a
// user-chosen homeserver) is checked against it after DNS resolution
// (internal/netguard). A nil policy leaves the process unrestricted. A policy
// that is already installed (plugin running inside the server process, as in
// tests) is kept. Entries that cannot be parsed are an error: the plugin must
// not start with a weaker policy than the host intended.
func ApplyNetPolicy(np *wire.NetPolicy) error {
	if np == nil || netguard.Current() != nil {
		return nil
	}
	p := &netguard.Policy{AllowPrivate: np.AllowPrivate}
	for _, s := range np.Allowed {
		r, err := netguard.ParseRule(s)
		if err != nil {
			return wire.Errorf(wire.CodeInvalidParams, "netPolicy: %v", err)
		}
		p.Allowed = append(p.Allowed, r)
	}
	netguard.SetPolicy(p)
	return nil
}

// Emit sends an event to the host (safe for concurrent use).
func (e *Env) Emit(ev wire.Event) {
	if e.emit != nil {
		e.emit(ev)
	}
}

var accountIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidAccountID reports whether id is a safe account directory name.
func ValidAccountID(id string) bool { return accountIDRe.MatchString(id) }

// AccountDir returns (and creates) the session directory of an account.
func (e *Env) AccountDir(account string) (string, error) {
	if !ValidAccountID(account) {
		return "", wire.Errorf(wire.CodeInvalid, "invalid account id %q", account)
	}
	dir := filepath.Join(e.DataDir, account)
	return dir, os.MkdirAll(dir, 0o700)
}

// AccountDirs lists the account directories that exist.
func (e *Env) AccountDirs() ([]string, error) {
	entries, err := os.ReadDir(e.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() && ValidAccountID(en.Name()) {
			out = append(out, en.Name())
		}
	}
	return out, nil
}

// InAccountDir reports whether path lies inside the account directory
// (media paths sent by the host are checked with it).
func (e *Env) InAccountDir(account, path string) bool {
	dir, err := e.AccountDir(account)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.'
}

// Plugin is implemented by every messenger plugin. Embed Unsupported to
// get "not supported" defaults for optional calls.
type Plugin interface {
	Info() wire.HelloResult
	Init(ctx context.Context, env *Env) error
	Accounts(ctx context.Context) ([]wire.Account, error)
	Connect(ctx context.Context, account string) error
	Disconnect(ctx context.Context, account string) error
	Remove(ctx context.Context, account string) error
	// Pair links a new account. It runs in its own goroutine; progress is
	// reported through p, user input is read with p.Input.
	Pair(ctx context.Context, req wire.PairRequest, p *Pairing) (wire.Account, error)
	ListChats(ctx context.Context, account string) ([]wire.Chat, error)
	History(ctx context.Context, req wire.HistoryRequest) ([]wire.Message, error)
	Send(ctx context.Context, req wire.SendRequest) (wire.SendResult, error)
	React(ctx context.Context, req wire.ReactRequest) error
	Edit(ctx context.Context, req wire.EditRequest) error
	Delete(ctx context.Context, req wire.DeleteRequest) error
	MarkRead(ctx context.Context, req wire.MarkReadRequest) error
	Typing(ctx context.Context, req wire.TypingRequest) error
	DownloadMedia(ctx context.Context, req wire.MediaRequest) (wire.MediaResult, error)
	Shutdown(ctx context.Context) error
}

// Unsupported implements the optional Plugin calls with CodeNotSupported.
type Unsupported struct{}

func notSupported(what string) error {
	return wire.Errorf(wire.CodeNotSupported, "%s is not supported", what)
}

func (Unsupported) History(context.Context, wire.HistoryRequest) ([]wire.Message, error) {
	return nil, notSupported("history")
}
func (Unsupported) React(context.Context, wire.ReactRequest) error   { return notSupported("reactions") }
func (Unsupported) Edit(context.Context, wire.EditRequest) error     { return notSupported("editing") }
func (Unsupported) Delete(context.Context, wire.DeleteRequest) error { return notSupported("deleting") }
func (Unsupported) MarkRead(context.Context, wire.MarkReadRequest) error {
	return notSupported("read receipts")
}
func (Unsupported) Typing(context.Context, wire.TypingRequest) error { return nil }
func (Unsupported) DownloadMedia(context.Context, wire.MediaRequest) (wire.MediaResult, error) {
	return wire.MediaResult{}, notSupported("media")
}

// Pairing is a running pairing session.
type Pairing struct {
	ID      string
	Account string
	emit    func(wire.PairEvent)
	mu      sync.Mutex
	waiting map[string]chan string
	inputs  map[string]string
}

func newPairing(id, account string, emit func(wire.PairEvent)) *Pairing {
	return &Pairing{ID: id, Account: account, emit: emit, waiting: map[string]chan string{}, inputs: map[string]string{}}
}

// NewTestPairing creates a pairing whose events go to emit (for tests of
// plugin adapters).
func NewTestPairing(account string, emit func(wire.PairEvent)) *Pairing {
	return newPairing("test", account, emit)
}

// Emit sends a progress event (PairingID is filled in).
func (p *Pairing) Emit(ev wire.PairEvent) {
	ev.PairingID = p.ID
	p.emit(ev)
}

// QR asks the user to scan data as QR code.
func (p *Pairing) QR(data string) { p.Emit(wire.PairEvent{Kind: wire.PairEventQR, QR: data}) }

// Info reports progress.
func (p *Pairing) Info(msg string) { p.Emit(wire.PairEvent{Kind: wire.PairEventInfo, Message: msg}) }

// Input requests a value from the user and waits for it.
func (p *Pairing) Input(ctx context.Context, field, msg string) (string, error) {
	p.mu.Lock()
	if v, ok := p.inputs[field]; ok {
		delete(p.inputs, field)
		p.mu.Unlock()
		return v, nil
	}
	ch := make(chan string, 1)
	p.waiting[field] = ch
	p.mu.Unlock()
	p.Emit(wire.PairEvent{Kind: wire.PairEventInput, Field: field, Message: msg})
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiting, field)
		p.mu.Unlock()
		return "", ctx.Err()
	}
}

// Provide delivers user input (also before it was requested).
func (p *Pairing) Provide(field, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.waiting[field]; ok {
		delete(p.waiting, field)
		ch <- value
		return
	}
	p.inputs[field] = value
}

// Server runs a Plugin over a connection.
type Server struct {
	p    Plugin
	env  *Env
	log  *slog.Logger
	conn *wire.Conn

	mu       sync.Mutex
	pairings map[string]*runningPairing
	inited   bool
	stop     chan struct{}
	stopOnce sync.Once
}

type runningPairing struct {
	p      *Pairing
	cancel context.CancelFunc
}

// Main serves p on stdin/stdout and exits the process when done.
func Main(p Plugin) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := Serve(context.Background(), p, os.Stdin, os.Stdout, log); err != nil && !errors.Is(err, io.EOF) {
		log.Error("plugin stopped", "err", err)
		os.Exit(1)
	}
}

// Serve runs p until r ends, ctx is cancelled or the host sends shutdown.
func Serve(ctx context.Context, p Plugin, r io.Reader, w io.Writer, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{p: p, log: log, pairings: map[string]*runningPairing{}, stop: make(chan struct{})}
	s.env = &Env{Logger: log, emit: s.emit}
	s.conn = wire.NewIdleConn(r, w, s.handle)
	s.conn.Start()
	var err error
	select {
	case <-s.conn.Done():
		err = s.conn.Err()
	case <-s.stop:
	case <-ctx.Done():
		err = ctx.Err()
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	s.mu.Lock()
	for _, rp := range s.pairings {
		rp.cancel()
	}
	s.mu.Unlock()
	if s.inited {
		if serr := p.Shutdown(sctx); serr != nil {
			log.Warn("plugin shutdown", "err", serr)
		}
	}
	s.conn.Close()
	if errors.Is(err, wire.ErrClosed) {
		err = nil
	}
	return err
}

func (s *Server) emit(ev wire.Event) {
	if err := s.conn.Notify(wire.NotifyEvent, ev); err != nil && !errors.Is(err, wire.ErrClosed) {
		s.log.Warn("emit event", "kind", ev.Kind, "err", err)
	}
}

func decode[T any](params json.RawMessage) (T, error) {
	var v T
	err := wire.Decode(params, &v)
	return v, err
}

func (s *Server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == wire.MethodHello {
		hp, err := decode[wire.HelloParams](params)
		if err != nil {
			return nil, err
		}
		if hp.ProtocolVersion != wire.ProtocolVersion {
			return nil, wire.Errorf(wire.CodeOutdated, "host speaks protocol %d, plugin %d", hp.ProtocolVersion, wire.ProtocolVersion)
		}
		if err := ApplyNetPolicy(hp.NetPolicy); err != nil {
			return nil, err
		}
		s.env.DataDir, s.env.Config, s.env.HostVersion, s.env.NetPolicy = hp.DataDir, hp.Config, hp.HostVersion, hp.NetPolicy
		if s.env.DataDir == "" {
			return nil, wire.Errorf(wire.CodeInvalidParams, "dataDir is required")
		}
		if err := os.MkdirAll(s.env.DataDir, 0o700); err != nil {
			return nil, err
		}
		if err := s.p.Init(ctx, s.env); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.inited = true
		s.mu.Unlock()
		info := s.p.Info()
		info.ProtocolVersion = wire.ProtocolVersion
		return info, nil
	}
	s.mu.Lock()
	inited := s.inited
	s.mu.Unlock()
	if !inited && method != wire.MethodPing && method != wire.MethodShutdown {
		return nil, wire.Errorf(wire.CodeInvalidRequest, "hello required before %s", method)
	}
	switch method {
	case wire.MethodPing:
		n := 0
		if inited {
			if accs, err := s.p.Accounts(ctx); err == nil {
				n = len(accs)
			}
		}
		return wire.PingResult{OK: true, Accounts: n}, nil
	case wire.MethodShutdown:
		s.stopOnce.Do(func() { close(s.stop) })
		return nil, nil
	case wire.MethodListAccounts:
		accs, err := s.p.Accounts(ctx)
		if accs == nil {
			accs = []wire.Account{}
		}
		return wire.AccountList{Accounts: accs}, err
	case wire.MethodConnect, wire.MethodDisconnect, wire.MethodRemoveAccount:
		ref, err := decode[wire.AccountRef](params)
		if err != nil {
			return nil, err
		}
		if !ValidAccountID(ref.Account) {
			return nil, wire.Errorf(wire.CodeInvalid, "invalid account id")
		}
		switch method {
		case wire.MethodConnect:
			return nil, s.p.Connect(ctx, ref.Account)
		case wire.MethodDisconnect:
			return nil, s.p.Disconnect(ctx, ref.Account)
		default:
			return nil, s.p.Remove(ctx, ref.Account)
		}
	case wire.MethodStartPairing:
		req, err := decode[wire.PairRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.startPairing(req)
	case wire.MethodPairingInput:
		in, err := decode[wire.PairInput](params)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		rp := s.pairings[in.PairingID]
		s.mu.Unlock()
		if rp == nil {
			return nil, wire.Errorf(wire.CodeNotFound, "no pairing %q", in.PairingID)
		}
		rp.p.Provide(in.Field, in.Value)
		return nil, nil
	case wire.MethodCancelPairing:
		ref, err := decode[wire.PairingRef](params)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		rp := s.pairings[ref.PairingID]
		s.mu.Unlock()
		if rp != nil {
			rp.cancel()
		}
		return nil, nil
	case wire.MethodListChats:
		ref, err := decode[wire.AccountRef](params)
		if err != nil {
			return nil, err
		}
		chats, err := s.p.ListChats(ctx, ref.Account)
		if chats == nil {
			chats = []wire.Chat{}
		}
		return wire.ChatList{Chats: chats}, err
	case wire.MethodHistory:
		req, err := decode[wire.HistoryRequest](params)
		if err != nil {
			return nil, err
		}
		if req.Limit <= 0 || req.Limit > 500 {
			req.Limit = 50
		}
		msgs, err := s.p.History(ctx, req)
		if msgs == nil {
			msgs = []wire.Message{}
		}
		return wire.MessageList{Messages: msgs}, err
	case wire.MethodSend:
		req, err := decode[wire.SendRequest](params)
		if err != nil {
			return nil, err
		}
		if req.Media != nil && !s.env.InAccountDir(req.Account, req.Media.Path) {
			return nil, wire.Errorf(wire.CodeInvalid, "media path outside the account directory")
		}
		if req.Text == "" && req.Media == nil {
			return nil, wire.Errorf(wire.CodeInvalid, "empty message")
		}
		return s.p.Send(ctx, req)
	case wire.MethodReact:
		req, err := decode[wire.ReactRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.p.React(ctx, req)
	case wire.MethodEdit:
		req, err := decode[wire.EditRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.p.Edit(ctx, req)
	case wire.MethodDelete:
		req, err := decode[wire.DeleteRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.p.Delete(ctx, req)
	case wire.MethodMarkRead:
		req, err := decode[wire.MarkReadRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.p.MarkRead(ctx, req)
	case wire.MethodTyping:
		req, err := decode[wire.TypingRequest](params)
		if err != nil {
			return nil, err
		}
		return nil, s.p.Typing(ctx, req)
	case wire.MethodDownloadMedia:
		req, err := decode[wire.MediaRequest](params)
		if err != nil {
			return nil, err
		}
		if !s.env.InAccountDir(req.Account, req.Path) {
			return nil, wire.Errorf(wire.CodeInvalid, "media path outside the account directory")
		}
		return s.p.DownloadMedia(ctx, req)
	}
	return nil, wire.Errorf(wire.CodeMethodNotFound, "method %q not found", method)
}

func (s *Server) startPairing(req wire.PairRequest) error {
	if req.PairingID == "" {
		return wire.Errorf(wire.CodeInvalidParams, "pairingId is required")
	}
	if !ValidAccountID(req.Account) {
		return wire.Errorf(wire.CodeInvalid, "invalid account id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairingTimeout)
	emit := func(pe wire.PairEvent) {
		s.emit(wire.Event{Kind: wire.EventPairing, Account: req.Account, Pairing: &pe})
	}
	p := newPairing(req.PairingID, req.Account, emit)
	s.mu.Lock()
	if _, dup := s.pairings[req.PairingID]; dup {
		s.mu.Unlock()
		cancel()
		return wire.Errorf(wire.CodeInvalid, "pairing %q already running", req.PairingID)
	}
	s.pairings[req.PairingID] = &runningPairing{p: p, cancel: cancel}
	s.mu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.Emit(wire.PairEvent{Kind: wire.PairEventError, Message: fmt.Sprintf("pairing crashed: %v", r)})
			}
			cancel()
			s.mu.Lock()
			delete(s.pairings, req.PairingID)
			s.mu.Unlock()
		}()
		acc, err := s.p.Pair(ctx, req, p)
		if err != nil {
			msg := err.Error()
			var we *wire.Error
			if errors.Is(err, context.Canceled) {
				msg = "cancelled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				msg = "timed out"
			} else if errors.As(err, &we) {
				msg = we.Message // without the " (code N)" suffix: ErrorCode carries it
			}
			p.Emit(wire.PairEvent{Kind: wire.PairEventError, Message: msg, ErrorCode: wire.ErrorCode(err)})
			return
		}
		if acc.ID == "" {
			acc.ID = req.Account
		}
		p.Emit(wire.PairEvent{Kind: wire.PairEventSuccess, Account: &acc})
	}()
	return nil
}

// NewTestEnv creates an Env for adapter tests: events go to emit.
func NewTestEnv(dataDir string, config map[string]string, emit func(wire.Event)) *Env {
	if config == nil {
		config = map[string]string{}
	}
	return &Env{DataDir: dataDir, Config: config, Logger: slog.Default(), emit: emit}
}
