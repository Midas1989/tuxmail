// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

// fakeBinary builds testdata/fakeplugin as tuxmail-plugin-fake.
func fakeBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "tuxmail-plugin-test")
		if buildErr != nil {
			return
		}
		name := BinaryPrefix + "fake"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(buildDir, name), "./testdata/fakeplugin")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = errors.New(string(out))
		}
	})
	if buildErr != nil {
		t.Skipf("cannot build fake plugin: %v", buildErr)
	}
	return buildDir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	for _, n := range []string{"tuxmail-plugin-whatsapp" + exe, "tuxmail-plugin-x.sha256", "other" + exe} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dev := t.TempDir()
	if err := os.WriteFile(filepath.Join(dev, "tuxmail-plugin-whatsapp"+exe), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	ms := Discover(dev, dir, filepath.Join(dir, "missing"))
	if len(ms) != 1 || ms[0].Name != "whatsapp" || filepath.Dir(ms[0].Path) != dev {
		t.Fatalf("discover: %+v", ms)
	}
}

func TestLifecycleCrashRestart(t *testing.T) {
	dir := fakeBinary(t)
	var started atomic.Int32
	var mu sync.Mutex
	var states []string
	h := New(Options{Dirs: []string{dir}, DataDir: t.TempDir(), HealthInterval: time.Hour, BackoffMin: 50 * time.Millisecond,
		Config:    func(string) map[string]string { return map[string]string{"crash_on": "send"} },
		OnStarted: func(context.Context, string) { started.Add(1) },
		OnStatus: func(s Status) {
			mu.Lock()
			states = append(states, s.State)
			mu.Unlock()
		}})
	defer h.Close()
	if st := h.Status("fake"); st.State != StateStopped {
		t.Fatalf("before start: %+v", st)
	}
	ctx := context.Background()
	var accs wire.AccountList
	if err := h.Call(ctx, "fake", wire.MethodListAccounts, nil, &accs); err != nil {
		t.Fatal(err)
	}
	st := h.Status("fake")
	if st.State != StateRunning || st.Info.Version != "1.0.0-test" || st.PID == 0 {
		t.Fatalf("running: %+v", st)
	}
	firstPID := st.PID
	// The plugin exits during the call: the call fails, the host survives
	// and restarts the plugin.
	err := h.Call(ctx, "fake", wire.MethodSend, wire.SendRequest{Account: "a", ChatID: "c", Text: "boom"}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("crash call: %v", err)
	}
	waitFor(t, "restart", func() bool { s := h.Status("fake"); return s.State == StateRunning && s.PID != firstPID })
	if s := h.Status("fake"); s.Restarts != 1 || s.LastError == "" {
		t.Fatalf("after restart: %+v", s)
	}
	waitFor(t, "OnStarted twice", func() bool { return started.Load() == 2 })
	if err := h.Call(ctx, "fake", wire.MethodPing, nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sawCrash := false
	for _, s := range states {
		sawCrash = sawCrash || s == StateCrashed
	}
	mu.Unlock()
	if !sawCrash {
		t.Fatalf("states: %v", states)
	}
	h.Stop("fake")
	waitFor(t, "stopped", func() bool { return h.Status("fake").State == StateStopped })
}

func TestCrashLoopFails(t *testing.T) {
	dir := fakeBinary(t)
	h := New(Options{Dirs: []string{dir}, DataDir: t.TempDir(), BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
		MaxCrashes: 3, Config: func(string) map[string]string { return map[string]string{"crash_on": "hello"} }})
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := h.Ensure(ctx, "fake")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ensure: %v", err)
	}
	st := h.Status("fake")
	if st.State != StateFailed || st.Restarts != 3 {
		t.Fatalf("status: %+v", st)
	}
	// A manual restart clears the failed state (and fails again here).
	if err := h.Restart("fake"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed again", func() bool { s := h.Status("fake"); return s.State == StateFailed && s.Restarts == 6 })
}

func TestMissingPlugin(t *testing.T) {
	h := New(Options{DataDir: t.TempDir()})
	defer h.Close()
	if err := h.Call(context.Background(), "nope", wire.MethodPing, nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if st := h.Status("nope"); st.State != StateMissing {
		t.Fatal(st)
	}
}

// TestInProcessPairing exercises the protocol round trip: pairing events,
// input, success and events of a linked account.
func TestInProcessPairing(t *testing.T) {
	fp := fake.New("fake", "whatsapp")
	evs := make(chan wire.Event, 32)
	h := New(Options{Static: []Manifest{{Name: "fake", New: func() sdk.Plugin { return fp }}}, DataDir: t.TempDir(),
		OnEvent: func(_ string, ev wire.Event) { evs <- ev }})
	defer h.Close()
	ctx := context.Background()
	if err := h.Call(ctx, "fake", wire.MethodStartPairing, wire.PairRequest{PairingID: "p1", Account: "acc1", Method: wire.PairQR}, nil); err != nil {
		t.Fatal(err)
	}
	next := func() wire.Event {
		select {
		case ev := <-evs:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return wire.Event{}
	}
	if ev := next(); ev.Pairing == nil || ev.Pairing.Kind != wire.PairEventQR || ev.Pairing.QR != "fake://pair/acc1" {
		t.Fatalf("qr: %+v", ev)
	}
	if ev := next(); ev.Pairing.Kind != wire.PairEventInput || ev.Pairing.Field != "confirm" {
		t.Fatalf("input: %+v", ev.Pairing)
	}
	if err := h.Call(ctx, "fake", wire.MethodPairingInput, wire.PairInput{PairingID: "p1", Field: "confirm", Value: "ok"}, nil); err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.Pairing.Kind != wire.PairEventSuccess || ev.Pairing.Account.Self.Phone != "+4900000000" {
		t.Fatalf("success: %+v", ev.Pairing)
	}
	var res wire.SendResult
	err := h.Call(ctx, "fake", wire.MethodSend, wire.SendRequest{Account: "acc1", To: &wire.Contact{Phone: "+491701234567"}, Text: "hi"}, &res)
	if err != nil || res.ChatID != "+491701234567" || res.ID == "" {
		t.Fatal(res, err)
	}
	if ev := next(); ev.Kind != wire.EventReceipt || ev.Receipt.MessageIDs[0] != res.ID {
		t.Fatalf("receipt: %+v", ev)
	}
	err = h.Call(ctx, "fake", wire.MethodSend, wire.SendRequest{Account: "nope", ChatID: "x", Text: "hi"}, nil)
	if wire.ErrorCode(err) != wire.CodeNotLinked {
		t.Fatalf("not linked: %v", err)
	}
	err = h.Call(ctx, "fake", wire.MethodSend, wire.SendRequest{Account: "acc1", ChatID: "x", Media: &wire.OutMedia{Path: "/etc/passwd"}}, nil)
	if wire.ErrorCode(err) != wire.CodeInvalid {
		t.Fatalf("path check: %v", err)
	}
}
