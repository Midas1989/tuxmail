// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eas implements Exchange ActiveSync (EAS 2.5 – 16.1) for TuxMail:
// the HTTP layer of [MS-ASHTTP] (version negotiation, Basic and OAuth2
// Bearer authentication, provisioning, redirects, Retry-After), the
// commands of [MS-ASCMD] (Provision, FolderSync/Create/Update/Delete, Sync,
// GetItemEstimate, ItemOperations, MoveItems, SendMail/SmartReply/
// SmartForward, MeetingResponse, Search, Ping, Settings, ResolveRecipients),
// a domain.MailProvider (Provider), a domain.Transport (Transport) and the
// calendar/contacts/tasks synchronization behind small interfaces (PIM).
//
// Server quirks were cross-checked with the behaviour documented by the
// MPL-2.0 project EAS-4-TbSync (github.com/jobisoft/EAS-4-TbSync); no code
// was copied from it.
package eas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Config configures a Provider.
type Config struct {
	Client ClientConfig
	// FilterType limits the mail time window ("0" = all, the default;
	// "1" = 1 day ... "5" = 1 month, "6" = 3 months, "7" = 6 months).
	FilterType string
	// WindowSize is the number of changes per Sync response (default 100).
	WindowSize int
	// HeaderTruncation is the number of MIME bytes requested per message
	// during sync to read the threading headers (Message-ID, References;
	// default 16 KiB). Full messages are loaded with FetchRaw.
	HeaderTruncation int
	// Heartbeat is the initial Ping heartbeat (default 9 minutes; the
	// server may lower or raise it).
	Heartbeat time.Duration
	// MinBackoff / MaxBackoff bound the Ping reconnect delay (1 s / 5 min).
	MinBackoff, MaxBackoff time.Duration
	// RearmTimeout is how long Ping waits after reporting changes for the
	// folders to be synced before it polls again (default 2 min).
	RearmTimeout time.Duration
	// CategoryToKeyword / KeywordToCategory map Exchange categories to the
	// keyword flags used by the sync engine for tags (default: identity for
	// names without spaces or special characters).
	CategoryToKeyword func(category string) (keyword string, ok bool)
	KeywordToCategory func(keyword string) (category string, ok bool)
	Logger            *slog.Logger
}

// Provider is the EAS implementation of domain.MailProvider.
type Provider struct {
	cfg Config
	c   *Client
	log *slog.Logger

	mu     sync.Mutex
	colls  map[string]*collMem
	hub    *pingHub
	closed bool
}

// collMem is the in-memory state of a mail collection.
type collMem struct {
	returned string     // key last returned to the sync engine
	cur      *collState // current state (key may be newer)
	pending  domain.SyncDelta
}

var _ domain.MailProvider = (*Provider)(nil)

// New creates a provider; no request is made until the first call.
func New(cfg Config) (*Provider, error) {
	c, err := NewClient(cfg.Client)
	if err != nil {
		return nil, err
	}
	if cfg.FilterType == "" {
		cfg.FilterType = "0"
	}
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 100
	}
	if cfg.HeaderTruncation <= 0 {
		cfg.HeaderTruncation = 16 << 10
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 9 * time.Minute
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.RearmTimeout <= 0 {
		cfg.RearmTimeout = 2 * time.Minute
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.CategoryToKeyword == nil {
		cfg.CategoryToKeyword = defaultCategoryToKeyword
	}
	if cfg.KeywordToCategory == nil {
		cfg.KeywordToCategory = func(k string) (string, bool) { return k, k != "" && !strings.HasPrefix(k, "$") }
	}
	log := cfg.Logger
	if log == nil {
		log = c.log
	}
	p := &Provider{cfg: cfg, c: c, log: log, colls: map[string]*collMem{}}
	p.hub = newPingHub(p)
	return p, nil
}

func defaultCategoryToKeyword(c string) (string, bool) {
	if c == "" || strings.ContainsAny(c, " (){%*\"\\]") || strings.HasPrefix(c, "$") {
		return "", false
	}
	return c, true
}

// Client returns the underlying protocol client (for Search, OOF, ...).
func (p *Provider) Client() *Client { return p.c }

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps {
	return domain.MailCaps{Move: true, Idle: true}
}

// Close stops all watchers.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.hub.stop()
	return nil
}

