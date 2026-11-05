// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// Audit NF-03/NF-04: a hostile server answers UID SEARCH with a dynamic set
// ("1:*") or an absurdly large range. Neither may panic or allocate.
func TestSearchUIDsHostileResults(t *testing.T) {
	var dyn imap.UIDSet
	dyn.AddRange(1, 0) // 1:*
	if _, err := searchUIDs(&imap.SearchData{All: dyn}, 10); err == nil {
		t.Error("dynamic set 1:* accepted")
	}
	var huge imap.UIDSet
	huge.AddRange(1, 4294967295)
	if _, err := searchUIDs(&imap.SearchData{All: huge}, 10); err == nil {
		t.Error("range 1:4294967295 accepted for a 10 message folder")
	}
	var seq imap.SeqSet
	seq.AddRange(1, 3)
	if _, err := searchUIDs(&imap.SearchData{All: seq}, 10); err == nil {
		t.Error("sequence set accepted as UID set")
	}
}

func TestSearchUIDsNormalResults(t *testing.T) {
	var set imap.UIDSet
	set.AddRange(5, 7)
	set.AddNum(20)
	got, err := searchUIDs(&imap.SearchData{All: set}, 4)
	if err != nil || len(got) != 4 || got[0] != 5 || got[3] != 20 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := searchUIDs(&imap.SearchData{}, 4); err != nil || len(got) != 0 {
		t.Fatalf("empty result: %v, %v", got, err)
	}
	if got, err := searchUIDs(nil, 4); err != nil || len(got) != 0 {
		t.Fatalf("nil result: %v, %v", got, err)
	}
}
