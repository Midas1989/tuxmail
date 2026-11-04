// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"io"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Narrow interfaces for the parts of the core that are built in parallel
// (see doc.go, "Wiring points"). Every interface has a working fallback in
// this package so the application layer is usable on its own.

// Syncer is the sync manager of one user (internal/sync, step A). It owns
// the provider connections and the operations queue: actions are applied
// to the local store first by the application layer and then replayed
// against the server by the syncer.
type Syncer interface {
	// AddAccount starts syncing a newly created account.
	AddAccount(ctx context.Context, accountID int64) error
	// RemoveAccount stops syncing an account before it is deleted.
	RemoveAccount(ctx context.Context, accountID int64) error
	// Status reports the connection state of all accounts.
	Status(ctx context.Context) ([]AccountStatus, error)
	// Move moves messages to a folder (local first, then queued for the server).
	Move(ctx context.Context, messageIDs []int64, toFolderID int64) error
	// SetFlags adds and removes flags (local first, then queued).
	SetFlags(ctx context.Context, messageIDs []int64, add, remove []domain.Flag) error
	// Delete moves messages to the trash, or deletes them permanently.
	Delete(ctx context.Context, messageIDs []int64, permanent bool) error
}

// AccountStatus is the sync state of one account.
type AccountStatus struct {
	AccountID int64  `json:"accountId"`
	State     string `json:"state"` // connecting | syncing | idle | error | stopped | offline
	Error     string `json:"error,omitempty"`
	// ErrorAt is when Error happened; NextRetry when the next attempt is due.
	ErrorAt   time.Time `json:"errorAt,omitzero"`
	NextRetry time.Time `json:"nextRetry,omitzero"`
	LastSync  time.Time `json:"lastSync,omitzero"`
	// Folder / FolderID: the folder being synced; Done / Total its progress
	// (local / server messages); Initial during its first sync.
	Folder   string `json:"folder,omitempty"`
	FolderID int64  `json:"folderId,omitempty"`
	Done     int    `json:"done,omitempty"`
	Total    int    `json:"total,omitempty"`
	Initial  bool   `json:"initial,omitempty"`
}

// SendRequest is a message to send, resolved by the application layer.
type SendRequest struct {
	Message OutgoingMessage
	// Identity of a mail; nil for messenger messages.
	Identity *domain.Identity
	// Attachments resolved from upload tokens (opened lazily).
	Attachments []SendAttachment
	// InReplyTo / ForwardOf are the stored messages being answered or forwarded.
	InReplyTo *store.Message
	ForwardOf *store.Message
	// Unattended marks sends without a user waiting for the result (due
	// scheduled messages): send-check warnings do not block them.
	Unattended bool
}

// SendAttachment is an uploaded attachment.
type SendAttachment struct {
	UploadToken string
	Filename    string
	MIMEType    string
	Size        int64
	Delivery    string
	Open        func() (io.ReadCloser, error)
}

// Sender sends mails and messenger messages (internal/send, step B).
type Sender interface {
	Send(ctx context.Context, req SendRequest) (SendResult, error)
}

// RenderedMessage is a parsed message body ready for display.
type RenderedMessage struct {
	Text string
	// HTML must be sanitized (no scripts, no remote content unless allowed);
	// the UI still renders it only inside a sandboxed iframe.
	HTML             string
	HasRemoteContent bool
	InReplyTo        []string
	References       []string
	Attachments      []RenderedAttachment
	Encrypted        bool
	// Automated: list/bulk mail or Auto-Submitted (newsletters,
	// notifications); rendered as a card in the chat views.
	Automated bool
}

// RenderedAttachment is an attachment found in a raw message. Index is the
// stable position used in download URLs.
type RenderedAttachment struct {
	Index     int
	Filename  string
	MIMEType  string
	Size      int64
	ContentID string
	Inline    bool
}

// MessageRenderer parses raw RFC 5322 messages (internal/mime/parse +
// internal/mime/sanitize, step B).
type MessageRenderer interface {
	Render(ctx context.Context, raw []byte) (RenderedMessage, error)
	// Attachment returns the decoded content of the attachment with the
	// given index (see RenderedAttachment.Index).
	Attachment(ctx context.Context, raw []byte, index int) (RenderedAttachment, []byte, error)
}

// Discoverer finds account settings for an address (internal/autoconfig).
type Discoverer interface {
	Discover(ctx context.Context, email, password string) (DiscoveryResult, error)
}

// PushBackend is the Web Push part of the server mode for one user.
type PushBackend interface {
	VAPIDPublicKey() string
	Subscribe(ctx context.Context, sub PushSubscriptionInput) error
	Unsubscribe(ctx context.Context, endpoint string) error
	Settings(ctx context.Context) (NotificationSettings, error)
	SaveSettings(ctx context.Context, s NotificationSettings) (NotificationSettings, error)
	Test(ctx context.Context) error
}

// AIProvider runs a chat completion (internal/ai).
type AIProvider interface {
	Complete(ctx context.Context, system, prompt string) (string, error)
}

// AIFactory creates a provider from settings and the API key (nil = not configured).
type AIFactory func(s AiSettings, apiKey string) (AIProvider, error)
