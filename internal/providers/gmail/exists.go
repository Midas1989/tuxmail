// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"

	"github.com/Midas1989/Mailclient/internal/domain"
)

var _ domain.ExistenceChecker = (*Provider)(nil)

// ConfirmExisting implements domain.ExistenceChecker (format=minimal): the
// messages must exist and still carry the folder's label.
func (p *Provider) ConfirmExisting(ctx context.Context, folderRemoteID string, _ domain.SyncState, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	ms, err := p.metadata(ctx, ids, "?format=minimal")
	if err != nil {
		return nil, err
	}
	label := lastSeg(folderRemoteID)
	for _, m := range ms {
		if m.ID != "" && m.has(label) {
			out[m.ID] = true
		}
	}
	return out, nil
}
