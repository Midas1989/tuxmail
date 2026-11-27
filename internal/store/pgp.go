// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PGPKey is a stored OpenPGP key. Only public key material is kept in the
// database; SecretRef names the private key in the secret store.
type PGPKey struct {
	ID          int64
	Fingerprint string
	Email       string
	UserID      string
	PublicKey   []byte
	Source      string
	SecretRef   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const pgpKeyCols = `id, fingerprint, email, user_id, public_key, source, COALESCE(secret_ref, ''), created_at, updated_at`

func scanPGPKey(sc scanner) (PGPKey, error) {
	var k PGPKey
	var created, updated int64
	err := sc.Scan(&k.ID, &k.Fingerprint, &k.Email, &k.UserID, &k.PublicKey, &k.Source, &k.SecretRef, &created, &updated)
	k.CreatedAt, k.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return k, err
}

// PutPGPKey inserts or updates a key by fingerprint. A secret_ref is kept when
// the new value has none; the source only changes when one is given.
func (s *Store) PutPGPKey(ctx context.Context, k PGPKey) (int64, error) {
	now := time.Now()
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, `INSERT INTO pgp_keys(fingerprint, email, user_id, public_key, source, secret_ref, created_at, updated_at)
		VALUES(?,?,?,?,COALESCE(NULLIF(?, ''), 'manual'),?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET email = excluded.email, user_id = excluded.user_id,
		public_key = excluded.public_key, source = CASE WHEN ? <> '' THEN excluded.source ELSE source END,
		secret_ref = COALESCE(excluded.secret_ref, secret_ref), updated_at = excluded.updated_at RETURNING id`,
		k.Fingerprint, strings.ToLower(k.Email), k.UserID, k.PublicKey, k.Source, nullString(k.SecretRef), now.Unix(), now.Unix(), k.Source).Scan(&id)
	return id, err
}

// ListPGPKeys returns all keys.
func (s *Store) ListPGPKeys(ctx context.Context) ([]PGPKey, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+pgpKeyCols+` FROM pgp_keys ORDER BY secret_ref IS NULL, email, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PGPKey
	for rows.Next() {
		k, err := scanPGPKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetPGPKey returns a key by id.
func (s *Store) GetPGPKey(ctx context.Context, id int64) (PGPKey, error) {
	k, err := scanPGPKey(s.q(ctx).QueryRowContext(ctx, `SELECT `+pgpKeyCols+` FROM pgp_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// GetPGPKeyByFingerprint returns a key by fingerprint.
func (s *Store) GetPGPKeyByFingerprint(ctx context.Context, fingerprint string) (PGPKey, error) {
	k, err := scanPGPKey(s.q(ctx).QueryRowContext(ctx, `SELECT `+pgpKeyCols+` FROM pgp_keys WHERE fingerprint = ?`, fingerprint))
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// DeletePGPKey removes a key and the trust decisions about it.
func (s *Store) DeletePGPKey(ctx context.Context, fingerprint string) error {
	if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM pgp_trust WHERE fingerprint = ?`, fingerprint); err != nil {
		return err
	}
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM pgp_keys WHERE fingerprint = ?`, fingerprint)
	return err
}

// PGPTrust is one row of pgp_trust.
type PGPTrust struct {
	Address     string
	Fingerprint string
	Level       string
	Source      string
	FirstSeen   time.Time
	LastSeen    time.Time
	VerifiedAt  time.Time
	SeenCount   int
}

const pgpTrustCols = `address, fingerprint, level, source, first_seen, last_seen, verified_at, seen_count`

func scanPGPTrust(sc scanner) (PGPTrust, error) {
	var t PGPTrust
	var first, last, verified sql.NullInt64
	err := sc.Scan(&t.Address, &t.Fingerprint, &t.Level, &t.Source, &first, &last, &verified, &t.SeenCount)
	t.FirstSeen, t.LastSeen, t.VerifiedAt = timeFromNull(first), timeFromNull(last), timeFromNull(verified)
	return t, err
}

// GetPGPTrust returns the trust row of (address, fingerprint).
func (s *Store) GetPGPTrust(ctx context.Context, address, fingerprint string) (PGPTrust, error) {
	t, err := scanPGPTrust(s.q(ctx).QueryRowContext(ctx, `SELECT `+pgpTrustCols+` FROM pgp_trust WHERE address = ? AND fingerprint = ?`, address, fingerprint))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// PutPGPTrust stores a trust row.
func (s *Store) PutPGPTrust(ctx context.Context, t PGPTrust) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO pgp_trust(address, fingerprint, level, source, first_seen, last_seen, verified_at, seen_count)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(address, fingerprint) DO UPDATE SET level = excluded.level, source = excluded.source,
		first_seen = excluded.first_seen, last_seen = excluded.last_seen, verified_at = excluded.verified_at, seen_count = excluded.seen_count`,
		t.Address, t.Fingerprint, t.Level, t.Source, unixOrNull(t.FirstSeen), unixOrNull(t.LastSeen), unixOrNull(t.VerifiedAt), t.SeenCount)
	return err
}

// ListPGPTrust returns the trust rows of an address, most recently seen first.
func (s *Store) ListPGPTrust(ctx context.Context, address string) ([]PGPTrust, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+pgpTrustCols+` FROM pgp_trust WHERE address = ? ORDER BY last_seen DESC`, address)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PGPTrust
	for rows.Next() {
		t, err := scanPGPTrust(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeletePGPTrust removes a trust row.
func (s *Store) DeletePGPTrust(ctx context.Context, address, fingerprint string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM pgp_trust WHERE address = ? AND fingerprint = ?`, address, fingerprint)
	return err
}

