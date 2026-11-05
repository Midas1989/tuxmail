// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth implements server-mode user management (KONZEPT 4.5, 15.6):
// users, roles, groups, password and TOTP login, recovery codes, sessions and
// device tokens, invites, OIDC login, login throttling, the audit log and the
// per-user secret encryption.
//
// All state lives in its own SQLite database data_dir/server.db, separate from
// the per-user mail databases under data_dir/users/<id>/.
//
// Key hierarchy: data_dir/master.key holds a random 256-bit server master key
// (KEK, file mode 0600, created on first start). Every user gets a random
// 256-bit data encryption key (DEK) at creation, stored in users.dek_wrapped
// encrypted with the KEK (XChaCha20-Poly1305, associated data "dek:<id>").
// The DEK is used as the password of the user's internal/secrets Vault file
// data_dir/users/<id>/secrets.vault (see UserSecrets). TOTP secrets are
// encrypted with the KEK directly. Losing master.key makes all stored user
// secrets and TOTP enrolments unreadable, so it must be part of backups.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/secrets"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Errors returned by the service. HTTP handlers map them to status codes.
var (
	ErrNotFound           = errors.New("auth: not found")
	ErrExists             = errors.New("auth: already exists")
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	ErrUserDisabled       = errors.New("auth: user is disabled")
	ErrThrottled          = errors.New("auth: too many failed logins, try again later")
	ErrWeakPassword       = errors.New("auth: password does not meet the policy")
	ErrInvalidCode        = errors.New("auth: invalid or expired code")
	ErrInvalidInput       = errors.New("auth: invalid input")
	ErrLastAdmin          = errors.New("auth: cannot remove the last active administrator")
	ErrRegistration       = errors.New("auth: registration is disabled")
	ErrReauthRequired     = errors.New("auth: re-authentication required")
)

// Role is a user role.
type Role string

// Roles (KONZEPT 15.6).
const (
	RoleAdmin    Role = "admin"
	RoleUser     Role = "user"
	RoleReadonly Role = "readonly"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleUser || r == RoleReadonly }

// Service is the user management service. It is safe for concurrent use.
type Service struct {
	db      *sql.DB
	cfg     *config.Config
	master  []byte
	dataDir string

	// Now returns the current time; tests may replace it.
	Now func() time.Time
	// PasswordParams are used for new password hashes.
	PasswordParams ArgonParams
	// PressureDelay is the pause before each password verification of an
	// account under distributed guessing (NA-06).
	PressureDelay time.Duration

	gates *[pressureStripes]pressureGate

	vaultMu sync.Mutex
	vaults  map[int64]*secrets.Vault

	oidcMu sync.Mutex
	oidc   *oidcClient
}

// Open creates data_dir if needed, opens (or creates) server.db, applies
// migrations and loads (or creates) the master key.
func Open(ctx context.Context, cfg *config.Config) (*Service, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create data dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "users"), 0o700); err != nil {
		return nil, fmt.Errorf("auth: create users dir: %w", err)
	}
	master, err := LoadOrCreateMasterKey(filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		return nil, err
	}
	db, err := openDB(ctx, cfg.ServerDB())
	if err != nil {
		return nil, err
	}
	s := &Service{
		db: db, cfg: cfg, master: master, dataDir: cfg.DataDir,
		Now:            time.Now,
		PasswordParams: DefaultArgonParams,
		PressureDelay:  DefaultPressureDelay,
		gates:          newPressureGates(),
		vaults:         map[int64]*secrets.Vault{},
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func openDB(ctx context.Context, path string) (*sql.DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("auth: database path must not contain '?': %q", path)
	}
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(10000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("auth: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth: open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database and locks all open vaults.
func (s *Service) Close() error {
	s.vaultMu.Lock()
	for id, v := range s.vaults {
		v.Lock()
		delete(s.vaults, id)
	}
	s.vaultMu.Unlock()
	return s.db.Close()
}

// DB exposes the server database for sibling packages that own tables in it
// (e.g. internal/server/push for push_subscriptions).
func (s *Service) DB() *sql.DB { return s.db }

// Config returns the configuration the service was opened with.
func (s *Service) Config() *config.Config { return s.cfg }

// UserDir returns data_dir/users/<id>, the per-user data directory.
func (s *Service) UserDir(id int64) string {
	return filepath.Join(s.dataDir, "users", fmt.Sprint(id))
}

func (s *Service) now() time.Time { return s.Now().UTC() }

func (s *Service) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// randomToken returns n random bytes encoded as unpadded base64url.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken returns the SHA-256 digest stored instead of a bearer secret.
func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func nullTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
