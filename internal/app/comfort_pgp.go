// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp/vks"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp/wkd"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/unsubscribe"
)

// OpenPGP key management on internal/crypto/pgp. Keys work like S/MIME
// certificates (comfort_smime.go): public material in the user's database,
// private keys only in the secret store (reference "pgp:<fingerprint>"). A
// passphrase of an imported key is used once to unlock it and is then
// forgotten; the secret store is what protects the key at rest.
//
// Decrypted content is never stored. The search index and snippets are built
// by ParseExtractor without a key ring, the blob store holds the original
// ciphertext, and decrypted bodies only live in the in-memory render caches.
// Unlike S/MIME, the insight analysis (spam, categories, invoices, optional
// AI refinement) does not decrypt OpenPGP mail at all.

// minPGPExportPassphrase follows the password length of spec/PARAMETER.md.
const minPGPExportPassphrase = 12

// pgpSecretRefPrefix is the prefix of private keys in the secret store.
const pgpSecretRefPrefix = "pgp:"

// PGPSettings configure OpenPGP defaults.
type PGPSettings struct {
	// SignByDefault signs every mail of identities with a key.
	SignByDefault bool `json:"signByDefault"`
	// SendAutocrypt adds the Autocrypt header to outgoing mail of identities
	// with a key.
	SendAutocrypt bool `json:"sendAutocrypt"`
	// PreferEncrypt publishes prefer-encrypt=mutual: mails are encrypted
	// automatically when the other side asks for that as well.
	PreferEncrypt bool `json:"preferEncrypt"`
}

