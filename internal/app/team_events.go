// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// UI events of team mailboxes (types.ts TeamApiEvent in frontend
// src/lib/api/team.ts). The server fans team.Service events and the
// events of team runtimes out to the Apps of all users with access; the
// payload is in Event.Team.
const (
	// EventTeamChanged: data of a team mailbox changed (new mail, flags,
	// assignment, comment, draft). Payload: TeamChange.
	EventTeamChanged = "team.changed"
	// EventTeamPresence: who views/answers a conversation. Payload: TeamChange with Presence.
	EventTeamPresence = "team.presence"
	// EventTeamNotice: in-app notification for this user (mention,
	// assignment). Payload: TeamNotice.
	EventTeamNotice = "team.notice"
	// EventTeamMailboxes: the list of accessible mailboxes changed.
	EventTeamMailboxes = "team.mailboxes"
)

// TeamChange is the payload of team.changed and team.presence.
type TeamChange struct {
	MailboxID      int64  `json:"mailboxId"`
	ConversationID int64  `json:"conversationId,omitempty"`
	Kind           string `json:"kind"` // message | conversation | folder | assigned | updated | comment | draft | presence
	ActorID        int64  `json:"actorId,omitempty"`
	ActorName      string `json:"actorName,omitempty"`
	FolderID       int64  `json:"folderId,omitempty"`
	Presence       any    `json:"presence,omitempty"`
	Assignment     any    `json:"assignment,omitempty"`
}

// TeamNotice is the payload of team.notice.
type TeamNotice struct {
	Kind           string `json:"kind"` // mention | assigned
	MailboxID      int64  `json:"mailboxId"`
	MailboxName    string `json:"mailboxName"`
	ConversationID int64  `json:"conversationId"`
	ActorName      string `json:"actorName"`
	Text           string `json:"text"`
}

// PublishTeam sends a team event to this user's UI (WebSocket subscribers).
func (a *App) PublishTeam(typ string, payload any) {
	a.hub.Publish(Event{Type: typ, Team: payload})
}
