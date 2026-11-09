// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// MessageScope is what the classic message list shows (api.ts MessageScope).
type MessageScope struct {
	Kind           string `json:"kind"` // folder | unified | tag | searchFolder
	FolderID       int64  `json:"folderId,omitempty"`
	Role           string `json:"role,omitempty"`
	TagID          int64  `json:"tagId,omitempty"`
	SearchFolderID int64  `json:"searchFolderId,omitempty"`
}

// MessageQuery is a page request of the classic list.
type MessageQuery struct {
	Scope  MessageScope `json:"scope"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
	Search string       `json:"search,omitempty"`
}

const maxScopeIDs = 5000

// messageFilter translates a message scope into a store filter.
func (a *App) messageFilter(ctx context.Context, sc MessageScope) (store.MessageFilter, error) {
	var f store.MessageFilter
	switch sc.Kind {
	case "all": // every mail of the user (terminal search)
	case "folder":
		if _, err := a.st.GetFolder(ctx, sc.FolderID); err != nil {
			return f, err
		}
		f.FolderIDs = []int64{sc.FolderID}
	case "unified":
		switch sc.Role {
		case "inbox", "sent", "drafts", "archive", "trash", "junk":
			f.Roles = []domain.FolderRole{domain.FolderRole(sc.Role)}
		case "flagged":
			f.FlagsSet = store.FlagFlagged
		case "unread":
			f.Roles = []domain.FolderRole{domain.RoleInbox}
			f.FlagsClear = store.FlagSeen
		default:
			return f, invalid("unknown unified folder %q", sc.Role)
		}
	case "tag":
		tags, err := a.st.TagSubtreeIDs(ctx, sc.TagID)
		if err != nil {
			return f, err
		}
		if f.IDs, err = a.st.TaggedItemIDs(ctx, store.ItemMessage, tags); err != nil {
			return f, err
		}
		convs, err := a.st.TaggedItemIDs(ctx, store.ItemConversation, tags)
		if err != nil {
			return f, err
		}
		for _, c := range convs {
			ids, err := a.st.MessageIDsOfConversation(ctx, c)
			if err != nil {
				return f, err
			}
			f.IDs = append(f.IDs, ids...)
		}
	case "searchFolder":
		ids, err := a.searchFolderItems(ctx, sc.SearchFolderID, store.ScopeMail)
		if err != nil {
			return f, err
		}
		f.IDs = ids
	default:
		return f, invalid("unknown message scope %q", sc.Kind)
	}
	return f, nil
}

// searchFolderItems returns the ids of the items of one kind in a search folder.
func (a *App) searchFolderItems(ctx context.Context, id int64, scope store.SearchScope) ([]int64, error) {
	res, err := a.st.RunSearchFolder(ctx, id, store.SearchRequest{Limit: maxScopeIDs, Scopes: []store.SearchScope{scope}, Sort: store.SortDateDesc})
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, h := range res.Hits {
		if h.Kind == scope {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

// searchIDs runs a plain-text search and returns matching item ids of a kind.
func (a *App) searchIDs(ctx context.Context, text string, scope store.SearchScope) ([]int64, error) {
	res, err := a.st.SearchAdvanced(ctx, store.SearchRequest{Query: text, Simple: true, Scopes: []store.SearchScope{scope},
		Limit: maxScopeIDs, Sort: store.SortDateDesc, Location: a.opts.Location})
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, h := range res.Hits {
		if h.Kind == scope {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

func intersect(a, b []int64) []int64 {
	if a == nil {
		return b
	}
	set := make(map[int64]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	out := []int64{}
	for _, x := range a {
		if set[x] {
			out = append(out, x)
		}
	}
	return out
}

// ListMessages returns one page of the classic message list.
func (a *App) ListMessages(ctx context.Context, q MessageQuery) (Page[MessageSummary], error) {
	f, err := a.messageFilter(ctx, q.Scope)
	if err != nil {
		return Page[MessageSummary]{}, err
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		ids, err := a.searchIDs(ctx, s, store.ScopeMail)
		if err != nil {
			return Page[MessageSummary]{}, err
		}
		f.IDs = intersect(f.IDs, ids)
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	msgs, total, err := a.st.ListFilteredMessages(ctx, f, limit, q.Offset)
	if err != nil {
		return Page[MessageSummary]{}, err
	}
	items, err := a.summaries(ctx, msgs)
	if err != nil {
		return Page[MessageSummary]{}, err
	}
	return Page[MessageSummary]{Items: items, Total: total, Offset: max(q.Offset, 0)}, nil
}

func addrList(as []store.MessageAddress, role string) []Address {
	out := []Address{}
	for _, x := range as {
		if x.Role == role {
			out = append(out, Address{Name: x.Name, Email: x.Email})
		}
	}
	return out
}

func tagIDs(ts []store.Tag) []int64 {
	if len(ts) == 0 {
		return nil
	}
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// summaries hydrates list rows (addresses, folders, tags, projects).
func (a *App) summaries(ctx context.Context, msgs []store.Message) ([]MessageSummary, error) {
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	addrs, err := a.st.AddressesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	folders, err := a.st.FoldersFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	tags, err := a.st.TagsForItems(ctx, store.ItemMessage, ids)
	if err != nil {
		return nil, err
	}
	projects, err := a.st.ProjectsForItems(ctx, store.ItemMessage, ids)
	if err != nil {
		return nil, err
	}
	out := make([]MessageSummary, 0, len(msgs))
	for _, m := range msgs {
		s := MessageSummary{ID: m.ID, AccountID: m.AccountID, FolderIDs: nonNilIDs(folders[m.ID]), ConversationID: m.ConversationID,
			MessageID: m.MessageIDHdr, Subject: m.Subject, From: addrList(addrs[m.ID], store.RoleFrom), To: addrList(addrs[m.ID], store.RoleTo),
			Cc: addrList(addrs[m.ID], store.RoleCc), Date: rfc3339(msgTime(m)), Preview: m.Snippet, Flags: m.Flags.Domain(),
			HasAttachments: m.HasAttachments, Size: m.Size, TagIDs: tagIDs(tags[m.ID]), ProjectIDs: projects[m.ID], HeadersOnly: m.BlobHash == ""}
		if len(s.From) == 0 && m.From != "" {
			s.From = []Address{{Email: m.From}}
		}
		if s.Flags == nil {
			s.Flags = []Flag{}
		}
		out = append(out, s)
	}
	return out, nil
}

// rawMessage loads the raw RFC 5322 message from the blob store.
func (a *App) rawMessage(m store.Message) ([]byte, error) {
	if m.BlobHash == "" || a.blobs == nil {
		if f, ok := a.sync.(bodyFetcher); ok && m.BlobHash == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return f.FetchBody(ctx, m.ID)
		}
		return nil, errNotFound
	}
	r, err := a.blobs.Open(m.BlobHash)
	if err != nil {
		// Released in the meantime (mails kept with header only): load it again.
		if f, ok := a.sync.(bodyFetcher); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if raw, ferr := f.FetchBody(ctx, m.ID); ferr == nil {
				return raw, nil
			}
		}
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, maxRenderBytes))
}

func attachmentURL(messageID int64, index int) string {
	return fmt.Sprintf("/api/messages/%d/attachments/%d", messageID, index)
}

// GetMessage returns a message with its (sanitized) body.
func (a *App) GetMessage(ctx context.Context, id int64) (MessageDetail, error) {
	m, err := a.st.GetMessage(ctx, id)
	if err != nil {
		return MessageDetail{}, err
	}
	if m.ConversationID == 0 && !a.opts.ReadOnly {
		if m.ConversationID, err = a.ensureThread(ctx, m); err != nil {
			return MessageDetail{}, err
		}
	}
	sums, err := a.summaries(ctx, []store.Message{m})
	if err != nil {
		return MessageDetail{}, err
	}
	d := MessageDetail{MessageSummary: sums[0], InReplyTo: []string{}, References: []string{}, Attachments: []Attachment{}}
	addrs, err := a.st.MessageAddresses(ctx, id)
	if err != nil {
		return MessageDetail{}, err
	}
	if bcc := addrList(addrs, store.RoleBcc); len(bcc) > 0 {
		d.Bcc = bcc
	}
	if rt := addrList(addrs, store.RoleReplyTo); len(rt) > 0 {
		d.ReplyTo = rt
	}
	r, rerr := a.renderCached(ctx, m)
	if errors.Is(rerr, errNoBody) {
		// Kept with header only and the content could not be loaded now
		// (no connection, server unreachable): header and preview.
		d.ContentUnavailable = true
	} else {
		d.HeadersOnly = false // the content is here now
	}
	if !errors.Is(rerr, errNoBody) {
		if rerr == nil {
			d.Text, d.HTML, d.HasRemoteContent = r.Text, r.HTML, r.HasRemoteContent
			a.autoRemote(ctx, &d, m, nil) // mailproto.go: allowlisted senders (raw loaded there)
			if r.InReplyTo != nil {
				d.InReplyTo = r.InReplyTo
			}
			if r.References != nil {
				d.References = r.References
			}
			for _, at := range r.Attachments {
				att := Attachment{ID: int64(at.Index), Filename: at.Filename, MIMEType: at.MIMEType, Size: at.Size,
					ContentID: at.ContentID, Inline: at.Inline, URL: attachmentURL(id, at.Index)}
				if strings.HasPrefix(at.MIMEType, "image/") {
					att.PreviewURL = att.URL
				}
				d.Attachments = append(d.Attachments, att)
			}
		} else {
			a.log.Warn("app: rendering message failed", "message", id, "err", rerr)
		}
	}
	if d.Text == "" && d.HTML == "" {
		d.Text = m.Snippet
	}
	if len(d.Attachments) == 0 {
		rows, err := a.st.AttachmentsOf(ctx, id)
		if err != nil {
			return MessageDetail{}, err
		}
		for i, r := range rows {
			d.Attachments = append(d.Attachments, Attachment{ID: int64(i), Filename: r.Filename, MIMEType: r.MIMEType, Size: r.Size, ContentID: r.ContentID})
		}
	}
	return d, nil
}

// AttachmentContent is a downloadable attachment.
type AttachmentContent struct {
	Filename string
	MIMEType string
	Data     []byte
}

// MessageAttachment returns the content of an attachment by index.
func (a *App) MessageAttachment(ctx context.Context, messageID int64, index int) (AttachmentContent, error) {
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return AttachmentContent{}, err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return AttachmentContent{}, err
	}
	at, data, err := a.render.Attachment(ctx, raw, index)
	if err != nil {
		return AttachmentContent{}, err
	}
	return AttachmentContent{Filename: at.Filename, MIMEType: at.MIMEType, Data: data}, nil
}

// MessageCID resolves a cid: reference of an HTML body to its inline part.
func (a *App) MessageCID(ctx context.Context, messageID int64, cid string) (AttachmentContent, error) {
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return AttachmentContent{}, err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return AttachmentContent{}, err
	}
	r, err := a.render.Render(WithMessageID(ctx, messageID), raw)
	if err != nil {
		return AttachmentContent{}, err
	}
	cid = strings.Trim(cid, "<> ")
	for _, at := range r.Attachments {
		if strings.EqualFold(at.ContentID, cid) {
			return a.MessageAttachment(ctx, messageID, at.Index)
		}
	}
	return AttachmentContent{}, errNotFound
}

// ConversationForMessage maps a mail to its conversation for a grouping.
func (a *App) ConversationForMessage(ctx context.Context, messageID int64, grouping string) (ConversationRef, error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return ConversationRef{}, err
	}
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return ConversationRef{}, err
	}
	ref := ConversationRef{ItemID: mailItemID(messageID)}
	if grouping == "person" {
		pid, _, err := a.st.MessageCounterpart(ctx, messageID)
		if err != nil {
			return ref, err
		}
		if pid != 0 {
			p, err := a.st.GetPerson(ctx, pid)
			if err != nil {
				return ref, err
			}
			ref.ConversationID, err = a.st.PersonConversation(ctx, pid, p.DisplayName)
			return ref, err
		}
	}
	ref.ConversationID, err = a.ensureThread(ctx, m)
	return ref, err
}

// Move moves mails to a folder.
func (a *App) Move(ctx context.Context, ids []int64, folderID int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := checkBulk(len(ids)); err != nil {
		return err
	}
	before, _ := a.st.FoldersFor(ctx, ids)
	if err := a.sync.Move(ctx, ids, folderID); err != nil {
		return err
	}
	a.afterMailChange(ctx, ids, before, folderID)
	return nil
}

// SetFlags adds and removes flags of mails.
func (a *App) SetFlags(ctx context.Context, ids []int64, add, remove []Flag) error {
	if len(ids) == 0 {
		return nil
	}
	if err := checkBulk(len(ids)); err != nil {
		return err
	}
	for _, f := range slices.Concat(add, remove) {
		if store.FlagsFromDomain([]domain.Flag{f}) == 0 {
			return invalid("unknown flag %q", f)
		}
	}
	if err := a.sync.SetFlags(ctx, ids, add, remove); err != nil {
		return err
	}
	before, _ := a.st.FoldersFor(ctx, ids)
	a.afterMailChange(ctx, ids, before)
	return nil
}

// afterMailChange publishes the UI events after mails changed.
func (a *App) afterMailChange(ctx context.Context, ids []int64, before map[int64][]int64, extraFolders ...int64) {
	folders := map[int64]bool{}
	for _, f := range extraFolders {
		folders[f] = true
	}
	for _, fs := range before {
		for _, f := range fs {
			folders[f] = true
		}
	}
	convs := map[int64]bool{}
	for _, id := range ids {
		a.hub.Publish(Event{Type: EventMessageUpdated, MessageID: id})
		if m, err := a.st.GetMessage(ctx, id); err == nil && m.ConversationID != 0 {
			convs[m.ConversationID] = true
		}
	}
	for f := range folders {
		a.publishFolder(ctx, f)
	}
	for c := range convs {
		a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: c})
	}
}

// statusOf maps an error to an HTTP status (used by the server).
func statusOf(err error) int {
	if e := AsError(err); e != nil && e.Status != 0 {
		return e.Status
	}
	return http.StatusInternalServerError
}

// PendingMailOps counts the queued operations (flags, moves, sends) that have
// not reached the servers yet. One-shot commands wait for them before exiting.
func (a *App) PendingMailOps(ctx context.Context) (int, error) {
	n := 0
	for _, state := range []store.OpState{store.OpPending, store.OpRunning} {
		ops, err := a.st.ListOps(ctx, state)
		if err != nil {
			return 0, err
		}
		n += len(ops)
	}
	return n, nil
}
