// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/internal/sync/content"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// BodyPolicy decides which raw messages are downloaded during sync.
type BodyPolicy string

const (
	// BodiesRecent downloads messages of the last RecentBodyDays (default);
	// older ones are loaded on demand (Manager.FetchBody).
	BodiesRecent BodyPolicy = "recent"
	// BodiesAll downloads recent messages first and then backfills all
	// others in the background.
	BodiesAll BodyPolicy = "all"
	// BodiesOnDemand only stores headers; bodies are loaded on demand. New
	// mails arriving during incremental sync are still downloaded so that
	// workflows see their content.
	BodiesOnDemand BodyPolicy = "on_demand"
)

// ProjectHooks is called for every new item (implemented by
// *project.Service).
type ProjectHooks interface {
	ProcessNewItem(ctx context.Context, it store.Item) error
}

// Tagger assigns and removes tags with events (implemented by
// *project.Service). If nil, the store is used directly.
type Tagger interface {
	Tag(ctx context.Context, path string, source store.TagSource, items ...store.Item) ([]store.Item, error)
	Untag(ctx context.Context, path string, items ...store.Item) ([]store.Item, error)
	SetProviderTags(ctx context.Context, it store.Item, source store.TagSource, paths []string) error
}

// WorkflowRunner processes queued workflow runs (implemented by
// *workflow.Engine). Set it only if the workflow engine does not run its
// own loop (workflow.Engine.Run).
type WorkflowRunner interface {
	ProcessOne(ctx context.Context) (bool, error)
}

// Sender sends replies and forwards for workflow actions. It is provided by
// the send pipeline; without it those actions return ErrNotImplemented.
type Sender interface {
	Forward(ctx context.Context, msg store.Message, req workflow.ForwardRequest) error
	// Reply sends (or drafts) a reply; automatic marks out-of-office replies
	// (Auto-Submitted: auto-replied, RFC 3834).
	Reply(ctx context.Context, msg store.Message, req workflow.ReplyRequest, automatic bool) error
}

// Config configures a Manager.
type Config struct {
	Store *store.Store // required
	Blobs *blob.Store  // required: raw messages
	Bus   *events.Bus  // optional

	// Credentials for connecting accounts (default SecretCredentials{Secrets}).
	Credentials Credentials
	Secrets     domain.SecretStore
	// Providers maps account kinds to providers (default DefaultRegistry()).
	Providers       *Registry
	ProviderOptions ProviderOptions

	// Extractor extracts text, snippet and attachments (default content.Default{}).
	Extractor content.Extractor
	// Threading is the conversation engine (default: a new engine on the
	// store's ThreadRepository with ThreadingConfig).
	Threading       *threading.Engine
	ThreadingConfig threading.Config

	Projects ProjectHooks
	// Comfort analyzes new mail (spam, categories, e-invoices, S/MIME) and
	// enriches mail events (comfort.go); optional.
	Comfort  MailProcessor
	Tags     Tagger
	Workflow WorkflowRunner
	Sender   Sender
	// KeywordMapper maps tags to IMAP keywords (default store.DefaultKeywordMapper).
	KeywordMapper *store.KeywordMapper

	Logger *slog.Logger
	Now    func() time.Time

	// Bodies is the download policy (default BodiesRecent).
	Bodies BodyPolicy
	// RecentBodyDays is the age limit of BodiesRecent (default 30).
	RecentBodyDays int
	// MaxBodySize skips downloading larger messages (default 50 MiB).
	MaxBodySize int64
	// BackfillBatch is the number of bodies downloaded per worker cycle (default 50).
	BackfillBatch int
	// IngestBatch is the number of new messages committed per batch during
	// a folder sync; events are published after each batch (default 100).
	IngestBatch int

	// PollInterval is how often every folder is synced (default 5 min).
	PollInterval time.Duration
	// FolderListInterval is how often the folder list is refreshed (default 15 min).
	FolderListInterval time.Duration
	// WatchRoles are the folders watched with IDLE (default inbox).
	WatchRoles []domain.FolderRole
	// Debounce delays the sync after a change hint (default 300 ms).
	Debounce time.Duration
	// MinBackoff / MaxBackoff bound the reconnect delay (default 1 s / 5 min).
	MinBackoff, MaxBackoff time.Duration

	// OpMaxAttempts is the number of attempts of an operation (default 8).
	OpMaxAttempts int
	// OpBackoff is the retry delay after attempt n (default store.Backoff).
	OpBackoff func(attempt int) time.Duration
	// PermanentDelete deletes without moving to the trash folder.
	PermanentDelete bool

	// OrphanMaxAge is how long orphaned messages are kept (default 7 days).
	OrphanMaxAge time.Duration
	// GCInterval / OptimizeInterval schedule the maintenance (default 24 h);
	// MaintenanceDelay is the delay of the first run after Start (default 5 min).
	GCInterval, OptimizeInterval, MaintenanceDelay time.Duration
}

func (c *Config) defaults() error {
	if c.Store == nil {
		return errors.New("sync: config needs a store")
	}
	if c.Blobs == nil {
		return errors.New("sync: config needs a blob store")
	}
	if c.Credentials == nil {
		c.Credentials = SecretCredentials{Secrets: c.Secrets}
	}
	if c.Providers == nil {
		c.Providers = DefaultRegistry()
	}
	if c.Extractor == nil {
		c.Extractor = content.Default{}
	}
	if c.KeywordMapper == nil {
		km := store.DefaultKeywordMapper
		c.KeywordMapper = &km
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.ProviderOptions.Logger == nil {
		c.ProviderOptions.Logger = c.Logger
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Bodies == "" {
		c.Bodies = BodiesRecent
	}
	setInt(&c.RecentBodyDays, 30)
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 50 << 20
	}
	setInt(&c.BackfillBatch, 50)
	setInt(&c.IngestBatch, 100)
	setDur(&c.PollInterval, 5*time.Minute)
	setDur(&c.FolderListInterval, 15*time.Minute)
	if c.WatchRoles == nil {
		c.WatchRoles = []domain.FolderRole{domain.RoleInbox}
	}
	setDur(&c.Debounce, 300*time.Millisecond)
	setDur(&c.MinBackoff, time.Second)
	setDur(&c.MaxBackoff, 5*time.Minute)
	if c.MaxBackoff < c.MinBackoff {
		c.MaxBackoff = c.MinBackoff
	}
	setInt(&c.OpMaxAttempts, 8)
	if c.OpBackoff == nil {
		c.OpBackoff = store.Backoff
	}
	setDur(&c.OrphanMaxAge, 7*24*time.Hour)
	setDur(&c.GCInterval, 24*time.Hour)
	setDur(&c.OptimizeInterval, 24*time.Hour)
	setDur(&c.MaintenanceDelay, 5*time.Minute)
	return nil
}

func setInt(v *int, def int) {
	if *v <= 0 {
		*v = def
	}
}

func setDur(v *time.Duration, def time.Duration) {
	if *v <= 0 {
		*v = def
	}
}
