// SPDX-License-Identifier: AGPL-3.0-or-later

// Package team implements shared team mailboxes of the server mode
// (KONZEPT 8.14, 15.6): metadata and permissions, conversation assignment
// and status, internal comments with @mentions, per-user reply drafts,
// presence/collision hints, statistics, the workflow action "assign" and
// the lifecycle of the team stores (Manager).
//
// Data layout: the mail data of a team mailbox is synced exactly once into
// its own store data_dir/teams/<id>/tuxmail.db (a normal internal/store
// database, opened by the Manager and served by a team-level runtime that
// the server builds with internal/app). Everything that is about people
// (grants, assignments, comments, mentions, drafts) lives in server.db
// (tables team_*, migration internal/auth/migrations/0003_team.sql) because
// it references users and groups.
//
// The package does not import internal/app; the application layer reads a
// team mailbox through app.TeamSource, which checks permissions with
// Service.RoleOf before every access.
package team

import (
	"encoding/json"
	"errors"
	"time"
)

// Errors returned by the service.
var (
	ErrNotFound     = errors.New("team: not found")
	ErrForbidden    = errors.New("team: permission denied")
	ErrInvalidInput = errors.New("team: invalid input")
	ErrExists       = errors.New("team: already exists")
)

// Role is a permission level on a team mailbox. Every role includes the
// rights of the lower ones: read < reply < assign < manage.
type Role string

// Roles.
const (
	RoleNone   Role = ""
	RoleRead   Role = "read"   // see conversations, comments, members, stats
	RoleReply  Role = "reply"  // answer as the team, comment, move/flag, assign to oneself
	RoleAssign Role = "assign" // assign conversations to anyone, change status of all
	RoleManage Role = "manage" // edit snippets, workflows, settings and grants
)

// Level orders the roles (0 = no access).
func (r Role) Level() int {
	switch r {
	case RoleRead:
		return 1
	case RoleReply:
		return 2
	case RoleAssign:
		return 3
	case RoleManage:
		return 4
	}
	return 0
}

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool { return r.Level() > 0 }

// Allows reports whether r includes need.
func (r Role) Allows(need Role) bool { return r.Level() > 0 && r.Level() >= need.Level() }

// Max returns the higher of two roles.
func Max(a, b Role) Role {
	if b.Level() > a.Level() {
		return b
	}
	return a
}

// Mailbox kinds. Messenger accounts (e.g. a company WhatsApp through a
// bridge) are a hook for the messenger integration: the team store and all
// collaboration features are channel independent.
const (
	KindMail      = "mail"
	KindMessenger = "messenger"
)

// Mailbox is a shared team mailbox.
type Mailbox struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Address      string    `json:"address"`
	Description  string    `json:"description"`
	Kind         string    `json:"kind"`
	SharedDrafts bool      `json:"sharedDrafts"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MailboxInput creates or updates a mailbox.
type MailboxInput struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	Description  string `json:"description"`
	Kind         string `json:"kind"`
	SharedDrafts bool   `json:"sharedDrafts"`
	Disabled     bool   `json:"disabled"`
}

// Access is a mailbox together with the role of the asking user.
type Access struct {
	Mailbox
	Role Role `json:"role"`
	// Counters for the sidebar.
	Mine       int `json:"mine"`
	Unassigned int `json:"unassigned"`
	Open       int `json:"open"`
	Mentions   int `json:"mentions"` // unread mentions of the user
	// UserID is the asking user (the UI tells own presence from others').
	UserID int64 `json:"userId"`
}

// Grant gives a user or a group a role on a mailbox.
type Grant struct {
	MailboxID int64  `json:"mailboxId"`
	UserID    int64  `json:"userId,omitempty"`
	GroupID   int64  `json:"groupId,omitempty"`
	Role      Role   `json:"role"`
	Name      string `json:"name"` // username or group name (resolved)
}

// Member is a user with access to a mailbox (assignee picker, mentions).
type Member struct {
	UserID      int64  `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        Role   `json:"role"`
}

// Status of a team conversation.
type Status string

// Statuses (KONZEPT 8.14: offen, in Bearbeitung, wartet, erledigt).
const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusWaiting    Status = "waiting"
	StatusDone       Status = "done"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	return s == StatusOpen || s == StatusInProgress || s == StatusWaiting || s == StatusDone
}

// Priorities.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

func validPriority(p string) bool {
	return p == PriorityLow || p == PriorityNormal || p == PriorityHigh || p == PriorityUrgent
}

