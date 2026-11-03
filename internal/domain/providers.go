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
