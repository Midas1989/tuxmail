// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pop3 implements domain.MailProvider for POP3 / POP3S servers
// (RFC 1939, CAPA RFC 2449, STLS RFC 2595, SASL RFC 5034 with PLAIN and
// XOAUTH2).
//
// POP3 only knows a single mailbox. The provider presents it as the virtual
// folder "INBOX" and adds local-only folders (remote ids with the prefix
// domain.LocalFolderPrefix, e.g. "local/Archive") that exist only in the
// TuxMail store: messages moved there keep their stored body and stay in
// the local folder even after the server copy is deleted.
//
// Sync is based on UIDL: new unique ids are reported as added (headers via
// TOP, falling back to RETR); ids that vanish from the server are forgotten
// silently, because the downloaded copy is the user's mail (POP3 semantics).
// Deletion on the server follows the Policy: leave on server (default),
// delete after N days, delete when deleted locally. A server copy is only
// deleted once a local copy of the message exists (LocalStore.HasLocalCopy
// or a successful FetchRaw in this process).
package pop3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// InboxID is the remote id of the POP3 mailbox.
const InboxID = "INBOX"

// localRefPrefix marks messages that only exist locally (appended to a
// local folder); they have no server counterpart.
const localRefPrefix = "l-"

// ErrMessageNotFound is returned when a message is no longer on the server
// (or never was, for local-only messages). It is permanent.
var ErrMessageNotFound = errors.New("pop3: message not on the server")

// ErrNotSupported is returned for operations POP3 cannot do.
var ErrNotSupported = errors.New("pop3: operation not supported")

// TokenSource returns a currently valid OAuth2 access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token implements TokenSource.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// LocalStore gives the provider access to the local state of the account
// (implemented by the sync engine on top of the store).
type LocalStore interface {
	// LocalFolders lists the account's local folders (remote id prefix
	// domain.LocalFolderPrefix) known to the store.
	LocalFolders(ctx context.Context) ([]domain.Folder, error)
	// HasLocalCopy reports whether the message with this UIDL has its raw
	// body stored locally.
	HasLocalCopy(ctx context.Context, uidl string) (bool, error)
}

// Policy decides when messages are deleted on the server.
type Policy struct {
	// LeaveOnServer keeps messages on the server (default true via
	// PolicyFromExtra). When false, a message is deleted as soon as a local
	// copy exists.
	LeaveOnServer bool
	// DeleteAfterDays deletes messages that were first seen this many days
	// ago (0 = never; only with LeaveOnServer).
	DeleteAfterDays int
	// DeleteOnLocalDelete deletes the server copy when the message is
	// deleted permanently in TuxMail.
	DeleteOnLocalDelete bool
}

// Extra keys of domain.Account.Extra used for the policy.
const (
	ExtraLeaveOnServer       = "pop3LeaveOnServer"       // "true" (default) | "false"
	ExtraDeleteAfterDays     = "pop3DeleteAfterDays"     // decimal days, "" or "0" = never
	ExtraDeleteOnLocalDelete = "pop3DeleteOnLocalDelete" // "true" | "false" (default)
)

// PolicyFromExtra reads the policy from account settings.
func PolicyFromExtra(extra map[string]string) Policy {
	p := Policy{LeaveOnServer: true}
	if extra == nil {
		return p
	}
	if v := strings.TrimSpace(extra[ExtraLeaveOnServer]); v == "false" || v == "0" {
		p.LeaveOnServer = false
	}
	if v := strings.TrimSpace(extra[ExtraDeleteAfterDays]); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil && n > 0 {
			p.DeleteAfterDays = n
		}
	}
	if v := strings.TrimSpace(extra[ExtraDeleteOnLocalDelete]); v == "true" || v == "1" {
		p.DeleteOnLocalDelete = true
	}
	return p
}

