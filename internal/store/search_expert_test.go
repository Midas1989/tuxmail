// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

func (f *searchFixture) expert(t *testing.T, req SearchRequest) (SearchResult, []string) {
	t.Helper()
	res, err := f.s.SearchAdvanced(context.Background(), req)
	if err != nil {
		t.Fatalf("SearchAdvanced(%q): %v", req.Query, err)
	}
	return res, f.names(t, res.Hits)
}

func TestExpertSearch(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	// Two attachments for m1.
	for _, name := range []string{"rechnung.pdf", "agb.pdf"} {
		_, err := f.s.db.Exec(`INSERT INTO attachments(message_id, filename, mime_type) VALUES (?, ?, 'application/pdf')`, f.m1, name)
		noErr(t, err)
	}
	_, err := f.s.db.Exec(`INSERT INTO attachments(message_id, filename, mime_type) VALUES (?, 'invite.ics', 'text/calendar')`, f.m2)
	noErr(t, err)
	noErr(t, f.s.SetMessageFlags(ctx, f.m3, FlagSeen|FlagFlagged))
	allMails := []string{"m1", "m2", "m3", "m4", "m5"}
	allRechnung := []string{"event", "file", "m1", "m2", "m3", "m4", "m5", "sig1", "task", "wa1"}

	cases := []struct {
		q    string
		want []string
	}{
		{`rechnung`, allRechnung},
		{`Rechnung UND type:mail`, allMails},
		{`rechnung AND NOT subject:spam type:mail`, []string{"m1", "m2", "m3", "m4"}},
		{`type:mail (subject:rechnung OR from:bob)`, []string{"m1", "m2", "m3", "m5"}},
		{`(rechnung OR invoice) -in:trash -in:spam type:mail`, []string{"m1", "m2", "m4"}},
		{`rechnung NOT (from:alice OR subject:alt) type:mail`, []string{"m2", "m4", "m5"}},
		{`NOT urlaub type:mail`, []string{"m1", "m2", "m3", "m5"}},
		{`!urlaub && type:mail && !in:junk`, []string{"m1", "m2", "m3"}},
		{`from:(alice OR bob)`, []string{"m1", "m2"}},
		{`from:*@vendor.example`, []string{"m2"}},
		{`from:ali*`, []string{"m1"}},
		{`from:/^(alice|carol)@/`, []string{"m1", "m3"}},
		{`from:alise~1`, []string{"m1"}},
		{`participant:me@home`, []string{"m4"}},
		{`to:me@work.example subject:alt`, []string{"m3"}},
		{`cc:me@work.example`, nil},
		{`from:mama`, []string{"wa1"}},
		{`"bitte betrag"~3`, []string{"m1"}},
		{`"bitte betrag"~2`, nil},
		{`"bitte überweisen"`, []string{"m1"}},
		{`rechnug~1 type:mail`, allMails},
		{`rechnug~1`, allRechnung},
		{`rechnnug~1`, nil}, // two edits
		{`rechnnug~2 type:mail`, allMails},
		{`rech?ung type:mail`, allMails},
		{`*echnung type:mail`, allMails},
		{`bezahl*`, []string{"task", "wa1"}},
		{`b?zahl*`, []string{"task", "wa1"}},
		{`/rechn[u]ng\s+m/`, []string{"m1"}},
		{`subject:/^urlaub$/`, []string{"m4"}},
		{`filename:rechnung`, []string{"m1"}},
		{`filename:*.pdf`, []string{"m1"}},
		{`attachment:steuernummer`, []string{"m1"}}, // m4's attachment content is not indexed (policy)
		{`body:hotel`, []string{"m4"}},
		{`date:[2026-03-01 TO 2026-03-31]`, []string{"event", "m1", "m5", "task"}},
		{`date:2026-03 type:mail`, []string{"m1", "m5"}},
		{`date:>=2026-05-01`, []string{"m4", "sig1", "wa1"}},
		{`date:<2026-01-01`, []string{"m3"}},
		{`date:{2026-03-01 TO 2026-03-02]`, []string{"m5"}},
		{`size:>1000 type:mail`, allMails},
		{`size:<1KB`, nil},
		{`attachments:>1`, []string{"m1"}},
		{`attachments:1`, []string{"m2", "sig1"}},
		{`is:unread type:mail`, []string{"m1"}},
		{`is:read type:mail`, []string{"m2", "m3", "m4", "m5"}},
		{`is:flagged`, []string{"m3"}},
		{`is:unread type:chat`, []string{"sig1", "wa1"}},
		{`is:signed`, nil},
		{`has:attachment`, []string{"m1", "sig1"}},
		{`exists:attachment`, []string{"m1", "sig1"}},
		{`-has:attachment rechnung type:mail`, []string{"m2", "m3", "m4", "m5"}},
		{`has:calendar`, []string{"m2"}},
		{`exists:body type:mail`, []string{"m1", "m2", "m4"}},
		{`channel:whatsapp OR channel:signal`, []string{"sig1", "wa1"}},
		{`channel:mail rechnung in:inbox`, []string{"m1", "m4"}},
		{`account:work type:task`, []string{"task"}},
		{`account:Wo* type:file`, []string{"file"}},
		{`type:contact *`, []string{"contact"}},
		{`type:(event OR task)`, []string{"event", "task"}},
		{`rechnung^5 OR fotos`, allRechnung},
		{`participant:buchhaltung`, []string{"contact"}},
		{`label:wichtig`, nil},
		{`header.x-mailer:foo`, nil},
		{`(rechnung AND from:alice) OR (urlaub AND is:read)`, []string{"m1", "m4"}},
		{`rechnung AND (from:alice OR channel:signal)`, []string{"m1", "sig1"}},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			res, got := f.expert(t, SearchRequest{Query: c.q, Limit: 100})
			if res.ParseError != nil {
				t.Fatalf("parse error: %v", res.ParseError)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}

	// Warnings for unsupported fields.
	res, _ := f.expert(t, SearchRequest{Query: "category:nosuchcategory OR type:nonsense"})
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	// Invalid syntax falls back to simple mode.
	res, got := f.expert(t, SearchRequest{Query: "rechnung (", Scopes: []SearchScope{ScopeMail}})
	var se *query.SyntaxError
	if !errors.As(res.ParseError, &se) || len(res.Warnings) == 0 || !reflect.DeepEqual(got, allMails) {
		t.Fatalf("fallback: %v %v %v", res.ParseError, res.Warnings, got)
	}
	// Simple mode does not interpret operators.
	if _, got := f.expert(t, SearchRequest{Query: "rechnung OR urlaub", Simple: true}); got != nil {
		t.Fatalf("simple mode: %v", got)
	}
	// Root and Query are combined.
	if _, got := f.expert(t, SearchRequest{Query: "rechnung", Root: &query.Term{Field: "from", Value: "alice"}}); !reflect.DeepEqual(got, []string{"m1"}) {
		t.Fatalf("root+query: %v", got)
	}
}

func TestExpertSearchRelativeDates(t *testing.T) {
	f := newSearchFixture(t)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		berlin = time.UTC
	}
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, berlin) // Wednesday; chats were sent on Monday 2026-06-01
	cases := []struct {
		q    string
		want []string
	}{
		{`date:"last week"`, []string{"sig1", "wa1"}},
		{`date:"letzte Woche"`, []string{"sig1", "wa1"}},
		{`date:"this week"`, nil},
		{`date:>now-10d`, []string{"sig1", "wa1"}},
		{`date:[now-1y TO now-2M]`, []string{"event", "file", "m1", "m2", "m3", "m5", "task"}},
		{`date:"last month" type:mail`, []string{"m4"}},
	}
	for _, c := range cases {
		_, got := f.expert(t, SearchRequest{Query: c.q, Now: now, Location: berlin})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.q, got, c.want)
		}
	}
}

