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
	"github.com/Midas1989/Mailclient/internal/sync/content"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Actions are the local-first mail operations used by the app layer
// (KONZEPT 4.2): each call changes the store immediately, publishes the
// corresponding events and queues an operation that the account's worker
// replays against the server. If the operation fails permanently, its local
// effect is reverted and EventOpFailed is published.
//
// The workflow chain of ctx (workflow.WithEventChain) is propagated into the
// published events for loop protection.
type Actions struct{ m *Manager }

// ErrCrossAccount is returned when messages and target folder belong to
// different accounts.
var ErrCrossAccount = errors.New("sync: messages and folder belong to different accounts")

// enqueue stores an operation and hands it to the account's worker.
func (a *Actions) enqueue(ctx context.Context, accountID int64, kind string, p opPayload, token int64) (int64, error) {
	m := a.m
	p.Chain = workflow.EventChain(ctx)
	b, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	opID, err := m.st.EnqueueOp(ctx, accountID, kind, string(b))
	if w := m.worker(accountID); w != nil {
		if token != 0 {
			w.protectRekey(token, opID)
		}
		w.poke()
	}
	return opID, err
}

// protect registers a protection before the local change is applied, so a
// concurrent sync cannot revert it; it is re-keyed by enqueue.
func (a *Actions) protect(accountID int64, p protection) int64 {
	if w := a.m.worker(accountID); w != nil {
		return w.protectNew(p)
	}
	return 0
}

func (a *Actions) release(accountID, token int64) {
	if w := a.m.worker(accountID); w != nil && token != 0 {
		w.protectRekey(token, 0)
	}
}

func (a *Actions) folder(ctx context.Context, id int64) (domain.Folder, error) {
	f, err := a.m.st.GetFolder(ctx, id)
	if err != nil {
		return f, fmt.Errorf("sync: folder %d: %w", id, err)
	}
	return f, nil
}

// Move moves messages to a folder (one location per message: the first
// one outside the target folder).
func (a *Actions) Move(ctx context.Context, messageIDs []int64, toFolderID int64) error {
	return a.MoveFrom(ctx, messageIDs, 0, toFolderID)
}

// MoveFrom moves the location of each message in fromFolderID (0 = the
// first location outside the target) to toFolderID. Messages already in
// the target folder are skipped.
func (a *Actions) MoveFrom(ctx context.Context, messageIDs []int64, fromFolderID, toFolderID int64) error {
	m := a.m
	to, err := a.folder(ctx, toFolderID)
	if err != nil {
		return err
	}
	tok := a.protect(to.AccountID, protection{msgs: messageIDs})
	var (
		items   []opItem
		folders = []int64{to.ID}
		convs   []int64
		moved   []events.Event
	)
	for _, mid := range messageIDs {
		msg, err := m.st.GetMessage(ctx, mid)
		if err != nil {
			a.release(to.AccountID, tok)
			return err
		}
		if msg.AccountID != to.AccountID {
			a.release(to.AccountID, tok)
			return ErrCrossAccount
		}
		locs, err := m.st.Locations(ctx, mid)
		if err != nil {
			a.release(to.AccountID, tok)
			return err
		}
		var src *store.Location
		for i, l := range locs {
			if l.FolderID == to.ID {
				continue
			}
			if fromFolderID == 0 || l.FolderID == fromFolderID {
				src = &locs[i]
				break
			}
		}
		if src == nil {
			continue
		}
		ph := newPlaceholder()
		if err := m.st.MoveLocation(ctx, src.FolderID, src.RemoteRef, to.ID, ph); err != nil {
			a.release(to.AccountID, tok)
			return err
		}
		items = append(items, opItem{Message: mid, Folder: src.FolderID, Ref: src.RemoteRef, Placeholder: ph})
		folders = append(folders, src.FolderID)
		convs = append(convs, msg.ConversationID)
		if from, err := m.st.GetFolder(ctx, src.FolderID); err == nil {
			ev := m.mailEvent(ctx, events.MailMoved, msg, to, nil)
			ev.Mail.OldFolder = from.RemoteID
			ev.Source = "user"
			moved = append(moved, ev)
		}
	}
	if len(items) == 0 {
		a.release(to.AccountID, tok)
		return nil
	}
	if _, err := a.enqueue(ctx, to.AccountID, OpMove, opPayload{Items: items, Folder: to.ID}, tok); err != nil {
		return err
	}
	m.refreshFolders(ctx, to.AccountID, folders...)
	for _, it := range items {
		m.publishMessage(ctx, to.AccountID, it.Message, 0, "moved")
	}
	m.publishConversations(ctx, convs...)
	for _, ev := range moved {
		m.publish(ctx, ev)
	}
	return nil
}

