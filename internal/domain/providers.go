// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"context"
	"io"
)

// MailProvider is implemented by IMAP, POP3, EAS, Graph, EWS, JMAP and Gmail providers.
// Implementations must be safe for use by a single sync worker goroutine; the core
// never calls one provider instance concurrently unless documented otherwise.
type MailProvider interface {
	Capabilities() MailCaps
	ListFolders(ctx context.Context) ([]Folder, error)
	CreateFolder(ctx context.Context, parentRemoteID, name string) (Folder, error)
	RenameFolder(ctx context.Context, remoteID, newName string) (Folder, error)
	DeleteFolder(ctx context.Context, remoteID string) error
	Sync(ctx context.Context, folderRemoteID string, state SyncState) (SyncDelta, SyncState, error)
	FetchRaw(ctx context.Context, ref RemoteRef) (io.ReadCloser, error)
	// Move returns the new references in the destination folder (same order as refs; empty ID if unknown).
	Move(ctx context.Context, refs []RemoteRef, toFolderRemoteID string) ([]RemoteRef, error)
	SetFlags(ctx context.Context, refs []RemoteRef, add, remove []Flag) error
	Delete(ctx context.Context, refs []RemoteRef) error
	Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []Flag) (RemoteRef, error)
	Watch(ctx context.Context, folderRemoteID string) (<-chan ChangeHint, error)
	Close() error
}

// PartialFetcher is implemented by providers that can load the header and
// the beginning of the body of messages without downloading them completely
// (IMAP: BODY.PEEK[HEADER] and BODY.PEEK[TEXT]<0.n>). The sync engine uses
// it for the preview of mails kept with their header only. The result maps
// ids of the folder to header + body prefix (a truncated RFC 5322 message);
// missing ids were not found. It never marks a message as read.
type PartialFetcher interface {
	FetchPartial(ctx context.Context, folderRemoteID string, ids []string, maxBodyBytes int64) (map[string][]byte, error)
}

// TextSearcher is implemented by providers that can search the text of the
// messages of a folder on the server (IMAP SEARCH TEXT). The sync engine
// uses it to find mails kept with their header only by their body; it
// returns the ids of the matches, newest last, at most limit.
type TextSearcher interface {
	SearchText(ctx context.Context, folderRemoteID, text string, limit int) ([]string, error)
}

// ExistenceChecker is implemented by providers that can confirm cheaply that
// messages still exist on the server. Before a locally stored body is
// released (mails kept with header only), the sync engine asks it; state is
// the folder's stored sync state, so that a changed folder identity (IMAP
// UIDVALIDITY) confirms nothing. Ids not in the result do not exist (or
// are marked deleted).
type ExistenceChecker interface {
	ConfirmExisting(ctx context.Context, folderRemoteID string, state SyncState, ids []string) (map[string]bool, error)
}

// OutgoingMessage is a fully rendered RFC 5322 message ready for submission.
type OutgoingMessage struct {
	From       Address
	Recipients []string // envelope recipients (To+Cc+Bcc)
	Raw        []byte
}

// Transport submits outgoing messages (SMTP, Graph sendMail, EAS SendMail ...).
type Transport interface {
	Send(ctx context.Context, msg OutgoingMessage) error
	Close() error
}

// SecretStore stores credentials (passwords, OAuth refresh tokens, session keys).
type SecretStore interface {
	Get(ref string) ([]byte, error)
	Set(ref string, value []byte) error
	Delete(ref string) error
}
