// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// StoredKey is the persistence form of a key.
type StoredKey struct {
	// Fingerprint of the primary key (upper-case hex, see Key.Fingerprint).
	Fingerprint string
	// Data is the binary transferable key (secret keys keep their
	// passphrase protection).
	Data []byte
	// Secret reports whether Data contains secret key material.
	Secret bool
	// Emails are the lower-cased addresses of the user IDs (an index hint
	// for persistent stores; KeyRing does not rely on it).
	Emails []string
	// Updated is the time the entry was last written.
	Updated time.Time
}

// KeyStore persists keys by fingerprint. Implementations must be safe for
// concurrent use. Get and Delete return ErrKeyNotFound for unknown
// fingerprints.
type KeyStore interface {
	List(ctx context.Context) ([]StoredKey, error)
	Get(ctx context.Context, fingerprint string) (StoredKey, error)
	Put(ctx context.Context, key StoredKey) error
	Delete(ctx context.Context, fingerprint string) error
}

// MemoryKeyStore is an in-memory KeyStore.
type MemoryKeyStore struct {
	mu   sync.RWMutex
	keys map[string]StoredKey
}

// NewMemoryKeyStore returns an empty in-memory key store.
func NewMemoryKeyStore() *MemoryKeyStore {
	return &MemoryKeyStore{keys: map[string]StoredKey{}}
}

// List implements KeyStore. Entries are sorted by fingerprint.
func (m *MemoryKeyStore) List(ctx context.Context) ([]StoredKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StoredKey, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, copyStored(k))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
}

// Get implements KeyStore.
func (m *MemoryKeyStore) Get(ctx context.Context, fingerprint string) (StoredKey, error) {
	if err := ctx.Err(); err != nil {
		return StoredKey{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.keys[NormalizeFingerprint(fingerprint)]
	if !ok {
		return StoredKey{}, ErrKeyNotFound
	}
	return copyStored(k), nil
}

// Put implements KeyStore.
func (m *MemoryKeyStore) Put(ctx context.Context, key StoredKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key = copyStored(key)
	key.Fingerprint = NormalizeFingerprint(key.Fingerprint)
	m.keys[key.Fingerprint] = key
	return nil
}

// Delete implements KeyStore.
func (m *MemoryKeyStore) Delete(ctx context.Context, fingerprint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fpr := NormalizeFingerprint(fingerprint)
	if _, ok := m.keys[fpr]; !ok {
		return ErrKeyNotFound
	}
	delete(m.keys, fpr)
	return nil
}

func copyStored(k StoredKey) StoredKey {
	k.Data = append([]byte(nil), k.Data...)
	k.Emails = append([]string(nil), k.Emails...)
	return k
}

// PassphraseRequest describes the key a passphrase is requested for.
type PassphraseRequest struct {
	Fingerprint string
	KeyID       string
	UserID      string // primary user ID, for display
	// Attempt starts at 1 and is incremented after each wrong passphrase.
	Attempt int
}

// PassphraseFunc supplies the passphrase for a locked secret key. Returning
// an error aborts the operation (e.g. the user cancelled the dialog).
type PassphraseFunc func(ctx context.Context, req PassphraseRequest) ([]byte, error)

// MaxPassphraseAttempts is the number of times a PassphraseFunc is asked for
// the same key before an operation fails with ErrWrongPassphrase.
const MaxPassphraseAttempts = 3

// KeyRing is a thread-safe collection of public and secret keys backed by a
// KeyStore. Parsed keys are cached in memory; all modifications must go
// through the KeyRing to keep the cache consistent.
type KeyRing struct {
	store      KeyStore
	passphrase PassphraseFunc

	mu     sync.RWMutex
	loaded bool
	keys   map[string]*Key // by fingerprint
}

// NewKeyRing creates a key ring on top of store. passphrase may be nil if
// all secret keys are unprotected.
func NewKeyRing(store KeyStore, passphrase PassphraseFunc) *KeyRing {
	if store == nil {
		store = NewMemoryKeyStore()
	}
	return &KeyRing{store: store, passphrase: passphrase, keys: map[string]*Key{}}
}

// SetPassphraseFunc replaces the passphrase callback.
func (r *KeyRing) SetPassphraseFunc(f PassphraseFunc) {
	r.mu.Lock()
	r.passphrase = f
	r.mu.Unlock()
}

func (r *KeyRing) ensureLoaded(ctx context.Context) error {
	r.mu.RLock()
	loaded := r.loaded
	r.mu.RUnlock()
	if loaded {
		return nil
	}
	stored, err := r.store.List(ctx)
	if err != nil {
		return fmt.Errorf("pgp: list keys: %w", err)
	}
	keys := map[string]*Key{}
	for _, s := range stored {
		k, err := ReadKey(s.Data)
		if err != nil {
			// Skip unparsable entries instead of failing the whole ring.
			continue
		}
		keys[k.Fingerprint()] = k
	}
	r.mu.Lock()
	if !r.loaded {
		r.keys = keys
		r.loaded = true
	}
	r.mu.Unlock()
	return nil
}

// Reload drops the in-memory cache; the next access reloads from the store.
func (r *KeyRing) Reload() {
	r.mu.Lock()
	r.loaded = false
	r.keys = map[string]*Key{}
	r.mu.Unlock()
}

// Keys returns all keys sorted by fingerprint.
func (r *KeyRing) Keys(ctx context.Context) ([]*Key, error) {
	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Key, 0, len(r.keys))
	for _, k := range r.keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint() < out[j].Fingerprint() })
	return out, nil
}

