// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
)

var sortOrders = map[string]store.SortOrder{"relevance": store.SortRelevance, "date": store.SortDateDesc,
	"sender": store.SortSender, "size": store.SortSizeDesc}

func storeQuery(q SearchQuery) (store.SearchQuery, error) {
	sq := store.SearchQuery{AccountIDs: q.AccountIDs, FolderIDs: q.FolderIDs, IncludeSubfolders: q.IncludeSubfolders,
		ExcludeTrashSpam: q.ExcludeTrashSpam}
	for _, s := range q.Scopes {
		sc := store.SearchScope(s)
		switch sc {
		case store.ScopeMail, store.ScopeChat, store.ScopeEvent, store.ScopeTask, store.ScopeContact, store.ScopeFile:
		default:
			return sq, invalid("unknown scope %q", s)
		}
		sq.Scopes = append(sq.Scopes, sc)
	}
	var err error
	if sq.From, err = parseTime(q.From); err != nil {
		return sq, err
	}
	if q.To != "" {
		t, err := parseTime(q.To)
		if err != nil {
			return sq, err
		}
		if len(q.To) == len("2006-01-02") {
			t = t.AddDate(0, 0, 1) // inclusive date -> exclusive bound
		}
		sq.To = t
	}
	return sq, nil
}

// searchRequest builds the store request of a UI query.
func (a *App) searchRequest(q SearchQuery) (store.SearchRequest, error) {
	sq, err := storeQuery(q)
	if err != nil {
		return store.SearchRequest{}, err
	}
	req := sq.Request()
	req.Query, req.Simple = q.Text, q.Mode != "expert"
	req.Sort = sortOrders[q.Sort]
	if req.Sort == "" {
		req.Sort = store.SortRelevance
	}
	req.Cursor = q.Cursor
	req.Limit = q.Limit
	if req.Limit <= 0 || req.Limit > 200 {
		req.Limit = 50
	}
	req.Location, req.Now = a.opts.Location, a.now()
	return req, nil
}

// highlights strips the highlight markers and returns UTF-16 ranges (JS indexes).
func highlights(s string) (string, [][2]int) {
	var b strings.Builder
	ranges := [][2]int{}
	pos, start := 0, -1
	for _, r := range s {
		switch string(r) {
		case store.HighlightStart:
			start = pos
			continue
		case store.HighlightEnd:
			if start >= 0 && pos > start {
				ranges = append(ranges, [2]int{start, pos})
			}
			start = -1
			continue
		}
		b.WriteRune(r)
		pos += len(utf16.Encode([]rune{r}))
	}
	return b.String(), ranges
}

