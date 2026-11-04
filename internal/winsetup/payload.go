// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Payload is the application archive embedded into the setup program
// (build/windows/package-setup.sh, WritePayload): a zip (zstd or deflate)
// with TuxMail.exe, plugins\ and the license files at its root.
type Payload struct {
	files []*zip.File
}

// ErrNoPayload: the setup was built without application files.
var ErrNoPayload = errors.New("this setup program contains no application files (development build)")

// OpenPayload reads a payload zip. It rejects unsafe names and archives
// without TuxMail.exe.
func OpenPayload(data []byte) (*Payload, error) {
	if len(data) == 0 {
		return nil, ErrNoPayload
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	registerZstd(zr)
	p := &Payload{}
	hasApp := false
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue // directory entry
		}
		name, err := safeName(f.Name)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(name, AppExe) {
			hasApp = true
		}
		if strings.EqualFold(name, RecordFile) || strings.EqualFold(name, SetupExe) {
			return nil, fmt.Errorf("payload: reserved file name %q", name)
		}
		p.files = append(p.files, f)
	}
	if !hasApp {
		return nil, fmt.Errorf("payload: %s missing", AppExe)
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].Name < p.files[j].Name })
	return p, nil
}

// safeName validates a zip entry name: relative, no "..", no drive or
// stream syntax; returns it cleaned with "/" separators.
func safeName(n string) (string, error) {
	bad := func() (string, error) { return "", fmt.Errorf("payload: unsafe file name %q", n) }
	if n == "" || strings.ContainsAny(n, `\:*?"<>|`) || strings.ContainsRune(n, 0) {
		return bad()
	}
	if strings.HasPrefix(n, "/") {
		return bad()
	}
	c := path.Clean(n)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || c != n {
		return bad()
	}
	return c, nil
}

// Files returns the file names ("/" separated), sorted.
func (p *Payload) Files() []string {
	out := make([]string, len(p.files))
	for i, f := range p.files {
		out[i] = f.Name
	}
	return out
}

// Size is the uncompressed size in bytes.
func (p *Payload) Size() int64 {
	var n int64
	for _, f := range p.files {
		n += int64(f.UncompressedSize64)
	}
	return n
}

// SizeKB is the size for the EstimatedSize registry value.
func (p *Payload) SizeKB(extra int64) uint32 {
	return uint32((p.Size() + extra + 1023) / 1024)
}

// ExtractProgress is called after each chunk.
type ExtractProgress func(done, total int64, name string)

// Extract writes the payload into dest. Each file is written next to its
// target as "<name>.tuxmail-new" and then renamed over it, so a failed
// update never leaves a half-written TuxMail.exe. A target that cannot be
// replaced (still running) is moved aside to "<name>.tuxmail-old" first;
// CleanupOld removes those later.
func (p *Payload) Extract(ctx context.Context, dest string, report ExtractProgress) error {
	total := p.Size()
	var done int64
	buf := make([]byte, 256<<10)
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		tmp := target + newSuffix
		if err := writeEntry(ctx, f, tmp, buf, func(n int64) {
			done += n
			if report != nil {
				report(done, total, f.Name)
			}
		}); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		if err := replaceFile(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return nil
}

func writeEntry(ctx context.Context, f *zip.File, dst string, buf []byte, progress func(int64)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := rc.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			progress(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if !f.Modified.IsZero() {
		_ = os.Chtimes(dst, time.Now(), f.Modified)
	}
	return nil
}

// replaceFile renames src over dst; if dst is locked it is moved aside.
func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if _, serr := os.Stat(dst); serr != nil {
		return err
	}
	// Windows: a running executable cannot be replaced but can be renamed.
	old := dst + oldSuffix
	_ = os.Remove(old)
	if rerr := os.Rename(dst, old); rerr != nil {
		return fmt.Errorf("%w (file in use?)", err)
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return nil
}

// CleanupOld removes "*.tuxmail-old" and "*.tuxmail-new" leftovers below dir.
func CleanupOld(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && d.Name() == PortableData {
			return filepath.SkipDir // never touch user data
		}
		if !d.IsDir() && (strings.HasSuffix(d.Name(), oldSuffix) || strings.HasSuffix(d.Name(), newSuffix)) {
			_ = os.Remove(p)
		}
		return nil
	})
}

// Record is the list of installed files (tuxmail-install.json in the
// installation folder). Uninstall and updates remove exactly these files,
// so an installation into a non-empty folder never deletes foreign files.
type Record struct {
	Version   string    `json:"version"`
	Scope     Scope     `json:"scope"`
	Options   Options   `json:"options"`
	Installed time.Time `json:"installed"`
	Files     []string  `json:"files"`
}

// ReadRecord reads the install record of dir.
func ReadRecord(dir string) (*Record, error) {
	b, err := os.ReadFile(filepath.Join(dir, RecordFile))
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", RecordFile, err)
	}
	clean := r.Files[:0]
	for _, f := range r.Files {
		if n, err := safeName(f); err == nil {
			clean = append(clean, n)
		}
	}
	r.Files = clean
	return &r, nil
}

// WriteRecord writes the install record of dir.
func WriteRecord(dir string, r *Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, RecordFile+newSuffix)
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return replaceFile(tmp, filepath.Join(dir, RecordFile))
}

// legacyFiles are removed when no install record exists (classic NSIS
// installation).
var legacyFiles = []string{AppExe, "LICENSE.txt", "NOTICE.txt", legacyUninst, SetupExe}

// RemoveFiles deletes files (relative, "/" separated) below dir and then
// all directories that became empty, deepest first; dir itself is kept.
// It returns the files that could not be deleted.
func RemoveFiles(dir string, files []string) []string {
	var failed []string
	dirs := map[string]bool{}
	for _, f := range files {
		n, err := safeName(f)
		if err != nil {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(n))
		for _, q := range []string{p + newSuffix, p + oldSuffix} {
			_ = os.Remove(q)
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, n)
		}
		for d := path.Dir(n); d != "." && d != "/"; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return strings.Count(list[i], "/") > strings.Count(list[j], "/") })
	for _, d := range list {
		_ = os.Remove(filepath.Join(dir, filepath.FromSlash(d))) // fails when not empty: fine
	}
	return failed
}

// Stale returns the files of an old record that the new payload no longer
// contains (e.g. a removed plugin).
func Stale(old *Record, newFiles []string) []string {
	if old == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, f := range newFiles {
		keep[strings.ToLower(f)] = true
	}
	var out []string
	for _, f := range old.Files {
		if !keep[strings.ToLower(f)] {
			out = append(out, f)
		}
	}
	return out
}

// StatDir inspects a destination folder for CheckDir.
func StatDir(dir string) DirState {
	st, err := os.Stat(dir)
	if err != nil {
		return DirState{}
	}
	s := DirState{Exists: true, IsDir: st.IsDir()}
	if !s.IsDir {
		return s
	}
	entries, _ := os.ReadDir(dir)
	s.Empty = len(entries) == 0
	for _, e := range entries {
		switch strings.ToLower(e.Name()) {
		case strings.ToLower(AppExe), RecordFile:
			s.HasApp = true
		case PortableMark:
			s.Portable = true
		}
	}
	return s
}
