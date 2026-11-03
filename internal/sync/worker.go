// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Worker syncs one account. All provider calls go through withProvider and
// are serialized, so a provider instance is never used concurrently (except
// for its Watch connections, which are independent by contract).
type Worker struct {
	m      *Manager
	acc    domain.Account
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{} // new work (ops, hints, requests)
	retry  chan struct{} // cut a backoff short (SyncNow)
	wg     stdsync.WaitGroup

	provMu stdsync.Mutex
	prov   domain.MailProvider

	mu          stdsync.Mutex
	status      AccountStatus
	dirty       map[int64]bool // folders to sync soon
	syncAll     bool
	listFolders bool
	lastSync    map[int64]time.Time
	lastList    time.Time
	watching    map[int64]bool
	protect     map[int64]protection // by op id (negative: not yet enqueued)
	nextToken   int64
	bodyFailed  map[int64]bool
	moreBodies  bool
}

// protection describes what a pending operation changed locally, so that a
// concurrent sync does not undo it before the operation reached the server.
type protection struct {
	msgs          []int64
	folders       []int64  // local folder ids (pending create/rename)
	remoteFolders []string // remote ids (pending rename/delete)
}

func newWorker(m *Manager, a domain.Account) *Worker {
	return &Worker{
		m: m, acc: a, log: m.log.With("account", a.ID),
		done: make(chan struct{}), wake: make(chan struct{}, 1), retry: make(chan struct{}, 1),
		status:   AccountStatus{AccountID: a.ID, Name: a.DisplayName, State: StateConnecting},
		dirty:    map[int64]bool{},
		lastSync: map[int64]time.Time{}, watching: map[int64]bool{}, protect: map[int64]protection{},
		bodyFailed:  map[int64]bool{},
		listFolders: true,
	}
}

func (w *Worker) stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *Worker) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) requestSync(folderIDs ...int64) {
	w.mu.Lock()
	if len(folderIDs) == 0 {
		w.syncAll, w.listFolders = true, true
	}
	for _, id := range folderIDs {
		w.dirty[id] = true
	}
	w.mu.Unlock()
	select {
	case w.retry <- struct{}{}:
	default:
	}
	w.poke()
}

func (w *Worker) markDirty(folderID int64) {
	w.mu.Lock()
	w.dirty[folderID] = true
	w.mu.Unlock()
	w.poke()
}

// Status returns a snapshot of the worker's status.
func (w *Worker) Status() AccountStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *Worker) setStatus(fn func(st *AccountStatus)) {
	w.mu.Lock()
	old := w.status
	fn(&w.status)
	st := w.status
	w.mu.Unlock()
	if st.State != old.State || st.Folder != old.Folder || st.LastError != old.LastError || !st.NextRetry.Equal(old.NextRetry) {
		w.m.publishStatus(st)
	}
}

func (w *Worker) setState(s State, folder string) {
	w.setStatus(func(st *AccountStatus) {
		st.State, st.Folder = s, folder
		if s != StateError {
			st.NextRetry = time.Time{}
		}
	})
}

// ==== protections ==============================================================

func (w *Worker) protectNew(p protection) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextToken--
	w.protect[w.nextToken] = p
	return w.nextToken
}

func (w *Worker) protectRekey(token, opID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.protect[token]; ok {
		delete(w.protect, token)
		if opID != 0 {
			w.protect[opID] = p
		}
	}
}

func (w *Worker) unprotect(opID int64) {
	w.mu.Lock()
	delete(w.protect, opID)
	w.mu.Unlock()
}

func (w *Worker) isProtectedMsg(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, p := range w.protect {
		for _, m := range p.msgs {
			if m == id {
				return true
			}
		}
	}
	return false
}

func (w *Worker) protectedFolders() (local map[int64]bool, remote map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	local, remote = map[int64]bool{}, map[string]bool{}
	for _, p := range w.protect {
		for _, f := range p.folders {
			local[f] = true
		}
		for _, r := range p.remoteFolders {
			remote[r] = true
		}
	}
	return local, remote
}

