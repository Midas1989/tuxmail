// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// TrustLevel is the trust TuxMail places in the binding between an address
// and a key.
type TrustLevel string

const (
	// TrustUnknown: the key is known but its binding to the address has not
	// been observed in a meaningful way (e.g. imported from a file or found
	// via a key server without further evidence).
	TrustUnknown TrustLevel = "unknown"
	// TrustTOFU: trust on first use; the key was seen in mail from this
	// address (Autocrypt header, gossip, attached key, WKD of the domain).
	TrustTOFU TrustLevel = "tofu"
	// TrustVerified: the user compared and confirmed the fingerprint.
	TrustVerified TrustLevel = "verified"
	// TrustRevoked: the key has been revoked. Derived from the key itself.
	TrustRevoked TrustLevel = "revoked"
	// TrustExpired: the key has expired. Derived from the key itself.
	TrustExpired TrustLevel = "expired"
)

// rank orders the stored (non-derived) trust levels.
func (l TrustLevel) rank() int {
	switch l {
	case TrustVerified:
		return 2
	case TrustTOFU:
		return 1
	}
	return 0
}

// KeySource describes how a key/address binding was observed.
type KeySource string

const (
	SourceManual    KeySource = "manual"    // imported by the user
	SourceAutocrypt KeySource = "autocrypt" // Autocrypt header of the sender
	SourceGossip    KeySource = "gossip"    // Autocrypt-Gossip header
	SourceAttached  KeySource = "attached"  // application/pgp-keys attachment
	SourceWKD       KeySource = "wkd"       // Web Key Directory
	SourceKeyserver KeySource = "keyserver" // e.g. keys.openpgp.org
	SourceOwn       KeySource = "own"       // one of the user's own keys
)

// TrustRecord is the trust state of one (address, key) pair.
type TrustRecord struct {
	Address     string     `json:"address"`     // normalised (lower-case)
	Fingerprint string     `json:"fingerprint"` // normalised (upper-case hex)
	Level       TrustLevel `json:"level"`       // stored level: unknown, tofu or verified
	Source      KeySource  `json:"source"`      // first source the binding was observed from
	FirstSeen   time.Time  `json:"firstSeen"`
	LastSeen    time.Time  `json:"lastSeen"`
	VerifiedAt  time.Time  `json:"verifiedAt,omitempty"`
	// SeenCount counts observations (messages) of this binding.
	SeenCount int `json:"seenCount"`
}

// Observe records that the binding was seen at time at via source and
// upgrades an unknown binding to TOFU when the source is mail-based.
func (r *TrustRecord) Observe(at time.Time, source KeySource) {
	if r.FirstSeen.IsZero() || at.Before(r.FirstSeen) {
		r.FirstSeen = at
	}
	if at.After(r.LastSeen) {
		r.LastSeen = at
	}
	if r.Source == "" {
		r.Source = source
	}
	r.SeenCount++
	if r.Level == "" {
		r.Level = TrustUnknown
	}
	switch source {
	case SourceAutocrypt, SourceGossip, SourceAttached, SourceWKD:
		if r.Level.rank() < TrustTOFU.rank() {
			r.Level = TrustTOFU
		}
	case SourceOwn:
		r.Level = TrustVerified
		if r.VerifiedAt.IsZero() {
			r.VerifiedAt = at
		}
	}
}

// MarkVerified records that the user confirmed the fingerprint.
func (r *TrustRecord) MarkVerified(at time.Time) {
	r.Level = TrustVerified
	r.VerifiedAt = at
	if r.FirstSeen.IsZero() {
		r.FirstSeen = at
	}
	if at.After(r.LastSeen) {
		r.LastSeen = at
	}
}

// Effective combines the stored level with the key's validity: revoked and
// expired keys override any stored trust. key may be nil.
func (r TrustRecord) Effective(key *Key, now time.Time) TrustLevel {
	if key != nil {
		switch key.Status(now) {
		case StatusRevoked:
			return TrustRevoked
		case StatusExpired:
			return TrustExpired
		}
	}
	if r.Level == "" {
		return TrustUnknown
	}
	return r.Level
}

