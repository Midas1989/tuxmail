// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// KeyLookup discovers public keys for an e-mail address over the network
// (WKD, VKS ...). Implementations return ErrKeyNotFound when nothing was
// found.
type KeyLookup interface {
	LookupByEmail(ctx context.Context, email string) ([]*Key, error)
	// Source identifies the lookup for trust bookkeeping.
	Source() KeySource
}

// RecipientPlan is the encryption decision for a single recipient.
type RecipientPlan struct {
	Address string `json:"address"`
	// Key is the key that would be used for encryption; nil if none.
	Key *Key `json:"-"`
	// Fingerprint of Key ("" if none).
	Fingerprint string `json:"fingerprint,omitempty"`
	// Source tells where Key came from: SourceOwn, SourceManual (key ring),
	// SourceAutocrypt or SourceGossip.
	Source         KeySource      `json:"source,omitempty"`
	Trust          TrustLevel     `json:"trust"`
	Recommendation Recommendation `json:"recommendation"`
	// PendingKey is a key seen in mail for this address that is not used: it
	// differs from the key ring's key, or it replaced an earlier Autocrypt key
	// (KeyChanged) and the user has not accepted that yet.
	PendingKey string `json:"pendingKey,omitempty"`
	KeyChanged bool   `json:"keyChanged,omitempty"`
}

// EncryptionPlan is the result of Planner.Plan.
type EncryptionPlan struct {
	Recipients []RecipientPlan `json:"recipients"`
	// CanEncrypt is true when every recipient has a usable key.
	CanEncrypt bool `json:"canEncrypt"`
	// MissingKeys lists recipients without a usable key.
	MissingKeys []string `json:"missingKeys,omitempty"`
	// Recommendation combines the per-recipient recommendations (Autocrypt
	// semantics: disable, discourage, available, encrypt).
	Recommendation Recommendation `json:"recommendation"`
}

// Keys returns the encryption keys of all recipients that have one.
func (p *EncryptionPlan) Keys() []*Key {
	var out []*Key
	for _, r := range p.Recipients {
		if r.Key != nil {
			out = append(out, r.Key)
		}
	}
	return out
}

// Planner decides for the composer whether a message can and should be
// encrypted. Peers and Trust are optional.
type Planner struct {
	Ring  *KeyRing
	Peers PeerStore
	Trust TrustStore
	// OwnPreferEncrypt is the user's Autocrypt prefer-encrypt setting.
	OwnPreferEncrypt PreferEncrypt
}

// Plan evaluates recipients. replyToEncrypted must be true when composing a
// reply to an encrypted message (Autocrypt then recommends encryption).
func (p *Planner) Plan(ctx context.Context, recipients []string, replyToEncrypted bool, now time.Time) (*EncryptionPlan, error) {
	now = signerTime(now)
	plan := &EncryptionPlan{}
	var recs []Recommendation
	seen := map[string]bool{}
	for _, raw := range recipients {
		addr := NormalizeEmail(raw)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		rp, err := p.planRecipient(ctx, addr, replyToEncrypted, now)
		if err != nil {
			return nil, err
		}
		if rp.Key == nil {
			plan.MissingKeys = append(plan.MissingKeys, addr)
		}
		recs = append(recs, rp.Recommendation)
		plan.Recipients = append(plan.Recipients, rp)
	}
	plan.CanEncrypt = len(plan.Recipients) > 0 && len(plan.MissingKeys) == 0
	plan.Recommendation = CombineRecommendations(recs)
	return plan, nil
}