// Assignment is the collaboration state of one conversation. Conversations
// without a row are open and unassigned.
type Assignment struct {
	MailboxID       int64      `json:"mailboxId"`
	ConversationID  int64      `json:"conversationId"`
	AssigneeID      int64      `json:"assigneeId,omitempty"`
	AssigneeName    string     `json:"assigneeName,omitempty"`
	Status          Status     `json:"status"`
	Priority        string     `json:"priority"`
	DueAt           *time.Time `json:"dueAt,omitempty"`
	OpenedAt        time.Time  `json:"openedAt"`
	FirstResponseAt *time.Time `json:"firstResponseAt,omitempty"`
	DoneAt          *time.Time `json:"doneAt,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	UpdatedBy       int64      `json:"updatedBy,omitempty"`
	Comments        int        `json:"comments"`
}

// AssignmentPatch changes an assignment; nil fields are kept. AssigneeID 0
// unassigns; DueAt "" clears the due date.
type AssignmentPatch struct {
	AssigneeID *int64  `json:"assigneeId,omitempty"`
	Status     *Status `json:"status,omitempty"`
	Priority   *string `json:"priority,omitempty"`
	DueAt      *string `json:"dueAt,omitempty"` // RFC 3339 or YYYY-MM-DD
}

// Comment is an internal note on a conversation. It is never sent to the
// customer.
type Comment struct {
	ID             int64      `json:"id"`
	MailboxID      int64      `json:"mailboxId"`
	ConversationID int64      `json:"conversationId"`
	AuthorID       int64      `json:"authorId,omitempty"`
	AuthorName     string     `json:"authorName"`
	Body           string     `json:"body"`
	Mentions       []int64    `json:"mentions"`
	CreatedAt      time.Time  `json:"createdAt"`
	EditedAt       *time.Time `json:"editedAt,omitempty"`
}

// Mention is an unread or read @mention of the user.
type Mention struct {
	Comment
	MailboxName string     `json:"mailboxName"`
	ReadAt      *time.Time `json:"readAt,omitempty"`
}

// Draft is a reply draft of one user for a team conversation.
type Draft struct {
	MailboxID      int64           `json:"mailboxId"`
	ConversationID int64           `json:"conversationId"`
	UserID         int64           `json:"userId"`
	UserName       string          `json:"userName"`
	Body           json.RawMessage `json:"body,omitempty"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// Views of the team conversation list (KONZEPT 8.14).
const (
	ViewAll        = ""
	ViewMine       = "mine"       // Mir zugewiesen (not done)
	ViewUnassigned = "unassigned" // Nicht zugewiesen (not done)
	ViewDone       = "done"       // Erledigt
	ViewOpen       = "open"       // everything not done
)

// Stats are simple team statistics.
type Stats struct {
	Total      int `json:"total"`
	Open       int `json:"open"`
	InProgress int `json:"inProgress"`
	Waiting    int `json:"waiting"`
	Done       int `json:"done"`
	Unassigned int `json:"unassigned"`
	Overdue    int `json:"overdue"`
	// MedianFirstResponseSeconds is the median time between a conversation
	// being opened and the first team reply (0 if there is none yet).
	MedianFirstResponseSeconds int64           `json:"medianFirstResponseSeconds"`
	Responded                  int             `json:"responded"`
	ByAssignee                 []AssigneeStats `json:"byAssignee"`
}

// AssigneeStats are the counters of one assignee.
type AssigneeStats struct {
	UserID int64  `json:"userId"`
	Name   string `json:"name"`
	Open   int    `json:"open"`
	Done   int    `json:"done"`
}

// Event kinds delivered to Service listeners.
const (
	EventAssigned = "assigned" // assignee changed (UserIDs: new assignee)
	EventUpdated  = "updated"  // status/priority/due changed
	EventComment  = "comment"  // comment added/changed/deleted
	EventMention  = "mention"  // UserIDs were mentioned in Comment
	EventPresence = "presence" // presence of a conversation changed
	EventDraft    = "draft"    // a (shared) draft changed
	EventMailbox  = "mailbox"  // mailbox created/changed/deleted or grants changed
	EventWorkflow = "workflow" // a workflow with external effect was saved/enabled (UserIDs: the managers to tell)
)

// Event is a change of collaboration state. Listeners fan it out to the
// UIs of all users with access (WebSocket) and send notifications.
type Event struct {
	Kind           string      `json:"kind"`
	MailboxID      int64       `json:"mailboxId"`
	ConversationID int64       `json:"conversationId,omitempty"`
	ActorID        int64       `json:"actorId,omitempty"`
	ActorName      string      `json:"actorName,omitempty"`
	UserIDs        []int64     `json:"userIds,omitempty"`
	Assignment     *Assignment `json:"assignment,omitempty"`
	Comment        *Comment    `json:"comment,omitempty"`
	Presence       []Presence  `json:"presence,omitempty"`
	// Previous assignee for EventAssigned (0 = was unassigned).
	PreviousAssigneeID int64 `json:"previousAssigneeId,omitempty"`
	// Text describes the change for notifications (EventWorkflow).
	Text string `json:"text,omitempty"`
}

// Actor is the user performing an action.
type Actor struct {
	UserID int64
	Name   string // display name (falls back to the username)
	IP     string
}
