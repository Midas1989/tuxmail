// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
)

// The desktop client against a real server (handlers, auth, per-user Apps),
// in one process: connection, revocation, offline, exactly-once sending.

type countingSender struct {
	mu   sync.Mutex
	sent []string
}

func (s *countingSender) Send(_ context.Context, r app.SendRequest) (app.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, r.Message.Subject)
	return app.SendResult{MessageID: int64(len(s.sent))}, nil
}

func (s *countingSender) count(subject string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.sent {
		if x == subject {
			n++
		}
	}
	return n
}

// keepSecrets is a secret store that survives restarting the desktop App.
type keepSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *keepSecrets) Get(ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[ref]; ok {
		return v, nil
	}
	return nil, secrets.ErrNotFound
}

func (k *keepSecrets) Set(ref string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[ref] = append([]byte{}, v...)
	return nil
}

func (k *keepSecrets) Delete(ref string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, ref)
	return nil
}

// gate can cut the path to the server.
type gate struct {
	next http.Handler
	down atomic.Bool
	// loseSend lets this many POST /api/send reach the server and then loses
	// their answers. (Go's transport itself repeats a request once if it
	// carries an Idempotency-Key: two lost answers reach the outbox.)
	loseSend atomic.Int32
}

type discardResponse struct {
	h http.Header
}

func (d *discardResponse) Header() http.Header       { return d.h }
func (d *discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponse) WriteHeader(int)           {}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		panic(http.ErrAbortHandler)
	}
	if r.URL.Path == "/api/send" && g.loseSend.Add(-1) >= 0 {
		g.next.ServeHTTP(&discardResponse{h: http.Header{}}, r)
		panic(http.ErrAbortHandler)
	}
	g.next.ServeHTTP(w, r)
}