func (p *Planner) planRecipient(ctx context.Context, addr string, replyToEncrypted bool, now time.Time) (RecipientPlan, error) {
	rp := RecipientPlan{Address: addr, Trust: TrustUnknown, Recommendation: RecDisable}

	// 1. Key ring.
	var ringKey *Key
	ringSource := SourceManual
	if p.Ring != nil {
		keys, err := p.Ring.FindByEmail(ctx, addr)
		if err != nil {
			return rp, err
		}
		for _, k := range keys {
			if k.CanEncrypt(now) {
				ringKey = k
				if k.IsSecret() {
					ringSource = SourceOwn
				}
				break
			}
		}
	}

	// 2. Autocrypt peer state.
	var (
		peer       *PeerState
		peerKey    *Key
		peerGossip bool
	)
	if p.Peers != nil {
		ps, err := p.Peers.Get(ctx, addr)
		switch {
		case err == nil:
			peer = &ps
			if k, g, err := ps.TargetKey(); err == nil && k.CanEncrypt(now) {
				peerKey, peerGossip = k, g
				rp.KeyChanged = ps.KeyChanged
			}
		case !errors.Is(err, ErrKeyNotFound):
			return rp, err
		}
	}

	ringTrust := func() TrustLevel {
		if ringKey == nil {
			return TrustUnknown
		}
		if ringSource == SourceOwn {
			return TrustVerified
		}
		return p.trustOf(ctx, addr, ringKey, now)
	}()

	// 3. Select the key: a key the user put into the ring wins over anything
	// a mail brought along, otherwise a forged header would redirect the
	// encryption. An Autocrypt key that replaced an earlier one waits until
	// the user accepts the change.
	switch {
	case ringKey != nil:
		rp.Key, rp.Source = ringKey, ringSource
		if peerKey != nil && peerKey.Fingerprint() != ringKey.Fingerprint() {
			rp.PendingKey = peerKey.Fingerprint()
		} else {
			rp.KeyChanged = false
		}
	case peerKey != nil && rp.KeyChanged:
		rp.PendingKey = peerKey.Fingerprint()
	case peerKey != nil:
		rp.Key = peerKey
		rp.Source = SourceAutocrypt
		if peerGossip {
			rp.Source = SourceGossip
		}
	}
	if rp.Key == nil {
		return rp, nil
	}
	rp.Fingerprint = rp.Key.Fingerprint()
	if rp.Key == ringKey {
		rp.Trust = ringTrust
	} else {
		rp.Trust = p.trustOf(ctx, addr, rp.Key, now)
		if rp.Trust == TrustUnknown {
			// A key seen via Autocrypt is at least TOFU.
			rp.Trust = TrustTOFU
		}
	}

	// 4. Recommendation.
	pre := RecAvailable
	peerMutual := false
	if peer != nil && rp.Source != SourceManual && rp.Source != SourceOwn {
		pre = peer.Preliminary(now)
		peerMutual = peer.PreferEncrypt == PreferMutual
	} else if peer != nil {
		peerMutual = peer.PreferEncrypt == PreferMutual
	}
	if rp.Trust == TrustVerified && pre.rank() < RecAvailable.rank() {
		pre = RecAvailable
	}
	rp.Recommendation = finalRecommendation(pre, peerMutual, p.OwnPreferEncrypt, replyToEncrypted)
	if rp.Trust == TrustVerified && rp.Source == SourceOwn {
		// Writing to oneself: encryption is always fine.
		rp.Recommendation = finalRecommendation(RecAvailable, true, PreferMutual, replyToEncrypted)
	}
	return rp, nil
}

func (p *Planner) trustOf(ctx context.Context, addr string, k *Key, now time.Time) TrustLevel {
	if p.Trust == nil {
		return (TrustRecord{}).Effective(k, now)
	}
	rec, err := p.Trust.Get(ctx, addr, k.Fingerprint())
	if err != nil {
		return (TrustRecord{}).Effective(k, now)
	}
	return rec.Effective(k, now)
}

// FetchMissing queries the lookups (in order) for every recipient in
// plan.MissingKeys, imports keys that carry the address into the ring and
// records a trust observation. It returns the addresses for which a key was
// found; call Plan again afterwards. Lookup errors other than
// ErrKeyNotFound are collected and returned together with the results.
func (p *Planner) FetchMissing(ctx context.Context, plan *EncryptionPlan, now time.Time, lookups ...KeyLookup) ([]string, error) {
	if p.Ring == nil {
		return nil, errors.New("pgp: planner has no key ring")
	}
	now = signerTime(now)
	var found []string
	var errs []error
	for _, addr := range plan.MissingKeys {
		for _, l := range lookups {
			keys, err := l.LookupByEmail(ctx, addr)
			if err != nil {
				if !errors.Is(err, ErrKeyNotFound) {
					errs = append(errs, fmt.Errorf("%s: %w", addr, err))
				}
				continue
			}
			ok := false
			for _, k := range keys {
				if !k.HasEmail(addr) || !k.CanEncrypt(now) {
					continue
				}
				if _, err := p.Ring.ImportKey(ctx, k.Public()); err != nil {
					return found, err
				}
				if p.Trust != nil {
					if _, err := ObserveKey(ctx, p.Trust, addr, k.Fingerprint(), l.Source(), now); err != nil {
						return found, err
					}
				}
				ok = true
			}
			if ok {
				found = append(found, addr)
				break
			}
		}
	}
	return found, errors.Join(errs...)
}
