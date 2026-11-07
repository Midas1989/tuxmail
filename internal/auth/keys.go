// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/secretbox"
)

// Secrets at rest and their rotation (Basis spec/KRYPTO.md, ADR 0004).
//
// data_dir/secretbox.json is the key ring. New values are written in the format
// bx1.<kid>.<...> under its primary key. Three kinds of values exist:
//
//	users.dek_wrapped, users.totp_secret_enc   one per user
//	recovery_codes.code_hash                   codes from before the HMAC (recovery.go)
//	server_keys.key_enc                        team vault passwords, known-network key,
//	                                           recovery pepper, audit chain key
//
// data_dir/master.key is the key of the format before bx1 (raw
// XChaCha20-Poly1305, associated data "dek:<id>" / "totp:<id>"). It stays
// readable until every value is converted and `tuxmail keys retire-legacy` has
// moved the file away. A value without the bx1 prefix is only ever opened with
// that authenticated cipher, never passed through as plain text.
//
// Rotation without data loss:
//
//	tuxmail keys rotate         new primary key; old keys keep decrypting
//	tuxmail keys rewrap         converts everything not under the primary key
//	tuxmail keys status         counts values per key
//	tuxmail keys retire --kid K removes a key once no value uses it
//
// Rewrapping converts one row per transaction with a compare-and-swap, so it can
// be interrupted at any point, repeated, and run next to a live server. A running
// server notices a changed key file (ring).
const (
	keyringFile = "secretbox.json"
	masterFile  = "master.key"

	networkKeyName = "known_networks"
	serverKeyBytes = 32
	rewrapBatch    = 200
)

// secretColumn is an encrypted column.
type secretColumn struct {
	table, column string
	where         string // restricts the rows; "" = all
	context       func(id int64) string
	legacyTag     string // "" = never existed in the old format
}

func (c secretColumn) legacy(id int64) string {
	if c.legacyTag == "" {
		return ""
	}
	return c.legacyTag + ":" + strconv.FormatInt(id, 10)
}

var (
	secretDEK = secretColumnFor("dek_wrapped", "dek")
	// Context as in Basis spec/PARAMETER.md.
	secretTOTP = secretColumnFor("totp_secret_enc", "totp")
	// The SHA-256 of a recovery code from before the HMAC, sealed (recovery.go).
	secretRecovery = secretColumn{table: "recovery_codes", column: "code_hash", where: "scheme = 'enc-sha256'",
		context: func(id int64) string { return "recovery_codes.code_hash:" + strconv.FormatInt(id, 10) }}
	secretColumns = []secretColumn{secretDEK, secretTOTP, secretRecovery}
)

func secretColumnFor(column, legacyTag string) secretColumn {
	return secretColumn{table: "users", column: column, legacyTag: legacyTag, context: func(id int64) string {
		return "users." + column + ":" + strconv.FormatInt(id, 10)
	}}
}

func (c secretColumn) rowFilter() string {
	f := c.column + ` IS NOT NULL AND length(` + c.column + `) > 0`
	if c.where != "" {
		f += ` AND ` + c.where
	}
	return f
}

func serverKeyContext(name string) string { return "server_keys.key_enc:" + name }

func teamKeyName(teamID int64) string { return "team_vault:" + strconv.FormatInt(teamID, 10) }

// keyState is the mutable key material of a Service.
type keyState struct {
	mu     sync.Mutex
	master []byte // nil once master.key was retired
	ring   *secretbox.KeyRing
	mtime  time.Time // of the loaded key file
	size   int64
	netKey []byte

	derived map[string][]byte // recovery pepper, audit chain key

	rewrapHook func(id string) // tests: runs before each converted value
}

func (s *Service) ringPath() string   { return filepath.Join(s.dataDir, keyringFile) }
func (s *Service) masterPath() string { return filepath.Join(s.dataDir, masterFile) }

