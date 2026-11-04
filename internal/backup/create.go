// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Mode is the scope of a backup (KONZEPT 14.1).
type Mode string

const (
	ModeConfig Mode = "config" // configuration only
	ModeLocal  Mode = "local"  // configuration + local data
	ModeFull   Mode = "full"   // the whole database and blob store
	ModeCustom Mode = "custom" // per account and area
)

// Areas of a custom backup.
const (
	AreaMail     = "mail"
	AreaCalendar = "calendar"
	AreaContacts = "contacts"
	AreaChats    = "chats"
	AreaSettings = "settings"
)

// AllAreas lists the areas of a custom backup.
var AllAreas = []string{AreaMail, AreaCalendar, AreaContacts, AreaChats, AreaSettings}

// LocalAccountKind is the account kind of local folders (not synced).
const LocalAccountKind = "local"

// MessengerKinds are account kinds whose sessions cannot be used in
// parallel on two devices (KONZEPT 14.3).
var MessengerKinds = []string{string(domain.KindWhatsApp), string(domain.KindSignal), string(domain.KindMatrixBridge)}

// Scope selects the content of a backup.
type Scope struct {
	Mode Mode `json:"mode"`
	// Accounts limits a custom backup to these accounts (empty: all).
	Accounts []int64 `json:"accounts,omitempty"`
	// Areas limits a custom backup (empty: all).
	Areas []string `json:"areas,omitempty"`
	// Secrets includes the stored credentials (requires a passphrase).
	Secrets bool `json:"secrets,omitempty"`
}

func (s Scope) area(a string) bool { return len(s.Areas) == 0 || slices.Contains(s.Areas, a) }

// Validate checks the scope.
func (s Scope) Validate() error {
	switch s.Mode {
	case ModeConfig, ModeLocal, ModeFull, ModeCustom:
	default:
		return fmt.Errorf("backup: unknown mode %q", s.Mode)
	}
	for _, a := range s.Areas {
		if !slices.Contains(AllAreas, a) {
			return fmt.Errorf("backup: unknown area %q", a)
		}
	}
	return nil
}

// Options configure Create.
type Options struct {
	Store   *store.Store
	Blobs   *blob.Store
	Secrets domain.SecretStore
	// SecretRefs are additional secret references to include besides the
	// accounts' (e.g. the AI API key).
	SecretRefs []string
	Scope      Scope
	// Passphrase encrypts the archive (age, scrypt). It is required when
	// secrets or messenger accounts are included.
	Passphrase string
	// Base is the manifest of the previous backup; only blobs it does not
	// list are written (incremental backup).
	Base       *Manifest
	AppVersion string
	TempDir    string
	Now        func() time.Time
	// Progress reports the stage ("snapshot", "blobs", "done") and counts.
	Progress func(stage string, done, total int)
}

func (o *Options) progress(stage string, done, total int) {
	if o.Progress != nil {
		o.Progress(stage, done, total)
	}
}

