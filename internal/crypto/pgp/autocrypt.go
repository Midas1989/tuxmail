// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// Autocrypt header names.
const (
	AutocryptHeaderName       = "Autocrypt"
	AutocryptGossipHeaderName = "Autocrypt-Gossip"
)

// PreferEncrypt is the Autocrypt prefer-encrypt setting.
type PreferEncrypt string

const (
	PreferNoPreference PreferEncrypt = "nopreference"
	PreferMutual       PreferEncrypt = "mutual"
)

// AutocryptHeader is a parsed Autocrypt or Autocrypt-Gossip header.
type AutocryptHeader struct {
	Addr          string // as given (compare with NormalizeEmail)
	PreferEncrypt PreferEncrypt
	KeyData       []byte // binary OpenPGP public key
}

// NewAutocryptHeader builds the Autocrypt header for addr from key. The key
// data is minimised (see Key.MinimalPublicKey). prefer is the user's
// prefer-encrypt setting.
func NewAutocryptHeader(addr string, key *Key, prefer PreferEncrypt, now time.Time) (*AutocryptHeader, error) {
	data, err := key.MinimalPublicKey(addr, now)
	if err != nil {
		return nil, err
	}
	if prefer == "" {
		prefer = PreferNoPreference
	}
	return &AutocryptHeader{Addr: addr, PreferEncrypt: prefer, KeyData: data}, nil
}

// Value returns the header value, folded with CRLF+space so that it can be
// written directly after "Autocrypt: ". Each line stays below 78 characters.
func (h *AutocryptHeader) Value() string { return h.value(true) }

// GossipValue returns the value for an Autocrypt-Gossip header (without
// prefer-encrypt, as recommended by the specification).
func (h *AutocryptHeader) GossipValue() string { return h.value(false) }

func (h *AutocryptHeader) value(withPrefer bool) string {
	var b strings.Builder
	b.WriteString("addr=")
	b.WriteString(h.Addr)
	b.WriteString(";")
	if withPrefer && h.PreferEncrypt == PreferMutual {
		b.WriteString(" prefer-encrypt=mutual;")
	}
	b.WriteString(" keydata=")
	enc := base64.StdEncoding.EncodeToString(h.KeyData)
	const lineLen = 76
	for len(enc) > 0 {
		n := min(lineLen, len(enc))
		b.WriteString("\r\n ")
		b.WriteString(enc[:n])
		enc = enc[n:]
	}
	return b.String()
}

// Field returns the Autocrypt header field.
func (h *AutocryptHeader) Field() HeaderField {
	return HeaderField{Name: AutocryptHeaderName, Value: h.Value()}
}

// GossipField returns the Autocrypt-Gossip header field.
func (h *AutocryptHeader) GossipField() HeaderField {
	return HeaderField{Name: AutocryptGossipHeaderName, Value: h.GossipValue()}
}

// Key parses the key data.
func (h *AutocryptHeader) Key() (*Key, error) { return ReadKey(h.KeyData) }

// ParseAutocryptHeader parses an Autocrypt or Autocrypt-Gossip header value
// (folded or unfolded). Unknown critical attributes (not starting with "_")
// make the header invalid, as required by the specification.
func ParseAutocryptHeader(value string) (*AutocryptHeader, error) {
	h := &AutocryptHeader{PreferEncrypt: PreferNoPreference}
	var keydata string
	seen := map[string]bool{}
	for _, attr := range strings.Split(value, ";") {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		i := strings.IndexByte(attr, '=')
		if i <= 0 {
			return nil, fmt.Errorf("%w: malformed attribute %q", ErrInvalidAutocrypt, truncate(attr, 20))
		}
		name := strings.ToLower(strings.TrimSpace(attr[:i]))
		val := strings.TrimSpace(attr[i+1:])
		if seen[name] {
			return nil, fmt.Errorf("%w: duplicate attribute %q", ErrInvalidAutocrypt, name)
		}
		seen[name] = true
		switch name {
		case "addr":
			h.Addr = val
		case "prefer-encrypt":
			if strings.EqualFold(val, string(PreferMutual)) {
				h.PreferEncrypt = PreferMutual
			}
		case "keydata":
			keydata = val
		default:
			if !strings.HasPrefix(name, "_") {
				return nil, fmt.Errorf("%w: unknown critical attribute %q", ErrInvalidAutocrypt, name)
			}
		}
	}
	if h.Addr == "" || keydata == "" {
		return nil, fmt.Errorf("%w: addr and keydata are required", ErrInvalidAutocrypt)
	}
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, keydata)
	data, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: keydata: %v", ErrInvalidAutocrypt, err)
	}
	h.KeyData = data
	return h, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// MinimalPublicKey returns a minimal binary public key for Autocrypt: the
