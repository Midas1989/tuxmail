// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeLookup struct {
	keys map[string][]*Key
	err  error
}

func (f *fakeLookup) LookupByEmail(ctx context.Context, email string) ([]*Key, error) {
	if f.err != nil {
		return nil, f.err
	}
	if k, ok := f.keys[NormalizeEmail(email)]; ok {
		return k, nil
	}
	return nil, ErrKeyNotFound
}

func (f *fakeLookup) Source() KeySource { return SourceWKD }

func TestPlannerPlan(t *testing.T) {
	ctx := context.Background()
	me := testKey(t, "me@example.org")
	bob := testKey(t, "bob@example.org")
	carol := testKey(t, "carol@example.org")
	now := time.Now()

	ring := ringWith(t, nil, me, bob.Public())
	peers := NewMemoryPeerStore()
	trust := NewMemoryTrustStore()
	hc, _ := NewAutocryptHeader("carol@example.org", carol, PreferMutual, time.Time{})
	pc := PeerState{Addr: "carol@example.org"}
	pc.UpdateFromHeader(hc, now.Add(-time.Hour))
	if err := peers.Put(ctx, pc); err != nil {
		t.Fatal(err)
	}

	pl := &Planner{Ring: ring, Peers: peers, Trust: trust, OwnPreferEncrypt: PreferMutual}

	plan, err := pl.Plan(ctx, []string{"Carol <carol@example.org>", "carol@example.org"}, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanEncrypt || plan.Recommendation != RecEncrypt || len(plan.Recipients) != 1 {
		t.Fatalf("carol plan = %+v", plan)
	}
	rc := plan.Recipients[0]
	if rc.Source != SourceAutocrypt || rc.Trust != TrustTOFU || rc.Fingerprint != carol.Fingerprint() {
		t.Fatalf("carol = %+v", rc)
	}

	// Bob is only in the key ring (manually imported, unknown trust).
	plan, _ = pl.Plan(ctx, []string{"bob@example.org", "carol@example.org"}, false, now)
	if !plan.CanEncrypt || plan.Recommendation != RecAvailable || len(plan.Keys()) != 2 {
		t.Fatalf("bob+carol = %+v", plan)
	}
	if plan.Recipients[0].Source != SourceManual || plan.Recipients[0].Trust != TrustUnknown {
		t.Fatalf("bob = %+v", plan.Recipients[0])
	}
	// Reply to an encrypted message -> encrypt.
	plan, _ = pl.Plan(ctx, []string{"bob@example.org", "carol@example.org"}, true, now)
	if plan.Recommendation != RecEncrypt {
		t.Fatalf("reply = %s", plan.Recommendation)
	}

	// Missing key.
	plan, _ = pl.Plan(ctx, []string{"bob@example.org", "dave@example.org"}, false, now)
	if plan.CanEncrypt || len(plan.MissingKeys) != 1 || plan.MissingKeys[0] != "dave@example.org" || plan.Recommendation != RecDisable {
		t.Fatalf("missing = %+v", plan)
	}

	// Own address uses the own secret key.
	plan, _ = pl.Plan(ctx, []string{"me@example.org"}, false, now)
	if r := plan.Recipients[0]; r.Source != SourceOwn || r.Trust != TrustVerified || r.Recommendation != RecEncrypt {
		t.Fatalf("self = %+v", r)
	}

	// A verified key-ring key wins over a different Autocrypt key.
	if _, err := VerifyKey(ctx, trust, "bob@example.org", bob.Fingerprint(), now); err != nil {
		t.Fatal(err)
	}
	hb, _ := NewAutocryptHeader("bob@example.org", carol, PreferMutual, time.Time{}) // wrong key for bob
	pb := PeerState{Addr: "bob@example.org"}
	pb.UpdateFromHeader(hb, now)
	_ = peers.Put(ctx, pb)
	plan, _ = pl.Plan(ctx, []string{"bob@example.org"}, false, now)
	if r := plan.Recipients[0]; r.Fingerprint != bob.Fingerprint() || r.Trust != TrustVerified {
		t.Fatalf("verified bob = %+v", r)
	}

	// Empty recipient list.
	plan, _ = pl.Plan(ctx, nil, false, now)
	if plan.CanEncrypt || plan.Recommendation != RecDisable {
		t.Fatalf("empty = %+v", plan)
	}
}

func TestPlannerFetchMissing(t *testing.T) {
	ctx := context.Background()
	dave := testKey(t, "dave@example.org")
	ring := ringWith(t, nil)
	trust := NewMemoryTrustStore()
	pl := &Planner{Ring: ring, Trust: trust}
	now := time.Now()

	plan, _ := pl.Plan(ctx, []string{"dave@example.org", "eve@example.org"}, false, now)
	if len(plan.MissingKeys) != 2 {
		t.Fatalf("missing = %v", plan.MissingKeys)
	}
	failing := &fakeLookup{err: errors.New("network down")}
	lookup := &fakeLookup{keys: map[string][]*Key{"dave@example.org": {dave.Public()}}}
	found, err := pl.FetchMissing(ctx, plan, now, failing, lookup)
	if err == nil {
		t.Error("network errors should be reported")
	}
	if len(found) != 1 || found[0] != "dave@example.org" {
		t.Fatalf("found = %v", found)
	}
	plan, _ = pl.Plan(ctx, []string{"dave@example.org"}, false, now)
	if !plan.CanEncrypt || plan.Recipients[0].Trust != TrustTOFU {
		t.Fatalf("after fetch = %+v", plan.Recipients[0])
	}
	k, _ := ring.Get(ctx, dave.Fingerprint())
	if k.IsSecret() {
		t.Fatal("fetched key must be public")
	}
}
