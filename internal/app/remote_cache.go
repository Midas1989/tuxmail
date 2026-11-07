// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// The read cache of the server mode: what was last loaded stays readable
// without a connection. It holds whole server responses (the same bytes the
// interface would get) plus an index of the loaded messages for lists and
// searching. It is bounded (LRU) and can be deleted when disconnecting.

const (
	defaultRemoteCacheMax = 512 << 20
	remoteCacheMaxEntry   = 20 << 20
	remoteSearchTextMax   = 64 << 10
)

// remoteCacheRoutes lists what may be kept. Only mail, calendar, contacts and
// what the interface needs to start: not sessions, passwords, keys, AI
// settings or anything from the administration.
var remoteCacheRoutes = regexp.MustCompile(`^/api/(` +
	`accounts(/\d+/(folders|folder-config))?|identities|signatures|persons(/frequent)?|` +
	`messages(/\d+(/conversation|/attachments/\d+|/cid/[^/]+|/invitations|/remote-content)?)?|` +
	`conversations(/\d+(/items|/draft)?)?|tags|projects|search-folders|search/profiles|preferences|` +
	`locales(/[\w-]+)?|calendars|calendar-events|tasks|addressbooks|contacts(/\d+(/photo)?)?|contact-groups|` +
	`chat-folders|snippets|focus|starred|scheduled|workflows|auth/me|auth/config|autocomplete` +
	`)$`)

func remoteCacheable(path string) bool { return remoteCacheRoutes.MatchString(path) }

// cacheKey is path + sorted query, so that the order of parameters does not matter.
func cacheKey(u *url.URL) string {
	if q := u.Query().Encode(); q != "" {
		return u.Path + "?" + q
	}
	return u.Path
}

func (a *App) remoteCacheMax() int64 {
	if a.opts.RemoteCacheMax > 0 {
		return a.opts.RemoteCacheMax
	}
	return defaultRemoteCacheMax
}

func (a *App) cacheResponse(ctx context.Context, u *url.URL, res *connResponse) {
	if len(res.Body) > remoteCacheMaxEntry || a.connCurrent() == nil {
		return
	}
	now := a.now()
	var err error
	if a.remoteContentOld(ctx, u, res.Body) {
		// Older than mail.headersOnlyOlderThanDays: only the list entry and
		// the preview are kept (headersonly.go).
		err = a.indexMessages(ctx, u, res.Body, now, true)
	} else {
		err = a.st.PutRemoteCache(ctx, store.RemoteCacheEntry{Key: cacheKey(u), ContentType: res.Header.Get("Content-Type"),
			Disposition: res.Header.Get("Content-Disposition"), Body: res.Body}, now)
		if err == nil {
			err = a.indexMessages(ctx, u, res.Body, now, false)
		}
	}
	// Measuring the cache takes a moment: not on every answer.
	if err == nil && a.cr.cacheWritten.Add(int64(len(res.Body))) > min(a.remoteCacheMax()/8, 16<<20) {
		a.cr.cacheWritten.Store(0)
		_, err = a.st.PruneRemoteCache(ctx, a.remoteCacheMax())
	}
	if err != nil {
		a.log.Warn("app: updating the read cache failed", "err", err) // reading still works, online
	}
}

var (
	reMessageDetail = regexp.MustCompile(`^/api/messages/(\d+)$`)
	reConvItems     = regexp.MustCompile(`^/api/conversations/\d+/items$`)
	reMessageExtras = regexp.MustCompile(`^/api/messages/\d+/`)
)

func messageSearchText(m *MessageSummary, body string) string {
	var b strings.Builder
	b.WriteString(m.Subject)
	for _, l := range [][]Address{m.From, m.To, m.Cc} {
		for _, ad := range l {
			b.WriteString(" " + ad.Name + " " + ad.Email)
		}
	}
	b.WriteString(" " + m.Preview + " " + body)
	s := strings.ToLower(b.String())
	if len(s) > remoteSearchTextMax {
		s = strings.ToValidUTF8(s[:remoteSearchTextMax], "")
	}
	return s
}

func cachedMessage(m *MessageSummary, body string) store.RemoteCachedMessage {
	js, _ := json.Marshal(m)
	folders := m.FolderIDs
	if folders == nil {
		folders = []int64{}
	}
	return store.RemoteCachedMessage{ID: m.ID, AccountID: m.AccountID, FolderIDs: folders, Date: m.Date, SummaryJSON: string(js), SearchText: messageSearchText(m, body)}
}

