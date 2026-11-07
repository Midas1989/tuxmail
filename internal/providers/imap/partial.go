// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"fmt"
	"strconv"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// maxPartialBody bounds the body prefix of FetchPartial per message.
const maxPartialBody = 64 << 10

var _ domain.PartialFetcher = (*Provider)(nil)

// FetchPartial implements domain.PartialFetcher: for every UID it fetches
// BODY.PEEK[HEADER] and BODY.PEEK[TEXT]<0.n> (never BODY[], never \Seen) and
// returns the header followed by the body prefix. The caller derives the
// preview of mails kept with their header only from it; the prefix of a
// multipart message ends in the middle of a part, which the lenient MIME
// parser accepts.
func (p *Provider) FetchPartial(ctx context.Context, folderRemoteID string, ids []string, maxBodyBytes int64) (map[string][]byte, error) {
	if maxBodyBytes <= 0 || maxBodyBytes > maxPartialBody {
		maxBodyBytes = maxPartialBody
	}
	var uids []uint32
	for _, id := range ids {
		u, err := parseUID(domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: id})
		if err != nil {
			continue // not an IMAP UID (e.g. a local placeholder)
		}
		uids = append(uids, uint32(u))
	}
	out := make(map[string][]byte, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	hdr := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
	text := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText, Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: maxBodyBytes}}
	err := p.do(ctx, func(c *imapclient.Client) error {
		if err := ensureSelected(c, folderRemoteID); err != nil {
			return err
		}
		for _, batch := range batches(uids, min(p.cfg.FetchBatchSize, 100)) {
			if err := ctx.Err(); err != nil {
				return err
			}
			opts := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{hdr, text}}
			// A hostile server could ignore the partial: bound every literal.
			msgs, err := collectFetch(c, c.Fetch(uidSet(batch), opts), maxFetchSection+maxBodyBytes, maxFetchTotal, 2*len(batch)+100)
			if err != nil {
				return fmt.Errorf("imap: fetch partial: %w", err)
			}
			for _, m := range msgs {
				if m.UID == 0 {
					continue
				}
				h := m.FindBodySection(hdr)
				if len(h) == 0 {
					continue
				}
				b := m.FindBodySection(text)
				if int64(len(b)) > maxBodyBytes {
					b = b[:maxBodyBytes]
				}
				raw := make([]byte, 0, len(h)+len(b)+2)
				raw = append(raw, h...)
				if len(h) < 2 || string(h[len(h)-2:]) != "\n\n" && (len(h) < 4 || string(h[len(h)-4:]) != "\r\n\r\n") {
					raw = append(raw, "\r\n"...)
				}
				raw = append(raw, b...)
				out[strconv.FormatUint(uint64(m.UID), 10)] = raw
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ domain.TextSearcher = (*Provider)(nil)

// SearchText implements domain.TextSearcher with UID SEARCH TEXT (header
// and body, case-insensitive on the server). Only the newest limit UIDs are
// returned.
func (p *Provider) SearchText(ctx context.Context, folderRemoteID, text string, limit int) ([]string, error) {
	if text == "" {
		return nil, nil
	}
	var out []string
	err := p.do(ctx, func(c *imapclient.Client) error {
		sel, err := selectMailbox(c, folderRemoteID, false)
		if err != nil {
			return err
		}
		if sel.NumMessages == 0 {
			return nil
		}
		data, err := c.UIDSearch(&imap.SearchCriteria{Text: []string{text}}, nil).Wait()
		if err != nil {
			return fmt.Errorf("imap: search %q: %w", folderRemoteID, err)
		}
		uids, err := searchUIDs(data, sel.NumMessages)
		if err != nil {
			return fmt.Errorf("imap: search %q: %w", folderRemoteID, err)
		}
		if limit > 0 && len(uids) > limit {
			uids = uids[len(uids)-limit:]
		}
		for _, u := range uids {
			out = append(out, strconv.FormatUint(uint64(u), 10))
		}
		return nil
	})
	return out, err
}
