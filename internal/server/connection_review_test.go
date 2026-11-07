// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Findings of the independent review, each as a test.

func connectLocal(t *testing.T, c *connEnv, d *desktop, in app.ProvisioningImport) {
	t.Helper()
	d.login(c.url, "anna")
	in.ServerURL, in.Mode = c.url, app.ConnModeLocal
	if err := d.a.ImportProvisioning(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	d.waitState(app.ConnStateOnline)
}

func syncNow(t *testing.T, d *desktop) app.ConnectionStatus {
	t.Helper()
	s, err := d.a.ServerSyncNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func archiveRule(name string, enabled bool) app.Workflow {
	return app.Workflow{Name: name, Enabled: enabled, Trigger: app.WorkflowTrigger{Event: "mail.received"},
		Actions: []app.WorkflowAction{{Type: "archive", Params: map[string]any{}}}}
}

// 1: a rule changed on the server is off here until released, with the change shown.
func TestReviewChangedServerRuleWaitsForRelease(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	connectLocal(t, c, d, app.ProvisioningImport{})
	saved, err := c.srvApp.SaveWorkflow(ctx, archiveRule("Sortieren", false))
	if err != nil {
		t.Fatal(err)
	}
	s := syncNow(t, d)
	if len(s.Attention.PendingWorkflows) != 1 || !s.Attention.PendingWorkflows[0].IsNew {
		t.Fatalf("new rule: %+v", s.Attention)
	}
	if err := d.a.ServerWorkflowApprove(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.a.GetWorkflow(ctx, saved.ID); !got.Enabled {
		t.Fatal("not released")
	}
	if s := syncNow(t, d); len(s.Attention.PendingWorkflows) != 0 {
		t.Fatalf("still waiting: %+v", s.Attention)
	}
	// The server changes it: delete permanently.
	time.Sleep(1100 * time.Millisecond)
	w, _ := c.srvApp.GetWorkflow(ctx, saved.ID)
	w.Actions = []app.WorkflowAction{{Type: "delete", Params: map[string]any{"permanent": true}}}
	if _, err := c.srvApp.SaveWorkflow(ctx, w); err != nil {
		t.Fatal(err)
	}
	s = syncNow(t, d)
	got, err := d.a.GetWorkflow(ctx, saved.ID)
	if err != nil || got.Enabled {
		t.Fatalf("changed rule stays on: %+v %v", got, err)
	}
	if len(s.Attention.PendingWorkflows) != 1 {
		t.Fatalf("not waiting: %+v", s.Attention)
	}
	p := s.Attention.PendingWorkflows[0]
	if p.IsNew || !p.WasEnabled || p.Before[0] != "archive" || p.After[0] != "delete" {
		t.Fatalf("change not shown: %+v", p)
	}
	// Keeping it off stops the asking, the rule stays off.
	if err := d.a.ServerWorkflowDismiss(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if s := syncNow(t, d); len(s.Attention.PendingWorkflows) != 0 {
		t.Fatalf("after dismiss: %+v", s.Attention)
	}
	if got, _ := d.a.GetWorkflow(ctx, saved.ID); got.Enabled {
		t.Fatal("on after dismiss")
	}
}

// 1: on the first sync a local rule is never overwritten.
func TestReviewFirstSyncConflictKeepsLocalRule(t *testing.T) {
	for _, keepLocal := range []bool{true, false} {
		t.Run(fmt.Sprint("keepLocal=", keepLocal), func(t *testing.T) {
			c := newConnEnv(t)
			d := newDesktop(t)
			ctx := context.Background()
			own, err := d.a.SaveWorkflow(ctx, archiveRule("Sortieren", true))
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond) // the server's copy is newer
			if _, err := c.srvApp.SaveWorkflow(ctx, app.Workflow{ID: own.ID, Name: "Sortieren", Trigger: app.WorkflowTrigger{Event: "mail.received"},
				Actions: []app.WorkflowAction{{Type: "delete", Params: map[string]any{"permanent": true}}}}); err != nil {
				t.Fatal(err)
			}
			connectLocal(t, c, d, app.ProvisioningImport{})
			s := syncNow(t, d)
			got, _ := d.a.GetWorkflow(ctx, own.ID)
			if !got.Enabled || got.Actions[0].Type != "archive" {
				t.Fatalf("local rule overwritten: %+v", got)
			}
			if len(s.Attention.Conflicts) != 1 || s.Attention.Conflicts[0].ServerAfter[0] != "delete" {
				t.Fatalf("no conflict shown: %+v", s.Attention)
			}
			if err := d.a.ServerWorkflowResolve(ctx, own.ID, keepLocal); err != nil {
				t.Fatal(err)
			}
			got, _ = d.a.GetWorkflow(ctx, own.ID)
			srv, _ := c.srvApp.GetWorkflow(ctx, own.ID)
			if keepLocal {
				if got.Actions[0].Type != "archive" || srv.Actions[0].Type != "archive" {
					t.Fatalf("keep mine: here %+v server %+v", got.Actions, srv.Actions)
				}
			} else if got.Enabled || got.Actions[0].Type != "delete" {
				t.Fatalf("take server: %+v", got)
			}
			if s := syncNow(t, d); len(s.Attention.Conflicts) != 0 {
				t.Fatalf("conflict stays: %+v", s.Attention)
			}
		})
	}
}

// 2: no client follows a redirect with the token or the password.
func TestReviewNoRedirectWithCredentials(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	var hits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer evil.Close()
	next := c.g.handler()
	redirected := func(paths ...string) {
		c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range paths {
				if r.URL.Path == p {
					http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
					return
				}
			}
			next.ServeHTTP(w, r)
		}))
	}
	ctx := context.Background()
	redirected("/api/device/login")
	if _, err := d.a.ServerLogin(ctx, c.url, app.ServerLoginInput{Username: "anna", Password: pw}); err == nil {
		t.Error("login through a redirect succeeded")
	}
	c.g.setHandler(next)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	redirected("/api/provisioning/profile", "/api/tags")
	if _, err := d.a.ServerProfile(ctx); err == nil {
		t.Error("profile through a redirect succeeded")
	}
	d.request("GET", "/api/tags", nil)
	if n := hits.Load(); n != 0 {
		t.Fatalf("redirect followed %d times", n)
	}
}

