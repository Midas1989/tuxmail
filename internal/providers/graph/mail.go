// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Provider implements domain.MailProvider on Graph.
type Provider struct {
	*Client
	km store.KeywordMapper

	mu     sync.Mutex
	roles  map[string]domain.FolderRole // graph folder id -> role
	known  map[string]map[string]bool   // folder id -> message ids reported (this process)
	closed bool
}

var _ domain.MailProvider = (*Provider)(nil)

// New creates a mail provider.
func New(cfg Config) (*Provider, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Provider{Client: c, km: store.DefaultKeywordMapper, known: map[string]map[string]bool{}}, nil
}

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps {
	return domain.MailCaps{Move: true, ObjectID: true, Thread: true, Idle: p.cfg.Notifications != nil}
}

// Close implements domain.MailProvider.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// ==== folders =================================================================

type gFolder struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ParentFolderID   string `json:"parentFolderId"`
	ChildFolderCount int    `json:"childFolderCount"`
	UnreadItemCount  int    `json:"unreadItemCount"`
	TotalItemCount   int    `json:"totalItemCount"`
	IsHidden         bool   `json:"isHidden"`
}

var wellKnown = []struct {
	name string
	role domain.FolderRole
}{
	{"inbox", domain.RoleInbox}, {"sentitems", domain.RoleSent}, {"drafts", domain.RoleDrafts},
	{"deleteditems", domain.RoleTrash}, {"junkemail", domain.RoleJunk}, {"archive", domain.RoleArchive},
}

// loadRoles resolves the well-known folder ids with one $batch.
func (p *Provider) loadRoles(ctx context.Context) (map[string]domain.FolderRole, error) {
	p.mu.Lock()
	r := p.roles
	p.mu.Unlock()
	if r != nil {
		return r, nil
	}
	reqs := make([]BatchRequest, len(wellKnown))
	for i, w := range wellKnown {
		reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodGet, URL: p.user() + "/mailFolders/" + w.name + "?$select=id"}
	}
	resp, err := p.Batch(ctx, reqs)
	if err != nil {
		return nil, err
	}
	r = map[string]domain.FolderRole{}
	for i, w := range wellKnown {
		br := resp[strconv.Itoa(i)]
		if br.Err() != nil {
			continue // e.g. no archive folder
		}
		var f gFolder
		if json.Unmarshal(br.Body, &f) == nil && f.ID != "" {
			r[f.ID] = w.role
		}
	}
	p.mu.Lock()
	p.roles = r
	p.mu.Unlock()
	return r, nil
}

// RoleFolderID returns the Graph id of a well-known folder.
func (p *Provider) RoleFolderID(ctx context.Context, role domain.FolderRole) (string, error) {
	roles, err := p.loadRoles(ctx)
	if err != nil {
		return "", err
	}
	for id, r := range roles {
		if r == role {
			return id, nil
		}
	}
	return "", fmt.Errorf("graph: no %s folder", role)
}

const folderSelect = "$select=id,displayName,parentFolderId,childFolderCount,unreadItemCount,totalItemCount,isHidden"