// Copy copies messages into a folder (download + append on the server).
func (a *Actions) Copy(ctx context.Context, messageIDs []int64, toFolderID int64) error {
	m := a.m
	to, err := a.folder(ctx, toFolderID)
	if err != nil {
		return err
	}
	var (
		items []opItem
		convs []int64
	)
	tok := a.protect(to.AccountID, protection{msgs: messageIDs})
	for _, mid := range messageIDs {
		msg, err := m.st.GetMessage(ctx, mid)
		if err != nil {
			a.release(to.AccountID, tok)
			return err
		}
		if msg.AccountID != to.AccountID {
			a.release(to.AccountID, tok)
			return ErrCrossAccount
		}
		locs, err := m.st.Locations(ctx, mid)
		if err != nil || len(locs) == 0 {
			continue
		}
		src := locs[0]
		ph := newPlaceholder()
		if err := m.st.AddLocation(ctx, store.Location{MessageID: mid, FolderID: to.ID, RemoteRef: ph}); err != nil {
			a.release(to.AccountID, tok)
			return err
		}
		items = append(items, opItem{Message: mid, Folder: src.FolderID, Ref: src.RemoteRef, Placeholder: ph})
		convs = append(convs, msg.ConversationID)
	}
	if len(items) == 0 {
		a.release(to.AccountID, tok)
		return nil
	}
	if _, err := a.enqueue(ctx, to.AccountID, OpCopy, opPayload{Items: items, Folder: to.ID}, tok); err != nil {
		return err
	}
	m.refreshFolders(ctx, to.AccountID, to.ID)
	m.publishConversations(ctx, convs...)
	return nil
}

// SetFlags adds and removes flags. Standard flags are applied locally;
// keywords are only sent to the server (use tags for labels).
func (a *Actions) SetFlags(ctx context.Context, messageIDs []int64, add, remove []domain.Flag) error {
	m := a.m
	byAcc := map[int64][]store.Message{}
	var accOrder []int64
	for _, mid := range messageIDs {
		msg, err := m.st.GetMessage(ctx, mid)
		if err != nil {
			return err
		}
		if _, ok := byAcc[msg.AccountID]; !ok {
			accOrder = append(accOrder, msg.AccountID)
		}
		byAcc[msg.AccountID] = append(byAcc[msg.AccountID], msg)
	}
	addBits, remBits := store.FlagsFromDomain(add), store.FlagsFromDomain(remove)
	for _, acc := range accOrder {
		msgs := byAcc[acc]
		ids := make([]int64, len(msgs))
		for i, msg := range msgs {
			ids[i] = msg.ID
		}
		tok := a.protect(acc, protection{msgs: ids})
		p := opPayload{Add: fromFlags(add), Remove: fromFlags(remove)}
		var (
			folders []int64
			convs   []int64
			flagged []events.Event
		)
		for _, msg := range msgs {
			nf := (msg.Flags | addBits) &^ remBits
			if nf != msg.Flags {
				if err := m.st.SetMessageFlags(ctx, msg.ID, nf); err != nil {
					a.release(acc, tok)
					return err
				}
			}
			p.Messages = append(p.Messages, msg.ID)
			p.OldFlags = append(p.OldFlags, uint32(msg.Flags))
			convs = append(convs, msg.ConversationID)
			locs, _ := m.st.Locations(ctx, msg.ID)
			for _, l := range locs {
				folders = append(folders, l.FolderID)
			}
			if nf&store.FlagFlagged != 0 && msg.Flags&store.FlagFlagged == 0 && len(locs) > 0 {
				if f, err := m.st.GetFolder(ctx, locs[0].FolderID); err == nil {
					msg.Flags = nf
					ev := m.mailEvent(ctx, events.MailFlagged, msg, f, nil)
					ev.Source = "user"
					flagged = append(flagged, ev)
				}
			}
		}
		if _, err := a.enqueue(ctx, acc, OpSetFlags, p, tok); err != nil {
			return err
		}
		if err := m.st.RefreshConversations(ctx, convs...); err != nil {
			return err
		}
		m.refreshFolders(ctx, acc, folders...)
		for _, mid := range p.Messages {
			m.publishMessage(ctx, acc, mid, 0, "flags")
		}
		m.publishConversations(ctx, convs...)
		for _, ev := range flagged {
			m.publish(ctx, ev)
		}
	}
	return nil
}

