// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
)

// SyncOptions enable the sync engine (internal/sync) for an App.
type SyncOptions struct {
	// Configure may adjust the sync configuration before the manager is
	// created (providers, body policy, intervals ...).
	Configure func(*tsync.Config)
}

// managerSyncer adapts *sync.Manager to the Syncer interface. The manager
// applies every action locally first and queues it for the server.
type managerSyncer struct{ m *tsync.Manager }

func (s managerSyncer) AddAccount(ctx context.Context, id int64) error {
	return s.m.AddAccount(ctx, id)
}

func (s managerSyncer) RemoveAccount(_ context.Context, id int64) error {
	s.m.RemoveAccount(id)
	return nil
}

func (s managerSyncer) Status(context.Context) ([]AccountStatus, error) {
	sts := s.m.Statuses()
	out := make([]AccountStatus, 0, len(sts))
	for _, st := range sts {
		out = append(out, statusDTO(st))
	}
	return out, nil
}

func statusDTO(st tsync.AccountStatus) AccountStatus {
	return AccountStatus{AccountID: st.AccountID, State: string(st.State), Error: st.LastError, ErrorAt: st.LastErrorAt,
		NextRetry: st.NextRetry, LastSync: st.LastSync, Folder: st.Folder, FolderID: st.FolderID, Done: st.Done,
		Total: st.Total, Initial: st.Initial}
}

// SyncStatus returns the sync status of all mail accounts.
func (a *App) SyncStatus(ctx context.Context) ([]AccountStatus, error) {
	out, err := a.sync.Status(ctx)
	if out == nil {
		out = []AccountStatus{}
	}
	return out, err
}

func (s managerSyncer) Move(ctx context.Context, ids []int64, to int64) error {
	return s.m.Actions().Move(ctx, ids, to)
}

func (s managerSyncer) SetFlags(ctx context.Context, ids []int64, add, remove []domain.Flag) error {
	return s.m.Actions().SetFlags(ctx, ids, add, remove)
}

func (s managerSyncer) Delete(ctx context.Context, ids []int64, permanent bool) error {
	if permanent {
		return s.m.Actions().DeletePermanently(ctx, ids)
	}
	return s.m.Actions().Delete(ctx, ids)
}

// FetchBody loads a raw message on demand (bodies not downloaded during sync).
func (s managerSyncer) FetchBody(ctx context.Context, messageID int64) ([]byte, error) {
	return s.m.FetchBody(ctx, messageID)
}

// bodyFetcher is implemented by syncers that can download bodies on demand.
type bodyFetcher interface {
	FetchBody(ctx context.Context, messageID int64) ([]byte, error)
}

// SyncManager returns the sync manager (nil when sync is not enabled).
func (a *App) SyncManager() *tsync.Manager { return a.mgr }

func dataID(ev events.Event, key string) int64 {
	v, _ := strconv.ParseInt(ev.Data[key], 10, 64)
	return v
}

// publishSyncEvent maps the UI topics of internal/sync to ApiEvents.
func (a *App) publishSyncEvent(ctx context.Context, ev events.Event) {
	switch ev.Type {
	case tsync.EventAccountStatus:
		if id := dataID(ev, "accountId"); id != 0 && a.mgr != nil {
			st, _ := a.mgr.Status(id)
			dto := statusDTO(st)
			a.hub.Publish(Event{Type: EventSyncStatus, AccountID: id, Sync: &dto})
		}
	case tsync.EventFoldersChanged:
		if id := dataID(ev, "accountId"); id != 0 {
			a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
		}
	case tsync.EventConversationUpdated:
		a.invalidateViews()
		a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: dataID(ev, "conversationId")})
	case tsync.EventMessageUpdated:
		id := dataID(ev, "messageId")
		switch ev.Data["reason"] {
		case "added":
			a.NotifyMessageAdded(ctx, id)
		case "removed", "deleted":
			a.NotifyMessageRemoved(ctx, id)
		default:
			a.hub.Publish(Event{Type: EventMessageUpdated, MessageID: id})
		}
	case tsync.EventFolderUpdated:
		a.hub.Publish(Event{Type: EventFolderUpdated, FolderID: dataID(ev, "folderId"), AccountID: ev.AccountID})
	case tsync.EventOpFailed:
		a.log.Warn("app: operation failed permanently", "op", ev.Data["opId"], "kind", ev.Data["kind"], "err", ev.Data["error"])
		a.invalidateViews()
	}
}