func TestExpertSearchRankingAndHighlight(t *testing.T) {
	f := newSearchFixture(t)
	res, _ := f.expert(t, SearchRequest{Query: "rechnung type:mail"})
	// Default weights: subject matches first.
	top := f.names(t, res.Hits[:3])
	if !reflect.DeepEqual(top, []string{"m1", "m3", "m5"}) {
		t.Fatalf("subject matches should rank first: %v", f.names(t, res.Hits))
	}
	if !strings.Contains(res.Hits[0].TitleHighlight, HighlightStart+"Rechnung"+HighlightEnd) {
		t.Fatalf("title highlight %q", res.Hits[0].TitleHighlight)
	}
	// Body-heavy weights invert that.
	w := SearchWeights{Subject: 0.01, Body: 100, Addresses: 1, Attachments: 1, AttachmentContent: 1}
	res, _ = f.expert(t, SearchRequest{Query: "rechnung type:mail", Weights: &w})
	if top := f.names(t, res.Hits[:2]); !reflect.DeepEqual(top, []string{"m2", "m4"}) {
		t.Fatalf("body weights: %v", f.names(t, res.Hits))
	}
	// Boost.
	res, _ = f.expert(t, SearchRequest{Query: "type:mail (rechnung OR fotos^20)"})
	if res.Hits[0].ID != f.m4 {
		t.Fatalf("boosted term should rank first: %v", f.names(t, res.Hits))
	}
	boosted := res.Hits[0].Score
	res, _ = f.expert(t, SearchRequest{Query: "type:mail (rechnung OR fotos)"})
	for _, h := range res.Hits {
		if h.ID == f.m4 && boosted < 10*h.Score {
			t.Fatalf("boost not applied: %v vs %v", boosted, h.Score)
		}
	}
}

