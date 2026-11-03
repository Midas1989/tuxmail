// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Sync computes the changes in folderRemoteID since state.
//
// Algorithm:
//   - SELECT (with CONDSTORE when available). A changed UIDVALIDITY (or an
//     unreadable state) yields FullResync and a fresh initial sync.
//   - If UIDNEXT and the message count are unchanged, the UID set is known
//     to be unchanged; otherwise UID SEARCH ALL yields the current UID set,
//     whose difference to the stored set gives Added and Removed.
//   - Added messages are fetched with envelope, flags, size, internal date,
//     body structure and the threading headers; OBJECTID / X-GM-* IDs are
//     added when the server supports them.
//   - Flag changes come from FETCH (CHANGEDSINCE modseq) when CONDSTORE is
//     usable, else from a FLAGS fetch over the known UID set compared with
//     the stored flag fingerprints.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	var delta domain.SyncDelta
	prev, err := decodeState(state)
	if err != nil {
		p.log.Warn("imap: discarding unreadable sync state", "folder", folderRemoteID, "err", err)
		prev = &folderState{}
		delta.FullResync = true
	}
	var next *folderState
	err = p.do(ctx, func(c *imapclient.Client) error {
		var err error
		next, err = p.syncLocked(ctx, c, folderRemoteID, prev, &delta)
		return err
	})
	if err != nil {
		return domain.SyncDelta{}, state, err
	}
	return delta, next.encode(), nil
}