// ==== folders ====================================================================

func isMailType(t int) bool {
	switch t {
	case FolderUserGeneric, FolderInbox, FolderDrafts, FolderDeleted, FolderSent, FolderUserMail:
		return true
	}
	return false
}

// folderRole maps the EAS type (and, for user folders, the name) to a role.
func folderRole(f Folder) domain.FolderRole {
	switch f.Type {
	case FolderInbox:
		return domain.RoleInbox
	case FolderDrafts:
		return domain.RoleDrafts
	case FolderDeleted:
		return domain.RoleTrash
	case FolderSent:
		return domain.RoleSent
	}
	switch strings.ToLower(f.Name) {
	case "junk", "junk e-mail", "junk email", "junk-e-mail", "spam", "junk-e-mails", "junk-mail":
		return domain.RoleJunk
	case "archive", "archiv", "archives":
		return domain.RoleArchive
	}
	return domain.RoleNone
}

// mailFolders converts the hierarchy to domain folders with path remote ids.
func mailFolders(all []Folder) []domain.Folder {
	byID := map[string]Folder{}
	for _, f := range all {
		byID[f.ServerID] = f
	}
	var path func(f Folder, depth int) string
	path = func(f Folder, depth int) string {
		par, ok := byID[f.ParentID]
		if !ok || depth > 64 || !isMailType(par.Type) {
			return escapeID(f.ServerID)
		}
		return path(par, depth+1) + delimiter + escapeID(f.ServerID)
	}
	var out []domain.Folder
	for _, f := range all {
		if !isMailType(f.Type) {
			continue
		}
		out = append(out, domain.Folder{RemoteID: path(f, 0), Name: f.Name, Delimiter: delimiter,
			Role: folderRole(f), Subscribed: true})
	}
	return out
}

// ListFolders implements domain.MailProvider (FolderSync).
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	all, err := p.c.FolderSync(ctx)
	if err != nil {
		return nil, err
	}
	return mailFolders(all), nil
}

func (p *Provider) folderByID(ctx context.Context, id string) (Folder, bool) {
	all, err := p.c.Folders(ctx)
	if err != nil {
		return Folder{}, false
	}
	for _, f := range all {
		if f.ServerID == id {
			return f, true
		}
	}
	return Folder{}, false
}

// CreateFolder implements domain.MailProvider (FolderCreate).
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	parent := "0"
	if parentRemoteID != "" {
		parent = CollectionID(parentRemoteID)
	}
	f, err := p.c.CreateFolder(ctx, parent, name, FolderUserMail)
	if err != nil {
		return domain.Folder{}, err
	}
	rid := escapeID(f.ServerID)
	if parentRemoteID != "" {
		rid = parentRemoteID + delimiter + rid
	}
	return domain.Folder{RemoteID: rid, Name: name, Delimiter: delimiter, Role: folderRole(f), Subscribed: true}, nil
}

// RenameFolder implements domain.MailProvider (FolderUpdate). The remote
// id does not change because it consists of ServerIds.
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	id := CollectionID(remoteID)
	parent := "0"
	if f, ok := p.folderByID(ctx, id); ok && f.ParentID != "" {
		parent = f.ParentID
	}
	f, err := p.c.UpdateFolder(ctx, id, parent, newName)
	if err != nil {
		return domain.Folder{}, err
	}
	return domain.Folder{RemoteID: remoteID, Name: newName, Delimiter: delimiter, Role: folderRole(f), Subscribed: true}, nil
}

// DeleteFolder implements domain.MailProvider (FolderDelete).
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	id := CollectionID(remoteID)
	p.mu.Lock()
	delete(p.colls, id)
	p.mu.Unlock()
	return p.c.DeleteFolder(ctx, id)
}

// ==== sync =======================================================================

func (p *Provider) mailOptions(ctx context.Context) SyncOptions {
	o := SyncOptions{Class: "Email", FilterType: p.cfg.FilterType, WindowSize: p.cfg.WindowSize, MIMESupport: 2}
	if p.c.versionAtLeast(ctx, "12.0") {
		o.BodyPreferences = []BodyPref{{Type: BodyMIME, TruncationSize: p.cfg.HeaderTruncation}}
	} else {
		o.MIMETruncation = 5 // 20 KB
	}
	return o
}

