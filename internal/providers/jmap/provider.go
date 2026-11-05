// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jmap implements domain.MailProvider, domain.Transport and
// vacation.ServerVacationSync for JMAP servers (RFC 8620 core, RFC 8621
// mail, submission and vacation response), e.g. Fastmail, Stalwart and
// Cyrus. It is a lean own client on net/http and encoding/json.
//
// Mailboxes are exposed with remote ids that are the path of mailbox ids
// ("parentId/childId", delimiter "/"), so that the sync engine can rebuild
// the hierarchy; a message ref is the JMAP Email id (stable across moves).
// Folder sync diffs the Email/query result of a mailbox against the known
// ids and uses Email/changes to detect keyword changes cheaply; when
// nothing changed since the last state, no query is sent at all.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

// ErrNotFound is returned for messages or mailboxes that do not exist (any more).
var ErrNotFound = errors.New("jmap: not found")

// Config configures a Provider.
type Config struct {
	// SessionURL is the session resource, e.g. https://api.fastmail.com/jmap/session
	// or https://mail.example.org/.well-known/jmap.
	SessionURL string
	HTTPClient *http.Client
	Auth       Auth
	Logger     *slog.Logger
	// PollInterval is used by Watch when the server has no EventSource.
	PollInterval time.Duration
	// MinBackoff / MaxBackoff bound EventSource reconnects.
	MinBackoff, MaxBackoff time.Duration
	// PageSize is the Email/query page size (default 1000).
	PageSize int
	// AllowInsecure permits a plain-http session URL for non-loopback hosts
	// (explicit user confirmation, see ApplyAccountOptions).
	AllowInsecure bool
	// TrustedHosts are confirmed hosts besides the session host's
	// registrable domain that may receive the credentials (download host).
	TrustedHosts []string
}

// Account options (Account.Extra) that affect the transport rules.
const (
	// ExtraAllowInsecure ("true") records that the user confirmed a plain
	// http:// endpoint for the account.
	ExtraAllowInsecure = safehttp.ExtraAllowInsecure
	// ExtraTrustedHosts is a comma separated list of additional hosts of
	// the session document that may receive the credentials.
	ExtraTrustedHosts = "jmapTrustedHosts"
)

// ApplyAccountOptions copies the transport options of acc into cfg.
func ApplyAccountOptions(cfg *Config, acc domain.Account) {
	if acc.Extra == nil {
		return
	}
	cfg.AllowInsecure = acc.Extra[ExtraAllowInsecure] == "true"
	for _, h := range strings.Split(acc.Extra[ExtraTrustedHosts], ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.TrustedHosts = append(cfg.TrustedHosts, h)
		}
	}
}

// Provider is a JMAP mail provider.
type Provider struct {
	cfg Config
	c   *Client
	log *slog.Logger

	mu      sync.Mutex
	paths   map[string]string // mailbox id -> remote id (path)
	byPath  map[string]string // remote id -> mailbox id
	account string
}

var _ domain.MailProvider = (*Provider)(nil)

// SessionURLFor derives the session URL of an account: Extra["jmapUrl"]
// or https://<incoming host>[:port]/.well-known/jmap.
func SessionURLFor(acc domain.Account) string {
	if acc.Extra != nil && strings.TrimSpace(acc.Extra["jmapUrl"]) != "" {
		return strings.TrimSpace(acc.Extra["jmapUrl"])
	}
	if acc.Incoming == nil || acc.Incoming.Host == "" {
		return ""
	}
	scheme := "https"
	if acc.Incoming.TLS == domain.TLSNone {
		scheme = "http"
	}
	host := acc.Incoming.Host
	if p := acc.Incoming.Port; p != 0 && p != 443 && !(p == 80 && scheme == "http") {
		host = fmt.Sprintf("%s:%d", host, p)
	}
	return scheme + "://" + host + "/.well-known/jmap"
}

