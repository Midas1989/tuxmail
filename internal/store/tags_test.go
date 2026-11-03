// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"testing"
)

func upsertMsg(t *testing.T, s *Store, acc int64, mid, fp string) int64 {
	t.Helper()
	id, _, err := s.UpsertMessage(context.Background(), testMessage(acc, mid, fp))
	noErr(t, err)
	return id
}

func tagPaths(t *testing.T, s *Store) []string {
	t.Helper()
	tags := must[[]Tag](t)(s.ListTags(context.Background()))
	var out []string
	for _, tg := range tags {
		out = append(out, tg.Path)
	}
	return out
}

func pendingOps(t *testing.T, s *Store) []string {
	t.Helper()
	ops := must[[]TagSyncOp](t)(s.PendingTagSync(context.Background(), 0, 0))
	var out []string
	for _, o := range ops {
		out = append(out, o.Op+" "+o.TagPath)
	}
	sort.Strings(out)
	return out
}

func TestTagTreeAndAssignments(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	m1 := upsertMsg(t, s, acc, "m1@x", "fp1")
	m2 := upsertMsg(t, s, acc, "m2@x", "fp2")
	p := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Anna"}))

	kundeA, created, err := s.EnsureTag(ctx, " Projekt / Kunde-A ")
	noErr(t, err)
	if !created || kundeA.Path != "Projekt/Kunde-A" || kundeA.Name != "Kunde-A" || kundeA.ParentID == 0 {
		t.Fatalf("EnsureTag: %+v created=%v", kundeA, created)
	}
	again, created, err := s.EnsureTag(ctx, "projekt/kunde-a")
	noErr(t, err)
	if created || again.ID != kundeA.ID {
		t.Fatalf("EnsureTag is not idempotent/case-insensitive: %+v %v", again, created)
	}
	if got := tagPaths(t, s); !reflect.DeepEqual(got, []string{"Projekt", "Projekt/Kunde-A"}) {
		t.Fatalf("tags: %v", got)
	}

	// Bulk tag; the second call adds nothing.
	added := must[[]Item](t)(s.TagItems(ctx, kundeA.ID, TagSourceUser, Item{ItemMessage, m1}, Item{ItemMessage, m2}, Item{ItemPerson, p}))
	if len(added) != 3 {
		t.Fatalf("added %v", added)
	}
	if again := must[[]Item](t)(s.TagItems(ctx, kundeA.ID, TagSourceUser, Item{ItemMessage, m1})); len(again) != 0 {
		t.Fatalf("re-tag added %v", again)
	}
	if _, err := s.TagItems(ctx, kundeA.ID, TagSourceUser, Item{ItemMessage, 9999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tagging a missing item: %v", err)
	}
	if got := pendingOps(t, s); !reflect.DeepEqual(got, []string{"add Projekt/Kunde-A", "add Projekt/Kunde-A"}) {
		t.Fatalf("pending after tag: %v", got)
	}
	// Untag before push cancels the pending add.
	removed := must[[]Item](t)(s.UntagItems(ctx, kundeA.ID, Item{ItemMessage, m2}))
	if len(removed) != 1 {
		t.Fatalf("removed %v", removed)
	}
	if got := pendingOps(t, s); !reflect.DeepEqual(got, []string{"add Projekt/Kunde-A"}) {
		t.Fatalf("pending after untag: %v", got)
	}
	ops := must[[]TagSyncOp](t)(s.PendingTagSync(ctx, acc, 10))
	if len(ops) != 1 || ops[0].AccountID != acc || ops[0].Item != (Item{ItemMessage, m1}) {
		t.Fatalf("pending for account: %+v", ops)
	}
	noErr(t, s.AckTagSync(ctx, ops...))
	if got := pendingOps(t, s); len(got) != 0 {
		t.Fatalf("pending after ack: %v", got)
	}

	// Rename the parent: paths follow, mails get remove+add.
	proj := must[Tag](t)(s.TagByPath(ctx, "projekt"))
	renamed := must[Tag](t)(s.RenameTag(ctx, proj.ID, "Projekte"))
	if renamed.Path != "Projekte" {
		t.Fatalf("renamed: %+v", renamed)
	}
	if got := tagPaths(t, s); !reflect.DeepEqual(got, []string{"Projekte", "Projekte/Kunde-A"}) {
		t.Fatalf("tags after rename: %v", got)
	}
	if got := pendingOps(t, s); !reflect.DeepEqual(got, []string{"add Projekte/Kunde-A", "remove Projekt/Kunde-A"}) {
		t.Fatalf("pending after rename: %v", got)
	}
	noErr(t, s.AckTagSync(ctx, must[[]TagSyncOp](t)(s.PendingTagSync(ctx, 0, 0))...))
	other := must[Tag](t)(func() (Tag, error) { tg, _, err := s.EnsureTag(ctx, "Kunde-A"); return tg, err }())
	if _, err := s.MoveTag(ctx, other.ID, renamed.ID); !errors.Is(err, ErrTagExists) {
		t.Fatalf("move onto existing path: %v", err)
	}
	if _, err := s.MoveTag(ctx, renamed.ID, kundeA.ID); err == nil {
		t.Fatal("moving a tag below its child must fail")
	}

	// Merge "Kunde-A" (top level, tagged m2) into "Projekte/Kunde-A".
	_ = must[[]Item](t)(s.TagItems(ctx, other.ID, TagSourceLocal, Item{ItemMessage, m2}, Item{ItemMessage, m1}))
	sub, _, err := s.EnsureTag(ctx, "Kunde-A/Notizen")
	noErr(t, err)
	n := must[int](t)(s.MergeTags(ctx, kundeA.ID, other.ID))
	if n != 2 {
		t.Fatalf("merge changed %d items", n)
	}
	if got := tagPaths(t, s); !reflect.DeepEqual(got, []string{"Projekte", "Projekte/Kunde-A", "Projekte/Kunde-A/Notizen"}) {
		t.Fatalf("tags after merge: %v", got)
	}
	if moved := must[Tag](t)(s.GetTag(ctx, sub.ID)); moved.Path != "Projekte/Kunde-A/Notizen" {
		t.Fatalf("child after merge: %+v", moved)
	}
	paths := must[[]string](t)(s.TagPaths(ctx, Item{ItemMessage, m2}))
	if !reflect.DeepEqual(paths, []string{"Projekte/Kunde-A"}) {
		t.Fatalf("m2 tags after merge: %v", paths)
	}

	// Counts include descendants in Total.
	_ = must[[]Item](t)(s.TagItems(ctx, sub.ID, TagSourceLocal, Item{ItemMessage, m1}))
	counts := must[[]TagCount](t)(s.TagCounts(ctx))
	byPath := map[string]TagCount{}
	for _, c := range counts {
		byPath[c.Tag.Path] = c
	}
	if c := byPath["Projekte/Kunde-A"]; c.Direct != 3 || c.Total != 3 || c.ByKind[ItemMessage] != 2 || c.ByKind[ItemPerson] != 1 || c.Unread != 2 {
		t.Fatalf("counts Kunde-A: %+v", c)
	}
	if c := byPath["Projekte"]; c.Direct != 0 || c.Total != 3 {
		t.Fatalf("counts Projekte: %+v", c)
	}
	list := must[[]ItemTag](t)(s.ListTaggedItems(ctx, renamed.ID, TaggedItemsOptions{IncludeDescendants: true, Kinds: []ItemKind{ItemMessage}}))
	if len(list) != 3 { // m1 twice (two tags), m2 once
		t.Fatalf("tagged items: %+v", list)
	}
	byItem := must[map[int64][]Tag](t)(s.TagsForItems(ctx, ItemMessage, []int64{m1, m2}))
	if len(byItem[m1]) != 2 || len(byItem[m2]) != 1 {
		t.Fatalf("TagsForItems: %+v", byItem)
	}

	// Deleting a message removes its assignments (trigger).
	noErr(t, s.DeleteMessage(ctx, m2))
	if got := must[[]ItemTag](t)(s.ItemTags(ctx, Item{ItemMessage, m2})); len(got) != 0 {
		t.Fatalf("tags of deleted message: %v", got)
	}
	// Deleting a tag deletes the subtree and queues removals.
	noErr(t, s.AckTagSync(ctx, must[[]TagSyncOp](t)(s.PendingTagSync(ctx, 0, 0))...))
	if n := must[int](t)(s.DeleteTag(ctx, renamed.ID)); n != 3 {
		t.Fatalf("deleted %d assignments", n)
	}
	if got := tagPaths(t, s); len(got) != 0 {
		t.Fatalf("tags after delete: %v", got)
	}
	if got := pendingOps(t, s); !reflect.DeepEqual(got, []string{"remove Projekte/Kunde-A", "remove Projekte/Kunde-A/Notizen"}) {
		t.Fatalf("pending after delete: %v", got)
	}
}

func TestSetProviderTags(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	m := Item{ItemMessage, upsertMsg(t, s, acc, "m@x", "fp")}

	added, removed, err := s.SetProviderTags(ctx, m, TagSourceIMAPKeyword, []string{"Work", "Projekt/A"})
	noErr(t, err)
	if len(added) != 2 || len(removed) != 0 {
		t.Fatalf("first sync: +%v -%v", added, removed)
	}
	if got := pendingOps(t, s); len(got) != 0 {
		t.Fatalf("provider tags must not be queued: %v", got)
	}
	// A local tag survives provider syncs; a local removal is not undone
	// before it was pushed.
	local, _, _ := s.EnsureTag(ctx, "Lokal")
	_ = must[[]Item](t)(s.TagItems(ctx, local.ID, TagSourceUser, m))
	work := must[Tag](t)(s.TagByPath(ctx, "work"))
	_ = must[[]Item](t)(s.UntagItems(ctx, work.ID, m))
	added, removed, err = s.SetProviderTags(ctx, m, TagSourceIMAPKeyword, []string{"Work"})
	noErr(t, err)
	if len(added) != 0 || len(removed) != 1 || removed[0].Path != "Projekt/A" {
		t.Fatalf("second sync: +%v -%v", added, removed)
	}
	paths := must[[]string](t)(s.TagPaths(ctx, m))
	if !reflect.DeepEqual(paths, []string{"Lokal"}) {
		t.Fatalf("tags: %v", paths)
	}
}

func TestTagSearch(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	s := f.s
	tag := func(path string, items ...Item) {
		tg, _, err := s.EnsureTag(ctx, path)
		noErr(t, err)
		_ = must[[]Item](t)(s.TagItems(ctx, tg.ID, TagSourceUser, items...))
	}
	tag("Projekt/Kunde-A", Item{ItemMessage, f.m1}, Item{ItemChatMessage, f.cm1})
	tag("Projekt/Kunde-B", Item{ItemMessage, f.m2})
	tag("Privat", Item{ItemMessage, f.m4})
	// m3 is tagged through its conversation.
	conv := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: "email_thread", Title: "c"}))
	noErr(t, s.AssignConversation(ctx, f.m3, conv))
	tag("Archiv/2025", Item{ItemConversation, conv})

	cases := []struct {
		q    string
		want []string
	}{
		{`tag:Projekt`, []string{"m1", "m2", "wa1"}},
		{`label:"Projekt/Kunde-A"`, []string{"m1", "wa1"}},
		{`tag:kunde-a`, []string{"m1", "wa1"}},
		{`tag:Kunde`, nil}, // whole levels only
		{`tag:Projekt/Kunde-*`, []string{"m1", "m2", "wa1"}},
		{`tag:/kunde-[ab]$/ type:mail`, []string{"m1", "m2"}},
		{`tag:Archiv`, []string{"m3"}},
		{`tag:* type:mail`, []string{"m1", "m2", "m3", "m4"}},
		{`rechnung NOT tag:Projekt type:mail`, []string{"m3", "m4", "m5"}},
		{`tag:Projekt OR tag:Privat`, []string{"m1", "m2", "m4", "wa1"}},
	}
	for _, c := range cases {
		_, got := f.expert(t, SearchRequest{Query: c.q, Sort: SortDateAsc})
		sort.Strings(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.q, got, c.want)
		}
	}
	res, _ := f.expert(t, SearchRequest{Query: `type:mail OR type:chat`, Facets: true})
	var facet *Facet
	for i := range res.Facets {
		if res.Facets[i].Name == "tag" {
			facet = &res.Facets[i]
		}
	}
	if facet == nil {
		t.Fatalf("no tag facet in %+v", res.Facets)
	}
	counts := map[string]int{}
	for _, b := range facet.Buckets {
		counts[b.Label] = b.Count
	}
	if counts["Projekt/Kunde-A"] != 2 || counts["Projekt/Kunde-B"] != 1 || counts["Privat"] != 1 {
		t.Fatalf("tag facet: %+v", facet.Buckets)
	}
	for _, b := range facet.Buckets {
		if b.Label == "Projekt/Kunde-A" {
			_, got := f.expert(t, SearchRequest{Root: b.Clause})
			sort.Strings(got)
			if !slices.Equal(got, []string{"m1", "wa1"}) {
				t.Fatalf("facet clause: %v", got)
			}
		}
	}
}
