// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	// Restrict the DLL search path before any GUI DLL is loaded: the setup
	// usually runs from the Downloads folder (AUDIT-D D-02).
	_ "github.com/Midas1989/Mailclient/internal/dllsafe"
	"github.com/Midas1989/Mailclient/internal/winsetup"
)

// version is set with -ldflags "-X main.version=..." (build/version.sh).
var version = "dev"

// payload/ holds payload.zip (and optionally MicrosoftEdgeWebview2Setup.exe)
// at build time; in the repository only payload/.keep exists.
//
//go:embed all:payload
var payloadFS embed.FS

const (
	exitOK        = 0
	exitError     = 1
	exitUsage     = 2
	exitElevation = 740  // ERROR_ELEVATION_REQUIRED
	exitCancelled = 1223 // ERROR_CANCELLED
)

func main() { os.Exit(run()) }

// app is the state shared by the silent mode and the window.
type app struct {
	cfg       winsetup.Config
	lang      string
	self      string
	log       *log.Logger
	logPath   string
	payload   *winsetup.Payload
	payErr    error
	reg       winsetup.Registry
	existing  []winsetup.Existing
	webview2  []byte // embedded bootstrapper (optional)
	elevated  bool
	setupDir  string
	installer *winsetup.Installer
}

func run() int {
	cfg, err := winsetup.ParseArgs(os.Args[1:])
	lang := cfg.Lang
	if lang == "" {
		lang = winsetup.UILanguage()
	}
	if err != nil {
		if !cfg.Silent {
			winsetup.MessageBox(msg(lang, "title"), fmt.Sprintf(msg(lang, "usage"), err, winsetup.Usage), windows.MB_ICONERROR)
		}
		return exitUsage
	}
	if cfg.Help {
		winsetup.MessageBox(msg(lang, "title"), winsetup.Usage, windows.MB_ICONINFORMATION)
		return exitOK
	}
	if winsetup.WindowsBuild() < 10240 {
		if !cfg.Silent {
			winsetup.MessageBox(msg(lang, "title"), msg(lang, "oldWindows"), windows.MB_ICONERROR)
		}
		return exitError
	}
	a := newApp(cfg, lang)
	defer a.log.Printf("exit")

	if cfg.Mode == winsetup.ModeUninstall && !cfg.TempCopy {
		if code, relaunched := a.relaunchFromTemp(); relaunched {
			return code
		}
	}
	if cfg.Silent {
		return a.silent()
	}
	if cfg.Mode == winsetup.ModeUninstall && winsetup.WebView2Version() == "" {
		// No WebView2 (removed meanwhile): ask natively, keep the user data.
		if winsetup.MessageBox(msg(lang, "title"), msg(lang, "uninstallConfirm"), windows.MB_YESNO|windows.MB_ICONQUESTION) != 6 /* IDYES */ {
			return exitCancelled
		}
		a.cfg.Silent = true
		return a.silent()
	}
	if !a.ensureWebView2() {
		return exitError
	}
	return a.runUI()
}