// 2: https only, http only for this machine.
func TestReviewOnlyHTTPS(t *testing.T) {
	d := newDesktop(t)
	ctx := context.Background()
	for _, u := range []string{"http://mail.example.de", "http://192.168.1.5:8080", "ftp://x", "http://127.0.0.1.evil.example", "https://user:pw@mail.example.de"} {
		if _, err := d.a.ServerLogin(ctx, u, app.ServerLoginInput{Username: "a", Password: "b"}); err == nil || app.AsError(err).Code == "network" {
			t.Errorf("%s accepted: %v", u, err)
		}
	}
	for _, u := range []string{"http://localhost:1", "http://127.0.0.1:1", "http://[::1]:1"} {
		_, err := d.a.ServerLogin(ctx, u, app.ServerLoginInput{Username: "a", Password: "b"})
		if err == nil || app.AsError(err).Code != "network" {
			t.Errorf("%s refused: %v", u, err)
		}
	}
}

type ambiguousSender struct {
	mu    sync.Mutex
	sent  int
	err   error
	block bool
	seen  [][]string
}

func (s *ambiguousSender) Send(_ context.Context, r app.SendRequest) (app.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r.Message.ConfirmChecks)
	if s.block && len(r.Message.ConfirmChecks) == 0 {
		return app.SendResult{Blocked: true, Findings: []app.SendFinding{{Check: "forgotten_attachment", Severity: "warn", Message: "Attachment forgotten?"}}}, nil
	}
	s.sent++
	if s.err != nil {
		return app.SendResult{}, s.err // the relay took it, then the answer was lost
	}
	return app.SendResult{MessageID: 1}, nil
}