// PGPPeer is the Autocrypt peer state of an address.
type PGPPeer struct {
	Addr          string
	LastSeen      time.Time
	AutocryptTS   time.Time
	PublicKey     []byte
	PreferEncrypt string
	GossipTS      time.Time
	GossipKey     []byte
	KeyChanged    bool
}

const pgpPeerCols = `addr, last_seen, autocrypt_ts, public_key, prefer_encrypt, gossip_ts, gossip_key, key_changed`

func scanPGPPeer(sc scanner) (PGPPeer, error) {
	var p PGPPeer
	var last, ac, gossip sql.NullInt64
	err := sc.Scan(&p.Addr, &last, &ac, &p.PublicKey, &p.PreferEncrypt, &gossip, &p.GossipKey, &p.KeyChanged)
	p.LastSeen, p.AutocryptTS, p.GossipTS = timeFromNull(last), timeFromNull(ac), timeFromNull(gossip)
	return p, err
}

// GetPGPPeer returns the peer state of an address.
func (s *Store) GetPGPPeer(ctx context.Context, addr string) (PGPPeer, error) {
	p, err := scanPGPPeer(s.q(ctx).QueryRowContext(ctx, `SELECT `+pgpPeerCols+` FROM pgp_peers WHERE addr = ?`, addr))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PutPGPPeer stores the peer state of an address.
func (s *Store) PutPGPPeer(ctx context.Context, p PGPPeer) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO pgp_peers(addr, last_seen, autocrypt_ts, public_key, prefer_encrypt, gossip_ts, gossip_key, key_changed)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(addr) DO UPDATE SET last_seen = excluded.last_seen, autocrypt_ts = excluded.autocrypt_ts,
		public_key = excluded.public_key, prefer_encrypt = excluded.prefer_encrypt, gossip_ts = excluded.gossip_ts,
		gossip_key = excluded.gossip_key, key_changed = excluded.key_changed`,
		p.Addr, unixOrNull(p.LastSeen), unixOrNull(p.AutocryptTS), p.PublicKey, p.PreferEncrypt, unixOrNull(p.GossipTS), p.GossipKey, boolInt(p.KeyChanged))
	return err
}

// ListPGPPeers returns the peers that have a key, sorted by address.
func (s *Store) ListPGPPeers(ctx context.Context) ([]PGPPeer, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+pgpPeerCols+` FROM pgp_peers WHERE public_key IS NOT NULL OR gossip_key IS NOT NULL ORDER BY addr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PGPPeer
	for rows.Next() {
		p, err := scanPGPPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePGPPeer forgets the Autocrypt state of an address.
func (s *Store) DeletePGPPeer(ctx context.Context, addr string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM pgp_peers WHERE addr = ?`, addr)
	return err
}