// New creates a provider; no request is made until the first call.
func New(cfg Config) (*Provider, error) {
	if cfg.SessionURL == "" {
		return nil, errors.New("jmap: missing session URL")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Minute
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 1000
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Client{SessionURL: cfg.SessionURL, HTTP: cfg.HTTPClient, Auth: cfg.Auth, AllowInsecure: cfg.AllowInsecure, TrustedHosts: cfg.TrustedHosts}
	if err := c.checkURL("session URL", cfg.SessionURL); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg, log: log, c: c}, nil
}

// Client returns the underlying JMAP client.
func (p *Provider) Client() *Client { return p.c }

var mailUsing = []string{CapCore, CapMail}

func (p *Provider) accountID(ctx context.Context) (string, error) {
	p.mu.Lock()
	id := p.account
	p.mu.Unlock()
	if id != "" {
		return id, nil
	}
	s, err := p.c.Session(ctx, false)
	if err != nil {
		return "", err
	}
	id = s.AccountID(CapMail)
	if id == "" {
		return "", errors.New("jmap: the server has no mail account")
	}
	p.mu.Lock()
	p.account = id
	p.mu.Unlock()
	return id, nil
}

// call runs one method and decodes its response.
func (p *Provider) call(ctx context.Context, using []string, name string, args map[string]any, out any) error {
	acc, err := p.accountID(ctx)
	if err != nil {
		return err
	}
	if _, ok := args["accountId"]; !ok {
		args["accountId"] = acc
	}
	res, err := p.c.Call(ctx, using, Invocation{Name: name, Args: args, CallID: "c0"})
	if err != nil {
		return err
	}
	return res.Get("c0", out)
}

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps {
	return domain.MailCaps{Move: true, Idle: true, ObjectID: true, CondStore: true, Thread: true}
}

// ==== mailboxes ================================================================

type mailbox struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ParentID     string `json:"parentId"`
	Role         string `json:"role"`
	TotalEmails  int    `json:"totalEmails"`
	UnreadEmails int    `json:"unreadEmails"`
	IsSubscribed bool   `json:"isSubscribed"`
}

func roleOf(r string) domain.FolderRole {
	switch strings.ToLower(r) {
	case "inbox":
		return domain.RoleInbox
	case "sent":
		return domain.RoleSent
	case "drafts":
		return domain.RoleDrafts
	case "trash":
		return domain.RoleTrash
	case "junk":
		return domain.RoleJunk
	case "archive":
		return domain.RoleArchive
	}
	return domain.RoleNone
}

func (p *Provider) mailboxes(ctx context.Context) ([]mailbox, error) {
	var out struct {
		List []mailbox `json:"list"`
	}
	if err := p.call(ctx, mailUsing, "Mailbox/get", map[string]any{"ids": nil}, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// ListFolders implements domain.MailProvider.
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	list, err := p.mailboxes(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]mailbox{}
	for _, m := range list {
		byID[m.ID] = m
	}
	paths := map[string]string{}
	var path func(id string, depth int) string
	path = func(id string, depth int) string {
		if v, ok := paths[id]; ok {
			return v
		}
		m := byID[id]
		v := id
		if m.ParentID != "" && depth < 32 {
			if _, ok := byID[m.ParentID]; ok {
				v = path(m.ParentID, depth+1) + "/" + id
			}
		}
		paths[id] = v
		return v
	}
	out := make([]domain.Folder, 0, len(list))
	byPath := map[string]string{}
	for _, m := range list {
		rid := path(m.ID, 0)
		byPath[rid] = m.ID
		out = append(out, domain.Folder{RemoteID: rid, Name: m.Name, Delimiter: "/", Role: roleOf(m.Role),
			Subscribed: m.IsSubscribed || m.Role != "", Total: m.TotalEmails, Unread: m.UnreadEmails})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemoteID < out[j].RemoteID })
	p.mu.Lock()
	p.paths, p.byPath = paths, byPath
	p.mu.Unlock()
	return out, nil
}

// mailboxID returns the JMAP id of a remote id (the last path segment).
func mailboxID(remoteID string) string {
	if i := strings.LastIndexByte(remoteID, '/'); i >= 0 {
		return remoteID[i+1:]
	}
	return remoteID
}

