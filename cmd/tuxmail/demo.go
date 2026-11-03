// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/server"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// demoCmd seeds sample data for development and screenshots:
//
//	tuxmail demo seed --config FILE --user NAME   (server mode: the user's store)
//	tuxmail demo seed --data-dir DIR              (desktop data directory)
func (c *cli) demoCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "seed" {
		return fmt.Errorf("%w: demo seed --user NAME [--config FILE] | --data-dir DIR", errUsage)
	}
	fs, cfgPath := newFlagSet("demo seed", c)
	user := fs.String("user", "", "server user whose store is seeded")
	dataDir := fs.String("data-dir", "", "desktop data directory to seed")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var dir string
	switch {
	case *dataDir != "":
		dir = *dataDir
	case *user != "":
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		svc, err := auth.Open(ctx, cfg)
		if err != nil {
			return err
		}
		defer svc.Close()
		u, err := svc.GetUserByName(ctx, *user)
		if err != nil {
			return fmt.Errorf("user %q: %w", *user, err)
		}
		dir = svc.UserDir(u.ID)
	default:
		fs.Usage()
		return flag.ErrHelp
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(dir, server.UserStoreFile))
	if err != nil {
		return err
	}
	defer st.Close()
	if accs, err := st.ListAccounts(ctx); err != nil {
		return err
	} else if len(accs) > 0 {
		return fmt.Errorf("the store in %s already has accounts; refusing to add demo data", dir)
	}
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		return err
	}
	if _, err := demo.Seed(ctx, st, blobs, time.Now()); err != nil {
		return err
	}
	fmt.Fprintln(c.stdout, "demo data written to", dir)
	return nil
}
