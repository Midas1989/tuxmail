// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Midas1989/Mailclient/internal/messenger/signalcli"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
)

func nativeTarball(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho fake\n")
	if err := tw.WriteHeader(&tar.Header{Name: "signal-cli", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestComponentInstall(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PATH", t.TempDir()) // no system signal-cli
	t.Setenv("TUXMAIL_SIGNAL_SIGNAL_CLI_PATH", "")
	tarball := nativeTarball(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tarball) }))
	defer srv.Close()
	h := sha256.Sum256(tarball)

	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n := &recNotifier{}
	pins := signalcli.Pins{"SIGNAL_CLI_VERSION": "9.9.9", "SIGNAL_CLI_NATIVE_URL": srv.URL + "/signal-cli-9.9.9-Linux-native.tar.gz",
		"SIGNAL_CLI_NATIVE_SHA256": hex.EncodeToString(h[:])}
	svc, err := New(Options{Store: st, DataDir: dir, Dirs: []string{}, Notifier: n,
		Components: ComponentOptions{Pins: pins, Client: srv.Client(), GOOS: "linux", GOARCH: "amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if svc.Component("whatsapp") != nil {
		t.Fatal("whatsapp has no component")
	}
	if err := svc.InstallComponent(ctx, "whatsapp"); !errors.Is(err, ErrNoComponent) {
		t.Fatal(err)
	}
	c := svc.Component("signal")
	if c.State != ComponentMissing || !c.Installable || c.InstallVersion != "9.9.9" {
		t.Fatalf("before: %+v", c)
	}
	if err := svc.InstallComponent(ctx, "signal"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "installed", func() bool { return svc.Component("signal").State == ComponentReady })
	c = svc.Component("signal")
	if c.Source != signalcli.SourceInstalled || c.Version != "9.9.9" || c.Path != filepath.Join(dir, "plugins", "signal-cli", "signal-cli") {
		t.Fatalf("after: %+v", c)
	}

	// Not pinned (empty checksum): not installable, install refused.
	pins["SIGNAL_CLI_NATIVE_SHA256"] = ""
	svc.opts.Components.GOARCH = "amd64"
	if err := svc.InstallComponent(ctx, "signal"); !errors.Is(err, signalcli.ErrNotPinned) {
		t.Fatalf("unpinned: %v", err)
	}
	// Unsupported platform.
	svc.opts.Components.GOOS = "plan9"
	if err := svc.InstallComponent(ctx, "signal"); !errors.Is(err, signalcli.ErrUnsupported) {
		t.Fatalf("plan9: %v", err)
	}
}

func TestReasonFor(t *testing.T) {
	if ReasonFor(wire.CodeComponentMissing) != ReasonComponentMissing || ReasonFor(wire.CodeUnavailable) != ReasonUnavailable || ReasonFor(0) != "" {
		t.Fatal("reasons")
	}
}
