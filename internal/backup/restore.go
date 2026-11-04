// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Extracted is a verified archive unpacked into a temporary directory.
type Extracted struct {
	Dir      string
	Manifest *Manifest
	// Blobs is a blob store on the extracted blobs (archive + bases).
	Blobs *blob.Store
}

// Close removes the temporary directory.
func (e *Extracted) Close() error { return os.RemoveAll(e.Dir) }

// DBPath is the extracted database snapshot.
func (e *Extracted) DBPath() string { return filepath.Join(e.Dir, filepath.FromSlash(dbName)) }

// Secrets returns the extracted secrets (nil if the archive has none).
func (e *Extracted) Secrets() (map[string][]byte, error) {
	b, err := os.ReadFile(filepath.Join(e.Dir, secretsName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var enc map[string]string
	if err := json.Unmarshal(b, &enc); err != nil {
		return nil, fmt.Errorf("%w: secrets: %v", ErrTampered, err)
	}
	out := make(map[string][]byte, len(enc))
	for k, v := range enc {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("%w: secrets: %v", ErrTampered, err)
		}
		out[k] = raw
	}
	return out, nil
}

// Source is an archive to read (the newest first, then its bases).
type Source struct {
	Open func() (io.ReadCloser, error)
	Name string
}

// FileSource opens a local archive file.
func FileSource(p string) Source {
	return Source{Name: filepath.Base(p), Open: func() (io.ReadCloser, error) { return os.Open(p) }}
}

// Extract unpacks and verifies an archive and, for incremental archives,
// the blobs of its bases (in any order; matched by manifest id).
func Extract(ctx context.Context, src Source, passphrase string, bases []Source, tempDir string) (*Extracted, error) {
	dir, err := os.MkdirTemp(tempDir, "tuxmail-restore-*")
	if err != nil {
		return nil, err
	}
	ex := &Extracted{Dir: dir}
	fail := func(err error) (*Extracted, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	m, err := extractOne(ctx, src, passphrase, dir, true)
	if err != nil {
		return fail(err)
	}
	ex.Manifest = m
	want := m.BaseID
	seen := map[string]bool{m.ID: true}
	for want != "" {
		found := false
		for _, b := range bases {
			bm, err := peek(b, passphrase)
			if err != nil || bm.ID != want || seen[bm.ID] {
				continue
			}
			if _, err := extractOne(ctx, b, passphrase, dir, false); err != nil {
				return fail(fmt.Errorf("base %s: %w", b.Name, err))
			}
			seen[bm.ID] = true
			want, found = bm.BaseID, true
			break
		}
		if !found {
			return fail(fmt.Errorf("%w (id %s)", ErrMissingBase, want))
		}
	}
	bs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		return fail(err)
	}
	ex.Blobs = bs
	for _, h := range m.Blobs {
		if ok, _ := bs.Has(h); !ok {
			if m.Incremental() {
				return fail(fmt.Errorf("%w: blob %s is missing", ErrMissingBase, h))
			}
			return fail(fmt.Errorf("%w: blob %s is missing", ErrTampered, h))
		}
	}
	return ex, nil
}

func peek(src Source, passphrase string) (*Manifest, error) {
	rc, err := src.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return Inspect(rc, passphrase)
}

// extractOne unpacks one archive into dir. full also extracts the regular
// files (db, secrets); bases only contribute blobs.
func extractOne(ctx context.Context, src Source, passphrase, dir string, full bool) (*Manifest, error) {
	rc, err := src.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	ar, err := openReader(rc, passphrase)
	if err != nil {
		return nil, err
	}
	defer ar.close()
	m, err := ar.manifest()
	if err != nil {
		return nil, err
	}
	sums := map[string]FileEntry{}
	for _, f := range m.Files {
		sums[f.Path] = f
	}
	got := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := ar.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean(h.Name)
		// filepath.IsLocal rejects "..", absolute, drive and UNC paths;
		// backslashes would be separators on Windows (zip-slip, AUDIT-C).
		if h.Typeflag != '0' && h.Typeflag != 0 || strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") ||
			strings.Contains(name, `\`) || !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("%w: unexpected entry %q", ErrTampered, h.Name)
		}
		switch {
		case strings.HasPrefix(name, blobPrefix):
			hash := path.Base(name)
			if !blob.ValidHash(hash) || name != blobPrefix+hash[0:2]+"/"+hash[2:4]+"/"+hash {
				return nil, fmt.Errorf("%w: bad blob entry %q", ErrTampered, name)
			}
			dst := filepath.Join(dir, "blobs", hash[0:2], hash[2:4], hash)
			if err := writeEntry(dst, ar, h.Size); err != nil {
				return nil, err
			}
			if err := verifyBlob(dst, hash); err != nil {
				return nil, err
			}
		case !full:
			if _, err := io.Copy(io.Discard, ar.tr); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrTampered, err)
			}
		default:
			fe, ok := sums[name]
			if !ok {
				return nil, fmt.Errorf("%w: %q is not listed in the manifest", ErrTampered, name)
			}
			dst := filepath.Join(dir, filepath.FromSlash(name))
			if err := writeEntry(dst, ar, h.Size); err != nil {
				return nil, err
			}
			sum, size, err := fileSum(dst)
			if err != nil {
				return nil, err
			}
			if sum != fe.SHA256 || size != fe.Size {
				return nil, fmt.Errorf("%w: checksum of %s does not match", ErrTampered, name)
			}
			got[name] = true
		}
	}
	if full {
		for _, f := range m.Files {
			if !got[f.Path] {
				return nil, fmt.Errorf("%w: %s is missing", ErrTampered, f.Path)
			}
		}
	}
	return m, nil
}

func writeEntry(dst string, ar *reader, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, ar.tr)
	cerr := f.Close()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if n != size {
		return fmt.Errorf("%w: truncated entry", ErrTampered)
	}
	return cerr
}

// verifyBlob decompresses a blob file and compares its content hash.
func verifyBlob(p, hash string) error {
	bs, err := blob.New(filepath.Dir(filepath.Dir(filepath.Dir(p))))
	if err != nil {
		return err
	}
	rc, err := bs.Open(hash)
	if err != nil {
		return fmt.Errorf("%w: blob %s: %v", ErrTampered, hash, err)
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("%w: blob %s: %v", ErrTampered, hash, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return fmt.Errorf("%w: blob %s has wrong content", ErrTampered, hash)
	}
	return nil
}

// Verify reads a complete archive (and its bases) and checks every checksum.
func Verify(ctx context.Context, src Source, passphrase string, bases []Source, tempDir string) (*Manifest, error) {
	ex, err := Extract(ctx, src, passphrase, bases, tempDir)
	if err != nil {
		return nil, err
	}
	defer ex.Close()
	return ex.Manifest, nil
}

// ==== restore ==================================================================

// RestoreMode decides how the backup is applied.
type RestoreMode string

const (
	// Replace rebuilds the data directory from the backup.
	Replace RestoreMode = "replace"
	// Merge adds accounts and settings of the backup to the existing data.
	Merge RestoreMode = "merge"
)

// RestoreOptions configure Restore.
type RestoreOptions struct {
	Mode RestoreMode
	// Accounts limits the restore to these accounts of the backup (ids as in
	// the manifest; empty: all).
	Accounts []int64
	// ConfigOnly restores accounts, identities, signatures and settings but
	// no mail, calendar or contact data.
	ConfigOnly bool
	// Secrets restores the credentials if the archive has them.
	Secrets bool

	// Replace: the data directory (the store must be closed). The previous
	// database is kept as tuxmail.db.before-restore-<time>.
	DataDir string
	// Merge: the live store, blob store and secret store.
	Store       *store.Store
	Blobs       *blob.Store
	SecretStore domain.SecretStore
	// OnMessage is called for every merged message (e.g. to index it).
	OnMessage func(ctx context.Context, messageID int64)
	Now       func() time.Time
}

// RestoreResult reports what was restored.
type RestoreResult struct {
	Mode            RestoreMode    `json:"mode"`
	Accounts        int            `json:"accounts"`
	Messages        int            `json:"messages"`
	Blobs           int            `json:"blobs"`
	Secrets         int            `json:"secrets"`
	Counts          map[string]int `json:"counts,omitempty"`
	SchemaFrom      int            `json:"schemaFrom"`
	SchemaTo        int            `json:"schemaTo"`
	Warnings        []string       `json:"warnings,omitempty"`
	RestartRequired bool           `json:"restartRequired,omitempty"`
}

// Restore applies an extracted archive.
func Restore(ctx context.Context, ex *Extracted, opts RestoreOptions) (*RestoreResult, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if ex.Manifest.Server {
		return nil, errors.New("backup: this is a full-server backup; use RestoreServer")
	}
	res := &RestoreResult{Mode: opts.Mode, SchemaFrom: ex.Manifest.SchemaVersion, Counts: map[string]int{}}
	if ex.Manifest.HasMessenger() {
		res.Warnings = append(res.Warnings, "messenger")
	}
	switch opts.Mode {
	case Replace:
		return res, restoreReplace(ctx, ex, opts, res)
	case Merge:
		return res, restoreMerge(ctx, ex, opts, res)
	}
	return nil, fmt.Errorf("backup: unknown restore mode %q", opts.Mode)
}

func restoreReplace(ctx context.Context, ex *Extracted, opts RestoreOptions, res *RestoreResult) error {
	if opts.DataDir == "" {
		return errors.New("backup: replace needs the data directory")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return err
	}
	// Prepare the new database next to the extracted one (schema
	// migration, account selection) before touching the data directory.
	src, err := store.Open(ctx, ex.DBPath())
	if err != nil {
		return fmt.Errorf("backup: open snapshot: %w", err)
	}
	if v, err := src.SchemaVersion(ctx); err == nil {
		res.SchemaTo = v
	}
	if len(opts.Accounts) > 0 {
		if _, err := src.DB().ExecContext(ctx, `DELETE FROM accounts WHERE id NOT IN (`+placeholders(len(opts.Accounts))+`)`, idArgs(opts.Accounts)...); err != nil {
			src.Close()
			return err
		}
	}
	if opts.ConfigOnly {
		for _, q := range []string{`DELETE FROM messages`, `DELETE FROM chats`, `DELETE FROM cal_objects`, `DELETE FROM contacts`, `DELETE FROM conversations`} {
			if _, err := src.DB().ExecContext(ctx, q); err != nil {
				src.Close()
				return err
			}
		}
	}
	accs, _ := src.ListAccounts(ctx)
	res.Accounts = len(accs)
	_ = src.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&res.Messages)
	prepared := filepath.Join(ex.Dir, "prepared.db")
	if err := src.Backup(ctx, prepared); err != nil {
		src.Close()
		return err
	}
	src.Close()

	dbPath := filepath.Join(opts.DataDir, "tuxmail.db")
	if _, err := os.Stat(dbPath); err == nil {
		keep := dbPath + ".before-restore-" + opts.Now().UTC().Format("20060102-150405")
		if err := os.Rename(dbPath, keep); err != nil {
			return err
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}
	if err := moveFile(prepared, dbPath); err != nil {
		return err
	}
	bs, err := blob.New(filepath.Join(opts.DataDir, "blobs"))
	if err != nil {
		return err
	}
	n, err := copyBlobs(ex.Blobs, bs, ex.Manifest.Blobs)
	res.Blobs = n
	if err != nil {
		return err
	}
	if opts.Secrets && opts.SecretStore != nil {
		sec, err := ex.Secrets()
		if err != nil {
			return err
		}
		for k, v := range sec {
			if err := opts.SecretStore.Set(k, v); err != nil {
				res.Warnings = append(res.Warnings, "secret "+k+": "+err.Error())
				continue
			}
			res.Secrets++
		}
	}
	return nil
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyBlobs(from, to *blob.Store, hashes []string) (int, error) {
	n := 0
	for _, h := range hashes {
		if ok, _ := to.Has(h); ok {
			continue
		}
		rc, err := from.Open(h)
		if err != nil {
			return n, err
		}
		_, err = to.Put(rc)
		rc.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// restoreMerge adds the selected accounts (with identities, folders,
// messages, calendars and address books) and missing settings to the
// live store. Ids are remapped; nothing existing is overwritten.
func restoreMerge(ctx context.Context, ex *Extracted, opts RestoreOptions, res *RestoreResult) error {
	if opts.Store == nil {
		return errors.New("backup: merge needs the live store")
	}
	src, err := store.Open(ctx, ex.DBPath())
	if err != nil {
		return fmt.Errorf("backup: open snapshot: %w", err)
	}
	defer src.Close()
	if v, err := src.SchemaVersion(ctx); err == nil {
		res.SchemaTo = v
	}
	dst := opts.Store
	var secrets map[string][]byte
	if opts.Secrets {
		if secrets, err = ex.Secrets(); err != nil {
			return err
		}
	}
	// Signatures (shared by identities).
	sigMap := map[int64]int64{}
	sigs, err := src.ListSignatures(ctx)
	if err != nil {
		return err
	}
	have, _ := dst.ListSignatures(ctx)
	for _, s := range sigs {
		// Signature images live in the blob store.
		if opts.Blobs != nil && ex.Blobs != nil && len(s.Images) > 0 {
			hashes := make([]string, 0, len(s.Images))
			for _, im := range s.Images {
				if ok, _ := ex.Blobs.Has(im.BlobHash); ok {
					hashes = append(hashes, im.BlobHash)
				}
			}
			if _, err := copyBlobs(ex.Blobs, opts.Blobs, hashes); err != nil {
				return err
			}
		}
		var id int64
		for _, h := range have {
			if h.Name == s.Name && h.HTML == s.HTML && h.Plain == s.Plain {
				id = h.ID
			}
		}
		if id == 0 {
			old := s.ID
			s.ID = 0
			if id, err = dst.CreateSignature(ctx, s); err != nil {
				return err
			}
			s.ID = old
		}
		sigMap[s.ID] = id
	}
	accs, err := src.ListAccounts(ctx)
	if err != nil {
		return err
	}
	existing, _ := dst.ListAccounts(ctx)
	for _, acc := range accs {
		if len(opts.Accounts) > 0 && !slices.Contains(opts.Accounts, acc.ID) {
			continue
		}
		oldID, oldRef := acc.ID, acc.SecretRef
		acc.ID, acc.SecretRef, acc.SortOrder = 0, "", len(existing)+res.Accounts
		newID, err := dst.CreateAccount(ctx, acc)
		if err != nil {
			return err
		}
		res.Accounts++
		if v, ok := secrets[oldRef]; ok && oldRef != "" && opts.SecretStore != nil {
			ref := fmt.Sprintf("account:%d", newID)
			if err := opts.SecretStore.Set(ref, v); err == nil {
				acc.ID, acc.SecretRef = newID, ref
				if err := dst.UpdateAccount(ctx, acc); err != nil {
					return err
				}
				res.Secrets++
			}
		}
		folderMap, err := mergeFolders(ctx, src, dst, oldID, newID)
		if err != nil {
			return err
		}
		ids, err := src.ListIdentities(ctx, oldID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			sent, drafts, _ := src.IdentityFolders(ctx, id.ID)
			id.ID, id.AccountID = 0, newID
			id.DefaultSignatureID, id.ReplySignatureID = sigMap[id.DefaultSignatureID], sigMap[id.ReplySignatureID]
			id.SMTPAccountID = 0
			nid, err := dst.CreateIdentity(ctx, id)
			if err != nil {
				return err
			}
			_ = dst.SetIdentityFolders(ctx, nid, folderMap[sent], folderMap[drafts])
		}
		if !opts.ConfigOnly {
			n, err := mergeMessages(ctx, src, dst, ex.Blobs, opts, oldID, newID, folderMap)
			res.Messages += n
			if err != nil {
				return err
			}
			if err := mergePIM(ctx, src, dst, oldID, newID, res); err != nil {
				return err
			}
		}
	}
	if !opts.ConfigOnly && len(opts.Accounts) == 0 {
		if err := mergePIM(ctx, src, dst, 0, 0, res); err != nil {
			return err
		}
	}
	// Settings and workflows that do not exist yet.
	// Workflows from a backup must be confirmed again before they may run
	// external actions (webhook, send, ...): external_confirmed is reset
	// (AUDIT-C).
	for _, q := range []struct{ table, cols, sel string }{
		{"app_settings", "key, value_json, updated_at", "key, value_json, updated_at"},
		{"workflows", "id, name, enabled, yaml, external_confirmed, created_at, updated_at", "id, name, enabled, yaml, 0, created_at, updated_at"},
	} {
		rows, err := src.DB().QueryContext(ctx, `SELECT `+q.sel+` FROM `+q.table) // #nosec G202 -- table and column lists are constants above
		if err != nil {
			continue
		}
		cols := strings.Split(q.cols, ", ")
		var batch [][]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if rows.Scan(ptrs...) == nil {
				batch = append(batch, vals)
			}
		}
		rows.Close()
		for _, vals := range batch {
			r, err := dst.DB().ExecContext(ctx, `INSERT OR IGNORE INTO `+q.table+`(`+q.cols+`) VALUES (`+placeholders(len(cols))+`)`, vals...)
			if err == nil {
				if n, _ := r.RowsAffected(); n > 0 {
					res.Counts[q.table]++
				}
			}
		}
	}
	if v, ok := secrets["ai:api-key"]; ok && opts.SecretStore != nil {
		if _, err := opts.SecretStore.Get("ai:api-key"); err != nil {
			if opts.SecretStore.Set("ai:api-key", v) == nil {
				res.Secrets++
			}
		}
	}
	return nil
}

func mergeFolders(ctx context.Context, src, dst *store.Store, oldAcc, newAcc int64) (map[int64]int64, error) {
	folders, err := src.ListFolders(ctx, oldAcc)
	if err != nil {
		return nil, err
	}
	m := map[int64]int64{}
	// Parents first: repeat until all are placed.
	pending := folders
	for len(pending) > 0 {
		var next []domain.Folder
		for _, f := range pending {
			if f.ParentID != 0 && m[f.ParentID] == 0 {
				next = append(next, f)
				continue
			}
			old := f.ID
			st, _ := src.FolderSyncState(ctx, old)
			f.ID, f.AccountID, f.ParentID = 0, newAcc, m[f.ParentID]
			id, err := dst.UpsertFolder(ctx, f)
			if err != nil {
				return nil, err
			}
			m[old] = id
			if st != "" {
				_ = dst.SetFolderSyncState(ctx, id, st)
			}
		}
		if len(next) == len(pending) {
			break // broken parent chain
		}
		pending = next
	}
	return m, nil
}

func mergeMessages(ctx context.Context, src, dst *store.Store, blobs *blob.Store, opts RestoreOptions, oldAcc, newAcc int64, folderMap map[int64]int64) (int, error) {
	n := 0
	for oldFolder, newFolder := range folderMap {
		for off := 0; ; off += 500 {
			ms, err := src.ListMessagesInFolder(ctx, oldFolder, 500, off)
			if err != nil {
				return n, err
			}
			for _, msg := range ms {
				if err := ctx.Err(); err != nil {
					return n, err
				}
				locs, _ := src.Locations(ctx, msg.ID)
				addrs, _ := src.MessageAddresses(ctx, msg.ID)
				oldID := msg.ID
				msg.ID, msg.AccountID, msg.ConversationID = 0, newAcc, 0
				id, created, err := dst.UpsertMessage(ctx, msg)
				if err != nil {
					return n, err
				}
				for _, l := range locs {
					if l.FolderID == oldFolder {
						if err := dst.AddLocation(ctx, store.Location{MessageID: id, FolderID: newFolder, RemoteRef: l.RemoteRef}); err != nil {
							return n, err
						}
					}
				}
				if !created {
					continue
				}
				for i := range addrs {
					addrs[i].PersonID = 0
				}
				if err := dst.SetMessageAddresses(ctx, id, addrs); err != nil {
					return n, err
				}
				if msg.BlobHash != "" && opts.Blobs != nil {
					if _, err := copyBlobs(blobs, opts.Blobs, []string{msg.BlobHash}); err != nil {
						return n, err
					}
				}
				_ = oldID
				n++
				if opts.OnMessage != nil {
					opts.OnMessage(ctx, id)
				}
			}
			if len(ms) < 500 {
				break
			}
		}
		_, _, _ = dst.RefreshFolderCounts(ctx, newFolder)
	}
	return n, nil
}

// mergePIM copies the calendars and address books of an account (0: the
// local ones) with their objects.
func mergePIM(ctx context.Context, src, dst *store.Store, oldAcc, newAcc int64, res *RestoreResult) error {
	cals, err := src.ListAccountCalendars(ctx, oldAcc)
	if err != nil {
		return err
	}
	for _, c := range cals {
		objs, err := src.ListCalObjects(ctx, c.ID)
		if err != nil {
			return err
		}
		c.ID, c.AccountID, c.SyncToken = 0, newAcc, ""
		if newAcc == 0 {
			c.RemoteID = ""
		}
		id, err := dst.CreateCalendar(ctx, c)
		if err != nil {
			return err
		}
		for _, o := range objs {
			o.ID, o.CalendarID = 0, id
			if _, _, err := dst.UpsertCalObject(ctx, o); err != nil {
				return err
			}
			res.Counts["events"]++
		}
	}
	books, err := src.ListAccountAddressbooks(ctx, oldAcc)
	if err != nil {
		return err
	}
	for _, b := range books {
		cs, err := src.ListContacts(ctx, b.ID)
		if err != nil {
			return err
		}
		b.ID, b.AccountID, b.SyncToken = 0, newAcc, ""
		if newAcc == 0 {
			b.RemoteID = ""
		}
		id, err := dst.CreateAddressbook(ctx, b)
		if err != nil {
			return err
		}
		for _, c := range cs {
			c.ID, c.AddressbookID, c.PersonID = 0, id, 0
			if _, _, err := dst.UpsertContact(ctx, c); err != nil {
				return err
			}
			res.Counts["contacts"]++
		}
	}
	return nil
}