// Config configures a Provider.
type Config struct {
	Server      domain.ServerConfig
	Password    string
	TokenSource TokenSource
	TLSConfig   *tls.Config
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Logger      *slog.Logger
	Policy      Policy
	// Local is optional; without it local folders live in memory and the
	// local-copy check falls back to FetchRaw history.
	Local LocalStore
	// PollInterval is the interval of the Watch ticker (default 2 min).
	PollInterval time.Duration
	// IdleTimeout closes an unused session (default 20 s). POP3 servers
	// lock the mailbox while a session is open.
	IdleTimeout time.Duration
	// CommandTimeout bounds every command (default 2 min).
	CommandTimeout time.Duration
	// MaxMessageSize bounds RETR (default 64 MiB).
	MaxMessageSize int64
	// CacheBytes bounds the in-memory cache of messages retrieved during
	// sync (default 16 MiB).
	CacheBytes int64
	Now        func() time.Time
}

// Provider is a POP3 implementation of domain.MailProvider.
type Provider struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	c       *conn
	uidls   map[string]int // uidl -> message number of the open session
	idle    *time.Timer
	closed  bool
	fetched map[string]bool // FetchRaw succeeded in this process
	folders map[string]domain.Folder

	cacheMu   sync.Mutex
	cache     map[string][]byte
	cacheKeys []string
	cacheSize int64
}

var _ domain.MailProvider = (*Provider)(nil)

// New validates cfg. Connections are opened on demand.
func New(cfg Config) (*Provider, error) {
	if cfg.Server.Host == "" {
		return nil, errors.New("pop3: missing host")
	}
	if cfg.Server.TLS == "" {
		cfg.Server.TLS = domain.TLSImplicit
	}
	if cfg.Server.Port == 0 {
		if cfg.Server.TLS == domain.TLSImplicit {
			cfg.Server.Port = 995
		} else {
			cfg.Server.Port = 110
		}
	}
	if cfg.Server.Auth == domain.AuthOAuth2 && cfg.TokenSource == nil {
		return nil, errors.New("pop3: OAuth2 authentication requires a token source")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Minute
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 20 * time.Second
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = 64 << 20
	}
	if cfg.CacheBytes <= 0 {
		cfg.CacheBytes = 16 << 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Provider{cfg: cfg, log: log, fetched: map[string]bool{}, folders: map[string]domain.Folder{},
		cache: map[string][]byte{}}, nil
}

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps { return domain.MailCaps{Move: true} }

// ==== session ==================================================================

// session returns the open session (opening and authenticating one).
// Callers hold p.mu.
func (p *Provider) session(ctx context.Context) (*conn, error) {
	if p.closed {
		return nil, errors.New("pop3: provider closed")
	}
	if p.idle != nil {
		p.idle.Stop()
	}
	if p.c != nil {
		return p.c, nil
	}
	c, err := dial(ctx, dialConfig{server: p.cfg.Server, tls: p.cfg.TLSConfig, dial: p.cfg.DialContext, timeout: p.cfg.CommandTimeout})
	if err != nil {
		return nil, err
	}
	if err := p.login(ctx, c); err != nil {
		_ = c.quit()
		return nil, err
	}
	p.c, p.uidls = c, nil
	return c, nil
}

func (p *Provider) login(ctx context.Context, c *conn) error {
	if !c.tls && p.cfg.Server.TLS != domain.TLSNone {
		return errors.New("pop3: refusing to authenticate without TLS")
	}
	user := p.cfg.Server.Username
	switch p.cfg.Server.Auth {
	case domain.AuthOAuth2:
		tok, err := p.cfg.TokenSource.Token(ctx)
		if err != nil {
			return fmt.Errorf("pop3: obtain OAuth2 token: %w", err)
		}
		return c.authenticate("XOAUTH2", xoauth2IR(user, tok))
	default:
		if c.hasMech("PLAIN") {
			return c.authenticate("PLAIN", plainIR(user, p.cfg.Password))
		}
		return c.userPass(user, p.cfg.Password)
	}
}

// release schedules closing the idle session. Callers hold p.mu.
func (p *Provider) release() {
	if p.c == nil {
		return
	}
	c := p.c
	if p.idle != nil {
		p.idle.Stop()
	}
	p.idle = time.AfterFunc(p.cfg.IdleTimeout, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.c == c {
			p.endSession()
		}
	})
}

