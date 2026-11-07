// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/secretbox"
	"github.com/Midas1989/Mailclient/internal/secrets"
)

const keysTestPW = "korrekt-pferd-batterie"

// legacyInstall builds a data directory as the previous release left it: schema
// version 7, master.key, secrets in the old format, a user vault, a team vault,
// a web session, device tokens and a known network.
type legacyInstall struct {
	cfg             *config.Config
	master          []byte
	dek             map[int64][]byte
	totp            map[int64][]byte
	deviceToken     string
	unusedDevice    string
	webToken        string
	schemaV7Columns map[string]string
	teamID          int64
}

func tableColumns(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type IN ('table','index','trigger') AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n, q string
		if err := rows.Scan(&n, &q); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
		out[n] = ""
	}
	rows.Close()
	for _, n := range names {
		cols, err := db.Query(`SELECT name, type FROM pragma_table_info(?) ORDER BY name`, n)
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for cols.Next() {
			var a, b string
			cols.Scan(&a, &b)
			sb.WriteString(a + ":" + b + ",")
		}
		cols.Close()
		out[n] = sb.String()
	}
	return out
}

func newLegacyInstall(t *testing.T) *legacyInstall {
	t.Helper()
	ctx := context.Background()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	os.MkdirAll(filepath.Join(cfg.DataDir, "users"), 0o700)
	li := &legacyInstall{cfg: &cfg, dek: map[int64][]byte{}, totp: map[int64][]byte{}, teamID: 7}
	master, err := LoadOrCreateMasterKey(filepath.Join(cfg.DataDir, masterFile))
	if err != nil {
		t.Fatal(err)
	}
	li.master = master
	db, err := openDB(ctx, cfg.ServerDB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 7 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		db.Exec(`INSERT INTO schema_version VALUES (?, 1)`, m.version)
	}
	li.schemaV7Columns = tableColumns(t, db)
	now := time.Now().Unix()
	hash, err := HashPassword(keysTestPW, testArgon)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 5; id++ {
		dek := bytes.Repeat([]byte{byte(id)}, 32)
		secret := bytes.Repeat([]byte{byte(id + 100)}, 20)
		li.dek[id], li.totp[id] = dek, secret
		wrapped, _ := sealLegacy(master, dek, fmt.Sprintf("dek:%d", id))
		enc, _ := sealLegacy(master, secret, fmt.Sprintf("totp:%d", id))
		if _, err := db.Exec(`INSERT INTO users(id, username, email, role, password_hash, dek_wrapped, totp_secret_enc, totp_enabled, created_at, updated_at, password_changed_at)
			VALUES (?,?,?,?,?,?,?,1,?,?,?)`, id, fmt.Sprintf("user%d", id), fmt.Sprintf("user%d@example.de", id), "user", hash, wrapped, enc, now, now, now); err != nil {
			t.Fatal(err)
		}
		// The user's vault, unlocked by the DEK as usersecrets.go does.
		dir := filepath.Join(cfg.DataDir, "users", fmt.Sprint(id))
		os.MkdirAll(dir, 0o700)
		v, err := secrets.CreateVault(filepath.Join(dir, "secrets.vault"), vaultPassword(dek), vaultKDF)
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Set("mail", []byte(fmt.Sprintf("pw-%d", id))); err != nil {
			t.Fatal(err)
		}
		v.Lock()
	}
	teamDir := filepath.Join(cfg.DataDir, "teams", fmt.Sprint(li.teamID))
	os.MkdirAll(teamDir, 0o700)
	tv, err := secrets.CreateVault(filepath.Join(teamDir, "secrets.vault"), vaultPassword(legacyTeamKey(master, li.teamID)), vaultKDF)
	if err != nil {
		t.Fatal(err)
	}
	tv.Set("team", []byte("team-secret"))
	tv.Lock()

	li.deviceToken, li.unusedDevice, li.webToken = randomToken(32), randomToken(32), randomToken(32)
	if _, err := db.Exec(`INSERT INTO sessions(id, token_hash, user_id, kind, device_name, mfa, login_method, client_pubkey, created_at, expires_at, last_seen_at) VALUES
		(10, ?, 1, 'device', 'Handy', 0, 'device_code', ?, ?, ?, ?),
		(11, ?, 2, 'device', 'Alt', 0, 'password', NULL, ?, ?, ?),
		(12, ?, 3, 'web', '', 0, 'password', NULL, ?, ?, ?)`,
		hashToken(li.deviceToken), bytes.Repeat([]byte{9}, 32), now-100*86400, now+265*86400, now-86400,
		hashToken(li.unusedDevice), now-100*86400, now+40*86400, now-86400,
		hashToken(li.webToken), now-60, now+700*3600, now-60); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO push_subscriptions(id, user_id, session_id, endpoint, p256dh, auth, created_at) VALUES (1, 1, 10, 'https://e/1', 'k', 'a', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO login_known_networks(user_id, net_hash, first_seen, last_seen) VALUES (1, ?, ?, ?)`,
		netHash(master, 1, "192.0.2.0/24"), now, now); err != nil {
		t.Fatal(err)
	}
	return li
}

func (li *legacyInstall) open(t *testing.T) *Service {
	t.Helper()
	s, err := Open(context.Background(), li.cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.PasswordParams = testArgon
	t.Cleanup(func() { s.Close() })
	return s
}

// checkAll proves that nothing was lost: every DEK and TOTP secret decrypts,
// every vault opens.
func (li *legacyInstall) checkAll(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	for id, want := range li.dek {
		got, err := s.userDEK(ctx, id)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("DEK of user %d: %v", id, err)
		}
		var enc []byte
		s.db.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = ?`, id).Scan(&enc)
		secret, err := s.open(enc, secretTOTP.context(id), secretTOTP.legacy(id))
		if err != nil || !bytes.Equal(secret, li.totp[id]) {
			t.Fatalf("TOTP of user %d: %v", id, err)
		}
		v, err := s.UserSecrets(ctx, id)
		if err != nil {
			t.Fatalf("vault of user %d: %v", id, err)
		}
		if b, err := v.Get("mail"); err != nil || string(b) != fmt.Sprintf("pw-%d", id) {
			t.Fatalf("vault content of user %d: %q %v", id, b, err)
		}
	}
	tv, err := s.TeamSecrets(li.teamID)
	if err != nil {
		t.Fatal("team vault:", err)
	}
	if b, err := tv.Get("team"); err != nil || string(b) != "team-secret" {
		t.Fatalf("team vault content: %q %v", b, err)
	}
}

