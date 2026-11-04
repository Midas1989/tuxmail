// SPDX-License-Identifier: AGPL-3.0-or-later

package events

// TeamAssigned is published on the bus of a team mailbox when a
// conversation got a new assignee (KONZEPT 8.14, internal/team). Data holds
// mailboxId, conversationId, assignee (username), assigneeId, assigneeName,
// previousAssigneeId and status.
const TeamAssigned Type = "team.assigned"

func init() { AllTypes = append(AllTypes, TeamAssigned) }
