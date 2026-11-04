// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import "github.com/Midas1989/Mailclient/internal/events"

// The trigger team.assigned (team mailboxes, internal/team) carries its
// values in data.* (mailboxId, conversationId, assignee, status ...).
func init() {
	triggerDocs[events.TeamAssigned] = TriggerInfo{Description: "A team conversation was assigned (team mailboxes)",
		Fields: []string{"data."}}
}