// collection returns the current state of a collection given the state
// the sync engine passed in. The engine's state wins unless it is the one
// last returned, in which case changes made by the provider itself (newer
// sync keys) are applied.
func (p *Provider) collection(ctx context.Context, id string, st *collState) *collState {
	p.mu.Lock()
	mem := p.colls[id]
	p.mu.Unlock()
	if mem != nil {
		if st.Key != "" && st.Key == mem.returned {
			return mem.cur.clone()
		}
		return st
	}
	keys := p.c.snapshotState(ctx).Collections[id]
	if st.Key != "" && st.Key == keys.R && keys.C != "" {
		st.Key = keys.C
	}
	return st
}

// remember stores the state after a sync or a provider-side change.
func (p *Provider) remember(ctx context.Context, id string, st *collState, returned bool) {
	p.mu.Lock()
	mem := p.colls[id]
	if mem == nil {
		mem = &collMem{}
		p.colls[id] = mem
	}
	if returned {
		mem.returned = st.Key
	}
	mem.cur = st.clone()
	r := mem.returned
	p.mu.Unlock()
	p.c.updateState(ctx, func(s *accountState) {
		if s.Collections == nil {
			s.Collections = map[string]collKeys{}
		}
		s.Collections[id] = collKeys{R: r, C: st.Key}
	})
}

// takePending returns and clears changes received during change-only syncs.
func (p *Provider) takePending(id string) domain.SyncDelta {
	p.mu.Lock()
	defer p.mu.Unlock()
	mem := p.colls[id]
	if mem == nil {
		return domain.SyncDelta{}
	}
	d := mem.pending
	mem.pending = domain.SyncDelta{}
	return d
}

// Sync implements domain.MailProvider. The state holds the collection's
// SyncKey and the flags of the known items; refs are EAS ServerIds.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	id := CollectionID(folderRemoteID)
	var delta domain.SyncDelta
	st, err := decodeCollState(state)
	if err != nil {
		p.log.Warn("eas: discarding unreadable sync state", "folder", folderRemoteID, "err", err)
		st, delta.FullResync = newCollState(), true
	}
	st = p.collection(ctx, id, st)
	pending := p.takePending(id)
	if !delta.FullResync && st.Key != "" {
		delta = pending
	}
	opts := p.mailOptions(ctx)
	for attempt := 0; ; attempt++ {
		err = p.syncLoop(ctx, folderRemoteID, id, st, opts, &delta)
		if errors.Is(err, ErrInvalidSyncKey) && attempt == 0 {
			p.log.Info("eas: sync key rejected, full resync", "folder", folderRemoteID)
			st, delta = newCollState(), domain.SyncDelta{FullResync: true}
			continue
		}
		break
	}
	if err != nil {
		return domain.SyncDelta{}, state, err
	}
	p.remember(ctx, id, st, true)
	p.hub.markSynced(id)
	return delta, st.encode(), nil
}

func (p *Provider) syncLoop(ctx context.Context, folder, id string, st *collState, opts SyncOptions, delta *domain.SyncDelta) error {
	if st.Key == "" || st.Key == "0" {
		res, err := p.c.SyncCollection(ctx, id, "0", opts, nil)
		if err != nil {
			return err
		}
		st.Key = res.SyncKey
		if st.Key == "0" || st.Key == "" {
			return errors.New("eas: Sync: server returned no sync key")
		}
	}
	for round := 0; round < 1000; round++ {
		res, err := p.c.SyncCollection(ctx, id, st.Key, opts, nil)
		if err != nil {
			return err
		}
		p.applyServerChanges(folder, st, res, delta)
		st.Key = res.SyncKey
		if !res.MoreAvailable {
			return nil
		}
	}
	return nil
}