func (s *Service) legacyMaster() []byte {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	return s.keys.master
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// loadKeyFiles reads what exists. A completely new data directory also gets a
// master.key (a fresh install stays able to use `keys rollback`); with a key ring
// present a missing master.key means it was retired and is not created again.
func loadKeyFiles(dataDir string) (master []byte, ring *secretbox.KeyRing, err error) {
	ringPath, masterPath := filepath.Join(dataDir, keyringFile), filepath.Join(dataDir, masterFile)
	haveRing, err := fileExists(ringPath)
	if err != nil {
		return nil, nil, err
	}
	if haveRing {
		if ring, err = secretbox.Load(ringPath); err != nil {
			return nil, nil, fmt.Errorf("auth: %w", err)
		}
	}
	haveMaster, err := fileExists(masterPath)
	if err != nil {
		return nil, nil, err
	}
	if haveMaster || !haveRing {
		if master, err = LoadOrCreateMasterKey(masterPath); err != nil {
			return nil, nil, err
		}
	}
	return master, ring, nil
}

// initKeys creates the key ring if necessary and refuses to run with key files
// that cannot match the database: better a clear error at start than a server
// that silently cannot read (or newly encrypts past) what is stored.
func (s *Service) initKeys(ctx context.Context, master []byte, ring *secretbox.KeyRing) error {
	st, err := s.countSecrets(ctx, ring)
	if err != nil {
		return err
	}
	if ring == nil {
		if st.bx1 > 0 {
			return fmt.Errorf("auth: %s is missing but the database holds %d values encrypted with it; restore the file from its backup", s.ringPath(), st.bx1)
		}
		if ring, err = secretbox.Generate(); err != nil {
			return err
		}
		if err := ring.SaveNew(s.ringPath()); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("auth: create key ring: %w", err)
			}
			// Another process was faster.
			if ring, err = secretbox.Load(s.ringPath()); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}
	}
	if st.legacy > 0 && master == nil {
		return fmt.Errorf("auth: %s is missing but the database holds %d values encrypted with it; restore the file from its backup", s.masterPath(), st.legacy)
	}
	s.keys.master = master
	s.keys.ring = ring
	if fi, err := os.Stat(s.ringPath()); err == nil {
		s.keys.mtime, s.keys.size = fi.ModTime(), fi.Size()
	}
	// Data that needs a key to reach its current form (idempotent).
	if err := s.sealLegacyRecoveryCodes(ctx); err != nil {
		return fmt.Errorf("auth: recovery codes: %w", err)
	}
	if err := s.sealAuditBacklog(ctx); err != nil {
		return fmt.Errorf("auth: audit chain: %w", err)
	}
	return nil
}

// currentRing returns the key ring, reloading the file if another process (the
// CLI) changed it. force reloads in any case.
func (s *Service) currentRing(force bool) (*secretbox.KeyRing, error) {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	fi, err := os.Stat(s.ringPath())
	if err != nil {
		return nil, fmt.Errorf("auth: key ring: %w", err)
	}
	if force || !fi.ModTime().Equal(s.keys.mtime) || fi.Size() != s.keys.size {
		ring, err := secretbox.Load(s.ringPath())
		if err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		s.keys.ring, s.keys.mtime, s.keys.size = ring, fi.ModTime(), fi.Size()
	}
	return s.keys.ring, nil
}

// seal encrypts plain under the primary key, bound to aad.
func (s *Service) seal(plain []byte, aad string) ([]byte, error) {
	ring, err := s.currentRing(false)
	if err != nil {
		return nil, err
	}
	tok, err := ring.Encrypt(plain, aad)
	if err != nil {
		return nil, err
	}
	return []byte(tok), nil
}