// PGPKeyInfo is a key in the key manager.
type PGPKeyInfo struct {
	ID          int64     `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Email       string    `json:"email"`
	Emails      []string  `json:"emails,omitempty"`
	UserID      string    `json:"userId"`
	Algorithm   string    `json:"algorithm"`
	Created     time.Time `json:"created"`
	Expires     string    `json:"expires,omitempty"`
	Status      string    `json:"status"` // valid | expired | revoked | ...
	Own         bool      `json:"own"`    // private key available
	CanEncrypt  bool      `json:"canEncrypt"`
	CanSign     bool      `json:"canSign"`
	Source      string    `json:"source"` // own | manual | wkd | keyserver
	Trust       string    `json:"trust"`  // unknown | tofu | verified | revoked | expired
}

// PGPGenerateInput creates an own key (Curve25519).
type PGPGenerateInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// PGPImportInput imports armored or binary key data.
type PGPImportInput struct {
	Data       string `json:"data"` // base64 file content
	Passphrase string `json:"passphrase,omitempty"`
}

// PGPExportInput protects an exported private key.
type PGPExportInput struct {
	Passphrase string `json:"passphrase"`
}

// PGPLookupInput asks a key directory, only ever on an explicit user request.
type PGPLookupInput struct {
	Email  string `json:"email"`
	Source string `json:"source"` // wkd | keyserver
}

// PGPPeerInfo is a key collected through Autocrypt.
type PGPPeerInfo struct {
	Address       string `json:"address"`
	Fingerprint   string `json:"fingerprint"`
	Gossip        bool   `json:"gossip"` // only known through Autocrypt-Gossip
	PreferEncrypt bool   `json:"preferEncrypt"`
	LastSeen      string `json:"lastSeen,omitempty"`
	KeyChanged    bool   `json:"keyChanged"`
	Trust         string `json:"trust"`
}

// PGPPeerInput names a collected key.
type PGPPeerInput struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// PGPRecipientStatus is the composer's view of one recipient.
type PGPRecipientStatus struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Source      string `json:"source,omitempty"`
	Trust       string `json:"trust"`
}

// PGPStatus tells the composer what OpenPGP can do for a draft.
type PGPStatus struct {
	CanSign        bool                 `json:"canSign"`
	CanEncrypt     bool                 `json:"canEncrypt"`
	Missing        []string             `json:"missing,omitempty"`
	Recipients     []PGPRecipientStatus `json:"recipients,omitempty"`
	Recommendation string               `json:"recommendation,omitempty"`
}

// PGPMessageStatus is the OpenPGP state of a mail in the reading pane. It is
// computed when the mail is shown, so it follows keys and trust decisions.
type PGPMessageStatus struct {
	Present   bool `json:"present"`
	Encrypted bool `json:"encrypted"`
	Decrypted bool `json:"decrypted"`
	Signed    bool `json:"signed"`
	// Signature: valid | invalid | unknown_key | expired_key | revoked_key |
	// mismatch (valid, but the key does not belong to the From address).
	Signature         string `json:"signature,omitempty"`
	SignerFingerprint string `json:"signerFingerprint,omitempty"`
	SignerKeyID       string `json:"signerKeyId,omitempty"`
	SignerTrust       string `json:"signerTrust,omitempty"`
	// ProtectedSubject is the subject from the encrypted part, if it differs
	// from the outer one.
	ProtectedSubject string `json:"protectedSubject,omitempty"`
	// PartiallyProtected: inline PGP with text outside the protected block,
	// which was dropped.
	PartiallyProtected bool `json:"partiallyProtected,omitempty"`
}

// pgpState holds the key ring and the stores of one App.
type pgpState struct {
	keys    *pgpKeyStore
	ring    *pgp.KeyRing
	trust   pgpTrustStore
	peers   pgpPeerStore
	planner *pgp.Planner
	// lookup returns the key directory client; only called on a user request.
	lookup func(source string) pgp.KeyLookup
}

func newPGPState(a *App) *pgpState {
	s := &pgpState{keys: &pgpKeyStore{a: a}, trust: pgpTrustStore{st: a.st}, peers: pgpPeerStore{st: a.st}}
	// Keys are unlocked through the secret store, there is nothing to ask for.
	s.ring = pgp.NewKeyRing(s.keys, nil)
	s.planner = &pgp.Planner{Ring: s.ring, Peers: s.peers, Trust: s.trust}
	s.lookup = pgpDirectory
	return s
}

// pgpDirectory returns a client for a key directory: the address decides which
// host WKD talks to, so requests go through the SSRF-safe client.
func pgpDirectory(source string) pgp.KeyLookup {
	const agent = "TuxMail"
	switch source {
	case string(pgp.SourceWKD):
		return &wkd.Client{HTTPClient: unsubscribe.NewSafeClient(), UserAgent: agent}
	case string(pgp.SourceKeyserver):
		return &vks.Client{HTTPClient: unsubscribe.NewSafeClient(), UserAgent: agent}
	}
	return nil
}

// pgpRing returns the key ring. A secret store that was locked when the ring
// loaded its keys leaves them without private parts: they are read again.
func (a *App) pgpRing() *pgp.KeyRing {
	s := a.comfort.pgp
	if s.keys.incomplete.CompareAndSwap(true, false) {
		s.ring.Reload()
		a.pgpChanged()
	}
	return s.ring
}

// pgpChanged drops everything that was rendered with the old set of keys.
func (a *App) pgpChanged() {
	a.rendered.clear()
	a.bubbles.clear()
}

// ==== stores =====================================================================

// pgpKeyStore implements pgp.KeyStore: public keys in the database, private
// keys in the secret store.
type pgpKeyStore struct {
	a          *App
	incomplete atomic.Bool
}

func (s *pgpKeyStore) stored(k store.PGPKey) pgp.StoredKey {
	out := pgp.StoredKey{Fingerprint: k.Fingerprint, Data: k.PublicKey, Updated: k.UpdatedAt}
	if k.Email != "" {
		out.Emails = []string{k.Email}
	}
	if k.SecretRef == "" {
		return out
	}
	enc, err := s.a.opts.Secrets.Get(k.SecretRef)
	if err == nil {
		var raw []byte
		if raw, err = base64.StdEncoding.DecodeString(string(enc)); err == nil {
			out.Data, out.Secret = raw, true
			return out
		}
	}
	s.incomplete.Store(true)
	s.a.log.Info("comfort: OpenPGP private key unavailable", "key", k.Fingerprint)
	return out
}

// List implements pgp.KeyStore.
func (s *pgpKeyStore) List(ctx context.Context) ([]pgp.StoredKey, error) {
	ks, err := s.a.st.ListPGPKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]pgp.StoredKey, 0, len(ks))
	for _, k := range ks {
		out = append(out, s.stored(k))
	}
	return out, nil
}

// Get implements pgp.KeyStore.
func (s *pgpKeyStore) Get(ctx context.Context, fingerprint string) (pgp.StoredKey, error) {
	k, err := s.a.st.GetPGPKeyByFingerprint(ctx, pgp.NormalizeFingerprint(fingerprint))
	if errors.Is(err, store.ErrNotFound) {
		return pgp.StoredKey{}, pgp.ErrKeyNotFound
	}
	if err != nil {
		return pgp.StoredKey{}, err
	}
	return s.stored(k), nil
}

// Put implements pgp.KeyStore.
func (s *pgpKeyStore) Put(ctx context.Context, sk pgp.StoredKey) error {
	k, err := pgp.ReadKey(sk.Data)
	if err != nil {
		return err
	}
	public, err := k.PublicBytes()
	if err != nil {
		return err
	}
	row := store.PGPKey{Fingerprint: pgp.NormalizeFingerprint(sk.Fingerprint), PublicKey: public, UserID: primaryUserID(k)}
	if em := k.Emails(); len(em) > 0 {
		row.Email = em[0]
	}
	if sk.Secret {
		row.SecretRef = pgpSecretRefPrefix + row.Fingerprint
		row.Source = string(pgp.SourceOwn)
		// Base64: not every secret store backend takes arbitrary bytes.
		if err := s.a.opts.Secrets.Set(row.SecretRef, []byte(base64.StdEncoding.EncodeToString(sk.Data))); err != nil {
			return err
		}
	}
	_, err = s.a.st.PutPGPKey(ctx, row)
	return err
}

// Delete implements pgp.KeyStore.
func (s *pgpKeyStore) Delete(ctx context.Context, fingerprint string) error {
	fpr := pgp.NormalizeFingerprint(fingerprint)
	k, err := s.a.st.GetPGPKeyByFingerprint(ctx, fpr)
	if errors.Is(err, store.ErrNotFound) {
		return pgp.ErrKeyNotFound
	}
	if err != nil {
		return err
	}
	if k.SecretRef != "" {
		// A private key that is already gone must not keep the key alive; a
		// locked or failing store must.
		if err := s.a.opts.Secrets.Delete(k.SecretRef); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return err
		}
	}
	return s.a.st.DeletePGPKey(ctx, fpr)
}

func primaryUserID(k *pgp.Key) string {
	if u := k.UserIDs(); len(u) > 0 {
		return u[0].Raw
	}
	return ""
}

// pgpTrustStore implements pgp.TrustStore.
type pgpTrustStore struct{ st *store.Store }

func pgpTrustRecord(t store.PGPTrust) pgp.TrustRecord {
	return pgp.TrustRecord{Address: t.Address, Fingerprint: t.Fingerprint, Level: pgp.TrustLevel(t.Level), Source: pgp.KeySource(t.Source),
		FirstSeen: t.FirstSeen, LastSeen: t.LastSeen, VerifiedAt: t.VerifiedAt, SeenCount: t.SeenCount}
}

// Get implements pgp.TrustStore.
func (s pgpTrustStore) Get(ctx context.Context, address, fingerprint string) (pgp.TrustRecord, error) {
	t, err := s.st.GetPGPTrust(ctx, pgp.NormalizeEmail(address), pgp.NormalizeFingerprint(fingerprint))
	if errors.Is(err, store.ErrNotFound) {
		return pgp.TrustRecord{}, pgp.ErrKeyNotFound
	}
	if err != nil {
		return pgp.TrustRecord{}, err
	}
	return pgpTrustRecord(t), nil
}

// Put implements pgp.TrustStore.
func (s pgpTrustStore) Put(ctx context.Context, r pgp.TrustRecord) error {
	return s.st.PutPGPTrust(ctx, store.PGPTrust{Address: pgp.NormalizeEmail(r.Address), Fingerprint: pgp.NormalizeFingerprint(r.Fingerprint),
		Level: string(r.Level), Source: string(r.Source), FirstSeen: r.FirstSeen, LastSeen: r.LastSeen, VerifiedAt: r.VerifiedAt, SeenCount: r.SeenCount})
}

// ListByAddress implements pgp.TrustStore.
func (s pgpTrustStore) ListByAddress(ctx context.Context, address string) ([]pgp.TrustRecord, error) {
	ts, err := s.st.ListPGPTrust(ctx, pgp.NormalizeEmail(address))
	if err != nil {
		return nil, err
	}
	out := make([]pgp.TrustRecord, 0, len(ts))
	for _, t := range ts {
		out = append(out, pgpTrustRecord(t))
	}
	return out, nil
}

// Delete implements pgp.TrustStore.
func (s pgpTrustStore) Delete(ctx context.Context, address, fingerprint string) error {
	return s.st.DeletePGPTrust(ctx, pgp.NormalizeEmail(address), pgp.NormalizeFingerprint(fingerprint))
}

// pgpPeerStore implements pgp.PeerStore. A key that replaces a different one
// is remembered, so that the key manager can warn about it.
type pgpPeerStore struct{ st *store.Store }

// Get implements pgp.PeerStore.
func (s pgpPeerStore) Get(ctx context.Context, addr string) (pgp.PeerState, error) {
	p, err := s.st.GetPGPPeer(ctx, pgp.NormalizeEmail(addr))
	if errors.Is(err, store.ErrNotFound) {
		return pgp.PeerState{}, pgp.ErrKeyNotFound
	}
	if err != nil {
		return pgp.PeerState{}, err
	}
	return pgp.PeerState{Addr: p.Addr, LastSeen: p.LastSeen, AutocryptTimestamp: p.AutocryptTS, PublicKey: p.PublicKey,
		PreferEncrypt: pgp.PreferEncrypt(p.PreferEncrypt), GossipTimestamp: p.GossipTS, GossipKey: p.GossipKey}, nil
}

// Put implements pgp.PeerStore.
func (s pgpPeerStore) Put(ctx context.Context, p pgp.PeerState) error {
	row := store.PGPPeer{Addr: pgp.NormalizeEmail(p.Addr), LastSeen: p.LastSeen, AutocryptTS: p.AutocryptTimestamp, PublicKey: p.PublicKey,
		PreferEncrypt: string(p.PreferEncrypt), GossipTS: p.GossipTimestamp, GossipKey: p.GossipKey}
	if prev, err := s.st.GetPGPPeer(ctx, row.Addr); err == nil {
		row.KeyChanged = prev.KeyChanged || (len(prev.PublicKey) > 0 && len(row.PublicKey) > 0 && pgpFingerprintDiffers(prev.PublicKey, row.PublicKey))
	}
	return s.st.PutPGPPeer(ctx, row)
}

// pgpFingerprintDiffers compares keys, not bytes: re-signing is not a change.
func pgpFingerprintDiffers(a, b []byte) bool {
	ka, err := pgp.ReadKey(a)
	if err != nil {
		return true
	}
	kb, err := pgp.ReadKey(b)
	if err != nil {
		return true
	}
	return ka.Fingerprint() != kb.Fingerprint()
}

// ==== key manager ================================================================

func (a *App) pgpTrustOf(ctx context.Context, k *pgp.Key) string {
	if k.IsSecret() {
		return string((pgp.TrustRecord{Level: pgp.TrustVerified}).Effective(k, a.now()))
	}
	s := a.comfort.pgp
	best := pgp.TrustRecord{}
	for _, addr := range k.Emails() {
		if r, err := s.trust.Get(ctx, addr, k.Fingerprint()); err == nil && pgpTrustRank(r.Level) >= pgpTrustRank(best.Level) {
			best = r
		}
	}
	return string(best.Effective(k, a.now()))
}

func pgpTrustRank(l pgp.TrustLevel) int {
	switch l {
	case pgp.TrustVerified:
		return 2
	case pgp.TrustTOFU:
		return 1
	}
	return 0
}

func (a *App) pgpInfo(ctx context.Context, k *pgp.Key, row store.PGPKey) PGPKeyInfo {
	now := a.now()
	in := k.Info(now)
	out := PGPKeyInfo{ID: row.ID, Fingerprint: k.Fingerprint(), Email: row.Email, Emails: k.Emails(), UserID: row.UserID, Algorithm: in.Algorithm,
		Created: k.CreationTime(), Status: string(in.Status), Own: k.IsSecret(), CanEncrypt: k.CanEncrypt(now), CanSign: k.IsSecret() && k.CanSign(now),
		Source: row.Source, Trust: a.pgpTrustOf(ctx, k)}
	if exp := k.Expires(); !exp.IsZero() {
		out.Expires = exp.Format(time.RFC3339)
	}
	return out
}

// ListPGPKeys returns all keys of the key ring.
func (a *App) ListPGPKeys(ctx context.Context) ([]PGPKeyInfo, error) {
	rows, err := a.st.ListPGPKeys(ctx)
	if err != nil {
		return nil, err
	}
	ring := a.pgpRing()
	out := make([]PGPKeyInfo, 0, len(rows))
	for _, row := range rows {
		k, err := ring.Get(ctx, row.Fingerprint)
		if err != nil {
			continue
		}
		out = append(out, a.pgpInfo(ctx, k, row))
	}
	return out, nil
}

func (a *App) pgpKeyByID(ctx context.Context, id int64) (*pgp.Key, store.PGPKey, error) {
	row, err := a.st.GetPGPKey(ctx, id)
	if err != nil {
		return nil, row, err
	}
	k, err := a.pgpRing().Get(ctx, row.Fingerprint)
	if errors.Is(err, pgp.ErrKeyNotFound) {
		return nil, row, errNotFound
	}
	return k, row, err
}

// putPGPKey merges a key into the ring and records where it came from.
func (a *App) putPGPKey(ctx context.Context, k *pgp.Key, source pgp.KeySource) (PGPKeyInfo, error) {
	s := a.comfort.pgp
	if _, err := a.pgpRing().ImportKey(ctx, k); err != nil {
		return PGPKeyInfo{}, err
	}
	now := a.now()
	for _, addr := range k.Emails() {
		if _, err := pgp.ObserveKey(ctx, s.trust, addr, k.Fingerprint(), source, now); err != nil {
			return PGPKeyInfo{}, err
		}
	}
	row, err := a.st.GetPGPKeyByFingerprint(ctx, k.Fingerprint())
	if err != nil {
		return PGPKeyInfo{}, err
	}
	if !k.IsSecret() && row.SecretRef == "" {
		row.Source = string(source)
		if _, err := a.st.PutPGPKey(ctx, row); err != nil {
			return PGPKeyInfo{}, err
		}
	}
	a.pgpChanged()
	stored, err := a.pgpRing().Get(ctx, k.Fingerprint())
	if err != nil {
		return PGPKeyInfo{}, err
	}
	return a.pgpInfo(ctx, stored, row), nil
}

// GeneratePGPKey creates an own Curve25519 key. It carries no passphrase of
// its own: like S/MIME keys it is protected by the secret store.
func (a *App) GeneratePGPKey(ctx context.Context, in PGPGenerateInput) (PGPKeyInfo, error) {
	email := pgp.NormalizeEmail(in.Email)
	if !strings.Contains(email, "@") {
		return PGPKeyInfo{}, invalid("an e-mail address is required")
	}
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: strings.TrimSpace(in.Name), Email: email}}, Algorithm: pgp.AlgoCurve25519, Now: a.now()})
	if err != nil {
		return PGPKeyInfo{}, invalid("the name or address cannot be used for a key")
	}
	return a.putPGPKey(ctx, k, pgp.SourceOwn)
}

// ImportPGPKeys imports public and private keys, armored or binary. Locked
// private keys are unlocked with the passphrase, which is not kept.
func (a *App) ImportPGPKeys(ctx context.Context, in PGPImportInput) ([]PGPKeyInfo, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Data))
	if err != nil {
		return nil, invalid("data must be base64")
	}
	keys, err := pgp.ReadKeys(data)
	if err != nil {
		return nil, &Error{Code: "pgp_import", Status: http.StatusBadRequest, Message: "the file contains no readable OpenPGP key"}
	}
	// Check all before storing any.
	for i, k := range keys {
		if !k.IsSecret() || !k.IsLocked() {
			continue
		}
		if in.Passphrase == "" {
			return nil, &Error{Code: "pgp_passphrase_required", Status: http.StatusBadRequest, Message: "the private key is protected by a passphrase"}
		}
		u, err := k.Unlock([]byte(in.Passphrase))
		if err != nil {
			// Never wrap err: nothing about the attempt belongs in a log.
			return nil, &Error{Code: "pgp_wrong_passphrase", Status: http.StatusBadRequest, Message: "the passphrase does not unlock the private key"}
		}
		keys[i] = u
	}
	out := make([]PGPKeyInfo, 0, len(keys))
	for _, k := range keys {
		source := pgp.SourceManual
		if k.IsSecret() {
			source = pgp.SourceOwn
		}
		info, err := a.putPGPKey(ctx, k, source)
		if err != nil {
			return out, err
		}
		out = append(out, info)
	}
	return out, nil
}

// ExportPGPPublicKey returns the armored public key.
func (a *App) ExportPGPPublicKey(ctx context.Context, id int64) (string, error) {
	k, _, err := a.pgpKeyByID(ctx, id)
	if err != nil {
		return "", err
	}
	return k.ArmoredPublic()
}

// ExportPGPSecretKey returns the armored private key, always protected by the
// given passphrase.
func (a *App) ExportPGPSecretKey(ctx context.Context, id int64, in PGPExportInput) (string, error) {
	if len([]rune(in.Passphrase)) < minPGPExportPassphrase {
		return "", &Error{Code: "pgp_passphrase_short", Status: http.StatusBadRequest, Message: "the passphrase must have at least 12 characters"}
	}
	k, _, err := a.pgpKeyByID(ctx, id)
	if err != nil {
		return "", err
	}
	if !k.IsSecret() {
		return "", &Error{Code: "pgp_no_private_key", Status: http.StatusConflict, Message: "this key has no private part"}
	}
	locked, err := k.ChangePassphrase(nil, []byte(in.Passphrase))
	if err != nil {
		return "", errors.New("app: protecting the exported key failed")
	}
	a.log.Info("comfort: OpenPGP private key exported", "key", k.Fingerprint())
	return locked.ArmoredSecret()
}

// SetPGPKeyVerified records that the user compared the fingerprint (or takes
// that back).
func (a *App) SetPGPKeyVerified(ctx context.Context, id int64, verified bool) error {
	k, _, err := a.pgpKeyByID(ctx, id)
	if err != nil {
		return err
	}
	s := a.comfort.pgp
	now := a.now()
	for _, addr := range k.Emails() {
		if verified {
			if _, err := pgp.VerifyKey(ctx, s.trust, addr, k.Fingerprint(), now); err != nil {
				return err
			}
			continue
		}
		r, err := s.trust.Get(ctx, addr, k.Fingerprint())
		if errors.Is(err, pgp.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		r.Level, r.VerifiedAt = pgp.TrustUnknown, time.Time{}
		if err := s.trust.Put(ctx, r); err != nil {
			return err
		}
	}
	a.pgpChanged()
	return nil
}

// DeletePGPKey removes a key, with its private part and trust decisions.
func (a *App) DeletePGPKey(ctx context.Context, id int64) error {
	row, err := a.st.GetPGPKey(ctx, id)
	if err != nil {
		return err
	}
	if err := a.pgpRing().Delete(ctx, row.Fingerprint); err != nil {
		return err
	}
	a.pgpChanged()
	return nil
}

// LookupPGPKey asks a key directory for the key of an address. This is the
// only way TuxMail talks to one: on a click, never while reading or sending,
// because the request tells the directory (and for WKD the recipient's mail
// domain) who the user is about to write to.
func (a *App) LookupPGPKey(ctx context.Context, in PGPLookupInput) (PGPKeyInfo, error) {
	email := pgp.NormalizeEmail(in.Email)
	if !strings.Contains(email, "@") {
		return PGPKeyInfo{}, invalid("an e-mail address is required")
	}
	l := a.comfort.pgp.lookup(in.Source)
	if l == nil {
		return PGPKeyInfo{}, invalid("unknown key directory")
	}
	keys, err := l.LookupByEmail(ctx, email)
	if errors.Is(err, pgp.ErrKeyNotFound) {
		return PGPKeyInfo{}, &Error{Code: "pgp_key_not_found", Status: http.StatusNotFound, Message: "no key was found for this address"}
	}
	if err != nil {
		return PGPKeyInfo{}, &Error{Code: "pgp_lookup_failed", Status: http.StatusBadGateway, Message: "the key directory could not be reached", Err: err}
	}
	now := a.now()
	for _, k := range keys {
		if k.HasEmail(email) && k.CanEncrypt(now) {
			return a.putPGPKey(ctx, k.Public(), l.Source())
		}
	}
	return PGPKeyInfo{}, &Error{Code: "pgp_key_not_found", Status: http.StatusNotFound, Message: "no usable key was found for this address"}
}

// PGPStatusFor reports what OpenPGP can do for a sender and recipients.
func (a *App) PGPStatusFor(ctx context.Context, from string, recipients []string) (PGPStatus, error) {
	ring := a.pgpRing()
	var st PGPStatus
	now := a.now()
	if keys, err := ring.FindByEmail(ctx, from); err == nil {
		for _, k := range keys {
			if k.IsSecret() && k.CanSign(now) {
				st.CanSign = true
				break
			}
		}
	}
	pl := *a.comfort.pgp.planner
	pl.OwnPreferEncrypt = a.pgpOwnPrefer()
	plan, err := pl.Plan(ctx, recipients, false, now)
	if err != nil {
		return st, err
	}
	st.Missing, st.Recommendation = plan.MissingKeys, string(plan.Recommendation)
	for _, r := range plan.Recipients {
		st.Recipients = append(st.Recipients, PGPRecipientStatus{Address: r.Address, Fingerprint: r.Fingerprint, Source: string(r.Source), Trust: string(r.Trust)})
	}
	// Without an own key the copy in Sent would be unreadable.
	st.CanEncrypt = st.CanSign && plan.CanEncrypt
	return st, nil
}

func (a *App) pgpOwnPrefer() pgp.PreferEncrypt {
	if a.comfortSettings().PGP.PreferEncrypt {
		return pgp.PreferMutual
	}
	return pgp.PreferNoPreference
}

// ==== Autocrypt ==================================================================

// ListPGPPeers returns the keys collected through Autocrypt. They count as
// "seen in mail" (tofu), never as verified.
func (a *App) ListPGPPeers(ctx context.Context) ([]PGPPeerInfo, error) {
	rows, err := a.st.ListPGPPeers(ctx)
	if err != nil {
		return nil, err
	}
	s := a.comfort.pgp
	now := a.now()
	out := make([]PGPPeerInfo, 0, len(rows))
	for _, row := range rows {
		ps, err := s.peers.Get(ctx, row.Addr)
		if err != nil {
			return nil, err
		}
		k, gossip, err := ps.TargetKey()
		if err != nil || k == nil {
			continue
		}
		trust := pgp.TrustTOFU
		if r, err := s.trust.Get(ctx, row.Addr, k.Fingerprint()); err == nil && pgpTrustRank(r.Level) > pgpTrustRank(trust) {
			trust = r.Level
		}
		info := PGPPeerInfo{Address: row.Addr, Fingerprint: k.Fingerprint(), Gossip: gossip, PreferEncrypt: ps.PreferEncrypt == pgp.PreferMutual,
			KeyChanged: row.KeyChanged, Trust: string((pgp.TrustRecord{Level: trust}).Effective(k, now))}
		if !row.LastSeen.IsZero() {
			info.LastSeen = row.LastSeen.Format(time.RFC3339)
		}
		out = append(out, info)
	}
	return out, nil
}

// VerifyPGPPeer moves a collected key into the key ring after the user
// compared its fingerprint. The fingerprint must still be the one the user saw.
func (a *App) VerifyPGPPeer(ctx context.Context, in PGPPeerInput) (PGPKeyInfo, error) {
	s := a.comfort.pgp
	addr := pgp.NormalizeEmail(in.Address)
	ps, err := s.peers.Get(ctx, addr)
	if errors.Is(err, pgp.ErrKeyNotFound) {
		return PGPKeyInfo{}, errNotFound
	}
	if err != nil {
		return PGPKeyInfo{}, err
	}
	k, _, err := ps.TargetKey()
	if err != nil || k == nil {
		return PGPKeyInfo{}, errNotFound
	}
	if pgp.NormalizeFingerprint(in.Fingerprint) != k.Fingerprint() {
		return PGPKeyInfo{}, &Error{Code: "pgp_key_changed", Status: http.StatusConflict, Message: "the key has changed in the meantime"}
	}
	info, err := a.putPGPKey(ctx, k.Public(), pgp.SourceAutocrypt)
	if err != nil {
		return info, err
	}
	if _, err := pgp.VerifyKey(ctx, s.trust, addr, k.Fingerprint(), a.now()); err != nil {
		return info, err
	}
	if row, err := a.st.GetPGPPeer(ctx, addr); err == nil && row.KeyChanged {
		row.KeyChanged = false
		if err := a.st.PutPGPPeer(ctx, row); err != nil {
			return info, err
		}
	}
	a.pgpChanged()
	info.Trust = string(pgp.TrustVerified)
	return info, nil
}

// DeletePGPPeer forgets a collected key.
func (a *App) DeletePGPPeer(ctx context.Context, address string) error {
	err := a.st.DeletePGPPeer(ctx, pgp.NormalizeEmail(address))
	a.pgpChanged()
	return err
}

// collectAutocrypt applies the Autocrypt update rules to an incoming mail.
// Mail from the user's own addresses and mail without a header from a sender
// that is not known yet leave no trace.
func (a *App) collectAutocrypt(ctx context.Context, pm *parse.Message) {
	if len(pm.Header.From) != 1 {
		return
	}
	from := pgp.NormalizeEmail(pm.Header.From[0].Email)
	if from == "" || a.isOwnAddress(ctx, from) {
		return
	}
	s := a.comfort.pgp
	headers := pm.Headers.Values(pgp.AutocryptHeaderName)
	if len(headers) == 0 {
		if _, err := s.peers.Get(ctx, from); err != nil {
			return
		}
	}
	_, err := pgp.ProcessIncoming(ctx, s.peers, pgp.IncomingMessage{From: []string{from}, Date: pm.Header.Date,
		ContentType: pm.Headers.Get("Content-Type"), AutocryptHeaders: headers}, a.now())
	if err != nil {
		a.log.Info("comfort: storing Autocrypt state failed", "err", err)
	}
}

// isOwnAddress reports whether the address belongs to one of the identities.
func (a *App) isOwnAddress(ctx context.Context, email string) bool {
	ids, err := a.ListIdentities(ctx)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if strings.EqualFold(id.Email, email) {
			return true
		}
	}
	return false
}

// ==== reading ====================================================================

// pgpSenderKeys adds the Autocrypt key of the sender for verifying signatures.
// Nothing is fetched from the network here.
func (a *App) pgpSenderKeys(ctx context.Context, from string) []*pgp.Key {
	ps, err := a.comfort.pgp.peers.Get(ctx, from)
	if err != nil || len(ps.PublicKey) == 0 {
		return nil
	}
	k, err := pgp.ReadKey(ps.PublicKey)
	if err != nil {
		return nil
	}
	return []*pgp.Key{k}
}

// messagePGPStatus computes the OpenPGP state of a stored mail without
// downloading anything. Autocrypt-Gossip of a decrypted mail is collected here.
func (a *App) messagePGPStatus(ctx context.Context, m store.Message) *PGPMessageStatus {
	if m.BlobHash == "" {
		return nil
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return nil
	}
	pm, err := parse.ParseContext(ctx, raw, parse.Options{SkipAttachmentData: true, KeyRing: a.pgpRing(), PGPSenderKeys: a.pgpSenderKeys})
	if err != nil || pm.PGP.Format == pgp.FormatNone || pm.PGP.Format == "" {
		return nil
	}
	info := pm.PGP
	out := &PGPMessageStatus{Present: true, Encrypted: info.Encrypted, Decrypted: info.Decrypted, Signed: info.Signed, Signature: string(info.Signature),
		SignerFingerprint: info.SignerFingerprint, SignerKeyID: info.SignerKeyID, PartiallyProtected: info.PartiallyProtected}
	if !info.Signed {
		out.Signature = ""
	}
	if info.ProtectedHeaders && pm.Header.Subject != m.Subject {
		out.ProtectedSubject = pm.Header.Subject
	}
	if info.Signature == pgp.SigValid || info.Signature == pgp.SigExpiredKey || info.Signature == pgp.SigRevokedKey {
		a.pgpJudgeSigner(ctx, pm, out)
	}
	if info.Decrypted && len(pm.AutocryptGossip) > 0 && len(pm.Header.From) == 1 {
		var rcpts []string
		for _, ad := range append(append([]domain.Address{}, pm.Header.To...), pm.Header.Cc...) {
			rcpts = append(rcpts, ad.Email)
		}
		// From stays empty: the outer header was handled when the mail came in.
		if _, err := pgp.ProcessIncoming(ctx, a.comfort.pgp.peers, pgp.IncomingMessage{Date: pm.Header.Date,
			ContentType: pm.Headers.Get("Content-Type"), GossipHeaders: pm.AutocryptGossip, Recipients: rcpts}, a.now()); err != nil {
			a.log.Info("comfort: storing Autocrypt gossip failed", "err", err)
		}
	}
	return out
}

// pgpJudgeSigner ties a signature to the sender: a valid signature by a key
// that does not carry the From address says nothing about the sender.
func (a *App) pgpJudgeSigner(ctx context.Context, pm *parse.Message, out *PGPMessageStatus) {
	from := fromEmail(pm)
	k, err := a.pgpRing().Get(ctx, out.SignerFingerprint)
	if err != nil {
		for _, c := range a.pgpSenderKeys(ctx, from) {
			if c.Fingerprint() == pgp.NormalizeFingerprint(out.SignerFingerprint) {
				k = c
			}
		}
	}
	if k == nil {
		out.Signature = string(pgp.SigUnknownKey)
		return
	}
	if from == "" || !k.HasEmail(from) {
		if out.Signature == string(pgp.SigValid) {
			out.Signature = "mismatch"
		}
		return
	}
	if k.IsSecret() {
		out.SignerTrust = string(pgp.TrustVerified)
		return
	}
	trust := pgp.TrustTOFU
	if r, err := a.comfort.pgp.trust.Get(ctx, from, k.Fingerprint()); err == nil {
		trust = r.Level
	} else if _, ringErr := a.pgpRing().Get(ctx, k.Fingerprint()); ringErr == nil {
		trust = pgp.TrustUnknown
	}
	out.SignerTrust = string(trust)
}
