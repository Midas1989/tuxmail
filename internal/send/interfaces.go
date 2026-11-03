// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"io"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
)

// Identities resolves identities, accounts and signatures (implemented by
// *store.Store).
type Identities interface {
	GetIdentity(ctx context.Context, id int64) (domain.Identity, error)
	ListIdentities(ctx context.Context, accountID int64) ([]domain.Identity, error)
	GetAccount(ctx context.Context, id int64) (domain.Account, error)
	GetSignature(ctx context.Context, id int64) (domain.Signature, error)
}

// TransportFactory creates the outgoing transport of an account. The
// service closes the returned transport after each send; factories that pool
// connections return a wrapper whose Close is a no-op.
type TransportFactory interface {
	Transport(ctx context.Context, account domain.Account) (domain.Transport, error)
}

// HookRunner runs synchronous hooks (implemented by *events.Bus).
type HookRunner interface {
	RunHooks(ctx context.Context, ev events.Event) events.HookResult
}

// Publisher publishes events (implemented by *events.Bus).
type Publisher interface {
	Publish(ev events.Event) int
}

// UploadFile is a file to share via cloud link.
type UploadFile struct {
	Name        string
	ContentType string
	Size        int64
	Body        io.Reader
	// Subject of the mail, used by uploaders for the target directory
	// (KONZEPT 12.2: /TuxMail/Mail-Anhänge/<date>/<subject>/).
	Subject string
	// Expiry requested for the share (0 = provider default).
	Expiry time.Duration
}

// Share is a created share link.
type Share struct {
	URL      string
	Password string // "" if the share has no password
	Expires  time.Time
	// Provider, RemotePath and ShareID are recorded in cloud_links.
	Provider   string
	RemotePath string
	ShareID    string
}

// CloudUploader uploads files and creates share links (Nextcloud, OpenCloud,
// Vaultwarden Send). accountID is the cloud account.
type CloudUploader interface {
	Upload(ctx context.Context, accountID int64, file UploadFile) (Share, error)
}

// CloudLinkStore records created links (implemented by *store.Store).
type CloudLinkStore interface {
	CreateCloudLink(ctx context.Context, l store.CloudLink) (int64, error)
}

// MessengerSender delivers a share password through a messenger channel
// (Signal, WhatsApp ...) to the recipient.
type MessengerSender interface {
	SendPassword(ctx context.Context, accountID int64, to domain.Address, text string) error
}

// MailboxAppender stores a sent message in the Sent folder of the
// identity's account (implemented by the sync engine).
type MailboxAppender interface {
	AppendSent(ctx context.Context, account domain.Account, identity domain.Identity, raw []byte, flags []domain.Flag) error
}

// RelationRecorder stores compose relations (see StoreRelations and
// EngineRelations).
type RelationRecorder interface {
	RecordRelations(ctx context.Context, rels []threading.Relation) error
}

// FlagSetter sets flags on the original of a reply/forward (\Answered,
// $Forwarded), locally and on the server (implemented by the sync engine).
type FlagSetter interface {
	MarkOriginal(ctx context.Context, accountID int64, messageKey string, add []domain.Flag) error
}

// FollowUpScheduler schedules "waiting for reply" reminders (implemented by
// *followup.Scheduler).
type FollowUpScheduler interface {
	Schedule(ctx context.Context, req followup.Request) (int64, error)
}

// Queue persists delayed sends (see StoreQueue).
type Queue interface {
	EnqueueAt(ctx context.Context, kind, payload string, due time.Time) (int64, error)
	// CancelPending removes a not yet started operation and returns its
	// payload; ok is false if it is unknown or already running.
	CancelPending(ctx context.Context, id int64) (payload string, ok bool, err error)
}

// BlobResolver opens stored attachment content (compose.BlobResolver).
type BlobResolver interface {
	OpenBlob(ctx context.Context, ref string) (io.ReadCloser, error)
}

// ==== Adapters ================================================================

// StoreQueue implements Queue on the store's operations queue. Operations are
// enqueued without account so that a delayed send does not block other
// operations of the account (ClaimNextOp serializes per account).
type StoreQueue struct{ Store *store.Store }

// EnqueueAt implements Queue.
func (q StoreQueue) EnqueueAt(ctx context.Context, kind, payload string, due time.Time) (int64, error) {
	return q.Store.EnqueueOpAt(ctx, 0, kind, payload, due)
}

// CancelPending implements Queue.
func (q StoreQueue) CancelPending(ctx context.Context, id int64) (string, bool, error) {
	return q.Store.CancelPendingOp(ctx, id)
}

// StoreRelations records relations directly in message_relations.
type StoreRelations struct{ Store *store.Store }

// RecordRelations implements RelationRecorder.
func (r StoreRelations) RecordRelations(ctx context.Context, rels []threading.Relation) error {
	for _, rel := range rels {
		if _, err := r.Store.InsertRelation(ctx, store.Relation{
			SourceKey: rel.SourceKey, TargetKey: rel.TargetKey, Kind: string(rel.Kind),
			Origin: string(rel.Origin), Confidence: rel.Confidence,
		}); err != nil {
			return err
		}
	}
	return nil
}

// EngineRelations records relations through the threading engine, which
// also re-threads the affected conversations.
type EngineRelations struct{ Engine *threading.Engine }

// RecordRelations implements RelationRecorder.
func (r EngineRelations) RecordRelations(ctx context.Context, rels []threading.Relation) error {
	_, err := r.Engine.AddRelations(ctx, rels...)
	return err
}
