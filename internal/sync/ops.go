// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Operation kinds of the sync engine in the operations queue. Each one is
// applied locally first (Actions) and replayed against the server by the
// account's worker.
const (
	OpMove         = "mail.move"
	OpCopy         = "mail.copy"
	OpSetFlags     = "mail.set_flags"
	OpDelete       = "mail.delete" // permanent (expunge)
	OpAppend       = "mail.append"
	OpFolderCreate = "folder.create"
	OpFolderRename = "folder.rename"
	OpFolderDelete = "folder.delete"
)

var opKinds = []string{OpMove, OpCopy, OpSetFlags, OpDelete, OpAppend, OpFolderCreate, OpFolderRename, OpFolderDelete}

func isSyncOp(kind string) bool {
	for _, k := range opKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// opItem is one message location an operation works on.
type opItem struct {
	Message int64  `json:"m"`
	Folder  int64  `json:"f,omitempty"` // source folder (move/copy/delete)
	Ref     string `json:"r,omitempty"` // source remote ref (may be a placeholder)
	// Placeholder is the local location created in the target folder.
	Placeholder string `json:"p,omitempty"`
}

// opPayload is the JSON payload of all sync operations.
type opPayload struct {
	Items    []opItem `json:"items,omitempty"`
	Messages []int64  `json:"messages,omitempty"` // set_flags
	OldFlags []uint32 `json:"oldFlags,omitempty"` // set_flags: for revert, parallel to Messages
	Add      []string `json:"add,omitempty"`
	Remove   []string `json:"remove,omitempty"`
	// Folder is the target folder (move, copy, append) or the local folder
	// (folder.create, folder.rename).
	Folder       int64    `json:"folder,omitempty"`
	Blob         string   `json:"blob,omitempty"`  // append: raw message
	Flags        []string `json:"flags,omitempty"` // append
	Name         string   `json:"name,omitempty"`
	ParentRemote string   `json:"parentRemote,omitempty"`
	OldRemote    string   `json:"oldRemote,omitempty"`
	OldName      string   `json:"oldName,omitempty"`
	OldParent    int64    `json:"oldParent,omitempty"`
	Remote       string   `json:"remote,omitempty"` // folder.delete
	Chain        []string `json:"chain,omitempty"`  // workflow chain of the action
}

func decodePayload(s string) (opPayload, error) {
	var p opPayload
	err := json.Unmarshal([]byte(s), &p)
	return p, err
}

func (p opPayload) protection(kind string) protection {
	var pr protection
	for _, it := range p.Items {
		pr.msgs = append(pr.msgs, it.Message)
	}
	pr.msgs = append(pr.msgs, p.Messages...)
	switch kind {
	case OpFolderCreate:
		pr.folders = append(pr.folders, p.Folder)
	case OpFolderRename:
		pr.folders = append(pr.folders, p.Folder)
		pr.remoteFolders = append(pr.remoteFolders, p.OldRemote)
	case OpFolderDelete:
		pr.remoteFolders = append(pr.remoteFolders, p.Remote)
	}
	return pr
}

func toFlags(ss []string) []domain.Flag {
	out := make([]domain.Flag, 0, len(ss))
	for _, s := range ss {
		out = append(out, domain.Flag(s))
	}
	return out
}

func fromFlags(fs []domain.Flag) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, string(f))
	}
	return out
}

