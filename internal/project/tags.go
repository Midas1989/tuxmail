// SPDX-License-Identifier: AGPL-3.0-or-later

package project

import (
	"context"
	"errors"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

func (s *Service) tagChanged(ctx context.Context, t store.Tag, op, oldPath string, items int) {
	s.publish(ctx, events.Event{Type: events.TagChanged, Tag: &events.TagChange{TagID: t.ID, Name: t.Name, Path: t.Path,
		OldPath: oldPath, Items: items, Op: op}})
}

func (s *Service) tagItemEvent(ctx context.Context, typ events.Type, t store.Tag, source store.TagSource, it store.Item) {
	ev := events.Event{Type: typ, Tag: &events.TagChange{TagID: t.ID, Name: t.Name, Path: t.Path, Source: string(source),
		Item: itemKey(it)}}
	s.itemEvent(ctx, &ev, it)
	s.publish(ctx, ev)
}

// Tag adds the tag with the given path (created if missing) to the items
// and returns the items that did not have it. It publishes tag.added for
// each of them and tag.changed.
func (s *Service) Tag(ctx context.Context, path string, source store.TagSource, items ...store.Item) ([]store.Item, error) {
	t, created, err := s.st.EnsureTag(ctx, path)
	if err != nil {
		return nil, err
	}
	if created {
		s.tagChanged(ctx, t, events.OpCreated, "", 0)
	}
	added, err := s.st.TagItems(ctx, t.ID, source, items...)
	if err != nil {
		return nil, err
	}
	for _, it := range added {
		s.tagItemEvent(ctx, events.TagAdded, t, source, it)
	}
	if len(added) > 0 {
		s.tagChanged(ctx, t, events.OpTagged, "", len(added))
	}
	return added, nil
}

// Untag removes the tag from the items (tag.removed, tag.changed). An
// unknown tag removes nothing.
func (s *Service) Untag(ctx context.Context, path string, items ...store.Item) ([]store.Item, error) {
	t, err := s.st.TagByPath(ctx, path)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	removed, err := s.st.UntagItems(ctx, t.ID, items...)
	if err != nil {
		return nil, err
	}
	for _, it := range removed {
		s.tagItemEvent(ctx, events.TagRemoved, t, store.TagSourceLocal, it)
	}
	if len(removed) > 0 {
		s.tagChanged(ctx, t, events.OpUntagged, "", len(removed))
	}
	return removed, nil
}

// SetProviderTags stores the tags a mail server reports for an item (see
// store.SetProviderTags) and publishes tag.added / tag.removed.
func (s *Service) SetProviderTags(ctx context.Context, it store.Item, source store.TagSource, paths []string) error {
	added, removed, err := s.st.SetProviderTags(ctx, it, source, paths)
	if err != nil {
		return err
	}
	for _, t := range added {
		s.tagItemEvent(ctx, events.TagAdded, t, source, it)
		s.tagChanged(ctx, t, events.OpTagged, "", 1)
	}
	for _, t := range removed {
		s.tagItemEvent(ctx, events.TagRemoved, t, source, it)
		s.tagChanged(ctx, t, events.OpUntagged, "", 1)
	}
	return nil
}

// UpdateTag stores color, icon and sort order (tag.changed).
func (s *Service) UpdateTag(ctx context.Context, t store.Tag) error {
	if err := s.st.UpdateTag(ctx, t); err != nil {
		return err
	}
	if t2, err := s.st.GetTag(ctx, t.ID); err == nil {
		s.tagChanged(ctx, t2, events.OpUpdated, "", 0)
	}
	return nil
}

// RenameTag renames a tag (tag.changed with the old path).
func (s *Service) RenameTag(ctx context.Context, id int64, name string) (store.Tag, error) {
	old, err := s.st.GetTag(ctx, id)
	if err != nil {
		return store.Tag{}, err
	}
	t, err := s.st.RenameTag(ctx, id, name)
	if err != nil {
		return store.Tag{}, err
	}
	s.tagChanged(ctx, t, events.OpRenamed, old.Path, 0)
	return t, nil
}

// MoveTag moves a tag below another one (0 = top level).
func (s *Service) MoveTag(ctx context.Context, id, parentID int64) (store.Tag, error) {
	old, err := s.st.GetTag(ctx, id)
	if err != nil {
		return store.Tag{}, err
	}
	t, err := s.st.MoveTag(ctx, id, parentID)
	if err != nil {
		return store.Tag{}, err
	}
	s.tagChanged(ctx, t, events.OpRenamed, old.Path, 0)
	return t, nil
}

// MergeTags merges tags into one (tag.changed per merged tag).
func (s *Service) MergeTags(ctx context.Context, into int64, from ...int64) (int, error) {
	var olds []store.Tag
	for _, f := range from {
		if t, err := s.st.GetTag(ctx, f); err == nil {
			olds = append(olds, t)
		}
	}
	n, err := s.st.MergeTags(ctx, into, from...)
	if err != nil {
		return 0, err
	}
	dst, err := s.st.GetTag(ctx, into)
	if err != nil {
		return n, err
	}
	for _, o := range olds {
		s.tagChanged(ctx, dst, events.OpMerged, o.Path, n)
	}
	return n, nil
}

// DeleteTag deletes a tag and its descendants (tag.changed).
func (s *Service) DeleteTag(ctx context.Context, id int64) (int, error) {
	t, err := s.st.GetTag(ctx, id)
	if err != nil {
		return 0, err
	}
	n, err := s.st.DeleteTag(ctx, id)
	if err != nil {
		return 0, err
	}
	s.tagChanged(ctx, t, events.OpDeleted, "", n)
	return n, nil
}

// TaggedSummaries lists the items of a tag with their summaries.
func (s *Service) TaggedSummaries(ctx context.Context, tagID int64, o store.TaggedItemsOptions) ([]store.ItemSummary, error) {
	its, err := s.st.ListTaggedItems(ctx, tagID, o)
	if err != nil {
		return nil, err
	}
	seen := map[store.Item]bool{}
	var out []store.ItemSummary
	for _, it := range its {
		if seen[it.Item] {
			continue
		}
		seen[it.Item] = true
		sum, err := s.st.ItemSummary(ctx, it.Item)
		if err != nil {
			continue // deleted meanwhile
		}
		out = append(out, sum)
	}
	return out, nil
}