func (s *ambiguousSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

func envWithSender(t *testing.T, sender app.Sender) *connEnv {
	t.Helper()
	e := newEnv(t, Options{AppOptions: func(_ int64, o *app.Options) { o.Sync = nil; o.Send = nil; o.Sender = sender }})
	u := e.user("anna", auth.RoleUser)
	a, err := e.srv.App(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Seed(context.Background(), a.Store(), a.Blobs(), time.Now()); err != nil {
		t.Fatal(err)
	}
	g := &gate{next: e.srv.Handler()}
	ts := httptest.NewServer(g)
	t.Cleanup(ts.Close)
	return &connEnv{env: e, g: g, url: ts.URL, user: u, srvApp: a, sender: &countingSender{}}
}

func offlineSend(t *testing.T, c *connEnv, d *desktop, subject string) {
	t.Helper()
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	mail := c.mail(t, subject)
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)
	if st, raw, _ := d.request("POST", "/api/send", mail); st != 200 {
		t.Fatalf("%d %s", st, raw)
	}
	c.g.down.Store(false)
}

func waitOutboxState(t *testing.T, d *desktop, state string) app.OutboxEntry {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if o := d.status().Outbox; len(o) == 1 && o[0].State == state {
			return o[0]
		}
	}
	t.Fatalf("outbox: %+v", d.status().Outbox)
	return app.OutboxEntry{}
}

// 3: the relay took the mail, then the server failed: never automatically again.
func TestReviewAmbiguousFailureIsNotRepeated(t *testing.T) {
	s := &ambiguousSender{err: errors.New("smtp: read tcp: connection reset after DATA")}
	c := envWithSender(t, s)
	d := newDesktop(t)
	offlineSend(t, c, d, "Rechnung")
	e := waitOutboxState(t, d, store.OutboxInDoubt)
	time.Sleep(300 * time.Millisecond)
	if n := s.count(); n != 1 {
		t.Fatalf("delivered %d times", n)
	}
	// The user decides: with a new key, once more.
	s.mu.Lock()
	s.err = nil
	s.mu.Unlock()
	if err := d.a.ServerOutboxRetry(context.Background(), e.ID); err != nil {
		t.Fatal(err)
	}
	d.waitOutboxEmpty()
	if n := s.count(); n != 2 {
		t.Fatalf("after the user's decision: %d", n)
	}
}

// 3: the server keeps the doubt, also for a client repeating the same key.
func TestReviewServerKeepsDoubt(t *testing.T) {
	s := &ambiguousSender{err: errors.New("smtp: connection reset after DATA")}
	c := envWithSender(t, s)
	hc := &http.Client{Transport: testTransport}
	js, _ := json.Marshal(map[string]string{"username": "anna", "password": pw, "device_name": "T"})
	lr, err := hc.Post(c.url+"/api/device/login", "application/json", bytes.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(lr.Body).Decode(&tok)
	lr.Body.Close()
	body, _ := json.Marshal(c.mail(t, "A"))
	send := func() (int, string) {
		req, _ := http.NewRequest("POST", c.url+"/api/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Idempotency-Key", "key-doubt-001")
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(raw)
	}
	if st, _ := send(); st < 500 {
		t.Fatalf("first: %d", st)
	}
	for range 3 {
		if st, raw := send(); st != 409 || !strings.Contains(raw, "idempotency_in_doubt") {
			t.Fatalf("repeat: %d %s", st, raw)
		}
	}
	if n := s.count(); n != 1 {
		t.Fatalf("delivered %d times", n)
	}
}

// 4: a stalling body ends in time and shows as offline.
func TestReviewStalledBodyGoesOffline(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	next := c.g.handler()
	release := make(chan struct{})
	defer close(release)
	var stall atomic.Bool
	stall.Store(true)
	c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stall.Load() && r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.Write([]byte("["))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		next.ServeHTTP(w, r)
	}))
	start := time.Now()
	st, raw, h := d.request("GET", "/api/tags", nil) // ReadTimeout 2 s in the tests
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	if st != 200 || h["X-Tuxmail-Offline"] != "1" {
		t.Fatalf("no offline answer: %d %s %v", st, raw, h)
	}
	stall.Store(false)
}