// open decrypts a bx1 token bound to aad, or a value of the older format with
// legacyAD. Both paths authenticate; a raw legacy value contains bytes a token
// cannot have, so the two are told apart reliably (secretbox.IsToken).
func (s *Service) open(ct []byte, aad, legacyAD string) ([]byte, error) {
	if secretbox.IsToken(ct) {
		ring, err := s.currentRing(false)
		if err != nil {
			return nil, err
		}
		plain, err := ring.Decrypt(string(ct), aad)
		if errors.Is(err, secretbox.ErrFormat) {
			// Possibly a key another process added within the file clock's resolution.
			if ring, rerr := s.currentRing(true); rerr == nil {
				plain, err = ring.Decrypt(string(ct), aad)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("auth: cannot decrypt (%w)", err)
		}
		return plain, nil
	}
	master := s.legacyMaster()
	if master == nil || legacyAD == "" {
		return nil, errors.New("auth: value in the old format, but master.key was retired")
	}
	return openLegacy(master, ct, legacyAD)
}

// --- keys for one purpose --------------------------------------------------

// serverKey returns the named key from server_keys, creating it with create on
// first use. Concurrent creators settle on what is stored.
func (s *Service) serverKey(ctx context.Context, name string, create func() ([]byte, error)) ([]byte, error) {
	read := func() ([]byte, error) {
		var enc []byte
		err := s.db.QueryRowContext(ctx, `SELECT key_enc FROM server_keys WHERE name = ?`, name).Scan(&enc)
		if err != nil {
			return nil, err
		}
		return s.open(enc, serverKeyContext(name), "")
	}
	key, err := read()
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return key, err
	}
	fresh, err := create()
	if err != nil {
		return nil, err
	}
	defer clear(fresh)
	enc, err := s.seal(fresh, serverKeyContext(name))
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT OR IGNORE INTO server_keys(name, key_enc) VALUES (?, ?)`, name, enc); err != nil {
		return nil, err
	}
	return read()
}

func randomServerKey() ([]byte, error) {
	k := make([]byte, serverKeyBytes)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// purposeKey is a cached random key of its own (Basis spec/KRYPTO.md: separate keys per purpose).
func (s *Service) purposeKey(ctx context.Context, name string) ([]byte, error) {
	s.keys.mu.Lock()
	k := s.keys.derived[name]
	s.keys.mu.Unlock()
	if k != nil {
		return k, nil
	}
	k, err := s.serverKey(ctx, name, randomServerKey)
	if err != nil {
		return nil, err
	}
	s.keys.mu.Lock()
	if s.keys.derived == nil {
		s.keys.derived = map[string][]byte{}
	}
	s.keys.derived[name] = k
	s.keys.mu.Unlock()
	return k, nil
}

// networkKey is the HMAC key of login_known_networks (pressure.go).
func (s *Service) networkKey(ctx context.Context) ([]byte, error) {
	s.keys.mu.Lock()
	k := s.keys.netKey
	s.keys.mu.Unlock()
	if k != nil {
		return k, nil
	}
	k, err := s.serverKey(ctx, networkKeyName, randomServerKey)
	if err != nil {
		return nil, err
	}
	s.keys.mu.Lock()
	s.keys.netKey = k
	s.keys.mu.Unlock()
	return k, nil
}

func legacyTeamKey(master []byte, teamID int64) []byte {
	mac := hmac.New(sha256.New, master)
	fmt.Fprintf(mac, "team-vault:%d", teamID)
	return mac.Sum(nil)
}

// teamVaultKey is the source of a team vault's password. Vaults exist whose
// password was derived from master.key: that value moves into server_keys once
// and is independent of the file from then on. New vaults get the same derivation
// while master.key exists (so `keys rollback` stays possible), a random key after.
func (s *Service) teamVaultKey(ctx context.Context, teamID int64) ([]byte, error) {
	return s.serverKey(ctx, teamKeyName(teamID), func() ([]byte, error) {
		if master := s.legacyMaster(); master != nil {
			return legacyTeamKey(master, teamID), nil
		}
		exists, err := fileExists(filepath.Join(s.TeamDir(teamID), "secrets.vault"))
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("the vault of team %d needs a key derived from master.key, which was retired", teamID)
		}
		return randomServerKey()
	})
}

// --- status ----------------------------------------------------------------

// KeyStatus says what depends on which key.
type KeyStatus struct {
	Primary string `json:"primary"`
	// Kids are the keys in the ring with the number of values under each.
	Kids map[string]int `json:"kids"`
	// Legacy counts values still in the format of master.key.
	Legacy int `json:"legacy"`
	// UnknownKid counts bx1 values under a key that is not in the ring (data loss!).
	UnknownKid int `json:"unknownKid"`
	// TeamVaultsPending counts team vaults whose password still comes from master.key.
	TeamVaultsPending int `json:"teamVaultsPending"`
	// MasterKeyPresent tells whether master.key is still in use.
	MasterKeyPresent bool `json:"masterKeyPresent"`
}

// NeedsRewrap is the number of values rewrap would convert.
func (st *KeyStatus) NeedsRewrap() int {
	n := st.Legacy + st.TeamVaultsPending
	for kid, c := range st.Kids {
		if kid != st.Primary {
			n += c
		}
	}
	return n
}

type secretCounts struct {
	bx1, legacy, unknown int
	kids                 map[string]int
}

// eachSecretValue visits all encrypted values of the database.
func (s *Service) eachSecretValue(ctx context.Context, fn func(v []byte)) error {
	queries := []string{`SELECT key_enc FROM server_keys`}
	for _, c := range secretColumns {
		queries = append(queries, `SELECT `+c.column+` FROM `+c.table+` WHERE `+c.rowFilter())
	}
	for _, q := range queries {
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v []byte
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			fn(v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) countSecrets(ctx context.Context, ring *secretbox.KeyRing) (*secretCounts, error) {
	c := &secretCounts{kids: map[string]int{}}
	err := s.eachSecretValue(ctx, func(v []byte) {
		if !secretbox.IsToken(v) {
			c.legacy++
			return
		}
		kid, _ := secretbox.KidOf(string(v))
		c.bx1++
		c.kids[kid]++
		if ring != nil && !ring.Has(kid) {
			c.unknown++
		}
	})
	return c, err
}

// KeyStatus counts the values per key.
func (s *Service) KeyStatus(ctx context.Context) (*KeyStatus, error) {
	ring, err := s.currentRing(true)
	if err != nil {
		return nil, err
	}
	c, err := s.countSecrets(ctx, ring)
	if err != nil {
		return nil, err
	}
	st := &KeyStatus{Primary: ring.Primary(), Kids: map[string]int{}, Legacy: c.legacy, UnknownKid: c.unknown, MasterKeyPresent: s.legacyMaster() != nil}
	for _, kid := range ring.Kids() {
		st.Kids[kid] = c.kids[kid]
	}
	teams, err := s.teamVaultIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range teams {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_keys WHERE name = ?`, teamKeyName(id)).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			st.TeamVaultsPending++
		}
	}
	return st, nil
}

