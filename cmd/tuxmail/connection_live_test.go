// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/devserver"
)

// The whole way, as a person walks it: the core of the desktop app (no window)
// against a real `tuxmail serve` process with real IMAP and SMTP on loopback.
// Connect, restart, read a mail, stop the server, read and write offline,
// restart offline, start the server: the mail arrives exactly once.
//
// Builds the binary, so it only runs on request: TUXMAIL_LIVE_SERVE=1.
func TestLiveDesktopAgainstServe(t *testing.T) {
	if os.Getenv("TUXMAIL_LIVE_SERVE") == "" {
		t.Skip("set TUXMAIL_LIVE_SERVE=1")
	}
	ctx := context.Background()
	mail, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	defer mail.Close()
	if err := mail.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "tuxmail")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr
	cfg := filepath.Join(dir, "config.yaml")
	cfgText := fmt.Sprintf("listen: %q\npublic_url: %q\ndata_dir: %q\nsecurity: {allowed_networks: [%q, %q]}\n", addr, base, filepath.Join(dir, "server"), mail.IMAPAddr(), mail.SMTPAddr())
	if err := os.WriteFile(cfg, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	const password = "correct horse battery staple"
	add := exec.Command(bin, "user", "add", "--config", cfg, "--username", "anna", "--email", devserver.AnnaAddr, "--password-stdin")
	add.Stdin = strings.NewReader(password + "\n")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("user add: %v\n%s", err, out)
	}

	var serverLog bytes.Buffer
	var server *exec.Cmd
	startServer := func() {
		t.Helper()
		server = exec.Command(bin, "serve", "--config", cfg)
		server.Stdout, server.Stderr = &serverLog, &serverLog
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			if c, err := net.Dial("tcp", addr); err == nil {
				c.Close()
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("server did not start:\n%s", serverLog.String())
			}
		}
	}
	stopServer := func() {
		t.Helper()
		if server == nil {
			return
		}
		_ = server.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- server.Wait() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = server.Process.Kill()
			<-done
		}
		server = nil
	}
	startServer()
	defer stopServer()

	// The mail account lives on the server.
	hc := &http.Client{Timeout: 20 * time.Second}
	js, _ := json.Marshal(map[string]string{"username": "anna", "password": password, "device_name": "Einrichtung"})
	res, err := hc.Post(base+"/api/device/login", "application/json", bytes.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&tok)
	res.Body.Close()
	api := func(method, path string, in, out any) int {
		t.Helper()
		var rd io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if out != nil {
			_ = json.NewDecoder(r.Body).Decode(out)
		}
		return r.StatusCode
	}
	srvConf := func(a string) map[string]any {
		h, p, _ := net.SplitHostPort(a)
		port, _ := strconv.Atoi(p)
		return map[string]any{"host": h, "port": port, "tls": "none", "auth": "password", "username": devserver.AnnaAddr}
	}
	if st := api("POST", "/api/accounts", map[string]any{"kind": "imap", "displayName": "Anna", "email": devserver.AnnaAddr, "name": "Anna Berger",
		"password": devserver.DefaultPassword, "incoming": srvConf(mail.IMAPAddr()), "outgoing": srvConf(mail.SMTPAddr())}, nil); st/100 != 2 {
		t.Fatalf("create account: %d\n%s", st, serverLog.String())
	}

	// The desktop core, with a real (portable) vault.
	data := filepath.Join(dir, "desktop")
	var logs bytes.Buffer
	const vaultPass = "ein anderes langes passwort"
	var a *app.App
	var closeApp func()
	open := func() {
		t.Helper()
		log := slog.New(slog.NewTextHandler(&logs, nil))
		ap, st, err := openDesktopApp(ctx, dataLocation{Dir: data, Portable: true}, log, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := ap.UnlockVault(ctx, vaultPass); err != nil {
			t.Fatal(err)
		}
		ap.Start()
		a = ap
		closeApp = func() { ap.Close(); st.Close() }
	}
	open()
	defer func() { closeApp() }()

	login, err := a.ServerLogin(ctx, base, app.ServerLoginInput{Username: "anna", Password: password})
	if err != nil || login.Status != "approved" {
		t.Fatalf("login: %+v %v", login, err)
	}
	if err := a.ImportProvisioning(ctx, app.ProvisioningImport{ServerURL: base, Mode: app.ConnModeThin}); err != nil {
		t.Fatal(err)
	}
	waitState := func(want string, within time.Duration) {
		t.Helper()
		for deadline := time.Now().Add(within); ; time.Sleep(50 * time.Millisecond) {
			s, err := a.ServerConnectionStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if s.State == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("state %q, want %q", s.State, want)
			}
		}
	}
	request := func(method, path string, body any) (int, []byte, map[string]string) {
		t.Helper()
		in := app.ServerProxyRequest{Method: method, Path: path, Headers: map[string]string{"Accept": "application/json"}}
		if body != nil {
			b, _ := json.Marshal(body)
			in.Body, in.Headers["Content-Type"] = base64.StdEncoding.EncodeToString(b), "application/json"
		}
		start := time.Now()
		r, err := a.ServerRequest(ctx, in)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		if took := time.Since(start); took > 30*time.Second {
			t.Fatalf("%s %s took %v", method, path, took)
		}
		raw, _ := base64.StdEncoding.DecodeString(r.Body)
		return r.Status, raw, r.Headers
	}
	waitState(app.ConnStateOnline, 20*time.Second)

	// Restart: still connected.
	closeApp()
	open()
	s, err := a.ServerConnectionStatus(ctx)
	if err != nil || !s.Connected || s.Username != "anna" {
		t.Fatalf("after restart: %+v %v", s, err)
	}
	waitState(app.ConnStateOnline, 20*time.Second)

	// Reading a mail through the server, the same call as the web client.
	const listPath = "/api/messages?scope=unified&role=inbox&offset=0&limit=50"
	var msgID int64
	var identity int64
	for deadline := time.Now().Add(30 * time.Second); msgID == 0; time.Sleep(200 * time.Millisecond) {
		st, raw, _ := request("GET", listPath, nil)
		var p struct {
			Items []struct {
				ID      int64  `json:"id"`
				Subject string `json:"subject"`
			} `json:"items"`
		}
		if st == 200 && json.Unmarshal(raw, &p) == nil {
			for _, m := range p.Items {
				if m.Subject == "Mittagessen morgen?" {
					msgID = m.ID
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("seed mail not on the server: %d %s", st, raw)
		}
	}
	msgPath := "/api/messages/" + strconv.FormatInt(msgID, 10)
	if st, raw, _ := request("GET", msgPath, nil); st != 200 || !bytes.Contains(raw, []byte("Mittagessen")) {
		t.Fatalf("read: %d %s", st, raw)
	}
	st, raw, _ := request("GET", "/api/identities", nil)
	var ids []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &ids); st != 200 || err != nil || len(ids) == 0 {
		t.Fatalf("identities: %d %s", st, raw)
	}
	identity = ids[0].ID

	// The server goes away.
	stopServer()
	offlineRead := func() {
		t.Helper()
		st, raw, h := request("GET", msgPath, nil)
		if st != 200 || !bytes.Contains(raw, []byte("Mittagessen")) || h["X-Tuxmail-Offline"] != "1" {
			t.Fatalf("offline read: %d %s %v", st, raw, h)
		}
		st, raw, _ = request("POST", "/api/search", map[string]any{"text": "mittagessen", "mode": "simple"})
		if st != 200 || !bytes.Contains(raw, []byte("Mittagessen")) {
			t.Fatalf("offline search: %d %s", st, raw)
		}
	}
	offlineRead()
	waitState(app.ConnStateOffline, 10*time.Second)
	const subject = "Offline geschrieben 42"
	send := map[string]any{"channel": "email", "identityId": identity, "subject": subject, "text": "Hallo Ben", "html": "<p>Hallo Ben</p>",
		"to": []map[string]string{{"email": devserver.BenAddr}}, "cc": []any{}, "bcc": []any{}, "attachments": []any{}}
	if st, raw, _ := request("POST", "/api/send", send); st != 200 || !bytes.Contains(raw, []byte(`"offline":true`)) {
		t.Fatalf("offline send: %d %s", st, raw)
	}
	if st, _, _ := request("POST", "/api/messages/flags", map[string]any{"ids": []int64{msgID}, "add": []string{`\Flagged`}, "remove": []string{}}); st != 204 {
		t.Fatalf("offline mark: %d", st)
	}
	// Restart without a server: starts, shows everything, the mail still waits.
	closeApp()
	began := time.Now()
	open()
	offlineRead()
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("start offline took %v", took)
	}
	s, err = a.ServerConnectionStatus(ctx)
	if err != nil || len(s.Outbox) != 2 {
		t.Fatalf("outbox after restart: %+v %v", s, err)
	}
	sentCount := func() int {
		t.Helper()
		msgs, err := mail.Messages(devserver.BenAddr, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range msgs {
			if m.Subject == subject {
				n++
			}
		}
		return n
	}
	if n := sentCount(); n != 0 {
		t.Fatalf("delivered %d while the server was down", n)
	}

	// The server is back: without doing anything, it goes out, once.
	startServer()
	waitState(app.ConnStateOnline, 90*time.Second)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		s, err = a.ServerConnectionStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Outbox) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox: %+v\n%s", s.Outbox, serverLog.String())
		}
	}
	for deadline := time.Now().Add(20 * time.Second); sentCount() == 0 && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
	}
	// Restarts must not send it again.
	closeApp()
	open()
	waitState(app.ConnStateOnline, 20*time.Second)
	time.Sleep(2 * time.Second)
	if n := sentCount(); n != 1 {
		t.Fatalf("mail arrived %d times", n)
	}
	st, raw, _ = request("GET", msgPath, nil)
	if st != 200 || !bytes.Contains(raw, []byte(`\\Flagged`)) {
		t.Fatalf("mark not replayed: %d %s", st, raw)
	}
	// Leaving: the server forgets the device.
	token := ""
	if v, err := a.Secrets().Get("server:" + base + ":token"); err == nil {
		token = string(v)
	}
	r, err := a.ServerDisconnect(ctx, app.DisconnectInput{DeleteCache: true})
	if err != nil || !r.Revoked {
		t.Fatalf("disconnect: %+v %v", r, err)
	}
	if token == "" {
		t.Fatal("no token found")
	}
	tok.AccessToken = token
	if st := api("GET", "/api/accounts", nil, nil); st != 401 {
		t.Fatalf("revoked token answers %d", st)
	}
	// The token never touched the desktop's database or log.
	closeApp()
	for _, f := range []string{"tuxmail.db", "tuxmail.db-wal"} {
		b, err := os.ReadFile(filepath.Join(data, f))
		if err == nil && bytes.Contains(b, []byte(token)) {
			t.Errorf("%s contains the token", f)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Error("the log contains the token")
	}
	open()
}