// 4: trickling bytes cannot hold an exchange for ever either.
func TestReviewTricklingBodyHasATotalLimit(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.stop()
	st, err := store.Open(context.Background(), filepath.Join(d.dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	tm := fastTiming()
	tm.ReadTimeout, tm.TotalTimeout = 500*time.Millisecond, time.Second
	a, err := app.New(context.Background(), app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop, HTTPClient: &http.Client{Transport: testTransport},
		ConnTiming: tm, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	d.a, d.st = a, st
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	next := c.g.handler()
	c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			next.ServeHTTP(w, r)
			return
		}
		for r.Context().Err() == nil {
			w.Write([]byte(" "))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	start := time.Now()
	d.request("GET", "/api/tags", nil)
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("took %v", took)
	}
}

// 5: nonsense answers delete nothing.
func TestReviewNonsenseAnswersDeleteNothing(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"empty": func(w http.ResponseWriter) { w.WriteHeader(200) },
		"html": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>Anmelden</html>"))
		},
		"emptyJSON": func(w http.ResponseWriter) { w.Header().Set("Content-Type", "application/json"); w.Write([]byte("{}")) },
		"null": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("null"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newConnEnv(t)
			d := newDesktop(t)
			ctx := context.Background()
			sig, _ := c.srvApp.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>x</p>"})
			wf, err := c.srvApp.SaveWorkflow(ctx, archiveRule("Sortieren", false))
			if err != nil {
				t.Fatal(err)
			}
			connectLocal(t, c, d, app.ProvisioningImport{SignatureIDs: []int64{sig.ID}})
			syncNow(t, d)
			next := c.g.handler()
			c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && (r.URL.Path == "/api/provisioning/profile" || r.URL.Path == "/api/workflows" || r.URL.Path == "/api/search/profiles" || r.URL.Path == "/api/settings-sync/tombstones") {
					answer(w)
					return
				}
				next.ServeHTTP(w, r)
			}))
			syncNow(t, d)
			if list, _ := d.a.ListSignatures(ctx); len(list) != 1 {
				t.Fatalf("signature deleted: %+v", list)
			}
			if _, err := d.a.GetWorkflow(ctx, wf.ID); err != nil {
				t.Fatalf("rule deleted: %v", err)
			}
		})
	}
}

// 5: gone on the server without a record of deleting it: it comes back, nothing is lost.
func TestReviewMissingWithoutTombstoneComesBack(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	sig, _ := c.srvApp.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>x</p>"})
	connectLocal(t, c, d, app.ProvisioningImport{SignatureIDs: []int64{sig.ID}})
	syncNow(t, d)
	// e.g. the server's data was restored from an older backup
	if err := c.srvApp.Store().DeleteSignature(ctx, sig.ID); err != nil {
		t.Fatal(err)
	}
	syncNow(t, d)
	if list, _ := d.a.ListSignatures(ctx); len(list) != 1 {
		t.Fatalf("deleted here: %+v", list)
	}
	back := false
	list, _ := c.srvApp.ListSignatures(ctx)
	for _, x := range list {
		back = back || x.Name == "Firma"
	}
	if !back {
		t.Fatalf("not back on the server: %+v", list)
	}
}