// SecretKeys returns all keys with secret key material.
func (r *KeyRing) SecretKeys(ctx context.Context) ([]*Key, error) {
	all, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Key
	for _, k := range all {
		if k.IsSecret() {
			out = append(out, k)
		}
	}
	return out, nil
}

// Get returns the key with the given primary key fingerprint.
func (r *KeyRing) Get(ctx context.Context, fingerprint string) (*Key, error) {
	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[NormalizeFingerprint(fingerprint)]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return k, nil
}

// FindByEmail returns all keys with a user ID for the address, best first:
// valid keys before invalid ones, secret before public, newest first.
func (r *KeyRing) FindByEmail(ctx context.Context, email string) ([]*Key, error) {
	all, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []*Key
	for _, k := range all {
		if k.HasEmail(email) {
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		vi, vj := out[i].CanEncrypt(now), out[j].CanEncrypt(now)
		if vi != vj {
			return vi
		}
		if out[i].IsSecret() != out[j].IsSecret() {
			return out[i].IsSecret()
		}
		return out[i].CreationTime().After(out[j].CreationTime())
	})
	return out, nil
}

// FindByKeyID returns keys containing a primary key or subkey with the
// given 64-bit key ID.
func (r *KeyRing) FindByKeyID(ctx context.Context, keyID uint64) ([]*Key, error) {
	all, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Key
	for _, k := range all {
		if entityHasKeyID(k.entity, keyID) {
			out = append(out, k)
		}
	}
	return out, nil
}

func entityHasKeyID(e *openpgp.Entity, id uint64) bool {
	if e.PrimaryKey.KeyId == id {
		return true
	}
	for _, sk := range e.Subkeys {
		if sk.PublicKey.KeyId == id {
			return true
		}
	}
	return false
}

