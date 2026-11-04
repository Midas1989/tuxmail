// SPDX-License-Identifier: AGPL-3.0-or-later

package signalcli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed licenses/GPL-3.0.txt
var gpl3 string

// ErrChecksum: a download does not match its pinned SHA-256.
var ErrChecksum = errors.New("signal-cli: checksum mismatch")

// Limits of the installer.
const (
	maxDownload = 512 << 20 // per file
	maxUnpacked = 1 << 30   // per archive
)

// Progress of an install.
type Progress struct {
	Stage string // download | verify | unpack | done
	File  string
	Done  int64 // bytes downloaded of the current file
	Total int64 // its size (0 = unknown)
	// Step/Steps count the files of the plan (1-based).
	Step, Steps int
}

// Installer downloads, verifies and unpacks a Plan.
type Installer struct {
	// Client defaults to an HTTP client that refuses non-HTTPS redirects.
	Client *http.Client
	// CacheDir keeps verified downloads (build tool); empty = no cache.
	CacheDir string
	// Progress is called during the install (may be nil).
	Progress func(Progress)
	// AllowHTTP permits http:// URLs (tests with httptest only).
	AllowHTTP bool
}

func (in *Installer) progress(p Progress) {
	if in.Progress != nil {
		in.Progress(p)
	}
}

func (in *Installer) client() *http.Client {
	if in.Client != nil {
		return in.Client
	}
	return &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && !in.AllowHTTP {
			return fmt.Errorf("refusing redirect to %s (not HTTPS)", req.URL.Redacted())
		}
		return nil
	}}
}