func TestExpertSearchSortingAndCursor(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	order := func(sortBy SortOrder, q string) []string {
		res, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: q, Sort: sortBy})
		noErr(t, err)
		var out []string
		for _, h := range res.Hits {
			out = append(out, f.labels[string(h.Kind)+":"+itoa(int(h.ID))])
		}
		return out
	}
	if got := order(SortDateAsc, "type:mail"); !reflect.DeepEqual(got, []string{"m3", "m1", "m5", "m2", "m4"}) {
		t.Fatalf("date asc: %v", got)
	}
	if got := order(SortDateDesc, "type:mail"); !reflect.DeepEqual(got, []string{"m4", "m2", "m5", "m1", "m3"}) {
		t.Fatalf("date desc: %v", got)
	}
	if got := order("", "type:mail"); !reflect.DeepEqual(got, []string{"m4", "m2", "m5", "m1", "m3"}) {
		t.Fatalf("relevance without text = date desc: %v", got)
	}
	if got := order(SortSender, "type:mail"); !reflect.DeepEqual(got, []string{"m1", "m2", "m3", "m4", "m5"}) {
		t.Fatalf("sender: %v", got)
	}
	noErr(t, f.s.db.QueryRow(`UPDATE messages SET size = 99999 WHERE id = ? RETURNING id`, f.m3).Scan(new(int64)))
	if got := order(SortSizeDesc, "type:mail"); got[0] != "m3" {
		t.Fatalf("size desc: %v", got)
	}
	if got := order(SortSizeAsc, "type:mail"); got[len(got)-1] != "m3" {
		t.Fatalf("size asc: %v", got)
	}
	if _, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "x", Sort: "bogus"}); err == nil {
		t.Fatal("unknown sort must fail")
	}

	// Keyset paging yields the same sequence as one big page, for every order.
	for _, so := range []SortOrder{SortRelevance, SortDateAsc, SortDateDesc, SortSender, SortSizeDesc, SortSizeAsc} {
		for _, q := range []string{"rechnung", "*"} {
			all, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: q, Sort: so, Limit: 100})
			noErr(t, err)
			var paged []SearchHit
			cursor := ""
			for range 20 {
				res, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: q, Sort: so, Limit: 3, Cursor: cursor})
				noErr(t, err)
				paged = append(paged, res.Hits...)
				if res.NextCursor == "" {
					break
				}
				cursor = res.NextCursor
			}
			if !reflect.DeepEqual(all.Hits, paged) || len(paged) < 10 {
				t.Fatalf("%s %q: paging mismatch %d vs %d", so, q, len(all.Hits), len(paged))
			}
		}
	}
	for _, bad := range []string{"!!", "W10=", "WyJ4Il0"} {
		if _, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "rechnung", Cursor: bad}); !errors.Is(err, ErrBadCursor) {
			t.Fatalf("cursor %q: %v", bad, err)
		}
	}
}

