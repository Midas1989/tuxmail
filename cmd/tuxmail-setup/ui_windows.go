// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wwin "github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Midas1989/Mailclient/internal/winsetup"
)

//go:embed ui
var uiFS embed.FS

// Setup is bound to the window: window.go.main.Setup.<Method>.
type Setup struct {
	a   *app
	ctx context.Context

	mu        sync.Mutex
	busy      bool
	installed winsetup.Request
}

// ExistingInfo is an installation with its upgrade classification.
type ExistingInfo struct {
	winsetup.Existing
	Kind winsetup.UpgradeKind `json:"kind"`
}

// State is everything the UI needs at start.
type State struct {
	Mode         winsetup.Mode             `json:"mode"`
	Version      string                    `json:"version"`
	Arch         string                    `json:"arch"`
	Lang         string                    `json:"lang"`
	Dark         bool                      `json:"dark"`
	Mica         bool                      `json:"mica"`
	Elevated     bool                      `json:"elevated"`
	WindowsBuild uint32                    `json:"windowsBuild"`
	WebView2     string                    `json:"webview2"`
	Payload      string                    `json:"payloadError,omitempty"`
	SizeBytes    int64                     `json:"sizeBytes"`
	Existing     []ExistingInfo            `json:"existing"`
	Dirs         map[winsetup.Scope]string `json:"dirs"`
	DataDir      string                    `json:"dataDir"`
	Preset       *winsetup.Request         `json:"preset,omitempty"`
	Uninstall    *UninstallInfo            `json:"uninstall,omitempty"`
}

// UninstallInfo describes the installation the uninstaller removes.
type UninstallInfo struct {
	Scope   winsetup.Scope `json:"scope"`
	Dir     string         `json:"dir"`
	Version string         `json:"version"`
}

func (a *app) runUI() int {
	s := &Setup{a: a}
	assets, _ := fs.Sub(uiFS, "ui")
	mica := winsetup.SupportsMica()
	bg := &options.RGBA{R: 247, G: 248, B: 251, A: 255}
	if !winsetup.LightTheme() {
		bg = &options.RGBA{R: 15, G: 23, B: 38, A: 255}
	}
	if mica {
		bg = &options.RGBA{A: 0}
	}
	title := "TuxMail Setup"
	if a.cfg.Mode == winsetup.ModeUninstall {
		title = "TuxMail"
	}
	err := wails.Run(&options.App{
		Title:            title,
		Width:            860,
		Height:           580,
		DisableResize:    true,
		Frameless:        true,
		BackgroundColour: bg,
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        func(ctx context.Context) { s.ctx = ctx },
		OnBeforeClose: func(context.Context) bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.busy // no closing in the middle of an installation
		},
		Bind: []any{s},
		Windows: &wwin.Options{
			WebviewIsTransparent: mica,
			WindowIsTranslucent:  mica,
			BackdropType:         wwin.Mica,
			Theme:                wwin.SystemDefault,
			// The WebView2 cache of the setup lives in %TEMP%, not next to the exe.
			WebviewUserDataPath: filepath.Join(os.TempDir(), "TuxMail-Setup-WebView2"),
		},
	})
	if err != nil {
		a.log.Printf("window: %v", err)
		winsetup.MessageBox(msg(a.lang, "title"), fmt.Sprintf(msg(a.lang, "failed"), err), 0x10)
		return exitError
	}
	if a.cfg.TempCopy {
		_ = winsetup.ScheduleDelete([]string{a.self}, nil)
	}
	return exitOK
}

