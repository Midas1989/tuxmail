// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"context"
	"testing"
	"time"
)

func TestTrustObserveAndVerify(t *testing.T) {
	ctx := context.Background()
	ts := NewMemoryTrustStore()
	k1 := testKey(t, "peer@example.org")
	k2 := testKey(t, "peer2@example.org")
	t0 := mustTime("2026-01-01T10:00:00Z")
	t1 := t0.Add(24 * time.Hour)

	res, err := ObserveKey(ctx, ts, "Peer@Example.org", k1.Fingerprint(), SourceAutocrypt, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.New || res.Conflict || res.Record.Level != TrustTOFU || !res.Record.FirstSeen.Equal(t0) {
		t.Fatalf("first observation: %+v", res)
	}
	res, _ = ObserveKey(ctx, ts, "peer@example.org", k1.Fingerprint(), SourceAutocrypt, t1)
	if res.New || !res.Record.LastSeen.Equal(t1) || !res.Record.FirstSeen.Equal(t0) || res.Record.SeenCount != 2 {
		t.Fatalf("second observation: %+v", res.Record)
	}
	// An out-of-order older message does not move last-seen backwards.
	res, _ = ObserveKey(ctx, ts, "peer@example.org", k1.Fingerprint(), SourceAutocrypt, t0.Add(time.Hour))
	if !res.Record.LastSeen.Equal(t1) {
		t.Fatalf("last seen moved back: %v", res.Record.LastSeen)
	}

	// A different key for the same address is a TOFU conflict.
	res, _ = ObserveKey(ctx, ts, "peer@example.org", k2.Fingerprint(), SourceAttached, t1)
	if !res.Conflict || len(res.ConflictingFingerprints) != 1 || res.ConflictingFingerprints[0] != k1.Fingerprint() {
		t.Fatalf("conflict: %+v", res)
	}

	// Manual import only yields unknown trust.
	res, _ = ObserveKey(ctx, ts, "other@example.org", k2.Fingerprint(), SourceManual, t1)
	if res.Record.Level != TrustUnknown {
		t.Fatalf("manual: %s", res.Record.Level)
	}

	rec, err := VerifyKey(ctx, ts, "peer@example.org", k1.Fingerprint(), t1)
	if err != nil || rec.Level != TrustVerified || !rec.VerifiedAt.Equal(t1) {
		t.Fatalf("verify: %v %+v", err, rec)
	}
	// Later TOFU observations do not downgrade verified trust.
	res, _ = ObserveKey(ctx, ts, "peer@example.org", k1.Fingerprint(), SourceGossip, t1.Add(time.Hour))
	if res.Record.Level != TrustVerified {
		t.Fatalf("downgraded: %s", res.Record.Level)
	}
	if got := res.Record.Effective(k1, time.Time{}); got != TrustVerified {
		t.Fatalf("effective = %s", got)
	}
	list, _ := ts.ListByAddress(ctx, "PEER@example.org")
	if len(list) != 2 || list[0].Fingerprint != k1.Fingerprint() {
		t.Fatalf("list = %+v", list)
	}
	if err := ts.Delete(ctx, "peer@example.org", k2.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Get(ctx, "peer@example.org", k2.Fingerprint()); err == nil {
		t.Fatal("record not deleted")
	}
}

func TestTrustEffectiveExpired(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour)
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "e@example.org"}}, Lifetime: time.Hour, Now: created})
	if err != nil {
		t.Fatal(err)
	}
	if got := (TrustRecord{Level: TrustVerified}).Effective(k, time.Time{}); got != TrustExpired {
		t.Fatalf("effective = %s", got)
	}
	if got := (TrustRecord{}).Effective(nil, time.Time{}); got != TrustUnknown {
		t.Fatalf("empty = %s", got)
	}
}
