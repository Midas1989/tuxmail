// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Mails kept with header only ("Nur Kopf", mail.headersOnlyOlderThanDays):
// mails older than the day count are stored with header, flags, attachment
// names and a short preview; the body is loaded on opening (FetchBody).
// Bodies already stored are released bit by bit in the background when the
// day count is switched on or made smaller. A body is only ever released if
// it can be loaded again from the server (store.ReleasableBodies), never
// for POP3, local folders, drafts, imports or encrypted mails.

// partialPreviewBytes is how much of the body is requested for a preview.
const partialPreviewBytes = 16 << 10

// previewLen is the length of a preview in runes (like the body snippet).
const previewLen = 200

// previewOf derives the preview of a mail stored without body from what the
// provider delivered with the header data.
func (m *Manager) previewOf(rm domain.RemoteMessage) string {
	if p := cleanPreview(rm.Preview); p != "" {
		return p
	}
	if len(rm.Partial) > 0 {
		return cleanPreview(m.extract(rm.Partial).Snippet)
	}
	return ""
}

// cleanPreview collapses whitespace and bounds the length.
func cleanPreview(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if utf8.RuneCountInString(s) <= previewLen {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:previewLen]), " ,.;:") + "…"
}

// HeadersOnlyChanged tells the workers that the global day count of this
// device changed: they download or release bodies accordingly.
func (m *Manager) HeadersOnlyChanged() {
	m.pokeAll()
	m.syncAllNow()
}

// syncAllNow asks every worker for a full sync (the release of bodies only
// considers folders synced without error in the same cycle).
func (m *Manager) syncAllNow() {
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	for _, w := range ws {
		w.requestSync()
	}
}

// pokeAll wakes every worker.
func (m *Manager) pokeAll() {
	m.mu.Lock()
	ws := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	for _, w := range ws {
		w.mu.Lock()
		w.moreBodies = true
		w.mu.Unlock()
		w.poke()
	}
}

// backfillPreviews requests the preview of header-only mails that have none
// (providers implementing domain.PartialFetcher, i.e. IMAP). It reports
// whether more remain.
func (w *Worker) backfillPreviews(ctx context.Context) (bool, error) {
	rule := w.contentRule()
	cut := rule.cutoff(w.m.now())
	if cut.IsZero() || !rule.release {
		return false, nil // only with a day count set by the user: no extra requests otherwise
	}
	var pf domain.PartialFetcher
	if err := w.withProvider(ctx, func(p domain.MailProvider) error {
		pf, _ = p.(domain.PartialFetcher)
		return nil
	}); err != nil || pf == nil {
		return false, err
	}
	limit := w.m.cfg.BackfillBatch * 4
	tasks, err := w.m.st.MessagesNeedingPreview(ctx, w.acc.ID, cut, placeholderPrefix, limit)
	if err != nil || len(tasks) == 0 {
		return false, err
	}
	byFolder := map[string][]store.PreviewTask{}
	var order []string
	for _, t := range tasks {
		if _, ok := byFolder[t.FolderRemoteID]; !ok {
			order = append(order, t.FolderRemoteID)
		}
		byFolder[t.FolderRemoteID] = append(byFolder[t.FolderRemoteID], t)
	}
	convs := map[int64]bool{}
	for _, folder := range order {
		ts := byFolder[folder]
		ids := make([]string, len(ts))
		for i, t := range ts {
			ids[i] = t.RemoteRef
		}
		var got map[string][]byte
		err := w.withProvider(ctx, func(p domain.MailProvider) error {
			var err error
			got, err = pf.FetchPartial(ctx, folder, ids, partialPreviewBytes)
			return err
		})
		if err != nil {
			if ctx.Err() != nil || IsNetworkError(err) {
				return false, err
			}
			w.log.Info("sync: fetching previews failed", "folder", folder, "err", err)
			got = nil // mark as tried below: no endless retries
		}
		for _, t := range ts {
			p := ""
			if raw, ok := got[t.RemoteRef]; ok {
				p = cleanPreview(w.m.extract(raw).Snippet)
			}
			if err := w.m.st.SetMessagePreview(ctx, t.MessageID, p); err != nil {
				return false, err
			}
			if p == "" {
				continue
			}
			msg, err := w.m.st.GetMessage(ctx, t.MessageID)
			if err != nil {
				continue
			}
			if err := w.m.indexMessage(ctx, msg, nil); err != nil {
				return false, err
			}
			convs[msg.ConversationID] = true
		}
	}
	w.m.refreshConvs(ctx, convs)
	return len(tasks) >= limit, nil
}