// teamVaultIDs lists the team vaults on disk.
func (s *Service) teamVaultIDs() ([]int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "teams"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, e := range entries {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || id <= 0 || !e.IsDir() {
			continue
		}
		if ok, _ := fileExists(filepath.Join(s.dataDir, "teams", e.Name(), "secrets.vault")); ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// --- rotation --------------------------------------------------------------

// RotateKey adds a new primary key to the ring and returns its id. Values under
// older keys stay readable; RewrapSecrets moves them. The write happens inside a
// database write lock so that two administrators cannot lose each other's key.
func (s *Service) RotateKey(ctx context.Context) (string, error) {
	var kid string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// _txlock=immediate: this transaction holds the write lock from here on.
		ring, err := secretbox.Load(s.ringPath())
		if err != nil {
			return err
		}
		if kid, err = ring.Rotate(); err != nil {
			return err
		}
		return ring.Save(s.ringPath())
	})
	if err != nil {
		s.Audit(ctx, "keys.rotate", "", false, "")
		return "", err
	}
	if _, err := s.currentRing(true); err != nil {
		return "", err
	}
	if err := s.Audit(ctx, "keys.rotate", kid, true, ""); err != nil {
		return "", err
	}
	return kid, nil
}

// RewrapReport is the result of RewrapSecrets.
type RewrapReport struct {
	Converted int `json:"converted"`
	Current   int `json:"current"` // already under the primary key
	Changed   int `json:"changed"` // changed by someone else meanwhile: rerun picks them up
	Failed    int `json:"failed"`  // could not be decrypted, left untouched
	Teams     int `json:"teams"`   // team vault keys moved into server_keys
}