// ListFolders implements domain.MailProvider.
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	roles, err := p.loadRoles(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Folder
	var walk func(path, prefix string, depth int) error
	walk = func(path, prefix string, depth int) error {
		fs, err := list[gFolder](ctx, p.Client, path)
		if err != nil {
			return err
		}
		for _, f := range fs {
			if f.IsHidden {
				continue
			}
			remote := f.ID
			if prefix != "" {
				remote = prefix + "/" + f.ID
			}
			out = append(out, domain.Folder{RemoteID: remote, Name: f.DisplayName, Delimiter: "/", Role: roles[f.ID],
				Subscribed: true, Unread: f.UnreadItemCount, Total: f.TotalItemCount})
			if f.ChildFolderCount > 0 && depth < 20 {
				if err := walk(fmt.Sprintf("%s/mailFolders/%s/childFolders?%s&$top=100", p.user(), url.PathEscape(f.ID), folderSelect),
					remote, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(p.user()+"/mailFolders?"+folderSelect+"&$top=100", "", 0); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateFolder implements domain.MailProvider.
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	path := p.user() + "/mailFolders"
	if parentRemoteID != "" {
		path += "/" + url.PathEscape(lastSeg(parentRemoteID)) + "/childFolders"
	}
	var f gFolder
	if err := p.do(ctx, http.MethodPost, path, map[string]any{"displayName": name}, &f); err != nil {
		return domain.Folder{}, err
	}
	remote := f.ID
	if parentRemoteID != "" {
		remote = parentRemoteID + "/" + f.ID
	}
	return domain.Folder{RemoteID: remote, Name: f.DisplayName, Delimiter: "/", Subscribed: true}, nil
}

// RenameFolder implements domain.MailProvider.
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	var f gFolder
	if err := p.do(ctx, http.MethodPatch, p.user()+"/mailFolders/"+url.PathEscape(lastSeg(remoteID)),
		map[string]any{"displayName": newName}, &f); err != nil {
		return domain.Folder{}, err
	}
	return domain.Folder{RemoteID: remoteID, Name: f.DisplayName, Delimiter: "/", Subscribed: true}, nil
}

// DeleteFolder implements domain.MailProvider.
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	err := p.do(ctx, http.MethodDelete, p.user()+"/mailFolders/"+url.PathEscape(lastSeg(remoteID)), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ==== messages ================================================================

type gAddr struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type gMessage struct {
	ID                string    `json:"id"`
	ConversationID    string    `json:"conversationId"`
	ConversationIndex string    `json:"conversationIndex"`
	InternetMessageID string    `json:"internetMessageId"`
	Subject           string    `json:"subject"`
	From              *gAddr    `json:"from"`
	Sender            *gAddr    `json:"sender"`
	To                []gAddr   `json:"toRecipients"`
	Cc                []gAddr   `json:"ccRecipients"`
	Bcc               []gAddr   `json:"bccRecipients"`
	ReplyTo           []gAddr   `json:"replyTo"`
	SentDateTime      time.Time `json:"sentDateTime"`
	ReceivedDateTime  time.Time `json:"receivedDateTime"`
	IsRead            bool      `json:"isRead"`
	IsDraft           bool      `json:"isDraft"`
	HasAttachments    bool      `json:"hasAttachments"`
	Categories        []string  `json:"categories"`
	Flag              *struct {
		FlagStatus string `json:"flagStatus"`
	} `json:"flag"`
	ParentFolderID string          `json:"parentFolderId"`
	BodyPreview    string          `json:"bodyPreview"`
	Removed        json.RawMessage `json:"@removed"`
}

const messageSelect = "id,conversationId,conversationIndex,internetMessageId,subject,from,sender,toRecipients," +
	"ccRecipients,bccRecipients,replyTo,sentDateTime,receivedDateTime,isRead,isDraft,hasAttachments,categories,flag,parentFolderId,bodyPreview"

func addrs(in []gAddr) []domain.Address {
	var out []domain.Address
	for _, a := range in {
		if a.EmailAddress.Address != "" {
			out = append(out, domain.Address{Name: a.EmailAddress.Name, Email: a.EmailAddress.Address})
		}
	}
	return out
}

func (p *Provider) remoteMessage(folderRemote string, m gMessage) domain.RemoteMessage {
	h := domain.MessageHeaderInfo{
		MessageID: strings.Trim(m.InternetMessageID, "<>"), Subject: m.Subject,
		To: addrs(m.To), Cc: addrs(m.Cc), Bcc: addrs(m.Bcc), ReplyTo: addrs(m.ReplyTo), Date: m.SentDateTime,
		ServerObjectID: m.ID, ServerThreadID: m.ConversationID, ThreadIndex: m.ConversationIndex,
	}
	if m.From != nil {
		h.From = addrs([]gAddr{*m.From})
	}
	if h.Date.IsZero() {
		h.Date = m.ReceivedDateTime
	}
	return domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folderRemote, ID: m.ID}, Header: h,
		Flags: p.flags(m), ReceivedAt: m.ReceivedDateTime, HasAttachments: m.HasAttachments, Preview: strings.TrimSpace(m.BodyPreview)}
}

func (p *Provider) flags(m gMessage) []domain.Flag {
	var fl []domain.Flag
	if m.IsRead {
		fl = append(fl, domain.FlagSeen)
	}
	if m.IsDraft {
		fl = append(fl, domain.FlagDraft)
	}
	if m.Flag != nil && m.Flag.FlagStatus == "flagged" {
		fl = append(fl, domain.FlagFlagged)
	}
	for _, c := range m.Categories {
		if path, ok := store.TagFromOutlookCategory(c); ok {
			if kw, err := p.km.ToKeyword(path); err == nil {
				fl = append(fl, domain.Flag(kw))
			}
		}
	}
	return fl
}

// syncState is the JSON SyncState of a folder.
type syncState struct {
	DeltaLink string `json:"d"`
}

// Sync implements domain.MailProvider with a delta query per folder; the
// deltaLink is the SyncState. An expired delta token restarts with a full
// resync.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	fid := lastSeg(folderRemoteID)
	var st syncState
	if state != "" {
		_ = json.Unmarshal([]byte(state), &st)
	}
	initial := st.DeltaLink == ""
	start := fmt.Sprintf("%s/mailFolders/%s/messages/delta?$select=%s", p.user(), url.PathEscape(fid), messageSelect)
	path := st.DeltaLink
	if initial {
		path = start
	}
	var delta domain.SyncDelta
	p.mu.Lock()
	known := p.known[fid]
	if known == nil || initial {
		known = map[string]bool{}
		p.known[fid] = known
	}
	p.mu.Unlock()
	var next string
	for path != "" {
		var pg page[gMessage]
		err := p.rest.Do(ctx, rest.Request{Method: http.MethodGet, Path: path,
			Header: http.Header{"Prefer": {`IdType="ImmutableId"`, fmt.Sprintf("odata.maxpagesize=%d", p.cfg.PageSize)}}}, &pg)
		if err != nil && isGone(err) && !initial {
			p.log.Info("graph: delta token expired, full resync", "folder", fid)
			delta = domain.SyncDelta{FullResync: true}
			initial, path = true, start
			p.mu.Lock()
			known = map[string]bool{}
			p.known[fid] = known
			p.mu.Unlock()
			continue
		}
		if err != nil {
			return domain.SyncDelta{}, state, err
		}
		for _, m := range pg.Value {
			if len(m.Removed) > 0 {
				delta.Removed = append(delta.Removed, domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: m.ID})
				p.mu.Lock()
				delete(known, m.ID)
				p.mu.Unlock()
				continue
			}
			rm := p.remoteMessage(folderRemoteID, m)
			p.mu.Lock()
			seen := known[m.ID]
			known[m.ID] = true
			p.mu.Unlock()
			if seen {
				delta.FlagsChanged = append(delta.FlagsChanged, rm)
			} else {
				// Unknown in this process: report as added; the sync engine
				// recognises already stored messages (Message-ID) and only
				// updates them.
				delta.Added = append(delta.Added, rm)
			}
		}
		if pg.DeltaLink != "" {
			next = pg.DeltaLink
		}
		path = pg.NextLink
	}
	b, _ := json.Marshal(syncState{DeltaLink: next})
	return delta, domain.SyncState(b), nil
}

