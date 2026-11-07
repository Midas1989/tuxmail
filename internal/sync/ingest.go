// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/internal/sync/content"
)

// placeholderPrefix marks locations created locally by a pending operation
// (the server reference is not known yet). It cannot clash with IMAP UIDs,
// EAS ServerIds or Graph ids.
const placeholderPrefix = "~pending:"

func isPlaceholder(ref string) bool { return strings.HasPrefix(ref, placeholderPrefix) }

// batch collects the effects of a part of a folder sync; they are
// published together.
type batch struct {
	convs    map[int64]bool
	received []events.Event
	changed  int
}

func newBatch() *batch { return &batch{convs: map[int64]bool{}} }

func (b *batch) conv(ids ...int64) {
	for _, id := range ids {
		if id != 0 {
			b.convs[id] = true
		}
	}
}

// syncFolder applies one incremental sync of a folder. Providers may return
// large changes in chunks (SyncDelta.More, newest mails first); every chunk
// is committed, its state stored and its events published before the next
// one is requested, so that the UI fills from the top while a large folder
// is still syncing. Progress is reported in the account status.
func (w *Worker) syncFolder(ctx context.Context, f domain.Folder) error {
	for {
		more, err := w.syncFolderChunk(ctx, f)
		if err != nil || !more {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Keep user operations responsive during long initial syncs.
		if err := w.runOps(ctx); err != nil {
			return err
		}
	}
}

func (w *Worker) syncFolderChunk(ctx context.Context, f domain.Folder) (bool, error) {
	st := w.m.st
	state, err := st.FolderSyncState(ctx, f.ID)
	if err != nil {
		return false, err
	}
	// A folder whose initial sync is not complete yet keeps the marker
	// partialPrefix in front of the provider state (across restarts), so
	// that its older mails never count as newly received.
	partial := strings.HasPrefix(string(state), partialPrefix)
	state = domain.SyncState(strings.TrimPrefix(string(state), partialPrefix))
	initial := state == ""
	var (
		delta domain.SyncDelta
		next  domain.SyncState
	)
	if err := w.withProvider(ctx, func(p domain.MailProvider) error {
		var err error
		delta, next, err = p.Sync(ctx, f.RemoteID, state)
		return err
	}); err != nil {
		return false, fmt.Errorf("sync %q: %w", f.RemoteID, err)
	}
	b := newBatch()
	if delta.FullResync && !initial {
		n, err := st.RemoveAllLocations(ctx, f.ID)
		if err != nil {
			return false, err
		}
		w.log.Info("sync: full resync", "folder", f.RemoteID, "dropped_locations", n)
	}
	for _, r := range delta.Removed {
		if err := w.applyRemoved(ctx, f, r, b); err != nil {
			return false, err
		}
	}
	for _, rm := range delta.FlagsChanged {
		if err := w.applyFlags(ctx, f, rm, b); err != nil {
			return false, err
		}
	}
	added := delta.Added
	// Newest first so that the UI fills from the top.
	sort.SliceStable(added, func(i, j int) bool {
		ti, tj := added[i].ReceivedAt, added[j].ReceivedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return refNum(added[i].Ref.ID) > refNum(added[j].Ref.ID)
	})
	// A chunked sync stays "initial" (no mail.received) until the folder
	// has been synced completely once.
	quiet := initial || partial || delta.FullResync
	local := 0
	if delta.Total > 0 && len(added) > 0 {
		if fl, err := st.GetFolder(ctx, f.ID); err == nil && !initial {
			local = fl.Total
		}
		w.setProgress(f, local, delta.Total, quiet)
	}
	for i := 0; i < len(added); i += w.m.cfg.IngestBatch {
		if err := ctx.Err(); err != nil {
			return false, err // stop quickly; this chunk is fetched again next time
		}
		for _, rm := range added[i:min(i+w.m.cfg.IngestBatch, len(added))] {
			if err := w.ingest(ctx, f, rm, quiet, b); err != nil {
				if ctx.Err() != nil || IsNetworkError(err) {
					return false, err
				}
				w.log.Warn("sync: ingest failed", "folder", f.RemoteID, "ref", rm.Ref.ID, "err", err)
			}
		}
		n := b.changed
		w.flush(ctx, f, b)
		if delta.Total > 0 {
			local += n
			w.setProgress(f, min(local, delta.Total), delta.Total, quiet)
		}
	}
	if delta.More && quiet {
		next = partialPrefix + next
	}
	if err := st.SetFolderSyncState(ctx, f.ID, next); err != nil {
		return false, err
	}
	w.flush(ctx, f, b)
	return delta.More, nil
}

// partialPrefix marks the stored sync state of a folder whose chunked
// initial sync is still running.
const partialPrefix = "partial:"

func refNum(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// flush publishes the effects collected in b.
func (w *Worker) flush(ctx context.Context, f domain.Folder, b *batch) {
	m := w.m
	if b.changed == 0 && len(b.convs) == 0 && len(b.received) == 0 {
		return
	}
	if total, unread, err := m.st.RefreshFolderCounts(ctx, f.ID); err == nil {
		m.publishFolder(ctx, w.acc.ID, f.ID, total, unread)
	}
	ids := make([]int64, 0, len(b.convs))
	for id := range b.convs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if err := m.st.RefreshConversations(ctx, ids...); err != nil {
		w.log.Warn("sync: refresh conversations", "err", err)
	}
	m.publishConversations(ctx, ids...)
	for _, ev := range b.received {
		m.publish(ctx, ev)
	}
	*b = *newBatch()
}

func (w *Worker) applyRemoved(ctx context.Context, f domain.Folder, ref domain.RemoteRef, b *batch) error {
	st := w.m.st
	mid, err := st.RemoveLocation(ctx, f.ID, ref.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // already removed locally (own move/delete)
	}
	if err != nil {
		return err
	}
	b.changed++
	if msg, err := st.GetMessage(ctx, mid); err == nil {
		b.conv(msg.ConversationID)
		w.m.publishMessage(ctx, w.acc.ID, mid, msg.ConversationID, "removed")
	}
	return nil
}

func (w *Worker) applyFlags(ctx context.Context, f domain.Folder, rm domain.RemoteMessage, b *batch) error {
	st := w.m.st
	msg, err := st.FindMessageByLocation(ctx, f.ID, rm.Ref.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if w.isProtectedMsg(msg.ID) {
		return nil // a local change is on its way to the server
	}
	flags := store.FlagsFromDomain(rm.Flags)
	if flags != msg.Flags {
		if err := st.SetMessageFlags(ctx, msg.ID, flags); err != nil {
			return err
		}
		b.changed++
		b.conv(msg.ConversationID)
		w.m.publishMessage(ctx, w.acc.ID, msg.ID, msg.ConversationID, "flags")
		if flags&store.FlagFlagged != 0 && msg.Flags&store.FlagFlagged == 0 {
			msg.Flags = flags
			ev := w.m.mailEvent(ctx, events.MailFlagged, msg, f, nil)
			b.received = append(b.received, ev)
		}
	}
	return w.m.setProviderTags(ctx, msg.ID, rm.Flags, true)
}

// ingest stores one message reported as added in folder f. quiet suppresses
// mail.received (initial sync / backfill).
func (w *Worker) ingest(ctx context.Context, f domain.Folder, rm domain.RemoteMessage, quiet bool, b *batch) error {
	m, st := w.m, w.m.st
	h := rm.Header
	h.MessageID = threading.NormalizeMessageID(h.MessageID)

	var existing *store.Message
	if h.MessageID != "" {
		if e, err := st.FindMessageByMessageID(ctx, w.acc.ID, h.MessageID); err == nil {
			existing = &e
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	// Messages without Message-ID need their body for the fingerprint;
	// genuinely new mail is downloaded right away so that events and
	// workflows see the content. Everything else is backfilled.
	needRaw := h.MessageID == "" || (!quiet && (existing == nil || existing.BlobHash == ""))
	var (
		raw []byte
		ct  *content.Content
	)
	if needRaw && (rm.Size <= 0 || rm.Size <= m.cfg.MaxBodySize) {
		r, err := w.fetchRaw(ctx, rm.Ref)
		switch {
		case err == nil:
			raw = r
		case ctx.Err() != nil || IsNetworkError(err):
			return err
		default:
			w.log.Info("sync: fetch body failed", "ref", rm.Ref.ID, "err", err)
		}
	}
	if raw != nil {
		ct = m.extract(raw)
	}
	msg := store.Message{
		AccountID: w.acc.ID, MessageIDHdr: h.MessageID, Fingerprint: fingerprint(h, ct),
		ServerObjectID: h.ServerObjectID, ServerThreadID: h.ServerThreadID, Subject: h.Subject, From: firstEmail(h.From),
		SentAt: h.Date, ReceivedAt: rm.ReceivedAt, Size: rm.Size, HasAttachments: rm.HasAttachments,
		Flags: store.FlagsFromDomain(rm.Flags),
	}
	if existing != nil {
		msg.HasAttachments = msg.HasAttachments || existing.HasAttachments
	}
	id, created, err := st.UpsertMessage(ctx, msg)
	if err != nil {
		return err
	}
	if err := st.AddLocation(ctx, store.Location{MessageID: id, FolderID: f.ID, RemoteRef: rm.Ref.ID}); err != nil {
		return err
	}
	if created {
		if err := st.SetMessageAddresses(ctx, id, store.AddressesFromHeader(h)); err != nil {
			return err
		}
	}
	row, err := st.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	b.changed++
	if raw != nil {
		if row, err = m.storeContent(ctx, row, raw, ct); err != nil {
			return err
		}
	} else if created {
		// Kept with header only for now: a preview the server delivered with
		// the header data (no extra request) is stored right away.
		if p := m.previewOf(rm); p != "" && w.contentRule().release {
			if err := st.SetMessagePreview(ctx, id, p); err != nil {
				return err
			}
			row.Snippet = p
		}
		if err := m.indexMessage(ctx, row, nil); err != nil {
			return err
		}
	}
	if err := m.setProviderTags(ctx, id, rm.Flags, !created); err != nil {
		return err
	}
	if created || row.ConversationID == 0 {
		convs, err := m.threadMessage(ctx, row, h, ct)
		if err != nil {
			return err
		}
		b.conv(convs...)
		if row, err = st.GetMessage(ctx, id); err != nil {
			return err
		}
	} else if raw != nil && existing != nil && existing.BlobHash == "" {
		convs, err := m.contentRelations(ctx, row, ct)
		if err != nil {
			return err
		}
		b.conv(convs...)
	}
	b.conv(row.ConversationID)
	if created {
		if m.cfg.Comfort != nil && raw != nil {
			m.cfg.Comfort.ProcessNewMail(ctx, row, raw, f.Role) // comfort.go
		}
		if m.cfg.Projects != nil {
			if err := m.cfg.Projects.ProcessNewItem(ctx, store.Item{Kind: store.ItemMessage, ID: id}); err != nil {
				w.log.Warn("sync: project rules", "message", id, "err", err)
			}
		}
		if !quiet && receivable(f.Role) {
			b.received = append(b.received, m.mailEvent(ctx, events.MailReceived, row, f, ct))
		}
		// The initial sync of a folder announces its mails per batch
		// (folder.updated, conversation.updated in flush) instead of one event
		// per mail, which flooded the UI (and the desktop event bridge).
		if !quiet {
			w.m.publishMessage(ctx, w.acc.ID, id, row.ConversationID, "added")
		}
	}
	return nil
}

// receivable reports whether new messages in a folder of this role are
// "received" (not the user's own sent mail, drafts or deleted items).
func receivable(r domain.FolderRole) bool {
	return r != domain.RoleSent && r != domain.RoleDrafts && r != domain.RoleTrash
}

func firstEmail(as []domain.Address) string {
	if len(as) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(as[0].Email))
}

func fingerprint(h domain.MessageHeaderInfo, ct *content.Content) string {
	in := threading.FingerprintInput{Date: h.Date, From: h.From, To: h.To, Subject: h.Subject}
	if ct != nil && h.MessageID == "" {
		in.TextSize = int64(len(ct.Text))
		in.Body = []byte(ct.Text)
	}
	return threading.Fingerprint(in)
}

// limitedFetcher is implemented by providers whose FetchRaw can enforce a
// size limit while the message is being received.
type limitedFetcher interface {
	FetchRawLimit(ctx context.Context, ref domain.RemoteRef, limit int64) (io.ReadCloser, error)
}

func (w *Worker) fetchRaw(ctx context.Context, ref domain.RemoteRef) ([]byte, error) {
	var raw []byte
	err := w.withProvider(ctx, func(p domain.MailProvider) error {
		// Providers that can bound the download while it arrives (IMAP)
		// must not buffer more than the sync limit (audit NF-04).
		var rc io.ReadCloser
		var err error
		if lf, ok := p.(limitedFetcher); ok {
			rc, err = lf.FetchRawLimit(ctx, ref, w.m.cfg.MaxBodySize)
		} else {
			rc, err = p.FetchRaw(ctx, ref)
		}
		if err != nil {
			if errors.Is(err, imapprov.ErrMessageTooLarge) {
				return Permanent(fmt.Errorf("sync: message larger than %d bytes: %w", w.m.cfg.MaxBodySize, err))
			}
			return err
		}
		defer rc.Close()
		raw, err = io.ReadAll(io.LimitReader(rc, w.m.cfg.MaxBodySize+1))
		if err == nil && int64(len(raw)) > w.m.cfg.MaxBodySize {
			return Permanent(fmt.Errorf("sync: message larger than %d bytes", w.m.cfg.MaxBodySize))
		}
		return err
	})
	return raw, err
}

// ==== shared ingestion helpers (also used by Actions) ==========================

func (m *Manager) extract(raw []byte) *content.Content {
	c, err := m.cfg.Extractor.Extract(raw)
	if err != nil {
		m.log.Debug("sync: content extraction failed", "err", err)
		return &content.Content{}
	}
	return &c
}

// storeContent stores the raw message in the blob store and records
// snippet, attachments and the full-text index entry.
func (m *Manager) storeContent(ctx context.Context, msg store.Message, raw []byte, ct *content.Content) (store.Message, error) {
	// Releasing a body (headersonly.go) must not delete a blob stored here
	// between its reference check and the deletion.
	m.blobMu.RLock()
	defer m.blobMu.RUnlock()
	hash, err := m.cfg.Blobs.Put(bytes.NewReader(raw))
	if err != nil {
		return msg, fmt.Errorf("sync: store raw message: %w", err)
	}
	if ct == nil {
		ct = m.extract(raw)
	}
	atts := make([]store.MessageAttachment, 0, len(ct.Attachments))
	named := false
	for _, a := range ct.Attachments {
		atts = append(atts, store.MessageAttachment{Filename: a.Name, MIMEType: a.MIME, Size: a.Size, ContentID: a.ContentID})
		if a.Name != "" && !(a.Inline && a.ContentID != "") {
			named = true
		}
	}
	if err := m.st.SetMessageAttachments(ctx, msg.ID, atts); err != nil {
		return msg, err
	}
	hasAtt := msg.HasAttachments || named
	if err := m.st.SetMessageContent(ctx, msg.ID, hash, ct.Snippet, hasAtt); err != nil {
		return msg, err
	}
	if err := m.st.SetContentTime(ctx, msg.ID, m.now()); err != nil {
		return msg, err
	}
	msg.BlobHash, msg.Snippet, msg.HasAttachments = hash, ct.Snippet, hasAtt
	return msg, m.indexMessage(ctx, msg, ct)
}

// indexMessage (re)indexes a message; without content only metadata
// (subject, addresses) is indexed.
func (m *Manager) indexMessage(ctx context.Context, msg store.Message, ct *content.Content) error {
	addrs, err := m.st.MessageAddresses(ctx, msg.ID)
	if err != nil {
		return err
	}
	var ab strings.Builder
	for _, a := range addrs {
		if a.Name != "" {
			ab.WriteString(a.Name)
			ab.WriteByte(' ')
		}
		ab.WriteString(a.Email)
		ab.WriteByte(' ')
	}
	doc := store.SearchDoc{Subject: msg.Subject, Addresses: strings.TrimSpace(ab.String())}
	if ct == nil && msg.BlobHash == "" {
		// A mail kept with header only is found by its preview and the
		// names of its attachments (known from before a release).
		doc.Body = msg.Snippet
		if atts, err := m.st.AttachmentsOf(ctx, msg.ID); err == nil {
			names := make([]string, 0, len(atts))
			for _, a := range atts {
				if a.Filename != "" {
					names = append(names, a.Filename)
				}
			}
			doc.Attachments = strings.Join(names, " ")
		}
	}
	if ct != nil {
		doc.Body = ct.Text
		names := make([]string, 0, len(ct.Attachments))
		for _, a := range ct.Attachments {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		doc.Attachments = strings.Join(names, " ")
	}
	return m.st.IndexMessage(ctx, msg.ID, doc)
}

// threadMessage runs a new (or re-appearing) message through the threading
// engine and returns the affected conversations.
func (m *Manager) threadMessage(ctx context.Context, msg store.Message, h domain.MessageHeaderInfo, ct *content.Content) ([]int64, error) {
	key := store.MessageKey(msg)
	in := threading.MessageInput{Header: h, Key: key, Namespace: id(msg.AccountID), Fingerprint: msg.Fingerprint}
	if ct != nil {
		in.AttachedMessageIDs = ct.AttachedMessageIDs
		in.CalendarUIDs = ct.CalendarUIDs
		in.InlineForwardKeys = m.matchForwards(ctx, key, msg, ct)
	}
	res, err := m.engine.AddMessage(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("sync: threading: %w", err)
	}
	convs := resultConvs(res)
	// The engine treats keys as present once any row has a conversation;
	// rows of the same key in other accounts are aligned here.
	c, err := m.st.AlignKeyConversation(ctx, key)
	if err != nil {
		return nil, err
	}
	return append(convs, c), nil
}

// contentRelations adds the relations that can only be derived from the
// body (attached messages, iCalendar UIDs, inline forwards) after the body
// was downloaded later.
func (m *Manager) contentRelations(ctx context.Context, msg store.Message, ct *content.Content) ([]int64, error) {
	if ct == nil {
		return nil, nil
	}
	key := store.MessageKey(msg)
	rels := threading.ExtractRelations(threading.MessageInput{Key: key, Namespace: id(msg.AccountID),
		AttachedMessageIDs: ct.AttachedMessageIDs, CalendarUIDs: ct.CalendarUIDs,
		InlineForwardKeys: m.matchForwards(ctx, key, msg, ct)})
	if len(rels) == 0 {
		return nil, nil
	}
	res, err := m.engine.AddRelations(ctx, rels...)
	if err != nil {
		return nil, fmt.Errorf("sync: threading: %w", err)
	}
	return resultConvs(res), nil
}

func resultConvs(res threading.Result) []int64 {
	out := []int64{res.ConversationID}
	for _, mv := range res.Moves {
		out = append(out, mv.From, mv.To)
	}
	return append(out, res.Created...)
}

// matchForwards matches inline-forwarded blocks against local messages
// (KONZEPT 6.3, confidence 0.8).
func (m *Manager) matchForwards(ctx context.Context, key string, msg store.Message, ct *content.Content) []string {
	var out []string
	for _, fh := range ct.InlineForwards {
		from, to := fh.Date.Add(-15*time.Hour), fh.Date.Add(15*time.Hour)
		if fh.Date.IsZero() {
			if fh.From.Email == "" {
				continue
			}
			ref := msg.SentAt
			if ref.IsZero() {
				ref = m.now()
			}
			from, to = ref.AddDate(-1, 0, 0), ref
		}
		cands, err := m.st.ForwardCandidates(ctx, fh.From.Email, from, to)
		if err != nil {
			m.log.Debug("sync: forward candidates", "err", err)
			continue
		}
		filtered := cands[:0]
		for _, c := range cands {
			if c.Key != key {
				filtered = append(filtered, c)
			}
		}
		if c, ok := threading.MatchForward(fh, filtered); ok {
			out = append(out, c.Key)
		}
	}
	return out
}

// setProviderTags stores the IMAP keywords of a message as tags. With
// always=false nothing is done when there are no keywords (new messages).
func (m *Manager) setProviderTags(ctx context.Context, messageID int64, flags []domain.Flag, always bool) error {
	var paths []string
	for _, f := range flags {
		s := string(f)
		if s == "" || strings.HasPrefix(s, `\`) {
			continue
		}
		if p, ok := m.cfg.KeywordMapper.FromKeyword(s); ok {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 && !always {
		return nil
	}
	it := store.Item{Kind: store.ItemMessage, ID: messageID}
	if m.cfg.Tags != nil {
		return m.cfg.Tags.SetProviderTags(ctx, it, store.TagSourceIMAPKeyword, paths)
	}
	_, _, err := m.st.SetProviderTags(ctx, it, store.TagSourceIMAPKeyword, paths)
	return err
}

// mailEvent builds a mail.* event payload.
func (m *Manager) mailEvent(ctx context.Context, typ events.Type, msg store.Message, f domain.Folder, ct *content.Content) events.Event {
	mail := &events.Mail{AccountID: msg.AccountID, Key: store.MessageKey(msg), MessageID: msg.MessageIDHdr,
		Folder: f.RemoteID, FolderRole: string(f.Role), Subject: msg.Subject, Date: msg.SentAt, Size: msg.Size}
	if acc, err := m.st.GetAccount(ctx, msg.AccountID); err == nil {
		mail.Account = acc.DisplayName
	}
	if locs, err := m.st.Locations(ctx, msg.ID); err == nil {
		for _, l := range locs {
			if l.FolderID == f.ID {
				mail.RemoteID = l.RemoteRef
			}
		}
	}
	if addrs, err := m.st.MessageAddresses(ctx, msg.ID); err == nil {
		for _, a := range addrs {
			da := domain.Address{Name: a.Name, Email: a.Email}
			switch a.Role {
			case store.RoleFrom:
				if mail.From.Email == "" {
					mail.From = da
				}
			case store.RoleTo:
				mail.To = append(mail.To, da)
			case store.RoleCc:
				mail.Cc = append(mail.Cc, da)
			case store.RoleBcc:
				mail.Bcc = append(mail.Bcc, da)
			case store.RoleReplyTo:
				mail.ReplyTo = append(mail.ReplyTo, da)
			}
		}
	}
	for _, fl := range msg.Flags.Domain() {
		mail.Flags = append(mail.Flags, string(fl))
	}
	if paths, err := m.st.TagPaths(ctx, store.Item{Kind: store.ItemMessage, ID: msg.ID}); err == nil {
		mail.Labels = paths
	}
	if ct != nil {
		const maxBody = 256 << 10
		mail.Body = ct.Text
		if len(mail.Body) > maxBody {
			mail.Body = mail.Body[:maxBody]
		}
		for _, a := range ct.Attachments {
			mail.Attachments = append(mail.Attachments, events.Attachment{Name: a.Name, MIME: a.MIME, Size: a.Size})
		}
	}
	if m.cfg.Comfort != nil {
		m.cfg.Comfort.EnrichMailEvent(ctx, msg, mail)
	}
	return events.Event{Type: typ, AccountID: msg.AccountID, Ref: mail.Key, Mail: mail}
}

// ==== bodies ====================================================================

// backfillBodies downloads a batch of missing bodies according to the body
// policy. It reports whether more remain.
func (w *Worker) backfillBodies(ctx context.Context) (bool, error) {
	cfg := &w.m.cfg
	var notBefore time.Time
	rule := w.contentRule()
	switch rule.policy {
	case BodiesOnDemand:
		return false, nil
	case BodiesRecent:
		notBefore = rule.cutoff(w.m.now())
	}
	if rule.setting {
		// Downloads caused by the day count: pausable and throttled.
		if cfg.DownloadPaused != nil && cfg.DownloadPaused() {
			return false, nil
		}
		w.mu.Lock()
		wait := w.bodyResumeAt.After(w.m.now())
		if !wait {
			w.bodyResumeAt = time.Time{}
		}
		w.mu.Unlock()
		if wait {
			return false, nil
		}
	}
	tasks, err := w.m.st.MessagesWithoutBody(ctx, w.acc.ID, notBefore, cfg.MaxBodySize, placeholderPrefix, cfg.BackfillBatch+len(w.failedBodies()))
	if err != nil {
		return false, err
	}
	done := 0
	for _, t := range tasks {
		if w.bodyFailedFor(t.MessageID) {
			continue
		}
		if done >= cfg.BackfillBatch {
			break
		}
		raw, err := w.fetchRaw(ctx, domain.RemoteRef{FolderRemoteID: t.FolderRemoteID, ID: t.RemoteRef})
		if err != nil {
			if ctx.Err() != nil || IsNetworkError(err) {
				return false, err
			}
			w.log.Info("sync: body download failed", "message", t.MessageID, "err", err)
			w.mu.Lock()
			w.bodyFailed[t.MessageID] = true
			w.mu.Unlock()
			continue
		}
		msg, err := w.m.st.GetMessage(ctx, t.MessageID)
		if err != nil {
			continue
		}
		if _, err := w.m.bodyArrived(ctx, msg, raw); err != nil {
			return false, err
		}
		done++
	}
	more := done >= cfg.BackfillBatch
	if more && rule.setting {
		// Pause before the next batch (nextDue wakes the worker then).
		w.mu.Lock()
		w.bodyResumeAt = w.m.now().Add(cfg.BackfillPause)
		w.mu.Unlock()
		return false, nil
	}
	return more, nil
}

func (w *Worker) failedBodies() map[int64]bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[int64]bool, len(w.bodyFailed))
	for k, v := range w.bodyFailed {
		out[k] = v
	}
	return out
}

func (w *Worker) bodyFailedFor(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bodyFailed[id]
}

// bodyArrived stores a downloaded body of an existing message.
func (m *Manager) bodyArrived(ctx context.Context, msg store.Message, raw []byte) (store.Message, error) {
	ct := m.extract(raw)
	msg, err := m.storeContent(ctx, msg, raw, ct)
	if err != nil {
		return msg, err
	}
	convs, err := m.contentRelations(ctx, msg, ct)
	if err != nil {
		return msg, err
	}
	convs = append(convs, msg.ConversationID)
	if err := m.st.RefreshConversations(ctx, convs...); err != nil {
		return msg, err
	}
	m.publishMessage(ctx, msg.AccountID, msg.ID, msg.ConversationID, "content")
	m.publishConversations(ctx, convs...)
	return msg, nil
}

// fetchBody downloads the raw message on demand.
func (w *Worker) fetchBody(ctx context.Context, msg store.Message) ([]byte, error) {
	locs, err := w.m.st.Locations(ctx, msg.ID)
	if err != nil {
		return nil, err
	}
	var lastErr error = store.ErrNotFound
	for _, l := range locs {
		if isPlaceholder(l.RemoteRef) {
			continue
		}
		f, err := w.m.st.GetFolder(ctx, l.FolderID)
		if err != nil {
			continue
		}
		raw, err := w.fetchRaw(ctx, domain.RemoteRef{FolderRemoteID: f.RemoteID, ID: l.RemoteRef})
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := w.m.bodyArrived(ctx, msg, raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, lastErr
}

func readBlob(bs *blob.Store, hash string) ([]byte, error) {
	rc, err := bs.Open(hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
