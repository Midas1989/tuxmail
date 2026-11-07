// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
)

// Mails kept with header only ("Nur Kopf", mail.headersOnlyOlderThanDays):
// on this device, mails older than the day count are stored with sender,
// subject, date and a preview; the content is loaded on opening. The global
// day count is a setting of this device (Einstellungen > Mail > Speicher),
// never synced to the server; accounts may have their own
// (AccountSettings.HeadersOnlyOlderThanDays). In the mode "Auf diesem Gerät"
// the sync engine follows it (internal/sync/headersonly.go), in the mode
// "Über den Server" the read cache does: older mails keep their list entry
// and preview, not their content. The server itself always loads everything.

// MailStorageSettings is the storage section of the mail settings.
type MailStorageSettings struct {
	// HeadersOnlyOlderThanDays: -1 = not set (default: as before, the last
	// 30 days completely, older mails on opening), 0 = all mails completely,
	// n = header and preview only for mails older than n days.
	HeadersOnlyOlderThanDays int `json:"headersOnlyOlderThanDays"`
	// Available: this core keeps mail itself (desktop app).
	Available bool `json:"available"`
	// HeadersOnly and Complete count the mails of this device's own accounts
	// stored with header only and with content (informational).
	HeadersOnly int `json:"headersOnly"`
	Complete    int `json:"complete"`
	// Pending and PendingBytes estimate what is still to be downloaded under
	// the current setting (output only); DownloadPaused pauses that download.
	Pending        int   `json:"pending"`
	PendingBytes   int64 `json:"pendingBytes"`
	DownloadPaused bool  `json:"downloadPaused"`
}