func TestExpertSearchFacets(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	res, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "rechnung", Facets: true, Limit: 2})
	noErr(t, err)
	if len(res.Hits) != 2 {
		t.Fatalf("limit applies to hits only: %d", len(res.Hits))
	}
	facets := map[string]map[string]int{}
	for _, fc := range res.Facets {
		facets[fc.Name] = map[string]int{}
		for _, b := range fc.Buckets {
			facets[fc.Name][b.Label] += b.Count
		}
	}
	want := map[string]map[string]int{
		"type":           {"mail": 5, "chat": 2, "event": 1, "task": 1, "file": 1},
		"account":        {"Work": 7, "Privat": 1, "whatsapp": 1, "signal": 1},
		"folder":         {"INBOX": 2, "Rechnungen": 1, "Trash": 1, "Junk": 1},
		"domain":         {"shop.example": 1, "vendor.example": 1, "old.example": 1, "travel.example": 1, "spam.example": 1},
		"month":          {"2025-12": 1, "2026-01": 1, "2026-03": 4, "2026-04": 1, "2026-05": 1, "2026-06": 2},
		"channel":        {"mail": 5, "whatsapp": 1, "signal": 1},
		"has_attachment": {"1": 2, "0": 8},
	}
	if !reflect.DeepEqual(facets, want) {
		t.Fatalf("facets:\n got %v\nwant %v", facets, want)
	}
	// Buckets are sorted by count; months chronologically.
	if res.Facets[0].Name != "type" || res.Facets[0].Buckets[0].Value != "mail" {
		t.Fatalf("facet order: %+v", res.Facets[0])
	}
	// Clicking a bucket narrows the query to exactly that bucket's count.
	for _, fc := range res.Facets {
		for _, b := range fc.Buckets {
			q := query.String(query.AddClause(res.Root, b.Clause))
			r2, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: q, Limit: 100})
			noErr(t, err)
			if r2.ParseError != nil || len(r2.Hits) != b.Count {
				t.Errorf("facet %s=%s: query %q gave %d hits, want %d (%v)", fc.Name, b.Value, q, len(r2.Hits), b.Count, r2.ParseError)
			}
		}
	}
	// Facets of an empty result.
	res, err = f.s.SearchAdvanced(ctx, SearchRequest{Query: "nichtvorhanden", Facets: true})
	noErr(t, err)
	if len(res.Hits) != 0 || len(res.Facets) != 0 {
		t.Fatalf("empty: %+v", res)
	}
}