// runOps executes all due operations of the account. Network errors abort
// (the operation is released without counting the attempt).
func (w *Worker) runOps(ctx context.Context) error {
	st := w.m.st
	for ctx.Err() == nil {
		op, ok, err := st.ClaimNextAccountOp(ctx, w.m.now(), w.acc.ID, opKinds...)
		if err != nil || !ok {
			return err
		}
		if err := w.runOp(ctx, op); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (w *Worker) runOp(ctx context.Context, op store.Op) error {
	st, cfg := w.m.st, &w.m.cfg
	p, err := decodePayload(op.Payload)
	if err != nil {
		_, ferr := st.MarkOpRetry(ctx, op.ID, fmt.Errorf("bad payload: %w", err), w.m.now(), cfg.OpMaxAttempts, true)
		w.unprotect(op.ID)
		return ferr
	}
	execErr := w.execute(ctx, op, p)
	bg := context.WithoutCancel(ctx)
	switch {
	case execErr == nil:
		w.unprotect(op.ID)
		return st.MarkOpDone(bg, op.ID)
	case ctx.Err() != nil:
		_ = st.ReleaseOp(bg, op.ID, execErr, w.m.now())
		return ctx.Err()
	case IsNetworkError(execErr):
		_ = st.ReleaseOp(bg, op.ID, execErr, w.m.now())
		return execErr
	}
	state, err := st.MarkOpRetry(bg, op.ID, execErr, w.m.now().Add(cfg.OpBackoff(op.Attempts)), cfg.OpMaxAttempts, IsPermanent(execErr))
	if err != nil {
		return err
	}
	w.log.Warn("sync: operation failed", "op", op.ID, "kind", op.Kind, "attempt", op.Attempts, "state", state, "err", execErr)
	if state == store.OpFailed {
		w.revert(bg, op, p)
		w.unprotect(op.ID)
		w.m.publish(bg, events.Event{Type: EventOpFailed, AccountID: w.acc.ID, Chain: p.Chain, Data: map[string]string{
			"opId": id(op.ID), "kind": op.Kind, "error": execErr.Error()}})
	}
	return nil
}

// folderCache caches folder lookups during one operation.
type folderCache struct {
	st *store.Store
	m  map[int64]domain.Folder
}

func (c *folderCache) get(ctx context.Context, id int64) (domain.Folder, error) {
	if f, ok := c.m[id]; ok {
		return f, nil
	}
	f, err := c.st.GetFolder(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return f, Permanent(fmt.Errorf("sync: folder %d no longer exists", id))
	}
	if err != nil {
		return f, err
	}
	if c.m == nil {
		c.m = map[int64]domain.Folder{}
	}
	c.m[id] = f
	return f, nil
}

// resolveRef returns the server reference of an item's source location. A
// placeholder (the item was the target of an earlier, now completed,
// operation) is resolved to the message's real location in that folder.
func (w *Worker) resolveRef(ctx context.Context, it opItem) (string, bool, error) {
	if !isPlaceholder(it.Ref) {
		return it.Ref, true, nil
	}
	locs, err := w.m.st.Locations(ctx, it.Message)
	if err != nil {
		return "", false, err
	}
	for _, l := range locs {
		if l.FolderID == it.Folder && !isPlaceholder(l.RemoteRef) {
			return l.RemoteRef, true, nil
		}
	}
	return "", false, nil
}

// replacePlaceholder turns the placeholder location created by an action
// into the real location reported by the server. Without a server ref the
// placeholder is dropped and the folder is synced to find the message.
func (w *Worker) replacePlaceholder(ctx context.Context, folderID int64, placeholder string, msgID int64, newRef string) error {
	st := w.m.st
	if placeholder == "" {
		return nil
	}
	if newRef == "" {
		_, err := st.RemoveLocation(ctx, folderID, placeholder)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		w.markDirty(folderID)
		return nil
	}
	err := st.MoveLocation(ctx, folderID, placeholder, folderID, newRef)
	if errors.Is(err, store.ErrNotFound) {
		// The placeholder was moved on by a later action or dropped by a
		// full resync; record the server location anyway.
		return st.AddLocation(ctx, store.Location{MessageID: msgID, FolderID: folderID, RemoteRef: newRef})
	}
	return err
}

func (w *Worker) execute(ctx context.Context, op store.Op, p opPayload) error {
	fc := &folderCache{st: w.m.st}
	switch op.Kind {
	case OpMove:
		return w.execMove(ctx, fc, p)
	case OpCopy:
		return w.execCopy(ctx, fc, p)
	case OpSetFlags:
		return w.execSetFlags(ctx, fc, p)
	case OpDelete:
		return w.execDelete(ctx, fc, p)
	case OpAppend:
		return w.execAppend(ctx, fc, p)
	case OpFolderCreate:
		return w.execFolderCreate(ctx, fc, p)
	case OpFolderRename:
		return w.execFolderRename(ctx, fc, p)
	case OpFolderDelete:
		return w.withProvider(ctx, func(prov domain.MailProvider) error { return prov.DeleteFolder(ctx, p.Remote) })
	}
	return Permanent(fmt.Errorf("sync: unknown operation %q", op.Kind))
}

func (w *Worker) execMove(ctx context.Context, fc *folderCache, p opPayload) error {
	to, err := fc.get(ctx, p.Folder)
	if err != nil {
		return err
	}
	var (
		refs  []domain.RemoteRef
		items []opItem
	)
	for _, it := range p.Items {
		src, err := fc.get(ctx, it.Folder)
		if err != nil {
			return err
		}
		ref, ok, err := w.resolveRef(ctx, it)
		if err != nil {
			return err
		}
		if !ok {
			w.markDirty(it.Folder)
			return fmt.Errorf("sync: source of message %d in %q not synced yet", it.Message, src.RemoteID)
		}
		refs = append(refs, domain.RemoteRef{FolderRemoteID: src.RemoteID, ID: ref})
		items = append(items, it)
	}
	if len(refs) == 0 {
		return nil
	}
	var newRefs []domain.RemoteRef
	if err := w.withProvider(ctx, func(prov domain.MailProvider) error {
		var err error
		newRefs, err = prov.Move(ctx, refs, to.RemoteID)
		return err
	}); err != nil {
		return err
	}
	for i, it := range items {
		nr := ""
		if i < len(newRefs) {
			nr = newRefs[i].ID
		}
		if err := w.replacePlaceholder(ctx, to.ID, it.Placeholder, it.Message, nr); err != nil {
			return err
		}
	}
	w.refreshFolders(ctx, to.ID)
	return nil
}

func (w *Worker) execCopy(ctx context.Context, fc *folderCache, p opPayload) error {
	to, err := fc.get(ctx, p.Folder)
	if err != nil {
		return err
	}
	for _, it := range p.Items {
		src, err := fc.get(ctx, it.Folder)
		if err != nil {
			return err
		}
		ref, ok, err := w.resolveRef(ctx, it)
		if err != nil {
			return err
		}
		if !ok {
			w.markDirty(it.Folder)
			return fmt.Errorf("sync: source of message %d not synced yet", it.Message)
		}
		msg, err := w.m.st.GetMessage(ctx, it.Message)
		if err != nil {
			return Permanent(err)
		}
		raw, err := w.fetchRaw(ctx, domain.RemoteRef{FolderRemoteID: src.RemoteID, ID: ref})
		if err != nil {
			return err
		}
		var nr domain.RemoteRef
		if err := w.withProvider(ctx, func(prov domain.MailProvider) error {
			var err error
			nr, err = prov.Append(ctx, to.RemoteID, bytes.NewReader(raw), msg.Flags.Domain())
			return err
		}); err != nil {
			return err
		}
		if err := w.replacePlaceholder(ctx, to.ID, it.Placeholder, it.Message, nr.ID); err != nil {
			return err
		}
	}
	w.refreshFolders(ctx, to.ID)
	return nil
}

func (w *Worker) execSetFlags(ctx context.Context, fc *folderCache, p opPayload) error {
	var refs []domain.RemoteRef
	for _, mid := range p.Messages {
		locs, err := w.m.st.Locations(ctx, mid)
		if err != nil {
			return err
		}
		for _, l := range locs {
			if isPlaceholder(l.RemoteRef) {
				continue
			}
			f, err := fc.get(ctx, l.FolderID)
			if err != nil {
				continue
			}
			refs = append(refs, domain.RemoteRef{FolderRemoteID: f.RemoteID, ID: l.RemoteRef})
		}
	}
	if len(refs) == 0 {
		return nil // nothing on the server (anymore)
	}
	return w.withProvider(ctx, func(prov domain.MailProvider) error {
		return prov.SetFlags(ctx, refs, toFlags(p.Add), toFlags(p.Remove))
	})
}

func (w *Worker) execDelete(ctx context.Context, fc *folderCache, p opPayload) error {
	var (
		refs []domain.RemoteRef
		locs []store.Location
	)
	for _, it := range p.Items {
		f, err := fc.get(ctx, it.Folder)
		if err != nil {
			continue // folder gone: so is the message
		}
		ref, ok, err := w.resolveRef(ctx, it)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		refs = append(refs, domain.RemoteRef{FolderRemoteID: f.RemoteID, ID: ref})
		locs = append(locs, store.Location{MessageID: it.Message, FolderID: f.ID, RemoteRef: ref})
	}
	if len(refs) == 0 {
		return nil
	}
	if err := w.withProvider(ctx, func(prov domain.MailProvider) error { return prov.Delete(ctx, refs) }); err != nil {
		return err
	}
	for _, l := range locs {
		if _, err := w.m.st.RemoveLocation(ctx, l.FolderID, l.RemoteRef); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (w *Worker) execAppend(ctx context.Context, fc *folderCache, p opPayload) error {
	f, err := fc.get(ctx, p.Folder)
	if err != nil {
		return err
	}
	raw, err := readBlob(w.m.cfg.Blobs, p.Blob)
	if err != nil {
		return Permanent(fmt.Errorf("sync: append: raw message: %w", err))
	}
	var ref domain.RemoteRef
	if err := w.withProvider(ctx, func(prov domain.MailProvider) error {
		var err error
		ref, err = prov.Append(ctx, f.RemoteID, bytes.NewReader(raw), toFlags(p.Flags))
		return err
	}); err != nil {
		return err
	}
	for _, it := range p.Items {
		if err := w.replacePlaceholder(ctx, f.ID, it.Placeholder, it.Message, ref.ID); err != nil {
			return err
		}
	}
	w.refreshFolders(ctx, f.ID)
	return nil
}

func (w *Worker) execFolderCreate(ctx context.Context, fc *folderCache, p opPayload) error {
	local, err := fc.get(ctx, p.Folder)
	if err != nil {
		return err
	}
	var f domain.Folder
	if err := w.withProvider(ctx, func(prov domain.MailProvider) error {
		var err error
		f, err = prov.CreateFolder(ctx, p.ParentRemote, p.Name)
		return err
	}); err != nil {
		return err
	}
	if f.RemoteID != "" && (f.RemoteID != local.RemoteID || f.Name != local.Name) {
		if err := w.m.st.UpdateFolderPath(ctx, local.ID, f.RemoteID, f.Name, local.ParentID); err != nil {
			return err
		}
	}
	w.mu.Lock()
	w.listFolders = true
	w.mu.Unlock()
	w.m.publish(ctx, folderListEvent(w.acc.ID))
	return nil
}

func (w *Worker) execFolderRename(ctx context.Context, fc *folderCache, p opPayload) error {
	local, err := fc.get(ctx, p.Folder)
	if err != nil {
		return err
	}
	var f domain.Folder
	if err := w.withProvider(ctx, func(prov domain.MailProvider) error {
		var err error
		f, err = prov.RenameFolder(ctx, p.OldRemote, p.Name)
		return err
	}); err != nil {
		return err
	}
	if f.RemoteID != "" && f.RemoteID != local.RemoteID {
		if err := w.m.st.UpdateFolderPath(ctx, local.ID, f.RemoteID, f.Name, local.ParentID); err != nil {
			return err
		}
		w.m.publish(ctx, folderListEvent(w.acc.ID))
	}
	return nil
}

// revert undoes the local effect of a permanently failed operation.
func (w *Worker) revert(ctx context.Context, op store.Op, p opPayload) {
	st := w.m.st
	logErr := func(err error) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			w.log.Warn("sync: revert failed", "op", op.ID, "err", err)
		}
	}
	var msgs []int64
	switch op.Kind {
	case OpMove, OpCopy, OpAppend:
		for _, it := range p.Items {
			msgs = append(msgs, it.Message)
			if it.Placeholder != "" {
				_, err := st.RemoveLocation(ctx, p.Folder, it.Placeholder)
				logErr(err)
			}
			if op.Kind == OpMove && it.Ref != "" && !isPlaceholder(it.Ref) {
				logErr(st.AddLocation(ctx, store.Location{MessageID: it.Message, FolderID: it.Folder, RemoteRef: it.Ref}))
				w.refreshFolders(ctx, it.Folder)
			}
		}
		w.refreshFolders(ctx, p.Folder)
	case OpDelete:
		for _, it := range p.Items {
			msgs = append(msgs, it.Message)
			if !isPlaceholder(it.Ref) {
				logErr(st.AddLocation(ctx, store.Location{MessageID: it.Message, FolderID: it.Folder, RemoteRef: it.Ref}))
				w.refreshFolders(ctx, it.Folder)
			}
		}
	case OpSetFlags:
		for i, mid := range p.Messages {
			msgs = append(msgs, mid)
			if i < len(p.OldFlags) {
				logErr(st.SetMessageFlags(ctx, mid, store.Flags(p.OldFlags[i])))
			}
			if locs, err := st.Locations(ctx, mid); err == nil {
				for _, l := range locs {
					w.refreshFolders(ctx, l.FolderID)
				}
			}
		}
	case OpFolderCreate:
		logErr(st.DeleteFolder(ctx, p.Folder))
		w.m.publish(ctx, folderListEvent(w.acc.ID))
	case OpFolderRename:
		logErr(st.UpdateFolderPath(ctx, p.Folder, p.OldRemote, p.OldName, p.OldParent))
		w.m.publish(ctx, folderListEvent(w.acc.ID))
	case OpFolderDelete:
		w.mu.Lock()
		w.listFolders = true
		w.mu.Unlock()
	}
	if len(msgs) > 0 {
		convs, _ := st.ConversationIDsOf(ctx, msgs...)
		logErr(st.RefreshConversations(ctx, convs...))
		for _, mid := range msgs {
			w.m.publishMessage(ctx, w.acc.ID, mid, 0, "reverted")
		}
		w.m.publishConversations(ctx, convs...)
	}
}

// refreshFolders recomputes and publishes folder counters.
func (w *Worker) refreshFolders(ctx context.Context, ids ...int64) {
	w.m.refreshFolders(ctx, w.acc.ID, ids...)
}

func (m *Manager) refreshFolders(ctx context.Context, accountID int64, ids ...int64) {
	seen := map[int64]bool{}
	for _, fid := range ids {
		if fid == 0 || seen[fid] {
			continue
		}
		seen[fid] = true
		if total, unread, err := m.st.RefreshFolderCounts(ctx, fid); err == nil {
			m.publishFolder(ctx, accountID, fid, total, unread)
		}
	}
}

// ==== tag sync =================================================================

// pushTags pushes pending local tag changes of the account's mails to the
// server as IMAP keywords (store.PendingTagSync) and acknowledges them.
func (w *Worker) pushTags(ctx context.Context) error {
	st := w.m.st
	pending, err := st.PendingTagSync(ctx, w.acc.ID, 200)
	if err != nil || len(pending) == 0 {
		return err
	}
	type change struct {
		add, remove []domain.Flag
		ops         []store.TagSyncOp
	}
	byMsg := map[int64]*change{}
	var order []int64
	for _, o := range pending {
		if o.Item.Kind != store.ItemMessage {
			continue
		}
		c := byMsg[o.Item.ID]
		if c == nil {
			c = &change{}
			byMsg[o.Item.ID] = c
			order = append(order, o.Item.ID)
		}
		c.ops = append(c.ops, o)
		kw, err := w.m.cfg.KeywordMapper.ToKeyword(o.TagPath)
		if err != nil {
			w.log.Info("sync: tag not representable as keyword", "tag", o.TagPath, "err", err)
			continue
		}
		if o.Op == "remove" {
			c.remove = append(c.remove, domain.Flag(kw))
		} else {
			c.add = append(c.add, domain.Flag(kw))
		}
	}
	fc := &folderCache{st: st}
	for _, mid := range order {
		c := byMsg[mid]
		locs, err := st.Locations(ctx, mid)
		if err != nil {
			return err
		}
		var refs []domain.RemoteRef
		for _, l := range locs {
			if isPlaceholder(l.RemoteRef) {
				continue
			}
			if f, err := fc.get(ctx, l.FolderID); err == nil {
				refs = append(refs, domain.RemoteRef{FolderRemoteID: f.RemoteID, ID: l.RemoteRef})
			}
		}
		if w.isProtectedMsg(mid) && len(refs) == 0 {
			continue // e.g. moved: push after the move reached the server
		}
		if len(refs) > 0 && (len(c.add) > 0 || len(c.remove) > 0) {
			err := w.withProvider(ctx, func(prov domain.MailProvider) error {
				return prov.SetFlags(ctx, refs, c.add, c.remove)
			})
			if err != nil {
				if ctx.Err() != nil || IsNetworkError(err) {
					return err
				}
				w.log.Warn("sync: pushing tags failed, giving up", "message", mid, "err", err)
			}
		}
		if err := st.AckTagSync(ctx, c.ops...); err != nil {
			return err
		}
	}
	return nil
}

// newPlaceholder returns a unique placeholder remote ref.
func newPlaceholder() string {
	return placeholderPrefix + strings.ToLower(events.NewID())
}