// MarkRead sets or clears \Seen.
func (a *Actions) MarkRead(ctx context.Context, messageIDs []int64, read bool) error {
	if read {
		return a.SetFlags(ctx, messageIDs, []domain.Flag{domain.FlagSeen}, nil)
	}
	return a.SetFlags(ctx, messageIDs, nil, []domain.Flag{domain.FlagSeen})
}

// Delete moves messages to the trash folder of their account, or deletes
// them permanently if they already are in the trash, the account has no
// trash folder or Config.PermanentDelete is set. The account's trash mode
// (store.FolderSettings) may instead mark them \Deleted (removed by
// Expunge) or delete them immediately.
func (a *Actions) Delete(ctx context.Context, messageIDs []int64) error {
	m := a.m
	if m.cfg.PermanentDelete {
		return a.DeletePermanently(ctx, messageIDs)
	}
	var permanent, mark []int64
	toTrash := map[int64][]int64{}
	modes := map[int64]string{}
	for _, mid := range messageIDs {
		msg, err := m.st.GetMessage(ctx, mid)
		if err != nil {
			return err
		}
		switch a.trashMode(ctx, modes, msg.AccountID) {
		case store.TrashDelete:
			permanent = append(permanent, mid)
			continue
		case store.TrashMark:
			if msg.Flags&store.FlagDeleted != 0 {
				permanent = append(permanent, mid)
			} else {
				mark = append(mark, mid)
			}
			continue
		}
		trash, err := m.st.FolderByRole(ctx, msg.AccountID, domain.RoleTrash)
		if errors.Is(err, store.ErrNotFound) {
			permanent = append(permanent, mid)
			continue
		}
		if err != nil {
			return err
		}
		locs, err := m.st.Locations(ctx, mid)
		if err != nil {
			return err
		}
		outside := false
		for _, l := range locs {
			outside = outside || l.FolderID != trash.ID
		}
		if outside {
			toTrash[trash.ID] = append(toTrash[trash.ID], mid)
		} else {
			permanent = append(permanent, mid)
		}
	}
	for trash, ids := range toTrash {
		if err := a.MoveFrom(ctx, ids, 0, trash); err != nil {
			return err
		}
	}
	if len(mark) > 0 {
		if err := a.SetFlags(ctx, mark, []domain.Flag{domain.FlagDeleted}, nil); err != nil {
			return err
		}
	}
	if len(permanent) > 0 {
		return a.DeletePermanently(ctx, permanent)
	}
	return nil
}

// DeletePermanently removes all locations of the messages (expunging them on
// the server). The messages become orphaned locally and are garbage
// collected later (their relations are kept).
func (a *Actions) DeletePermanently(ctx context.Context, messageIDs []int64) error {
	m := a.m
	byAcc := map[int64][]opItem{}
	var accOrder []int64
	var convs []int64
	folders := map[int64][]int64{}
	for _, mid := range messageIDs {
		msg, err := m.st.GetMessage(ctx, mid)
		if err != nil {
			return err
		}
		locs, err := m.st.Locations(ctx, mid)
		if err != nil {
			return err
		}
		if _, ok := byAcc[msg.AccountID]; !ok {
			accOrder = append(accOrder, msg.AccountID)
		}
		for _, l := range locs {
			byAcc[msg.AccountID] = append(byAcc[msg.AccountID], opItem{Message: mid, Folder: l.FolderID, Ref: l.RemoteRef})
			folders[msg.AccountID] = append(folders[msg.AccountID], l.FolderID)
		}
		if err := m.st.MarkOrphaned(ctx, mid); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		convs = append(convs, msg.ConversationID)
		m.publishMessage(ctx, msg.AccountID, mid, msg.ConversationID, "deleted")
	}
	for _, acc := range accOrder {
		if items := byAcc[acc]; len(items) > 0 {
			if _, err := a.enqueue(ctx, acc, OpDelete, opPayload{Items: items}, 0); err != nil {
				return err
			}
		}
		m.refreshFolders(ctx, acc, folders[acc]...)
	}
	if err := m.st.RefreshConversations(ctx, convs...); err != nil {
		return err
	}
	m.publishConversations(ctx, convs...)
	return nil
}

