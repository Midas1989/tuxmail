// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/locales"
)

func TestDesktopTestNotification(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var gotTitle string
	var fail error
	a, err := app.New(ctx, app.Options{Store: st, DataDir: t.TempDir(), Locales: locales.FS, Mode: app.ModeDesktop,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC,
		DesktopNotify: func(_ context.Context, title, _ string) error { gotTitle = title; return fail }})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.SendTestNotification(ctx); err != nil || gotTitle != "TuxMail" {
		t.Fatalf("native notification: %v %q", err, gotTitle)
	}
	fail = errors.New("boom")
	if err := a.SendTestNotification(ctx); code(err) != app.CodeNotifyUnavailable {
		t.Fatalf("got %v", err)
	}
}