func newApp(cfg winsetup.Config, lang string) *app {
	a := &app{cfg: cfg, lang: lang, reg: winsetup.WinRegistry{}, elevated: winsetup.IsElevated()}
	a.self, _ = os.Executable()
	a.setupDir = filepath.Dir(a.self)
	a.logPath = cfg.LogFile
	if a.logPath == "" {
		a.logPath = filepath.Join(os.TempDir(), "TuxMail-Setup.log")
		if a.elevated {
			// Not the predictable, user-writable %TEMP%\TuxMail-Setup.log:
			// an elevated append to a file the user prepared (link) would
			// write anywhere (AUDIT-D D-03).
			if dir, err := winsetup.PrivateTempDir("TuxMail-Setup-log-"); err == nil {
				a.logPath = filepath.Join(dir, "TuxMail-Setup.log")
			}
		}
	}
	var w io.Writer = io.Discard
	if f, err := os.OpenFile(a.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		w = f
	}
	a.log = log.New(w, fmt.Sprintf("[%d] ", os.Getpid()), log.LstdFlags)
	a.log.Printf("TuxMail Setup %s %s/%s, Windows build %d, elevated=%v, args=%q",
		version, runtime.GOOS, runtime.GOARCH, winsetup.WindowsBuild(), a.elevated, os.Args[1:])

	var data []byte
	var err error
	if cfg.Payload != "" {
		data, err = os.ReadFile(cfg.Payload)
	} else {
		data, err = payloadFS.ReadFile("payload/payload.zip")
		if errors.Is(err, fs.ErrNotExist) {
			err = winsetup.ErrNoPayload
		}
	}
	if err == nil {
		a.payload, err = winsetup.OpenPayload(data)
	}
	a.payErr = err
	if err != nil {
		a.log.Printf("payload: %v", err)
	}
	a.webview2, _ = payloadFS.ReadFile("payload/MicrosoftEdgeWebview2Setup.exe")
	a.existing = winsetup.DetectExisting(a.reg)
	a.installer = &winsetup.Installer{
		Reg: a.reg, Links: winsetup.ShellLinker{}, Folders: winsetup.ShellFolders,
		Payload: a.payload, Version: version, SetupPath: a.self,
		Description: description(lang), Logf: a.log.Printf,
		Protect: winsetup.ProtectInstallDir, Env: a.env,
	}
	return a
}

func description(lang string) string {
	if lang == "de" {
		return "E-Mail, Kalender, Kontakte und Messenger in einer Anwendung"
	}
	return "Email, calendar, contacts and messengers in one app"
}

func (a *app) env(k string) string { return os.Getenv(k) }

// uninstallTarget works out which installation to remove: --dir, the folder
// of this program, or the registered installation of --scope.
func (a *app) uninstallTarget() (winsetup.Scope, string, string) {
	dir := a.cfg.Dir
	if dir == "" && !a.cfg.TempCopy {
		if _, err := winsetup.ReadRecord(a.setupDir); err == nil {
			dir = a.setupDir
		}
	}
	for _, e := range a.existing {
		if (dir != "" && winsetup.WinSame(e.Dir, dir)) || (dir == "" && (a.cfg.Scope == "" || a.cfg.Scope == e.Scope)) {
			return e.Scope, e.Dir, e.Version
		}
	}
	scope := a.cfg.Scope
	ver := ""
	if rec, err := winsetup.ReadRecord(dir); dir != "" && err == nil {
		ver = rec.Version
		if scope == "" {
			scope = rec.Scope
		}
	}
	if scope == "" {
		scope = winsetup.ScopeUser
	}
	return scope, dir, ver
}

// relaunchFromTemp: the uninstaller cannot delete the folder it runs from,
// so a copy in %TEMP% does the work.
func (a *app) relaunchFromTemp() (int, bool) {
	scope, dir, _ := a.uninstallTarget()
	if dir == "" || !winsetup.WinWithin(dir, a.self) {
		return 0, false
	}
	if winsetup.ElevateBeforeTempCopy(scope, a.elevated) {
		// Elevate the copy in the (administrator-only) installation folder;
		// the elevated instance copies itself into a private temp folder.
		// Copying first would elevate a file in the user's %TEMP% that
		// another process could replace before the UAC prompt (AUDIT-D D-04).
		args := append(append([]string{}, os.Args[1:]...), "--elevated", "--lang="+a.lang)
		h, err := winsetup.RunElevated(a.self, args)
		if errors.Is(err, winsetup.ErrCancelled) {
			a.log.Printf("elevation cancelled")
			return exitCancelled, true
		}
		if err != nil {
			a.log.Printf("elevation: %v", err)
			return exitElevation, true
		}
		if !a.cfg.Silent {
			_ = windows.CloseHandle(h)
			return exitOK, true
		}
		code, err := winsetup.WaitProcess(h, nil)
		if err != nil {
			return exitError, true
		}
		return int(code), true
	}
	tmp, err := winsetup.CopyToTemp(a.self)
	if err != nil {
		a.log.Printf("copy to temp: %v", err)
		return 0, false
	}
	args := append([]string{}, os.Args[1:]...)
	args = append(args, "--temp-copy", "--dir="+dir, "--lang="+a.lang)
	cmd := exec.Command(tmp, args...)
	if err := cmd.Start(); err != nil {
		a.log.Printf("start temp copy: %v", err)
		return 0, false
	}
	a.log.Printf("uninstall continues in %s (pid %d)", tmp, cmd.Process.Pid)
	if !a.cfg.Silent {
		return exitOK, true
	}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), true
		}
		return exitError, true
	}
	return exitOK, true
}