// applyServerChanges merges server Commands into st and delta.
func (p *Provider) applyServerChanges(folder string, st *collState, res *SyncResult, delta *domain.SyncDelta) {
	addedIdx := map[string]int{}
	for i, a := range delta.Added {
		addedIdx[a.Ref.ID] = i
	}
	for _, it := range res.Added {
		if it.ServerID == "" {
			continue
		}
		f := mergeFlags(itemFlags{}, it.Data)
		st.Items[it.ServerID] = f
		rm := emailMessage(folder, it.ServerID, it.Data, f, p.cfg.CategoryToKeyword)
		if i, ok := addedIdx[it.ServerID]; ok {
			delta.Added[i] = rm
			continue
		}
		addedIdx[it.ServerID] = len(delta.Added)
		delta.Added = append(delta.Added, rm)
	}
	for _, it := range res.Changed {
		f := mergeFlags(st.Items[it.ServerID], it.Data)
		st.Items[it.ServerID] = f
		flags := domainFlags(f, p.cfg.CategoryToKeyword)
		if i, ok := addedIdx[it.ServerID]; ok {
			delta.Added[i].Flags = flags
			continue
		}
		delta.FlagsChanged = append(delta.FlagsChanged, domain.RemoteMessage{
			Ref: domain.RemoteRef{FolderRemoteID: folder, ID: it.ServerID}, Flags: flags})
	}
	for _, sid := range res.Deleted {
		delete(st.Items, sid)
		if i, ok := addedIdx[sid]; ok {
			delta.Added = append(delta.Added[:i], delta.Added[i+1:]...)
			addedIdx = map[string]int{}
			for j, a := range delta.Added {
				addedIdx[a.Ref.ID] = j
			}
			continue
		}
		delta.Removed = append(delta.Removed, domain.RemoteRef{FolderRemoteID: folder, ID: sid})
	}
}

// change sends local changes for one collection with GetChanges=0 and
// returns the per-item responses. Server changes contained in the answer
// are kept for the next Sync.
func (p *Provider) change(ctx context.Context, folder string, changes []ClientChange, opts SyncOptions,
	update func(st *collState)) ([]ItemResponse, error) {
	id := CollectionID(folder)
	p.mu.Lock()
	mem := p.colls[id]
	var st *collState
	if mem != nil {
		st = mem.cur.clone()
	}
	p.mu.Unlock()
	if st == nil {
		keys := p.c.snapshotState(ctx).Collections[id]
		if keys.C == "" {
			// Never synced: obtain a sync key first.
			if _, _, err := p.Sync(ctx, folder, ""); err != nil {
				return nil, err
			}
			return p.change(ctx, folder, changes, opts, update)
		}
		st = newCollState()
		st.Key = keys.C
	}
	opts.NoChanges = true
	res, err := p.c.SyncCollection(ctx, id, st.Key, opts, changes)
	if err != nil {
		return nil, err
	}
	var extra domain.SyncDelta
	p.applyServerChanges(folder, st, res, &extra)
	st.Key = res.SyncKey
	if update != nil {
		update(st)
	}
	p.remember(ctx, id, st, false)
	if len(extra.Added)+len(extra.FlagsChanged)+len(extra.Removed) > 0 {
		p.mu.Lock()
		m := p.colls[id]
		m.pending.Added = append(m.pending.Added, extra.Added...)
		m.pending.FlagsChanged = append(m.pending.FlagsChanged, extra.FlagsChanged...)
		m.pending.Removed = append(m.pending.Removed, extra.Removed...)
		p.mu.Unlock()
	}
	return res.Responses, nil
}

func groupRefs(refs []domain.RemoteRef) ([]string, map[string][]string) {
	var order []string
	by := map[string][]string{}
	for _, r := range refs {
		if _, ok := by[r.FolderRemoteID]; !ok {
			order = append(order, r.FolderRemoteID)
		}
		by[r.FolderRemoteID] = append(by[r.FolderRemoteID], r.ID)
	}
	return order, by
}

// responseErrors collects failed item responses; not-found items are
// ignored when ignoreMissing is set.
func responseErrors(rs []ItemResponse, ignoreMissing bool) error {
	var errs []error
	for _, r := range rs {
		if r.Status == SyncStatusNotFound && ignoreMissing {
			continue
		}
		if err := ItemStatusError(r); err != nil {
			errs = append(errs, fmt.Errorf("item %s: %w", r.ServerID, err))
		}
	}
	return errors.Join(errs...)
}

