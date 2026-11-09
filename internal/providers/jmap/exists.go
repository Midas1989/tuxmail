// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"

	"github.com/Midas1989/Mailclient/internal/domain"
)

var _ domain.ExistenceChecker = (*Provider)(nil)

// ConfirmExisting implements domain.ExistenceChecker with Email/get: the
// emails must exist and still be in the mailbox.
func (p *Provider) ConfirmExisting(ctx context.Context, folderRemoteID string, _ domain.SyncState, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	list, _, err := p.getEmails(ctx, ids, []string{"id", "mailboxIds"})
	if err != nil {
		return nil, err
	}
	mb := mailboxID(folderRemoteID)
	for _, e := range list {
		if e.MailboxIDs[mb] {
			out[e.ID] = true
		}
	}
	return out, nil
}