// State returns the start state.
func (s *Setup) State() State {
	a := s.a
	st := State{
		Mode: a.cfg.Mode, Version: version, Arch: runtime.GOARCH, Lang: a.lang, Dark: !winsetup.LightTheme(),
		Mica: winsetup.SupportsMica(), Elevated: a.elevated, WindowsBuild: winsetup.WindowsBuild(),
		WebView2: winsetup.WebView2Version(), DataDir: winsetup.UserDataDir(a.env),
		Dirs: map[winsetup.Scope]string{},
	}
	for _, sc := range []winsetup.Scope{winsetup.ScopeUser, winsetup.ScopeMachine, winsetup.ScopePortable} {
		st.Dirs[sc] = winsetup.DefaultDir(sc, a.env, a.setupDir)
	}
	for _, e := range a.existing {
		st.Existing = append(st.Existing, ExistingInfo{Existing: e, Kind: winsetup.Classify(e.Version, version)})
		st.Dirs[e.Scope] = e.Dir
	}
	if a.payErr != nil {
		st.Payload = a.payErr.Error()
	} else {
		st.SizeBytes = a.payload.Size()
	}
	if a.cfg.Mode == winsetup.ModeUninstall {
		scope, dir, ver := a.uninstallTarget()
		st.Uninstall = &UninstallInfo{Scope: scope, Dir: dir, Version: ver}
	} else if a.cfg.Scope != "" || a.cfg.Dir != "" || len(a.cfg.Opt) > 0 {
		req := a.silentRequest()
		st.Preset = &req
	}
	return st
}

// DefaultDir returns the proposed folder of a scope.
func (s *Setup) DefaultDir(scope winsetup.Scope) string {
	if e, ok := winsetup.FindExisting(s.a.existing, scope); ok {
		return e.Dir
	}
	return winsetup.DefaultDir(scope, s.a.env, s.a.setupDir)
}

// CheckDir validates a destination folder.
func (s *Setup) CheckDir(scope winsetup.Scope, dir string) winsetup.DirCheck {
	return winsetup.CheckDir(scope, dir, s.a.env, winsetup.StatDir)
}

// Browse opens the folder dialog and returns the proposed destination
// ("" when cancelled).
func (s *Setup) Browse(current string) (string, error) {
	start := current
	for start != "" && !isDir(start) {
		parent := filepath.Dir(start)
		if parent == start {
			start = ""
			break
		}
		start = parent
	}
	title := "Choose a folder"
	if s.a.lang == "de" {
		title = "Ordner auswählen"
	}
	picked, err := wruntime.OpenDirectoryDialog(s.ctx, wruntime.OpenDialogOptions{Title: title, DefaultDirectory: start})
	if err != nil || picked == "" {
		return "", err
	}
	return winsetup.SuggestDir(picked), nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Running returns the number of TuxMail processes started from dir.
func (s *Setup) Running(dir string) int { return len(winsetup.FindRunning(dir)) }

// SetLanguage switches the language of native texts.
func (s *Setup) SetLanguage(lang string) {
	s.a.lang = winsetup.NormalizeLang(lang)
	s.a.installer.Description = description(s.a.lang)
}

func (s *Setup) setBusy(b bool) {
	s.mu.Lock()
	s.busy = b
	s.mu.Unlock()
}

// reporter throttles progress events for the window.
func (s *Setup) reporter() func(winsetup.Progress) {
	var last time.Time
	var step winsetup.Step
	log := s.a.progressWriter()
	return func(p winsetup.Progress) {
		log(p)
		if p.Step == step && p.Percent < 100 && time.Since(last) < 40*time.Millisecond {
			return
		}
		last, step = time.Now(), p.Step
		wruntime.EventsEmit(s.ctx, "setup:progress", p)
	}
}

// Install installs (in this process, or in an elevated child for all users).
func (s *Setup) Install(req winsetup.Request, closeApps bool) error {
	a := s.a
	if a.payErr != nil {
		return a.payErr
	}
	check := winsetup.CheckDir(req.Scope, req.Dir, a.env, winsetup.StatDir)
	if check.Error != "" {
		return errors.New(string(check.Error))
	}
	req.Dir = check.Dir
	if req.Scope == winsetup.ScopePortable {
		req.Options = winsetup.Options{}
	}
	s.setBusy(true)
	defer s.setBusy(false)
	report := s.reporter()
	var err error
	if req.Scope == winsetup.ScopeMachine && !a.elevated {
		if !closeApps && len(winsetup.FindRunning(req.Dir)) > 0 {
			return errors.New("running")
		}
		err = s.runChild(winsetup.ChildArgs(req, a.lang, s.progressFile(), a.cfg.LogFile), report)
	} else {
		err = a.install(s.ctx, req, closeApps, report)
	}
	if err != nil {
		a.log.Printf("install failed: %v", err)
		return err
	}
	s.mu.Lock()
	s.installed = req
	s.mu.Unlock()
	return nil
}

func (s *Setup) progressFile() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("TuxMail-Setup-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
}

// runChild runs this program elevated (UAC) and forwards its progress file.
func (s *Setup) runChild(args []string, report func(winsetup.Progress)) error {
	var pf string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--progress="); ok {
			pf = v
		}
	}
	defer os.Remove(pf)
	h, err := winsetup.RunElevated(s.a.self, args)
	if errors.Is(err, winsetup.ErrCancelled) {
		return errors.New("elevationCancelled")
	}
	if err != nil {
		return err
	}
	var last winsetup.Progress
	code, err := winsetup.WaitProcess(h, func() {
		if p, ok := readProgress(pf); ok && p != last {
			last = p
			if !p.Done {
				report(p)
			}
		}
	})
	if err != nil {
		return err
	}
	if p, ok := readProgress(pf); ok {
		last = p
	}
	if last.Error != "" {
		return errors.New(last.Error)
	}
	if code != 0 {
		return fmt.Errorf("setup (administrator) exited with code %d, see %s", code, s.a.logPath)
	}
	report(winsetup.Progress{Step: winsetup.StepFinish, Percent: 100})
	return nil
}