func (p *Provider) remoteID(ctx context.Context, mailboxID string) string {
	p.mu.Lock()
	v, ok := p.paths[mailboxID]
	p.mu.Unlock()
	if ok {
		return v
	}
	if _, err := p.ListFolders(ctx); err == nil {
		p.mu.Lock()
		v, ok = p.paths[mailboxID]
		p.mu.Unlock()
		if ok {
			return v
		}
	}
	return mailboxID
}

func (p *Provider) setOne(ctx context.Context, using []string, method string, args map[string]any) (SetResponse, error) {
	var res SetResponse
	if err := p.call(ctx, using, method, args, &res); err != nil {
		return res, err
	}
	return res, res.FirstError()
}

// CreateFolder implements domain.MailProvider.
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	create := map[string]any{"name": name, "isSubscribed": true}
	if parentRemoteID != "" {
		create["parentId"] = mailboxID(parentRemoteID)
	}
	res, err := p.setOne(ctx, mailUsing, "Mailbox/set", map[string]any{"create": map[string]any{"k": create}})
	if err != nil {
		return domain.Folder{}, err
	}
	id := res.CreatedID("k")
	rid := id
	if parentRemoteID != "" {
		rid = parentRemoteID + "/" + id
	}
	p.mu.Lock()
	if p.paths != nil {
		p.paths[id], p.byPath[rid] = rid, id
	}
	p.mu.Unlock()
	return domain.Folder{RemoteID: rid, Name: name, Delimiter: "/", Subscribed: true}, nil
}

// RenameFolder implements domain.MailProvider (the id path stays the same).
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	_, err := p.setOne(ctx, mailUsing, "Mailbox/set", map[string]any{"update": map[string]any{mailboxID(remoteID): map[string]any{"name": newName}}})
	if err != nil {
		return domain.Folder{}, err
	}
	return domain.Folder{RemoteID: remoteID, Name: newName, Delimiter: "/", Subscribed: true}, nil
}

// DeleteFolder implements domain.MailProvider.
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	_, err := p.setOne(ctx, mailUsing, "Mailbox/set", map[string]any{"destroy": []string{mailboxID(remoteID)}, "onDestroyRemoveEmails": false})
	return err
}

// ==== emails ===================================================================

type emailAddr struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type email struct {
	ID            string          `json:"id"`
	BlobID        string          `json:"blobId"`
	ThreadID      string          `json:"threadId"`
	MailboxIDs    map[string]bool `json:"mailboxIds"`
	Keywords      map[string]bool `json:"keywords"`
	Size          int64           `json:"size"`
	ReceivedAt    time.Time       `json:"receivedAt"`
	MessageID     []string        `json:"messageId"`
	InReplyTo     []string        `json:"inReplyTo"`
	References    []string        `json:"references"`
	Subject       string          `json:"subject"`
	From          []emailAddr     `json:"from"`
	To            []emailAddr     `json:"to"`
	Cc            []emailAddr     `json:"cc"`
	Bcc           []emailAddr     `json:"bcc"`
	ReplyTo       []emailAddr     `json:"replyTo"`
	SentAt        *time.Time      `json:"sentAt"`
	HasAttachment bool            `json:"hasAttachment"`
}

var headerProps = []string{"id", "blobId", "threadId", "mailboxIds", "keywords", "size", "receivedAt", "messageId",
	"inReplyTo", "references", "subject", "from", "to", "cc", "bcc", "replyTo", "sentAt", "hasAttachment"}

// keyword <-> flag mapping (RFC 8621 section 4.1.1).
var kwToFlag = map[string]domain.Flag{"$seen": domain.FlagSeen, "$flagged": domain.FlagFlagged, "$answered": domain.FlagAnswered,
	"$draft": domain.FlagDraft, "$forwarded": domain.FlagForwarded}