// endSession commits pending deletions with QUIT. Callers hold p.mu.
func (p *Provider) endSession() error {
	if p.c == nil {
		return nil
	}
	err := p.c.quit()
	p.c, p.uidls = nil, nil
	return err
}

// drop discards a broken session. Callers hold p.mu.
func (p *Provider) drop() {
	if p.c != nil {
		_ = p.c.close()
	}
	p.c, p.uidls = nil, nil
}

// withSession runs fn on a session, retrying once on a broken connection.
func (p *Provider) withSession(ctx context.Context, fn func(c *conn) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for attempt := 0; ; attempt++ {
		c, err := p.session(ctx)
		if err != nil {
			return err
		}
		err = fn(c)
		if err == nil || errors.Is(err, ErrServer) || errors.Is(err, ErrMessageNotFound) || attempt > 0 || ctx.Err() != nil {
			if err != nil && !errors.Is(err, ErrServer) && !errors.Is(err, ErrMessageNotFound) {
				p.drop()
			} else {
				p.release()
			}
			return err
		}
		p.log.Debug("pop3: retrying after connection error", "err", err)
		p.drop()
	}
}

// numbers returns uidl -> message number of the session. Callers hold p.mu.
func (p *Provider) numbers(c *conn) (map[string]int, error) {
	if p.uidls != nil {
		return p.uidls, nil
	}
	byNum, err := c.uidlList()
	if err != nil {
		if errors.Is(err, ErrServer) {
			return nil, fmt.Errorf("pop3: server does not support UIDL: %w", err)
		}
		return nil, err
	}
	m := make(map[string]int, len(byNum))
	for n, u := range byNum {
		m[u] = n
	}
	p.uidls = m
	return m, nil
}

// ==== folders ==================================================================

var defaultLocal = []struct {
	name string
	role domain.FolderRole
}{
	{"Sent", domain.RoleSent}, {"Drafts", domain.RoleDrafts}, {"Archive", domain.RoleArchive}, {"Trash", domain.RoleTrash},
}

// ListFolders implements domain.MailProvider: the inbox, the default local
// folders (sent, drafts, archive, trash) and the user's local folders.
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	out := []domain.Folder{{RemoteID: InboxID, Name: "Inbox", Role: domain.RoleInbox, Delimiter: "/", Subscribed: true}}
	seen := map[string]bool{InboxID: true}
	for _, d := range defaultLocal {
		id := domain.LocalFolderPrefix + d.name
		out = append(out, domain.Folder{RemoteID: id, Name: d.name, Role: d.role, Delimiter: "/", Subscribed: true})
		seen[id] = true
	}
	var local []domain.Folder
	if p.cfg.Local != nil {
		l, err := p.cfg.Local.LocalFolders(ctx)
		if err != nil {
			return nil, err
		}
		local = l
	}
	p.mu.Lock()
	for _, f := range p.folders {
		local = append(local, f)
	}
	p.mu.Unlock()
	sort.Slice(local, func(i, j int) bool { return local[i].RemoteID < local[j].RemoteID })
	for _, f := range local {
		if seen[f.RemoteID] || !domain.IsLocalFolder(f.RemoteID) {
			continue
		}
		seen[f.RemoteID] = true
		out = append(out, domain.Folder{RemoteID: f.RemoteID, Name: f.Name, Role: f.Role, Delimiter: "/", Subscribed: true})
	}
	return out, nil
}

