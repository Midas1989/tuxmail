// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

// Audit I-2: an absurd offset is capped (no error, no huge scan), a
// cancelled context aborts the search.
func TestSearchBounds(t *testing.T) {
	f := newSearchFixture(t)
	res, err := f.s.SearchAdvanced(context.Background(), SearchRequest{Query: "rechnung", Offset: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("hits beyond the offset cap: %d", len(res.Hits))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "rechnung"}); err == nil {
		t.Error("cancelled search succeeded")
	}
}
