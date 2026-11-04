// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Shortcut is a .lnk file.
type Shortcut struct {
	Target      string
	Args        string
	WorkDir     string
	Icon        string // "path,index"
	Description string
}

// Linker creates shortcuts (IShellLink / WScript.Shell on Windows).
type Linker interface {
	CreateShortcut(link string, s Shortcut) error
}

// Folders are the shell folders of a scope.
type Folders struct {
	StartMenu string // ...\Start Menu\Programs (per user or common)
	Desktop   string // user or public desktop
}

// Installer installs, updates and removes TuxMail. All OS access goes
// through its fields; see the *_windows.go files for the real ones.
type Installer struct {
	Reg     Registry
	Links   Linker
	Folders func(Scope) (Folders, error)
	Payload *Payload
	Version string
	// SetupPath is the running setup program; it is copied into the
	// installation as TuxMail-Setup.exe (uninstall, modify, repair).
	SetupPath   string
	Description string
	Now         func() time.Time
	Logf        func(format string, args ...any)
	// Protect replaces the ACL of an all-users installation folder outside
	// Program Files (ProtectInstallDir on Windows; AUDIT-D D-05). Env
	// resolves the Program Files folders for NeedsProtectedACL.
	Protect func(dir string) error
	Env     Env
}

// Request is an installation request.
type Request struct {
	Scope   Scope   `json:"scope"`
	Dir     string  `json:"dir"`
	Options Options `json:"options"`
}

// Step names a phase of the progress page (translation keys of the UI).
type Step string

const (
	StepPrepare   Step = "prepare"
	StepClose     Step = "close"
	StepWebView2  Step = "webview2"
	StepFiles     Step = "files"
	StepShortcuts Step = "shortcuts"
	StepRegister  Step = "register"
	StepRemove    Step = "remove"
	StepData      Step = "data"
	StepFinish    Step = "finish"
)

// Progress is reported to the UI (and to the progress file of an
// elevated child process).
type Progress struct {
	Step    Step    `json:"step"`
	Percent float64 `json:"percent"`
	File    string  `json:"file,omitempty"`
	Done    bool    `json:"done,omitempty"`
	Error   string  `json:"error,omitempty"`
}

func (in *Installer) logf(format string, args ...any) {
	if in.Logf != nil {
		in.Logf(format, args...)
	}
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

// LinkName is the file name of the shortcuts.
const LinkName = AppName + ".lnk"

// Install installs or updates TuxMail.
func (in *Installer) Install(ctx context.Context, req Request, report func(Progress)) (err error) {
	if report == nil {
		report = func(Progress) {}
	}
	if in.Payload == nil {
		return ErrNoPayload
	}
	switch req.Scope {
	case ScopeUser, ScopeMachine, ScopePortable:
	default:
		return fmt.Errorf("unknown scope %q", req.Scope)
	}
	dir := filepath.Clean(req.Dir)
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("destination %q is not an absolute path", req.Dir)
	}
	in.logf("install %s scope=%s dir=%s options=%s", in.Version, req.Scope, dir, req.Options)
	report(Progress{Step: StepPrepare, Percent: 1})

	_, statErr := os.Stat(dir)
	fresh := errors.Is(statErr, os.ErrNotExist)
	hadApp := StatDir(dir).HasApp
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if in.Protect != nil && in.Env != nil && NeedsProtectedACL(req.Scope, dir, in.Env) {
		// Before any file is written, so every file inherits the ACL.
		if err := in.Protect(dir); err != nil {
			return fmt.Errorf("protect installation folder: %w", err)
		}
	}
	old, _ := ReadRecord(dir)
	existing, hadEntry := FindExisting(DetectExisting(in.Reg), req.Scope)
	files := in.Payload.Files()
	defer func() {
		if err != nil && !hadApp {
			// Roll back a failed fresh installation.
			RemoveFiles(dir, files)
			_ = os.Remove(filepath.Join(dir, SetupExe))
			if fresh {
				_ = os.Remove(dir)
			}
		}
	}()

	const filesFrom, filesTo = 3.0, 84.0
	if err := in.Payload.Extract(ctx, dir, func(done, total int64, name string) {
		pct := filesFrom
		if total > 0 {
			pct += (filesTo - filesFrom) * float64(done) / float64(total)
		}
		report(Progress{Step: StepFiles, Percent: pct, File: name})
	}); err != nil {
		return err
	}

	extra := int64(0)
	recorded := append([]string(nil), files...)
	if req.Scope == ScopePortable {
		if err := touch(filepath.Join(dir, PortableMark)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, PortableData), 0o755); err != nil {
			return err
		}
	} else if in.SetupPath != "" {
		n, err := copySelf(in.SetupPath, filepath.Join(dir, SetupExe))
		if err != nil {
			return fmt.Errorf("copy setup program: %w", err)
		}
		extra = n
		recorded = append(recorded, SetupExe)
	}

	// Files of the previous version that are gone now (e.g. a dropped plugin).
	if stale := Stale(old, recorded); len(stale) > 0 {
		in.logf("removing stale files %v", stale)
		RemoveFiles(dir, stale)
	}
	// Classic NSIS installation in the same folder: drop its uninstaller.
	if old == nil && hadEntry && existing.Legacy && WinSame(existing.Dir, dir) {
		_ = os.Remove(filepath.Join(dir, legacyUninst))
	}

	if req.Scope != ScopePortable {
		report(Progress{Step: StepShortcuts, Percent: 87})
		if err := in.shortcuts(req, dir); err != nil {
			return err
		}
		report(Progress{Step: StepRegister, Percent: 93})
		ops := InstallOps(EntryInfo{
			Scope: req.Scope, Dir: dir, Version: in.Version, SizeKB: in.Payload.SizeKB(extra),
			Date: in.now().Format("20060102"), Options: req.Options, Description: in.Description,
			PathJoin: filepath.Join,
		})
		if err := Apply(in.Reg, ops); err != nil {
			return err
		}
	}

	if err := WriteRecord(dir, &Record{Version: in.Version, Scope: req.Scope, Options: req.Options,
		Installed: in.now().UTC().Truncate(time.Second), Files: recorded}); err != nil {
		return err
	}
	CleanupOld(dir)
	report(Progress{Step: StepFinish, Percent: 100})
	in.logf("installed %s", in.Version)
	return nil
}