// Put stores a key as-is, replacing any existing key with the same
// fingerprint. Use Import to merge with existing data.
func (r *KeyRing) Put(ctx context.Context, k *Key) error {
	if err := r.ensureLoaded(ctx); err != nil {
		return err
	}
	data, err := k.Bytes()
	if err != nil {
		return err
	}
	// Store a private copy so that callers cannot mutate cached state.
	stored, err := ReadKey(data)
	if err != nil {
		return err
	}
	sk := StoredKey{
		Fingerprint: k.Fingerprint(),
		Data:        data,
		Secret:      k.entity.PrivateKey != nil,
		Emails:      k.Emails(),
		Updated:     time.Now().UTC(),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.store.Put(ctx, sk); err != nil {
		return fmt.Errorf("pgp: store key: %w", err)
	}
	r.keys[sk.Fingerprint] = stored
	return nil
}

// Delete removes a key.
func (r *KeyRing) Delete(ctx context.Context, fingerprint string) error {
	if err := r.ensureLoaded(ctx); err != nil {
		return err
	}
	fpr := NormalizeFingerprint(fingerprint)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.store.Delete(ctx, fpr); err != nil {
		return err
	}
	delete(r.keys, fpr)
	return nil
}

// ImportResult reports what Import did for one key.
type ImportResult struct {
	Fingerprint string
	New         bool // the key was not known before
	Updated     bool // an existing key was changed (merged)
	Secret      bool // the stored key contains secret material
}

// Import parses armored or binary key data and merges every key into the
// ring. Existing keys are merged: new user IDs, subkeys, signatures and
// revocations are added; secret material is kept or added.
func (r *KeyRing) Import(ctx context.Context, data []byte) ([]ImportResult, error) {
	keys, err := ReadKeys(data)
	if err != nil {
		return nil, err
	}
	var results []ImportResult
	for _, k := range keys {
		res, err := r.ImportKey(ctx, k)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// ImportKey merges a single parsed key into the ring.
func (r *KeyRing) ImportKey(ctx context.Context, k *Key) (ImportResult, error) {
	res := ImportResult{Fingerprint: k.Fingerprint()}
	existing, err := r.Get(ctx, k.Fingerprint())
	switch {
	case errors.Is(err, ErrKeyNotFound):
		res.New = true
		res.Secret = k.entity.PrivateKey != nil
		return res, r.Put(ctx, k)
	case err != nil:
		return res, err
	}
	merged, changed, err := MergeKeys(existing, k)
	if err != nil {
		return res, err
	}
	res.Updated = changed
	res.Secret = merged.entity.PrivateKey != nil
	if !changed {
		return res, nil
	}
	return res, r.Put(ctx, merged)
}

// MergeKeys merges update into base (both must have the same fingerprint)
// and returns the result and whether anything changed. Neither input is
// modified.
func MergeKeys(base, update *Key) (*Key, bool, error) {
	if base.Fingerprint() != update.Fingerprint() {
		return nil, false, errors.New("pgp: cannot merge keys with different fingerprints")
	}
	// Work on private copies: go-crypto mutates cached state even when
	// serialising signatures.
	var err error
	if base, err = base.Clone(); err != nil {
		return nil, false, err
	}
	if update, err = update.Clone(); err != nil {
		return nil, false, err
	}
	// Start from the variant with secret material.
	changed := false
	var dst, src *Key
	if base.entity.PrivateKey == nil && update.entity.PrivateKey != nil {
		dst, err = update.Clone()
		src = base
		changed = true
	} else {
		dst, err = base.Clone()
		src = update
	}
	if err != nil {
		return nil, false, err
	}
	d, s := dst.entity, src.entity

	sigKey := func(sig *packet.Signature) string {
		var b bytes.Buffer
		_ = sig.Serialize(&b)
		return b.String()
	}
	mergeSigs := func(dstSigs []*packet.VerifiableSignature, srcSigs []*packet.VerifiableSignature) []*packet.VerifiableSignature {
		seen := map[string]bool{}
		for _, v := range dstSigs {
			seen[sigKey(v.Packet)] = true
		}
		for _, v := range srcSigs {
			key := sigKey(v.Packet)
			if !seen[key] {
				seen[key] = true
				dstSigs = append(dstSigs, packet.NewVerifiableSig(v.Packet))
				changed = true
			}
		}
		return dstSigs
	}

	d.Revocations = mergeSigs(d.Revocations, s.Revocations)
	d.DirectSignatures = mergeSigs(d.DirectSignatures, s.DirectSignatures)
	for name, sid := range s.Identities {
		did, ok := d.Identities[name]
		if !ok {
			d.Identities[name] = &openpgp.Identity{
				Primary:             d,
				Name:                sid.Name,
				UserId:              sid.UserId,
				SelfCertifications:  mergeSigs(nil, sid.SelfCertifications),
				OtherCertifications: mergeSigs(nil, sid.OtherCertifications),
				Revocations:         mergeSigs(nil, sid.Revocations),
			}
			changed = true
			continue
		}
		did.SelfCertifications = mergeSigs(did.SelfCertifications, sid.SelfCertifications)
		did.OtherCertifications = mergeSigs(did.OtherCertifications, sid.OtherCertifications)
		did.Revocations = mergeSigs(did.Revocations, sid.Revocations)
	}
	for _, ssk := range s.Subkeys {
		found := false
		for i := range d.Subkeys {
			dsk := &d.Subkeys[i]
			if bytes.Equal(dsk.PublicKey.Fingerprint, ssk.PublicKey.Fingerprint) {
				found = true
				dsk.Bindings = mergeSigs(dsk.Bindings, ssk.Bindings)
				dsk.Revocations = mergeSigs(dsk.Revocations, ssk.Revocations)
				if dsk.PrivateKey == nil && ssk.PrivateKey != nil {
					dsk.PrivateKey = ssk.PrivateKey
					changed = true
				}
			}
		}
		if !found {
			d.Subkeys = append(d.Subkeys, openpgp.Subkey{
				Primary:     d,
				PublicKey:   ssk.PublicKey,
				PrivateKey:  ssk.PrivateKey,
				Bindings:    mergeSigs(nil, ssk.Bindings),
				Revocations: mergeSigs(nil, ssk.Revocations),
			})
			changed = true
		}
	}
	if !changed {
		return base, false, nil
	}
	// Normalise (and deep-copy shared packets) through serialization.
	out, err := dst.Clone()
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// Unlock returns an unlocked copy of the secret key with the given
// fingerprint, asking the PassphraseFunc if necessary.
func (r *KeyRing) Unlock(ctx context.Context, fingerprint string) (*Key, error) {
	k, err := r.Get(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	return r.unlockKey(ctx, k)
}

func (r *KeyRing) unlockKey(ctx context.Context, k *Key) (*Key, error) {
	if !k.IsSecret() {
		return nil, ErrNotSecretKey
	}
	if !k.IsLocked() {
		return k.Clone()
	}
	r.mu.RLock()
	pf := r.passphrase
	r.mu.RUnlock()
	if pf == nil {
		return nil, ErrNoPassphrase
	}
	req := PassphraseRequest{Fingerprint: k.Fingerprint(), KeyID: k.KeyID(), UserID: primaryUID(k)}
	for attempt := 1; attempt <= MaxPassphraseAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req.Attempt = attempt
		pass, err := pf(ctx, req)
		if err != nil {
			return nil, err
		}
		u, err := k.Unlock(pass)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, ErrWrongPassphrase) {
			return nil, err
		}
	}
	return nil, ErrWrongPassphrase
}

func primaryUID(k *Key) string {
	uids := k.UserIDs()
	if len(uids) == 0 {
		return ""
	}
	return uids[0].Raw
}

// ChangePassphrase re-protects a stored secret key. The old passphrase is
// obtained via the PassphraseFunc.
func (r *KeyRing) ChangePassphrase(ctx context.Context, fingerprint string, newPassphrase []byte) error {
	u, err := r.Unlock(ctx, fingerprint)
	if err != nil {
		return err
	}
	nk, err := u.ChangePassphrase(nil, newPassphrase)
	if err != nil {
		return err
	}
	return r.Put(ctx, nk)
}

// Revoke revokes the stored secret key and stores the result. The
// passphrase is obtained via the PassphraseFunc. The stored key keeps its
// original passphrase protection.
func (r *KeyRing) Revoke(ctx context.Context, fingerprint string, reason RevocationReason, text string) (*Key, error) {
	k, err := r.Get(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	u, err := r.unlockKey(ctx, k)
	if err != nil {
		return nil, err
	}
	revoked, err := u.Revoke(reason, text, time.Time{})
	if err != nil {
		return nil, err
	}
	// Merge only the revocation into the stored (still locked) key.
	merged, _, err := MergeKeys(k, revoked.Public())
	if err != nil {
		return nil, err
	}
	if err := r.Put(ctx, merged); err != nil {
		return nil, err
	}
	return merged, nil
}