// SetFlags implements domain.MailProvider: \Seen maps to Email:Read,
// \Flagged to Email:Flag; keywords map to categories (14.0+). \Answered and
// $Forwarded are maintained by the server (LastVerbExecuted) and ignored.
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	cats := p.c.versionAtLeast(ctx, "14.0")
	order, by := groupRefs(refs)
	for _, folder := range order {
		id := CollectionID(folder)
		var current map[string]itemFlags
		p.mu.Lock()
		if mem := p.colls[id]; mem != nil {
			current = mem.cur.clone().Items
		}
		p.mu.Unlock()
		var changes []ClientChange
		newFlags := map[string]itemFlags{}
		for _, sid := range by[folder] {
			f := current[sid]
			data, nf := flagChange(f, add, remove, cats, p.cfg.KeywordToCategory)
			if len(data) == 0 {
				continue
			}
			newFlags[sid] = nf
			changes = append(changes, ClientChange{Kind: "Change", ServerID: sid, Data: data})
		}
		if len(changes) == 0 {
			continue
		}
		rs, err := p.change(ctx, folder, changes, p.mailOptions(ctx), func(st *collState) {
			for sid, f := range newFlags {
				if _, ok := st.Items[sid]; ok || len(st.Items) == 0 {
					st.Items[sid] = f
				}
			}
		})
		if err != nil {
			return err
		}
		if err := responseErrors(rs, false); err != nil {
			return err
		}
	}
	return nil
}

func hasFlag(fs []domain.Flag, f domain.Flag) bool {
	for _, x := range fs {
		if strings.EqualFold(string(x), string(f)) {
			return true
		}
	}
	return false
}