// FetchRaw implements domain.MailProvider (MIME content, $value).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	resp, err := p.rest.Raw(ctx, rest.Request{Method: http.MethodGet, Path: p.user() + "/messages/" + url.PathEscape(ref.ID) + "/$value",
		Header: http.Header{"Accept": {"message/rfc822, */*"}}})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Move implements domain.MailProvider (immutable ids survive the move).
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, toFolderRemoteID string) ([]domain.RemoteRef, error) {
	return p.moveCopy(ctx, "move", refs, toFolderRemoteID)
}

// Copy copies messages into another folder and returns the new references.
func (p *Provider) Copy(ctx context.Context, refs []domain.RemoteRef, toFolderRemoteID string) ([]domain.RemoteRef, error) {
	return p.moveCopy(ctx, "copy", refs, toFolderRemoteID)
}

func (p *Provider) moveCopy(ctx context.Context, op string, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	reqs := make([]BatchRequest, len(refs))
	for i, r := range refs {
		reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodPost, URL: p.user() + "/messages/" + url.PathEscape(r.ID) + "/" + op,
			Body: map[string]string{"destinationId": lastSeg(to)}}
	}
	resp, err := p.Batch(ctx, reqs)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RemoteRef, len(refs))
	for i := range refs {
		br := resp[strconv.Itoa(i)]
		if err := br.Err(); err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, err
		}
		var m gMessage
		_ = json.Unmarshal(br.Body, &m)
		out[i] = domain.RemoteRef{FolderRemoteID: to, ID: m.ID}
	}
	return out, nil
}

