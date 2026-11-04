// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// ServerOptions configure CreateServer.
type ServerOptions struct {
	// DataDir is the server's data_dir (server.db, master.key, vapid.json,
	// users/<id>/...).
	DataDir    string
	Passphrase string
	AppVersion string
	TempDir    string
	Now        func() time.Time
}

// ServerWarning is printed by the CLI: the archive contains master.key,
// which decrypts every user's stored credentials.
const ServerWarning = "the archive contains master.key, which decrypts all stored user credentials: keep it as safe as the server itself"

// CreateServer writes an admin full-server backup: every SQLite database
// (snapshotted with VACUUM INTO) and every other file of the data
// directory. A passphrase is mandatory.
func CreateServer(ctx context.Context, w io.Writer, opts ServerOptions) (*Manifest, error) {
	if opts.Passphrase == "" {
		return nil, fmt.Errorf("%w: server backups contain master.key", ErrNeedPassphrase)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tmp, err := os.MkdirTemp(opts.TempDir, "tuxmail-server-backup-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	type item struct{ name, src string }
	var items []item
	err = filepath.WalkDir(opts.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(opts.DataDir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), "tuxmail-") || d.Name() == "tmp" && strings.HasSuffix(path.Dir(rel), "blobs")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") || strings.HasSuffix(rel, "-journal") ||
			strings.Contains(rel, ".before-restore-") {
			return nil
		}
		if strings.HasSuffix(rel, ".db") || strings.HasSuffix(rel, ".sqlite") {
			snap := filepath.Join(tmp, fmt.Sprintf("db-%d", len(items)))
			if err := vacuumInto(ctx, p, snap); err != nil {
				return fmt.Errorf("backup: snapshot %s: %w", rel, err)
			}
			items = append(items, item{name: serverPrefix + rel, src: snap})
			return nil
		}
		items = append(items, item{name: serverPrefix + rel, src: p})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	m := &Manifest{Format: FormatVersion, ID: newID(), Created: opts.Now().UTC(), AppVersion: opts.AppVersion,
		Scope: Scope{Mode: ModeFull, Secrets: true}, Encrypted: true, Secrets: true, Server: true, Warnings: []string{"master.key"}}
	for _, it := range items {
		sum, size, err := fileSum(it.src)
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, FileEntry{Path: it.name, Size: size, SHA256: sum})
	}
	aw, err := newWriter(w, opts.Passphrase)
	if err != nil {
		return nil, err
	}
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := aw.bytes(manifestName, m.Created, mj); err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := copyFileInto(aw, it.name, it.src, m.Created); err != nil {
			return nil, err
		}
	}
	return m, aw.close()
}

func vacuumInto(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// RestoreServer unpacks a full-server backup into an empty data directory
// (checksums verified before anything is written to dataDir).
func RestoreServer(ctx context.Context, src Source, passphrase, dataDir string) (*Manifest, error) {
	if ents, err := os.ReadDir(dataDir); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("backup: %s is not empty; restore a server backup into an empty data directory", dataDir)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(filepath.Clean(dataDir)), ".tuxmail-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
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
	if !m.Server {
		return nil, errors.New("backup: not a full-server backup")
	}
	sums := map[string]FileEntry{}
	for _, f := range m.Files {
		sums[f.Path] = f
	}
	for {
		h, err := ar.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := path.Clean(h.Name)
		fe, ok := sums[name]
		if !ok || !strings.HasPrefix(name, serverPrefix) || strings.Contains(name, "..") || strings.Contains(name, `\`) ||
			(h.Typeflag != tar.TypeReg && h.Typeflag != 0) || !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("%w: unexpected entry %q", ErrTampered, h.Name)
		}
		dst := filepath.Join(tmp, filepath.FromSlash(strings.TrimPrefix(name, serverPrefix)))
		if err := writeEntry(dst, ar, h.Size); err != nil {
			return nil, err
		}
		f, err := os.Open(dst)
		if err != nil {
			return nil, err
		}
		hs := sha256.New()
		_, err = io.Copy(hs, f)
		f.Close()
		if err != nil || hex.EncodeToString(hs.Sum(nil)) != fe.SHA256 {
			return nil, fmt.Errorf("%w: checksum of %s does not match", ErrTampered, name)
		}
		delete(sums, name)
	}
	if len(sums) > 0 {
		return nil, fmt.Errorf("%w: %d files are missing", ErrTampered, len(sums))
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(dataDir)), 0o700); err != nil {
		return nil, err
	}
	os.Remove(dataDir) // empty
	if err := os.Rename(tmp, dataDir); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Join(dataDir, "master.key"), 0o600)
	return m, nil
}

// ==== staged restore ===========================================================

// PendingDir is the directory below a data directory that holds a
// database staged by an in-app "replace" restore; it is swapped in by
// ApplyPending before the store is opened.
const PendingDir = "restore-pending"

// Stage prepares a replace restore of a running application: the blobs
// and secrets are restored right away (they are content-addressed or keyed
// and harmless), the database is staged for ApplyPending.
func Stage(ctx context.Context, ex *Extracted, opts RestoreOptions) (*RestoreResult, error) {
	if opts.DataDir == "" {
		return nil, errors.New("backup: stage needs the data directory")
	}
	pending := filepath.Join(opts.DataDir, PendingDir)
	os.RemoveAll(pending)
	o := opts
	o.DataDir = pending
	res, err := Restore(ctx, ex, o)
	if err != nil {
		os.RemoveAll(pending)
		return nil, err
	}
	if opts.Blobs != nil {
		// Move the restored blobs into the live store now.
		if bs, err := openBlobs(filepath.Join(pending, "blobs")); err == nil {
			if _, err := copyBlobs(bs, opts.Blobs, ex.Manifest.Blobs); err != nil {
				return nil, err
			}
		}
		os.RemoveAll(filepath.Join(pending, "blobs"))
	}
	res.RestartRequired = true
	return res, nil
}

// ApplyPending swaps in a staged database (call before opening the store).
// It reports whether a restore was applied.
func ApplyPending(dataDir string) (bool, error) {
	pending := filepath.Join(dataDir, PendingDir)
	db := filepath.Join(pending, "tuxmail.db")
	if _, err := os.Stat(db); err != nil {
		return false, nil
	}
	dst := filepath.Join(dataDir, "tuxmail.db")
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, dst+".before-restore-"+time.Now().UTC().Format("20060102-150405")); err != nil {
			return false, err
		}
	}
	for _, s := range []string{"-wal", "-shm"} {
		os.Remove(dst + s)
	}
	if err := os.Rename(db, dst); err != nil {
		return false, err
	}
	if bs, err := openBlobs(filepath.Join(pending, "blobs")); err == nil {
		if live, err := openBlobs(filepath.Join(dataDir, "blobs")); err == nil {
			var hashes []string
			_ = bs.Walk(func(h string) error { hashes = append(hashes, h); return nil })
			if _, err := copyBlobs(bs, live, hashes); err != nil {
				return true, err
			}
		}
	}
	return true, os.RemoveAll(pending)
}

func openBlobs(dir string) (*blob.Store, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	return blob.New(dir)
}