// Create writes a backup archive to w and returns its manifest.
func Create(ctx context.Context, w io.Writer, opts Options) (*Manifest, error) {
	if opts.Store == nil {
		return nil, errors.New("backup: a store is required")
	}
	if err := opts.Scope.Validate(); err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Scope.Secrets && opts.Passphrase == "" {
		return nil, fmt.Errorf("%w: secrets are only stored encrypted", ErrNeedPassphrase)
	}
	tmp, err := os.MkdirTemp(opts.TempDir, "tuxmail-backup-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	opts.progress("snapshot", 0, 0)
	snap := filepath.Join(tmp, "tuxmail.db")
	if err := opts.Store.Backup(ctx, snap); err != nil {
		return nil, err
	}
	m := &Manifest{Format: FormatVersion, ID: newID(), Created: opts.Now().UTC(), AppVersion: opts.AppVersion, Scope: opts.Scope,
		Encrypted: opts.Passphrase != "", Counts: map[string]int{}}
	if opts.Base != nil {
		m.BaseID = opts.Base.ID
	}
	refs, hashes, err := prune(ctx, snap, opts.Scope, m)
	if err != nil {
		return nil, err
	}
	if m.HasMessenger() && opts.Passphrase == "" && (opts.Scope.Mode == ModeFull || (opts.Scope.Mode == ModeCustom && opts.Scope.area(AreaChats))) {
		return nil, fmt.Errorf("%w: messenger sessions are only stored encrypted", ErrNeedPassphrase)
	}

	// Blobs.
	if opts.Blobs != nil {
		if opts.Scope.Mode == ModeFull {
			hashes = map[string]bool{}
			if err := opts.Blobs.Walk(func(h string) error { hashes[h] = true; return nil }); err != nil {
				return nil, err
			}
		}
		for h := range hashes {
			if ok, _ := opts.Blobs.Has(h); ok {
				m.Blobs = append(m.Blobs, h)
			}
		}
		sort.Strings(m.Blobs)
	}
	var base map[string]bool
	if opts.Base != nil {
		base = make(map[string]bool, len(opts.Base.Blobs))
		for _, h := range opts.Base.Blobs {
			base[h] = true
		}
	}
	var include []string
	for _, h := range m.Blobs {
		if !base[h] {
			include = append(include, h)
		}
	}
	m.BlobsIncluded = len(include)

	// Secrets.
	var secretsJSON []byte
	if opts.Scope.Secrets && opts.Secrets != nil {
		all := map[string]string{}
		for _, r := range append(refs, opts.SecretRefs...) {
			if r == "" {
				continue
			}
			if v, err := opts.Secrets.Get(r); err == nil {
				all[r] = base64.StdEncoding.EncodeToString(v)
			}
		}
		if secretsJSON, err = json.Marshal(all); err != nil {
			return nil, err
		}
		m.Secrets = true
		m.Counts["secrets"] = len(all)
	}

	// Checksums of the regular files.
	dbSum, dbSize, err := fileSum(snap)
	if err != nil {
		return nil, err
	}
	m.Files = append(m.Files, FileEntry{Path: dbName, Size: dbSize, SHA256: dbSum})
	if secretsJSON != nil {
		s := sha256.Sum256(secretsJSON)
		m.Files = append(m.Files, FileEntry{Path: secretsName, Size: int64(len(secretsJSON)), SHA256: hex.EncodeToString(s[:])})
	}
	if v, err := opts.Store.SchemaVersion(ctx); err == nil {
		m.SchemaVersion = v
	}

	aw, err := newWriter(w, opts.Passphrase)
	if err != nil {
		return nil, err
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	now := m.Created
	if err := aw.bytes(manifestName, now, mj); err != nil {
		return nil, err
	}
	if err := copyFileInto(aw, dbName, snap, now); err != nil {
		return nil, err
	}
	if secretsJSON != nil {
		if err := aw.bytes(secretsName, now, secretsJSON); err != nil {
			return nil, err
		}
	}
	for i, h := range include {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bp, err := opts.Blobs.Path(h)
		if err != nil {
			return nil, err
		}
		if err := copyFileInto(aw, blobPrefix+h[0:2]+"/"+h[2:4]+"/"+h, bp, now); err != nil {
			return nil, err
		}
		if i%200 == 0 {
			opts.progress("blobs", i, len(include))
		}
	}
	if err := aw.close(); err != nil {
		return nil, err
	}
	opts.progress("done", len(include), len(include))
	return m, nil
}

func copyFileInto(aw *writer, name, path string, mod time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return aw.file(name, mod, st.Size(), f)
}

func fileSum(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// prune reduces the snapshot to the scope and fills the account list and
// counts of m. It returns the secret references and the blob hashes the
// remaining rows reference.
func prune(ctx context.Context, path string, sc Scope, m *Manifest) ([]string, map[string]bool, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	exec := func(q string, args ...any) error {
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("backup: prune: %s: %w", q, err)
		}
		return nil
	}
	nonLocal := `(SELECT id FROM accounts WHERE kind <> '` + LocalAccountKind + `')`
	var stmts []string
	switch sc.Mode {
	case ModeConfig:
		stmts = []string{`DELETE FROM messages`, `DELETE FROM chats`, `DELETE FROM cal_objects`, `DELETE FROM contacts`}
	case ModeLocal:
		stmts = []string{`DELETE FROM messages WHERE account_id IN ` + nonLocal, `DELETE FROM chats`,
			`DELETE FROM cal_objects WHERE calendar_id IN (SELECT id FROM calendars WHERE account_id IS NOT NULL)`,
			`DELETE FROM contacts WHERE addressbook_id IN (SELECT id FROM addressbooks WHERE account_id IS NOT NULL)`}
	case ModeCustom:
		if len(sc.Accounts) > 0 {
			if err := exec(`DELETE FROM accounts WHERE id NOT IN (`+placeholders(len(sc.Accounts))+`)`, idArgs(sc.Accounts)...); err != nil {
				return nil, nil, err
			}
		}
		if !sc.area(AreaMail) {
			stmts = append(stmts, `DELETE FROM messages`)
		}
		if !sc.area(AreaChats) {
			stmts = append(stmts, `DELETE FROM chats`)
		}
		if !sc.area(AreaCalendar) {
			stmts = append(stmts, `DELETE FROM cal_objects`)
		}
		if !sc.area(AreaContacts) {
			stmts = append(stmts, `DELETE FROM contacts`)
		}
		if !sc.area(AreaSettings) {
			stmts = append(stmts, `DELETE FROM app_settings`, `DELETE FROM workflows`, `DELETE FROM snippets`)
		}
	}
	if sc.Mode != ModeFull {
		stmts = append(stmts, `DELETE FROM ops`, `DELETE FROM uploads`, `DELETE FROM search_history`, `DELETE FROM workflow_runs`,
			`DELETE FROM conversations WHERE id NOT IN (SELECT conversation_id FROM messages WHERE conversation_id IS NOT NULL
				UNION SELECT conversation_id FROM chats WHERE conversation_id IS NOT NULL)`)
	}
	for _, q := range stmts {
		if err := exec(q); err != nil {
			return nil, nil, err
		}
	}
	if sc.Mode != ModeFull {
		if err := exec(`VACUUM`); err != nil {
			return nil, nil, err
		}
	}
	// Accounts and counts.
	rows, err := db.QueryContext(ctx, `SELECT a.id, a.kind, a.display_name, a.config_json, COALESCE(a.secret_ref, ''),
		(SELECT COUNT(*) FROM messages WHERE account_id = a.id) FROM accounts a ORDER BY a.sort_order, a.id`)
	if err != nil {
		return nil, nil, err
	}
	var refs []string
	for rows.Next() {
		var (
			ai       AccountInfo
			cfg, ref string
		)
		if err := rows.Scan(&ai.ID, &ai.Kind, &ai.Name, &cfg, &ref, &ai.Messages); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var acc domain.Account
		if json.Unmarshal([]byte(cfg), &acc) == nil {
			ai.Address = acc.Extra["address"]
		}
		ai.Messenger = slices.Contains(MessengerKinds, ai.Kind)
		m.Accounts = append(m.Accounts, ai)
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	rows.Close()
	for name, q := range map[string]string{"messages": `SELECT COUNT(*) FROM messages`, "chatMessages": `SELECT COUNT(*) FROM chat_messages`,
		"events": `SELECT COUNT(*) FROM cal_objects`, "contacts": `SELECT COUNT(*) FROM contacts`, "workflows": `SELECT COUNT(*) FROM workflows`,
		"identities": `SELECT COUNT(*) FROM identities`, "signatures": `SELECT COUNT(*) FROM signatures`} {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err == nil {
			m.Counts[name] = n
		}
	}
	if pr, err := db.QueryContext(ctx, `SELECT password_ref FROM cloud_links WHERE password_ref IS NOT NULL`); err == nil {
		for pr.Next() {
			var r string
			if pr.Scan(&r) == nil {
				refs = append(refs, r)
			}
		}
		pr.Close()
	}
	hashes := map[string]bool{}
	hr, err := db.QueryContext(ctx, `SELECT blob_hash FROM messages WHERE blob_hash IS NOT NULL
		UNION SELECT blob_hash FROM attachments WHERE blob_hash IS NOT NULL
		UNION SELECT media_blob FROM chat_messages WHERE media_blob IS NOT NULL
		UNION SELECT avatar_blob FROM persons WHERE avatar_blob IS NOT NULL
		UNION SELECT blob_hash FROM signature_images`)
	if err != nil {
		return nil, nil, err
	}
	defer hr.Close()
	for hr.Next() {
		var h string
		if hr.Scan(&h) == nil && blob.ValidHash(h) {
			hashes[h] = true
		}
	}
	if m.HasMessenger() {
		m.Warnings = append(m.Warnings, "messenger")
	}
	return refs, hashes, hr.Err()
}