func (s *Service) rewrapValue(ctx context.Context, ring *secretbox.KeyRing, id string, old []byte, aad, legacyAD string,
	swap func(c context.Context, next, old []byte) (int64, error), rep *RewrapReport) error {
	if secretbox.IsToken(old) && !ring.NeedsReencrypt(string(old)) {
		if _, err := ring.Decrypt(string(old), aad); err == nil {
			rep.Current++
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.keys.rewrapHook != nil {
		s.keys.rewrapHook(id)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	plain, err := s.open(old, aad, legacyAD)
	if err != nil {
		slog.Error("auth: rewrap: cannot decrypt", "value", id, "err", err)
		rep.Failed++
		return nil
	}
	defer clear(plain)
	tok, err := ring.Encrypt(plain, aad)
	if err != nil {
		return err
	}
	// Never store what cannot be read back.
	back, err := ring.Decrypt(tok, aad)
	if err != nil || !hmac.Equal(back, plain) {
		return fmt.Errorf("auth: rewrap: verification of %s failed", id)
	}
	n, err := swap(ctx, []byte(tok), old)
	if err != nil {
		return err
	}
	if n == 0 {
		rep.Changed++
		return nil
	}
	rep.Converted++
	return nil
}

// RewrapSecrets converts every value that is not under the primary key. It is
// idempotent and may be interrupted at any point (ctx): every value is at all
// times readable with what is in the key files.
func (s *Service) RewrapSecrets(ctx context.Context) (*RewrapReport, error) {
	ring, err := s.currentRing(true)
	if err != nil {
		return nil, err
	}
	rep := &RewrapReport{}
	audit := func(err error) {
		s.Audit(context.WithoutCancel(ctx), "keys.rewrap", ring.Primary(), err == nil && rep.Failed == 0,
			fmt.Sprintf("converted=%d current=%d changed=%d failed=%d teams=%d", rep.Converted, rep.Current, rep.Changed, rep.Failed, rep.Teams))
	}
	for _, col := range secretColumns {
		var last int64
		for {
			ids, vals, err := s.readSecretBatch(ctx, col, last)
			if err != nil {
				audit(err)
				return rep, err
			}
			if len(ids) == 0 {
				break
			}
			for i, id := range ids {
				last = id
				err := s.rewrapValue(ctx, ring, col.column+":"+strconv.FormatInt(id, 10), vals[i], col.context(id), col.legacy(id),
					func(c context.Context, next, old []byte) (int64, error) {
						res, err := s.db.ExecContext(c, `UPDATE `+col.table+` SET `+col.column+` = ? WHERE id = ? AND `+col.column+` = ?`, next, id, old)
						if err != nil {
							return 0, err
						}
						return res.RowsAffected()
					}, rep)
				if err != nil {
					audit(err)
					return rep, err
				}
			}
		}
	}
	teams, err := s.teamVaultIDs()
	if err != nil {
		audit(err)
		return rep, err
	}
	for _, id := range teams {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_keys WHERE name = ?`, teamKeyName(id)).Scan(&n); err != nil {
			audit(err)
			return rep, err
		}
		if n > 0 {
			continue
		}
		key, err := s.teamVaultKey(ctx, id)
		if err != nil {
			slog.Error("auth: rewrap: team vault", "team", id, "err", err)
			rep.Failed++
			continue
		}
		clear(key)
		rep.Teams++
	}
	if _, err := s.networkKey(ctx); err != nil {
		audit(err)
		return rep, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name, key_enc FROM server_keys ORDER BY name`)
	if err != nil {
		audit(err)
		return rep, err
	}
	type keyRow struct {
		name string
		enc  []byte
	}
	var keysToDo []keyRow
	for rows.Next() {
		var r keyRow
		if err := rows.Scan(&r.name, &r.enc); err != nil {
			rows.Close()
			audit(err)
			return rep, err
		}
		keysToDo = append(keysToDo, r)
	}
	rows.Close()
	for _, r := range keysToDo {
		err := s.rewrapValue(ctx, ring, "server_keys:"+r.name, r.enc, serverKeyContext(r.name), "",
			func(c context.Context, next, old []byte) (int64, error) {
				res, err := s.db.ExecContext(c, `UPDATE server_keys SET key_enc = ? WHERE name = ? AND key_enc = ?`, next, r.name, old)
				if err != nil {
					return 0, err
				}
				return res.RowsAffected()
			}, rep)
		if err != nil {
			audit(err)
			return rep, err
		}
	}
	audit(nil)
	if rep.Failed > 0 {
		return rep, fmt.Errorf("auth: %d values could not be converted (see the log); nothing was lost", rep.Failed)
	}
	return rep, nil
}

func (s *Service) readSecretBatch(ctx context.Context, col secretColumn, after int64) ([]int64, [][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, `+col.column+` FROM `+col.table+` WHERE id > ? AND `+col.rowFilter()+`
		ORDER BY id LIMIT ?`, after, rewrapBatch)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var vals [][]byte
	for rows.Next() {
		var id int64
		var v []byte
		if err := rows.Scan(&id, &v); err != nil {
			return nil, nil, err
		}
		ids, vals = append(ids, id), append(vals, v)
	}
	return ids, vals, rows.Err()
}

// RetireKey removes a key from the ring. It refuses while any value uses it.
func (s *Service) RetireKey(ctx context.Context, kid string) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		ring, err := secretbox.Load(s.ringPath())
		if err != nil {
			return err
		}
		if !ring.Has(kid) {
			return fmt.Errorf("%w: no key %q in the ring", ErrInvalidInput, kid)
		}
		if kid == ring.Primary() {
			return fmt.Errorf("%w: %q is the primary key; rotate first", ErrInvalidInput, kid)
		}
		q := `SELECT key_enc FROM server_keys`
		for _, c := range secretColumns {
			q += ` UNION ALL SELECT ` + c.column + ` FROM ` + c.table + ` WHERE ` + c.rowFilter()
		}
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		used := 0
		for rows.Next() {
			var v []byte
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			if k, ok := secretbox.KidOf(string(v)); ok && k == kid {
				used++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("%w: %d values still use key %q; run keys rewrap first", ErrInvalidInput, used, kid)
		}
		if err := ring.Remove(kid); err != nil {
			return err
		}
		return ring.Save(s.ringPath())
	})
	s.Audit(ctx, "keys.retire", kid, err == nil, "")
	if err != nil {
		return err
	}
	_, err = s.currentRing(true)
	return err
}

// RetireLegacyKey moves master.key aside once nothing depends on it. The file is
// renamed, not deleted: backups of older states still need it, the administrator
// decides where it goes. Rollback is impossible afterwards.
func (s *Service) RetireLegacyKey(ctx context.Context) (string, error) {
	st, err := s.KeyStatus(ctx)
	if err != nil {
		return "", err
	}
	if st.Legacy > 0 || st.TeamVaultsPending > 0 {
		return "", fmt.Errorf("%w: %d values and %d team vaults still use master.key; run keys rewrap first", ErrInvalidInput, st.Legacy, st.TeamVaultsPending)
	}
	if !st.MasterKeyPresent {
		return "", fmt.Errorf("%w: master.key was already retired", ErrInvalidInput)
	}
	dst := s.masterPath() + ".retired-" + s.now().Format("20060102-150405")
	if err := os.Rename(s.masterPath(), dst); err != nil {
		s.Audit(ctx, "keys.retire_legacy", "", false, "")
		return "", err
	}
	s.keys.mu.Lock()
	s.keys.master = nil
	s.keys.mu.Unlock()
	return dst, s.Audit(ctx, "keys.retire_legacy", "", true, "")
}