// loadProtections registers the pending operations of the account (queued
// by an earlier run or while the worker was stopped).
func (w *Worker) loadProtections(ctx context.Context) {
	ops, err := w.m.st.ListOps(ctx, store.OpPending)
	if err != nil {
		w.log.Warn("sync: list pending ops", "err", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, op := range ops {
		if op.AccountID != w.acc.ID || !isSyncOp(op.Kind) {
			continue
		}
		p, err := decodePayload(op.Payload)
		if err != nil {
			continue
		}
		w.protect[op.ID] = p.protection(op.Kind)
	}
}

// ==== provider =================================================================

// withProvider runs fn with the connected provider, building it on first use.
func (w *Worker) withProvider(ctx context.Context, fn func(p domain.MailProvider) error) error {
	w.provMu.Lock()
	defer w.provMu.Unlock()
	if w.prov == nil {
		p, err := w.m.cfg.Providers.Build(ctx, w.acc, w.m.cfg.Credentials, w.m.cfg.ProviderOptions)
		if err != nil {
			return err
		}
		w.prov = p
	}
	return fn(w.prov)
}

func (w *Worker) closeProvider() {
	w.provMu.Lock()
	defer w.provMu.Unlock()
	if w.prov != nil {
		if err := w.prov.Close(); err != nil {
			w.log.Debug("sync: close provider", "err", err)
		}
		w.prov = nil
	}
}

// ==== main loop ================================================================

func (w *Worker) run(ctx context.Context) {
	defer func() {
		w.wg.Wait() // watchers
		w.closeProvider()
	}()
	w.loadProtections(ctx)
	w.m.publishStatus(w.Status())
	bo := Backoff{Min: w.m.cfg.MinBackoff, Max: w.m.cfg.MaxBackoff}
	for ctx.Err() == nil {
		err := w.cycle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			d := bo.Next()
			retryAt := w.m.now().Add(d)
			w.setStatus(func(st *AccountStatus) {
				st.State, st.Folder, st.LastError, st.LastErrorAt, st.NextRetry = StateError, "", err.Error(), w.m.now(), retryAt
			})
			w.log.Warn("sync: cycle failed", "err", err, "retry_in", d.Round(time.Millisecond), "network", IsNetworkError(err))
			if !w.sleep(ctx, d) {
				return
			}
			w.setState(StateConnecting, "")
			continue
		}
		bo.Reset()
		w.setStatus(func(st *AccountStatus) {
			st.State, st.Folder, st.LastError, st.NextRetry, st.LastSync = StateIdle, "", "", time.Time{}, w.m.now()
		})
		w.wait(ctx)
	}
}

// sleep waits d (or until a retry is requested); false if ctx ended.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-w.retry:
	}
	return true
}

// wait blocks until new work arrives or the next scheduled sync is due.
func (w *Worker) wait(ctx context.Context) {
	d := w.nextDue(ctx)
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-w.wake:
	case <-w.retry:
	}
}

func (w *Worker) nextDue(ctx context.Context) time.Duration {
	now := w.m.now()
	cfg := &w.m.cfg
	w.mu.Lock()
	if len(w.dirty) > 0 || w.syncAll || w.listFolders || w.moreBodies {
		w.mu.Unlock()
		return 0
	}
	next := w.lastList.Add(cfg.FolderListInterval)
	for _, t := range w.lastSync {
		if n := t.Add(cfg.PollInterval); n.Before(next) {
			next = n
		}
	}
	w.mu.Unlock()
	if due, ok, err := w.m.st.NextAccountOpDue(ctx, w.acc.ID, opKinds...); err == nil && ok && due.Before(next) {
		next = due
	}
	// Op due times have second resolution.
	return max(next.Sub(now), 0) + 5*time.Millisecond
}

// cycle runs one round: folder list, operations, tag sync, due folders and
// a batch of body downloads.
func (w *Worker) cycle(ctx context.Context) error {
	w.setState(StateSyncing, "")
	w.mu.Lock()
	needList := w.listFolders || w.m.now().Sub(w.lastList) >= w.m.cfg.FolderListInterval
	w.mu.Unlock()
	if needList {
		if err := w.syncFolderList(ctx); err != nil {
			return err
		}
	}
	if err := w.runOps(ctx); err != nil {
		return err
	}
	if err := w.pushTags(ctx); err != nil {
		return err
	}
	w.ensureWatches(ctx)

	folders, err := w.dueFolders(ctx)
	if err != nil {
		return err
	}
	for _, f := range folders {
		w.setState(StateSyncing, f.RemoteID)
		err := w.syncFolder(ctx, f)
		w.mu.Lock()
		if err == nil || !IsNetworkError(err) {
			w.lastSync[f.ID] = w.m.now()
		}
		w.mu.Unlock()
		if err != nil {
			if ctx.Err() != nil || IsNetworkError(err) {
				w.markDirty(f.ID)
				return err
			}
			w.log.Warn("sync: folder sync failed", "folder", f.RemoteID, "err", err)
		}
		// Keep user operations responsive during long syncs.
		if err := w.runOps(ctx); err != nil {
			return err
		}
	}
	w.setState(StateSyncing, "")
	more, err := w.backfillBodies(ctx)
	w.mu.Lock()
	w.moreBodies = more
	w.mu.Unlock()
	return err
}

// rolePriority orders folders: Inbox, Sent, Drafts, Archive, others, Junk, Trash.
func rolePriority(r domain.FolderRole) int {
	switch r {
	case domain.RoleInbox:
		return 0
	case domain.RoleSent:
		return 1
	case domain.RoleDrafts:
		return 2
	case domain.RoleArchive:
		return 3
	case domain.RoleNone:
		return 4
	case domain.RoleJunk:
		return 5
	case domain.RoleTrash:
		return 6
	}
	return 4
}

