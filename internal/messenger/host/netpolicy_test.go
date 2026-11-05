// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

type envProbe struct {
	*fake.Plugin
	mu  sync.Mutex
	env *sdk.Env
}

func (p *envProbe) Init(ctx context.Context, env *sdk.Env) error {
	p.mu.Lock()
	p.env = env
	p.mu.Unlock()
	return p.Plugin.Init(ctx, env)
}

func TestNetPolicyFor(t *testing.T) {
	if NetPolicyFor(nil) != nil {
		t.Fatal("no policy must stay nil (unrestricted)")
	}
	r1, _ := netguard.ParseRule("10.0.0.0/8:993")
	r2, _ := netguard.ParseRule("127.0.0.1")
	got := NetPolicyFor(&netguard.Policy{AllowPrivate: true, Allowed: []netguard.Rule{r1, r2}})
	want := &wire.NetPolicy{AllowPrivate: true, Allowed: []string{"[10.0.0.0/8]:993", "127.0.0.1/32"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NetPolicyFor = %+v, want %+v", got, want)
	}
	// What the host sends must be accepted by the plugin side unchanged.
	for _, s := range got.Allowed {
		if _, err := netguard.ParseRule(s); err != nil {
			t.Errorf("rule %q does not round-trip: %v", s, err)
		}
	}
}

// Audit ND-02: the hello handshake carries the server's outbound policy.
func TestHelloCarriesNetPolicy(t *testing.T) {
	r, _ := netguard.ParseRule("127.0.0.1:8008")
	netguard.SetPolicy(&netguard.Policy{Allowed: []netguard.Rule{r}})
	t.Cleanup(func() { netguard.SetPolicy(nil) })

	fp := &envProbe{Plugin: fake.New("fake", "matrix")}
	h := New(Options{Static: []Manifest{{Name: "fake", New: func() sdk.Plugin { return fp }}}, DataDir: t.TempDir()})
	defer h.Close()
	if err := h.Call(context.Background(), "fake", wire.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.env == nil || fp.env.NetPolicy == nil {
		t.Fatalf("plugin did not receive the policy: %+v", fp.env)
	}
	if got := fp.env.NetPolicy.Allowed; len(got) != 1 || got[0] != "[127.0.0.1/32]:8008" || fp.env.NetPolicy.AllowPrivate {
		t.Fatalf("policy = %+v", fp.env.NetPolicy)
	}
}
