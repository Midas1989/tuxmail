// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"testing"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

func setTestPolicy(t *testing.T, allowed ...string) {
	t.Helper()
	p := &netguard.Policy{}
	for _, a := range allowed {
		r, err := netguard.ParseRule(a)
		if err != nil {
			t.Fatal(err)
		}
		p.Allowed = append(p.Allowed, r)
	}
	netguard.SetPolicy(p)
	t.Cleanup(func() { netguard.SetPolicy(nil) })
}

// Audit ND-02: the Matrix homeserver is chosen by the user; in server mode it
// has to be https and must pass the outbound policy.
func TestCheckHomeserverServerMode(t *testing.T) {
	setTestPolicy(t, "127.0.0.1:8008")
	svc, _ := newHostSettingsService(t, true)
	ctx := context.Background()
	for _, hs := range []string{
		"https://127.0.0.1", "https://localhost", "https://[::1]:8448", "https://10.0.0.5", "https://169.254.169.254/",
		"https://169.254.169.254:80", "http://example.org", "http://8.8.8.8", "http://127.0.0.1:9999", "ftp://8.8.8.8",
		"https://user:pw@8.8.8.8", "https://8.8.8.8:0", "https://",
	} {
		err := svc.checkHomeserver(ctx, hs)
		if err == nil {
			t.Errorf("checkHomeserver(%q) accepted, want an error", hs)
		} else if wire.ErrorCode(err) != wire.CodeInvalidParams {
			t.Errorf("checkHomeserver(%q) = %v, want an invalid-params error", hs, err)
		}
	}
	for _, hs := range []string{"", "https://8.8.8.8", "8.8.8.8:8448", "http://127.0.0.1:8008"} {
		if err := svc.checkHomeserver(ctx, hs); err != nil {
			t.Errorf("checkHomeserver(%q) = %v", hs, err)
		}
	}
}

// Desktop mode (users own the machine): no restriction, as before.
func TestCheckHomeserverDesktopMode(t *testing.T) {
	setTestPolicy(t)
	svc, _ := newHostSettingsService(t, false)
	for _, hs := range []string{"http://127.0.0.1:8008", "https://localhost", "http://10.0.0.5"} {
		if err := svc.checkHomeserver(context.Background(), hs); err != nil {
			t.Errorf("checkHomeserver(%q) = %v", hs, err)
		}
	}
}

// StartPairing and the later "homeserver" input both go through the check
// before anything reaches the plugin.
func TestStartPairingRejectsInternalHomeserver(t *testing.T) {
	setTestPolicy(t)
	ctx := context.Background()
	svc, _ := newHostSettingsService(t, true)
	en := true
	if err := svc.UpdatePlugin(ctx, "matrix", PluginPatch{Enabled: &en, AcceptRisk: true}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.StartPairing(ctx, PairingInput{Plugin: "matrix", Method: wire.PairPassword, Homeserver: "http://127.0.0.1:8008",
		Username: "a", Password: "b"})
	if wire.ErrorCode(err) != wire.CodeInvalidParams {
		t.Fatalf("StartPairing = %v, want an invalid-params error (before the plugin is contacted)", err)
	}
}