type desktop struct {
	t       *testing.T
	dir     string
	secrets *keepSecrets
	logs    *syncBuffer
	a       *app.App
	st      *store.Store
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fastTiming() *app.ConnTiming {
	return &app.ConnTiming{Heartbeat: 30 * time.Millisecond, RetryMin: 20 * time.Millisecond, RetryMax: 50 * time.Millisecond,
		ProbeTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, DataEvery: time.Hour, SettingsSync: time.Hour, RotateAfter: 90 * 24 * time.Hour, RotateRetry: time.Hour}
}

func newDesktop(t *testing.T) *desktop {
	d := &desktop{t: t, dir: t.TempDir(), secrets: &keepSecrets{m: map[string][]byte{}}, logs: &syncBuffer{}}
	d.start()
	t.Cleanup(d.stop)
	return d
}

func (d *desktop) start() {
	d.t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(d.dir, "tuxmail.db"))
	if err != nil {
		d.t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop, HTTPClient: &http.Client{Transport: testTransport, Timeout: 10 * time.Second},
		ConnTiming: fastTiming(), Logger: slog.New(slog.NewTextHandler(d.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		d.t.Fatal(err)
	}
	a.Start()
	d.a, d.st = a, st
}

func (d *desktop) stop() {
	if d.a == nil {
		return
	}
	d.a.Close()
	d.st.Close()
	d.a, d.st = nil, nil
}

func (d *desktop) restart() {
	d.t.Helper()
	d.stop()
	d.start()
}

func (d *desktop) status() app.ConnectionStatus {
	d.t.Helper()
	s, err := d.a.ServerConnectionStatus(context.Background())
	if err != nil {
		d.t.Fatal(err)
	}
	return s
}

func (d *desktop) waitState(want string) {
	d.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if d.status().State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.t.Fatalf("state %q, want %q", d.status().State, want)
}

func (d *desktop) waitOutboxEmpty() {
	d.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(d.status().Outbox) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.t.Fatalf("outbox not empty: %+v", d.status().Outbox)
}

func (d *desktop) login(base, user string) {
	d.t.Helper()
	res, err := d.a.ServerLogin(context.Background(), base, app.ServerLoginInput{Username: user, Password: pw})
	if err != nil || res.Status != "approved" {
		d.t.Fatalf("login: %+v %v", res, err)
	}
}

func (d *desktop) connect(base, user, mode string) {
	d.t.Helper()
	d.login(base, user)
	if err := d.a.ImportProvisioning(context.Background(), app.ProvisioningImport{ServerURL: base, Mode: mode}); err != nil {
		d.t.Fatal(err)
	}
}

func (d *desktop) request(method, path string, body any) (int, []byte, map[string]string) {
	d.t.Helper()
	in := app.ServerProxyRequest{Method: method, Path: path, Headers: map[string]string{"Accept": "application/json"}}
	if body != nil {
		b, _ := json.Marshal(body)
		in.Body = base64.StdEncoding.EncodeToString(b)
		in.Headers["Content-Type"] = "application/json"
	}
	res, err := d.a.ServerRequest(context.Background(), in)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.Body)
	return res.Status, raw, res.Headers
}

// connEnv is a server with demo data and a gate in front of it.
type connEnv struct {
	*env
	g      *gate
	url    string
	sender *countingSender
	user   *auth.User
	demo   demo.Result
	srvApp *app.App
}

func newConnEnv(t *testing.T) *connEnv {
	t.Helper()
	sender := &countingSender{}
	e := newEnv(t, Options{AppOptions: func(_ int64, o *app.Options) { o.Sync = nil; o.Send = nil; o.Sender = sender }})
	u := e.user("anna", auth.RoleUser)
	a, err := e.srv.App(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	dm, err := demo.Seed(context.Background(), a.Store(), a.Blobs(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{next: e.srv.Handler()}
	ts := httptest.NewServer(g)
	t.Cleanup(ts.Close)
	return &connEnv{env: e, g: g, url: ts.URL, sender: sender, user: u, demo: dm, srvApp: a}
}

func (c *connEnv) firstInbox(t *testing.T, d *desktop) (id int64, subject string) {
	t.Helper()
	status, raw, _ := d.request("GET", "/api/messages?scope=unified&role=inbox&offset=0&limit=50", nil)
	if status != 200 {
		t.Fatalf("messages: %d %s", status, raw)
	}
	var p struct {
		Items []struct {
			ID      int64  `json:"id"`
			Subject string `json:"subject"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Items) == 0 {
		t.Fatalf("no messages: %s %v", raw, err)
	}
	return p.Items[0].ID, p.Items[0].Subject
}

func (c *connEnv) identity(t *testing.T) int64 {
	t.Helper()
	ids, err := c.srvApp.ListIdentities(context.Background())
	if err != nil || len(ids) == 0 {
		t.Fatalf("identities: %v", err)
	}
	return ids[0].ID
}

func (c *connEnv) mail(t *testing.T, subject string) map[string]any {
	return map[string]any{"channel": "email", "identityId": c.identity(t), "subject": subject, "text": "Hallo", "html": "",
		"to": []map[string]string{{"name": "Ben", "email": "ben@example.de"}}, "cc": []any{}, "bcc": []any{}, "attachments": []any{}}
}

// The token must only exist in the secret store: not in the database, the
// log, or anything the interface can read.
func assertNoSecret(t *testing.T, d *desktop, secret []byte) {
	t.Helper()
	if len(secret) < 16 {
		t.Fatalf("secret too short for a search: %d", len(secret))
	}
	s := d.status()
	js, _ := json.Marshal(s)
	if bytes.Contains(js, secret) {
		t.Error("status contains the secret")
	}
	if strings.Contains(d.logs.String(), string(secret)) {
		t.Error("log contains the secret")
	}
	err := filepath.Walk(d.dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Contains(b, secret) {
			t.Errorf("%s contains the secret", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSurvivesRestartAndKeepsSecretsOutOfDB(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	s := d.status()
	if !s.Connected || s.Username != "anna" || s.Mode != app.ConnModeThin || s.DeviceName == "" || s.LastConnectedAt == "" {
		t.Fatalf("status: %+v", s)
	}
	token := d.secrets.m["server:"+c.url+":token"]
	key := d.secrets.m["server:"+c.url+":clientkey"]
	if len(token) == 0 || len(key) != 32 {
		t.Fatalf("secrets not stored: %d %d", len(token), len(key))
	}
	if _, raw, _ := d.request("GET", "/api/accounts", nil); bytes.Contains(raw, token) {
		t.Error("answer contains the token")
	}
	d.restart()
	s = d.status()
	if !s.Connected || s.ServerURL != c.url || s.Username != "anna" {
		t.Fatalf("after restart: %+v", s)
	}
	d.waitState(app.ConnStateOnline)
	if status, _, _ := d.request("GET", "/api/accounts", nil); status != 200 {
		t.Fatalf("after restart: %d", status)
	}
	d.stop() // flushes the database files
	d.a, d.st = nil, nil
	d2 := *d
	d2.start()
	defer d2.stop()
	assertNoSecret(t, &d2, token)
	assertNoSecret(t, &d2, key)
}

func TestConnectionServerRevocationKeepsLocalData(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	id, _ := c.firstInbox(t, d)
	if status, _, _ := d.request("GET", "/api/messages/"+i64s(id), nil); status != 200 {
		t.Fatalf("detail %d", status)
	}
	sessions, err := c.svc.ListSessions(context.Background(), c.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if err := c.svc.RevokeSession(context.Background(), c.user.ID, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	d.waitState(app.ConnStateExpired)
	// Everything loaded stays readable, a new mail still waits in the outbox.
	if status, raw, h := d.request("GET", "/api/messages/"+i64s(id), nil); status != 200 || h[http.CanonicalHeaderKey("X-Tuxmail-Offline")] == "" {
		t.Fatalf("offline read after revocation: %d %s %v", status, raw, h)
	}
	if status, _, _ := d.request("POST", "/api/send", c.mail(t, "Nach Widerruf")); status != 200 {
		t.Fatal("send not noted")
	}
	d.restart()
	if s := d.status(); s.State != app.ConnStateExpired || !s.Connected || len(s.Outbox) != 1 {
		t.Fatalf("after restart: %+v", s)
	}
	// Another user may not take over the data of this one.
	c.env.user("ben", auth.RoleUser)
	d.login(c.url, "ben")
	var ae *app.Error
	if err := d.a.ServerReauthComplete(context.Background(), c.url); !errors.As(err, &ae) || ae.Code != "different_user" {
		t.Fatalf("different user: %v", err)
	}
	// Signing in again as the same user: nothing was lost and the mail goes out.
	d.login(c.url, "anna")
	if err := d.a.ServerReauthComplete(context.Background(), c.url); err != nil {
		t.Fatal(err)
	}
	d.waitState(app.ConnStateOnline)
	d.waitOutboxEmpty()
	if n := c.sender.count("Nach Widerruf"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

func TestConnectionOfflineReadSearchAndNoHang(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	id, subject := c.firstInbox(t, d)
	if status, _, _ := d.request("GET", "/api/messages/"+i64s(id), nil); status != 200 {
		t.Fatal(status)
	}
	accs, _ := c.srvApp.ListAccounts(context.Background())
	if status, _, _ := d.request("GET", "/api/accounts", nil); status != 200 {
		t.Fatal(status)
	}
	if status, _, _ := d.request("GET", "/api/accounts/"+i64s(accs[0].ID)+"/folders", nil); status != 200 {
		t.Fatal(status)
	}
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)
	d.restart() // also without a network at start
	if d.status().State == app.ConnStateOnline {
		t.Fatal("online without a network")
	}
	start := time.Now()
	check := func(path string, wantBody string) {
		t.Helper()
		status, raw, h := d.request("GET", path, nil)
		if status != 200 || h["X-Tuxmail-Offline"] != "1" {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		if wantBody != "" && !strings.Contains(string(raw), wantBody) {
			t.Fatalf("%s: %s lacks %q", path, raw, wantBody)
		}
	}
	check("/api/messages/"+i64s(id), subject)
	check("/api/accounts", accs[0].DisplayName)
	check("/api/messages?scope=unified&role=inbox&offset=0&limit=50", subject)
	// A page size never asked for online comes from the index of loaded mails.
	check("/api/messages?scope=unified&role=inbox&offset=0&limit=17", subject)
	// Never-loaded lists are empty, not an error.
	check("/api/tags", "[]")
	// Searching what was loaded.
	word := strings.Fields(subject)[0]
	status, raw, _ := d.request("POST", "/api/search", map[string]any{"text": word, "mode": "simple"})
	if status != 200 || !strings.Contains(string(raw), subject) {
		t.Fatalf("offline search: %d %s", status, raw)
	}
	if st, raw, _ := d.request("GET", "/api/messages?scope=unified&role=inbox&q="+word, nil); st != 200 || !strings.Contains(string(raw), subject) {
		t.Fatalf("offline list search: %d %s", st, raw)
	}
	// What cannot work says so with a clear code.
	if st, raw, _ := d.request("GET", "/api/diagnostics", nil); st != 503 || !strings.Contains(string(raw), `"offline"`) {
		t.Fatalf("unavailable: %d %s", st, raw)
	}
	if st, raw, _ := d.request("POST", "/api/messages/move", map[string]any{"ids": []int64{id}, "folderId": 1}); st != 503 || !strings.Contains(string(raw), `"offline"`) {
		t.Fatalf("move: %d %s", st, raw)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("offline answers took %v", time.Since(start))
	}
}

func TestConnectionOfflineOutboxSendsExactlyOnce(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	id, _ := c.firstInbox(t, d)
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)

	// Written offline, with an attachment.
	body := c.mail(t, "Offline geschrieben")
	in := app.ServerProxyRequest{Method: "POST", Path: "/api/uploads", Headers: map[string]string{"X-Filename": "notiz.txt", "Content-Type": "text/plain"},
		Body: base64.StdEncoding.EncodeToString([]byte("Anhang"))}
	res, err := d.a.ServerRequest(context.Background(), in)
	if err != nil || res.Status != 201 {
		t.Fatalf("upload: %+v %v", res, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.Body)
	var up struct {
		UploadToken string `json:"uploadToken"`
	}
	json.Unmarshal(raw, &up)
	body["attachments"] = []map[string]any{{"uploadToken": up.UploadToken, "filename": "notiz.txt", "mimeType": "text/plain", "delivery": "attach"}}
	status, raw, _ := d.request("POST", "/api/send", body)
	if status != 200 || !strings.Contains(string(raw), `"queued":true`) {
		t.Fatalf("send offline: %d %s", status, raw)
	}
	// A mark, noted and visible at once.
	if st, _, _ := d.request("POST", "/api/messages/flags", map[string]any{"ids": []int64{id}, "add": []string{`\Flagged`}, "remove": []string{}}); st != 204 {
		t.Fatal(st)
	}
	st, raw, _ := d.request("GET", "/api/messages?scope=unified&role=inbox&offset=0&limit=50", nil)
	if st != 200 || !strings.Contains(string(raw), `\\Flagged`) {
		t.Fatalf("mark not visible offline: %s", raw)
	}
	if n := len(d.status().Outbox); n != 2 {
		t.Fatalf("outbox %d", n)
	}
	d.restart() // the outbox survives
	if n := len(d.status().Outbox); n != 2 {
		t.Fatalf("outbox after restart %d", n)
	}
	c.g.down.Store(false)
	d.waitState(app.ConnStateOnline)
	d.waitOutboxEmpty()
	if n := c.sender.count("Offline geschrieben"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	m, err := c.srvApp.GetMessage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	flagged := false
	for _, f := range m.Flags {
		flagged = flagged || f == domain.FlagFlagged
	}
	if !flagged {
		t.Fatalf("mark not replayed: %v", m.Flags)
	}
}

func TestConnectionLostAnswerDoesNotSendTwice(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	c.identity(t) // load before the path is cut
	c.g.loseSend.Store(2)
	status, raw, _ := d.request("POST", "/api/send", c.mail(t, "Antwort verloren"))
	if status != 200 || !strings.Contains(string(raw), `"queued":true`) {
		t.Fatalf("lost answer: %d %s", status, raw)
	}
	d.waitOutboxEmpty()
	d.waitState(app.ConnStateOnline)
	if n := c.sender.count("Antwort verloren"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	// A plain online send: no outbox, once.
	if st, _, _ := d.request("POST", "/api/send", c.mail(t, "Normal")); st != 200 {
		t.Fatal(st)
	}
	if n := c.sender.count("Normal"); n != 1 || len(d.status().Outbox) != 0 {
		t.Fatalf("normal send %d, outbox %+v", n, d.status().Outbox)
	}
}

func TestIdempotencyKeyOnSend(t *testing.T) {
	c := newConnEnv(t)
	hc := &http.Client{Transport: testTransport}
	js, _ := json.Marshal(map[string]string{"username": "anna", "password": pw, "device_name": "Test"})
	lr, err := hc.Post(c.url+"/api/device/login", "application/json", bytes.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(lr.Body).Decode(&tok)
	lr.Body.Close()
	send := func(key string, subject string) (*http.Response, []byte) {
		js, _ := json.Marshal(c.mail(t, subject))
		req, _ := http.NewRequest("POST", c.url+"/api/send", bytes.NewReader(js))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, raw
	}
	r1, b1 := send("key-00000001", "A")
	r2, b2 := send("key-00000001", "A")
	if r1.StatusCode != 200 || r2.StatusCode != 200 || !bytes.Equal(b1, b2) || r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d %s %s", r1.StatusCode, r2.StatusCode, b1, b2)
	}
	if n := c.sender.count("A"); n != 1 {
		t.Fatalf("sent %d", n)
	}
	// Same key, other mail: refused.
	if r, raw := send("key-00000001", "B"); r.StatusCode != 422 || c.sender.count("B") != 0 {
		t.Fatalf("reuse: %d %s", r.StatusCode, raw)
	}
	if r, _ := send("bad key!", "B"); r.StatusCode != 400 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	// Without a key nothing changes.
	send("", "C")
	send("", "C")
	if n := c.sender.count("C"); n != 2 {
		t.Fatalf("no key: %d", n)
	}
	// A run that died midway: never sent again on its own. The record is the
	// one a crash leaves behind: same route, same request, still pending.
	mail, _ := json.Marshal(c.mail(t, "D"))
	sum := sha256.Sum256(mail)
	ctx := context.Background()
	if _, err := c.srvApp.Store().BeginIdempotency(ctx, "key-00000002", "send /api/send", hex.EncodeToString(sum[:]), time.Now().Add(-time.Hour), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if r, raw := send("key-00000002", "D"); r.StatusCode != 409 || !strings.Contains(string(raw), "idempotency_in_doubt") || c.sender.count("D") != 0 {
		t.Fatalf("in doubt: %d %s", r.StatusCode, raw)
	}
	// Still running: come back later.
	if _, err := c.srvApp.Store().BeginIdempotency(ctx, "key-00000003", "send /api/send", hex.EncodeToString(sum[:]), time.Now(), 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if r, raw := send("key-00000003", "D"); r.StatusCode != 409 || !strings.Contains(string(raw), "idempotency_in_progress") || c.sender.count("D") != 0 {
		t.Fatalf("in progress: %d %s", r.StatusCode, raw)
	}
}

func TestConnectionDisconnect(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	// A local account that has nothing to do with the server.
	ctx := context.Background()
	localID, err := d.st.CreateAccount(ctx, domain.Account{Kind: "imap", DisplayName: "Lokal", Enabled: true, Extra: map[string]string{"address": "a@b.de"}})
	if err != nil {
		t.Fatal(err)
	}
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	token := string(d.secrets.m["server:"+c.url+":token"])
	if st, _, _ := d.request("GET", "/api/accounts", nil); st != 200 {
		t.Fatal(st)
	}
	before, _ := c.svc.ListSessions(ctx, c.user.ID)

	// Waiting mail: leaving needs a decision.
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)
	if st, _, _ := d.request("POST", "/api/send", c.mail(t, "Wartet")); st != 200 {
		t.Fatal(st)
	}
	var ae *app.Error
	if _, err := d.a.ServerDisconnect(ctx, app.DisconnectInput{DeleteCache: true}); !errors.As(err, &ae) || ae.Code != "outbox_not_empty" {
		t.Fatalf("outbox guard: %v", err)
	}
	c.g.down.Store(false)
	d.waitState(app.ConnStateOnline)
	d.waitOutboxEmpty()

	res, err := d.a.ServerDisconnect(ctx, app.DisconnectInput{DeleteCache: true})
	if err != nil || !res.Revoked {
		t.Fatalf("disconnect: %+v %v", res, err)
	}
	after, _ := c.svc.ListSessions(ctx, c.user.ID)
	if len(after) != len(before)-1 {
		t.Fatalf("sessions %d -> %d", len(before), len(after))
	}
	hc := &http.Client{Transport: testTransport}
	req, _ := http.NewRequest("GET", c.url+"/api/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("old token still works: %d", r.StatusCode)
	}
	if s := d.status(); s.Connected || s.State != app.ConnStateNone || s.CacheBytes != 0 {
		t.Fatalf("status: %+v", s)
	}
	if _, ok := d.secrets.m["server:"+c.url+":token"]; ok {
		t.Error("token not deleted")
	}
	if _, ok := d.secrets.m["server:"+c.url+":clientkey"]; ok {
		t.Error("client key not deleted")
	}
	if _, err := d.st.GetAccount(ctx, localID); err != nil {
		t.Errorf("local account gone: %v", err)
	}
	if d.a.ThinClientActive() {
		t.Error("still thin")
	}
	d.restart()
	if s := d.status(); s.Connected {
		t.Fatalf("connected after restart: %+v", s)
	}
}

func TestConnectionDisconnectOfflineSaysNotRevoked(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)
	res, err := d.a.ServerDisconnect(context.Background(), app.DisconnectInput{})
	if err != nil || res.Revoked {
		t.Fatalf("offline disconnect: %+v %v", res, err)
	}
}

func TestConnectionMigrationOfOldThinCopies(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	stale, err := d.st.CreateAccount(ctx, domain.Account{Kind: "imap", DisplayName: "Alte Kopie", Enabled: false, Extra: map[string]string{"provisionedFrom": c.url}})
	if err != nil {
		t.Fatal(err)
	}
	otherServer, _ := d.st.CreateAccount(ctx, domain.Account{Kind: "imap", DisplayName: "Andere", Enabled: false, Extra: map[string]string{"provisionedFrom": "https://other.example"}})
	active, _ := d.st.CreateAccount(ctx, domain.Account{Kind: "imap", DisplayName: "Aktiv", Enabled: true, Extra: map[string]string{"provisionedFrom": c.url}})
	plain, _ := d.st.CreateAccount(ctx, domain.Account{Kind: "imap", DisplayName: "Eigen", Enabled: false})
	// No connection: nothing happens to any of them, also after a restart.
	d.restart()
	for _, id := range []int64{stale, otherServer, active, plain} {
		if _, err := d.st.GetAccount(ctx, id); err != nil {
			t.Fatalf("account %d touched without a connection: %v", id, err)
		}
	}
	if d.status().Connected {
		t.Fatal("connected")
	}
	d.connect(c.url, "anna", app.ConnModeThin)
	if _, err := d.st.GetAccount(ctx, stale); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stale copy not removed: %v", err)
	}
	for _, id := range []int64{otherServer, active, plain} {
		if _, err := d.st.GetAccount(ctx, id); err != nil {
			t.Errorf("account %d removed: %v", id, err)
		}
	}
}

func TestConnectionSettingsSync(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	srv := c.srvApp
	// One signature already on the server when connecting.
	sig, err := srv.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>Firma</p>"})
	if err != nil {
		t.Fatal(err)
	}
	d.login(c.url, "anna")
	if err := d.a.ImportProvisioning(ctx, app.ProvisioningImport{ServerURL: c.url, Mode: app.ConnModeLocal, SignatureIDs: []int64{sig.ID}}); err != nil {
		t.Fatal(err)
	}
	d.waitState(app.ConnStateOnline)
	sync := func() app.ConnectionStatus {
		t.Helper()
		s, err := d.a.ServerSyncNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	findSig := func(a *app.App, name string) *domain.Signature {
		list, err := a.ListSignatures(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range list {
			if list[i].Name == name {
				return &list[i]
			}
		}
		return nil
	}
	sync()
	if findSig(d.a, "Firma") == nil {
		t.Fatal("signature not on the device")
	}
	// Here → server.
	mine, err := d.a.SaveSignature(ctx, domain.Signature{Name: "Privat", HTML: "<p>Privat</p>"})
	if err != nil {
		t.Fatal(err)
	}
	sync()
	if findSig(srv, "Privat") == nil {
		t.Fatal("signature not on the server")
	}
	// Server → here.
	onSrv := findSig(srv, "Firma")
	onSrv.HTML = "<p>Firma neu</p>"
	if _, err := srv.SaveSignature(ctx, *onSrv); err != nil {
		t.Fatal(err)
	}
	sync()
	if got := findSig(d.a, "Firma"); got == nil || !strings.Contains(got.HTML, "Firma neu") {
		t.Fatalf("change not pulled: %+v", got)
	}
	// Both changed: the newer one wins, and the log says so.
	time.Sleep(1100 * time.Millisecond) // timestamps have one second
	onSrv = findSig(srv, "Firma")
	onSrv.HTML = "<p>vom Server</p>"
	if _, err := srv.SaveSignature(ctx, *onSrv); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	here := findSig(d.a, "Firma")
	here.HTML = "<p>von hier</p>"
	if _, err := d.a.SaveSignature(ctx, *here); err != nil {
		t.Fatal(err)
	}
	s := sync()
	if got := findSig(srv, "Firma"); !strings.Contains(got.HTML, "von hier") {
		t.Fatalf("newer did not win: %s", got.HTML)
	}
	conflict := false
	for _, e := range s.SettingsLog {
		conflict = conflict || (e.Conflict && e.Kind == "signature" && e.Name == "Firma")
	}
	if !conflict || s.LastSettingsSync == "" {
		t.Fatalf("log: %+v", s)
	}
	// Deleted here: gone on the server, and it stays gone.
	if err := d.a.DeleteSignature(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	sync()
	if findSig(srv, "Privat") != nil {
		t.Fatal("deletion not synchronised")
	}
	sync()
	if findSig(d.a, "Privat") != nil {
		t.Fatal("deleted signature came back")
	}
	// Saved searches and view settings.
	if _, err := srv.SaveSearchProfile(ctx, app.SearchProfile{Name: "Rechnungen", Query: app.SearchQuery{Text: "rechnung", Mode: "simple"}}); err != nil {
		t.Fatal(err)
	}
	prefs, _ := d.a.GetPreferences(ctx)
	prefs.View = json.RawMessage(`{"density":"compact"}`)
	if _, err := d.a.SetPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	sync()
	profiles, _ := d.a.ListSearchProfiles(ctx)
	if len(profiles) != 1 || profiles[0].Name != "Rechnungen" {
		t.Fatalf("profile: %+v", profiles)
	}
	got, _ := srv.GetPreferences(ctx)
	if !strings.Contains(string(got.View), "compact") {
		t.Fatalf("view settings: %s", got.View)
	}
	// Workflows travel switched off, on both sides.
	wf := app.Workflow{Name: "Sortieren", Enabled: true, Trigger: app.WorkflowTrigger{Event: "mail.received"},
		Actions: []app.WorkflowAction{{Type: "archive", Params: map[string]any{}}}}
	saved, err := srv.SaveWorkflow(ctx, wf)
	if err != nil {
		t.Fatal(err)
	}
	sync()
	local, err := d.a.GetWorkflow(ctx, saved.ID)
	if err != nil {
		t.Fatalf("workflow not on the device: %v", err)
	}
	if local.Enabled {
		t.Fatal("workflow arrived switched on")
	}
	onServer, _ := srv.GetWorkflow(ctx, saved.ID)
	if !onServer.Enabled {
		t.Fatal("switching it off here changed the server")
	}
	// Local mode does not use the proxy.
	var ae *app.Error
	if _, err := d.a.ServerRequest(ctx, app.ServerProxyRequest{Method: "GET", Path: "/api/accounts"}); !errors.As(err, &ae) || ae.Code != "not_connected" {
		t.Fatalf("proxy in local mode: %v", err)
	}
}

func TestConnectionLocalImportTakesSealedSecretsAndSwitchesMode(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	vault, err := c.svc.UserSecrets(ctx, c.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	srvAccs, _ := c.srvApp.Store().ListAccounts(ctx)
	acc := srvAccs[0]
	acc.SecretRef = "acct-secret"
	if err := c.srvApp.Store().UpdateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("acct-secret", []byte("mail-passwort-geheim-1234")); err != nil {
		t.Fatal(err)
	}
	d.login(c.url, "anna")
	if err := d.a.ImportProvisioning(ctx, app.ProvisioningImport{ServerURL: c.url, Mode: app.ConnModeLocal, AccountIDs: []int64{acc.ID}}); err != nil {
		t.Fatal(err)
	}
	local, _ := d.st.ListAccounts(ctx)
	if len(local) != 1 || !local[0].Enabled || local[0].SecretRef == "" || local[0].Extra["provisionedFrom"] != c.url {
		t.Fatalf("imported: %+v", local)
	}
	if v, err := d.secrets.Get(local[0].SecretRef); err != nil || string(v) != "mail-passwort-geheim-1234" {
		t.Fatalf("secret: %q %v", v, err)
	}
	// To "over the server" and back after a restart: no double accounts, the
	// client key still opens the sealed secrets.
	if err := d.a.ServerSetMode(ctx, app.ConnModeThin); err != nil {
		t.Fatal(err)
	}
	if !d.a.ThinClientActive() {
		t.Fatal("not thin")
	}
	d.restart()
	if !d.a.ThinClientActive() {
		t.Fatal("mode lost")
	}
	p, err := d.a.ServerProfile(ctx)
	if err != nil || len(p.Accounts) == 0 {
		t.Fatalf("profile: %+v %v", p, err)
	}
	if err := d.a.ImportProvisioning(ctx, app.ProvisioningImport{ServerURL: c.url, Mode: app.ConnModeLocal, AccountIDs: []int64{acc.ID}}); err != nil {
		t.Fatal(err)
	}
	local, _ = d.st.ListAccounts(ctx)
	if len(local) != 1 || !local[0].Enabled {
		t.Fatalf("after switching back: %+v", local)
	}
	d.stop()
	d2 := *d
	d2.start()
	defer d2.stop()
	assertNoSecret(t, &d2, []byte("mail-passwort-geheim-1234"))
	assertNoSecret(t, &d2, d.secrets.m["server:"+c.url+":token"])
}

func TestConnectionProxyBoundaries(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	var ae *app.Error
	if _, err := d.a.ServerRequest(ctx, app.ServerProxyRequest{Method: "GET", Path: "/api/accounts"}); !errors.As(err, &ae) || ae.Code != "not_connected" {
		t.Fatalf("without a connection: %v", err)
	}
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	for _, p := range []string{"/api/provisioning/secrets", "/api/device/login", "/index.html", "//evil.example/api/x", "http://evil.example/api/x", "/api/../device", "/api//x"} {
		if res, err := d.a.ServerRequest(ctx, app.ServerProxyRequest{Method: "GET", Path: p}); err == nil {
			t.Errorf("%s allowed: %+v", p, res)
		}
	}
	// Neither direction lets credentials through.
	got := make(chan http.Header, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Clone():
		default:
		}
		w.Header().Set("Set-Cookie", "evil=1")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	g2 := c.g.next
	c.g.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			h(w, r)
			return
		}
		g2.ServeHTTP(w, r)
	})
	res, err := d.a.ServerRequest(ctx, app.ServerProxyRequest{Method: "GET", Path: "/api/tags", Headers: map[string]string{"Authorization": "Bearer stolen", "Cookie": "a=b"}})
	if err != nil {
		t.Fatal(err)
	}
	seen := <-got
	if seen.Get("Cookie") != "" || seen.Get("Authorization") == "Bearer stolen" {
		t.Fatalf("headers passed on: %v", seen)
	}
	for k := range res.Headers {
		if strings.EqualFold(k, "Set-Cookie") {
			t.Fatal("Set-Cookie reached the interface")
		}
	}
	// A redirect is not followed with the token.
	var hit atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer other.Close()
	c.g.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/persons" {
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
			return
		}
		g2.ServeHTTP(w, r)
	})
	if _, err := d.a.ServerRequest(ctx, app.ServerProxyRequest{Method: "GET", Path: "/api/persons"}); err != nil {
		t.Fatal(err)
	}
	if hit.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestConnectionAssetHandlerForwards(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("local")) })
	h := d.a.RemoteAssetHandler(local)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec.Body.String() != "local" {
		t.Fatal("not connected: must answer locally")
	}
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec.Code != 200 || rec.Body.String() == "local" {
		t.Fatalf("thin: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	if rec.Body.String() != "local" {
		t.Fatal("assets stay local")
	}
}

func TestConnectionLiveEventsThroughCore(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	frames := make(chan string, 16)
	d.a.SetRemoteEventSink(func(f string) {
		select {
		case frames <- f:
		default:
		}
	})
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-frames:
			if !strings.Contains(f, "connection") {
				continue
			}
		case <-deadline:
			t.Fatal("no live event")
		}
		break
	}
	c.srvApp.NotifyConversationUpdated(context.Background(), 4711, "")
	for {
		select {
		case f := <-frames:
			if strings.Contains(f, "4711") {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server event did not arrive")
		}
	}
}

func i64s(n int64) string { return strconv.FormatInt(n, 10) }

func TestConnectionCacheStaysWithinItsLimit(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.stop()
	st, err := store.Open(context.Background(), filepath.Join(d.dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(context.Background(), app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop, RemoteCacheMax: 4 << 10,
		HTTPClient: &http.Client{Transport: testTransport}, ConnTiming: fastTiming(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	d.a, d.st = a, st
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	for range 20 {
		if status, _, _ := d.request("GET", "/api/messages?scope=unified&role=inbox&offset=0&limit=50", nil); status != 200 {
			t.Fatal(status)
		}
		if status, _, _ := d.request("GET", "/api/persons", nil); status != 200 {
			t.Fatal(status)
		}
	}
	if s := d.status(); s.CacheBytes > 64<<10 {
		t.Fatalf("cache %d bytes over its limit", s.CacheBytes)
	}
}

// Rotation against a stub of POST /api/device/rotate (basis/t-sicherheit brings
// the real route): the old token is replaced, 409 ends the sign-in without a
// retry, other failures keep the old token, and two at once never happens.
func TestConnectionTokenRotation(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	ref := "server:" + c.url + ":token"
	tokenNow := func() string { b, _ := d.secrets.Get(ref); return string(b) }
	oldToken := tokenNow()
	var calls, status atomic.Int32
	var seen atomic.Value
	next := c.g.next
	c.g.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/rotate" {
			// The stub server: the new token stands for the old one.
			if r.Header.Get("Authorization") == "Bearer renewed-token-0123456789" {
				r.Header.Set("Authorization", "Bearer "+oldToken)
			}
			next.ServeHTTP(w, r)
			return
		}
		calls.Add(1)
		seen.Store(r.Header.Get("Authorization"))
		switch status.Load() {
		case 200:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"renewed-token-0123456789","token_type":"Bearer"}`))
		default:
			w.WriteHeader(int(status.Load()))
		}
	})
	// Grown old: same data directory, shorter limit.
	restartWith := func(tm *app.ConnTiming) {
		t.Helper()
		d.stop()
		st, err := store.Open(context.Background(), filepath.Join(d.dir, "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := app.New(context.Background(), app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop,
			HTTPClient: &http.Client{Transport: testTransport}, ConnTiming: tm, Logger: slog.New(slog.NewTextHandler(d.logs, nil))})
		if err != nil {
			t.Fatal(err)
		}
		a.Start()
		d.a, d.st = a, st
	}
	due := fastTiming()
	due.RotateAfter, due.RotateRetry = 0, time.Hour

	// Another failure: the old token stays, one attempt until the pause is over.
	status.Store(500)
	restartWith(due)
	d.waitState(app.ConnStateOnline)
	time.Sleep(150 * time.Millisecond) // several checks
	if calls.Load() != 1 || tokenNow() != oldToken {
		t.Fatalf("after 500: %d calls, token changed: %v", calls.Load(), tokenNow() != oldToken)
	}
	// Success: the new token is stored and used; not due again.
	calls.Store(0)
	status.Store(200)
	again := fastTiming()
	again.RotateAfter, again.RotateRetry = 0, 0
	restartWith(again)
	deadline := time.Now().Add(5 * time.Second)
	for tokenNow() == oldToken && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := tokenNow(); got != "renewed-token-0123456789" {
		t.Fatal("token not replaced")
	}
	if seen.Load() != "Bearer "+oldToken {
		t.Fatalf("rotation used %v", seen.Load())
	}
	d.waitState(app.ConnStateOnline)
	// 409: already replaced. The sign-in is over, no second attempt, data stays.
	calls.Store(0)
	status.Store(409)
	deadline = time.Now().Add(5 * time.Second)
	for d.status().State != app.ConnStateExpired && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if d.status().State != app.ConnStateExpired {
		t.Fatalf("state %s after 409", d.status().State)
	}
	n := calls.Load()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != n || n != 1 {
		t.Fatalf("rotation repeated after 409: %d -> %d", n, calls.Load())
	}
	assertNoSecret(t, d, []byte("renewed-token-0123456789"))
}
