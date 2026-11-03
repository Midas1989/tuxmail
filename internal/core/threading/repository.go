// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"context"
	"errors"
	"time"
)

// ConversationKind mirrors conversations.kind.
type ConversationKind string

const (
	ConversationEmailThread ConversationKind = "email_thread"
	ConversationChat        ConversationKind = "chat"
	ConversationPersonView  ConversationKind = "person_view"
)

// Conversation is the part of a conversations row the engine manages.
type Conversation struct {
	ID    int64
	Kind  ConversationKind
	Title string
}

// OverrideAction mirrors conversation_overrides.action.
type OverrideAction string

const (
	// OverrideSplit detaches a message into its own conversation. The message
	// is a cut node: it neither pulls other messages in nor is pulled.
	OverrideSplit OverrideAction = "split"
	// OverridePinTo places a message in a given conversation. Like split, the
	// message is a cut node for automatic threading.
	OverridePinTo OverrideAction = "pin_to"
	// OverrideMerge places a message in a given conversation and acts as an
	// anchor: its automatically threaded component follows it.
	OverrideMerge OverrideAction = "merge"
)

// Override is a manual user decision (conversation_overrides row). It always
// wins over automatic detection.
type Override struct {
	MessageKey     string
	ConversationID int64
	Action         OverrideAction
}

// ErrNotFound is returned by repositories and the engine for unknown IDs.
var ErrNotFound = errors.New("threading: not found")

// Repository is the persistence the Engine needs. It maps onto the SQLite
// schema of KONZEPT 5.2:
//
//   - relations      -> message_relations (UNIQUE source_key, target_key, kind)
//   - assignments    -> messages.conversation_id, looked up by message key
//     (message_id_hdr / server_obj_id / fingerprint); a message whose
//     conversation is 0 is treated as not present (deleted / not yet synced)
//   - conversations  -> conversations
//   - overrides      -> conversation_overrides
//
// All methods are called with keys produced by this package. Implementations
// must not interpret virtual keys (see IsVirtualKey); they are only stored in
// relations. Repositories need not be safe for concurrent use; the Engine
// serializes access.
type Repository interface {
	// SaveRelations upserts relations by (SourceKey, TargetKey, Kind). An
	// existing relation keeps the higher confidence; createdAt is only set
	// for new rows.
	SaveRelations(ctx context.Context, rels []Relation, createdAt time.Time) error
	// RelationsOf returns all relations where key is source or target.
	RelationsOf(ctx context.Context, key string) ([]Relation, error)

	// ConversationOf returns the conversation of a present message, or 0 if
	// the message is unknown or not present.
	ConversationOf(ctx context.Context, key string) (int64, error)
	// SetConversation assigns a message to a conversation; 0 marks it as not
	// present (the relations of the key are kept).
	SetConversation(ctx context.Context, key string, conversationID int64) error
	// Members returns the keys of all present messages of a conversation.
	Members(ctx context.Context, conversationID int64) ([]string, error)

	// CreateConversation inserts a conversation and returns its ID. IDs must
	// be increasing so that a lower ID means an older conversation.
	CreateConversation(ctx context.Context, c Conversation) (int64, error)
	// GetConversation returns ErrNotFound if the conversation does not exist.
	GetConversation(ctx context.Context, id int64) (Conversation, error)
	// DeleteConversation removes an (empty) conversation.
	DeleteConversation(ctx context.Context, id int64) error

	// Override returns the override of a message key, if any.
	Override(ctx context.Context, key string) (Override, bool, error)
	SetOverride(ctx context.Context, o Override) error
	DeleteOverride(ctx context.Context, key string) error
}

// Transactor may be implemented by a Repository to run one engine operation
// atomically (e.g. inside a SQLite transaction). fn must use the passed
// repository for all calls.
type Transactor interface {
	InTx(ctx context.Context, fn func(Repository) error) error
}

// HeuristicIndex may be implemented by a Repository to support optional
// subject-heuristic matching (KONZEPT 6.3, confidence 0.5).
type HeuristicIndex interface {
	// IndexMessage records the matching metadata of a message. A SQLite
	// adapter can make this a no-op and answer FindBySubject from the
	// messages and message_addresses tables.
	IndexMessage(ctx context.Context, c Candidate) error
	// FindBySubject returns present messages whose normalized subject key
	// (SubjectKey) equals subjectKey and whose date lies in [from, to].
	FindBySubject(ctx context.Context, subjectKey string, from, to time.Time) ([]Candidate, error)
}
