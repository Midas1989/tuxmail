// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build desktop

package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/server/api"
	"github.com/Midas1989/Mailclient/web"
)

// serveDesktopTestHook serves the desktop API (the same handlers the
// window uses) on a loopback address for automated end-to-end tests of the
// native build (scripts/e2e.sh, docs/ENTWICKLUNG.md). It is off unless
// TUXMAIL_DESKTOP_TEST_LISTEN (loopback host:port) and
// TUXMAIL_DESKTOP_TEST_TOKEN are set; every request needs the header
// X-TuxMail-Test-Token.
func serveDesktopTestHook(ctx context.Context, mux http.Handler, log *slog.Logger) error {
	addr, token := os.Getenv("TUXMAIL_DESKTOP_TEST_LISTEN"), os.Getenv("TUXMAIL_DESKTOP_TEST_TOKEN")
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() || len(token) < 16 {
		return fmt.Errorf("TUXMAIL_DESKTOP_TEST_LISTEN must be a loopback host:port and TUXMAIL_DESKTOP_TEST_TOKEN at least 16 characters")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Warn("desktop test hook enabled: the API is reachable on loopback", "addr", ln.Addr().String())
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TuxMail-Test-Token")), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	return nil
}

// desktopBuild reports whether this binary contains the Wails desktop shell.
const desktopBuild = true

// desktop runs the Wails window (KONZEPT 4.5 "Desktop"): the frontend calls
// window.go.app.Api.<Method> (internal/app.Api) and receives ApiEvents as
// runtime event "tuxmail:event". Server-relative URLs (attachments, cid:
// images) are answered by the asset handler with the HTTP API handlers.
func (c *cli) desktop(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	dataDir := fs.String("data-dir", "", "data directory (implies portable mode)")
	portable := fs.Bool("portable", false, "keep all data in ./data next to the executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, _ := os.Executable()
	loc, err := resolveDataDir(*dataDir, *portable, exe)
	if err != nil {
		return err
	}
	// The log goes to <data_dir>/logs/tuxmail.log (rotated, 5 MB x 4; the
	// Windows GUI build has no console) and to stderr.
	var logOut io.Writer = c.stderr
	logFile := ""
	if err := os.MkdirAll(loc.Dir, 0o700); err == nil {
		if lf, err := openLogFile(filepath.Join(loc.Dir, "logs"), logMaxSize, logKeep); err == nil {
			defer lf.Close()
			logOut, logFile = io.MultiWriter(c.stderr, lf), lf.Path()
		} else {
			fmt.Fprintln(c.stderr, "tuxmail: log file:", err)
		}
	}
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("TUXMAIL_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	a, st, err := openDesktopApp(ctx, loc, log)
	if err != nil {
		log.Error("tuxmail desktop: start failed", "err", err)
		return err
	}
	defer st.Close()
	defer a.Close()
	a.SetDiagnostics(app.DiagnosticsOptions{LogFile: logFile, Portable: loc.Portable, Config: func() any {
		return map[string]any{"mode": "desktop", "portable": loc.Portable, "dataDir": loc.Dir, "logLevel": level.String(),
			"titleBar": loadDesktopPrefs(loc.Dir)}
	}})
	a.Start()
	log.Info("tuxmail desktop", "data_dir", loc.Dir, "portable", loc.Portable, "version", version, "log_file", logFile,
		"os", runtime.GOOS+"/"+runtime.GOARCH)

	mux := http.NewServeMux()
	api.Mount(mux, func(h api.AppHandler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, a) }
	}, api.Options{Logger: log, Skip: map[string]bool{"GET /api/events": true}})

	if err := serveDesktopTestHook(ctx, mux, log); err != nil {
		return err
	}

	assets := web.FS()
	if assets == nil {
		assets = placeholderFS()
	}
	bound := app.NewApi(ctx, a)
	// Window decoration: a frameless window with the app's own title bar
	// (drag region, window buttons; Windows default) or the system title
	// bar (Linux/macOS default; Settings → "Systemtitelleiste verwenden").
	// On Windows 11 the window gets a Mica backdrop.
	frameless := !loadDesktopPrefs(loc.Dir).nativeTitleBar(runtime.GOOS)
	backdrop := frameless && backdropSupported()
	desk := &Desktop{dataDir: loc.Dir, frameless: frameless, backdrop: backdrop, logFile: logFile}
	winOpts := &windows.Options{Theme: windows.SystemDefault}
	bg := &options.RGBA{R: 246, G: 247, B: 249, A: 255}
	if backdrop {
		winOpts.WebviewIsTransparent, winOpts.WindowIsTranslucent, winOpts.BackdropType = true, true, windows.Mica
		bg = &options.RGBA{A: 0}
	}
	log.Info("tuxmail desktop: window", "frameless", frameless, "backdrop", backdrop)
	return wails.Run(&options.App{
		Title:            "TuxMail",
		Width:            1280,
		Height:           820,
		MinWidth:         720,
		MinHeight:        520,
		Frameless:        frameless,
		BackgroundColour: bg,
		Windows:          winOpts,
		Linux:            &linux.Options{ProgramName: "TuxMail"},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: mux,
		},
		OnStartup: func(wctx context.Context) {
			events, _ := a.Events().Subscribe(wctx, 256)
			go func() {
				for ev := range events {
					wruntime.EventsEmit(wctx, "tuxmail:event", ev)
				}
			}()
			// OAuth sign-in (loopback flow) opens the system browser through Wails.
			a.SetOAuthOpener(func(_ context.Context, u string) error { wruntime.BrowserOpenURL(wctx, u); return nil })
			if st, _ := a.VaultStatus(wctx); st.Locked {
				wruntime.EventsEmit(wctx, "tuxmail:event", app.Event{Type: app.EventVaultLocked})
			}
		},
		Bind: []interface{}{bound, desk},
	})
}