func TestUpgradeKeepsExistingDataReadable(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	if st, _ := os.Stat(filepath.Join(li.cfg.DataDir, keyringFile)); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatal("key ring not created with mode 0600")
	}
	li.checkAll(t, s)
	st, err := s.KeyStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Legacy != 10 || st.TeamVaultsPending != 0 || !st.MasterKeyPresent {
		// The team vault was opened by checkAll and thus moved into server_keys.
		t.Fatalf("status %+v", st)
	}
	// A login with the old-format TOTP secret works; new values are bx1.
	if _, err := s.BeginTOTP(ctx, mustUser(t, s, "neu", RoleUser, keysTestPW).ID); err != nil {
		t.Fatal(err)
	}
	var enc []byte
	s.db.QueryRow(`SELECT totp_secret_enc FROM users WHERE username = 'neu'`).Scan(&enc)
	if !secretbox.IsToken(enc) || !strings.Contains(string(enc), "bx1."+st.Primary+".") {
		t.Fatalf("new value not in bx1 format: %q", enc)
	}
	// A known network from before the key ring still counts.
	if !s.networkKnown(ctx, 1, "192.0.2.9") {
		t.Fatal("known network from before the upgrade forgotten")
	}
	s.recordKnownNetwork(ctx, 2, "198.51.100.9")
	if !s.networkKnown(ctx, 2, "198.51.100.9") {
		t.Fatal("new known network not recognised")
	}
}

