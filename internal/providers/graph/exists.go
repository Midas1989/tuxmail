// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/domain"
)

var _ domain.ExistenceChecker = (*Provider)(nil)

// ConfirmExisting implements domain.ExistenceChecker: the messages must still
// exist and lie in the folder (a moved message has another parent).
func (p *Provider) ConfirmExisting(ctx context.Context, folderRemoteID string, _ domain.SyncState, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	fid := lastSeg(folderRemoteID)
	for start := 0; start < len(ids); start += 20 {
		chunk := ids[start:min(start+20, len(ids))]
		reqs := make([]BatchRequest, len(chunk))
		for i, id := range chunk {
			reqs[i] = BatchRequest{ID: strconv.Itoa(i), Method: http.MethodGet, URL: p.user() + "/messages/" + url.PathEscape(id) + "?$select=id,parentFolderId"}
		}
		resp, err := p.Batch(ctx, reqs)
		if err != nil {
			return nil, err
		}
		for i, id := range chunk {
			br, ok := resp[strconv.Itoa(i)]
			if !ok || br.Err() != nil {
				continue
			}
			var m gMessage
			if json.Unmarshal(br.Body, &m) == nil && m.ID == id && (m.ParentFolderID == "" || m.ParentFolderID == fid || m.ParentFolderID == folderRemoteID) {
				out[id] = true
			}
		}
	}
	return out, nil
}
