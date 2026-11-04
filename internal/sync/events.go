// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"strconv"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// UI live-update topics published by the sync engine (in addition to the
// workflow topics mail.received, mail.moved and mail.flagged). They are
// not workflow triggers. Their payload is Event.Data with the keys listed.
const (
	// EventAccountStatus: accountId, state, error, nextRetry (RFC 3339),
	// lastSync (RFC 3339), folder (folder being synced), folderId, done,
	// total (progress of that folder), initial ("1" during its first sync).
	// Progress updates are throttled (ProgressInterval).
	EventAccountStatus events.Type = "account.status"
	// EventConversationUpdated: conversationId.
	EventConversationUpdated events.Type = "conversation.updated"
	// EventMessageUpdated: messageId, conversationId, reason (flags, moved,
	// deleted, content, added, removed).
	EventMessageUpdated events.Type = "message.updated"
	// EventFolderUpdated: folderId, total, unread.
	EventFolderUpdated events.Type = "folder.updated"
	// EventFoldersChanged: accountId (folders were added, renamed or deleted).
	EventFoldersChanged events.Type = "folders.changed"
	// EventOpFailed: opId, kind, error (an operation failed permanently and
	// its local effect was reverted).
	EventOpFailed events.Type = "op.failed"
)

// UITypes lists the UI topics of the sync engine.
var UITypes = []events.Type{EventAccountStatus, EventConversationUpdated, EventMessageUpdated, EventFolderUpdated,
	EventFoldersChanged, EventOpFailed}

func id(v int64) string { return strconv.FormatInt(v, 10) }

func (m *Manager) publish(ctx context.Context, ev events.Event) {
	if m.cfg.Bus == nil {
		return
	}
	if ev.Source == "" {
		ev.Source = "sync"
	}
	if ev.Chain == nil && ctx != nil {
		ev.Chain = workflow.EventChain(ctx)
	}
	if ev.Time.IsZero() {
		ev.Time = m.now().UTC()
	}
	m.cfg.Bus.Publish(ev)
}

func (m *Manager) publishConversations(ctx context.Context, ids ...int64) {
	seen := map[int64]bool{}
	for _, c := range ids {
		if c == 0 || seen[c] {
			continue
		}
		seen[c] = true
		m.publish(ctx, events.Event{Type: EventConversationUpdated, Data: map[string]string{"conversationId": id(c)}})
	}
}

func (m *Manager) publishMessage(ctx context.Context, accountID, messageID, conversationID int64, reason string) {
	m.publish(ctx, events.Event{Type: EventMessageUpdated, AccountID: accountID, Data: map[string]string{
		"messageId": id(messageID), "conversationId": id(conversationID), "reason": reason}})
}

func (m *Manager) publishFolder(ctx context.Context, accountID, folderID int64, total, unread int) {
	m.publish(ctx, events.Event{Type: EventFolderUpdated, AccountID: accountID, Data: map[string]string{
		"folderId": id(folderID), "total": strconv.Itoa(total), "unread": strconv.Itoa(unread)}})
}

func folderListEvent(accountID int64) events.Event {
	return events.Event{Type: EventFoldersChanged, AccountID: accountID, Data: map[string]string{"accountId": id(accountID)}}
}

func (m *Manager) publishStatus(st AccountStatus) {
	data := map[string]string{"accountId": id(st.AccountID), "state": string(st.State), "error": st.LastError,
		"folder": st.Folder}
	if st.FolderID != 0 {
		data["folderId"] = id(st.FolderID)
	}
	if st.Total > 0 || st.Done > 0 {
		data["done"], data["total"] = strconv.Itoa(st.Done), strconv.Itoa(st.Total)
	}
	if st.Initial {
		data["initial"] = "1"
	}
	if !st.NextRetry.IsZero() {
		data["nextRetry"] = st.NextRetry.UTC().Format(time.RFC3339)
	}
	if !st.LastSync.IsZero() {
		data["lastSync"] = st.LastSync.UTC().Format(time.RFC3339)
	}
	m.publish(context.Background(), events.Event{Type: EventAccountStatus, AccountID: st.AccountID, Data: data})
}
