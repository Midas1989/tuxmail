// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gmail implements Gmail through the Gmail API (KONZEPT 7.4):
// labels as folders (system labels mapped to roles) and user labels also as
// tags, incremental sync with the history API (historyId as SyncState),
// threads → ServerThreadID, raw message download, label modification for
// read/star/move, trash/delete, raw send (Gmail stores Sent itself),
// polling for changes and the vacation responder (ServerVacationSync).
// Plain net/http + JSON (no google.golang.org/api dependency).
//
// Folder remote ids are paths of label ids separated by "/" following the
// label name hierarchy ("Kunde/Müller" → "Label_1/Label_2").
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/mimehdr"
	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// DefaultBaseURL is the Gmail API root.
const DefaultBaseURL = "https://gmail.googleapis.com"

// maxRawResponse bounds a format=raw message (base64 in JSON: 1.37x the
// message size, Gmail messages are at most about 50 MB).
const maxRawResponse = 128 << 20

// Config configures a Provider.
type Config struct {
	BaseURL    string
	Tokens     rest.TokenSource
	HTTPClient *http.Client
	// PollInterval of Watch (default 60 s).
	PollInterval time.Duration
	// Concurrency of metadata requests (default 8).
	Concurrency int
	Logger      *slog.Logger
	Sleep       func(ctx context.Context, d time.Duration) error
}

// Provider implements domain.MailProvider and domain.Transport.
type Provider struct {
	cfg  Config
	rest *rest.Client
	log  *slog.Logger
	km   store.KeywordMapper

	mu     sync.Mutex
	labels map[string]label // id -> label
}

var (
	_ domain.MailProvider         = (*Provider)(nil)
	_ domain.Transport            = (*Provider)(nil)
	_ vacation.ServerVacationSync = (*Provider)(nil)
)

// New creates a provider.
func New(cfg Config) (*Provider, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("gmail: a token source is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	rc := &rest.Client{BaseURL: strings.TrimRight(cfg.BaseURL, "/"), HTTP: cfg.HTTPClient, Tokens: cfg.Tokens,
		DecodeError: decodeError, Sleep: cfg.Sleep}
	return &Provider{cfg: cfg, rest: rc, log: cfg.Logger, km: store.DefaultKeywordMapper}, nil
}

// Error is a Google API error.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"status"`
}

func (e *Error) Error() string { return fmt.Sprintf("gmail: %d %s: %s", e.Status, e.Reason, e.Message) }

