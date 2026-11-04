// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

// IMAP bookkeeping keywords (JunkRecorded, NonJunk, Important, Old ...) are
// not tags; tags made from them by earlier versions are removed on start
// unless the user also uses them.
func TestSystemKeywordTags(t *testing.T) {
	for _, kw := range []string{"JunkRecorded", "NonJunk", "$NotJunk", "$Junk", "Junk", "$MDNSent", "$Forwarded", "Old", "Important", `\Seen`, "$SubmitPending"} {
		if !IsSystemKeyword(kw) {
			t.Errorf("%s should be a system keyword", kw)
		}
		if _, ok := DefaultKeywordMapper.FromKeyword(kw); ok {
			t.Errorf("%s must not become a tag", kw)
		}
	}
	if p, ok := DefaultKeywordMapper.FromKeyword("Kunde"); !ok || p != "Kunde" {
		t.Errorf("ordinary keyword: %q %v", p, ok)
	}

	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	m := Item{ItemMessage, upsertMsg(t, s, acc, "m@x", "fp")}
	_, _, err := s.SetProviderTags(ctx, m, TagSourceIMAPKeyword, []string{"JunkRecorded", "Kunde", "Important"})
	noErr(t, err)
	// The user also uses "Important" himself -> kept.
	imp := must[Tag](t)(s.TagByPath(ctx, "important"))
	_ = must[[]Item](t)(s.TagItems(ctx, imp.ID, TagSourceUser, Item{ItemMessage, upsertMsg(t, s, acc, "m2@x", "fp2")}))
	n := must[int](t)(s.PurgeSystemKeywordTags(ctx))
	if n != 1 {
		t.Fatalf("purged %d tags, want 1 (JunkRecorded)", n)
	}
	paths := must[[]string](t)(s.TagPaths(ctx, m))
	for _, p := range paths {
		if p == "JunkRecorded" {
			t.Errorf("JunkRecorded still assigned: %v", paths)
		}
	}
	if _, err := s.TagByPath(ctx, "kunde"); err != nil {
		t.Errorf("ordinary tag removed: %v", err)
	}
}