// SetFlags implements domain.MailProvider: \Seen → isRead, \Flagged →
// flag.flagStatus, keywords → categories, \Answered/$Forwarded →
// PidTagLastVerbExecuted.
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	patch := map[string]any{}
	var addCat, remCat []string
	for _, f := range add {
		switch f {
		case domain.FlagSeen:
			patch["isRead"] = true
		case domain.FlagFlagged:
			patch["flag"] = map[string]string{"flagStatus": "flagged"}
		case domain.FlagAnswered:
			patch["singleValueExtendedProperties"] = []map[string]string{{"id": "Integer 0x1081", "value": "102"}}
		case domain.FlagForwarded:
			patch["singleValueExtendedProperties"] = []map[string]string{{"id": "Integer 0x1081", "value": "104"}}
		case domain.FlagDraft, domain.FlagDeleted:
		default:
			if c, ok := p.category(f); ok {
				addCat = append(addCat, c)
			}
		}
	}
	for _, f := range remove {
		switch f {
		case domain.FlagSeen:
			patch["isRead"] = false
		case domain.FlagFlagged:
			patch["flag"] = map[string]string{"flagStatus": "notFlagged"}
		case domain.FlagAnswered, domain.FlagForwarded, domain.FlagDraft, domain.FlagDeleted:
		default:
			if c, ok := p.category(f); ok {
				remCat = append(remCat, c)
			}
		}
	}
	// Categories are a list property: read the current ones first.
	cats := map[string][]string{}
	if len(addCat)+len(remCat) > 0 {
		reqs := make([]BatchRequest, len(refs))
		for i, r := range refs {
			reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodGet, URL: p.user() + "/messages/" + url.PathEscape(r.ID) + "?$select=categories"}
		}
		resp, err := p.Batch(ctx, reqs)
		if err != nil {
			return err
		}
		for i, r := range refs {
			var m gMessage
			if br := resp[strconv.Itoa(i)]; br.Err() == nil {
				_ = json.Unmarshal(br.Body, &m)
			}
			cats[r.ID] = mergeCategories(m.Categories, addCat, remCat)
		}
	}
	if len(patch) == 0 && len(cats) == 0 {
		return nil
	}
	reqs := make([]BatchRequest, len(refs))
	for i, r := range refs {
		body := map[string]any{}
		for k, v := range patch {
			body[k] = v
		}
		if c, ok := cats[r.ID]; ok {
			body["categories"] = c
		}
		reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodPatch, URL: p.user() + "/messages/" + url.PathEscape(r.ID), Body: body}
	}
	resp, err := p.Batch(ctx, reqs)
	if err != nil {
		return err
	}
	for _, br := range resp {
		if err := br.Err(); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (p *Provider) category(f domain.Flag) (string, bool) {
	path, ok := p.km.FromKeyword(string(f))
	if !ok {
		return "", false
	}
	c, err := store.TagToOutlookCategory(path)
	return c, err == nil
}

func mergeCategories(cur, add, remove []string) []string {
	out := []string{}
	has := map[string]bool{}
	rm := map[string]bool{}
	for _, r := range remove {
		rm[strings.ToLower(r)] = true
	}
	for _, c := range append(append([]string{}, cur...), add...) {
		k := strings.ToLower(c)
		if has[k] || rm[k] {
			continue
		}
		has[k] = true
		out = append(out, c)
	}
	return out
}

// Delete implements domain.MailProvider: permanent deletion (the sync engine
// moves to deleteditems first unless permanent deletion is requested;
// Exchange keeps deleted items recoverable).
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	reqs := make([]BatchRequest, len(refs))
	for i, r := range refs {
		reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodDelete, URL: p.user() + "/messages/" + url.PathEscape(r.ID)}
	}
	resp, err := p.Batch(ctx, reqs)
	if err != nil {
		return err
	}
	for _, br := range resp {
		if err := br.Err(); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}