func decodeError(status int, body []byte) error {
	var env struct {
		Error Error `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		env.Error.Status = status
		return &env.Error
	}
	return &Error{Status: status, Message: strings.TrimSpace(string(body))}
}

func isStatus(err error, s int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == s
}

const api = "/gmail/v1/users/me"

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps {
	return domain.MailCaps{Move: true, ObjectID: true, Thread: true}
}

// Close implements domain.MailProvider and domain.Transport.
func (p *Provider) Close() error { return nil }

// ==== labels ==================================================================

type label struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Type                  string `json:"type"`
	LabelListVisibility   string `json:"labelListVisibility,omitempty"`
	MessageListVisibility string `json:"messageListVisibility,omitempty"`
	MessagesTotal         int    `json:"messagesTotal"`
	MessagesUnread        int    `json:"messagesUnread"`
}

// systemFolders maps system labels shown as folders to roles.
var systemFolders = map[string]domain.FolderRole{"INBOX": domain.RoleInbox, "SENT": domain.RoleSent,
	"DRAFT": domain.RoleDrafts, "TRASH": domain.RoleTrash, "SPAM": domain.RoleJunk}

var systemNames = map[string]string{"INBOX": "Inbox", "SENT": "Sent", "DRAFT": "Drafts", "TRASH": "Trash", "SPAM": "Spam"}

func (p *Provider) loadLabels(ctx context.Context, force bool) (map[string]label, error) {
	p.mu.Lock()
	ls := p.labels
	p.mu.Unlock()
	if ls != nil && !force {
		return ls, nil
	}
	var resp struct {
		Labels []label `json:"labels"`
	}
	if err := p.rest.Get(ctx, api+"/labels", &resp); err != nil {
		return nil, err
	}
	ls = map[string]label{}
	for _, l := range resp.Labels {
		ls[l.ID] = l
	}
	p.mu.Lock()
	p.labels = ls
	p.mu.Unlock()
	return ls, nil
}

// ListFolders implements domain.MailProvider.
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	ls, err := p.loadLabels(ctx, true)
	if err != nil {
		return nil, err
	}
	byName := map[string]label{}
	for _, l := range ls {
		if l.Type == "user" {
			byName[l.Name] = l
		}
	}
	var out []domain.Folder
	for _, l := range ls {
		if role, ok := systemFolders[l.ID]; ok {
			out = append(out, domain.Folder{RemoteID: l.ID, Name: systemNames[l.ID], Role: role, Delimiter: "/", Subscribed: true,
				Total: l.MessagesTotal, Unread: l.MessagesUnread})
			continue
		}
		if l.Type != "user" {
			continue
		}
		out = append(out, domain.Folder{RemoteID: labelPath(l, byName), Name: lastName(l.Name), Delimiter: "/",
			Subscribed: l.LabelListVisibility != "labelHide", Total: l.MessagesTotal, Unread: l.MessagesUnread})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemoteID < out[j].RemoteID })
	return out, nil
}

func lastName(n string) string {
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		return n[i+1:]
	}
	return n
}

// labelPath returns the id path of a user label along existing parents.
func labelPath(l label, byName map[string]label) string {
	parts := strings.Split(l.Name, "/")
	var ids []string
	for i := 1; i < len(parts); i++ {
		if pl, ok := byName[strings.Join(parts[:i], "/")]; ok {
			ids = append(ids, pl.ID)
		}
	}
	return strings.Join(append(ids, l.ID), "/")
}

func lastSeg(remote string) string {
	if i := strings.LastIndexByte(remote, '/'); i >= 0 {
		return remote[i+1:]
	}
	return remote
}

// CreateFolder implements domain.MailProvider (a nested user label).
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	full := name
	if parentRemoteID != "" {
		ls, err := p.loadLabels(ctx, false)
		if err != nil {
			return domain.Folder{}, err
		}
		if pl, ok := ls[lastSeg(parentRemoteID)]; ok && pl.Type == "user" {
			full = pl.Name + "/" + name
		}
	}
	l, err := p.createLabel(ctx, full)
	if err != nil {
		return domain.Folder{}, err
	}
	remote := l.ID
	if parentRemoteID != "" {
		remote = parentRemoteID + "/" + l.ID
	}
	return domain.Folder{RemoteID: remote, Name: name, Delimiter: "/", Subscribed: true}, nil
}

func (p *Provider) createLabel(ctx context.Context, name string) (label, error) {
	var l label
	err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/labels",
		JSON: map[string]string{"name": name, "labelListVisibility": "labelShow", "messageListVisibility": "show"}}, &l)
	if err == nil {
		p.mu.Lock()
		if p.labels != nil {
			p.labels[l.ID] = l
		}
		p.mu.Unlock()
	}
	return l, err
}

// RenameFolder implements domain.MailProvider.
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	ls, err := p.loadLabels(ctx, false)
	if err != nil {
		return domain.Folder{}, err
	}
	l, ok := ls[lastSeg(remoteID)]
	if !ok || l.Type != "user" {
		return domain.Folder{}, fmt.Errorf("gmail: label %q cannot be renamed", remoteID)
	}
	full := newName
	if i := strings.LastIndexByte(l.Name, '/'); i >= 0 {
		full = l.Name[:i+1] + newName
	}
	var out label
	if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPatch, Path: api + "/labels/" + url.PathEscape(l.ID),
		JSON: map[string]string{"name": full}}, &out); err != nil {
		return domain.Folder{}, err
	}
	p.mu.Lock()
	p.labels = nil
	p.mu.Unlock()
	return domain.Folder{RemoteID: remoteID, Name: newName, Delimiter: "/", Subscribed: true}, nil
}

// DeleteFolder implements domain.MailProvider (messages keep their other labels).
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	err := p.rest.Do(ctx, rest.Request{Method: http.MethodDelete, Path: api + "/labels/" + url.PathEscape(lastSeg(remoteID))}, nil)
	if isStatus(err, http.StatusNotFound) {
		return nil
	}
	p.mu.Lock()
	p.labels = nil
	p.mu.Unlock()
	return err
}

// labelID returns the id of the user label for a tag path (created on demand).
func (p *Provider) labelID(ctx context.Context, name string, create bool) (string, error) {
	ls, err := p.loadLabels(ctx, false)
	if err != nil {
		return "", err
	}
	for _, l := range ls {
		if l.Type == "user" && strings.EqualFold(l.Name, name) {
			return l.ID, nil
		}
	}
	if !create {
		return "", nil
	}
	l, err := p.createLabel(ctx, name)
	return l.ID, err
}

// ==== messages ================================================================

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type message struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	InternalDate string   `json:"internalDate"`
	SizeEstimate int64    `json:"sizeEstimate"`
	Raw          string   `json:"raw,omitempty"`
	HistoryID    string   `json:"historyId"`
	Payload      *struct {
		MimeType string   `json:"mimeType"`
		Headers  []header `json:"headers"`
	} `json:"payload,omitempty"`
}

func (m message) has(l string) bool {
	for _, x := range m.LabelIDs {
		if x == l {
			return true
		}
	}
	return false
}

func (m message) header(name string) string {
	if m.Payload == nil {
		return ""
	}
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func parseAddrs(v string) []domain.Address {
	if v == "" {
		return nil
	}
	list, err := mimehdr.ParseAddressList(v)
	if err != nil {
		return nil
	}
	return list
}

func msgIDs(v string) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		if f = strings.Trim(f, "<>,"); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (p *Provider) flags(m message) []domain.Flag {
	var fl []domain.Flag
	if !m.has("UNREAD") {
		fl = append(fl, domain.FlagSeen)
	}
	if m.has("STARRED") {
		fl = append(fl, domain.FlagFlagged)
	}
	if m.has("DRAFT") {
		fl = append(fl, domain.FlagDraft)
	}
	p.mu.Lock()
	ls := p.labels
	p.mu.Unlock()
	for _, id := range m.LabelIDs {
		l, ok := ls[id]
		if !ok || l.Type != "user" {
			continue
		}
		if path, ok := store.TagFromGmailLabel(l.Name); ok {
			if kw, err := p.km.ToKeyword(path); err == nil {
				fl = append(fl, domain.Flag(kw))
			}
		}
	}
	return fl
}

func (p *Provider) remote(folder string, m message) domain.RemoteMessage {
	h := domain.MessageHeaderInfo{
		MessageID: strings.Trim(m.header("Message-ID"), "<> "), InReplyTo: msgIDs(m.header("In-Reply-To")),
		References: msgIDs(m.header("References")), Subject: mimehdr.DecodeWords(m.header("Subject")),
		From: parseAddrs(m.header("From")), To: parseAddrs(m.header("To")), Cc: parseAddrs(m.header("Cc")),
		ReplyTo: parseAddrs(m.header("Reply-To")), ServerObjectID: m.ID, ServerThreadID: m.ThreadID,
	}
	var received time.Time
	if ms, err := strconv.ParseInt(m.InternalDate, 10, 64); err == nil {
		received = time.UnixMilli(ms).UTC()
	}
	if t, err := mimehdr.ParseDate(m.header("Date")); err == nil {
		h.Date = t
	} else {
		h.Date = received
	}
	hasAtt := m.Payload != nil && (m.Payload.MimeType == "multipart/mixed")
	return domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folder, ID: m.ID}, Header: h, Flags: p.flags(m),
		Size: m.SizeEstimate, ReceivedAt: received, HasAttachments: hasAtt}
}

const metaQuery = "?format=metadata&metadataHeaders=From&metadataHeaders=To&metadataHeaders=Cc&metadataHeaders=Subject" +
	"&metadataHeaders=Date&metadataHeaders=Message-ID&metadataHeaders=In-Reply-To&metadataHeaders=References&metadataHeaders=Reply-To"

// metadata fetches the metadata of ids concurrently; missing messages are skipped.
func (p *Provider) metadata(ctx context.Context, ids []string, format string) ([]message, error) {
	out := make([]message, len(ids))
	ok := make([]bool, len(ids))
	sem := make(chan struct{}, p.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			var m message
			err := p.rest.Get(ctx, api+"/messages/"+url.PathEscape(id)+format, &m)
			if isStatus(err, http.StatusNotFound) {
				return
			}
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			out[i], ok[i] = m, true
		}(i, id)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	res := out[:0]
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res, nil
}

type syncState struct {
	History string `json:"h"`
}

func (p *Provider) profileHistory(ctx context.Context) (string, error) {
	var prof struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    string `json:"historyId"`
	}
	err := p.rest.Get(ctx, api+"/profile", &prof)
	return prof.HistoryID, err
}

// Sync implements domain.MailProvider: a full listing of the label on the
// first call, then the history API from the stored historyId. An expired
// historyId (404) restarts with a full resync.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	lid := lastSeg(folderRemoteID)
	if _, err := p.loadLabels(ctx, false); err != nil {
		return domain.SyncDelta{}, state, err
	}
	var st syncState
	if state != "" {
		_ = json.Unmarshal([]byte(state), &st)
	}
	if st.History != "" {
		d, next, err := p.history(ctx, folderRemoteID, lid, st.History)
		if err == nil {
			b, _ := json.Marshal(syncState{History: next})
			return d, domain.SyncState(b), nil
		}
		if !isStatus(err, http.StatusNotFound) {
			return domain.SyncDelta{}, state, err
		}
		p.log.Info("gmail: history expired, full resync", "label", lid)
	}
	hist, err := p.profileHistory(ctx)
	if err != nil {
		return domain.SyncDelta{}, state, err
	}
	var ids []string
	q := url.Values{"labelIds": {lid}, "maxResults": {"500"}}
	if lid == "SPAM" || lid == "TRASH" {
		q.Set("includeSpamTrash", "true")
	}
	for {
		var resp struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := p.rest.Get(ctx, api+"/messages?"+q.Encode(), &resp); err != nil {
			return domain.SyncDelta{}, state, err
		}
		for _, m := range resp.Messages {
			ids = append(ids, m.ID)
		}
		if resp.NextPageToken == "" {
			break
		}
		q.Set("pageToken", resp.NextPageToken)
	}
	ms, err := p.metadata(ctx, ids, metaQuery)
	if err != nil {
		return domain.SyncDelta{}, state, err
	}
	d := domain.SyncDelta{FullResync: st.History != ""}
	for _, m := range ms {
		d.Added = append(d.Added, p.remote(folderRemoteID, m))
	}
	b, _ := json.Marshal(syncState{History: hist})
	return d, domain.SyncState(b), nil
}

type historyRecord struct {
	ID            string `json:"id"`
	MessagesAdded []struct {
		Message message `json:"message"`
	} `json:"messagesAdded"`
	MessagesDeleted []struct {
		Message message `json:"message"`
	} `json:"messagesDeleted"`
	LabelsAdded []struct {
		Message  message  `json:"message"`
		LabelIDs []string `json:"labelIds"`
	} `json:"labelsAdded"`
	LabelsRemoved []struct {
		Message  message  `json:"message"`
		LabelIDs []string `json:"labelIds"`
	} `json:"labelsRemoved"`
}

func contains(ls []string, l string) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}

func (p *Provider) history(ctx context.Context, folder, lid, start string) (domain.SyncDelta, string, error) {
	const (
		stAdded = iota + 1
		stRemoved
		stChanged
	)
	status := map[string]int{}
	var order []string
	set := func(id string, s int) {
		if _, ok := status[id]; !ok {
			order = append(order, id)
		}
		switch {
		case s == stChanged && status[id] != 0:
			return // keep added/removed
		}
		status[id] = s
	}
	q := url.Values{"startHistoryId": {start}, "maxResults": {"500"},
		"historyTypes": {"messageAdded", "messageDeleted", "labelAdded", "labelRemoved"}}
	next := start
	for {
		var resp struct {
			History       []historyRecord `json:"history"`
			HistoryID     string          `json:"historyId"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err := p.rest.Get(ctx, api+"/history?"+q.Encode(), &resp); err != nil {
			return domain.SyncDelta{}, start, err
		}
		for _, h := range resp.History {
			for _, a := range h.MessagesAdded {
				if a.Message.has(lid) {
					set(a.Message.ID, stAdded)
				}
			}
			for _, a := range h.LabelsAdded {
				if contains(a.LabelIDs, lid) {
					set(a.Message.ID, stAdded)
				} else if a.Message.has(lid) {
					set(a.Message.ID, stChanged)
				}
			}
			for _, a := range h.LabelsRemoved {
				if contains(a.LabelIDs, lid) {
					set(a.Message.ID, stRemoved)
				} else if a.Message.has(lid) {
					set(a.Message.ID, stChanged)
				}
			}
			for _, a := range h.MessagesDeleted {
				set(a.Message.ID, stRemoved)
			}
		}
		if resp.HistoryID != "" {
			next = resp.HistoryID
		}
		if resp.NextPageToken == "" {
			break
		}
		q.Set("pageToken", resp.NextPageToken)
	}
	var d domain.SyncDelta
	var added, changed []string
	for _, id := range order {
		switch status[id] {
		case stAdded:
			added = append(added, id)
		case stChanged:
			changed = append(changed, id)
		case stRemoved:
			d.Removed = append(d.Removed, domain.RemoteRef{FolderRemoteID: folder, ID: id})
		}
	}
	ms, err := p.metadata(ctx, added, metaQuery)
	if err != nil {
		return d, start, err
	}
	for _, m := range ms {
		if m.has(lid) {
			d.Added = append(d.Added, p.remote(folder, m))
		}
	}
	cs, err := p.metadata(ctx, changed, "?format=minimal")
	if err != nil {
		return d, start, err
	}
	for _, m := range cs {
		d.FlagsChanged = append(d.FlagsChanged, domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folder, ID: m.ID},
			Flags: p.flags(m)})
	}
	return d, next, nil
}