// Archive moves messages to the archive folder of their account (or its
// yearly / monthly subfolder, created on demand, depending on the account's
// archive mode).
func (a *Actions) Archive(ctx context.Context, messageIDs []int64) error {
	byFolder := map[int64][]int64{}
	modes := map[int64]string{}
	for _, mid := range messageIDs {
		msg, err := a.m.st.GetMessage(ctx, mid)
		if err != nil {
			return err
		}
		mode, ok := modes[msg.AccountID]
		if !ok {
			fs, _ := a.m.st.FolderSettings(ctx, msg.AccountID)
			mode, modes[msg.AccountID] = fs.ArchiveMode, fs.ArchiveMode
		}
		f, err := a.archiveFolder(ctx, msg, mode)
		if err != nil {
			return err
		}
		byFolder[f] = append(byFolder[f], mid)
	}
	for f, ids := range byFolder {
		if err := a.MoveFrom(ctx, ids, 0, f); err != nil {
			return err
		}
	}
	return nil
}

// Append stores a raw message (draft, sent copy) in a folder: it is
// ingested locally right away and uploaded by the worker. It returns the
// message id.
func (a *Actions) Append(ctx context.Context, folderID int64, raw []byte, flags []domain.Flag) (int64, error) {
	m := a.m
	f, err := a.folder(ctx, folderID)
	if err != nil {
		return 0, err
	}
	h, err := content.ParseHeader(raw)
	if err != nil {
		return 0, err
	}
	hash, err := m.cfg.Blobs.Put(bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	ct := m.extract(raw)
	if h.Date.IsZero() {
		h.Date = m.now()
	}
	msg := store.Message{AccountID: f.AccountID, MessageIDHdr: h.MessageID, Fingerprint: fingerprint(h, ct),
		Subject: h.Subject, From: firstEmail(h.From), SentAt: h.Date, ReceivedAt: m.now(), Size: int64(len(raw)),
		Flags: store.FlagsFromDomain(flags)}
	mid, created, err := m.st.UpsertMessage(ctx, msg)
	if err != nil {
		return 0, err
	}
	tok := a.protect(f.AccountID, protection{msgs: []int64{mid}})
	ph := newPlaceholder()
	if err := m.st.AddLocation(ctx, store.Location{MessageID: mid, FolderID: f.ID, RemoteRef: ph}); err != nil {
		a.release(f.AccountID, tok)
		return 0, err
	}
	if created {
		if err := m.st.SetMessageAddresses(ctx, mid, store.AddressesFromHeader(h)); err != nil {
			a.release(f.AccountID, tok)
			return 0, err
		}
	}
	row, err := m.st.GetMessage(ctx, mid)
	if err != nil {
		a.release(f.AccountID, tok)
		return 0, err
	}
	if row, err = m.storeContent(ctx, row, raw, ct); err != nil {
		a.release(f.AccountID, tok)
		return 0, err
	}
	var convs []int64
	if created || row.ConversationID == 0 {
		if convs, err = m.threadMessage(ctx, row, h, ct); err != nil {
			a.release(f.AccountID, tok)
			return 0, err
		}
	}
	if _, err := a.enqueue(ctx, f.AccountID, OpAppend, opPayload{Folder: f.ID, Blob: hash, Flags: fromFlags(flags),
		Items: []opItem{{Message: mid, Placeholder: ph}}}, tok); err != nil {
		return 0, err
	}
	if err := m.st.RefreshConversations(ctx, convs...); err != nil {
		return 0, err
	}
	m.refreshFolders(ctx, f.AccountID, f.ID)
	m.publishMessage(ctx, f.AccountID, mid, row.ConversationID, "added")
	m.publishConversations(ctx, convs...)
	return mid, nil
}

// CreateFolder creates a folder below parentFolderID (0 = top level) and
// returns its id.
func (a *Actions) CreateFolder(ctx context.Context, accountID, parentFolderID int64, name string) (int64, error) {
	m := a.m
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("sync: empty folder name")
	}
	folders, err := m.st.ListFolders(ctx, accountID)
	if err != nil {
		return 0, err
	}
	delim := "/"
	for _, f := range folders {
		if f.Delimiter != "" {
			delim = f.Delimiter
			break
		}
	}
	var parent domain.Folder
	remote := name
	if parentFolderID != 0 {
		if parent, err = a.folder(ctx, parentFolderID); err != nil {
			return 0, err
		}
		if parent.AccountID != accountID {
			return 0, ErrCrossAccount
		}
		if parent.Delimiter != "" {
			delim = parent.Delimiter
		}
		remote = parent.RemoteID + delim + name
	}
	if strings.Contains(name, delim) {
		return 0, fmt.Errorf("sync: folder name %q contains the hierarchy delimiter %q", name, delim)
	}
	if _, err := m.st.FolderByRemoteID(ctx, accountID, remote); err == nil {
		return 0, fmt.Errorf("sync: folder %q exists already", remote)
	}
	fid, err := m.st.UpsertFolder(ctx, domain.Folder{AccountID: accountID, ParentID: parentFolderID, RemoteID: remote,
		Name: name, Delimiter: delim, Subscribed: true})
	if err != nil {
		return 0, err
	}
	tok := a.protect(accountID, protection{folders: []int64{fid}})
	if _, err := a.enqueue(ctx, accountID, OpFolderCreate, opPayload{Folder: fid, ParentRemote: parent.RemoteID, Name: name}, tok); err != nil {
		return 0, err
	}
	m.publish(ctx, folderListEvent(accountID))
	return fid, nil
}

