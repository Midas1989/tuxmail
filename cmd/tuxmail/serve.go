// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/instance"
	"github.com/Midas1989/Mailclient/internal/server"
	"github.com/Midas1989/Mailclient/locales"
	"github.com/Midas1989/Mailclient/web"
)

// engineLockWait is how long a starting desktop window waits for a running
// `tuxmail mail` change (bounded by mailWriteTimeout). Terminal sessions hold
// no lock, and on server data no command takes it, so serve does not wait.
const engineLockWait = 35 * time.Second

// engineLockRetry is how often a service tries again to get the lock it did
// not get at the start.
var engineLockRetry = 5 * time.Second

// holdEngineLock takes the engine lock of a service. A service keeps running
// without it, so it tries again until it has it. release ends this.
func holdEngineLock(ctx context.Context, dir string, wait time.Duration, log *slog.Logger) (release func()) {
	l, err := instance.AcquireWait(dir, wait)
	if err == nil {
		return l.Release
	}
	log.Warn("the data directory lock is held or unavailable; retrying", "err", err)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(engineLockRetry)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if l, err = instance.Acquire(dir); err == nil {
					log.Info("data directory lock acquired")
					<-stop
					l.Release()
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// webFS is the embedded web frontend (package web; nil when the binary was
// built without `go generate ./web`, then a placeholder page is served).
var webFS fs.FS = web.FS()

// redactedLogKeys are attribute names whose value never reaches a log, wherever
// the attribute comes from (Basis spec/BETRIEB.md: central redaction).
var redactedLogKeys = []string{"password", "passwd", "passphrase", "secret", "token", "api_key", "apikey", "private_key", "authorization", "cookie"}

func redactLogAttr(_ []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, r := range redactedLogKeys {
		if strings.Contains(k, r) {
			a.Value = slog.StringValue("********")
			break
		}
	}
	return a
}

func newLogger(c config.Log, fallback io.Writer) (*slog.Logger, io.Closer, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Level)); err != nil {
		return nil, nil, err
	}
	w := fallback
	var closer io.Closer
	if c.File != "" {
		f, err := os.OpenFile(c.File, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		w, closer = f, f
	}
	opts := &slog.HandlerOptions{Level: lvl, ReplaceAttr: redactLogAttr}
	var h slog.Handler = slog.NewJSONHandler(w, opts)
	if c.Format == "text" {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), closer, nil
}

func (c *cli) serve(ctx context.Context, args []string) error {
	fs, cfgPath := newFlagSet("serve", c)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: serve takes no arguments", errUsage)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log, closer, err := newLogger(cfg.Log, c.stderr)
	if err == nil {
		if cfg.Auth.SessionLifetime.D() > config.MaxSessionLifetime {
			log.Warn("auth.session_lifetime is limited to the maximum", "configured", cfg.Auth.SessionLifetime.D().String(), "effective", config.MaxSessionLifetime.String())
		}
		if cfg.Auth.SessionIdleTimeout.D() > config.MaxSessionIdle {
			log.Warn("auth.session_idle_timeout is limited to the maximum", "configured", cfg.Auth.SessionIdleTimeout.D().String(), "effective", config.MaxSessionIdle.String())
		}
	}
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer.Close()
	}
	slog.SetDefault(log)
	svc, err := auth.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer svc.Close()
	// Marks the data as owned by a running engine.
	defer holdEngineLock(ctx, cfg.DataDir, 0, log)()
	if n, err := svc.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("no users exist yet; create an administrator with: tuxmail user add --admin --username NAME")
	}
	srv, err := server.New(cfg, svc, server.Options{Static: webFS, Logger: log, Version: version, Locales: locales.FS})
	if err != nil {
		return err
	}
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (c *cli) checkConfig(args []string) error {
	fs, cfgPath := newFlagSet("check-config", c)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	var problems []error
	if cfg.TLS.Mode == "file" {
		for _, f := range []string{cfg.TLS.CertFile, cfg.TLS.KeyFile} {
			if _, err := os.Stat(f); err != nil {
				problems = append(problems, fmt.Errorf("tls: %w", err))
			}
		}
	}
	if cfg.Auth.OIDC.Enabled {
		if _, err := cfg.OIDCClientSecret(); err != nil {
			problems = append(problems, err)
		}
	}
	if cfg.Log.File != "" {
		if f, err := os.OpenFile(cfg.Log.File, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err != nil {
			problems = append(problems, fmt.Errorf("log.file: %w", err))
		} else {
			f.Close()
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "configuration %s is valid\n", *cfgPath)
	return nil
}
