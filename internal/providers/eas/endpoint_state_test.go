// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"encoding/json"
	"testing"
)

// A stored endpoint is only reused while the account is configured with the
// server it was learned for: after a change of server, port or path the new
// configuration wins, otherwise a moved or switched-off old server would stay
// in use until the state is reset.
func TestStoredEndpointFollowsConfiguredServer(t *testing.T) {
	ctx := context.Background()
	const (
		oldBase   = "https://eas.example.org/Microsoft-Server-ActiveSync"
		redirect  = "https://eas2.example.org/Microsoft-Server-ActiveSync"
		newBase   = "https://mail.example.org/Microsoft-Server-ActiveSync"
		newerPort = "https://eas.example.org:8443/Microsoft-Server-ActiveSync"
	)
	open := func(t *testing.T, st memState, configured string) *Client {
		t.Helper()
		c, err := NewClient(ClientConfig{URL: configured, Username: "alice", Password: "x", DeviceID: "dev1", State: st})
		if err != nil {
			t.Fatal(err)
		}
		c.loadState(ctx)
		return c
	}
	store := func(s accountState) memState {
		st := memState{}
		data, _ := json.Marshal(s)
		st["alice|dev1"] = data
		return st
	}
	for _, tc := range []struct {
		name       string
		stored     accountState
		configured string
		want       string
	}{
		{"redirect kept for the same configuration", accountState{URL: redirect, Base: oldBase}, oldBase, redirect},
		{"other host of the same domain", accountState{URL: oldBase, Base: oldBase}, newBase, newBase},
		{"other port", accountState{URL: oldBase, Base: oldBase}, newerPort, newerPort},
		{"redirect of the old server dropped", accountState{URL: redirect, Base: oldBase}, newBase, newBase},
		{"legacy state equal to the configuration", accountState{URL: oldBase}, oldBase, oldBase},
		{"legacy state differing from the configuration", accountState{URL: oldBase}, newBase, newBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := open(t, store(tc.stored), tc.configured).URL(); got != tc.want {
				t.Fatalf("endpoint %q, want %q", got, tc.want)
			}
		})
	}
	// Saving records the configured endpoint.
	st := memState{}
	c := open(t, st, newBase)
	c.updateState(ctx, func(*accountState) {})
	var saved accountState
	if err := json.Unmarshal(st["alice|dev1"], &saved); err != nil || saved.Base != newBase {
		t.Fatalf("saved %+v %v", saved, err)
	}
}