// CreateFolder implements domain.MailProvider (local folders only).
func (p *Provider) CreateFolder(_ context.Context, parentRemoteID, name string) (domain.Folder, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "/", "-"))
	if name == "" {
		return domain.Folder{}, errors.New("pop3: folder name required")
	}
	id := domain.LocalFolderPrefix + name
	if domain.IsLocalFolder(parentRemoteID) {
		id = parentRemoteID + "/" + name
	}
	f := domain.Folder{RemoteID: id, Name: name, Delimiter: "/", Subscribed: true}
	p.mu.Lock()
	p.folders[id] = f
	p.mu.Unlock()
	return f, nil
}

// RenameFolder implements domain.MailProvider (local folders only).
func (p *Provider) RenameFolder(_ context.Context, remoteID, newName string) (domain.Folder, error) {
	if !domain.IsLocalFolder(remoteID) {
		return domain.Folder{}, fmt.Errorf("%w: the POP3 inbox cannot be renamed", ErrNotSupported)
	}
	newName = strings.TrimSpace(strings.ReplaceAll(newName, "/", "-"))
	if newName == "" {
		return domain.Folder{}, errors.New("pop3: folder name required")
	}
	parent := remoteID[:strings.LastIndexByte(remoteID, '/')]
	id := parent + "/" + newName
	f := domain.Folder{RemoteID: id, Name: newName, Delimiter: "/", Subscribed: true}
	p.mu.Lock()
	delete(p.folders, remoteID)
	p.folders[id] = f
	p.mu.Unlock()
	return f, nil
}

// DeleteFolder implements domain.MailProvider (local folders only).
func (p *Provider) DeleteFolder(_ context.Context, remoteID string) error {
	if !domain.IsLocalFolder(remoteID) {
		return fmt.Errorf("%w: the POP3 inbox cannot be deleted", ErrNotSupported)
	}
	p.mu.Lock()
	delete(p.folders, remoteID)
	p.mu.Unlock()
	return nil
}

// ==== sync =====================================================================

// syncState is the persisted SyncState of the inbox.
type syncState struct {
	V    int              `json:"v"`
	Seen map[string]entry `json:"seen"`
}

type entry struct {
	T int64 `json:"t"`           // first seen (Unix seconds)
	F bool  `json:"f,omitempty"` // a local copy was confirmed
}

func decodeState(s domain.SyncState) syncState {
	st := syncState{V: 1, Seen: map[string]entry{}}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &st)
		if st.Seen == nil {
			st.Seen = map[string]entry{}
		}
	}
	return st
}

func (st syncState) encode() domain.SyncState {
	b, _ := json.Marshal(st)
	return domain.SyncState(b)
}

// Sync implements domain.MailProvider. Local folders have no server state.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	if folderRemoteID != InboxID {
		if state == "" {
			state = "local"
		}
		return domain.SyncDelta{}, state, nil
	}
	st := decodeState(state)
	var delta domain.SyncDelta
	now := p.cfg.Now()
	// A POP3 session sees a snapshot of the maildrop: start a fresh one so
	// that new mail becomes visible.
	p.mu.Lock()
	if p.c != nil {
		if err := p.endSession(); err != nil {
			p.log.Debug("pop3: ending previous session", "err", err)
		}
	}
	p.mu.Unlock()
	err := p.withSession(ctx, func(c *conn) error {
		nums, err := p.numbers(c)
		if err != nil {
			return err
		}
		sizes, err := c.list()
		if err != nil {
			return err
		}
		// Forget ids that left the server (local copies stay).
		for u := range st.Seen {
			if _, ok := nums[u]; !ok {
				delete(st.Seen, u)
			}
		}
		// New messages, oldest (lowest number) first.
		type nu struct {
			uidl string
			num  int
		}
		var fresh []nu
		for u, n := range nums {
			if _, ok := st.Seen[u]; !ok {
				fresh = append(fresh, nu{u, n})
			}
		}
		sort.Slice(fresh, func(i, j int) bool { return fresh[i].num < fresh[j].num })
		for _, f := range fresh {
			if err := ctx.Err(); err != nil {
				return err
			}
			hdr, err := c.top(f.num, 0)
			if errors.Is(err, ErrServer) {
				// TOP is optional: download the full message (cached for FetchRaw).
				raw, rerr := c.retr(f.num, p.cfg.MaxMessageSize)
				if rerr != nil {
					return rerr
				}
				p.cachePut(f.uidl, raw)
				hdr, err = raw, nil
			}
			if err != nil {
				return err
			}
			hi := parseHeader(hdr)
			rcv := hi.received
			if rcv.IsZero() {
				rcv = now
			}
			delta.Added = append(delta.Added, domain.RemoteMessage{
				Ref: domain.RemoteRef{FolderRemoteID: InboxID, ID: f.uidl}, Header: hi.h,
				Size: sizes[f.num], ReceivedAt: rcv, HasAttachments: hi.hasAttachments,
			})
			st.Seen[f.uidl] = entry{T: now.Unix()}
		}
		return p.applyPolicy(ctx, c, nums, &st, now)
	})
	if err != nil {
		return domain.SyncDelta{}, state, err
	}
	return delta, st.encode(), nil
}