// FetchRaw implements domain.MailProvider (format=raw).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	var m message
	if err := p.rest.Do(ctx, rest.Request{Method: http.MethodGet, Path: api + "/messages/" + url.PathEscape(ref.ID) + "?format=raw",
		MaxBytes: maxRawResponse}, &m); err != nil {
		return nil, err
	}
	raw, err := base64.URLEncoding.DecodeString(padB64(m.Raw))
	if err != nil {
		return nil, fmt.Errorf("gmail: decode raw message: %w", err)
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}

func padB64(s string) string {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return s
}

// modify adds/removes labels on messages (batchModify, 1000 ids per call).
func (p *Provider) modify(ctx context.Context, ids, add, remove []string) error {
	if len(ids) == 0 || len(add)+len(remove) == 0 {
		return nil
	}
	for i := 0; i < len(ids); i += 1000 {
		body := map[string]any{"ids": ids[i:min(i+1000, len(ids))]}
		if len(add) > 0 {
			body["addLabelIds"] = add
		}
		if len(remove) > 0 {
			body["removeLabelIds"] = remove
		}
		if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages/batchModify", JSON: body}, nil); err != nil {
			return err
		}
	}
	return nil
}

func refIDs(refs []domain.RemoteRef) []string {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	return ids
}

// Move implements domain.MailProvider with label semantics: the source
// label is removed and the target added (TRASH uses trash/untrash).
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	target := lastSeg(to)
	out := make([]domain.RemoteRef, len(refs))
	bySource := map[string][]string{}
	for i, r := range refs {
		out[i] = domain.RemoteRef{FolderRemoteID: to, ID: r.ID}
		bySource[lastSeg(r.FolderRemoteID)] = append(bySource[lastSeg(r.FolderRemoteID)], r.ID)
	}
	for src, ids := range bySource {
		if src == target {
			continue
		}
		switch {
		case target == "TRASH":
			for _, id := range ids {
				if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages/" + url.PathEscape(id) + "/trash"}, nil); err != nil && !isStatus(err, 404) {
					return nil, err
				}
			}
			continue
		case src == "TRASH":
			for _, id := range ids {
				if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages/" + url.PathEscape(id) + "/untrash"}, nil); err != nil && !isStatus(err, 404) {
					return nil, err
				}
			}
		}
		var remove []string
		if src != "TRASH" && src != "SENT" && src != "DRAFT" {
			remove = []string{src}
		}
		if err := p.modify(ctx, ids, []string{target}, remove); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SetFlags implements domain.MailProvider: \Seen ↔ UNREAD, \Flagged ↔