// Uninstall removes the installation shown on the uninstall page.
func (s *Setup) Uninstall(keepData, closeApps bool) error {
	a := s.a
	scope, dir, _ := a.uninstallTarget()
	if dir == "" {
		return errors.New("no TuxMail installation found")
	}
	req := winsetup.UninstallRequest{Scope: scope, Dir: dir, KeepData: keepData}
	s.setBusy(true)
	defer s.setBusy(false)
	report := s.reporter()
	if !closeApps && len(winsetup.FindRunning(dir)) > 0 {
		return errors.New("running")
	}
	if scope == winsetup.ScopeMachine && !a.elevated {
		return s.runChild(winsetup.UninstallChildArgs(req, a.lang, s.progressFile(), true), report)
	}
	if err := winsetup.CloseRunning(dir, 15*time.Second); err != nil {
		return err
	}
	return a.uninstall(s.ctx, req, report)
}

// Launch starts the installed TuxMail.
func (s *Setup) Launch() error {
	s.mu.Lock()
	req := s.installed
	s.mu.Unlock()
	if req.Dir == "" {
		return errors.New("not installed")
	}
	return winsetup.Launch(filepath.Join(req.Dir, winsetup.AppExe))
}

// OpenDefaultApps opens the Windows default apps settings at TuxMail.
func (s *Setup) OpenDefaultApps() error {
	s.mu.Lock()
	scope := s.installed.Scope
	s.mu.Unlock()
	return winsetup.OpenDefaultApps(scope)
}

// OpenURL opens the project page or license (https only).
func (s *Setup) OpenURL(u string) error {
	if !strings.HasPrefix(u, "https://") {
		return errors.New("only https links")
	}
	return winsetup.OpenURL(u)
}

// OpenFolder shows a folder in Explorer.
func (s *Setup) OpenFolder(dir string) error {
	if !isDir(dir) {
		return errors.New("not a folder")
	}
	return winsetup.OpenURL(dir)
}

// Quit closes the window (refused while busy).
func (s *Setup) Quit() {
	s.mu.Lock()
	busy := s.busy
	s.mu.Unlock()
	if !busy {
		wruntime.Quit(s.ctx)
	}
}

// Minimise minimises the window.
func (s *Setup) Minimise() { wruntime.WindowMinimise(s.ctx) }