// Install downloads and verifies every file of the plan and replaces dest
// with the assembled signal-cli directory. Nothing is unpacked (let alone
// executed) before all checksums match; dest is replaced atomically (a
// rename), so a failed install leaves an existing dest untouched.
func (in *Installer) Install(ctx context.Context, plan Plan, dest string) error {
	if err := plan.Pinned(); err != nil {
		return err
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, ".signal-cli-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	files := make([]string, len(plan.Downloads))
	for i, d := range plan.Downloads {
		f, err := in.Fetch(ctx, d, work, i+1, len(plan.Downloads))
		if err != nil {
			return err
		}
		files[i] = f
	}

	stage := filepath.Join(work, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	for i, d := range plan.Downloads {
		in.progress(Progress{Stage: "unpack", File: d.Name, Step: i + 1, Steps: len(plan.Downloads)})
		if err := unpack(files[i], d, stage); err != nil {
			return fmt.Errorf("signal-cli: unpack %s: %w", d.Name, err)
		}
	}
	if err := writeMeta(stage, plan); err != nil {
		return err
	}
	// Sanity check of the layout before it goes live.
	if _, ok, err := (&Options{FS: osFS{}, GOOS: plan.OS, LookPath: noLookPath, Getenv: func(string) string { return "" }, hostPaths: true}).fromDir(stage); err != nil || !ok {
		if err == nil {
			err = ErrNotFound
		}
		return fmt.Errorf("signal-cli: unpacked files are incomplete: %w", err)
	}
	if err := replaceDir(stage, dest, work); err != nil {
		return err
	}
	in.progress(Progress{Stage: "done", Step: len(plan.Downloads), Steps: len(plan.Downloads)})
	return nil
}

func noLookPath(string) (string, error) { return "", errors.New("disabled") }

// Fetch downloads d into dir (or takes it from the cache) and verifies its
// SHA-256. It returns the path of the verified file.
func (in *Installer) Fetch(ctx context.Context, d Download, dir string, step, steps int) (string, error) {
	if d.SHA256 == "" {
		return "", fmt.Errorf("%w: %s", ErrNotPinned, d.Name)
	}
	if d.Name == "" || strings.ContainsAny(d.Name, `/\`) || d.Name == "." || d.Name == ".." {
		return "", fmt.Errorf("signal-cli: bad file name %q", d.Name)
	}
	u, err := url.Parse(d.URL)
	if err != nil || (u.Scheme != "https" && !(in.AllowHTTP && u.Scheme == "http")) || u.Host == "" {
		return "", fmt.Errorf("signal-cli: refusing download URL %q (HTTPS only)", d.URL)
	}
	if in.CacheDir != "" {
		cached := filepath.Join(in.CacheDir, d.Name)
		if sum, err := fileSHA256(cached); err == nil && sum == d.SHA256 {
			return cached, nil
		}
	}
	out := filepath.Join(dir, d.Name)
	if err := in.download(ctx, d, out, step, steps); err != nil {
		_ = os.Remove(out)
		return "", err
	}
	if in.CacheDir != "" {
		if err := os.MkdirAll(in.CacheDir, 0o755); err == nil {
			cached := filepath.Join(in.CacheDir, d.Name)
			if copyFile(out, cached) == nil {
				return cached, nil
			}
		}
	}
	return out, nil
}

func (in *Installer) download(ctx context.Context, d Download, out string, step, steps int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "TuxMail signal-cli installer")
	resp, err := in.client().Do(req)
	if err != nil {
		return fmt.Errorf("signal-cli: download %s: %w", d.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("signal-cli: download %s: HTTP %s", d.Name, resp.Status)
	}
	if resp.ContentLength > maxDownload {
		return fmt.Errorf("signal-cli: download %s too large (%d bytes)", d.Name, resp.ContentLength)
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	p := Progress{Stage: "download", File: d.Name, Total: max(resp.ContentLength, 0), Step: step, Steps: steps}
	in.progress(p)
	last := time.Now()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			n += int64(k)
			if n > maxDownload {
				f.Close()
				return fmt.Errorf("signal-cli: download %s too large", d.Name)
			}
			h.Write(buf[:k])
			if _, err := f.Write(buf[:k]); err != nil {
				f.Close()
				return err
			}
			if time.Since(last) > 250*time.Millisecond {
				p.Done, last = n, time.Now()
				in.progress(p)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("signal-cli: download %s: %w", d.Name, rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	p.Stage, p.Done = "verify", n
	in.progress(p)
	if sum := hex.EncodeToString(h.Sum(nil)); sum != d.SHA256 {
		return fmt.Errorf("%w for %s: got %s, want %s", ErrChecksum, d.Name, sum, d.SHA256)
	}
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ==== unpacking ================================================================

// unpack extracts a verified archive into stage: the native build as
// stage/signal-cli, the JVM build into stage (bin/, lib/), a JRE into
// stage/jre. The top-level directory of the archives is stripped.
func unpack(file string, d Download, stage string) error {
	var target string
	strip := 1
	switch d.Kind {
	case KindNative:
		return unpackNative(file, stage)
	case KindJVM:
		target = stage
	case KindJRE:
		target = filepath.Join(stage, "jre")
	default:
		return fmt.Errorf("unknown kind %q", d.Kind)
	}
	if strings.HasSuffix(strings.ToLower(d.Name), ".zip") {
		return unzip(file, target, strip)
	}
	return untar(file, target, strip, nil)
}

// unpackNative takes the signal-cli executable out of the native tarball
// (at the top level or up to two directories deep).
func unpackNative(file, stage string) error {
	found := false
	err := untar(file, stage, 0, func(name string, h *tar.Header) (string, bool) {
		if h.Typeflag == tar.TypeReg && path.Base(name) == "signal-cli" && strings.Count(name, "/") <= 2 && !found {
			found = true
			return "signal-cli", true
		}
		return "", false
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no signal-cli executable in the archive")
	}
	return os.Chmod(filepath.Join(stage, "signal-cli"), 0o755)
}

// safeRel cleans an archive entry name and strips n leading components;
// ok is false for entries to skip (the stripped prefix itself) and err is
// set for unsafe names.
func safeRel(name string, n int) (string, bool, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || (len(name) > 1 && name[1] == ':') {
		return "", false, fmt.Errorf("unsafe entry %q", name)
	}
	c := path.Clean(name)
	for _, part := range strings.Split(c, "/") {
		if part == ".." {
			return "", false, fmt.Errorf("unsafe entry %q", name)
		}
	}
	parts := strings.Split(c, "/")
	if len(parts) <= n {
		return "", false, nil
	}
	return strings.Join(parts[n:], "/"), true, nil
}

// within reports whether rel (slash separated, relative to its archive
// root) stays inside the root after following a symlink from dir.
func within(dir, target string) bool {
	if path.IsAbs(target) || strings.HasPrefix(target, "/") {
		return false
	}
	j := path.Clean(path.Join(dir, target))
	return j != ".." && !strings.HasPrefix(j, "../")
}

type tarFilter func(name string, h *tar.Header) (string, bool)

func untar(file, target string, strip int, filter tarFilter) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, ok, err := safeRel(h.Name, strip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if filter != nil {
			if rel, ok = filter(rel, h); !ok {
				continue
			}
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxUnpacked {
				return errors.New("archive too large")
			}
			if err := writeFile(dst, tr, h.FileInfo().Mode().Perm(), h.Size); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// JREs link legal notices between modules; only links that stay
			// inside the archive are kept.
			if !within(path.Dir(rel), h.Linkname) || strings.Contains(h.Linkname, `\`) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(filepath.FromSlash(h.Linkname), dst); err != nil {
				return err
			}
		default:
			// hard links, devices, fifos: not needed
		}
	}
}

func unzip(file, target string, strip int) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, zf := range zr.File {
		rel, ok, err := safeRel(zf.Name, strip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if strings.HasSuffix(zf.Name, "/") || zf.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			continue
		}
		total += int64(zf.UncompressedSize64)
		if total > maxUnpacked || zf.UncompressedSize64 > maxUnpacked {
			return errors.New("archive too large")
		}
		mode := zf.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if strings.HasPrefix(rel, "bin/") {
			mode |= 0o111
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeFile(dst, rc, mode, int64(zf.UncompressedSize64))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(dst string, r io.Reader, mode os.FileMode, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%s: size mismatch", filepath.Base(dst))
	}
	return err
}

// writeMeta writes VERSION, the GPL-3.0 text of signal-cli and NOTICE.txt.
func writeMeta(stage string, plan Plan) error {
	var b strings.Builder
	fmt.Fprintf(&b, "signal-cli %s for TuxMail (%s/%s)\n\n", plan.Version, plan.OS, plan.Arch)
	b.WriteString("This directory contains third-party programs that TuxMail starts as separate\n")
	b.WriteString("processes. They are not part of TuxMail and keep their own licenses.\n\n")
	b.WriteString("signal-cli - Copyright AsamK and contributors - GPL-3.0-or-later\n")
	b.WriteString("  Source: https://github.com/AsamK/signal-cli (license: LICENSE-signal-cli.txt)\n")
	b.WriteString("  It includes libsignal-client (Signal Messenger, AGPL-3.0-only) and other\n")
	b.WriteString("  libraries listed in its distribution.\n")
	for _, d := range plan.Downloads {
		if d.Kind == KindJRE {
			fmt.Fprintf(&b, "\nEclipse Temurin JRE %s (jre/) - GPL-2.0 WITH Classpath-exception-2.0\n", d.Version)
			b.WriteString("  Source: https://adoptium.net, https://github.com/adoptium (license and notices: jre/legal/, jre/NOTICE)\n")
		}
	}
	b.WriteString("\nDownloads (SHA-256 verified):\n")
	for _, d := range plan.Downloads {
		fmt.Fprintf(&b, "  %s\n    sha256 %s\n", d.URL, d.SHA256)
	}
	if plan.Note != "" {
		fmt.Fprintf(&b, "\nNote: %s\n", plan.Note)
	}
	text := b.String()
	gpl := gpl3
	if plan.OS == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
		gpl = strings.ReplaceAll(strings.ReplaceAll(gpl, "\r\n", "\n"), "\n", "\r\n")
	}
	for name, content := range map[string]string{"VERSION": plan.Version + "\n", "NOTICE.txt": text, "LICENSE-signal-cli.txt": gpl} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// replaceDir moves stage to dest; an existing dest is moved aside into
// work first and deleted with it.
func replaceDir(stage, dest, work string) error {
	if _, err := os.Lstat(dest); err == nil {
		old := filepath.Join(work, "old-"+randHex())
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("signal-cli: replace %s (still running?): %w", dest, err)
		}
		if err := os.Rename(stage, dest); err != nil {
			_ = os.Rename(old, dest)
			return err
		}
		return nil
	}
	return os.Rename(stage, dest)
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