// applyPolicy deletes due messages that have a local copy. Deletions are
// committed by ending the session.
func (p *Provider) applyPolicy(ctx context.Context, c *conn, nums map[string]int, st *syncState, now time.Time) error {
	pol := p.cfg.Policy
	if pol.LeaveOnServer && pol.DeleteAfterDays <= 0 {
		return nil
	}
	cutoff := now.Add(-time.Duration(pol.DeleteAfterDays) * 24 * time.Hour)
	deleted := 0
	uidls := make([]string, 0, len(st.Seen))
	for u := range st.Seen {
		uidls = append(uidls, u)
	}
	sort.Strings(uidls)
	for _, u := range uidls {
		e := st.Seen[u]
		if pol.LeaveOnServer && time.Unix(e.T, 0).After(cutoff) {
			continue
		}
		if !e.F {
			ok, err := p.hasLocalCopy(ctx, u)
			if err != nil {
				return err
			}
			if !ok {
				continue // never delete the only copy
			}
			e.F = true
			st.Seen[u] = e
		}
		n, ok := nums[u]
		if !ok {
			continue
		}
		if err := c.dele(n); err != nil {
			p.log.Info("pop3: DELE failed", "uidl", u, "err", err)
			continue
		}
		delete(st.Seen, u)
		deleted++
	}
	if deleted > 0 {
		p.log.Info("pop3: deleted messages on the server", "count", deleted)
		return p.endSession()
	}
	return nil
}

func (p *Provider) hasLocalCopy(ctx context.Context, uidl string) (bool, error) {
	if p.fetched[uidl] {
		return true, nil
	}
	if p.cfg.Local == nil {
		return false, nil
	}
	return p.cfg.Local.HasLocalCopy(ctx, uidl)
}

// ==== messages =================================================================