// MailStorageEstimate is what a day count would download (Rückfrage before
// switching to "all" or a larger count).
type MailStorageEstimate struct {
	Messages int   `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

const keyHeadersOnlyPaused = "mail.headersOnlyDownloadPaused"

// legacyBodyDays is the age limit before the day count existed (sync default).
const legacyBodyDays = 30

// downloadWindow is the date since which bodies are downloaded for a day
// count (-1: as before, 0: all = zero time).
func (a *App) downloadWindow(days int) time.Time {
	switch {
	case days == 0:
		return time.Time{}
	case days < 0:
		return a.now().AddDate(0, 0, -legacyBodyDays)
	}
	return a.now().AddDate(0, 0, -days)
}

// maxSyncBody is the sync limit of a body (tsync default).
const maxSyncBody = 50 << 20

// EstimateMailStorage estimates how many mails (and bytes) a day count would
// download that are not stored yet.
func (a *App) EstimateMailStorage(ctx context.Context, days int) (MailStorageEstimate, error) {
	if days < -1 || days > tsync.MaxHeadersOnlyDays {
		return MailStorageEstimate{}, invalid("days must be -1 or between 0 and %d", tsync.MaxHeadersOnlyDays)
	}
	n, b, err := a.st.PendingBodies(ctx, a.downloadWindow(days), maxSyncBody)
	return MailStorageEstimate{Messages: n, Bytes: b}, err
}

// EstimateMailStorage is the Wails binding.
func (x *Api) EstimateMailStorage(days int) (MailStorageEstimate, error) {
	return r1(x.app.EstimateMailStorage(x.ctx, days))
}

func (a *App) downloadPaused() bool { return a.hoPaused.Load() }

// headersOnlyDays is the global day count (-1 = not set, 0 = all); the
// sync engine reads it through Config.HeadersOnlyDays.
func (a *App) headersOnlyDays() int { return int(a.hoDays.Load()) }

func (a *App) loadHeadersOnlyDays(ctx context.Context) {
	var paused bool
	if ok, err := a.st.GetAppSetting(ctx, keyHeadersOnlyPaused, &paused); err == nil && ok {
		a.hoPaused.Store(paused)
	}
	a.hoDays.Store(-1)
	var n int
	if ok, err := a.st.GetAppSetting(ctx, store.KeyHeadersOnlyDays, &n); err == nil && ok && n >= 0 && n <= tsync.MaxHeadersOnlyDays {
		a.hoDays.Store(int64(n))
	}
}

// headersOnlyAvailable reports whether this core may keep mails of an
// account kind with header only: not on the server (it loads everything),
// only for accounts whose bodies can be loaded again (not POP3, not local
// folders).
func (a *App) headersOnlyAvailable(kind domain.AccountKind) bool {
	return a.opts.Mode != ModeServer && slices.Contains(store.HeadersOnlyKinds, string(kind))
}

// GetMailStorageSettings returns the storage settings of this device.
func (a *App) GetMailStorageSettings(ctx context.Context) (MailStorageSettings, error) {
	out := MailStorageSettings{HeadersOnlyOlderThanDays: a.headersOnlyDays(), Available: a.opts.Mode != ModeServer, DownloadPaused: a.downloadPaused()}
	if out.Available && out.HeadersOnlyOlderThanDays >= 0 { // only downloads the setting causes
		var err error
		if out.Pending, out.PendingBytes, err = a.st.PendingBodies(ctx, a.downloadWindow(out.HeadersOnlyOlderThanDays), maxSyncBody); err != nil {
			return out, err
		}
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return out, err
	}
	for _, acc := range accs {
		if !slices.Contains(store.HeadersOnlyKinds, string(acc.Kind)) {
			continue
		}
		h, c, err := a.st.HeadersOnlyStats(ctx, acc.ID)
		if err != nil {
			return out, err
		}
		out.HeadersOnly += h
		out.Complete += c
	}
	return out, nil
}

// SetMailStorageSettings stores the day count of this device. Making it
// smaller (or switching it on) releases the content of older mails in the
// background; making it larger downloads it again.
func (a *App) SetMailStorageSettings(ctx context.Context, in MailStorageSettings) (MailStorageSettings, error) {
	if a.opts.Mode == ModeServer {
		return MailStorageSettings{}, Errorf(http.StatusForbidden, "forbidden", "the server always keeps every mail completely")
	}
	n := in.HeadersOnlyOlderThanDays
	if n < -1 || n > tsync.MaxHeadersOnlyDays {
		return MailStorageSettings{}, invalid("headersOnlyOlderThanDays must be -1 or between 0 and %d", tsync.MaxHeadersOnlyDays)
	}
	if n == -1 {
		if err := a.st.DeleteAppSetting(ctx, store.KeyHeadersOnlyDays); err != nil {
			return MailStorageSettings{}, err
		}
	} else if err := a.st.SetAppSetting(ctx, store.KeyHeadersOnlyDays, n); err != nil {
		return MailStorageSettings{}, err
	}
	a.hoDays.Store(int64(n))
	if in.DownloadPaused != a.downloadPaused() {
		if err := a.st.SetAppSetting(ctx, keyHeadersOnlyPaused, in.DownloadPaused); err != nil {
			return MailStorageSettings{}, err
		}
		a.hoPaused.Store(in.DownloadPaused)
	}
	if a.mgr != nil {
		a.mgr.HeadersOnlyChanged()
	}
	if n > 0 && a.connCurrent() != nil {
		if err := a.releaseRemoteCacheBodies(ctx); err != nil {
			a.log.Warn("app: releasing older mails from the read cache failed", "err", err)
		}
	}
	return a.GetMailStorageSettings(ctx)
}

// GetMailStorageSettings is the Wails binding (desktop only: the server has
// no such setting, the day count is a matter of this device).
func (x *Api) GetMailStorageSettings() (MailStorageSettings, error) {
	return r1(x.app.GetMailStorageSettings(x.ctx))
}

// SetMailStorageSettings is the Wails binding.
func (x *Api) SetMailStorageSettings(in MailStorageSettings) (MailStorageSettings, error) {
	return r1(x.app.SetMailStorageSettings(x.ctx, in))
}

// ==== Read cache of the mode "Über den Server" ===================================

// headersOnlyCutoff is the date before which mails are kept with header
// only (zero: none).
func (a *App) headersOnlyCutoff() time.Time {
	n := a.headersOnlyDays()
	if n <= 0 {
		return time.Time{}
	}
	return a.now().AddDate(0, 0, -n)
}

// remoteHeadersOnly reports whether a mail of this date keeps only its list
// entry and preview in the read cache.
func (a *App) remoteHeadersOnly(date string) bool {
	cut := a.headersOnlyCutoff()
	if cut.IsZero() {
		return false
	}
	t, err := time.Parse(time.RFC3339, date)
	return err == nil && t.Before(cut)
}

// releaseRemoteCacheBodies removes the kept content (detail, attachments,
// inline images, loaded images) of mails older than the day count from the
// read cache; their list entry and preview stay searchable.
func (a *App) releaseRemoteCacheBodies(ctx context.Context) error {
	if a.headersOnlyCutoff().IsZero() {
		return nil
	}
	return a.st.ReleaseRemoteCachedBodies(ctx, func(id int64, date, summaryJSON string) (string, bool) {
		if !a.remoteHeadersOnly(date) {
			return "", false
		}
		var m MessageSummary
		if json.Unmarshal([]byte(summaryJSON), &m) != nil {
			return "", true
		}
		return messageSearchText(&m, ""), true
	})
}

// offlineHeadersOnly answers the detail of a mail whose content is not kept
// with what is: header and preview, marked as content only online.
func (a *App) offlineHeadersOnly(ctx context.Context, id int64) (*connResult, bool) {
	js, ok, err := a.st.RemoteCachedSummary(ctx, id)
	if err != nil || !ok {
		return nil, false
	}
	var d MessageDetail
	if json.Unmarshal([]byte(js), &d.MessageSummary) != nil || d.ID == 0 {
		return nil, false
	}
	d.InReplyTo, d.References, d.Attachments = []string{}, []string{}, []Attachment{}
	d.Text = d.Preview
	d.HeadersOnly, d.ContentUnavailable = true, true
	return jsonResult(http.StatusOK, d, true), true
}

// ==== Server search =============================================================

// serverSearchTimeout bounds the search on the mail servers: the local
// result is never held back for long.
const serverSearchTimeout = 4 * time.Second

// serverSearchHits adds the mails kept with header only that the mail
// servers (IMAP SEARCH) find by their content, which is not in the local
// index. Only for a plain text search of mails, first page, when the search
// may ask the server (location auto or server); it returns how many hits
// it added.
func (a *App) serverSearchHits(ctx context.Context, q SearchQuery, out *SearchResult) int {
	text := strings.TrimSpace(q.Text)
	if a.mgr == nil || text == "" || q.Mode == "expert" || q.Location == "local" || q.Cursor != "" || a.cr.localPaused.Load() {
		return 0
	}
	if len(q.Scopes) > 0 && !slices.Contains(q.Scopes, string(store.ScopeMail)) {
		return 0
	}
	sctx, cancel := context.WithTimeout(ctx, serverSearchTimeout)
	defer cancel()
	ids := a.mgr.ServerSearch(sctx, q.AccountIDs, text, 50)
	have := map[int64]bool{}
	for _, h := range out.Hits {
		if h.MessageID != 0 {
			have[h.MessageID] = true
		}
	}
	var add []int64
	for _, id := range ids {
		if !have[id] {
			add = append(add, id)
		}
	}
	if len(add) == 0 {
		return 0
	}
	msgs, err := a.st.GetMessages(ctx, add)
	if err != nil {
		return 0
	}
	folders, _ := a.st.FoldersFor(ctx, add)
	n := 0
	for _, m := range msgs {
		if len(q.FolderIDs) > 0 && !slices.ContainsFunc(folders[m.ID], func(f int64) bool { return slices.Contains(q.FolderIDs, f) }) {
			continue
		}
		hit := SearchHit{ID: fmt.Sprintf("%s:%d", store.ScopeMail, m.ID), Kind: string(store.ScopeMail), Title: m.Subject, Snippet: m.Snippet,
			Highlights: [][2]int{}, TitleHighlights: [][2]int{}, Date: rfc3339(msgTime(m)), AccountID: m.AccountID, Channel: "email",
			Sender: m.From, Size: m.Size, HasAttachments: m.HasAttachments, Unread: m.Flags&store.FlagSeen == 0, MessageID: m.ID,
			ConversationID: m.ConversationID, ItemID: mailItemID(m.ID), HeadersOnly: true, ServerMatch: true}
		if fs := folders[m.ID]; len(fs) > 0 {
			hit.FolderID = fs[0]
		}
		out.Hits = append(out.Hits, hit)
		n++
	}
	return n
}