// RenameFolder renames a folder (keeping it below its parent).
func (a *Actions) RenameFolder(ctx context.Context, folderID int64, newName string) error {
	m := a.m
	f, err := a.folder(ctx, folderID)
	if err != nil {
		return err
	}
	newName = strings.TrimSpace(newName)
	if newName == "" || (f.Delimiter != "" && strings.Contains(newName, f.Delimiter)) {
		return fmt.Errorf("sync: invalid folder name %q", newName)
	}
	if f.Role == domain.RoleInbox {
		return errors.New("sync: the inbox cannot be renamed")
	}
	remote := newName
	if f.Delimiter != "" {
		if i := strings.LastIndex(f.RemoteID, f.Delimiter); i >= 0 {
			remote = f.RemoteID[:i+len(f.Delimiter)] + newName
		}
	}
	tok := a.protect(f.AccountID, protection{folders: []int64{f.ID}, remoteFolders: []string{f.RemoteID}})
	if err := m.st.UpdateFolderPath(ctx, f.ID, remote, newName, f.ParentID); err != nil {
		a.release(f.AccountID, tok)
		return err
	}
	if _, err := a.enqueue(ctx, f.AccountID, OpFolderRename, opPayload{Folder: f.ID, Name: newName, OldRemote: f.RemoteID,
		OldName: f.Name, OldParent: f.ParentID}, tok); err != nil {
		return err
	}
	m.publish(ctx, folderListEvent(f.AccountID))
	return nil
}

// DeleteFolder deletes a folder and its subfolders; their messages become
// orphaned locally.
func (a *Actions) DeleteFolder(ctx context.Context, folderID int64) error {
	m := a.m
	f, err := a.folder(ctx, folderID)
	if err != nil {
		return err
	}
	if f.Role == domain.RoleInbox {
		return errors.New("sync: the inbox cannot be deleted")
	}
	tok := a.protect(f.AccountID, protection{remoteFolders: []string{f.RemoteID}})
	if err := m.st.DeleteFolder(ctx, f.ID); err != nil {
		a.release(f.AccountID, tok)
		return err
	}
	if _, err := a.enqueue(ctx, f.AccountID, OpFolderDelete, opPayload{Remote: f.RemoteID}, tok); err != nil {
		return err
	}
	m.publish(ctx, folderListEvent(f.AccountID))
	return nil
}
