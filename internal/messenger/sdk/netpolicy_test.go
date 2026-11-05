// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// startPlugin serves a fake plugin over pipes and returns the host side.
func startPlugin(t *testing.T) *wire.Conn {
	t.Helper()
	hr, pw := io.Pipe()
	pr, hw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sdk.Serve(ctx, fake.New("fake", "whatsapp"), pr, pw, nil)
	}()
	conn := wire.NewConn(hr, hw, nil)
	t.Cleanup(func() {
		cancel()
		conn.Close()
		hw.Close()
		pw.Close()
		<-done
	})
	return conn
}

// Audit ND-02: the policy of the server reaches the plugin process in the
// hello handshake; afterwards a dial of http.DefaultTransport to a loopback
// service (what a user-chosen Matrix homeserver URL would be) is refused.
func TestHelloInstallsNetPolicy(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()

	if netguard.Current() != nil {
		t.Fatal("a policy is installed before the test")
	}
	t.Cleanup(func() { netguard.SetPolicy(nil) })

	// Control: without a policy the loopback service is reachable.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections() // a kept-alive connection would not be dialed again
	hits.Store(0)

	conn := startPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var info wire.HelloResult
	err = conn.Call(ctx, wire.MethodHello, wire.HelloParams{ProtocolVersion: wire.ProtocolVersion, DataDir: t.TempDir(),
		NetPolicy: &wire.NetPolicy{}}, &info)
	if err != nil {
		t.Fatal(err)
	}
	if netguard.Current() == nil {
		t.Fatal("hello did not install the network policy")
	}
	_, err = http.Get(srv.URL)
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("GET %s after hello = %v, want netguard.ErrBlocked", srv.URL, err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the loopback service received %d requests", n)
	}
}

func TestApplyNetPolicy(t *testing.T) {
	t.Cleanup(func() { netguard.SetPolicy(nil) })
	if err := sdk.ApplyNetPolicy(nil); err != nil || netguard.Current() != nil {
		t.Fatalf("nil policy: %v, %v", err, netguard.Current())
	}
	// A malformed entry must not silently weaken the policy.
	if err := sdk.ApplyNetPolicy(&wire.NetPolicy{Allowed: []string{"not-a-network"}}); err == nil {
		t.Fatal("malformed rule accepted")
	}
	if netguard.Current() != nil {
		t.Fatal("a policy was installed despite the error")
	}
	if err := sdk.ApplyNetPolicy(&wire.NetPolicy{AllowPrivate: true, Allowed: []string{"127.0.0.1:8008", "[fd00::/8]:443"}}); err != nil {
		t.Fatal(err)
	}
	p := netguard.Current()
	if p == nil || !p.AllowPrivate || len(p.Allowed) != 2 || p.Allowed[0].String() != "[127.0.0.1/32]:8008" {
		t.Fatalf("policy = %+v", p)
	}
}
