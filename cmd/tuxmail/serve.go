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
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/instance"
	"github.com/Midas1989/Mailclient/internal/server"
	"github.com/Midas1989/Mailclient/locales"
	"github.com/Midas1989/Mailclient/web"
)

// engineLockWait is how long a starting service waits for a short command line
// call to finish.
const engineLockWait = 15 * time.Second

// webFS is the embedded web frontend (package web; nil when the binary was
// built without `go generate ./web`, then a placeholder page is served).
var webFS fs.FS = web.FS()

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
	opts := &slog.HandlerOptions{Level: lvl}
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
	// Marks the data as in use for `tuxmail mail` and `tuxmail term`, which
	// must not run a second sync engine. A terminal session already holding
	// it delays the start briefly; running anyway keeps the service available.
	if l, err := instance.AcquireWait(cfg.DataDir, engineLockWait); err != nil {
		log.Warn("another TuxMail process holds the data directory lock", "err", err)
	} else {
		defer l.Release()
	}
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