// TrustStore persists trust records. Get returns ErrKeyNotFound when there
// is no record for the pair.
type TrustStore interface {
	Get(ctx context.Context, address, fingerprint string) (TrustRecord, error)
	Put(ctx context.Context, rec TrustRecord) error
	// ListByAddress returns all records for an address, most recently seen first.
	ListByAddress(ctx context.Context, address string) ([]TrustRecord, error)
	Delete(ctx context.Context, address, fingerprint string) error
}

// MemoryTrustStore is an in-memory TrustStore.
type MemoryTrustStore struct {
	mu   sync.RWMutex
	recs map[[2]string]TrustRecord
}

// NewMemoryTrustStore returns an empty in-memory trust store.
func NewMemoryTrustStore() *MemoryTrustStore {
	return &MemoryTrustStore{recs: map[[2]string]TrustRecord{}}
}

func trustKey(addr, fpr string) [2]string {
	return [2]string{NormalizeEmail(addr), NormalizeFingerprint(fpr)}
}

// Get implements TrustStore.
func (m *MemoryTrustStore) Get(ctx context.Context, address, fingerprint string) (TrustRecord, error) {
	if err := ctx.Err(); err != nil {
		return TrustRecord{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.recs[trustKey(address, fingerprint)]
	if !ok {
		return TrustRecord{}, ErrKeyNotFound
	}
	return r, nil
}

// Put implements TrustStore.
func (m *MemoryTrustStore) Put(ctx context.Context, rec TrustRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rec.Address = NormalizeEmail(rec.Address)
	rec.Fingerprint = NormalizeFingerprint(rec.Fingerprint)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[[2]string{rec.Address, rec.Fingerprint}] = rec
	return nil
}

// ListByAddress implements TrustStore.
func (m *MemoryTrustStore) ListByAddress(ctx context.Context, address string) ([]TrustRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr := NormalizeEmail(address)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []TrustRecord
	for k, r := range m.recs {
		if k[0] == addr {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

// Delete implements TrustStore.
func (m *MemoryTrustStore) Delete(ctx context.Context, address, fingerprint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, trustKey(address, fingerprint))
	return nil
}

// ObserveResult is returned by ObserveKey.
type ObserveResult struct {
	Record TrustRecord
	// New is true when this (address, key) pair was not known before.
	New bool
	// Conflict is true when the address already had a different key with
	// TOFU or verified trust; the UI should warn about a possible key change
	// or MITM attempt.
	Conflict bool
	// ConflictingFingerprints lists the previously trusted keys.
	ConflictingFingerprints []string
}

// ObserveKey records that key fingerprint was seen for address via source
// and reports TOFU conflicts.
func ObserveKey(ctx context.Context, ts TrustStore, address, fingerprint string, source KeySource, at time.Time) (ObserveResult, error) {
	var res ObserveResult
	rec, err := ts.Get(ctx, address, fingerprint)
	switch {
	case errors.Is(err, ErrKeyNotFound):
		res.New = true
		rec = TrustRecord{Address: NormalizeEmail(address), Fingerprint: NormalizeFingerprint(fingerprint)}
	case err != nil:
		return res, err
	}
	if res.New {
		others, err := ts.ListByAddress(ctx, address)
		if err != nil {
			return res, err
		}
		for _, o := range others {
			if o.Fingerprint != rec.Fingerprint && o.Level.rank() >= TrustTOFU.rank() {
				res.Conflict = true
				res.ConflictingFingerprints = append(res.ConflictingFingerprints, o.Fingerprint)
			}
		}
	}
	rec.Observe(at, source)
	if err := ts.Put(ctx, rec); err != nil {
		return res, err
	}
	res.Record = rec
	return res, nil
}

// VerifyKey marks (address, fingerprint) as verified by the user.
func VerifyKey(ctx context.Context, ts TrustStore, address, fingerprint string, at time.Time) (TrustRecord, error) {
	rec, err := ts.Get(ctx, address, fingerprint)
	if errors.Is(err, ErrKeyNotFound) {
		rec = TrustRecord{Address: NormalizeEmail(address), Fingerprint: NormalizeFingerprint(fingerprint), Source: SourceManual}
	} else if err != nil {
		return rec, err
	}
	rec.MarkVerified(at)
	return rec, ts.Put(ctx, rec)
}
