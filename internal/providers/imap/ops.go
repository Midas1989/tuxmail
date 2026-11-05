// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"sort"
	"strconv"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// maxAppendSize bounds messages read into memory by Append.
const maxAppendSize = 512 << 20

func parseUID(r domain.RemoteRef) (imap.UID, error) {
	u, err := strconv.ParseUint(r.ID, 10, 32)
	if err != nil || u == 0 {
		return 0, fmt.Errorf("imap: invalid message reference %q", r.ID)
	}
	return imap.UID(u), nil
}

// group splits refs by folder, keeping the first-seen folder order.
func group(refs []domain.RemoteRef) (folders []string, byFolder map[string][]int, err error) {
	byFolder = make(map[string][]int)
	for i, r := range refs {
		if _, err := parseUID(r); err != nil {
			return nil, nil, err
		}
		if _, ok := byFolder[r.FolderRemoteID]; !ok {
			folders = append(folders, r.FolderRemoteID)
		}
		byFolder[r.FolderRemoteID] = append(byFolder[r.FolderRemoteID], i)
	}
	return folders, byFolder, nil
}

func refsUIDSet(refs []domain.RemoteRef, idx []int) imap.UIDSet {
	var s imap.UIDSet
	for _, i := range idx {
		u, _ := parseUID(refs[i])
		s.AddNum(u)
	}
	return s
}

// FetchRaw returns the complete RFC 5322 message. The message is buffered in
// memory so the shared connection is released immediately.
//
// The literal is read with a size limit (Config.MaxMessageSize) while it
// arrives; an oversized message aborts the connection and yields
// ErrMessageTooLarge instead of being buffered.
func (p *Provider) FetchRaw(ctx context.Context, r domain.RemoteRef) (io.ReadCloser, error) {
	return p.FetchRawLimit(ctx, r, p.cfg.MaxMessageSize)
}

// FetchRawLimit is FetchRaw with an explicit size limit in bytes (the
// smaller of limit and Config.MaxMessageSize applies; limit <= 0 means the
// configured maximum).
func (p *Provider) FetchRawLimit(ctx context.Context, r domain.RemoteRef, limit int64) (io.ReadCloser, error) {
	if limit <= 0 || limit > p.cfg.MaxMessageSize {
		limit = p.cfg.MaxMessageSize
	}
	uid, err := parseUID(r)
	if err != nil {
		return nil, err
	}
	var body []byte
	err = p.do(ctx, func(c *imapclient.Client) error {
		if err := ensureSelected(c, r.FolderRemoteID); err != nil {
			return err
		}
		section := &imap.FetchItemBodySection{Peek: true}
		cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
		defer cmd.Close()
		found := false
		for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
			for item := msg.Next(); item != nil; item = msg.Next() {
				if bs, ok := item.(imapclient.FetchItemDataBodySection); ok && bs.Literal != nil {
					b, ok, err := readLimited(bs.Literal, limit)
					if err != nil {
						return err
					}
					if !ok {
						// Do not drain the rest of the literal.
						hangUp(c, func() { _ = cmd.Close() })
						return fmt.Errorf("%w (limit %d bytes)", ErrMessageTooLarge, limit)
					}
					body, found = b, true
				}
			}
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("imap: fetch body: %w", err)
		}
		if !found {
			return ErrMessageNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// SetFlags adds and removes flags (UID STORE ... FLAGS.SILENT).
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	folders, byFolder, err := group(refs)
	if err != nil {
		return err
	}
	return p.do(ctx, func(c *imapclient.Client) error {
		for _, folder := range folders {
			if err := ensureSelected(c, folder); err != nil {
				return err
			}
			set := refsUIDSet(refs, byFolder[folder])
			if err := storeFlags(c, set, imap.StoreFlagsAdd, add); err != nil {
				return err
			}
			if err := storeFlags(c, set, imap.StoreFlagsDel, remove); err != nil {
				return err
			}
		}
		return nil
	})
}

func storeFlags(c *imapclient.Client, set imap.UIDSet, op imap.StoreFlagsOp, flags []domain.Flag) error {
	if len(flags) == 0 {
		return nil
	}
	err := c.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: toIMAPFlags(flags)}, nil).Close()
	if err != nil {
		return fmt.Errorf("imap: store flags: %w", err)
	}
	return nil
}

