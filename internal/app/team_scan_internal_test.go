// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"testing"

	"github.com/Midas1989/Mailclient/internal/team"
)

// fakeMailbox serves n conversations (ids 1..n, newest first) in pages.
type fakeMailbox struct {
	n     int
	calls int
	read  int
}

func (f *fakeMailbox) list(_ context.Context, q ConversationQuery) (Page[Conversation], error) {
	f.calls++
	var items []Conversation
	for i := q.Offset; i < min(f.n, q.Offset+q.Limit); i++ {
		items = append(items, Conversation{ID: int64(i + 1)})
	}
	f.read += len(items)
	return Page[Conversation]{Items: items, Total: f.n, Offset: q.Offset}, nil
}

// NH-11: the unassigned/open views stop scanning once the requested page is
// filled, and never read more than teamScanLimit conversations.
func TestTeamViewScanIsBounded(t *testing.T) {
	ctx := context.Background()
	none := func(context.Context, []int64) (map[int64]team.Assignment, error) { return nil, nil }

	f := &fakeMailbox{n: 100000}
	got, total, err := scanTeamView(ctx, ConversationQuery{Limit: 50}, team.ViewOpen, 1, f.list, none)
	if err != nil || len(got) != 50 {
		t.Fatalf("page of %d, %v", len(got), err)
	}
	if f.calls != 1 || total <= 50 {
		t.Fatalf("%d list calls, total %d: the scan did not stop at the first page", f.calls, total)
	}

	// Second page: offset 50 needs one more step at most.
	f = &fakeMailbox{n: 100000}
	got, total, _ = scanTeamView(ctx, ConversationQuery{Offset: 50, Limit: 50}, team.ViewOpen, 1, f.list, none)
	if len(got) != 50 || total <= 100 || f.calls != 1 {
		t.Fatalf("second page: %d items, total %d, %d calls", len(got), total, f.calls)
	}

	// Nothing matches (all done): bounded by teamScanLimit, not by the mailbox.
	f = &fakeMailbox{n: 100000}
	done := func(_ context.Context, ids []int64) (map[int64]team.Assignment, error) {
		m := map[int64]team.Assignment{}
		for _, id := range ids {
			m[id] = team.Assignment{ConversationID: id, Status: team.StatusDone}
		}
		return m, nil
	}
	got, total, err = scanTeamView(ctx, ConversationQuery{Limit: 50}, team.ViewOpen, 1, f.list, done)
	if err != nil || len(got) != 0 || total != 0 {
		t.Fatalf("done conversations listed as open: %d, %d, %v", len(got), total, err)
	}
	if f.read > teamScanLimit || f.calls > teamScanLimit/500 {
		t.Fatalf("read %d conversations in %d calls, limit %d", f.read, f.calls, teamScanLimit)
	}

	// A small mailbox is scanned completely and the total is exact.
	f = &fakeMailbox{n: 30}
	got, total, _ = scanTeamView(ctx, ConversationQuery{Limit: 50}, team.ViewUnassigned, 1, f.list, none)
	if len(got) != 30 || total != 30 {
		t.Fatalf("small mailbox: %d items, total %d", len(got), total)
	}
}
