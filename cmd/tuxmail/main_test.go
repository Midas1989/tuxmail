// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
)

func writeConfig(t *testing.T, extra string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	p := filepath.Join(dir, "config.yaml")
	y := "listen: \"127.0.0.1:0\"\npublic_url: \"https://mail.example.de\"\ndata_dir: \"" + data + "\"\n" + extra
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := runCLI(t, "", "version"); code != 0 || !strings.HasPrefix(out, "TuxMail ") {
		t.Fatal(code, out)
	}
	if code, _, _ := runCLI(t, "", "bogus"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := runCLI(t, "", "user"); code != 2 {
		t.Fatal(code)
	}
}

func TestCheckConfig(t *testing.T) {
	p, _ := writeConfig(t, "")
	if code, out, errs := runCLI(t, "", "check-config", "--config", p); code != 0 || !strings.Contains(out, "valid") {
		t.Fatal(code, out, errs)
	}
	bad, _ := writeConfig(t, "auth: {registration: open}\n")
	if code, _, errs := runCLI(t, "", "check-config", "--config", bad); code != 1 || !strings.Contains(errs, "registration") {
		t.Fatal(code, errs)
	}
	tlsBad, _ := writeConfig(t, "tls: {mode: file, cert_file: /nonexistent.crt, key_file: /nonexistent.key}\n")
	if code, _, _ := runCLI(t, "", "check-config", "--config", tlsBad); code != 1 {
		t.Fatal(code)
	}
	if code, _, _ := runCLI(t, "", "check-config", "--config", "/nonexistent/config.yaml"); code != 1 {
		t.Fatal(code)
	}
}

func TestUserAddIfNotExists(t *testing.T) {
	p, data := writeConfig(t, "")
	// The exact invocation of the Ansible playbook.
	args := []string{"user", "add", "--config", p, "--username", "admin", "--email", "admin@example.de", "--admin", "--password-stdin", "--if-not-exists"}
	code, out, errs := runCLI(t, "a very good admin password\n", args...)
	if code != 0 || !strings.Contains(out, "created user admin") || strings.Contains(out, "password:") {
		t.Fatal(code, out, errs)
	}
	for _, f := range []string{"server.db", "master.key", "users/1"} {
		if _, err := os.Stat(filepath.Join(data, f)); err != nil {
			t.Fatal(f, err)
		}
	}
	// Second run: exit 0, no change.
	code, out, _ = runCLI(t, "another password entirely\n", args...)
	if code != 0 || !strings.Contains(out, "already exists") {
		t.Fatal(code, out)
	}
	// Without --if-not-exists it fails.
	code, _, errs = runCLI(t, "x\n", "user", "add", "--config", p, "--username", "admin", "--password-stdin")
	if code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatal(code, errs)
	}
	// The original password still works.
	cfg, _ := config.Load(p)
	s, err := auth.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Login(context.Background(), auth.LoginRequest{Login: "admin", Password: "a very good admin password"})
	if err != nil || !res.User.IsAdmin() {
		t.Fatal(err)
	}
	s.Close()
	// Weak password via stdin is rejected.
	if code, _, errs := runCLI(t, "short\n", "user", "add", "--config", p, "--username", "weak", "--password-stdin"); code != 1 || !strings.Contains(errs, "password") {
		t.Fatal(code, errs)
	}
}

func TestUserCommands(t *testing.T) {
	p, _ := writeConfig(t, "")
	code, out, errs := runCLI(t, "", "user", "add", "--config", p, "--username", "bob", "--email", "bob@example.de", "--json")
	if code != 0 {
		t.Fatal(code, errs)
	}
	var added struct {
		Created  bool
		Password string
		User     auth.User
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || !added.Created || len(added.Password) < 20 || added.User.Role != auth.RoleUser {
		t.Fatal(out, err)
	}
	runCLI(t, "", "user", "add", "--config", p, "--username", "root", "--admin")

	code, out, _ = runCLI(t, "", "user", "list", "--config", p)
	if code != 0 || !strings.Contains(out, "bob@example.de") || !strings.Contains(out, "USERNAME") {
		t.Fatal(out)
	}
	// Positional username before or after flags.
	if code, out, errs := runCLI(t, "", "user", "disable", "bob", "--config", p); code != 0 || !strings.Contains(out, "disabled") {
		t.Fatal(code, out, errs)
	}
	code, out, _ = runCLI(t, "", "user", "list", "--config", p, "--json")
	var users []auth.User
	json.Unmarshal([]byte(out), &users)
	if len(users) != 2 || !users[0].Disabled {
		t.Fatal(out)
	}
	if code, _, _ := runCLI(t, "", "user", "enable", "--config", p, "bob"); code != 0 {
		t.Fatal(code)
	}
	if code, out, _ := runCLI(t, "", "user", "set-role", "--config", p, "bob", "readonly"); code != 0 || !strings.Contains(out, "readonly") {
		t.Fatal(code, out)
	}
	if code, _, _ := runCLI(t, "", "user", "set-role", "--config", p, "bob", "root"); code != 1 {
		t.Fatal(code)
	}
	if code, _, errs := runCLI(t, "", "user", "set-role", "--config", p, "root", "user"); code != 1 || !strings.Contains(errs, "last") {
		t.Fatal("last admin demoted", code, errs)
	}
	code, out, _ = runCLI(t, "", "user", "reset-password", "--config", p, "bob")
	if code != 0 || !strings.Contains(out, "password: ") {
		t.Fatal(code, out)
	}
	if code, out, _ := runCLI(t, "brand new password!\n", "user", "reset-password", "--config", p, "bob", "--password-stdin"); code != 0 || strings.Contains(out, "password: ") {
		t.Fatal(code, out)
	}
	if code, _, _ := runCLI(t, "", "user", "reset-2fa", "--config", p, "bob"); code != 0 {
		t.Fatal(code)
	}
	if code, out, _ := runCLI(t, "", "user", "unlock", "--config", p, "bob"); code != 0 || !strings.Contains(out, "cleared") {
		t.Fatal(code, out)
	}
	if code, _, _ := runCLI(t, "", "user", "unlock", "--config", p, "nobody"); code != 1 {
		t.Fatal("unlock of an unknown user", code)
	}
	if code, _, errs := runCLI(t, "", "user", "disable", "--config", p, "nobody"); code != 1 || !strings.Contains(errs, "not found") {
		t.Fatal(code, errs)
	}
	if code, _, _ := runCLI(t, "", "user", "delete", "--config", p, "bob"); code != 2 {
		t.Fatal("delete without --yes", code)
	}
	if code, _, _ := runCLI(t, "", "user", "delete", "--config", p, "bob", "--yes"); code != 0 {
		t.Fatal(code)
	}
	code, out, _ = runCLI(t, "", "user", "list", "--config", p, "--json")
	json.Unmarshal([]byte(out), &users)
	if len(users) != 1 {
		t.Fatal(out)
	}
}