var reMessageContent = regexp.MustCompile(`^/api/messages/(\d+)/(attachments/\d+|cid/[^/]+|remote-content)$`)

// remoteContentOld reports whether an answer is the content of a mail older
// than mail.headersOnlyOlderThanDays: it is not kept for reading offline.
func (a *App) remoteContentOld(ctx context.Context, u *url.URL, body []byte) bool {
	if a.headersOnlyCutoff().IsZero() {
		return false
	}
	if reMessageDetail.MatchString(u.Path) {
		var m MessageSummary
		return json.Unmarshal(body, &m) == nil && a.remoteHeadersOnly(m.Date)
	}
	if mm := reMessageContent.FindStringSubmatch(u.Path); mm != nil {
		id, _ := strconv.ParseInt(mm[1], 10, 64)
		date, err := a.st.RemoteCachedDate(ctx, id)
		return err == nil && a.remoteHeadersOnly(date)
	}
	return false
}

func (a *App) indexMessages(ctx context.Context, u *url.URL, body []byte, now time.Time, headersOnly bool) error {
	switch {
	case u.Path == "/api/messages":
		var p Page[MessageSummary]
		if err := json.Unmarshal(body, &p); err != nil {
			return nil // another shape: only the exact answer is kept
		}
		msgs := make([]store.RemoteCachedMessage, 0, len(p.Items))
		for i := range p.Items {
			msgs = append(msgs, cachedMessage(&p.Items[i], ""))
		}
		return a.st.PutRemoteMessages(ctx, msgs, now)
	case reMessageDetail.MatchString(u.Path):
		var d MessageDetail
		if err := json.Unmarshal(body, &d); err != nil || d.ID == 0 {
			return nil
		}
		text := d.Text
		if text == "" {
			text = stripTags(d.HTML)
		}
		if headersOnly {
			cm := cachedMessage(&d.MessageSummary, "")
			cm.ReplaceText = true
			return a.st.PutRemoteMessages(ctx, []store.RemoteCachedMessage{cm}, now)
		}
		return a.st.PutRemoteMessages(ctx, []store.RemoteCachedMessage{cachedMessage(&d.MessageSummary, text)}, now)
	}
	return nil
}

var reTags = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return reTags.ReplaceAllString(s, " ") }

// ==== Reading without a connection =============================================

func (a *App) offlineRead(ctx context.Context, u *url.URL) (*connResult, error) {
	if !remoteCacheable(u.Path) {
		return offlineUnavailable(), nil
	}
	now := a.now()
	e, ok, err := a.st.GetRemoteCache(ctx, cacheKey(u), now)
	if err != nil {
		return nil, err
	}
	if ok {
		body := a.overlayPending(ctx, u, e.Body)
		h := http.Header{headerOffline: {"1"}}
		if e.ContentType != "" {
			h.Set("Content-Type", e.ContentType)
		}
		if e.Disposition != "" {
			h.Set("Content-Disposition", e.Disposition)
		}
		return &connResult{status: http.StatusOK, header: h, body: body}, nil
	}
	return a.offlineFallback(ctx, u)
}