// progressWriter returns a reporter that writes the progress for the
// parent process (elevated child) and logs step changes.
func (a *app) progressWriter() func(winsetup.Progress) {
	var last winsetup.Step
	var lastWrite time.Time
	return func(p winsetup.Progress) {
		if p.Step != last {
			a.log.Printf("step %s (%.0f%%)", p.Step, p.Percent)
			last = p.Step
		}
		if a.cfg.ProgressFile == "" {
			return
		}
		if !p.Done && p.Percent < 100 && time.Since(lastWrite) < 80*time.Millisecond {
			return
		}
		lastWrite = time.Now()
		writeProgress(a.cfg.ProgressFile, p)
	}
}

func writeProgress(path string, p winsetup.Progress) {
	b, _ := json.Marshal(p)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func readProgress(path string) (winsetup.Progress, bool) {
	var p winsetup.Progress
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &p) != nil {
		return p, false
	}
	return p, true
}

// silent runs without window; errors go to the log (and the exit code).
func (a *app) silent() int {
	ctx := context.Background()
	report := a.progressWriter()
	fail := func(code int, err error) int {
		a.log.Printf("error: %v", err)
		if a.cfg.ProgressFile != "" {
			writeProgress(a.cfg.ProgressFile, winsetup.Progress{Done: true, Error: err.Error()})
		}
		return code
	}
	if a.cfg.Mode == winsetup.ModeUninstall {
		scope, dir, _ := a.uninstallTarget()
		if dir == "" {
			return fail(exitError, errors.New("no TuxMail installation found"))
		}
		if scope == winsetup.ScopeMachine && !a.elevated {
			return a.elevateSilent()
		}
		if err := a.closeApps(dir); err != nil {
			return fail(exitError, err)
		}
		if err := a.uninstall(ctx, winsetup.UninstallRequest{Scope: scope, Dir: dir, KeepData: a.cfg.KeepData}, report); err != nil {
			return fail(exitError, err)
		}
		report(winsetup.Progress{Step: winsetup.StepFinish, Percent: 100, Done: true})
		return exitOK
	}

	if a.payErr != nil {
		return fail(exitError, a.payErr)
	}
	req := a.silentRequest()
	check := winsetup.CheckDir(req.Scope, req.Dir, a.env, winsetup.StatDir)
	if check.Error != "" {
		return fail(exitUsage, fmt.Errorf("destination %q: %s", req.Dir, check.Error))
	}
	req.Dir = check.Dir
	if req.Scope == winsetup.ScopeMachine && !a.elevated {
		return a.elevateSilent()
	}
	if err := a.install(ctx, req, a.cfg.CloseApps, report); err != nil {
		return fail(exitError, err)
	}
	report(winsetup.Progress{Step: winsetup.StepFinish, Percent: 100, Done: true})
	return exitOK
}

func (a *app) silentRequest() winsetup.Request {
	scope := a.cfg.Scope
	var ex winsetup.Existing
	var found bool
	if scope == "" {
		if len(a.existing) > 0 {
			ex, found = a.existing[0], true
			scope = ex.Scope
		} else {
			scope = winsetup.ScopeUser
		}
	} else {
		ex, found = winsetup.FindExisting(a.existing, scope)
	}
	base := winsetup.DefaultOptions()
	if found {
		base = ex.Options
	}
	dir := a.cfg.Dir
	if dir == "" && found {
		dir = ex.Dir
	}
	if dir == "" {
		dir = winsetup.DefaultDir(scope, a.env, a.setupDir)
	}
	opts := a.cfg.MergeOptions(base)
	if scope == winsetup.ScopePortable {
		opts = winsetup.Options{}
	}
	return winsetup.Request{Scope: scope, Dir: dir, Options: opts}
}