// Delete permanently removes the messages: \Deleted + UID EXPUNGE (UIDPLUS).
// Without UIDPLUS a plain EXPUNGE is only issued when no other message in
// the folder carries \Deleted; otherwise the messages stay flagged \Deleted.
// Moving to the trash folder is the caller's job (Move).
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	folders, byFolder, err := group(refs)
	if err != nil {
		return err
	}
	return p.do(ctx, func(c *imapclient.Client) error {
		for _, folder := range folders {
			if err := ensureSelected(c, folder); err != nil {
				return err
			}
			set := refsUIDSet(refs, byFolder[folder])
			if err := storeFlags(c, set, imap.StoreFlagsAdd, []domain.Flag{domain.FlagDeleted}); err != nil {
				return err
			}
			if err := p.expungeUIDs(c, set); err != nil {
				return err
			}
		}
		return nil
	})
}

// expungeUIDs expunges exactly set, never other \Deleted messages.
func (p *Provider) expungeUIDs(c *imapclient.Client, set imap.UIDSet) error {
	if c.Caps().Has(imap.CapUIDPlus) {
		if err := c.UIDExpunge(set).Close(); err != nil {
			return fmt.Errorf("imap: uid expunge: %w", err)
		}
		return nil
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.FlagDeleted}}, nil).Wait()
	if err != nil {
		return fmt.Errorf("imap: search deleted: %w", err)
	}
	for _, u := range data.AllUIDs() {
		if !set.Contains(u) {
			p.log.Info("imap: skipping EXPUNGE, foreign \\Deleted messages present (no UIDPLUS)")
			return nil
		}
	}
	if err := c.Expunge().Close(); err != nil {
		return fmt.Errorf("imap: expunge: %w", err)
	}
	return nil
}

// Move moves messages to toFolderRemoteID and returns the new references in
// the same order as refs (empty ID when the server did not tell).
//
// MOVE (RFC 6851) is used when available; otherwise UID COPY + \Deleted +
// UID EXPUNGE. New UIDs come from COPYUID (UIDPLUS); without it they are
// looked up by Message-ID in the destination folder.
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, toFolderRemoteID string) ([]domain.RemoteRef, error) {
	out := make([]domain.RemoteRef, len(refs))
	for i := range out {
		out[i].FolderRemoteID = toFolderRemoteID
	}
	folders, byFolder, err := group(refs)
	if err != nil {
		return nil, err
	}
	err = p.do(ctx, func(c *imapclient.Client) error {
		for _, folder := range folders {
			if folder == toFolderRemoteID {
				for _, i := range byFolder[folder] {
					out[i] = refs[i]
				}
				continue
			}
			if err := p.moveFolder(c, refs, byFolder[folder], folder, toFolderRemoteID, out); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Provider) moveFolder(c *imapclient.Client, refs []domain.RemoteRef, idx []int, from, to string, out []domain.RemoteRef) error {
	if err := ensureSelected(c, from); err != nil {
		return err
	}
	set := refsUIDSet(refs, idx)
	caps := c.Caps()

	// Without UIDPLUS no COPYUID is returned: remember Message-IDs first.
	var msgIDs map[imap.UID]string
	if !caps.Has(imap.CapUIDPlus) {
		msgIDs = p.messageIDs(c, set)
	}

	var src, dst imap.UIDSet
	if caps.Has(imap.CapMove) {
		data, err := c.Move(set, to).Wait()
		if err != nil {
			return fmt.Errorf("imap: move to %q: %w", to, err)
		}
		src, _ = data.SourceUIDs.(imap.UIDSet)
		dst, _ = data.DestUIDs.(imap.UIDSet)
	} else {
		data, err := c.Copy(set, to).Wait()
		if err != nil {
			return fmt.Errorf("imap: copy to %q: %w", to, err)
		}
		src, dst = data.SourceUIDs, data.DestUIDs
		if err := storeFlags(c, set, imap.StoreFlagsAdd, []domain.Flag{domain.FlagDeleted}); err != nil {
			return err
		}
		if err := p.expungeUIDs(c, set); err != nil {
			return err
		}
	}

	mapping := mapCopyUID(src, dst)
	if mapping == nil && len(msgIDs) > 0 {
		mapping = p.findByMessageID(c, to, msgIDs)
	}
	for _, i := range idx {
		u, _ := parseUID(refs[i])
		if d, ok := mapping[u]; ok {
			out[i].ID = strconv.FormatUint(uint64(d), 10)
		}
	}
	return nil
}

// mapCopyUID pairs COPYUID source and destination UIDs. Both sets are
// expanded in ascending order (RFC 4315 servers assign destination UIDs in
// source order).
func mapCopyUID(src, dst imap.UIDSet) map[imap.UID]imap.UID {
	s, ok1 := src.Nums()
	d, ok2 := dst.Nums()
	if !ok1 || !ok2 || len(s) == 0 || len(s) != len(d) {
		return nil
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	m := make(map[imap.UID]imap.UID, len(s))
	for i := range s {
		m[s[i]] = d[i]
	}
	return m
}

func (p *Provider) messageIDs(c *imapclient.Client, set imap.UIDSet) map[imap.UID]string {
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	msgs, err := collectFetch(c, c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}), 64<<10, maxFetchTotal, 0)
	if err != nil {
		return nil
	}
	m := make(map[imap.UID]string)
	for _, msg := range msgs {
		if id := NormalizeMessageID(parseHeaderBlock(msg.FindBodySection(sec)).Get("Message-Id")); id != "" {
			m[msg.UID] = id
		}
	}
	return m
}

// findByMessageID looks up messages in folder by Message-ID (newest match).
// It leaves folder selected.
func (p *Provider) findByMessageID(c *imapclient.Client, folder string, ids map[imap.UID]string) map[imap.UID]imap.UID {
	if _, err := selectMailbox(c, folder, false); err != nil {
		return nil
	}
	m := make(map[imap.UID]imap.UID)
	for src, id := range ids {
		data, err := c.UIDSearch(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: id}},
		}, nil).Wait()
		if err != nil {
			continue
		}
		var best imap.UID
		for _, u := range data.AllUIDs() {
			if u > best {
				best = u
			}
		}
		if best != 0 {
			m[src] = best
		}
	}
	return m
}