// offlineFallback answers lists from what is known; the exact question was
// never asked online (other page size, other week, a search). An empty list
// is honest here: the UI marks the whole app as offline.
func (a *App) offlineFallback(ctx context.Context, u *url.URL) (*connResult, error) {
	q := u.Query()
	switch {
	case u.Path == "/api/messages":
		return a.offlineMessages(ctx, u)
	case u.Path == "/api/conversations":
		return a.offlineConversations(ctx, u)
	case reConvItems.MatchString(u.Path):
		return a.offlineUnion(ctx, u.Path, q, nil)
	case u.Path == "/api/calendar-events":
		from, to := q.Get("from"), q.Get("to")
		ids := splitIDs(q.Get("calendarIds"))
		return a.offlineUnion(ctx, u.Path, nil, func(o map[string]any) bool {
			if len(ids) > 0 && !slices.Contains(ids, jsonInt(o["calendarId"])) {
				return false
			}
			return overlaps(jsonStr(o["start"]), jsonStr(o["end"]), from, to)
		})
	case u.Path == "/api/tasks":
		return a.offlineUnion(ctx, u.Path, nil, textFilter(q.Get("q")))
	case u.Path == "/api/contacts":
		book := int64(0)
		if v, err := strconv.ParseInt(q.Get("addressBookId"), 10, 64); err == nil {
			book = v
		}
		txt := textFilter(q.Get("q"))
		return a.offlineUnion(ctx, u.Path, nil, func(o map[string]any) bool {
			return (book == 0 || jsonInt(o["addressBookId"]) == book) && txt(o)
		})
	case u.Path == "/api/autocomplete":
		return a.offlineAutocomplete(ctx, q.Get("q"), q.Get("limit"))
	case reMessageDetail.MatchString(u.Path):
		// Content not kept (older than the day count, or never opened):
		// header and preview, marked "Inhalt nur online verfügbar".
		id, _ := strconv.ParseInt(reMessageDetail.FindStringSubmatch(u.Path)[1], 10, 64)
		if res, ok := a.offlineHeadersOnly(ctx, id); ok {
			return res, nil
		}
		return offlineUnavailable(), nil
	case reMessageExtras.MatchString(u.Path):
		return offlineUnavailable(), nil
	}
	if strings.Count(u.Path, "/") == 2 || u.Path == "/api/search/profiles" || u.Path == "/api/persons/frequent" || strings.HasSuffix(u.Path, "/folders") {
		switch u.Path {
		case "/api/preferences", "/api/focus", "/api/auth/me", "/api/auth/config":
			return offlineUnavailable(), nil
		}
		return jsonResult(http.StatusOK, []any{}, true), nil // lists
	}
	return offlineUnavailable(), nil
}

func splitIDs(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}

func jsonInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// overlaps compares ISO 8601 times of one format family; unreadable values count as overlapping.
func overlaps(start, end, from, to string) bool {
	parse := func(s string) (time.Time, bool) {
		for _, l := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(l, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	st, ok1 := parse(start)
	en, ok2 := parse(end)
	if !ok2 {
		en, ok2 = st, ok1
	}
	f, ok3 := parse(from)
	t, ok4 := parse(to)
	if !ok1 || !ok2 {
		return true
	}
	return (!ok4 || st.Before(t)) && (!ok3 || !en.Before(f))
}

func textFilter(text string) func(map[string]any) bool {
	words := strings.Fields(strings.ToLower(text))
	return func(o map[string]any) bool {
		if len(words) == 0 {
			return true
		}
		b, _ := json.Marshal(o)
		s := strings.ToLower(string(b))
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	}
}

// offlineUnion merges all kept list answers of a route, without duplicates.
func (a *App) offlineUnion(ctx context.Context, path string, _ url.Values, keep func(map[string]any) bool) (*connResult, error) {
	bodies, err := a.st.ListRemoteCacheBodies(ctx, path, 200)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	for _, b := range bodies {
		var list []map[string]any
		if json.Unmarshal(b, &list) != nil {
			continue
		}
		for _, o := range list {
			id := fmt.Sprint(o["id"])
			if seen[id] || (keep != nil && !keep(o)) {
				continue
			}
			seen[id] = true
			out = append(out, o)
		}
	}
	return jsonResult(http.StatusOK, out, true), nil
}

func (a *App) rolefolders(ctx context.Context, role string) []int64 {
	bodies, err := a.st.ListRemoteCacheBodies(ctx, "/api/accounts/", 500)
	if err != nil {
		return nil
	}
	var ids []int64
	for _, b := range bodies {
		var list []struct {
			ID   int64  `json:"id"`
			Role string `json:"role"`
		}
		if json.Unmarshal(b, &list) != nil {
			continue
		}
		for _, f := range list {
			if f.Role == role {
				ids = append(ids, f.ID)
			}
		}
	}
	return ids
}

func (a *App) offlineMessages(ctx context.Context, u *url.URL) (*connResult, error) {
	q := u.Query()
	mq := store.RemoteMessageQuery{Text: q.Get("q")}
	mq.Offset, _ = strconv.Atoi(q.Get("offset"))
	mq.Limit, _ = strconv.Atoi(q.Get("limit"))
	switch q.Get("scope") {
	case "folder":
		mq.FolderID, _ = strconv.ParseInt(q.Get("folderId"), 10, 64)
	case "unified":
		mq.FolderIDs = a.rolefolders(ctx, q.Get("role"))
		if len(mq.FolderIDs) == 0 {
			return jsonResult(http.StatusOK, Page[json.RawMessage]{Items: []json.RawMessage{}}, true), nil
		}
	case "tag", "searchFolder":
		return jsonResult(http.StatusOK, Page[json.RawMessage]{Items: []json.RawMessage{}}, true), nil
	}
	rows, total, err := a.st.ListRemoteMessages(ctx, mq)
	if err != nil {
		return nil, err
	}
	items := make([]json.RawMessage, 0, len(rows))
	for _, r := range rows {
		items = append(items, json.RawMessage(r))
	}
	res := jsonResult(http.StatusOK, Page[json.RawMessage]{Items: items, Total: total, Offset: mq.Offset}, true)
	res.body = a.overlayPending(ctx, u, res.body)
	return res, nil
}

// offlineConversations uses the newest kept list of the same view, whatever its paging.
func (a *App) offlineConversations(ctx context.Context, u *url.URL) (*connResult, error) {
	want := u.Query()
	empty := jsonResult(http.StatusOK, Page[json.RawMessage]{Items: []json.RawMessage{}}, true)
	if want.Get("offset") != "" && want.Get("offset") != "0" {
		return empty, nil // no further pages offline
	}
	entries, err := a.st.ListRemoteCacheEntries(ctx, "/api/conversations?", 100)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		eu, err := url.Parse(e.Key)
		if err != nil {
			continue
		}
		q := eu.Query()
		same := true
		for _, k := range []string{"scope", "accountId", "folderId", "grouping", "role", "tagId", "searchFolderId", "q"} {
			if q.Get(k) != want.Get(k) {
				same = false
			}
		}
		if same {
			return &connResult{status: http.StatusOK, header: http.Header{"Content-Type": {"application/json"}, headerOffline: {"1"}}, body: a.overlayPending(ctx, u, e.Body)}, nil
		}
	}
	return empty, nil
}

func (a *App) offlineAutocomplete(ctx context.Context, text, limit string) (*connResult, error) {
	res, err := a.offlineUnion(ctx, "/api/contacts", nil, textFilter(text))
	if err != nil {
		return nil, err
	}
	var contacts []struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"displayName"`
		Emails      []struct {
			Value string `json:"value"`
		} `json:"emails"`
	}
	_ = json.Unmarshal(res.body, &contacts)
	n, _ := strconv.Atoi(limit)
	if n <= 0 {
		n = 10
	}
	out := []AddressSuggestion{}
	for _, c := range contacts {
		for _, e := range c.Emails {
			if len(out) < n && e.Value != "" {
				out = append(out, AddressSuggestion{Name: c.DisplayName, Email: e.Value, ContactID: c.ID, Source: "contact"})
			}
		}
	}
	return jsonResult(http.StatusOK, out, true), nil
}

// offlineSearch searches the loaded messages.
func (a *App) offlineSearch(ctx context.Context, body []byte) (*connResult, error) {
	var q SearchQuery
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, invalid("invalid search")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Cursor)
	rows, total, err := a.st.ListRemoteMessages(ctx, store.RemoteMessageQuery{Text: q.Text, AccountIDs: q.AccountIDs, FolderIDs: q.FolderIDs, Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := SearchResult{Hits: []SearchHit{}, Histogram: []FacetBucket{}, Facets: emptyFacets(), Total: total, Fallback: true}
	for _, r := range rows {
		var m MessageSummary
		if json.Unmarshal([]byte(r), &m) != nil {
			continue
		}
		hit := SearchHit{ID: fmt.Sprintf("%s:%d", store.ScopeMail, m.ID), Kind: string(store.ScopeMail), Title: m.Subject, Snippet: m.Preview, Highlights: [][2]int{}, TitleHighlights: [][2]int{},
			Date: m.Date, AccountID: m.AccountID, Size: m.Size, HasAttachments: m.HasAttachments, MessageID: m.ID, ConversationID: m.ConversationID, Unread: !slices.Contains(m.Flags, domain.FlagSeen), ItemID: mailItemID(m.ID)}
		if have, err := a.st.HasRemoteCache(ctx, fmt.Sprintf("/api/messages/%d", m.ID)); err == nil && !have {
			hit.HeadersOnly = true // the content is not kept here: header and preview only
		}
		if len(m.FolderIDs) > 0 {
			hit.FolderID = m.FolderIDs[0]
		}
		if len(m.From) > 0 {
			hit.Sender = m.From[0].Name
			if hit.Sender == "" {
				hit.Sender = m.From[0].Email
			}
		}
		out.Hits = append(out.Hits, hit)
	}
	if offset+len(rows) < total {
		out.NextCursor = strconv.Itoa(offset + len(rows))
	}
	return jsonResult(http.StatusOK, out, true), nil
}

// ==== Refreshing the cache =====================================================

// refreshRemoteCacheBG runs refreshRemoteCache once at a time.
func (a *App) refreshRemoteCacheBG(ctx context.Context) {
	if a.cr.dataRunning.Swap(true) {
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	a.cr.mu.Lock()
	a.cr.dataStop = cancel
	a.cr.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.cr.dataRunning.Store(false)
		defer cancel()
		a.refreshRemoteCache(rctx)
	}()
}

// remoteCacheAheadDetails is how many of the newest inbox mails are loaded in full ahead of time.
const remoteCacheAheadDetails = 30

// refreshRemoteCache loads what a person wants to see first when the network
// is gone, also if they did not open it this time: folders, the newest mail,
// calendar, contacts. Stops at the first failure: the loop will try again.
func (a *App) refreshRemoteCache(ctx context.Context) {
	c := a.connCurrent()
	if c == nil || c.Mode != ConnModeThin {
		return
	}
	get := func(pq string) []byte {
		if a.connState() != ConnStateOnline || ctx.Err() != nil {
			return nil
		}
		u, err := url.ParseRequestURI(pq)
		if err != nil {
			return nil
		}
		res, err := a.forwardRead(ctx, c, http.MethodGet, u, http.Header{"Accept": {"application/json"}})
		if err != nil || res.status != http.StatusOK || res.header.Get(headerOffline) != "" {
			return nil
		}
		return res.body
	}
	for _, p := range []string{"/api/identities", "/api/signatures", "/api/persons", "/api/preferences", "/api/tags", "/api/projects", "/api/search-folders",
		"/api/search/profiles", "/api/calendars", "/api/tasks", "/api/addressbooks", "/api/contacts", "/api/contact-groups", "/api/snippets", "/api/focus", "/api/chat-folders", "/api/auth/me"} {
		get(p)
	}
	var accounts []struct {
		ID int64 `json:"id"`
	}
	if b := get("/api/accounts"); b != nil {
		_ = json.Unmarshal(b, &accounts)
	}
	for _, acc := range accounts {
		get(fmt.Sprintf("/api/accounts/%d/folders", acc.ID))
	}
	now := a.now()
	qs := url.Values{"from": {now.AddDate(0, -2, 0).UTC().Format(time.RFC3339)}, "to": {now.AddDate(1, 0, 0).UTC().Format(time.RFC3339)}}
	get("/api/calendar-events?" + qs.Encode())
	if b := get("/api/messages?scope=unified&role=inbox&offset=0&limit=100"); b != nil {
		var p Page[MessageSummary]
		if json.Unmarshal(b, &p) == nil {
			sort.SliceStable(p.Items, func(i, j int) bool { return p.Items[i].Date > p.Items[j].Date })
			for i, m := range p.Items {
				if i >= remoteCacheAheadDetails {
					break
				}
				if a.remoteHeadersOnly(m.Date) {
					continue // older than the day count: header and preview only
				}
				if _, have, err := a.st.GetRemoteCache(ctx, fmt.Sprintf("/api/messages/%d", m.ID), a.now()); err == nil && !have {
					get(fmt.Sprintf("/api/messages/%d", m.ID))
				}
			}
		}
	}
	if err := a.releaseRemoteCacheBodies(ctx); err != nil && ctx.Err() == nil {
		a.log.Warn("app: releasing older mails from the read cache failed", "err", err)
	}
	if a.connState() == ConnStateOnline {
		if err := a.connUpdate(ctx, func(c *serverConnection) { c.LastDataSync = a.now().Unix() }); err != nil {
			a.log.Warn("app: storing the connection state failed", "err", err)
		}
		a.connNotify()
	}
}