// Search runs a unified search (KONZEPT 8.4).
func (a *App) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	started := time.Now()
	if err := a.ensureIndexed(ctx); err != nil {
		return SearchResult{}, err
	}
	req, err := a.searchRequest(q)
	if err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Hits: []SearchHit{}, Histogram: []FacetBucket{}, Facets: emptyFacets()}
	if strings.TrimSpace(q.Text) != "" {
		_ = a.addHistory(ctx, SearchHistoryEntry{Text: q.Text, Mode: q.Mode})
	}
	if q.Mode == "expert" && strings.TrimSpace(q.Text) != "" {
		if _, perr := query.Parse(q.Text); perr != nil {
			se := &SearchError{Message: perr.Error(), Code: "syntax"}
			var syn *query.SyntaxError
			if errors.As(perr, &syn) {
				se.Message, se.Position = syn.Msg, len(utf16.Encode([]rune(q.Text[:min(syn.Offset, len(q.Text))])))
			}
			out.Error, out.Fallback = se, true
		}
	}
	req.Facets = q.Cursor == ""
	res, err := a.st.SearchAdvanced(ctx, req)
	if err != nil {
		return SearchResult{}, err
	}
	out.NextCursor = res.NextCursor
	mailIDs := []int64{}
	for _, h := range res.Hits {
		if h.Kind == store.ScopeMail {
			mailIDs = append(mailIDs, h.ID)
		}
	}
	msgs, err := a.st.GetMessages(ctx, mailIDs)
	if err != nil {
		return SearchResult{}, err
	}
	byID := map[int64]store.Message{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	folders, err := a.st.FoldersFor(ctx, mailIDs)
	if err != nil {
		return SearchResult{}, err
	}
	for _, h := range res.Hits {
		hit := SearchHit{ID: fmt.Sprintf("%s:%d", h.Kind, h.ID), Kind: string(h.Kind), Date: rfc3339(h.Time), AccountID: h.AccountID,
			Sender: h.Sender, Size: h.Size, HasAttachments: h.HasAttachment, Score: h.Score}
		hit.Snippet, hit.Highlights = highlights(h.Snippet)
		title := h.TitleHighlight
		if title == "" {
			title = h.Title
		}
		hit.Title, hit.TitleHighlights = highlights(title)
		if h.Channel != "" && h.Channel != "mail" {
			hit.Channel = h.Channel
		} else if h.Kind == store.ScopeMail {
			hit.Channel = "email"
		}
		switch h.Kind {
		case store.ScopeMail:
			hit.MessageID, hit.ItemID = h.ID, mailItemID(h.ID)
			if m, ok := byID[h.ID]; ok {
				hit.ConversationID = m.ConversationID
				hit.Unread = m.Flags&store.FlagSeen == 0
			}
			if fs := folders[h.ID]; len(fs) > 0 {
				hit.FolderID = fs[0]
			}
		case store.ScopeChat:
			hit.ItemID = chatItemID(h.ID)
			if cid, err := a.st.ChatOfMessage(ctx, h.ID); err == nil {
				if c, err := a.st.GetChat(ctx, cid); err == nil {
					hit.ConversationID = c.ConversationID
				}
			}
		case store.ScopeContact:
			if c, err := a.st.GetContact(ctx, h.ID); err == nil {
				hit.PersonID = c.PersonID
			}
		}
		out.Hits = append(out.Hits, hit)
	}
	if n, err := a.st.CountSearch(ctx, req); err == nil {
		out.Total = n
	} else {
		out.Total = len(out.Hits)
	}
	for _, f := range res.Facets {
		buckets := make([]FacetBucket, 0, len(f.Buckets))
		for _, b := range f.Buckets {
			fb := FacetBucket{Key: b.Value, Label: b.Label, Count: b.Count}
			if b.Clause != nil {
				fb.Clause = query.String(b.Clause)
			}
			buckets = append(buckets, fb)
		}
		switch f.Name {
		case "type":
			out.Facets.Type = buckets
		case "account":
			out.Facets.Account = buckets
		case "folder":
			out.Facets.Folder = buckets
		case "domain":
			out.Facets.Domain = buckets
		case "channel":
			out.Facets.Channel = buckets
		case "has_attachment":
			out.Facets.Attachment = buckets
		case "month":
			out.Histogram = buckets
		}
	}
	out.Facets.Sender = senderFacet(out.Hits)
	out.TookMs = time.Since(started).Milliseconds()
	return out, nil
}

func emptyFacets() SearchFacets {
	e := func() []FacetBucket { return []FacetBucket{} }
	return SearchFacets{Account: e(), Folder: e(), Sender: e(), Domain: e(), Channel: e(), Type: e(), Attachment: e()}
}