// MoveToTrash moves messages to deleteditems.
func (p *Provider) MoveToTrash(ctx context.Context, refs []domain.RemoteRef) error {
	trash, err := p.RoleFolderID(ctx, domain.RoleTrash)
	if err != nil {
		return err
	}
	_, err = p.Move(ctx, refs, trash)
	return err
}

// Append implements domain.MailProvider: creates the message from MIME in
// the folder. Graph creates MIME uploads as drafts (isDraft cannot be
// cleared afterwards); this is used for drafts and cross-account moves.
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	data, err := io.ReadAll(raw)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	var m gMessage
	err = p.rest.Do(ctx, rest.Request{Method: http.MethodPost,
		Path: p.user() + "/mailFolders/" + url.PathEscape(lastSeg(folderRemoteID)) + "/messages",
		Body: []byte(base64.StdEncoding.EncodeToString(data)), ContentType: "text/plain"}, &m)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	ref := domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: m.ID}
	var add []domain.Flag
	for _, f := range flags {
		if f != domain.FlagDraft {
			add = append(add, f)
		}
	}
	if len(add) > 0 {
		if err := p.SetFlags(ctx, []domain.RemoteRef{ref}, add, nil); err != nil {
			p.log.Warn("graph: setting flags after append failed", "err", err)
		}
	}
	return ref, nil
}

// CreateDraft creates a draft from MIME in the Drafts folder.
func (p *Provider) CreateDraft(ctx context.Context, raw []byte) (domain.RemoteRef, error) {
	drafts, err := p.RoleFolderID(ctx, domain.RoleDrafts)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	return p.Append(ctx, drafts, bytes.NewReader(raw), nil)
}

// Watch implements domain.MailProvider. With change notifications it
// subscribes to the folder; otherwise it polls the folder counters every
// PollInterval and signals when they changed (plus every 10 intervals).
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	fid := lastSeg(folderRemoteID)
	ch := make(chan domain.ChangeHint, 1)
	hint := func() {
		select {
		case ch <- domain.ChangeHint{FolderRemoteID: folderRemoteID}:
		default:
		}
	}
	var notify <-chan struct{}
	if n := p.cfg.Notifications; n != nil {
		c, err := n.Subscribe(ctx, p.Client, fmt.Sprintf("%s/mailFolders('%s')/messages", strings.TrimPrefix(p.user(), "/"), fid))
		if err != nil {
			p.log.Warn("graph: change notifications unavailable, polling", "err", err)
		} else {
			notify = c
		}
	}
	go func() {
		defer close(ch)
		t := time.NewTicker(p.cfg.PollInterval)
		defer t.Stop()
		var last string
		n := 0
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-notify:
				if !ok {
					notify = nil
					continue
				}
				hint()
			case <-t.C:
				n++
				var f gFolder
				if err := p.get(ctx, p.user()+"/mailFolders/"+url.PathEscape(fid)+"?$select=totalItemCount,unreadItemCount", &f); err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				cur := fmt.Sprintf("%d/%d", f.TotalItemCount, f.UnreadItemCount)
				if (last != "" && cur != last) || n%10 == 0 {
					hint()
				}
				last = cur
			}
		}
	}()
	return ch, nil
}