// 5: a deletion on purpose arrives; many at once only after asking.
func TestReviewDeletionsNeedTombstoneAndMassDeleteAsks(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint("approve=", approve), func(t *testing.T) {
			c := newConnEnv(t)
			d := newDesktop(t)
			ctx := context.Background()
			seeded, _ := c.srvApp.ListSignatures(ctx) // of the demo data, not selected for this device
			var ids []int64
			for _, n := range []string{"A", "B", "C", "D"} {
				s, _ := c.srvApp.SaveSignature(ctx, domain.Signature{Name: n, HTML: "<p>x</p>"})
				ids = append(ids, s.ID)
			}
			connectLocal(t, c, d, app.ProvisioningImport{SignatureIDs: ids})
			syncNow(t, d)
			// One on purpose: carried out without asking.
			if err := c.srvApp.DeleteSignature(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
			syncNow(t, d)
			if list, _ := d.a.ListSignatures(ctx); len(list) != 3 {
				t.Fatalf("single deletion: %d", len(list))
			}
			// Three at once: nothing until the user answers.
			for _, id := range ids[1:] {
				if err := c.srvApp.DeleteSignature(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			s := syncNow(t, d)
			if list, _ := d.a.ListSignatures(ctx); len(list) != 3 {
				t.Fatalf("mass deletion carried out: %d", len(list))
			}
			if len(s.Attention.Deletes) != 1 || len(s.Attention.Deletes[0].Names) != 3 {
				t.Fatalf("not asked: %+v", s.Attention)
			}
			if err := d.a.ServerSettingsDeletes(ctx, "signature", approve); err != nil {
				t.Fatal(err)
			}
			here, _ := d.a.ListSignatures(ctx)
			there, _ := c.srvApp.ListSignatures(ctx)
			if approve && (len(here) != 0 || len(there) != len(seeded)) {
				t.Fatalf("approved: here %d there %d", len(here), len(there))
			}
			if !approve && (len(here) != 3 || len(there) != len(seeded)+3) {
				t.Fatalf("refused: here %d there %d", len(here), len(there))
			}
			if s := syncNow(t, d); len(s.Attention.Deletes) != 0 {
				t.Fatalf("still asking: %+v", s.Attention)
			}
		})
	}
}

// 6: attachment kept offline, sent after the connection came back.
func TestReviewOfflineUploadSentOnline(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	m := c.mail(t, "MitAnhang")
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)
	res, err := d.a.ServerRequest(context.Background(), app.ServerProxyRequest{Method: "POST", Path: "/api/uploads", Headers: map[string]string{"X-Filename": "a.txt", "Content-Type": "text/plain"},
		Body: base64.StdEncoding.EncodeToString([]byte("hello"))})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.Body)
	var up struct {
		UploadToken string `json:"uploadToken"`
	}
	json.Unmarshal(raw, &up)
	c.g.down.Store(false)
	d.waitState(app.ConnStateOnline)
	m["attachments"] = []map[string]any{{"uploadToken": up.UploadToken, "filename": "a.txt", "mimeType": "text/plain"}}
	if st, out, _ := d.request("POST", "/api/send", m); st != 200 {
		t.Fatalf("send: %d %s", st, out)
	}
	d.waitOutboxEmpty()
	if n := c.sender.count("MitAnhang"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

// 7: stopped by the send checks: the reason shows, the mail stays, and both ways out work.
func TestReviewBlockedMailComesBack(t *testing.T) {
	s := &ambiguousSender{block: true}
	c := envWithSender(t, s)
	d := newDesktop(t)
	offlineSend(t, c, d, "Anlage siehe Anhang")
	e := waitOutboxState(t, d, store.OutboxBlocked)
	if len(e.Findings) != 1 || e.Findings[0].Check != "forgotten_attachment" {
		t.Fatalf("no reason: %+v", e)
	}
	ctx := context.Background()
	m, err := d.a.ServerOutboxMessage(ctx, e.ID)
	if err != nil || m.Subject != "Anlage siehe Anhang" || len(m.To) != 1 {
		t.Fatalf("as a draft: %+v %v", m, err)
	}
	d.restart()
	if o := d.status().Outbox; len(o) != 1 || o[0].State != store.OutboxBlocked {
		t.Fatalf("lost: %+v", o)
	}
	// "Send anyway" confirms exactly these checks.
	if err := d.a.ServerOutboxRetry(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	d.waitOutboxEmpty()
	if n := s.count(); n != 1 {
		t.Fatalf("sent %d", n)
	}
}

// 8: a failed push is tried again.
func TestReviewFailedPushIsRepeated(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	sig, _ := c.srvApp.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>alt</p>"})
	connectLocal(t, c, d, app.ProvisioningImport{SignatureIDs: []int64{sig.ID}})
	syncNow(t, d)
	list, _ := d.a.ListSignatures(ctx)
	here := list[0]
	here.HTML = "<p>neu von hier</p>"
	if _, err := d.a.SaveSignature(ctx, here); err != nil {
		t.Fatal(err)
	}
	next := c.g.handler()
	var fail atomic.Bool
	fail.Store(true)
	c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/signatures/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"internal"}`))
			return
		}
		next.ServeHTTP(w, r)
	}))
	syncNow(t, d)
	fail.Store(false)
	syncNow(t, d)
	srv, _ := c.srvApp.ListSignatures(ctx)
	if !strings.Contains(srv[0].HTML, "neu von hier") {
		t.Fatalf("change lost: %q", srv[0].HTML)
	}
}

// 9: deleted also means gone from the disk.
func TestReviewDeletedCacheLeavesNoBytes(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	marker := "PVMARKER-vertraulich-4711-Geheimtext"
	if _, err := c.srvApp.SaveSignature(context.Background(), domain.Signature{Name: "M", HTML: "<p>" + marker + "</p>"}); err != nil {
		t.Fatal(err)
	}
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	if st, raw, _ := d.request("GET", "/api/signatures", nil); st != 200 || !strings.Contains(string(raw), marker) {
		t.Fatal(st)
	}
	if _, err := d.a.ServerDisconnect(context.Background(), app.DisconnectInput{DeleteCache: true, DiscardOutbox: true}); err != nil {
		t.Fatal(err)
	}
	scan := func(when string) {
		t.Helper()
		filepath.Walk(d.dir, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte(marker)) {
				t.Errorf("%s: %s still holds the text", when, filepath.Base(p))
			}
			return nil
		})
	}
	scan("running")
	d.stop()
	scan("closed")
	d.start()
}

// 10: the limit also holds over many short sessions.
func TestReviewCacheLimitAcrossShortSessions(t *testing.T) {
	c := newConnEnv(t)
	big := strings.Repeat("Lorem ipsum ", 7000)
	if _, err := c.srvApp.SaveSignature(context.Background(), domain.Signature{Name: "B", HTML: "<p>" + big + "</p>"}); err != nil {
		t.Fatal(err)
	}
	d := newDesktop(t)
	d.stop()
	const limit = 1 << 20
	open := func() {
		st, err := store.Open(context.Background(), filepath.Join(d.dir, "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := app.New(context.Background(), app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop, RemoteCacheMax: limit,
			HTTPClient: &http.Client{Transport: testTransport}, ConnTiming: fastTiming(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		a.Start()
		d.a, d.st = a, st
	}
	open()
	d.connect(c.url, "anna", app.ConnModeThin)
	for i := range 25 {
		d.waitState(app.ConnStateOnline)
		if st, _, _ := d.request("GET", fmt.Sprintf("/api/signatures?s=%d", i), nil); st != 200 {
			t.Fatal(st)
		}
		d.stop()
		open()
	}
	d.waitState(app.ConnStateOnline)
	if s := d.status(); s.CacheBytes > limit+limit/8+128<<10 {
		t.Fatalf("cache %d bytes, limit %d", s.CacheBytes, limit)
	}
}

// 11: a signature from before the migration does not lose against every copy.
func TestReviewOldSignatureDoesNotLose(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	ctx := context.Background()
	mine, _ := d.a.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>meine lokale Signatur</p>"})
	if _, err := d.st.DB().ExecContext(ctx, `UPDATE signatures SET updated_at = 0 WHERE id = ?`, mine.ID); err != nil {
		t.Fatal(err)
	}
	sig, _ := c.srvApp.SaveSignature(ctx, domain.Signature{Name: "Firma", HTML: "<p>Serverfassung</p>"})
	connectLocal(t, c, d, app.ProvisioningImport{SignatureIDs: []int64{sig.ID}})
	syncNow(t, d)
	loc, _ := d.a.ListSignatures(ctx)
	if len(loc) != 1 || !strings.Contains(loc[0].HTML, "meine lokale") {
		t.Fatalf("local: %+v", loc)
	}
}

// Rotation: the answer gets lost. The old token stays; the next attempt gets
// 409, and that ends the sign-in, once, without touching local data.
func TestReviewRotationAnswerLost(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	if st, _, _ := d.request("GET", "/api/accounts", nil); st != 200 {
		t.Fatal(st)
	}
	ref := "server:" + c.url + ":token"
	tokenNow := func() string { b, _ := d.secrets.Get(ref); return string(b) }
	old := tokenNow()
	var calls atomic.Int32
	next := c.g.handler()
	c.g.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/rotate" {
			next.ServeHTTP(w, r)
			return
		}
		if calls.Add(1) == 1 {
			panic(http.ErrAbortHandler) // rotated at the server, the answer is lost
		}
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"token_superseded"}`))
	}))
	d.stop()
	st, err := store.Open(context.Background(), filepath.Join(d.dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	tm := fastTiming()
	tm.RotateAfter, tm.RotateRetry = 0, 100*time.Millisecond
	a, err := app.New(context.Background(), app.Options{Store: st, DataDir: d.dir, Secrets: d.secrets, Mode: app.ModeDesktop, HTTPClient: &http.Client{Transport: testTransport},
		ConnTiming: tm, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	a.Start()
	d.a, d.st = a, st
	d.waitState(app.ConnStateExpired)
	if tokenNow() != old {
		t.Fatal("token changed without an answer")
	}
	n := calls.Load()
	if n != 2 {
		t.Fatalf("%d attempts", n)
	}
	time.Sleep(300 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("repeated after 409")
	}
	if st, _, h := d.request("GET", "/api/accounts", nil); st != 200 || h["X-Tuxmail-Offline"] != "1" {
		t.Fatalf("local data gone: %d", st)
	}
}