func (p *Provider) syncLocked(ctx context.Context, c *imapclient.Client, folder string, prev *folderState, delta *domain.SyncDelta) (*folderState, error) {
	condstore := p.caps.Has(imap.CapCondStore)
	sel, err := selectMailbox(c, folder, condstore)
	if err != nil {
		return nil, err
	}
	if prev.UIDValidity != 0 && prev.UIDValidity != sel.UIDValidity {
		p.log.Info("imap: UIDVALIDITY changed, full resync", "folder", folder)
		delta.FullResync = true
		prev = &folderState{}
	}
	initial := prev.UIDValidity == 0
	useModSeq := condstore && sel.HighestModSeq > 0

	// 1. Current UID set.
	var cur []uint32
	switch {
	case sel.NumMessages == 0:
	case !initial && sel.UIDNext != 0 && uint32(sel.UIDNext) == prev.UIDNext && int(sel.NumMessages) == len(prev.UIDs):
		cur = append([]uint32(nil), prev.UIDs...)
	default:
		opts := &imap.SearchOptions{}
		if c.Caps().Has(imap.CapESearch) {
			opts.ReturnAll = true
		}
		data, err := c.UIDSearch(&imap.SearchCriteria{}, opts).Wait()
		if err != nil {
			return nil, fmt.Errorf("imap: search %q: %w", folder, err)
		}
		for _, u := range data.AllUIDs() {
			cur = append(cur, uint32(u))
		}
		sort.Slice(cur, func(i, j int) bool { return cur[i] < cur[j] })
	}

	// 2. Diff against the known set.
	var added, kept []uint32
	curSet := make(map[uint32]struct{}, len(cur))
	for _, u := range cur {
		curSet[u] = struct{}{}
		if prev.index(u) < 0 {
			added = append(added, u)
		} else {
			kept = append(kept, u)
		}
	}
	for _, u := range prev.UIDs {
		if _, ok := curSet[u]; !ok {
			delta.Removed = append(delta.Removed, ref(folder, u))
		}
	}

	prints := make(map[uint32]flagPrint, len(cur))

	// 3. New messages.
	gotAdded := make(map[uint32]bool, len(added))
	for _, batch := range batches(added, p.cfg.FetchBatchSize) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msgs, err := c.Fetch(uidSet(batch), headerFetchOptions()).Collect()
		if err != nil {
			return nil, fmt.Errorf("imap: fetch headers: %w", err)
		}
		var uids []uint32
		start := len(delta.Added)
		for _, m := range msgs {
			if m.UID == 0 {
				continue
			}
			u := uint32(m.UID)
			gotAdded[u] = true
			prints[u] = fingerprint(m.Flags)
			delta.Added = append(delta.Added, toRemoteMessage(folder, m))
			uids = append(uids, u)
		}
		if ids := p.serverIDsLocked(ctx, folder, uids); len(ids) > 0 {
			for i := start; i < len(delta.Added); i++ {
				u, _ := strconv.ParseUint(delta.Added[i].Ref.ID, 10, 32)
				if id, ok := ids[uint32(u)]; ok {
					delta.Added[i].Header.ServerObjectID = id.object
					delta.Added[i].Header.ServerThreadID = id.thread
				}
			}
		}
	}
	sort.Slice(delta.Added, func(i, j int) bool { return uidOf(delta.Added[i].Ref) < uidOf(delta.Added[j].Ref) })

	// 4. Flag changes of known messages.
	keptOK := make(map[uint32]bool, len(kept))
	switch {
	case initial || len(kept) == 0:
	case useModSeq && prev.ModSeq > 0:
		for _, u := range kept {
			keptOK[u] = true
		}
		if sel.HighestModSeq != prev.ModSeq {
			set := imap.UIDSet{}
			set.AddRange(imap.UID(kept[0]), imap.UID(kept[len(kept)-1]))
			msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, ChangedSince: prev.ModSeq}).Collect()
			if err != nil {
				return nil, fmt.Errorf("imap: fetch changed flags: %w", err)
			}
			for _, m := range msgs {
				u := uint32(m.UID)
				if prev.index(u) < 0 || m.UID == 0 {
					continue
				}
				if _, ok := curSet[u]; !ok {
					continue
				}
				delta.FlagsChanged = append(delta.FlagsChanged, domain.RemoteMessage{Ref: ref(folder, u), Flags: convFlags(m.Flags)})
			}
		}
	default:
		for _, batch := range batches(kept, 5000) {
			msgs, err := c.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, Flags: true}).Collect()
			if err != nil {
				return nil, fmt.Errorf("imap: fetch flags: %w", err)
			}
			for _, m := range msgs {
				u := uint32(m.UID)
				i := prev.index(u)
				if i < 0 {
					continue
				}
				keptOK[u] = true
				fp := fingerprint(m.Flags)
				prints[u] = fp
				if prev.Flags == nil || prev.Flags[i] != fp {
					delta.FlagsChanged = append(delta.FlagsChanged, domain.RemoteMessage{Ref: ref(folder, u), Flags: convFlags(m.Flags)})
				}
			}
		}
		// Kept messages without a FETCH response were expunged meanwhile.
		for _, u := range kept {
			if !keptOK[u] {
				delta.Removed = append(delta.Removed, ref(folder, u))
			}
		}
	}

	// 5. New state.
	next := &folderState{UIDValidity: sel.UIDValidity, UIDNext: uint32(sel.UIDNext)}
	if useModSeq {
		next.ModSeq = sel.HighestModSeq
	}
	for _, u := range cur {
		if !gotAdded[u] && !keptOK[u] {
			continue // vanished between SEARCH and FETCH
		}
		next.UIDs = append(next.UIDs, u)
		if !useModSeq {
			next.Flags = append(next.Flags, prints[u])
		}
	}
	if next.UIDNext == 0 && len(cur) > 0 {
		next.UIDNext = cur[len(cur)-1] + 1
	}
	if !useModSeq && next.Flags == nil {
		next.Flags = []flagPrint{}
	}
	return next, nil
}

func ref(folder string, uid uint32) domain.RemoteRef {
	return domain.RemoteRef{FolderRemoteID: folder, ID: strconv.FormatUint(uint64(uid), 10)}
}

func uidOf(r domain.RemoteRef) uint32 {
	u, _ := strconv.ParseUint(r.ID, 10, 32)
	return uint32(u)
}

func uidSet(uids []uint32) imap.UIDSet {
	var s imap.UIDSet
	for _, u := range uids {
		s.AddNum(imap.UID(u))
	}
	return s
}

func batches(l []uint32, n int) [][]uint32 {
	var out [][]uint32
	for len(l) > 0 {
		k := min(n, len(l))
		out = append(out, l[:k])
		l = l[k:]
	}
	return out
}
