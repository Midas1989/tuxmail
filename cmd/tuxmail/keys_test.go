// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/config"
)

func TestKeysCommands(t *testing.T) {
	p, data := writeConfig(t, "")
	if code, _, errs := runCLI(t, "ein sehr gutes passwort\n", "user", "add", "--config", p, "--username", "admin", "--admin", "--password-stdin"); code != 0 {
		t.Fatal(errs)
	}
	status := func() map[string]any {
		t.Helper()
		code, out, errs := runCLI(t, "", "keys", "status", "--config", p, "--json")
		if code != 0 {
			t.Fatal(errs)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	st := status()
	old := st["primary"].(string)
	if st["kids"].(map[string]any)[old].(float64) < 1 || st["masterKeyPresent"] != true {
		t.Fatal(st)
	}
	code, out, errs := runCLI(t, "", "keys", "rotate", "--config", p)
	if code != 0 || !strings.Contains(out, "keys rewrap") {
		t.Fatal(out, errs)
	}
	if code, _, _ := runCLI(t, "", "keys", "retire", "--config", p, "--kid", old); code == 0 {
		t.Fatal("key in use retired")
	}
	if code, out, errs := runCLI(t, "", "keys", "rewrap", "--config", p); code != 0 || !strings.Contains(out, "converted") {
		t.Fatal(out, errs)
	}
	if code, out, errs := runCLI(t, "", "keys", "retire", "--config", p, "--kid", old); code != 0 {
		t.Fatal(out, errs)
	}
	if code, out, errs := runCLI(t, "", "audit", "verify", "--config", p); code != 0 || !strings.Contains(out, "intact") {
		t.Fatal(out, errs)
	}
	if code, _, _ := runCLI(t, "", "audit", "--config", p); code == 0 {
		t.Fatal("audit without verify")
	}
	if code, _, _ := runCLI(t, "", "keys", "rollback", "--config", p); code == 0 {
		t.Fatal("rollback without --yes")
	}
	if code, out, errs := runCLI(t, "", "keys", "rollback", "--config", p, "--yes"); code != 0 {
		t.Fatal(out, errs)
	}
	if _, err := os.Stat(filepath.Join(data, "secretbox.json")); err == nil {
		t.Fatal("key ring left in place after rollback")
	}
	// Upgrading again works and finds the user.
	if code, out, errs := runCLI(t, "", "user", "list", "--config", p); code != 0 || !strings.Contains(out, "admin") {
		t.Fatal(out, errs)
	}
	if code, _, _ := runCLI(t, "", "keys", "unsinn", "--config", p); code == 0 {
		t.Fatal("unknown subcommand")
	}
}

func TestLogRedaction(t *testing.T) {
	var buf bytes.Buffer
	log, _, err := newLogger(config.Log{Level: "info", Format: "json"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("x", "password", "geheim1", "device_token", "geheim2", "Client-Secret", "geheim3", slog.Group("g", "api_key", "geheim4"), "request_id", "sichtbar", "status", 200)
	got := buf.String()
	for _, leak := range []string{"geheim1", "geheim2", "geheim3", "geheim4"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s in the log: %s", leak, got)
		}
	}
	if !strings.Contains(got, "sichtbar") || !strings.Contains(got, `"status":200`) {
		t.Fatal(got)
	}
}