// FetchRaw implements domain.MailProvider (RETR by UIDL).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	if strings.HasPrefix(ref.ID, localRefPrefix) || ref.ID == "" {
		return nil, ErrMessageNotFound
	}
	if raw, ok := p.cacheTake(ref.ID); ok {
		p.markFetched(ref.ID)
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	var raw []byte
	err := p.withSession(ctx, func(c *conn) error {
		nums, err := p.numbers(c)
		if err != nil {
			return err
		}
		n, ok := nums[ref.ID]
		if !ok {
			return ErrMessageNotFound
		}
		raw, err = c.retr(n, p.cfg.MaxMessageSize)
		return err
	})
	if err != nil {
		return nil, err
	}
	p.markFetched(ref.ID)
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (p *Provider) markFetched(uidl string) {
	p.mu.Lock()
	p.fetched[uidl] = true
	p.mu.Unlock()
}

// Move implements domain.MailProvider: messages move between the inbox and
// local folders without touching the server.
func (p *Provider) Move(_ context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	if to != InboxID && !domain.IsLocalFolder(to) {
		return nil, fmt.Errorf("%w: unknown folder %q", ErrNotSupported, to)
	}
	out := make([]domain.RemoteRef, len(refs))
	for i, r := range refs {
		out[i] = domain.RemoteRef{FolderRemoteID: to, ID: r.ID}
	}
	return out, nil
}

// SetFlags implements domain.MailProvider. POP3 has no flags; they are kept
// locally.
func (p *Provider) SetFlags(context.Context, []domain.RemoteRef, []domain.Flag, []domain.Flag) error {
	return nil
}

// Delete implements domain.MailProvider. The server copy is deleted only
// with Policy.DeleteOnLocalDelete (or without LeaveOnServer).
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	pol := p.cfg.Policy
	if pol.LeaveOnServer && !pol.DeleteOnLocalDelete {
		return nil
	}
	var ids []string
	for _, r := range refs {
		if r.ID != "" && !strings.HasPrefix(r.ID, localRefPrefix) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return p.withSession(ctx, func(c *conn) error {
		nums, err := p.numbers(c)
		if err != nil {
			return err
		}
		n := 0
		for _, id := range ids {
			num, ok := nums[id]
			if !ok {
				continue // already gone
			}
			if err := c.dele(num); err != nil {
				return err
			}
			n++
		}
		if n > 0 {
			return p.endSession()
		}
		return nil
	})
}

// Append implements domain.MailProvider: only local folders can take new
// messages (the stored copy is the only one).
func (p *Provider) Append(_ context.Context, folderRemoteID string, _ io.Reader, _ []domain.Flag) (domain.RemoteRef, error) {
	if !domain.IsLocalFolder(folderRemoteID) {
		return domain.RemoteRef{}, fmt.Errorf("%w: cannot store messages in the POP3 inbox", ErrNotSupported)
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: localRefPrefix + hex.EncodeToString(b)}, nil
}

// Watch implements domain.MailProvider: POP3 has no push, so the inbox is
// polled every PollInterval; local folders never change remotely.
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	ch := make(chan domain.ChangeHint, 1)
	go func() {
		defer close(ch)
		if folderRemoteID != InboxID {
			<-ctx.Done()
			return
		}
		t := time.NewTicker(p.cfg.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case ch <- domain.ChangeHint{FolderRemoteID: InboxID}:
				default:
				}
			}
		}
	}()
	return ch, nil
}

// Close implements domain.MailProvider (QUIT commits pending deletions).
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.idle != nil {
		p.idle.Stop()
	}
	return p.endSession()
}

// ==== cache ====================================================================

func (p *Provider) cachePut(uidl string, raw []byte) {
	if int64(len(raw)) > p.cfg.CacheBytes {
		return
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if _, ok := p.cache[uidl]; ok {
		return
	}
	p.cache[uidl] = raw
	p.cacheKeys = append(p.cacheKeys, uidl)
	p.cacheSize += int64(len(raw))
	for p.cacheSize > p.cfg.CacheBytes && len(p.cacheKeys) > 0 {
		k := p.cacheKeys[0]
		p.cacheKeys = p.cacheKeys[1:]
		p.cacheSize -= int64(len(p.cache[k]))
		delete(p.cache, k)
	}
}

func (p *Provider) cacheTake(uidl string) ([]byte, bool) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	raw, ok := p.cache[uidl]
	if !ok {
		return nil, false
	}
	delete(p.cache, uidl)
	p.cacheSize -= int64(len(raw))
	for i, k := range p.cacheKeys {
		if k == uidl {
			p.cacheKeys = append(p.cacheKeys[:i], p.cacheKeys[i+1:]...)
			break
		}
	}
	return raw, true
}