// FlagsFromKeywords maps JMAP keywords to flags (custom keywords pass through).
func FlagsFromKeywords(kw map[string]bool) []domain.Flag {
	out := []domain.Flag{}
	for k, v := range kw {
		if !v {
			continue
		}
		if f, ok := kwToFlag[strings.ToLower(k)]; ok {
			out = append(out, f)
		} else {
			out = append(out, domain.Flag(k))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// KeywordOf maps a flag to its JMAP keyword.
func KeywordOf(f domain.Flag) string {
	for k, v := range kwToFlag {
		if v == f {
			return k
		}
	}
	return strings.TrimPrefix(string(f), `\`)
}

func addrs(in []emailAddr) []domain.Address {
	out := make([]domain.Address, 0, len(in))
	for _, a := range in {
		out = append(out, domain.Address{Name: a.Name, Email: a.Email})
	}
	return out
}

func trimIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, s := range ids {
		out = append(out, strings.Trim(strings.TrimSpace(s), "<>"))
	}
	return out
}

func (e email) remote(folder string) domain.RemoteMessage {
	h := domain.MessageHeaderInfo{InReplyTo: trimIDs(e.InReplyTo), References: trimIDs(e.References), Subject: e.Subject,
		From: addrs(e.From), To: addrs(e.To), Cc: addrs(e.Cc), Bcc: addrs(e.Bcc), ReplyTo: addrs(e.ReplyTo),
		ServerObjectID: e.ID, ServerThreadID: e.ThreadID}
	if len(e.MessageID) > 0 {
		h.MessageID = strings.Trim(e.MessageID[0], "<> ")
	}
	if e.SentAt != nil {
		h.Date = *e.SentAt
	} else {
		h.Date = e.ReceivedAt
	}
	return domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folder, ID: e.ID}, Header: h, Flags: FlagsFromKeywords(e.Keywords),
		Size: e.Size, ReceivedAt: e.ReceivedAt, HasAttachments: e.HasAttachment}
}

func (p *Provider) getEmails(ctx context.Context, ids []string, props []string) ([]email, string, error) {
	var all []email
	state := ""
	for i := 0; i < len(ids) || (i == 0 && len(ids) == 0); i += 200 {
		chunk := ids[i:min(i+200, len(ids))]
		if chunk == nil {
			chunk = []string{} // null would mean "all emails"
		}
		var out struct {
			State    string   `json:"state"`
			List     []email  `json:"list"`
			NotFound []string `json:"notFound"`
		}
		if err := p.call(ctx, mailUsing, "Email/get", map[string]any{"ids": chunk, "properties": props}, &out); err != nil {
			return nil, "", err
		}
		state = out.State
		all = append(all, out.List...)
		if len(ids) == 0 {
			break
		}
	}
	return all, state, nil
}

// queryIDs returns all email ids in a mailbox.
func (p *Provider) queryIDs(ctx context.Context, mbox string) ([]string, error) {
	var ids []string
	for pos := 0; ; {
		var out struct {
			IDs   []string `json:"ids"`
			Total *int     `json:"total"`
		}
		err := p.call(ctx, mailUsing, "Email/query", map[string]any{
			"filter": map[string]any{"inMailbox": mbox}, "sort": []map[string]any{{"property": "receivedAt", "isAscending": false}},
			"position": pos, "limit": p.cfg.PageSize, "calculateTotal": true, "collapseThreads": false,
		}, &out)
		if err != nil {
			return nil, err
		}
		ids = append(ids, out.IDs...)
		pos += len(out.IDs)
		if len(out.IDs) == 0 || (out.Total != nil && pos >= *out.Total) || len(out.IDs) < p.cfg.PageSize {
			return ids, nil
		}
	}
}

// folderState is the persisted SyncState of a mailbox.
type folderState struct {
	EmailState string   `json:"e"`
	IDs        []string `json:"ids"`
}

type changes struct {
	updated map[string]bool
	any     bool
}

// emailChanges collects Email/changes since state. ok=false means the
// server cannot calculate them (full diff needed).
func (p *Provider) emailChanges(ctx context.Context, since string) (changes, string, bool, error) {
	ch := changes{updated: map[string]bool{}}
	state := since
	for i := 0; i < 50; i++ {
		var out struct {
			NewState       string   `json:"newState"`
			HasMoreChanges bool     `json:"hasMoreChanges"`
			Created        []string `json:"created"`
			Updated        []string `json:"updated"`
			Destroyed      []string `json:"destroyed"`
		}
		err := p.call(ctx, mailUsing, "Email/changes", map[string]any{"sinceState": state, "maxChanges": 500}, &out)
		var me *MethodError
		if errors.As(err, &me) && me.Type == "cannotCalculateChanges" {
			return ch, "", false, nil
		}
		if err != nil {
			return ch, "", false, err
		}
		ch.any = ch.any || len(out.Created)+len(out.Updated)+len(out.Destroyed) > 0
		for _, id := range out.Updated {
			ch.updated[id] = true
		}
		state = out.NewState
		if !out.HasMoreChanges {
			return ch, state, true, nil
		}
	}
	return ch, "", false, nil
}

// Sync implements domain.MailProvider.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	var st folderState
	if state != "" {
		_ = json.Unmarshal([]byte(state), &st)
	}
	mbox := mailboxID(folderRemoteID)
	var delta domain.SyncDelta
	var ch changes
	newEmailState := ""
	full := true
	if st.EmailState != "" {
		c, ns, ok, err := p.emailChanges(ctx, st.EmailState)
		if err != nil {
			return delta, state, err
		}
		if ok {
			full = false
			ch, newEmailState = c, ns
			if !c.any {
				st.EmailState = ns
				return delta, encodeState(st), nil
			}
		}
	}
	if newEmailState == "" {
		// Read the state before the query so that no change is lost.
		_, s, err := p.getEmails(ctx, nil, []string{"id"})
		if err != nil {
			return delta, state, err
		}
		newEmailState = s
	}
	ids, err := p.queryIDs(ctx, mbox)
	if err != nil {
		var me *MethodError
		if errors.As(err, &me) && (me.Type == "invalidArguments" || me.Type == "notFound") {
			return delta, state, fmt.Errorf("%w: mailbox %s", ErrNotFound, folderRemoteID)
		}
		return delta, state, err
	}
	known := make(map[string]bool, len(st.IDs))
	for _, id := range st.IDs {
		known[id] = true
	}
	current := make(map[string]bool, len(ids))
	var added, flagCheck []string
	for _, id := range ids {
		current[id] = true
		switch {
		case !known[id]:
			added = append(added, id)
		case full || ch.updated[id]:
			flagCheck = append(flagCheck, id)
		}
	}
	for _, id := range st.IDs {
		if !current[id] {
			delta.Removed = append(delta.Removed, domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: id})
		}
	}
	if len(added) > 0 {
		list, _, err := p.getEmails(ctx, added, headerProps)
		if err != nil {
			return domain.SyncDelta{}, state, err
		}
		for _, e := range list {
			delta.Added = append(delta.Added, e.remote(folderRemoteID))
		}
	}
	if len(flagCheck) > 0 && st.EmailState != "" {
		list, _, err := p.getEmails(ctx, flagCheck, []string{"id", "keywords"})
		if err != nil {
			return domain.SyncDelta{}, state, err
		}
		for _, e := range list {
			delta.FlagsChanged = append(delta.FlagsChanged, domain.RemoteMessage{
				Ref: domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: e.ID}, Flags: FlagsFromKeywords(e.Keywords)})
		}
	}
	st.IDs, st.EmailState = ids, newEmailState
	return delta, encodeState(st), nil
}

func encodeState(st folderState) domain.SyncState {
	b, _ := json.Marshal(st)
	return domain.SyncState(b)
}

// FetchRaw implements domain.MailProvider (blob download).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	list, _, err := p.getEmails(ctx, []string{ref.ID}, []string{"id", "blobId"})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 || list[0].BlobID == "" {
		return nil, fmt.Errorf("%w: email %s", ErrNotFound, ref.ID)
	}
	acc, err := p.accountID(ctx)
	if err != nil {
		return nil, err
	}
	return p.c.Download(ctx, acc, list[0].BlobID)
}

// Move implements domain.MailProvider (Email/set mailboxIds patch).
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	dst := mailboxID(to)
	update := map[string]any{}
	for _, r := range refs {
		patch := map[string]any{"mailboxIds/" + dst: true}
		if src := mailboxID(r.FolderRemoteID); src != "" && src != dst {
			patch["mailboxIds/"+src] = nil
		}
		update[r.ID] = patch
	}
	if _, err := p.setOne(ctx, mailUsing, "Email/set", map[string]any{"update": update}); err != nil {
		return nil, err
	}
	out := make([]domain.RemoteRef, len(refs))
	for i, r := range refs {
		out[i] = domain.RemoteRef{FolderRemoteID: to, ID: r.ID}
	}
	return out, nil
}

// SetFlags implements domain.MailProvider (keywords patch).
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	if len(refs) == 0 || len(add)+len(remove) == 0 {
		return nil
	}
	patch := map[string]any{}
	for _, f := range add {
		patch["keywords/"+KeywordOf(f)] = true
	}
	for _, f := range remove {
		patch["keywords/"+KeywordOf(f)] = nil
	}
	update := map[string]any{}
	for _, r := range refs {
		update[r.ID] = patch
	}
	_, err := p.setOne(ctx, mailUsing, "Email/set", map[string]any{"update": update})
	return err
}

// Delete implements domain.MailProvider (Email/set destroy). A message in
// several mailboxes is only removed from the given one.
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	list, _, err := p.getEmails(ctx, ids, []string{"id", "mailboxIds"})
	if err != nil {
		return err
	}
	update := map[string]any{}
	var destroy []string
	for _, e := range list {
		var folder string
		for _, r := range refs {
			if r.ID == e.ID {
				folder = mailboxID(r.FolderRemoteID)
			}
		}
		if len(e.MailboxIDs) > 1 && folder != "" && e.MailboxIDs[folder] {
			update[e.ID] = map[string]any{"mailboxIds/" + folder: nil}
		} else {
			destroy = append(destroy, e.ID)
		}
	}
	args := map[string]any{}
	if len(update) > 0 {
		args["update"] = update
	}
	if len(destroy) > 0 {
		args["destroy"] = destroy
	}
	if len(args) == 0 {
		return nil
	}
	_, err = p.setOne(ctx, mailUsing, "Email/set", args)
	return err
}

// importEmail uploads raw and imports it into mailboxes with keywords.
func (p *Provider) importEmail(ctx context.Context, raw io.Reader, mailboxes []string, flags []domain.Flag) (string, error) {
	acc, err := p.accountID(ctx)
	if err != nil {
		return "", err
	}
	blobID, err := p.c.Upload(ctx, acc, raw, "message/rfc822")
	if err != nil {
		return "", err
	}
	mb := map[string]bool{}
	for _, m := range mailboxes {
		mb[mailboxID(m)] = true
	}
	kw := map[string]bool{}
	for _, f := range flags {
		kw[KeywordOf(f)] = true
	}
	var res struct {
		Created    map[string]struct{ ID string } `json:"created"`
		NotCreated map[string]SetError            `json:"notCreated"`
	}
	err = p.call(ctx, mailUsing, "Email/import", map[string]any{"emails": map[string]any{"k": map[string]any{
		"blobId": blobID, "mailboxIds": mb, "keywords": kw}}}, &res)
	if err != nil {
		return "", err
	}
	if e, ok := res.NotCreated["k"]; ok {
		return "", &e
	}
	return res.Created["k"].ID, nil
}

// Append implements domain.MailProvider (upload + Email/import).
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	id, err := p.importEmail(ctx, raw, []string{folderRemoteID}, flags)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	return domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: id}, nil
}

// Close implements domain.MailProvider.
func (p *Provider) Close() error {
	p.cfg.HTTPClient.CloseIdleConnections()
	return nil
}

// ==== identities ===============================================================

// Identity is a JMAP sending identity (RFC 8621 section 6).
type Identity struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Email   string      `json:"email"`
	ReplyTo []emailAddr `json:"replyTo"`
	Bcc     []emailAddr `json:"bcc"`
}

// Identities returns the sending identities (Identity/get).
func (p *Provider) Identities(ctx context.Context) ([]Identity, error) {
	var out struct {
		List []Identity `json:"list"`
	}
	acc, err := p.submissionAccount(ctx)
	if err != nil {
		return nil, err
	}
	err = p.call(ctx, []string{CapCore, CapMail, CapSubmission}, "Identity/get", map[string]any{"accountId": acc, "ids": nil}, &out)
	return out.List, err
}

// DomainIdentities maps the JMAP identities to domain identities.
func (p *Provider) DomainIdentities(ctx context.Context, accountID int64) ([]domain.Identity, error) {
	ids, err := p.Identities(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Identity, 0, len(ids))
	for i, id := range ids {
		di := domain.Identity{AccountID: accountID, Name: id.Name, Email: id.Email, IsDefault: i == 0, BccSelf: len(id.Bcc) > 0}
		if len(id.ReplyTo) > 0 {
			di.ReplyTo = id.ReplyTo[0].Email
		}
		out = append(out, di)
	}
	return out, nil
}

func (p *Provider) submissionAccount(ctx context.Context) (string, error) {
	s, err := p.c.Session(ctx, false)
	if err != nil {
		return "", err
	}
	if id := s.AccountID(CapSubmission); id != "" {
		return id, nil
	}
	return p.accountID(ctx)
}

// ==== push =====================================================================

// Watch implements domain.MailProvider with the JMAP EventSource (RFC 8620
// section 7.3); without one it polls. Every state change of Email or
// Mailbox emits a hint.
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	s, err := p.c.Session(ctx, false)
	if err != nil {
		return nil, err
	}
	ch := make(chan domain.ChangeHint, 1)
	hint := func() {
		select {
		case ch <- domain.ChangeHint{FolderRemoteID: folderRemoteID}:
		default:
		}
	}
	go func() {
		defer close(ch)
		if s.EventSourceURL == "" {
			t := time.NewTicker(p.cfg.PollInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					hint()
				}
			}
		}
		backoff := p.cfg.MinBackoff
		for ctx.Err() == nil {
			start := time.Now()
			err := p.eventStream(ctx, s.EventSourceURL, hint)
			if ctx.Err() != nil {
				return
			}
			if time.Since(start) > time.Minute {
				backoff = p.cfg.MinBackoff
			}
			p.log.Debug("jmap: event source ended", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			hint() // changes may have been missed while disconnected
			backoff = min(backoff*2, p.cfg.MaxBackoff)
		}
	}()
	return ch, nil
}

func (p *Provider) eventStream(ctx context.Context, tpl string, hint func()) error {
	u := expand(tpl, map[string]string{"types": "Email,Mailbox", "closeafter": "no", "ping": "60"})
	u = strings.ReplaceAll(u, "Email%2CMailbox", "Email,Mailbox")
	if err := p.c.checkURL("eventSourceUrl", u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if p.c.Auth != nil {
		if err := p.c.Auth(ctx, req); err != nil {
			return err
		}
	}
	// The stream is long-lived: no client timeout.
	hc := &http.Client{Transport: p.cfg.HTTPClient.Transport, CheckRedirect: safehttp.CheckRedirect(nil, 10)}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jmap: event source: %s", resp.Status)
	}
	return readEvents(resp.Body, func(event, data string) {
		if event == "state" || (event == "" && strings.Contains(data, "\"changed\"")) {
			hint()
		}
	})
}

// readEvents parses a text/event-stream.
func readEvents(r io.Reader, fn func(event, data string)) error {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	event, data := "", ""
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			line := strings.TrimRight(string(buf[:i]), "\r")
			buf = buf[i+1:]
			switch {
			case line == "":
				if data != "" || event != "" {
					fn(event, data)
				}
				event, data = "", ""
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "data:"):
				data += strings.TrimSpace(line[5:])
			}
		}
		if len(buf) > 1<<20 {
			return errors.New("jmap: event too large")
		}
		if err != nil {
			return err
		}
	}
}