// STARRED, keywords ↔ user labels (created on demand).
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	var addL, remL []string
	conv := func(f domain.Flag, adding bool) error {
		switch f {
		case domain.FlagSeen:
			if adding {
				remL = append(remL, "UNREAD")
			} else {
				addL = append(addL, "UNREAD")
			}
		case domain.FlagFlagged:
			if adding {
				addL = append(addL, "STARRED")
			} else {
				remL = append(remL, "STARRED")
			}
		case domain.FlagAnswered, domain.FlagForwarded, domain.FlagDraft, domain.FlagDeleted:
		default:
			path, ok := p.km.FromKeyword(string(f))
			if !ok {
				return nil
			}
			name, err := store.TagToGmailLabel(path)
			if err != nil {
				return nil
			}
			id, err := p.labelID(ctx, name, adding)
			if err != nil || id == "" {
				return err
			}
			if adding {
				addL = append(addL, id)
			} else {
				remL = append(remL, id)
			}
		}
		return nil
	}
	for _, f := range add {
		if err := conv(f, true); err != nil {
			return err
		}
	}
	for _, f := range remove {
		if err := conv(f, false); err != nil {
			return err
		}
	}
	return p.modify(ctx, refIDs(refs), addL, remL)
}

// Delete implements domain.MailProvider (permanent, batchDelete).
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	if len(refs) == 0 {
		return nil
	}
	return p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages/batchDelete",
		JSON: map[string]any{"ids": refIDs(refs)}}, nil)
}