func TestRotationInterruptedAndResumed(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	oldKid := func() string { st, _ := s.KeyStatus(context.Background()); return st.Primary }()
	// First rewrap from the old format, interrupted after three values.
	n := 0
	s.keys.rewrapHook = func(string) {
		n++
		if n == 3 {
			cancel()
		}
	}
	if _, err := s.RewrapSecrets(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted rewrap: %v", err)
	}
	s.keys.rewrapHook = nil
	bg := context.Background()
	st, err := s.KeyStatus(bg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Legacy == 0 || st.Kids[st.Primary] == 0 {
		t.Fatalf("expected a mix of formats: %+v", st)
	}
	li.checkAll(t, s) // also a mix is fully readable

	// A process that restarts at this point finds everything as well.
	s2 := li.open(t)
	li.checkAll(t, s2)
	rep, err := s2.RewrapSecrets(bg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Converted == 0 || rep.Failed != 0 {
		t.Fatalf("resume: %+v", rep)
	}
	// Idempotent: a second run has nothing to do.
	rep, err = s2.RewrapSecrets(bg)
	if err != nil || rep.Converted != 0 || rep.Current == 0 {
		t.Fatalf("second run: %+v %v", rep, err)
	}
	if st, _ := s2.KeyStatus(bg); st.NeedsRewrap() != 0 {
		t.Fatalf("after rewrap: %+v", st)
	}

	// Real rotation, interrupted again; the running server (s) has an old ring.
	newKid, err := s2.RotateKey(bg)
	if err != nil || newKid == oldKid {
		t.Fatal(newKid, err)
	}
	li.checkAll(t, s2)
	if err := s2.RetireKey(bg, oldKid); err == nil {
		t.Fatal("key in use retired")
	}
	ctx2, cancel2 := context.WithCancel(bg)
	defer cancel2()
	n = 0
	s2.keys.rewrapHook = func(string) {
		n++
		if n == 4 {
			cancel2()
		}
	}
	if _, err := s2.RewrapSecrets(ctx2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s2.keys.rewrapHook = nil
	// The other process reads values under the new key and writes with it.
	if _, err := s.userDEK(bg, 1); err != nil {
		t.Fatal("running server cannot read a value under the new key:", err)
	}
	if enc, err := s.seal([]byte("x"), "c"); err != nil || !strings.Contains(string(enc), "bx1."+newKid+".") {
		t.Fatalf("running server encrypts with the old key: %q %v", enc, err)
	}
	li.checkAll(t, s2)
	if err := s2.RetireKey(bg, oldKid); err == nil {
		t.Fatal("key in use retired after interrupted rewrap")
	}
	if _, err := s2.RewrapSecrets(bg); err != nil {
		t.Fatal(err)
	}
	if err := s2.RetireKey(bg, oldKid); err != nil {
		t.Fatal(err)
	}
	if err := s2.RetireKey(bg, newKid); err == nil {
		t.Fatal("primary key retired")
	}
	li.checkAll(t, s2)
	li.checkAll(t, li.open(t))

	// master.key can go now, the installation runs without it.
	dst, err := s2.RetireLegacyKey(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}
	s3 := li.open(t)
	li.checkAll(t, s3)
	if _, err := os.Stat(filepath.Join(li.cfg.DataDir, masterFile)); err == nil {
		t.Fatal("master.key was created again")
	}
	if err := s3.RollbackToLegacy(bg); err == nil {
		t.Fatal("rollback without master.key")
	}
	if s3.legacyMaster() != nil {
		t.Fatal("master key still loaded")
	}
	// Teams created now get a random key: no dependency on the retired file.
	if _, err := s3.TeamSecrets(99); err != nil {
		t.Fatal(err)
	}
}

func TestRetireLegacyRefusedWhileNeeded(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	if _, err := s.RetireLegacyKey(context.Background()); err == nil {
		t.Fatal("master.key retired while values need it")
	}
	if _, err := os.Stat(filepath.Join(li.cfg.DataDir, masterFile)); err != nil {
		t.Fatal(err)
	}
}

func TestRewrapLeavesBrokenAndChangedValuesAlone(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	garbage := bytes.Repeat([]byte{0xee}, 80)
	if _, err := s.db.Exec(`UPDATE users SET totp_secret_enc = ? WHERE id = 2`, garbage); err != nil {
		t.Fatal(err)
	}
	// A value changed by the running server between read and write must win.
	first := true
	s.keys.rewrapHook = func(id string) {
		if first && strings.HasPrefix(id, "totp_secret_enc:3") {
			first = false
			if _, err := s.db.Exec(`UPDATE users SET totp_secret_enc = x'01' WHERE id = 3`); err != nil {
				t.Error(err)
			}
		}
	}
	rep, err := s.RewrapSecrets(ctx)
	if err == nil || rep.Failed == 0 {
		t.Fatalf("broken value not reported: %+v %v", rep, err)
	}
	if rep.Changed != 1 {
		t.Fatalf("changed value: %+v", rep)
	}
	var got []byte
	s.db.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = 2`).Scan(&got)
	if !bytes.Equal(got, garbage) {
		t.Fatal("broken value overwritten")
	}
	s.db.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = 3`).Scan(&got)
	if !bytes.Equal(got, []byte{1}) {
		t.Fatal("concurrent change overwritten")
	}
}

// Values are bound to their row and column, also across the two formats.
func TestSecretsAreBoundToTheirContext(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	if _, err := s.RewrapSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET dek_wrapped = (SELECT dek_wrapped FROM users WHERE id = 2) WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.userDEK(ctx, 1); err == nil {
		t.Fatal("value of another row accepted")
	}
	var enc []byte
	s.db.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = 4`).Scan(&enc)
	if !strings.HasPrefix(string(enc), "bx1.") {
		t.Fatalf("%q", enc)
	}
	if _, err := s.open(enc, "users.dek_wrapped:4", "dek:4"); err == nil {
		t.Fatal("value of another column accepted")
	}
	// Plain text without the prefix is never passed through.
	if _, err := s.open([]byte("JBSWY3DPEHPK3PXP"), secretTOTP.context(1), secretTOTP.legacy(1)); err == nil {
		t.Fatal("plain text accepted")
	}
}

func TestOpenRefusesMissingKeyFiles(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	s.RewrapSecrets(ctx)
	if st, _ := s.KeyStatus(ctx); st.Legacy != 0 {
		t.Fatalf("%+v", st)
	}
	s.Close()
	ring := filepath.Join(li.cfg.DataDir, keyringFile)
	os.Rename(ring, ring+".weg")
	if _, err := Open(ctx, li.cfg); err == nil || !strings.Contains(err.Error(), keyringFile) {
		t.Fatalf("missing key ring: %v", err)
	}
	if _, err := os.Stat(ring); err == nil {
		t.Fatal("a new, useless key ring was created")
	}
	os.Rename(ring+".weg", ring)

	li2 := newLegacyInstall(t)
	li2.open(t).Close()
	os.Remove(filepath.Join(li2.cfg.DataDir, masterFile))
	if _, err := Open(ctx, li2.cfg); err == nil || !strings.Contains(err.Error(), masterFile) {
		t.Fatalf("missing master.key: %v", err)
	}
}

// Forwards with existing data, backwards, forwards again.
func TestMigrationForwardBackwardForward(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	var v int
	s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if v != 8 {
		t.Fatalf("schema version %d", v)
	}
	// Existing rows got the new columns with sensible values.
	var scopes string
	var issued int64
	s.db.QueryRow(`SELECT scopes, token_issued_at FROM sessions WHERE id = 10`).Scan(&scopes, &issued)
	if scopes != DefaultDeviceScopes || issued != 0 {
		t.Fatal(scopes, issued)
	}
	s.db.QueryRow(`SELECT scopes FROM sessions WHERE id = 12`).Scan(&scopes)
	if scopes != "" {
		t.Fatal("web session got scopes", scopes)
	}
	// Use the new features so that the way back has something to undo.
	if _, err := s.RotateKey(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RewrapSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	li.checkAll(t, s)
	sess, _, err := s.Authenticate(ctx, li.deviceToken, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := s.RotateDeviceToken(ctx, sess, li.deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, next, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if !s.networkKnown(ctx, 1, "192.0.2.77") {
		t.Fatal("known network")
	}
	for _, q := range []string{`INSERT INTO rate_limit VALUES ('x', 1, 1)`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.RollbackToLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Exactly the schema of the previous release, with the data in its format.
	db, err := openDB(ctx, li.cfg.ServerDB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if v != 7 {
		t.Fatalf("schema version after rollback: %d", v)
	}
	if got := tableColumns(t, db); fmt.Sprint(got) != fmt.Sprint(li.schemaV7Columns) {
		t.Fatalf("schema differs:\n%v\n%v", got, li.schemaV7Columns)
	}
	for id, want := range li.dek {
		var enc []byte
		db.QueryRow(`SELECT dek_wrapped FROM users WHERE id = ?`, id).Scan(&enc)
		if secretbox.IsToken(enc) {
			t.Fatalf("user %d still in bx1", id)
		}
		if got, err := openLegacy(li.master, enc, fmt.Sprintf("dek:%d", id)); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("user %d DEK: %v", id, err)
		}
		db.QueryRow(`SELECT totp_secret_enc FROM users WHERE id = ?`, id).Scan(&enc)
		if got, err := openLegacy(li.master, enc, fmt.Sprintf("totp:%d", id)); err != nil || !bytes.Equal(got, li.totp[id]) {
			t.Fatalf("user %d TOTP: %v", id, err)
		}
	}
	// The rotated device token and the push link are there for the old program.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 10 AND token_hash = ?`, hashToken(next)).Scan(&n)
	if n != 1 {
		t.Fatal("device token lost")
	}
	db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE session_id = 10`).Scan(&n)
	if n != 1 {
		t.Fatal("push link lost")
	}
	db.Close()

	// The upgrade can simply be repeated.
	s2 := li.open(t)
	li.checkAll(t, s2)
	if _, _, err := s2.Authenticate(ctx, next, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackRefusesForeignTeamKey(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	enc, _ := s.seal(bytes.Repeat([]byte{1}, 32), serverKeyContext(teamKeyName(li.teamID)))
	if _, err := s.db.Exec(`INSERT INTO server_keys(name, key_enc) VALUES (?, ?)`, teamKeyName(li.teamID), enc); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackToLegacy(ctx); err == nil {
		t.Fatal("rollback would lose a team vault")
	}
	var v int
	s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if v != 8 {
		t.Fatal("schema changed by the refused rollback")
	}
}

func TestDropTeamSecretsRemovesKey(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.TeamSecrets(5); err != nil {
		t.Fatal(err)
	}
	s.DropTeamSecrets(5)
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM server_keys WHERE name = ?`, teamKeyName(5)).Scan(&n)
	if n != 0 {
		t.Fatal("key of a deleted team kept")
	}
}