// flagChange builds the ApplicationData of a flag change.
func flagChange(f itemFlags, add, remove []domain.Flag, cats bool, toCategory func(string) (string, bool)) ([]*wbxml.Node, itemFlags) {
	var data []*wbxml.Node
	switch {
	case hasFlag(add, domain.FlagSeen):
		data = append(data, wbxml.S("Email:Read", "1"))
		f.Mask |= bitRead
	case hasFlag(remove, domain.FlagSeen):
		data = append(data, wbxml.S("Email:Read", "0"))
		f.Mask &^= bitRead
	}
	switch {
	case hasFlag(add, domain.FlagFlagged):
		data = append(data, wbxml.E("Email:Flag", wbxml.S("Email:Status", "2"),
			wbxml.S("Email:FlagType", "Flag for follow up")))
		f.Mask |= bitFlagged
	case hasFlag(remove, domain.FlagFlagged):
		data = append(data, wbxml.E("Email:Flag"))
		f.Mask &^= bitFlagged
	}
	if !cats {
		return data, f
	}
	set := append([]string(nil), f.Categories...)
	changed := false
	for _, k := range add {
		if strings.HasPrefix(string(k), `\`) {
			continue
		}
		c, ok := toCategory(string(k))
		if !ok {
			continue
		}
		found := false
		for _, x := range set {
			found = found || strings.EqualFold(x, c)
		}
		if !found {
			set, changed = append(set, c), true
		}
	}
	for _, k := range remove {
		if strings.HasPrefix(string(k), `\`) {
			continue
		}
		c, ok := toCategory(string(k))
		if !ok {
			continue
		}
		kept := set[:0]
		for _, x := range set {
			if strings.EqualFold(x, c) {
				changed = true
				continue
			}
			kept = append(kept, x)
		}
		set = kept
	}
	if changed {
		cn := wbxml.E("Email:Categories")
		for _, c := range set {
			cn.Add(wbxml.S("Email:Category", c))
		}
		data = append(data, cn)
		f.Categories = set
	}
	return data, f
}

// Delete implements domain.MailProvider: permanent deletion (Sync Delete
// with DeletesAsMoves=0). Moving to Deleted Items is done by the caller.
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	order, by := groupRefs(refs)
	no := false
	for _, folder := range order {
		var changes []ClientChange
		for _, sid := range by[folder] {
			changes = append(changes, ClientChange{Kind: "Delete", ServerID: sid})
		}
		opts := p.mailOptions(ctx)
		opts.DeletesAsMoves = &no
		ids := by[folder]
		rs, err := p.change(ctx, folder, changes, opts, func(st *collState) {
			for _, sid := range ids {
				delete(st.Items, sid)
			}
		})
		if err != nil {
			return err
		}
		if err := responseErrors(rs, true); err != nil {
			return err
		}
	}
	return nil
}

// Move implements domain.MailProvider (MoveItems).
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, toFolderRemoteID string) ([]domain.RemoteRef, error) {
	dst := CollectionID(toFolderRemoteID)
	out := make([]domain.RemoteRef, len(refs))
	order, by := groupRefs(refs)
	result := map[domain.RemoteRef]string{}
	for _, folder := range order {
		src := CollectionID(folder)
		rs, err := p.c.MoveItems(ctx, src, by[folder], dst)
		if err != nil {
			return nil, err
		}
		var errs []error
		p.mu.Lock()
		mem := p.colls[src]
		for _, r := range rs {
			switch r.Status {
			case 3:
				result[domain.RemoteRef{FolderRemoteID: folder, ID: r.SrcMsgID}] = r.DstMsgID
				if mem != nil {
					delete(mem.cur.Items, r.SrcMsgID)
				}
			case 1:
				errs = append(errs, fmt.Errorf("item %s: %w", r.SrcMsgID, errors.Join(ErrItemNotFound,
					&StatusError{Cmd: "MoveItems", Status: r.Status})))
			default:
				errs = append(errs, fmt.Errorf("item %s: %w", r.SrcMsgID, &StatusError{Cmd: "MoveItems", Status: r.Status}))
			}
		}
		p.mu.Unlock()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
	}
	for i, r := range refs {
		out[i] = domain.RemoteRef{FolderRemoteID: toFolderRemoteID, ID: result[r]}
	}
	return out, nil
}

// FetchRaw implements domain.MailProvider (ItemOperations/Fetch with MIME;
// Sync/Fetch for EAS 2.5).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	id := CollectionID(ref.FolderRemoteID)
	if p.c.versionAtLeast(ctx, "12.0") {
		data, err := p.c.FetchMIME(ctx, id, ref.ID)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	opts := SyncOptions{Class: "Email", MIMESupport: 2, MIMETruncation: 8}
	rs, err := p.change(ctx, ref.FolderRemoteID, []ClientChange{{Kind: "Fetch", ServerID: ref.ID}}, opts, nil)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.Kind == "Fetch" && r.ServerID == ref.ID {
			if err := ItemStatusError(r); err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(r.Data.Child("Email:MIMEData").Bytes())), nil
		}
	}
	return nil, ErrItemNotFound
}

// Append implements domain.MailProvider. EAS cannot upload arbitrary
// messages; with EAS 16.0+ a message appended to the Drafts folder is
// created as a draft (Sync Add with Email2 properties, attachments
// included). Everything else returns ErrNotSupported.
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	if !p.c.versionAtLeast(ctx, "16.0") {
		return domain.RemoteRef{}, fmt.Errorf("%w: append needs EAS 16.0 drafts", ErrNotSupported)
	}
	f, ok := p.folderByID(ctx, CollectionID(folderRemoteID))
	if !ok || f.Type != FolderDrafts {
		return domain.RemoteRef{}, fmt.Errorf("%w: EAS can only create messages in the Drafts folder", ErrNotSupported)
	}
	data, err := io.ReadAll(io.LimitReader(raw, 64<<20))
	if err != nil {
		return domain.RemoteRef{}, err
	}
	app, err := draftData(data, hasFlag(flags, domain.FlagSeen))
	if err != nil {
		return domain.RemoteRef{}, err
	}
	cid := newClientID()
	rs, err := p.change(ctx, folderRemoteID, []ClientChange{{Kind: "Add", ClientID: cid, Data: app}}, p.mailOptions(ctx), nil)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	for _, r := range rs {
		if r.Kind == "Add" && r.ClientID == cid {
			if err := ItemStatusError(r); err != nil {
				return domain.RemoteRef{}, err
			}
			return domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: r.ServerID}, nil
		}
	}
	return domain.RemoteRef{FolderRemoteID: folderRemoteID}, nil
}