// Trash moves messages to the trash.
func (p *Provider) Trash(ctx context.Context, refs []domain.RemoteRef) error {
	_, err := p.Move(ctx, refs, "TRASH")
	return err
}

// Append implements domain.MailProvider (messages.insert; drafts.create
// for the Drafts folder).
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	data, err := io.ReadAll(raw)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	enc := base64.URLEncoding.EncodeToString(data)
	lid := lastSeg(folderRemoteID)
	if lid == "DRAFT" {
		var d struct {
			Message message `json:"message"`
		}
		if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/drafts",
			JSON: map[string]any{"message": map[string]string{"raw": enc}}}, &d); err != nil {
			return domain.RemoteRef{}, err
		}
		return domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: d.Message.ID}, nil
	}
	labels := []string{lid}
	seen := false
	for _, f := range flags {
		switch f {
		case domain.FlagSeen:
			seen = true
		case domain.FlagFlagged:
			labels = append(labels, "STARRED")
		}
	}
	if !seen {
		labels = append(labels, "UNREAD")
	}
	var m message
	if err := p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages?internalDateSource=dateHeader",
		JSON: map[string]any{"raw": enc, "labelIds": labels}}, &m); err != nil {
		return domain.RemoteRef{}, err
	}
	return domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: m.ID}, nil
}

