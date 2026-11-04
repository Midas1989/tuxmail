// SPDX-License-Identifier: AGPL-3.0-or-later

package dav

import (
	"context"
	"errors"
)

// Pull computes the changes of a collection since the last sync and
// fetches the changed resources: sync-collection when useSync is set
// (token from the previous run; an invalid token restarts with a full
// sync), otherwise an ETag listing compared with local (href -> ETag).
// When sync-collection fails for another reason the listing is used.
func (c *Client) Pull(ctx context.Context, collection, token string, useSync bool, local map[string]string,
	reportNS, reportLocal, dataLocal string) (*Delta, error) {
	var (
		d   *Delta
		err error
	)
	if useSync {
		d, err = c.SyncCollection(ctx, collection, token)
		if errors.Is(err, ErrInvalidSyncToken) && token != "" {
			token = ""
			d, err = c.SyncCollection(ctx, collection, "")
		}
		if err == nil && token == "" {
			d.Full = true
			seen := map[string]bool{}
			for _, it := range d.Changed {
				seen[it.Href] = true
			}
			for h := range local {
				if !seen[h] {
					d.Deleted = append(d.Deleted, h)
				}
			}
			var changed []Item
			for _, it := range d.Changed {
				if local[it.Href] != it.ETag || it.ETag == "" {
					changed = append(changed, it)
				}
			}
			d.Changed = changed
		}
	}
	if !useSync || err != nil {
		items, lerr := c.ListETags(ctx, collection)
		if lerr != nil {
			if err != nil {
				return nil, err
			}
			return nil, lerr
		}
		d = DiffETags(items, local)
	}
	if len(d.Changed) > 0 {
		hrefs := make([]string, len(d.Changed))
		for i, it := range d.Changed {
			hrefs[i] = it.Href
		}
		got, err := c.MultiGet(ctx, collection, hrefs, reportNS, reportLocal, dataLocal)
		if err != nil {
			return nil, err
		}
		d.Changed = got
	}
	return d, nil
}
