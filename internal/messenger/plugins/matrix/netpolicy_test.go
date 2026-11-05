// SPDX-License-Identifier: AGPL-3.0-or-later

package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// Audit ND-02 (PoC): the plugin process used to POST /_matrix/client/v3/login
// to a user-chosen loopback homeserver. With the server's policy handed over
// in hello (sdk.ApplyNetPolicy) the connection is refused before any request
// is sent.
func TestPairRefusesInternalHomeserverUnderPolicy(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"boom"}`))
	}))
	defer srv.Close()
	if netguard.Current() != nil {
		t.Fatal("a policy is installed before the test")
	}
	t.Cleanup(func() { netguard.SetPolicy(nil) })
	if err := sdk.ApplyNetPolicy(&wire.NetPolicy{}); err != nil { // what the host sends in server mode
		t.Fatal(err)
	}

	p := New() // default HTTP client, as in the real plugin binary
	ctx := context.Background()
	if err := p.Init(ctx, sdk.NewTestEnv(t.TempDir(), nil, func(wire.Event) {})); err != nil {
		t.Fatal(err)
	}
	_, err := p.Pair(ctx, wire.PairRequest{Account: "m1", Method: wire.PairPassword, Homeserver: srv.URL, Username: "anna", Password: "pw"},
		sdk.NewTestPairing("m1", func(wire.PairEvent) {}))
	if err == nil {
		t.Fatal("pairing against a loopback homeserver succeeded")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the loopback service received %d requests (SSRF)", n)
	}
}
