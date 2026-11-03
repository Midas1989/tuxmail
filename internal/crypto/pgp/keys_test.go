// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerateDefaultKey(t *testing.T) {
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{
		{Name: "Alice", Email: "alice@example.org"},
		{Name: "Alice Work", Email: "Alice@Work.example"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	info := k.Info(time.Time{})
	if info.Version != 4 || len(info.Fingerprint) != 40 {
		t.Fatalf("unexpected version/fingerprint: %d %s", info.Version, info.Fingerprint)
	}
	if !strings.HasPrefix(info.Algorithm, "EdDSA") {
		t.Errorf("algorithm = %q", info.Algorithm)
	}
	if !info.Secret || info.Locked {
		t.Errorf("secret=%v locked=%v", info.Secret, info.Locked)
	}
	if info.Status != StatusValid {
		t.Errorf("status = %s", info.Status)
	}
	if !info.Capabilities.Sign || !info.Capabilities.Certify || info.Capabilities.Encrypt {
		t.Errorf("primary caps = %+v", info.Capabilities)
	}
	if !info.Usable.Sign || !info.Usable.Encrypt {
		t.Errorf("usable = %+v", info.Usable)
	}
	if len(info.Subkeys) != 1 || !info.Subkeys[0].Capabilities.Encrypt || info.Subkeys[0].Capabilities.String() != "E" {
		t.Fatalf("subkeys = %+v", info.Subkeys)
	}
	if !strings.HasPrefix(info.Subkeys[0].Algorithm, "ECDH") {
		t.Errorf("subkey algorithm = %q", info.Subkeys[0].Algorithm)
	}
	if !info.Expires.IsZero() {
		t.Errorf("expires = %v, want never", info.Expires)
	}
	if len(info.UserIDs) != 2 || !info.UserIDs[0].Primary || info.UserIDs[0].Email != "alice@example.org" {
		t.Fatalf("user IDs = %+v", info.UserIDs)
	}
	if !k.HasEmail("ALICE@work.example") || !k.HasEmail("Alice <alice@example.org>") || k.HasEmail("bob@example.org") {
		t.Errorf("HasEmail mismatch: %v", k.Emails())
	}
}

func TestGenerateV6Key(t *testing.T) {
	k, err := GenerateKey(GenerateOptions{
		Algorithm:  AlgoCurve25519V6,
		UserIDs:    []UserIDSpec{{Name: "Six", Email: "six@example.org"}},
		Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatal(err)
	}
	info := k.Info(time.Time{})
	if info.Version != 6 || len(info.Fingerprint) != 64 || info.Algorithm != "Ed25519" {
		t.Fatalf("info = %+v", info)
	}
	if info.Subkeys[0].Algorithm != "X25519" {
		t.Errorf("subkey algorithm = %s", info.Subkeys[0].Algorithm)
	}
	if !k.IsLocked() {
		t.Fatal("v6 key should be locked")
	}
	u, err := k.Unlock([]byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	// Round trip through PGP/MIME.
	enc, err := EncryptMIME(testInner, []*Key{u.Public()}, EncryptOptions{Signer: u})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ringWith(t, staticPassphrase("pw", nil), k).ReadMessage(ctxBG, enc.Bytes(), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature != SigValid || string(res.Content) != string(testInner) {
		t.Fatalf("v6 round trip: sig=%s err=%v", res.Signature, res.SignatureError)
	}
}

func TestGenerateRSA4096(t *testing.T) {
	if testing.Short() {
		t.Skip("RSA-4096 generation is slow")
	}
	k, err := GenerateKey(GenerateOptions{Algorithm: AlgoRSA4096, UserIDs: []UserIDSpec{{Email: "rsa@example.org"}}})
	if err != nil {
		t.Fatal(err)
	}
	info := k.Info(time.Time{})
	if info.Algorithm != "RSA-4096" || info.BitLength != 4096 || info.Subkeys[0].BitLength != 4096 {
		t.Fatalf("info = %+v", info)
	}
}

func TestGenerateRejectsBadInput(t *testing.T) {
	if _, err := GenerateKey(GenerateOptions{}); err == nil {
		t.Error("v4 key without user ID must fail")
	}
	if _, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "<x@y>"}}}); err == nil {
		t.Error("invalid characters must fail")
	}
	if _, err := GenerateKey(GenerateOptions{Algorithm: "dsa", UserIDs: []UserIDSpec{{Email: "x@y"}}}); err == nil {
		t.Error("unknown algorithm must fail")
	}
}

