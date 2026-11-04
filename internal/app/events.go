// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Midas1989/Mailclient/internal/events"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
)

// Hub fans out UI events (ApiEvent in types.ts) to subscribers: the
// WebSocket connections of a user in server mode, the Wails runtime in the
// desktop build. Slow subscribers lose events instead of blocking the core;
// the UI reloads lists on the next event anyway.
type Hub struct {
	mu   sync.Mutex
	subs map[*hubSub]struct{}
}

type hubSub struct {
	ch chan Event
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*hubSub]struct{}{}} }

// Subscribe returns a channel of events until ctx is done or cancel is called.
func (h *Hub) Subscribe(ctx context.Context, buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	s := &hubSub{ch: make(chan Event, buffer)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, s)
			h.mu.Unlock()
			close(s.ch)
		})
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return s.ch, cancel
}

// Publish sends ev to all subscribers without blocking.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
		}
	}
}

// Subscribers returns the number of subscribers.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// bridgeBus translates core events (tag/project/search folder live updates,
// new mail and chat messages) into UI events until ctx is done.
func (a *App) bridgeBus(ctx context.Context) {
	types := []events.Type{
		events.TagChanged, events.TagAdded, events.TagRemoved,
		events.ProjectChanged, events.ProjectItemAdded, events.ProjectStatusChanged,
		events.SearchFolderChanged, events.SearchFolderMatched,
		events.MailReceived, events.ChatReceived,
	}
	sub := a.bus.Subscribe(ctx, events.SubscribeOptions{Name: "app-ui", Types: append(types, tsync.UITypes...), Buffer: 512})
	defer sub.Unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			a.publishCoreEvent(ctx, ev)
		}
	}
}

func (a *App) publishCoreEvent(ctx context.Context, ev events.Event) {
	switch ev.Type {
	case events.TagChanged, events.TagAdded, events.TagRemoved:
		a.hub.Publish(Event{Type: EventTagsChanged})
	case events.ProjectChanged, events.ProjectItemAdded, events.ProjectStatusChanged:
		if ev.Project != nil {
			a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: ev.Project.ProjectID})
		}
	case events.SearchFolderChanged, events.SearchFolderMatched:
		a.hub.Publish(Event{Type: EventSearchFoldersChange})
	case tsync.EventAccountStatus, tsync.EventConversationUpdated, tsync.EventMessageUpdated,
		tsync.EventFolderUpdated, tsync.EventFoldersChanged, tsync.EventOpFailed:
		a.publishSyncEvent(ctx, ev)
	case events.MailReceived, events.ChatReceived:
		// The sync manager publishes UI events itself (Notify*); the bus
		// events only trigger a re-index of conversation counterparts.
		a.invalidateViews()
	}
}

// NotifyMessageAdded is called by the sync manager after a new mail was
// stored (wiring point for step A).
func (a *App) NotifyMessageAdded(ctx context.Context, messageID int64) {
	a.invalidateViews()
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return
	}
	folders, _ := a.st.FoldersFor(ctx, []int64{messageID})
	conv := m.ConversationID
	if conv == 0 {
		conv, _ = a.ensureThread(ctx, m)
	}
	a.hub.Publish(Event{Type: EventMessageAdded, MessageID: messageID, ConversationID: conv, FolderIDs: nonNilIDs(folders[messageID])})
	for _, f := range folders[messageID] {
		a.publishFolder(ctx, f)
	}
}

// NotifyMessageUpdated is called after flags or locations of a mail changed.
func (a *App) NotifyMessageUpdated(ctx context.Context, messageID int64) {
	a.hub.Publish(Event{Type: EventMessageUpdated, MessageID: messageID})
}

// NotifyMessageRemoved is called after a mail was removed.
func (a *App) NotifyMessageRemoved(ctx context.Context, messageID int64) {
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventMessageRemoved, MessageID: messageID})
}

// NotifyFolderUpdated is called after the counters of a folder changed.
func (a *App) NotifyFolderUpdated(ctx context.Context, folderID int64) {
	a.publishFolder(ctx, folderID)
}

// NotifyAccountUpdated is called after an account (state, folders) changed.
func (a *App) NotifyAccountUpdated(ctx context.Context, accountID int64) {
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: accountID})
}

// NotifyConversationUpdated is called after a chat message arrived or a
// conversation changed.
func (a *App) NotifyConversationUpdated(ctx context.Context, conversationID int64, itemID string) {
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: conversationID, ItemID: itemID})
}

// NotifyConnection reports the connection state to the UI.
func (a *App) NotifyConnection(state string) {
	a.hub.Publish(Event{Type: EventConnection, State: state})
}

func (a *App) publishFolder(ctx context.Context, folderID int64) {
	f, err := a.st.GetFolder(ctx, folderID)
	if err != nil {
		return
	}
	a.hub.Publish(Event{Type: EventFolderUpdated, FolderID: folderID, AccountID: f.AccountID})
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// MarshalJSON writes exactly the fields of the event type (types.ts ApiEvent).
func (e Event) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": e.Type}
	switch e.Type {
	case EventMessageAdded:
		m["messageId"], m["conversationId"], m["folderIds"] = e.MessageID, e.ConversationID, nonNilIDs(e.FolderIDs)
	case EventMessageUpdated, EventMessageRemoved:
		m["messageId"] = e.MessageID
	case EventConversationUpdated:
		m["conversationId"] = e.ConversationID
		if e.ItemID != "" {
			m["itemId"] = e.ItemID
		}
	case EventFolderUpdated:
		m["folderId"], m["accountId"] = e.FolderID, e.AccountID
	case EventAccountUpdated:
		m["accountId"] = e.AccountID
	case EventConnection:
		m["state"] = e.State
	case EventProjectUpdated:
		m["projectId"] = e.ProjectID
	case EventWorkflowRun:
		m["workflowId"], m["run"] = e.WorkflowID, e.Run
	case EventMessengerTyping:
		m["conversationId"] = e.ConversationID
	}
	for k, v := range e.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}
