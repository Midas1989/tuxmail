// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"testing"
)

// NH-07: bulk operations refuse more than maxBulkIDs ids with a 413.
func TestBulkOperationsAreCapped(t *testing.T) {
	a := newIsolationApp(t, Options{})
	ctx := context.Background()
	ids := make([]int64, maxBulkIDs+1)
	refs := make([]ItemRef, maxBulkIDs+1)
	for i := range ids {
		ids[i] = int64(i + 1)
		refs[i] = ItemRef{Kind: "message", ID: int64(i + 1)}
	}
	cases := map[string]func(ids []int64, refs []ItemRef) error{
		"Move":     func(ids []int64, _ []ItemRef) error { return a.Move(ctx, ids, 1) },
		"SetFlags": func(ids []int64, _ []ItemRef) error { return a.SetFlags(ctx, ids, []Flag{"\\Seen"}, nil) },
		"MarkSpam": func(ids []int64, _ []ItemRef) error { return a.MarkSpam(ctx, MarkSpamInput{IDs: ids, Spam: true}) },
		"MessageCategories": func(ids []int64, _ []ItemRef) error {
			_, err := a.MessageCategories(ctx, ids)
			return err
		},
		"MergeTags":         func(ids []int64, _ []ItemRef) error { return a.MergeTags(ctx, ids, 99999) },
		"SetItemTags":       func(_ []int64, refs []ItemRef) error { return a.SetItemTags(ctx, refs, nil, nil) },
		"SetItemTags(add)":  func(ids []int64, _ []ItemRef) error { return a.SetItemTags(ctx, nil, ids, nil) },
		"AddToProject":      func(_ []int64, refs []ItemRef) error { return a.AddToProject(ctx, 1, refs) },
		"RemoveFromProject": func(_ []int64, refs []ItemRef) error { return a.RemoveFromProject(ctx, 1, refs) },
		"MergeContacts": func(ids []int64, _ []ItemRef) error {
			_, err := a.MergeContacts(ctx, ids, nil)
			return err
		},
	}
	for name, call := range cases {
		err := call(ids, refs)
		if e := AsError(err); e == nil || e.Status != 413 || e.Code != CodeTooManyItems {
			t.Errorf("%s with %d ids: err = %v, want 413 %s", name, len(ids), err, CodeTooManyItems)
		}
		// Exactly at the limit the request is accepted by the check (it may
		// still fail later because the ids do not exist).
		if err := call(ids[:maxBulkIDs], refs[:maxBulkIDs]); AsError(err) != nil && AsError(err).Code == CodeTooManyItems {
			t.Errorf("%s with %d ids is refused", name, maxBulkIDs)
		}
	}
}
