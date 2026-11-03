// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryKeyStore(t *testing.T) {
	s := NewMemoryKeyStore()
	ctx := context.Background()
	if _, err := s.Get(ctx, "AA"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if err := s.Delete(ctx, "AA"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := s.Put(ctx, StoredKey{Fingerprint: "0xbb", Data: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, StoredKey{Fingerprint: "aa", Data: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "BB")
	if err != nil || got.Data[0] != 1 {
		t.Fatalf("get: %v %+v", err, got)
	}
	got.Data[0] = 9 // must not affect the store
	list, _ := s.List(ctx)
	if len(list) != 2 || list[0].Fingerprint != "AA" || list[1].Data[0] != 1 {
		t.Fatalf("list = %+v", list)
	}
	if err := s.Delete(ctx, "aa"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.List(cctx); err == nil {
		t.Fatal("cancelled context must fail")
	}
}

func TestKeyRingImportMergeAndLookup(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryKeyStore()
	ring := NewKeyRing(store, nil)
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")

	pubArmor, _ := alice.ArmoredPublic()
	res, err := ring.Import(ctx, []byte(pubArmor))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].New || res[0].Secret {
		t.Fatalf("import public: %+v", res)
	}
	// Importing the same key again changes nothing.
	res, _ = ring.Import(ctx, []byte(pubArmor))
	if res[0].New || res[0].Updated {
		t.Fatalf("re-import: %+v", res)
	}
	// Importing the secret key upgrades the entry.
	secArmor, _ := alice.ArmoredSecret()
	res, err = ring.Import(ctx, []byte(secArmor))
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Updated || !res[0].Secret {
		t.Fatalf("import secret: %+v", res)
	}
	// Importing the public key again keeps the secret material.
	if _, err := ring.Import(ctx, []byte(pubArmor)); err != nil {
		t.Fatal(err)
	}
	k, err := ring.Get(ctx, alice.Fingerprint())
	if err != nil || !k.IsSecret() {
		t.Fatalf("secret lost: %v", err)
	}

	// A new user ID on the public key is merged into the stored secret key.
	withUID, err := alice.AddUserID(UserIDSpec{Email: "alice@new.example"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := ring.ImportKey(ctx, withUID.Public())
	if err != nil || !res2.Updated {
		t.Fatalf("merge uid: %v %+v", err, res2)
	}
	k, _ = ring.Get(ctx, alice.Fingerprint())
	if !k.IsSecret() || !k.HasEmail("alice@new.example") {
		t.Fatalf("merged key: secret=%v emails=%v", k.IsSecret(), k.Emails())
	}

	if _, err := ring.ImportKey(ctx, bob.Public()); err != nil {
		t.Fatal(err)
	}
	found, _ := ring.FindByEmail(ctx, "BOB@example.org")
	if len(found) != 1 || found[0].Fingerprint() != bob.Fingerprint() {
		t.Fatalf("FindByEmail = %v", found)
	}
	secrets, _ := ring.SecretKeys(ctx)
	if len(secrets) != 1 {
		t.Fatalf("secret keys = %d", len(secrets))
	}
	byID, _ := ring.FindByKeyID(ctx, bob.Entity().Subkeys[0].PublicKey.KeyId)
	if len(byID) != 1 {
		t.Fatalf("FindByKeyID = %d", len(byID))
	}

	// A new ring on the same store sees the persisted keys.
	ring2 := NewKeyRing(store, nil)
	all, err := ring2.Keys(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("reload: %v %d", err, len(all))
	}
	if err := ring2.Delete(ctx, bob.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := ring2.Get(ctx, bob.Fingerprint()); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
	if _, err := store.Get(ctx, bob.Fingerprint()); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("store still has the key: %v", err)
	}
}

func TestKeyRingUnlockChangePassphraseRevoke(t *testing.T) {
	ctx := context.Background()
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "ring@example.org"}}, Passphrase: []byte("one")})
	if err != nil {
		t.Fatal(err)
	}
	pass := "one"
	pf := func(ctx context.Context, req PassphraseRequest) ([]byte, error) { return []byte(pass), nil }
	ring := ringWith(t, pf, k)

	u, err := ring.Unlock(ctx, k.Fingerprint())
	if err != nil || u.IsLocked() {
		t.Fatalf("unlock: %v", err)
	}
	if err := ring.ChangePassphrase(ctx, k.Fingerprint(), []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Unlock(ctx, k.Fingerprint()); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("old passphrase still works: %v", err)
	}
	pass = "two"
	stored, _ := ring.Get(ctx, k.Fingerprint())
	if !stored.IsLocked() {
		t.Fatal("stored key must be locked")
	}
	revoked, err := ring.Revoke(ctx, k.Fingerprint(), RevocationRetired, "bye")
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status(time.Time{}) != StatusRevoked || !revoked.IsLocked() || !revoked.IsSecret() {
		t.Fatalf("revoked: status=%s locked=%v", revoked.Status(time.Time{}), revoked.IsLocked())
	}
	if _, err := ring.Unlock(ctx, testKey(t, "alice@example.org").Fingerprint()); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("unknown key: %v", err)
	}
	pubRing := ringWith(t, nil, k.Public())
	if _, err := pubRing.Unlock(ctx, k.Fingerprint()); !errors.Is(err, ErrNotSecretKey) {
		t.Fatalf("public key unlock: %v", err)
	}
	noCb := ringWith(t, nil, k)
	if _, err := noCb.Unlock(ctx, k.Fingerprint()); !errors.Is(err, ErrNoPassphrase) {
		t.Fatalf("no callback: %v", err)
	}
}

func TestMergeKeysRejectsDifferentKeys(t *testing.T) {
	if _, _, err := MergeKeys(testKey(t, "a@example.org"), testKey(t, "b@example.org")); err == nil {
		t.Fatal("expected error")
	}
}

func TestKeyRingConcurrentReads(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	ring := ringWith(t, nil, bob, alice.Public())
	enc, err := EncryptMIME(testInner, []*Key{bob}, EncryptOptions{Signer: alice})
	if err != nil {
		t.Fatal(err)
	}
	raw := enc.Bytes()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ring.ReadMessage(context.Background(), raw, ReadOptions{})
			if err != nil || res.Signature != SigValid || !bytes.Equal(res.Content, testInner) {
				t.Errorf("concurrent read: %v %+v", err, res)
			}
		}()
	}
	wg.Wait()
}