func (m *Manager) refreshConvs(ctx context.Context, set map[int64]bool) {
	ids := make([]int64, 0, len(set))
	for id := range set {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := m.st.RefreshConversations(ctx, ids...); err != nil {
		m.log.Warn("sync: refresh conversations", "err", err)
		return
	}
	m.publishConversations(ctx, ids...)
}

// releaseBodies releases a batch of bodies of mails older than the day
// count. Only mails located in folders synced without error in this cycle
// count, and only after the server confirmed that they still exist there
// (domain.ExistenceChecker, same UIDVALIDITY); providers that cannot confirm
// never release. It reports whether more remain.
func (w *Worker) releaseBodies(ctx context.Context, syncedOK []int64) (bool, error) {
	rule := w.contentRule()
	if !rule.release || len(syncedOK) == 0 {
		return false, nil
	}
	cfg := &w.m.cfg
	now := w.m.now()
	cands, err := w.m.st.ReleasableBodies(ctx, w.acc.ID, rule.cutoff(now), now.Add(-cfg.ReleaseGrace), cfg.MaxBodySize, syncedOK, cfg.ReleaseBatch)
	if err != nil || len(cands) == 0 {
		return false, err
	}
	var ec domain.ExistenceChecker
	if err := w.withProvider(ctx, func(p domain.MailProvider) error {
		ec, _ = p.(domain.ExistenceChecker)
		return nil
	}); err != nil || ec == nil {
		return false, err
	}
	byFolder := map[int64][]store.ReleaseCandidate{}
	var order []int64
	for _, c := range cands {
		if w.isProtectedMsg(c.MessageID) {
			continue // an operation on its way to the server: later
		}
		if _, ok := byFolder[c.FolderID]; !ok {
			order = append(order, c.FolderID)
		}
		byFolder[c.FolderID] = append(byFolder[c.FolderID], c)
	}
	// Two steps: a mail confirmed in this cycle is released only in the next
	// one, after its folder was synced again (deletions in between turn it
	// orphaned first) and the server confirmed it once more.
	w.mu.Lock()
	ready := w.releaseReady
	w.mu.Unlock()
	next := map[int64]string{}
	var resync []int64
	released := 0
	for _, fid := range order {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		batch := byFolder[fid]
		state, err := w.m.st.FolderSyncState(ctx, fid)
		if err != nil || strings.HasPrefix(string(state), partialPrefix) || state == "" {
			continue // initial sync not complete: nothing is confirmed
		}
		ids := make([]string, len(batch))
		for i, c := range batch {
			ids[i] = c.RemoteRef
		}
		var exists map[string]bool
		if err := w.withProvider(ctx, func(p domain.MailProvider) error {
			var err error
			exists, err = ec.ConfirmExisting(ctx, batch[0].FolderRemoteID, state, ids)
			return err
		}); err != nil {
			if ctx.Err() != nil || IsNetworkError(err) {
				return false, err
			}
			w.log.Info("sync: confirming messages before releasing failed", "folder", batch[0].FolderRemoteID, "err", err)
			continue
		}
		marked := false
		for _, c := range batch {
			if !exists[c.RemoteRef] {
				continue // gone or moved on the server: the sync handles it, the body stays
			}
			if ready[c.MessageID] != c.BlobHash {
				next[c.MessageID] = c.BlobHash
				marked = true
				continue
			}
			ok, err := w.m.releaseBody(ctx, c)
			if err != nil {
				return false, err
			}
			if ok {
				released++
			}
		}
		if marked {
			resync = append(resync, fid)
		}
	}
	w.mu.Lock()
	w.releaseReady = next
	w.mu.Unlock()
	for _, fid := range resync {
		w.markDirty(fid) // the next cycle syncs the folder, then releases
	}
	if released > 0 {
		w.log.Info("sync: released bodies of older mails", "count", released)
	}
	return len(cands) >= cfg.ReleaseBatch && released > 0, nil
}

// releaseBody forgets the stored body of one message: header, preview,
// attachment names and the index entry of header and preview stay. A body
// that turns out to be encrypted is kept for good.
func (m *Manager) releaseBody(ctx context.Context, c store.ReleaseCandidate) (bool, error) {
	m.blobMu.Lock()
	defer m.blobMu.Unlock()
	enc, size, err := m.blobEncrypted(c.BlobHash)
	if err != nil {
		m.log.Info("sync: checking a body before releasing it failed", "message", c.MessageID, "err", err)
		return false, m.st.KeepBody(ctx, c.MessageID)
	}
	if enc || size > m.cfg.MaxBodySize {
		// Encrypted, or too large to be loaded again (sync limit): kept for good.
		return false, m.st.KeepBody(ctx, c.MessageID)
	}
	ok, err := m.st.ReleaseBody(ctx, c.MessageID, c.BlobHash)
	if err != nil || !ok {
		return false, err
	}
	msg, err := m.st.GetMessage(ctx, c.MessageID)
	if err != nil {
		return true, nil
	}
	if err := m.indexMessage(ctx, msg, nil); err != nil {
		return true, err
	}
	un, err := m.st.UnreferencedBlobs(ctx, []string{c.BlobHash})
	if err != nil {
		return true, err
	}
	for _, h := range un {
		if err := m.cfg.Blobs.Delete(h); err != nil {
			m.log.Info("sync: deleting a released body failed", "err", err)
		}
	}
	return true, nil
}

// encryptedMarkers identify mails that are (or contain) encrypted content:
// their body is never released, also if they were decrypted locally.
var encryptedMarkers = [][]byte{
	[]byte("-----begin pgp message-----"),
	[]byte("application/pgp-encrypted"),
	[]byte("application/pkcs7-mime"),
	[]byte("application/x-pkcs7-mime"),
	[]byte("smime.p7m"),
}

// blobEncrypted scans a stored body for encrypted parts.
func (m *Manager) blobEncrypted(hash string) (bool, int64, error) {
	rc, err := m.cfg.Blobs.Open(hash)
	if err != nil {
		return false, 0, err
	}
	defer rc.Close()
	cr := &countingReader{r: rc}
	enc, err := looksEncrypted(cr)
	if err != nil || enc {
		return enc, cr.n, err
	}
	return false, cr.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func looksEncrypted(r io.Reader) (bool, error) {
	const overlap = 64
	br := bufio.NewReaderSize(r, 64<<10)
	buf := make([]byte, 0, 64<<10+overlap)
	chunk := make([]byte, 64<<10)
	for {
		n, err := br.Read(chunk)
		if n > 0 {
			buf = append(buf, bytes.ToLower(chunk[:n])...)
			for _, mk := range encryptedMarkers {
				if bytes.Contains(buf, mk) {
					return true, nil
				}
			}
			if len(buf) > overlap {
				buf = append(buf[:0], buf[len(buf)-overlap:]...)
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// ServerSearch asks the servers of the accounts (every running worker if
// accountIDs is empty) for mails containing text and returns the ids of the
// local mails kept with header only among the matches: their body is not
// in the local index. Only accounts that keep older mails with header only
// and providers that can search (domain.TextSearcher) are asked; it is
// best effort and stops quietly at ctx (a timeout of the caller).
func (m *Manager) ServerSearch(ctx context.Context, accountIDs []int64, text string, limit int) []int64 {
	m.mu.Lock()
	var ws []*Worker
	for id, w := range m.workers {
		if len(accountIDs) == 0 || slices.Contains(accountIDs, id) {
			ws = append(ws, w)
		}
	}
	m.mu.Unlock()
	sort.Slice(ws, func(i, j int) bool { return ws[i].acc.ID < ws[j].acc.ID })
	var out []int64
	seen := map[int64]bool{}
	for _, w := range ws {
		if ctx.Err() != nil || len(out) >= limit {
			break
		}
		if r := w.contentRule(); r.cutoff(m.now()).IsZero() || !r.release {
			continue // everything is stored and indexed locally, or no day count set
		}
		for _, id := range w.serverSearch(ctx, text, limit-len(out)) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func (w *Worker) serverSearch(ctx context.Context, text string, limit int) []int64 {
	folders, err := w.m.st.ListFolders(ctx, w.acc.ID)
	if err != nil {
		return nil
	}
	sort.SliceStable(folders, func(i, j int) bool { return rolePriority(folders[i].Role) < rolePriority(folders[j].Role) })
	var out []int64
	for _, f := range folders {
		if ctx.Err() != nil || len(out) >= limit {
			break
		}
		if domain.IsLocalFolder(f.RemoteID) {
			continue
		}
		var ids []string
		// The provider is shared with the sync: do not wait beyond ctx for it.
		done := make(chan error, 1)
		go func() {
			done <- w.withProvider(ctx, func(p domain.MailProvider) error {
				ts, ok := p.(domain.TextSearcher)
				if !ok {
					return errNoTextSearch
				}
				var err error
				ids, err = ts.SearchText(ctx, f.RemoteID, text, limit)
				return err
			})
		}()
		select {
		case <-ctx.Done():
			return out
		case err := <-done:
			if errors.Is(err, errNoTextSearch) {
				return out
			}
			if err != nil {
				w.log.Debug("sync: server search", "folder", f.RemoteID, "err", err)
				continue
			}
		}
		for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- { // newest first
			msg, err := w.m.st.FindMessageByLocation(ctx, f.ID, ids[i])
			if err == nil && msg.BlobHash == "" && msg.State == store.StateLive {
				out = append(out, msg.ID)
			}
		}
	}
	return out
}

var errNoTextSearch = errors.New("sync: provider cannot search text")