// Watch implements domain.MailProvider by polling the mailbox historyId
// (Pub/Sub push is not used).
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	ch := make(chan domain.ChangeHint, 1)
	go func() {
		defer close(ch)
		t := time.NewTicker(p.cfg.PollInterval)
		defer t.Stop()
		last := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h, err := p.profileHistory(ctx)
				if err != nil {
					continue
				}
				if last != "" && h != last {
					select {
					case ch <- domain.ChangeHint{FolderRemoteID: folderRemoteID}:
					default:
					}
				}
				last = h
			}
		}
	}()
	return ch, nil
}

// Send implements domain.Transport (messages.send; Gmail stores the
// message in Sent).
func (p *Provider) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	raw := msg.Raw
	if bcc := mimehdr.MissingRecipients(raw, msg.Recipients); len(bcc) > 0 {
		raw = mimehdr.Ensure(raw, "Bcc", strings.Join(bcc, ", "))
	}
	return p.rest.Do(ctx, rest.Request{Method: http.MethodPost, Path: api + "/messages/send",
		JSON: map[string]string{"raw": base64.URLEncoding.EncodeToString(raw)}}, nil)
}

// ==== vacation responder ======================================================

// Supported implements vacation.ServerVacationSync.
func (p *Provider) Supported() bool { return true }

// Push implements vacation.ServerVacationSync (settings/vacation).
func (p *Provider) Push(ctx context.Context, s vacation.Settings) error {
	text := s.ExternalText
	if text == "" {
		text = s.InternalText
	}
	subject := s.Subject
	if strings.Contains(subject, "{{") || subject == "" {
		subject = "Abwesenheitsnotiz"
	}
	body := map[string]any{"enableAutoReply": s.Enabled, "responseSubject": subject, "responseBodyPlainText": text,
		"restrictToContacts": false, "restrictToDomain": false}
	if !s.Start.IsZero() {
		body["startTime"] = strconv.FormatInt(s.Start.UnixMilli(), 10)
	}
	if !s.End.IsZero() {
		body["endTime"] = strconv.FormatInt(s.End.UnixMilli(), 10)
	}
	return p.rest.Do(ctx, rest.Request{Method: http.MethodPut, Path: api + "/settings/vacation", JSON: body}, nil)
}

// SendAs is a send-as alias configured in Gmail.
type SendAs struct {
	Email     string `json:"sendAsEmail"`
	Name      string `json:"displayName"`
	IsPrimary bool   `json:"isPrimary"`
	IsDefault bool   `json:"isDefault"`
	Status    string `json:"verificationStatus"`
}

// ListSendAs returns the configured sender aliases (identities).
func (p *Provider) ListSendAs(ctx context.Context) ([]SendAs, error) {
	var resp struct {
		SendAs []SendAs `json:"sendAs"`
	}
	err := p.rest.Get(ctx, api+"/settings/sendAs", &resp)
	return resp.SendAs, err
}