func (in *Installer) shortcuts(req Request, dir string) error {
	if in.Folders == nil || in.Links == nil {
		return nil
	}
	f, err := in.Folders(req.Scope)
	if err != nil {
		return err
	}
	exe := filepath.Join(dir, AppExe)
	sc := Shortcut{Target: exe, WorkDir: dir, Icon: exe + ",0", Description: in.Description}
	for _, l := range []struct {
		folder string
		want   bool
	}{{f.StartMenu, req.Options.StartMenu}, {f.Desktop, req.Options.Desktop}} {
		if l.folder == "" {
			continue
		}
		link := filepath.Join(l.folder, LinkName)
		if !l.want {
			_ = os.Remove(link)
			continue
		}
		if err := os.MkdirAll(l.folder, 0o755); err != nil {
			return err
		}
		if err := in.Links.CreateShortcut(link, sc); err != nil {
			return fmt.Errorf("shortcut %s: %w", link, err)
		}
	}
	return nil
}

// UninstallRequest describes what to remove.
type UninstallRequest struct {
	Scope Scope  `json:"scope"`
	Dir   string `json:"dir"`
	// KeepData keeps %APPDATA%\TuxMail (accounts, mail cache, settings).
	KeepData bool   `json:"keepData"`
	DataDir  string `json:"-"`
}

// Uninstall removes an installation. It returns the files that could not
// be deleted (e.g. the running setup copy; the caller schedules those).
func (in *Installer) Uninstall(ctx context.Context, req UninstallRequest, report func(Progress)) ([]string, error) {
	if report == nil {
		report = func(Progress) {}
	}
	dir := filepath.Clean(req.Dir)
	if !filepath.IsAbs(dir) || WinIsRoot(dir) || dir == filepath.Dir(dir) {
		return nil, fmt.Errorf("refusing to uninstall from %q", req.Dir)
	}
	in.logf("uninstall scope=%s dir=%s keepData=%v", req.Scope, dir, req.KeepData)
	report(Progress{Step: StepRemove, Percent: 5})

	if req.Scope != ScopePortable && in.Folders != nil {
		if f, err := in.Folders(req.Scope); err == nil {
			for _, folder := range []string{f.StartMenu, f.Desktop} {
				if folder != "" {
					_ = os.Remove(filepath.Join(folder, LinkName))
				}
			}
		}
	}
	report(Progress{Step: StepRegister, Percent: 20})
	if err := Apply(in.Reg, UninstallOps(req.Scope)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	report(Progress{Step: StepFiles, Percent: 40})
	var files []string
	if rec, err := ReadRecord(dir); err == nil {
		files = rec.Files
	} else {
		files = append(files, legacyFiles...)
		plugins, _ := filepath.Glob(filepath.Join(dir, PluginsSubdir, "tuxmail-plugin-*"))
		for _, p := range plugins {
			files = append(files, PluginsSubdir+"/"+filepath.Base(p))
		}
	}
	failed := RemoveFiles(dir, files)
	_ = os.Remove(filepath.Join(dir, RecordFile))
	CleanupOld(dir)
	_ = os.Remove(dir) // only when empty

	if !req.KeepData && req.DataDir != "" {
		report(Progress{Step: StepData, Percent: 75})
		if err := removeDataDir(req.DataDir); err != nil {
			return failed, err
		}
	}
	report(Progress{Step: StepFinish, Percent: 100})
	return failed, nil
}

// removeDataDir deletes the TuxMail data folder; it refuses anything that
// is not called TuxMail.
func removeDataDir(dir string) error {
	dir = filepath.Clean(dir)
	if !strings.EqualFold(filepath.Base(dir), AppName) || !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing to delete data folder %q", dir)
	}
	return os.RemoveAll(dir)
}

func touch(p string) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// copySelf copies the setup program into the installation (unless it runs
// from there already) and returns its size.
func copySelf(src, dst string) (int64, error) {
	si, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if di, err := os.Stat(dst); err == nil && os.SameFile(si, di) {
		return si.Size(), nil
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + newSuffix
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := replaceFile(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}
