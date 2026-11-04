// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build goolm

package matrix

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	_ "modernc.org/sqlite" // database/sql driver "sqlite" (pure Go)
)

// e2eeAvailable reports whether this build contains end-to-end encryption.
const e2eeAvailable = true

// e2ee decrypts events of encrypted rooms.
type e2ee interface {
	Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)
	Close()
}

type helper struct {
	h  *cryptohelper.CryptoHelper
	db *sql.DB
}

func (h helper) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return h.h.Decrypt(ctx, evt)
}
func (h helper) Close() {
	_ = h.h.Close()
	_ = h.db.Close()
}

// pickleKey returns the per-account key that encrypts the Olm account in
// crypto.db (created on first use, kept next to the session).
func pickleKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "pickle.key")
	if b, err := os.ReadFile(path); err == nil {
		return hex.DecodeString(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600)
}

// setupE2EE initializes mautrix's crypto helper on goolm (pure Go) with
// its store in <account>/crypto.db (modernc.org/sqlite, no CGO). The
// helper decrypts incoming events and encrypts sends to encrypted rooms.
func setupE2EE(ctx context.Context, cli *mautrix.Client, dir string, _ *mautrix.DefaultSyncer) (e2ee, error) {
	key, err := pickleKey(dir)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dir, "crypto.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	db, err := dbutil.NewWithDB(sqlDB, "sqlite3")
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	cli.StateStore = nil // the helper manages a crypto-compatible state store
	h, err := cryptohelper.NewCryptoHelper(cli, key, db)
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := h.Init(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	cli.Crypto = h
	return helper{h: h, db: sqlDB}, nil
}