func (w *Worker) dueFolders(ctx context.Context) ([]domain.Folder, error) {
	all, err := w.m.st.ListFolders(ctx, w.acc.ID)
	if err != nil {
		return nil, err
	}
	now := w.m.now()
	localProt, _ := w.protectedFolders()
	w.mu.Lock()
	var out []domain.Folder
	for _, f := range all {
		if localProt[f.ID] {
			continue // not on the server yet
		}
		last, synced := w.lastSync[f.ID]
		if w.syncAll || w.dirty[f.ID] || !synced || now.Sub(last) >= w.m.cfg.PollInterval {
			out = append(out, f)
			delete(w.dirty, f.ID)
		}
	}
	// Dirty entries of unknown folders are dropped.
	for id := range w.dirty {
		known := false
		for _, f := range all {
			known = known || f.ID == id
		}
		if !known {
			delete(w.dirty, id)
		}
	}
	w.syncAll = false
	w.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := rolePriority(out[i].Role), rolePriority(out[j].Role)
		if pi != pj {
			return pi < pj
		}
		return out[i].RemoteID < out[j].RemoteID
	})
	return out, nil
}

// ==== folder list ==============================================================

func (w *Worker) syncFolderList(ctx context.Context) error {
	var remote []domain.Folder
	if err := w.withProvider(ctx, func(p domain.MailProvider) error {
		var err error
		remote, err = p.ListFolders(ctx)
		return err
	}); err != nil {
		return fmt.Errorf("list folders: %w", err)
	}
	st := w.m.st
	local, err := st.ListFolders(ctx, w.acc.ID)
	if err != nil {
		return err
	}
	localByRemote := map[string]domain.Folder{}
	for _, f := range local {
		localByRemote[f.RemoteID] = f
	}
	localProt, remoteProt := w.protectedFolders()

	// Parents first so that their ids are known.
	sort.SliceStable(remote, func(i, j int) bool { return depth(remote[i]) < depth(remote[j]) })
	ids := map[string]int64{}
	changed := false
	for _, f := range remote {
		if remoteProt[f.RemoteID] {
			continue
		}
		f.AccountID = w.acc.ID
		if p := parentPath(f); p != "" {
			f.ParentID = ids[p]
		}
		old, exists := localByRemote[f.RemoteID]
		if exists {
			// Counters are maintained locally once the folder is synced.
			f.Total, f.Unread = old.Total, old.Unread
		}
		id, err := st.UpsertFolder(ctx, f)
		if err != nil {
			return err
		}
		ids[f.RemoteID] = id
		if !exists || old.Name != f.Name || old.Role != f.Role || old.ParentID != f.ParentID {
			changed = true
		}
	}
	for _, f := range local {
		if _, ok := ids[f.RemoteID]; ok || localProt[f.ID] || remoteProt[f.RemoteID] {
			continue
		}
		w.log.Info("sync: folder vanished on server", "folder", f.RemoteID)
		if err := st.DeleteFolder(ctx, f.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		w.mu.Lock()
		delete(w.lastSync, f.ID)
		w.mu.Unlock()
		changed = true
	}
	w.mu.Lock()
	w.listFolders, w.lastList = false, w.m.now()
	w.mu.Unlock()
	if changed {
		w.m.publish(ctx, folderListEvent(w.acc.ID))
	}
	return nil
}

func depth(f domain.Folder) int {
	if f.Delimiter == "" {
		return 0
	}
	return strings.Count(f.RemoteID, f.Delimiter)
}

func parentPath(f domain.Folder) string {
	if f.Delimiter == "" {
		return ""
	}
	i := strings.LastIndex(f.RemoteID, f.Delimiter)
	if i <= 0 {
		return ""
	}
	return f.RemoteID[:i]
}

// ==== watching =================================================================

func (w *Worker) ensureWatches(ctx context.Context) {
	if len(w.m.cfg.WatchRoles) == 0 {
		return
	}
	folders, err := w.m.st.ListFolders(ctx, w.acc.ID)
	if err != nil {
		return
	}
	for _, f := range folders {
		watch := false
		for _, r := range w.m.cfg.WatchRoles {
			watch = watch || (r == f.Role && r != domain.RoleNone)
		}
		w.mu.Lock()
		active := w.watching[f.ID]
		w.mu.Unlock()
		if !watch || active {
			continue
		}
		var ch <-chan domain.ChangeHint
		err := w.withProvider(ctx, func(p domain.MailProvider) error {
			var err error
			ch, err = p.Watch(ctx, f.RemoteID)
			return err
		})
		if err != nil {
			w.log.Info("sync: watch failed", "folder", f.RemoteID, "err", err)
			continue
		}
		w.mu.Lock()
		w.watching[f.ID] = true
		w.mu.Unlock()
		w.wg.Add(1)
		go w.watchLoop(ctx, f.ID, ch)
	}
}

// watchLoop debounces change hints into folder syncs.
func (w *Worker) watchLoop(ctx context.Context, folderID int64, ch <-chan domain.ChangeHint) {
	defer w.wg.Done()
	defer func() {
		w.mu.Lock()
		delete(w.watching, folderID)
		w.mu.Unlock()
	}()
	var timer *time.Timer
	var fire <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if timer == nil {
				timer = time.NewTimer(w.m.cfg.Debounce)
			} else {
				timer.Reset(w.m.cfg.Debounce)
			}
			fire = timer.C
		case <-fire:
			fire = nil
			w.markDirty(folderID)
		}
	}
}