func TestPassphraseLockUnlockChange(t *testing.T) {
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "pw@example.org"}}, Passphrase: []byte("old")})
	if err != nil {
		t.Fatal(err)
	}
	if !k.IsLocked() {
		t.Fatal("expected locked key")
	}
	if _, err := k.Unlock([]byte("wrong")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := SignMIME(testInner, k, SignOptions{}); !errors.Is(err, ErrKeyLocked) {
		t.Fatalf("signing with locked key: %v", err)
	}
	k2, err := k.ChangePassphrase([]byte("old"), []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if !k2.IsLocked() {
		t.Fatal("changed key should be locked")
	}
	if _, err := k2.Unlock([]byte("old")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatal("old passphrase must no longer work")
	}
	u, err := k2.Unlock([]byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if u.IsLocked() || k2.Fingerprint() != k.Fingerprint() {
		t.Fatal("unlock result wrong")
	}
	// The original key is untouched.
	if !k.IsLocked() {
		t.Fatal("receiver must not be modified")
	}
	// Remove protection.
	plain, err := k2.ChangePassphrase([]byte("new"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.IsLocked() {
		t.Fatal("expected unprotected key")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "exp@example.org"}}, Passphrase: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	pubArmor, err := k.ArmoredPublic()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pubArmor, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
		t.Fatalf("armor: %q", pubArmor[:40])
	}
	secArmor, err := k.ArmoredSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secArmor, "-----BEGIN PGP PRIVATE KEY BLOCK-----") {
		t.Fatal("secret armor header missing")
	}
	pubBin, _ := k.PublicBytes()
	secBin, _ := k.SecretBytes()

	for name, data := range map[string][]byte{
		"pubArmor": []byte(pubArmor), "secArmor": []byte(secArmor),
		"pubBin": pubBin, "secBin": secBin,
	} {
		got, err := ReadKey(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Fingerprint() != k.Fingerprint() {
			t.Errorf("%s: fingerprint mismatch", name)
		}
		wantSecret := strings.HasPrefix(name, "sec")
		if got.IsSecret() != wantSecret {
			t.Errorf("%s: secret=%v", name, got.IsSecret())
		}
		if wantSecret && !got.IsLocked() {
			t.Errorf("%s: exported secret key must stay protected", name)
		}
	}

	// Multiple concatenated armored blocks.
	other := testKey(t, "other@example.org")
	otherArmor, _ := other.ArmoredPublic()
	keys, err := ReadKeys([]byte("some text\n" + pubArmor + "\n" + otherArmor))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[1].Fingerprint() != other.Fingerprint() {
		t.Fatalf("got %d keys", len(keys))
	}

	if _, err := ReadKeys([]byte("   ")); !errors.Is(err, ErrNoKeyData) {
		t.Errorf("empty input: %v", err)
	}
	if _, err := ReadKeys([]byte("garbage")); err == nil {
		t.Error("garbage must fail")
	}
	if pub := k.Public(); pub.IsSecret() {
		t.Error("Public() must strip secrets")
	}
	if _, err := k.Public().SecretBytes(); !errors.Is(err, ErrNotSecretKey) {
		t.Errorf("SecretBytes on public key: %v", err)
	}
}

func TestRevokeKeyAndSubkey(t *testing.T) {
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "rev@example.org"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Public().Revoke(RevocationRetired, "", time.Time{}); !errors.Is(err, ErrNotSecretKey) {
		t.Errorf("revoke public: %v", err)
	}
	sub := k.Info(time.Time{}).Subkeys[0].Fingerprint
	ks, err := k.RevokeSubkey(sub, RevocationSuperseded, "rotated", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if ks.Info(time.Time{}).Subkeys[0].Status != StatusRevoked || ks.CanEncrypt(time.Time{}) {
		t.Error("subkey should be revoked")
	}
	if ks.Status(time.Time{}) != StatusValid {
		t.Error("primary must stay valid after subkey revocation")
	}

	kr, err := k.Revoke(RevocationCompromised, "lost laptop", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if k.Status(time.Time{}) != StatusValid {
		t.Error("receiver must not be modified")
	}
	// Publish the revocation: parse the exported public key.
	pub, _ := kr.ArmoredPublic()
	pk, err := ReadKey([]byte(pub))
	if err != nil {
		t.Fatal(err)
	}
	if pk.Status(time.Time{}) != StatusRevoked {
		t.Errorf("status = %s", pk.Status(time.Time{}))
	}
	if pk.CanEncrypt(time.Time{}) || pk.CanSign(time.Time{}) {
		t.Error("revoked key must not be usable")
	}
	if (TrustRecord{Level: TrustVerified}).Effective(pk, time.Time{}) != TrustRevoked {
		t.Error("effective trust must be revoked")
	}
}

func TestExpiredKey(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	k, err := GenerateKey(GenerateOptions{
		UserIDs:  []UserIDSpec{{Email: "exp@example.org"}},
		Lifetime: time.Hour,
		Now:      created,
	})
	if err != nil {
		t.Fatal(err)
	}
	info := k.Info(time.Time{})
	if !info.Expires.Equal(created.Add(time.Hour)) {
		t.Errorf("expires = %v, want %v", info.Expires, created.Add(time.Hour))
	}
	if info.Status != StatusExpired || k.Status(created.Add(30*time.Minute)) != StatusValid {
		t.Errorf("status now=%s, before expiry=%s", info.Status, k.Status(created.Add(30*time.Minute)))
	}
	if !info.Subkeys[0].Expires.Equal(info.Expires) || info.Subkeys[0].Status != StatusExpired {
		t.Errorf("subkey = %+v", info.Subkeys[0])
	}
	if k.CanEncrypt(time.Time{}) {
		t.Error("expired key must not be usable for encryption")
	}
	if _, err := EncryptMIME(testInner, []*Key{k}, EncryptOptions{}); !errors.Is(err, ErrNoEncryptionKey) {
		t.Errorf("encrypt to expired key: %v", err)
	}
	if _, err := SignMIME(testInner, k, SignOptions{}); !errors.Is(err, ErrNoSigningKey) {
		t.Errorf("sign with expired key: %v", err)
	}
}

func TestAddUserIDAndMinimalKey(t *testing.T) {
	k := testKey(t, "multi@example.org")
	k2, err := k.AddUserID(UserIDSpec{Name: "Second", Email: "second@example.org"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !k2.HasEmail("second@example.org") || !k2.HasEmail("multi@example.org") {
		t.Fatalf("emails = %v", k2.Emails())
	}
	min, err := k2.MinimalPublicKey("second@example.org", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	mk, err := ReadKey(min)
	if err != nil {
		t.Fatal(err)
	}
	if mk.IsSecret() || len(mk.UserIDs()) != 1 || !mk.HasEmail("second@example.org") || !mk.CanEncrypt(time.Time{}) {
		t.Fatalf("minimal key: uids=%v", mk.UserIDs())
	}
	full, _ := k2.PublicBytes()
	if len(min) >= len(full) {
		t.Errorf("minimal key (%d) not smaller than full key (%d)", len(min), len(full))
	}
}

func TestNormalize(t *testing.T) {
	if got := NormalizeFingerprint(" 0xab cd:ef "); got != "ABCDEF" {
		t.Errorf("NormalizeFingerprint = %q", got)
	}
	if got := NormalizeEmail(" Bob <Bob@Example.ORG> "); got != "bob@example.org" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}