// elevateSilent restarts the silent setup with administrator rights and
// waits for it.
func (a *app) elevateSilent() int {
	args := append(append([]string{}, os.Args[1:]...), "--silent", "--elevated", "--lang="+a.lang)
	h, err := winsetup.RunElevated(a.self, args)
	if errors.Is(err, winsetup.ErrCancelled) {
		a.log.Printf("elevation cancelled")
		return exitElevation
	}
	if err != nil {
		a.log.Printf("elevation: %v", err)
		return exitElevation
	}
	code, err := winsetup.WaitProcess(h, nil)
	if err != nil {
		return exitError
	}
	return int(code)
}

func (a *app) closeApps(dir string) error {
	if len(winsetup.FindRunning(dir)) == 0 {
		return nil
	}
	if !a.cfg.CloseApps && a.cfg.Silent {
		return errors.New("TuxMail is running (--no-close-apps)")
	}
	a.log.Printf("closing running TuxMail in %s", dir)
	return winsetup.CloseRunning(dir, 15*time.Second)
}

// install performs an installation in this process (no elevation step).
func (a *app) install(ctx context.Context, req winsetup.Request, closeApps bool, report func(winsetup.Progress)) error {
	report(winsetup.Progress{Step: winsetup.StepClose, Percent: 0})
	if closeApps {
		if err := winsetup.CloseRunning(req.Dir, 15*time.Second); err != nil {
			return err
		}
	} else if len(winsetup.FindRunning(req.Dir)) > 0 {
		return errors.New("running")
	}
	if winsetup.WebView2Version() == "" && req.Scope != winsetup.ScopePortable {
		// Silent mode (the window already ensured it): TuxMail needs the runtime.
		report(winsetup.Progress{Step: winsetup.StepWebView2, Percent: 1})
		if err := winsetup.InstallWebView2(ctx, a.webview2); err != nil {
			a.log.Printf("WebView2: %v (TuxMail will ask for it at the first start)", err)
		}
	}
	if err := a.installer.Install(ctx, req, report); err != nil {
		return err
	}
	if req.Scope != winsetup.ScopePortable {
		winsetup.NotifyAssociationsChanged()
	}
	return nil
}

// uninstall removes an installation in this process and schedules what is
// still locked (this program's own copies).
func (a *app) uninstall(ctx context.Context, req winsetup.UninstallRequest, report func(winsetup.Progress)) error {
	req.DataDir = winsetup.UserDataDir(a.env)
	failed, err := a.installer.Uninstall(ctx, req, report)
	if err != nil {
		return err
	}
	winsetup.NotifyAssociationsChanged()
	var files []string
	for _, f := range failed {
		files = append(files, filepath.Join(req.Dir, filepath.FromSlash(f)))
	}
	dirs := []string{filepath.Join(req.Dir, winsetup.PluginsSubdir), req.Dir}
	if a.cfg.TempCopy && strings.HasPrefix(strings.ToLower(a.self), strings.ToLower(os.TempDir())) {
		files = append(files, a.self)
		dirs = append(dirs, filepath.Dir(a.self)) // the private TuxMail-Uninstall-* folder
	}
	if len(files) > 0 {
		a.log.Printf("scheduling deletion of %v", files)
		return winsetup.ScheduleDelete(files, dirs)
	}
	return nil
}

// ensureWebView2 installs the WebView2 runtime the window needs.
func (a *app) ensureWebView2() bool {
	if v := winsetup.WebView2Version(); v != "" {
		a.log.Printf("WebView2 %s", v)
		return true
	}
	a.log.Printf("WebView2 runtime missing")
	if winsetup.MessageBox(msg(a.lang, "title"), msg(a.lang, "wv2Missing"), windows.MB_OKCANCEL|windows.MB_ICONINFORMATION) != 1 /* IDOK */ {
		return false
	}
	if err := winsetup.InstallWebView2(context.Background(), a.webview2); err != nil {
		a.log.Printf("WebView2 install: %v", err)
		if winsetup.MessageBox(msg(a.lang, "title"), fmt.Sprintf(msg(a.lang, "wv2Failed"), err), windows.MB_YESNO|windows.MB_ICONWARNING) == 6 /* IDYES */ {
			_ = winsetup.OpenURL(winsetup.WebView2DownloadPage)
		}
		return false
	}
	return true
}
