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

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/server"
	"github.com/Midas1989/Mailclient/locales"
	"github.com/Midas1989/Mailclient/web"
)

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
		f, err := os.OpenFile(c.File, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
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
		if f, err := os.OpenFile(cfg.Log.File, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640); err != nil {
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
