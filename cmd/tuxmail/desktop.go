// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build desktop

package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
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
	log := slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, st, err := openDesktopApp(ctx, loc, log)
	if err != nil {
		return err
	}
	defer st.Close()
	defer a.Close()
	a.Start()
	log.Info("tuxmail desktop", "data_dir", loc.Dir, "portable", loc.Portable, "version", version)

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
	return wails.Run(&options.App{
		Title:     "TuxMail",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
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
		Bind: []interface{}{bound},
	})
}