// primary key (with its direct-key self-signature), the user ID matching
// addr (or the primary user ID) with its latest self-signature and the
// current encryption subkey with its latest binding signature.
func (k *Key) MinimalPublicKey(addr string, now time.Time) ([]byte, error) {
	now = signerTime(now)
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.entity
	encKey, ok := e.EncryptionKey(now, nil)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoEncryptionKey, k.Fingerprint())
	}
	var buf bytes.Buffer
	if err := e.PrimaryKey.Serialize(&buf); err != nil {
		return nil, err
	}
	if sig, err := e.LatestValidDirectSignature(now, nil); err == nil && sig != nil {
		if err := sig.Serialize(&buf); err != nil {
			return nil, err
		}
	}
	// Pick the user ID: matching addr first, otherwise the primary one.
	var ident *openpgp.Identity
	want := NormalizeEmail(addr)
	for _, id := range e.Identities {
		if _, err := id.Verify(now, nil); err != nil {
			continue
		}
		email := ""
		if id.UserId != nil {
			email = id.UserId.Email
		}
		if email == "" {
			email = id.Name
		}
		if NormalizeEmail(email) == want {
			ident = id
			break
		}
	}
	if ident == nil {
		_, ident = e.PrimaryIdentity(now, nil)
	}
	if ident != nil {
		sig, err := ident.LatestValidSelfCertification(now, nil)
		if err != nil {
			return nil, err
		}
		if err := ident.UserId.Serialize(&buf); err != nil {
			return nil, err
		}
		if err := sig.Serialize(&buf); err != nil {
			return nil, err
		}
	} else if e.PrimaryKey.Version != 6 {
		return nil, errors.New("pgp: key has no valid user ID")
	}
	if encKey.PublicKey != e.PrimaryKey {
		if err := encKey.PublicKey.Serialize(&buf); err != nil {
			return nil, err
		}
		if err := encKey.SelfSignature.Serialize(&buf); err != nil {
			return nil, err
		}
	}
	// Sanity check: the result must parse and be usable for encryption.
	mk, err := ReadKey(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("pgp: minimal key: %w", err)
	}
	if !mk.CanEncrypt(now) {
		return nil, fmt.Errorf("pgp: minimal key: %w", ErrNoEncryptionKey)
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// Peer state (Autocrypt Level 1, section 2.3)

// PeerState is the Autocrypt state kept per peer address.
type PeerState struct {
	Addr               string        `json:"addr"`
	LastSeen           time.Time     `json:"lastSeen"`
	AutocryptTimestamp time.Time     `json:"autocryptTimestamp"`
	PublicKey          []byte        `json:"publicKey,omitempty"`
	PreferEncrypt      PreferEncrypt `json:"preferEncrypt"`
	GossipTimestamp    time.Time     `json:"gossipTimestamp"`
	GossipKey          []byte        `json:"gossipKey,omitempty"`
	// KeyChanged: the key to encrypt to was replaced by a different one and
	// the user has not accepted that yet.
	KeyChanged bool `json:"keyChanged,omitempty"`
}

// targetFingerprint is the fingerprint of TargetKey ("" if there is none).
func (p *PeerState) targetFingerprint() string {
	if k, _, err := p.TargetKey(); err == nil && k != nil {
		return k.Fingerprint()
	}
	return ""
}

func (p *PeerState) noteChange(before string) {
	if after := p.targetFingerprint(); before != "" && after != before {
		p.KeyChanged = true
	}
}

// EffectiveDate returns the effective message date as defined by Autocrypt:
// the Date header, but never later than now.
func EffectiveDate(date, now time.Time) time.Time {
	if date.IsZero() || date.After(now) {
		return now
	}
	return date
}

// UpdateFromHeader applies the Autocrypt update rules for a message from
// this peer with effective date date. header is the single valid Autocrypt
// header of the message for the From address, or nil if there was none.
// It reports whether the state changed.
func (p *PeerState) UpdateFromHeader(header *AutocryptHeader, date time.Time) bool {
	if date.Before(p.AutocryptTimestamp) {
		return false
	}
	changed := false
	if date.After(p.LastSeen) {
		p.LastSeen = date
		changed = true
	}
	if header == nil {
		return changed
	}
	defer p.noteChange(p.targetFingerprint())
	p.AutocryptTimestamp = date
	p.PublicKey = append([]byte(nil), header.KeyData...)
	p.PreferEncrypt = header.PreferEncrypt
	if p.PreferEncrypt == "" {
		p.PreferEncrypt = PreferNoPreference
	}
	return true
}

// UpdateFromGossip applies an Autocrypt-Gossip header with effective date
// date. It reports whether the state changed.
func (p *PeerState) UpdateFromGossip(header *AutocryptHeader, date time.Time) bool {
	if header == nil || date.Before(p.GossipTimestamp) {
		return false
	}
	defer p.noteChange(p.targetFingerprint())
	p.GossipTimestamp = date
	p.GossipKey = append([]byte(nil), header.KeyData...)
	return true
}

// Recommendation is the Autocrypt UI recommendation.
type Recommendation string

const (
	RecDisable    Recommendation = "disable"
	RecDiscourage Recommendation = "discourage"
	RecAvailable  Recommendation = "available"
	RecEncrypt    Recommendation = "encrypt"
)

func (r Recommendation) rank() int {
	switch r {
	case RecEncrypt:
		return 3
	case RecAvailable:
		return 2
	case RecDiscourage:
		return 1
	}
	return 0
}

// TargetKey returns the key Autocrypt would encrypt to (public_key, or
// gossip_key if there is none) and whether it came from gossip.
func (p *PeerState) TargetKey() (*Key, bool, error) {
	if len(p.PublicKey) > 0 {
		k, err := ReadKey(p.PublicKey)
		return k, false, err
	}
	if len(p.GossipKey) > 0 {
		k, err := ReadKey(p.GossipKey)
		return k, true, err
	}
	return nil, false, ErrKeyNotFound
}

// Preliminary returns the preliminary recommendation (Autocrypt Level 1,
// section 2.4.1).
func (p *PeerState) Preliminary(now time.Time) Recommendation {
	if p == nil {
		return RecDisable
	}
	k, gossipOnly, err := p.TargetKey()
	if err != nil || k == nil || !k.CanEncrypt(now) {
		return RecDisable
	}
	if gossipOnly {
		return RecDiscourage
	}
	if p.LastSeen.Sub(p.AutocryptTimestamp) > 35*24*time.Hour {
		return RecDiscourage
	}
	return RecAvailable
}

// Recommend returns the final recommendation for this peer (section 2.4.2).
// ownPrefer is the user's own prefer-encrypt setting; replyToEncrypted is
// true when composing a reply to an encrypted message.
func (p *PeerState) Recommend(now time.Time, ownPrefer PreferEncrypt, replyToEncrypted bool) Recommendation {
	pre := p.Preliminary(now)
	return finalRecommendation(pre, p != nil && p.PreferEncrypt == PreferMutual, ownPrefer, replyToEncrypted)
}

func finalRecommendation(pre Recommendation, peerMutual bool, ownPrefer PreferEncrypt, replyToEncrypted bool) Recommendation {
	if (pre == RecAvailable || pre == RecDiscourage) && replyToEncrypted {
		return RecEncrypt
	}
	if pre == RecAvailable && peerMutual && ownPrefer == PreferMutual {
		return RecEncrypt
	}
	return pre
}

// CombineRecommendations combines per-recipient recommendations into the
// recommendation for the whole message: disable if any is disable,
// discourage if any is discourage, encrypt if all are encrypt, otherwise
// available. An empty list yields disable.
func CombineRecommendations(recs []Recommendation) Recommendation {
	if len(recs) == 0 {
		return RecDisable
	}
	worst := RecEncrypt
	for _, r := range recs {
		if r.rank() < worst.rank() {
			worst = r
		}
	}
	return worst
}

// PeerStore persists Autocrypt peer states by normalised address. Get
// returns ErrKeyNotFound for unknown peers.
type PeerStore interface {
	Get(ctx context.Context, addr string) (PeerState, error)
	Put(ctx context.Context, p PeerState) error
}

// MemoryPeerStore is an in-memory PeerStore.
type MemoryPeerStore struct {
	mu    sync.RWMutex
	peers map[string]PeerState
}

// NewMemoryPeerStore returns an empty in-memory peer store.
func NewMemoryPeerStore() *MemoryPeerStore {
	return &MemoryPeerStore{peers: map[string]PeerState{}}
}

// Get implements PeerStore.
func (m *MemoryPeerStore) Get(ctx context.Context, addr string) (PeerState, error) {
	if err := ctx.Err(); err != nil {
		return PeerState{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.peers[NormalizeEmail(addr)]
	if !ok {
		return PeerState{}, ErrKeyNotFound
	}
	return p, nil
}

// Put implements PeerStore.
func (m *MemoryPeerStore) Put(ctx context.Context, p PeerState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.Addr = NormalizeEmail(p.Addr)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[p.Addr] = p
	return nil
}

// IncomingMessage carries what ProcessIncoming needs from a received message.
type IncomingMessage struct {
	// From holds the addresses of the From header. Autocrypt headers are
	// only processed if there is exactly one.
	From []string
	// Date is the Date header (zero if missing or unparsable).
	Date time.Time
	// ContentType of the outer message; multipart/report messages are
	// ignored as required by the specification.
	ContentType string
	// AutocryptHeaders are the raw values of all Autocrypt headers of the
	// outer message.
	AutocryptHeaders []string
	// GossipHeaders are the raw values of the Autocrypt-Gossip headers of
	// the decrypted inner part (only pass them for successfully decrypted
	// messages).
	GossipHeaders []string
	// Recipients (To and Cc). Gossip for other addresses is ignored.
	Recipients []string
}

// PeerUpdate describes a peer whose state changed.
type PeerUpdate struct {
	State      PeerState
	FromGossip bool
	// KeyChanged is true if the peer's public (or gossip) key changed.
	KeyChanged bool
}

// ProcessIncoming applies the Autocrypt update rules for a received message
// and stores changed peer states.
func ProcessIncoming(ctx context.Context, store PeerStore, msg IncomingMessage, now time.Time) ([]PeerUpdate, error) {
	now = signerTime(now)
	mt, _ := mediaType(msg.ContentType)
	if mt == "multipart/report" {
		return nil, nil
	}
	date := EffectiveDate(msg.Date, now)
	var updates []PeerUpdate

	load := func(addr string) (PeerState, error) {
		p, err := store.Get(ctx, addr)
		if errors.Is(err, ErrKeyNotFound) {
			return PeerState{Addr: NormalizeEmail(addr), PreferEncrypt: PreferNoPreference}, nil
		}
		return p, err
	}

	if len(msg.From) == 1 {
		from := NormalizeEmail(msg.From[0])
		var valid []*AutocryptHeader
		for _, v := range msg.AutocryptHeaders {
			h, err := ParseAutocryptHeader(v)
			if err != nil || NormalizeEmail(h.Addr) != from {
				continue
			}
			if _, err := h.Key(); err != nil {
				continue
			}
			valid = append(valid, h)
		}
		var header *AutocryptHeader
		if len(valid) == 1 {
			header = valid[0]
		}
		p, err := load(from)
		if err != nil {
			return nil, err
		}
		oldKey := p.PublicKey
		if p.UpdateFromHeader(header, date) {
			if err := store.Put(ctx, p); err != nil {
				return nil, err
			}
			updates = append(updates, PeerUpdate{State: p, KeyChanged: !bytes.Equal(oldKey, p.PublicKey)})
		}
	}

	recips := map[string]bool{}
	for _, r := range msg.Recipients {
		recips[NormalizeEmail(r)] = true
	}
	for _, v := range msg.GossipHeaders {
		h, err := ParseAutocryptHeader(v)
		if err != nil {
			continue
		}
		addr := NormalizeEmail(h.Addr)
		if !recips[addr] {
			continue
		}
		if _, err := h.Key(); err != nil {
			continue
		}
		p, err := load(addr)
		if err != nil {
			return nil, err
		}
		oldKey := p.GossipKey
		if p.UpdateFromGossip(h, date) {
			if err := store.Put(ctx, p); err != nil {
				return nil, err
			}
			updates = append(updates, PeerUpdate{State: p, FromGossip: true, KeyChanged: !bytes.Equal(oldKey, p.GossipKey)})
		}
	}
	return updates, nil
}