func TestSearchHistoryAndMaintenance(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	s := f.s
	for _, q := range []string{"a", "b", "c", "a", "d"} {
		noErr(t, s.AddSearchHistory(ctx, q, 3))
	}
	noErr(t, s.AddSearchHistory(ctx, "", 3))
	if h := must[[]string](t)(s.SearchHistory(ctx, 10)); !reflect.DeepEqual(h, []string{"d", "a", "c"}) {
		t.Fatalf("history: %v", h)
	}
	noErr(t, s.AddSearchHistory(ctx, "e", 0)) // settings default
	if h := must[[]string](t)(s.SearchHistory(ctx, 10)); len(h) != 4 || h[0] != "e" {
		t.Fatalf("history: %v", h)
	}
	noErr(t, s.ClearSearchHistory(ctx))
	if h := must[[]string](t)(s.SearchHistory(ctx, 10)); len(h) != 0 {
		t.Fatal("clear")
	}

	st := must[SearchIndexStats](t)(s.SearchIndexStats(ctx))
	if st.Docs[ScopeMail] != 5 || st.Docs[ScopeChat] != 2 || st.IndexBytes == 0 || st.TrigramBytes == 0 || st.ContentBytes == 0 || st.Unindexed != 0 {
		t.Fatalf("stats: %+v", st)
	}
	before := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung"}))
	noErr(t, s.MergeSearchIndex(ctx, 16))
	noErr(t, s.OptimizeSearchIndex(ctx))
	noErr(t, s.RebuildSearchIndex(ctx))
	for _, tbl := range []string{"fts_items", "fts_trigram"} {
		_, err := s.db.Exec(`INSERT INTO ` + tbl + `(` + tbl + `, rank) VALUES ('integrity-check', 1)`)
		noErr(t, err)
	}
	after := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung"}))
	if len(before) != len(after) || len(after) != 10 {
		t.Fatalf("search after maintenance: %d vs %d", len(before), len(after))
	}

	// Profiles with expert queries.
	id := must[int64](t)(s.CreateSearchProfile(ctx, SearchProfile{Name: "Expert", Expert: "rechnung -in:trash",
		Query: SearchQuery{Scopes: []SearchScope{ScopeMail}}}))
	res, err := s.RunSearchProfileAdvanced(ctx, id, SearchRequest{Sort: SortDateAsc, Facets: true})
	noErr(t, err)
	if names := f.names(t, res.Hits); !reflect.DeepEqual(names, []string{"m1", "m2", "m4", "m5"}) || len(res.Facets) == 0 {
		// The query names folders, so the default trash/spam exclusion does not apply.
		t.Fatalf("expert profile: %v", names)
	}
	if _, err := s.CreateSearchProfile(ctx, SearchProfile{Name: "Bad", Expert: "a AND"}); err == nil {
		t.Fatal("invalid expert query must be rejected")
	}
	p := must[SearchProfile](t)(s.GetSearchProfile(ctx, id))
	p.Expert = "(("
	if err := s.UpdateSearchProfile(ctx, p); err == nil {
		t.Fatal("invalid expert query must be rejected on update")
	}

	// Settings: weights, sort, and expert mode switch.
	set := DefaultSearchSettings()
	set.ExpertMode = false
	r := set.ApplyRequest(SearchRequest{Query: "rechnung OR urlaub"})
	if !r.Simple || r.Sort != SortRelevance || r.Weights == nil || r.Limit != 50 || len(r.MailFields) != 4 {
		t.Fatalf("apply request: %+v", r)
	}
	set.AccountIDs, set.Channels = []int64{f.priv}, []string{ChannelMail}
	r = set.ApplyRequest(SearchRequest{Query: "rechnung"})
	if names := f.names(t, must[SearchResult](t)(s.SearchAdvanced(ctx, r)).Hits); !reflect.DeepEqual(names, []string{"m4"}) {
		t.Fatalf("settings accounts/channels: %v", names)
	}
	r = set.ApplyRequest(SearchRequest{Root: &query.Term{Field: "account", Value: "work"}, Query: "rechnung"})
	if names := f.names(t, must[SearchResult](t)(s.SearchAdvanced(ctx, r)).Hits); !reflect.DeepEqual(names, []string{"m1", "m2"}) {
		t.Fatalf("explicit account beats default: %v", names)
	}
}

func TestFuzzyAndWildcardFunctions(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		want bool
	}{
		{"rechnung", "rechnug", 1, true}, {"rechnung", "rechnnug", 1, false}, {"rechnung", "rechnnug", 2, true},
		{"", "ab", 2, true}, {"abc", "", 2, false}, {"kitten", "sitting", 3, true}, {"kitten", "sitting", 2, false},
	}
	for _, c := range cases {
		if got := fuzzyMatch([]rune(c.a), []rune(c.b), c.max); got != c.want {
			t.Errorf("fuzzyMatch(%q, %q, %d) = %v", c.a, c.b, c.max, got)
		}
	}
	if foldText("Überweisung ÄÖÜ é") != "uberweisung aou e" {
		t.Fatalf("fold: %q", foldText("Überweisung ÄÖÜ é"))
	}
	if g := trigrams("Märzen"); !reflect.DeepEqual(g, []string{"mar", "arz", "rze", "zen"}) {
		t.Fatalf("trigrams %v", g)
	}
}