// senderFacet counts the senders of the current page (the store has no
// sender facet over the full result).
func senderFacet(hits []SearchHit) []FacetBucket {
	counts := map[string]int{}
	var order []string
	for _, h := range hits {
		if h.Sender == "" {
			continue
		}
		if counts[h.Sender] == 0 {
			order = append(order, h.Sender)
		}
		counts[h.Sender]++
	}
	out := []FacetBucket{}
	for _, s := range order {
		out = append(out, FacetBucket{Key: s, Label: s, Count: counts[s], Clause: query.String(&query.Term{Field: "from", Value: s})})
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// ==== Profiles and history =====================================================

func profileDTO(p store.SearchProfile) SearchProfile {
	q := SearchQuery{Text: p.Query.Text, Mode: "simple", Scopes: []string{}, AccountIDs: nonNilIDs(p.Query.AccountIDs),
		FolderIDs: nonNilIDs(p.Query.FolderIDs), IncludeSubfolders: p.Query.IncludeSubfolders, ExcludeTrashSpam: p.Query.ExcludeTrashSpam,
		Location: "local", Sort: "relevance"}
	if p.Expert != "" {
		q.Text, q.Mode = p.Expert, "expert"
	}
	for _, s := range p.Query.Scopes {
		q.Scopes = append(q.Scopes, string(s))
	}
	if !p.Query.From.IsZero() {
		q.From = p.Query.From.Format("2006-01-02")
	}
	if !p.Query.To.IsZero() {
		q.To = p.Query.To.AddDate(0, 0, -1).Format("2006-01-02")
	}
	return SearchProfile{ID: strconv.FormatInt(p.ID, 10), Name: p.Name, Query: q, Pinned: p.Pinned}
}

// ListSearchProfiles returns the saved searches.
func (a *App) ListSearchProfiles(ctx context.Context) ([]SearchProfile, error) {
	ps, err := a.st.ListSearchProfiles(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []SearchProfile{}
	for _, p := range ps {
		out = append(out, profileDTO(p))
	}
	return out, nil
}

// SaveSearchProfile creates (empty/"new" id) or updates a saved search.
func (a *App) SaveSearchProfile(ctx context.Context, in SearchProfile) (SearchProfile, error) {
	if strings.TrimSpace(in.Name) == "" {
		return SearchProfile{}, invalid("a search profile needs a name")
	}
	sq, err := storeQuery(in.Query)
	if err != nil {
		return SearchProfile{}, err
	}
	p := store.SearchProfile{Name: in.Name, Query: sq, Pinned: in.Pinned}
	if in.Query.Mode == "expert" {
		p.Expert = in.Query.Text
	} else {
		p.Query.Text = in.Query.Text
	}
	if in.ID == "" || in.ID == "new" {
		id, err := a.st.CreateSearchProfile(ctx, p)
		if err != nil {
			return SearchProfile{}, AsConflict(err)
		}
		p.ID = id
	} else {
		id, err := strconv.ParseInt(in.ID, 10, 64)
		if err != nil {
			return SearchProfile{}, invalid("invalid profile id")
		}
		p.ID = id
		if err := a.st.UpdateSearchProfile(ctx, p); err != nil {
			return SearchProfile{}, AsConflict(err)
		}
	}
	got, err := a.st.GetSearchProfile(ctx, p.ID)
	if err != nil {
		return SearchProfile{}, err
	}
	return profileDTO(got), nil
}

// AsConflict maps unique-constraint violations to a conflict error.
func AsConflict(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return &Error{Code: CodeConflict, Status: 409, Message: "an entry with this name already exists", Err: err}
	}
	return err
}

// DeleteSearchProfile deletes a saved search.
func (a *App) DeleteSearchProfile(ctx context.Context, id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errNotFound
	}
	return a.st.DeleteSearchProfile(ctx, n)
}

const (
	keySearchHistory = "search.history"
	maxHistory       = 50
)

func (a *App) addHistory(ctx context.Context, e SearchHistoryEntry) error {
	var list []SearchHistoryEntry
	if _, err := a.st.GetAppSetting(ctx, keySearchHistory, &list); err != nil {
		return err
	}
	e.At = rfc3339(a.now())
	if e.Mode != "expert" {
		e.Mode = "simple"
	}
	out := []SearchHistoryEntry{e}
	for _, x := range list {
		if x.Text != e.Text || x.Mode != e.Mode {
			out = append(out, x)
		}
	}
	if len(out) > maxHistory {
		out = out[:maxHistory]
	}
	return a.st.SetAppSetting(ctx, keySearchHistory, out)
}

// GetSearchHistory returns the recent searches, newest first.
func (a *App) GetSearchHistory(ctx context.Context) ([]SearchHistoryEntry, error) {
	list := []SearchHistoryEntry{}
	_, err := a.st.GetAppSetting(ctx, keySearchHistory, &list)
	if list == nil {
		list = []SearchHistoryEntry{}
	}
	return list, err
}

// ClearSearchHistory deletes the search history.
func (a *App) ClearSearchHistory(ctx context.Context) error {
	return a.st.DeleteAppSetting(ctx, keySearchHistory)
}
