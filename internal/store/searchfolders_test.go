// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

func TestSearchFolders(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	s := f.s
	if _, err := s.CreateSearchFolder(ctx, SearchFolder{Name: "Bad", Query: "from:("}); err == nil {
		t.Fatal("invalid query accepted")
	}
	if _, err := s.CreateSearchFolder(ctx, SearchFolder{Name: "", Query: "x"}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := s.CreateSearchFolder(ctx, SearchFolder{Name: "Empty", Query: ""}); err == nil {
		t.Fatal("empty query without scopes accepted")
	}
	pid := must[int64](t)(s.CreateProject(ctx, Project{Name: "P"}))
	rech := must[int64](t)(s.CreateSearchFolder(ctx, SearchFolder{Name: "Rechnungen", Icon: "receipt", Query: "rechnung",
		Options: SearchFolderOptions{Scopes: []SearchScope{ScopeMail}}}))
	work := must[int64](t)(s.CreateSearchFolder(ctx, SearchFolder{Name: "Work Rechnungen", Query: "rechnung", AccountID: f.work,
		ParentID: rech, Options: SearchFolderOptions{Scopes: []SearchScope{ScopeMail}, IncludeTrashSpam: true}}))
	chats := must[int64](t)(s.CreateSearchFolder(ctx, SearchFolder{Name: "Chats", Query: "", ChatFolder: true, ProjectID: pid,
		Options: SearchFolderOptions{Scopes: []SearchScope{ScopeChat}}}))

	if l := must[[]SearchFolder](t)(s.ListSearchFolders(ctx, SearchFolderFilter{ChatFolders: true})); len(l) != 1 || l[0].ID != chats {
		t.Fatalf("chat folders: %+v", l)
	}
	if l := must[[]SearchFolder](t)(s.ListSearchFolders(ctx, SearchFolderFilter{AccountID: f.work})); len(l) != 1 || l[0].ID != work {
		t.Fatalf("account folders: %+v", l)
	}
	if l := must[[]SearchFolder](t)(s.ListSearchFolders(ctx, SearchFolderFilter{GlobalOnly: true})); len(l) != 1 || l[0].ID != rech {
		t.Fatalf("global folders: %+v", l)
	}
	if got := must[SearchFolder](t)(s.SearchFolderByName(ctx, "rechnungen")); got.ID != rech || got.Icon != "receipt" {
		t.Fatalf("by name: %+v", got)
	}

	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	counts := []struct {
		id            int64
		total, unread int
	}{
		{rech, 3, 1},  // m1 (unread), m2, m4 — trash and spam excluded
		{work, 4, 1},  // work account incl. trash/spam: m1, m2, m3, m5
		{chats, 2, 2}, // all chat messages (unread: never read)
	}
	for _, c := range counts {
		got := must[SearchFolderCount](t)(s.SearchFolderCounts(ctx, c.id, time.Minute, now))
		if got.Total != c.total || got.Unread != c.unread || got.Cached {
			t.Errorf("folder %d: %+v, want %d/%d", c.id, got, c.total, c.unread)
		}
	}
	// Cached within the TTL even if items change; recomputed after invalidation.
	noErr(t, s.SetMessageFlags(ctx, f.m1, FlagSeen))
	if got := must[SearchFolderCount](t)(s.SearchFolderCounts(ctx, rech, time.Minute, now.Add(30*time.Second))); !got.Cached || got.Unread != 1 {
		t.Fatalf("cached counts: %+v", got)
	}
	if got := must[SearchFolderCount](t)(s.SearchFolderCounts(ctx, rech, time.Minute, now.Add(2*time.Minute))); got.Cached || got.Unread != 0 {
		t.Fatalf("expired counts: %+v", got)
	}
	noErr(t, s.SetMessageFlags(ctx, f.m1, 0))
	noErr(t, s.InvalidateSearchFolderCounts(ctx, rech))
	if got := must[SearchFolderCount](t)(s.SearchFolderCounts(ctx, rech, time.Hour, now.Add(3*time.Minute))); got.Cached || got.Unread != 1 {
		t.Fatalf("invalidated counts: %+v", got)
	}

	res := must[SearchResult](t)(s.RunSearchFolder(ctx, work, SearchRequest{Sort: SortDateAsc}))
	if got := f.names(t, res.Hits); !slices.Equal(got, []string{"m1", "m2", "m3", "m5"}) {
		t.Fatalf("run folder: %v", got)
	}

	// Hits: newly matching items.
	checked := []Item{{ItemMessage, f.m1}, {ItemMessage, f.m3}, {ItemMessage, f.m4}}
	sfRec := must[SearchFolder](t)(s.GetSearchFolder(ctx, rech))
	req := must[SearchRequest](t)(sfRec.Request())
	matched := must[[]Item](t)(s.MatchItems(ctx, req, checked))
	if !slices.Equal(matched, []Item{{ItemMessage, f.m1}, {ItemMessage, f.m4}}) {
		t.Fatalf("matched %v", matched)
	}
	newly, gone, err := s.UpdateSearchFolderHits(ctx, rech, checked, matched, now)
	noErr(t, err)
	if len(newly) != 2 || len(gone) != 0 {
		t.Fatalf("first hits: +%v -%v", newly, gone)
	}
	newly, gone, err = s.UpdateSearchFolderHits(ctx, rech, checked, matched[:1], now)
	noErr(t, err)
	if len(newly) != 0 || len(gone) != 1 || gone[0].ID != f.m4 {
		t.Fatalf("second hits: +%v -%v", newly, gone)
	}

	// Update: changing the query invalidates; cycles are rejected.
	sfRec.Query = "invoice"
	noErr(t, s.UpdateSearchFolder(ctx, sfRec))
	if got := must[SearchFolder](t)(s.GetSearchFolder(ctx, rech)); !got.CachedAt.IsZero() {
		t.Fatal("query change must invalidate counts")
	}
	sfRec.ParentID = work
	if err := s.UpdateSearchFolder(ctx, sfRec); err == nil {
		t.Fatal("cycle accepted")
	}
	noErr(t, s.DeleteSearchFolder(ctx, rech))
	if l := must[[]SearchFolder](t)(s.ListSearchFolders(ctx, SearchFolderFilter{})); len(l) != 1 || l[0].ID != chats {
		t.Fatalf("after delete (sub folder must go too): %+v", l)
	}
}

func TestMatchItemsAndCount(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	s := f.s
	all := []Item{{ItemMessage, f.m1}, {ItemMessage, f.m2}, {ItemMessage, f.m3}, {ItemMessage, f.m4}, {ItemMessage, f.m5},
		{ItemChatMessage, f.cm1}, {ItemConversation, 1}}
	cases := []struct {
		q     string
		want  []string
		count int // mail and chat items matching overall (also sig1)
	}{
		{`rechnung`, []string{"m1", "m2", "m3", "m4", "m5", "wa1"}, 7},
		{`from:alice OR from:mama`, []string{"m1", "wa1"}, 2},
		{`is:unread type:mail`, []string{"m1"}, 1},
		{`in:trash`, []string{"m3"}, 1},
		{`date:>=2026-04-01`, []string{"m2", "m4", "wa1"}, 4},
		{`rechn~1`, nil, 0},
		{`*`, []string{"m1", "m2", "m3", "m4", "m5", "wa1"}, 7},
		// Regression: NOT on a text term must not drop items of other kinds.
		{`NOT invoice`, []string{"m1", "m3", "m4", "m5", "wa1"}, 6},
	}
	for _, c := range cases {
		root, err := query.Parse(c.q)
		noErr(t, err)
		got := must[[]Item](t)(s.MatchItems(ctx, SearchRequest{Root: root}, all))
		var names []string
		for _, it := range got {
			sc, _ := it.Kind.Scope()
			names = append(names, f.labels[string(sc)+":"+itoa(int(it.ID))])
		}
		sort.Strings(names)
		if !slices.Equal(names, c.want) {
			t.Errorf("MatchItems(%s) = %v, want %v", c.q, names, c.want)
		}
		n := must[int](t)(s.CountSearch(ctx, SearchRequest{Root: root, Scopes: []SearchScope{ScopeMail, ScopeChat}}))
		if n != c.count {
			t.Errorf("CountSearch(%s) = %d, want %d", c.q, n, c.count)
		}
	}
	if _, err := s.CountSearch(ctx, SearchRequest{Query: "from:("}); err == nil {
		t.Fatal("CountSearch must report syntax errors")
	}
	ok := must[bool](t)(s.MatchQuery(ctx, &query.Term{Field: "from", Value: "bob"}, Item{ItemMessage, f.m2}))
	if !ok {
		t.Fatal("MatchQuery")
	}
}
