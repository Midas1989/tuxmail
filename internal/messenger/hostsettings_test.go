// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Midas1989/Mailclient/internal/store"
)

func newHostSettingsService(t *testing.T, restrict bool) (*Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := New(Options{Store: st, DataDir: dir, Dirs: []string{}, RestrictHostSettings: restrict,
		Env: func(string) map[string]string {
			return map[string]string{"signal_cli_path": "/opt/signal-cli/bin/signal-cli"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, st
}

// Audit D (AUDIT-D-endpoint.md, D-01): in server mode a user must not be
// able to choose the program the signal plugin executes on the server.
func TestHostSettingsRestrictedInServerMode(t *testing.T) {
	ctx := context.Background()
	svc, st := newHostSettingsService(t, true)
	err := svc.UpdatePlugin(ctx, "signal", PluginPatch{Settings: map[string]string{"signal_cli_path": "/bin/sh"}})
	if !errors.Is(err, ErrHostSetting) {
		t.Fatalf("UpdatePlugin(signal_cli_path) = %v, want ErrHostSetting", err)
	}
	if err := svc.UpdatePlugin(ctx, "signal", PluginPatch{ResetSetting: []string{"signal_cli_path"}}); !errors.Is(err, ErrHostSetting) {
		t.Fatalf("reset: %v", err)
	}
	// A value stored before the restriction (or written directly) is ignored.
	p, err := st.GetMessengerPlugin(ctx, "signal")
	if err != nil {
		t.Fatal(err)
	}
	if p.Settings == nil {
		p.Settings = map[string]string{}
	}
	p.Settings["signal_cli_path"] = "/tmp/evil"
	if err := st.PutMessengerPlugin(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := svc.pluginConfig("signal")["signal_cli_path"]; got != "/opt/signal-cli/bin/signal-cli" {
		t.Fatalf("plugin config uses %q, want the administrator's value", got)
	}
	infos, err := svc.Plugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.Spec.Name == "signal" && (slices.Contains(in.Spec.Settings, "signal_cli_path") || in.Settings["signal_cli_path"] != "") {
			t.Fatalf("host setting offered to the user: %+v", in)
		}
	}
}

func TestHostSettingsAllowedOnDesktop(t *testing.T) {
	ctx := context.Background()
	svc, _ := newHostSettingsService(t, false)
	if err := svc.UpdatePlugin(ctx, "signal", PluginPatch{Settings: map[string]string{"signal_cli_path": "/home/me/signal-cli"}}); err != nil {
		t.Fatal(err)
	}
	if got := svc.pluginConfig("signal")["signal_cli_path"]; got != "/home/me/signal-cli" {
		t.Fatalf("got %q", got)
	}
}
