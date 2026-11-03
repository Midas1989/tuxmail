// SPDX-License-Identifier: AGPL-3.0-or-later

package project

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Search folder counts are cached in the database (store.SearchFolderCounts)
// and served for Options.CountTTL. The cache is invalidated
//
//   - for the affected folders when CheckItem records new or lost matches,
//   - for all folders when Watch sees an item event (mail, chat, calendar,
//     contact, file, tag, project link), at most once per second,
//   - when a folder's query or options change (store.UpdateSearchFolder).
//
// Each invalidation publishes searchfolder.changed (op "counts"; FolderID 0
// means all folders) so that the UI re-reads the counts.

func (s *Service) folderChanged(ctx context.Context, f store.SearchFolder, op string) {
	s.publish(ctx, events.Event{Type: events.SearchFolderChanged, Ref: "search_folder:" + idString(f.ID),
		SearchFolder: &events.SearchFolderChange{FolderID: f.ID, Name: f.Name, Query: f.Query, Total: f.CachedTotal,
			Unread: f.CachedUnread, Op: op}})
}

// CreateSearchFolder stores a search folder (searchfolder.changed).
func (s *Service) CreateSearchFolder(ctx context.Context, f store.SearchFolder) (store.SearchFolder, error) {
	id, err := s.st.CreateSearchFolder(ctx, f)
	if err != nil {
		return store.SearchFolder{}, err
	}
	f, err = s.st.GetSearchFolder(ctx, id)
	if err != nil {
		return f, err
	}
	s.folderChanged(ctx, f, events.OpCreated)
	return f, nil
}

// UpdateSearchFolder stores changes of a search folder.
func (s *Service) UpdateSearchFolder(ctx context.Context, f store.SearchFolder) error {
	if err := s.st.UpdateSearchFolder(ctx, f); err != nil {
		return err
	}
	if f2, err := s.st.GetSearchFolder(ctx, f.ID); err == nil {
		s.folderChanged(ctx, f2, events.OpUpdated)
	}
	return nil
}

// DeleteSearchFolder deletes a search folder and its sub folders.
func (s *Service) DeleteSearchFolder(ctx context.Context, id int64) error {
	f, err := s.st.GetSearchFolder(ctx, id)
	if err != nil {
		return err
	}
	if err := s.st.DeleteSearchFolder(ctx, id); err != nil {
		return err
	}
	s.folderChanged(ctx, f, events.OpDeleted)
	return nil
}

// Counts returns the total and unread counts of a folder (cached for
// Options.CountTTL).
func (s *Service) Counts(ctx context.Context, id int64) (store.SearchFolderCount, error) {
	return s.st.SearchFolderCounts(ctx, id, s.opts.CountTTL, s.now())
}

// InvalidateCounts invalidates the cached counts of the given folders (none
// = all) and publishes searchfolder.changed.
func (s *Service) InvalidateCounts(ctx context.Context, ids ...int64) error {
	if err := s.st.InvalidateSearchFolderCounts(ctx, ids...); err != nil {
		return err
	}
	if len(ids) == 0 {
		s.publish(ctx, events.Event{Type: events.SearchFolderChanged, SearchFolder: &events.SearchFolderChange{Op: events.OpCounts}})
	}
	for _, id := range ids {
		if f, err := s.st.GetSearchFolder(ctx, id); err == nil {
			s.folderChanged(ctx, f, events.OpCounts)
		}
	}
	return nil
}

// CheckItem checks an item against all search folders, records the matches
// and publishes searchfolder.matched for every folder the item newly
// matches. It returns the ids of those folders.
func (s *Service) CheckItem(ctx context.Context, it store.Item) ([]int64, error) {
	if _, ok := it.Kind.Scope(); !ok {
		return nil, nil
	}
	folders, err := s.st.ListSearchFolders(ctx, store.SearchFolderFilter{})
	if err != nil {
		return nil, err
	}
	var (
		newly   []int64
		changed []int64
		errs    []error
	)
	now := s.now()
	for _, f := range folders {
		req, err := f.Request()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		req.Now = now
		got, err := s.st.MatchItems(ctx, req, []store.Item{it})
		if err != nil {
			errs = append(errs, fmt.Errorf("search folder %d: %w", f.ID, err))
			continue
		}
		added, gone, err := s.st.UpdateSearchFolderHits(ctx, f.ID, []store.Item{it}, got, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(added)+len(gone) > 0 {
			changed = append(changed, f.ID)
		}
		if len(added) > 0 {
			newly = append(newly, f.ID)
			ev := events.Event{Type: events.SearchFolderMatched, SearchFolder: &events.SearchFolderChange{FolderID: f.ID, Name: f.Name,
				Query: f.Query, Item: itemKey(it)}}
			s.itemEvent(ctx, &ev, it)
			ev.Ref = fmt.Sprintf("search_folder:%d/%s", f.ID, it)
			s.publish(ctx, ev)
		}
	}
	if len(changed) > 0 {
		errs = append(errs, s.InvalidateCounts(ctx, changed...))
	}
	return newly, errors.Join(errs...)
}

// watchTypes are the events after which counts may have changed.
var watchTypes = []events.Type{events.MailReceived, events.MailSent, events.MailMoved, events.MailFlagged, events.ChatReceived,
	events.CalendarInviteAnswer, events.ContactCreated, events.FileCreated, events.TagAdded, events.TagRemoved, events.ProjectItemAdded}

// Watch invalidates search folder counts on item events until ctx ends
// (at most once per second; a pending invalidation is flushed after the
// second has passed). Run it in a goroutine.
func (s *Service) Watch(ctx context.Context) {
	if s.bus == nil {
		return
	}
	sub := s.bus.Subscribe(ctx, events.SubscribeOptions{Name: "project-searchfolders", Types: watchTypes})
	defer sub.Unsubscribe()
	var timer <-chan time.Time
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.C:
			if !ok {
				return
			}
			if !pending {
				pending = true
				timer = time.After(s.untilNextInvalidation())
			}
		case <-timer:
			pending, timer = false, nil
			s.invalidateMu.Lock()
			s.lastInval = s.now()
			s.invalidateMu.Unlock()
			if err := s.InvalidateCounts(ctx); err != nil && ctx.Err() == nil {
				s.opts.Logger.Warn("project: invalidate search folder counts", "err", err)
			}
		}
	}
}

func (s *Service) untilNextInvalidation() time.Duration {
	s.invalidateMu.Lock()
	defer s.invalidateMu.Unlock()
	if d := time.Second - s.now().Sub(s.lastInval); d > 0 {
		return d
	}
	return 0
}