// Append stores raw in folderRemoteID and returns its reference. The UID
// comes from APPENDUID (UIDPLUS); otherwise it is looked up by Message-ID.
// The message is read into memory because APPEND needs its size up front.
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	ref := domain.RemoteRef{FolderRemoteID: folderRemoteID}
	body, err := io.ReadAll(io.LimitReader(raw, maxAppendSize+1))
	if err != nil {
		return ref, fmt.Errorf("imap: read message: %w", err)
	}
	if len(body) > maxAppendSize {
		return ref, errors.New("imap: message too large to append")
	}
	err = p.do(ctx, func(c *imapclient.Client) error {
		if limit, ok := c.Caps().AppendLimit(); ok && limit != nil && int64(len(body)) > int64(*limit) {
			return fmt.Errorf("imap: message exceeds server APPENDLIMIT of %d bytes", *limit)
		}
		cmd := c.Append(folderRemoteID, int64(len(body)), &imap.AppendOptions{Flags: toIMAPFlags(flags)})
		if _, err := cmd.Write(body); err != nil {
			cmd.Close()
			return fmt.Errorf("imap: append: %w", err)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("imap: append: %w", err)
		}
		data, err := cmd.Wait()
		if err != nil {
			return fmt.Errorf("imap: append to %q: %w", folderRemoteID, err)
		}
		if data.UID != 0 {
			ref.ID = strconv.FormatUint(uint64(data.UID), 10)
			return nil
		}
		if id := messageIDOf(body); id != "" {
			m := p.findByMessageID(c, folderRemoteID, map[imap.UID]string{1: id})
			if u, ok := m[1]; ok {
				ref.ID = strconv.FormatUint(uint64(u), 10)
			}
		}
		return nil
	})
	return ref, err
}

func messageIDOf(raw []byte) string {
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	if err != nil && h == nil {
		return ""
	}
	return NormalizeMessageID(h.Get("Message-Id"))
}
