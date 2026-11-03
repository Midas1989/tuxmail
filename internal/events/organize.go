// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import "time"

// Workflow trigger topics for tags, projects and search folders (they are
// part of AllTypes).
const (
	TagAdded             Type = "tag.added"              // an item got a tag
	TagRemoved           Type = "tag.removed"            // a tag was removed from an item
	ProjectItemAdded     Type = "project.item_added"     // an item was linked to a project
	ProjectStatusChanged Type = "project.status_changed" // a project changed its status
	ProjectDueSoon       Type = "project.due_soon"       // an open project reaches its due date soon
	SearchFolderMatched  Type = "searchfolder.matched"   // an item newly matches a search folder
)

// UI live-update topics for tags, projects and search folders. They are not
// workflow triggers (they are not in AllTypes); UI subscribers receive them
// with an empty type filter or by naming them explicitly.
const (
	TagChanged          Type = "tag.changed"
	ProjectChanged      Type = "project.changed"
	SearchFolderChanged Type = "searchfolder.changed"
)

// UITypes lists the UI live-update topics.
var UITypes = []Type{TagChanged, ProjectChanged, SearchFolderChanged}

// Change operations reported in TagChange.Op, ProjectChange.Op and
// SearchFolderChange.Op of the UI topics.
const (
	OpCreated  = "created"
	OpUpdated  = "updated"
	OpDeleted  = "deleted"
	OpRenamed  = "renamed"
	OpMerged   = "merged"
	OpTagged   = "tagged"
	OpUntagged = "untagged"
	OpLinked   = "linked"   // items added to a project
	OpUnlinked = "unlinked" // items removed from a project
	OpStatus   = "status"   // project status changed
	OpCounts   = "counts"   // search folder counts changed or were invalidated
)

// ItemKey identifies a stored item by kind and row id. Kinds are the store
// item kinds: message, conversation, chat, chat_message, event, task,
// contact, person, file, search_folder, project.
type ItemKey struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// TagChange is the payload of tag.added, tag.removed and tag.changed.
type TagChange struct {
	TagID   int64    `json:"tagId"`
	Name    string   `json:"name"`              // leaf name
	Path    string   `json:"path"`              // full path, e.g. "Projekt/Kunde-A"
	OldPath string   `json:"oldPath,omitempty"` // renamed/merged: previous path
	Source  string   `json:"source,omitempty"`  // local, imap_keyword, gmail_label, outlook_category, workflow, user, rule
	Item    *ItemKey `json:"item,omitempty"`    // tag.added/removed: the tagged item
	Items   int      `json:"items,omitempty"`   // tag.changed: number of affected items
	Op      string   `json:"op,omitempty"`      // tag.changed only
}

// ProjectChange is the payload of project.* events and project.changed.
type ProjectChange struct {
	ProjectID int64     `json:"projectId"`
	Key       string    `json:"key,omitempty"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	OldStatus string    `json:"oldStatus,omitempty"` // project.status_changed
	DueAt     time.Time `json:"dueAt,omitzero"`
	Customer  string    `json:"customer,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Item      *ItemKey  `json:"item,omitempty"` // project.item_added
	Role      string    `json:"role,omitempty"` // project.item_added: role of the item
	AddedBy   string    `json:"addedBy,omitempty"`
	Items     int       `json:"items,omitempty"` // project.changed: number of affected items
	Op        string    `json:"op,omitempty"`    // project.changed only
}

// SearchFolderChange is the payload of searchfolder.matched and
// searchfolder.changed.
type SearchFolderChange struct {
	FolderID int64    `json:"folderId"`
	Name     string   `json:"name"`
	Query    string   `json:"query,omitempty"`
	Item     *ItemKey `json:"item,omitempty"` // searchfolder.matched: the newly matching item
	Total    int      `json:"total,omitempty"`
	Unread   int      `json:"unread,omitempty"`
	Op       string   `json:"op,omitempty"` // searchfolder.changed only
}
