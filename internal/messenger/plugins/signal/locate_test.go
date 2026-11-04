// SPDX-License-Identifier: AGPL-3.0-or-later

package signal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/signalcli"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// TestHelperSignalCLI is a fake signal-cli (JSON-RPC on stdin/stdout) when
// started by the wrapper script of installFakeCLI.
func TestHelperSignalCLI(t *testing.T) {
	if os.Getenv("TUXMAIL_FAKE_SIGNAL_CLI") != "1" {
		t.Skip("helper process")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if log := os.Getenv("TUXMAIL_FAKE_SIGNAL_CLI_LOG"); log != "" {
		_ = os.WriteFile(log, []byte(strings.Join(args, "\n")), 0o600)
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("signal-cli 0.0.0-fake")
		os.Exit(0)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "startLink":
			result = map[string]any{"deviceLinkUri": "sgnl://linkdevice?uuid=fake&pub_key=fake"}
		case "finishLink":
			result = map[string]any{"number": "+4917099999999", "uuid": "u-fake"}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		fmt.Println(string(b))
	}
	os.Exit(0)
}

// installFakeCLI puts a native-layout signal-cli into dir that runs the
// helper above.
func installFakeCLI(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nTUXMAIL_FAKE_SIGNAL_CLI=1 exec %q -test.run='^TestHelperSignalCLI$' -- \"$@\"\n", self)
	p := filepath.Join(dir, "signal-cli")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The plugin finds signal-cli installed on demand next to its data
// directory (<data dir>/plugins/signal-cli) and links with it.
func TestPairWithInstalledSignalCLI(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "plugins", "signal") // what the host hands the plugin
	installFakeCLI(t, filepath.Join(root, "plugins", signalcli.DirName))
	log := filepath.Join(root, "args.log")
	t.Setenv("TUXMAIL_FAKE_SIGNAL_CLI_LOG", log)
	t.Setenv("TUXMAIL_SIGNAL_SIGNAL_CLI_PATH", "")
	t.Setenv("PATH", root) // no signal-cli in PATH

	env := sdk.NewTestEnv(dataDir, nil, func(wire.Event) {})
	p := New()
	if err := p.Init(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	var qr string
	acc, err := p.Pair(context.Background(), wire.PairRequest{Account: "a1", Method: wire.PairQR},
		sdk.NewTestPairing("a1", func(pe wire.PairEvent) {
			if pe.QR != "" {
				qr = pe.QR
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(qr, "sgnl://linkdevice") || acc.Self.Phone != "+4917099999999" {
		t.Fatalf("qr %q acc %+v", qr, acc)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(string(b), "\n")
	if len(args) < 3 || args[0] != "--config" || args[1] != filepath.Join(dataDir, "a1", "signal-cli") || !strings.Contains(string(b), "jsonRpc") {
		t.Fatalf("args %q", args)
	}
}

// Without signal-cli the pairing fails with CodeComponentMissing (the UI
// offers the install); a wrong explicit path is reported as such.
func TestPairWithoutSignalCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("TUXMAIL_SIGNAL_SIGNAL_CLI_PATH", "")
	for _, tt := range []struct {
		cfg  map[string]string
		code int
		msg  string
	}{
		{nil, wire.CodeComponentMissing, "not installed"},
		{map[string]string{"signal_cli_path": filepath.Join(root, "nope")}, wire.CodeUnavailable, "signal_cli_path"},
	} {
		env := sdk.NewTestEnv(filepath.Join(root, "plugins", "signal"), tt.cfg, func(wire.Event) {})
		p := New()
		if err := p.Init(context.Background(), env); err != nil {
			t.Fatal(err)
		}
		_, err := p.Pair(context.Background(), wire.PairRequest{Account: "a1", Method: wire.PairQR}, sdk.NewTestPairing("a1", func(wire.PairEvent) {}))
		if wire.ErrorCode(err) != tt.code || !strings.Contains(err.Error(), tt.msg) {
			t.Fatalf("cfg %v: err = %v", tt.cfg, err)
		}
	}
}

func TestHint(t *testing.T) {
	if !strings.Contains(Hint("java.lang.UnsatisfiedLinkError: no signal_jni"), "libsignal") {
		t.Fatal("libsignal hint")
	}
	if !strings.Contains(Hint("UnsupportedClassVersionError"), "Java 21") || Hint("ok") != "" {
		t.Fatal("java hint")
	}
}
