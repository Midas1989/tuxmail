// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

const invoiceWF = `id: invoices
name: Invoices
enabled: true
trigger: {event: mail.received, folders: [inbox]}
condition: mail.isInvoice
actions:
  - type: set_var
    name: nr
    value: "R-{{jahr}}"
  - type: archive_pdf
    target: nextcloud
    path: "/Buchhaltung/{{jahr}}/{{monat}}/{{var \"nr\"}}_{{betreff}}.pdf"
  - type: label
    add: [Rechnung]
  - type: move
    folder: Archiv/{{jahr}}
`

func TestEngineRunsWorkflowDurably(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	ids, err := e.HandleEvent(ctx, mailEvent("k1", "rechnung@telekom.de", "Oktober"))
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	// Not matching: condition false, wrong event type.
	noInv := mailEvent("k2", "a@b.de", "Hi")
	noInv.Mail.IsInvoice = false
	if ids, _ := e.HandleEvent(ctx, noInv); len(ids) != 0 {
		t.Fatal("condition false must not queue")
	}
	sent := mailEvent("k3", "a@b.de", "Hi")
	sent.Type = events.MailSent
	if ids, _ := e.HandleEvent(ctx, sent); len(ids) != 0 {
		t.Fatal("other event type must not queue")
	}
	if len(r.Calls()) != 0 {
		t.Fatal("actions must not run before the queue is processed")
	}

	// A new engine on the same store (restart) executes the queued run.
	e2, err := New(Config{Store: st, Deps: fakeDeps(r), Now: func() time.Time { return t0 }, TimeZone: time.UTC, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if n := drain(t, e2); n != 1 {
		t.Fatalf("processed %d", n)
	}
	want := []string{
		"ArchivePDF k1 nextcloud:/Buchhaltung/2026/10/R-2026_Oktober.pdf embed=true",
		"SetLabels k1 [Rechnung] []",
		"Move k1 Archiv/2026",
	}
	if got := r.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	run, err := st.GetWorkflowRun(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunSucceeded || run.EventRef != "k1" || run.FinishedAt.IsZero() {
		t.Fatalf("run = %+v", run)
	}
	var steps []StepResult
	if err := json.Unmarshal([]byte(run.StepsJSON), &steps); err != nil || len(steps) != 4 || steps[0].Output["var.nr"] != "R-2026" {
		t.Fatalf("steps = %s", run.StepsJSON)
	}
	runs, _ := e2.Runs(ctx, "invoices", 10)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
}

func TestEngineRetryResumesAfterFailure(t *testing.T) {
	ctx := context.Background()
	r := &recorder{fail: map[string]error{"SetLabels": errors.New("server down")}}
	e, st, clk := newEngine(t, r)
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S"))
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunQueued || !strings.Contains(run.Error, "server down") {
		t.Fatalf("after transient failure: %+v", run)
	}
	// Not due yet (backoff).
	if ok, _ := e.ProcessOne(ctx); ok {
		t.Fatal("retry must wait for backoff")
	}
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	clk.Advance(time.Minute)
	drain(t, e)
	run, _ = st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunSucceeded || run.Attempts != 2 {
		t.Fatalf("after retry: %+v", run)
	}
	pdf := 0
	for _, c := range r.Calls() {
		if strings.HasPrefix(c, "ArchivePDF") {
			pdf++
		}
	}
	if pdf != 1 {
		t.Fatalf("completed step repeated: %v", r.Calls())
	}
}

func TestEnginePermanentFailureAndManualRetry(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	e.cfg.Deps.PDF = nil // missing backend -> permanent error
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S"))
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunFailed || !strings.Contains(run.Error, "no PDF backend") {
		t.Fatalf("run = %+v", run)
	}
	e.cfg.Deps.PDF = fakePDF{r}
	if err := e.Retry(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	run, _ = st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunSucceeded {
		t.Fatalf("after manual retry: %+v", run)
	}
	if err := e.Retry(ctx, ids[0]); err == nil {
		t.Fatal("retrying a succeeded run must fail")
	}
}

func TestEngineContinueOnErrorAndIf(t *testing.T) {
	ctx := context.Background()
	r := &recorder{fail: map[string]error{"Copy": errors.New("boom")}}
	e, st, _ := newEngine(t, r)
	w := mustParse(t, `id: w
name: W
enabled: true
trigger: {event: mail.received}
actions:
  - {type: copy, folder: X, continue_on_error: true}
  - {type: archive, if: 'mail.subject == "nope"'}
  - {type: delete}
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S"))
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	var steps []StepResult
	_ = json.Unmarshal([]byte(run.StepsJSON), &steps)
	if run.Status != store.RunSucceeded || len(steps) != 3 || steps[0].Status != StepFailed || steps[1].Status != StepSkipped || steps[2].Status != StepDone {
		t.Fatalf("run %+v steps %s", run, run.StepsJSON)
	}
}

func TestLoopProtection(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, _, clk := newEngine(t, r)
	w := mustParse(t, `id: loop
name: Loop
enabled: true
trigger: {event: mail.moved}
actions: [{type: move, folder: A}]
options:
  loop_protection: {max_per_message: 2, window: 1h}
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	ev := mailEvent("k1", "x@y.de", "S")
	ev.Type = events.MailMoved
	queued := 0
	for i := 0; i < 5; i++ {
		ids, _ := e.HandleEvent(ctx, ev)
		queued += len(ids)
	}
	if queued != 2 {
		t.Fatalf("queued %d runs for the same message, want 2", queued)
	}
	// Another message is unaffected.
	other := ev
	other.Ref = "k2"
	if ids, _ := e.HandleEvent(ctx, other); len(ids) != 1 {
		t.Fatal("other message blocked")
	}
	// After the window the message may trigger again.
	clk.Advance(61 * time.Minute)
	if ids, _ := e.HandleEvent(ctx, ev); len(ids) != 1 {
		t.Fatal("window did not slide")
	}
	// Events caused by the workflow itself never trigger it.
	self := ev
	self.Ref, self.Chain = "k3", []string{"loop"}
	if ids, _ := e.HandleEvent(ctx, self); len(ids) != 0 {
		t.Fatal("self-caused event triggered the workflow")
	}
	deep := ev
	deep.Ref, deep.Chain = "k4", []string{"a", "b", "c", "d", "e"}
	if ids, _ := e.HandleEvent(ctx, deep); len(ids) != 0 {
		t.Fatal("too deep chain triggered the workflow")
	}
	if s := e.Stats(); s.SkippedLoop != 5 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestRateLimit(t *testing.T) {
	ctx := context.Background()
	e, _, clk := newEngine(t, &recorder{})
	w := mustParse(t, `id: rl
name: RL
enabled: true
trigger: {event: chat.received}
actions: [{type: notify, text: "{{text}}"}]
options:
  rate_limit: {max: 3, per: 1m}
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := 0; i < 10; i++ {
		ev := events.Event{Type: events.ChatReceived, Ref: "c" + string(rune('a'+i)), Chat: &events.Chat{Channel: "signal", Text: "hi"}}
		ids, _ := e.HandleEvent(ctx, ev)
		n += len(ids)
	}
	if n != 3 || e.Stats().SkippedRate != 7 {
		t.Fatalf("queued %d, stats %+v", n, e.Stats())
	}
	clk.Advance(time.Minute + time.Second)
	if ids, _ := e.HandleEvent(ctx, events.Event{Type: events.ChatReceived, Ref: "z", Chat: &events.Chat{}}); len(ids) != 1 {
		t.Fatal("rate window did not slide")
	}
}

func TestDryRunHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	w := mustParse(t, invoiceWF+`  - type: webhook
    url: https://example.com/hook
  - type: archive
    if: mail.size > mb(100)
`)
	w.Enabled = false // dry runs work on disabled/unsaved workflows
	noInv := mailEvent("k2", "a@b.de", "Hi")
	noInv.Mail.IsInvoice = false
	chat := events.Event{ID: "c", Type: events.ChatReceived, Chat: &events.Chat{}}
	res, err := e.DryRun(ctx, w, []events.Event{mailEvent("k1", "rechnung@telekom.de", "Okt/2026"), noInv, chat})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Calls()) != 0 {
		t.Fatalf("dry run called backends: %v", r.Calls())
	}
	if ops, _ := st.ListOps(ctx, ""); len(ops) != 0 {
		t.Fatal("dry run queued ops")
	}
	if runs, _ := st.ListWorkflowRuns(ctx, "", 0); len(runs) != 0 {
		t.Fatal("dry run wrote run log")
	}
	r0 := res[0]
	if !r0.TriggerMatched || !r0.ConditionMet || len(r0.Planned) != 6 || r0.Error != "" {
		t.Fatalf("result 0 = %+v", r0)
	}
	if p := r0.Planned[1]; p.Type != ActArchivePDF || p.Params["path"] != "/Buchhaltung/2026/10/R-2026_Okt_2026.pdf" {
		t.Fatalf("planned pdf = %+v", p)
	}
	if !r0.Planned[4].ExternalEffect || r0.Planned[1].ExternalEffect || !r0.Planned[5].Skipped {
		t.Fatalf("flags: %+v", r0.Planned)
	}
	if res[1].ConditionMet || len(res[1].Planned) != 0 {
		t.Fatalf("result 1 = %+v", res[1])
	}
	if res[2].TriggerMatched {
		t.Fatalf("result 2 = %+v", res[2])
	}
	// Dry runs do not consume loop-protection budget.
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S")); len(ids) != 1 {
			t.Fatalf("run %d suppressed", i)
		}
	}
}

func TestExternalEffectNeedsConfirmation(t *testing.T) {
	ctx := context.Background()
	e, st, _ := newEngine(t, &recorder{})
	w := mustParse(t, `id: fw
name: Forward
enabled: true
trigger: {event: mail.received}
actions: [{type: forward, to: [deputy@example.com]}]
`)
	if err := e.Save(ctx, w, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("want ErrConfirmationRequired, got %v", err)
	}
	w.Enabled = false
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatalf("disabled save: %v", err)
	}
	if err := e.SetEnabled(ctx, "fw", true, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("enable: %v", err)
	}
	if err := e.SetEnabled(ctx, "fw", true, true); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.GetWorkflow(ctx, "fw")
	if !rec.Enabled || !rec.ExternalConfirmed {
		t.Fatalf("record %+v", rec)
	}
	// Editing with the same external actions keeps the confirmation...
	w.Enabled = true
	w.Name = "Forward 2"
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	// ...adding a new kind of external action needs a new one.
	w.Actions = append(w.Actions, ActionSpec{Type: ActWebhook, Params: map[string]any{"url": "https://x.example/h"}})
	if err := e.Save(ctx, w, false); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("new external action: %v", err)
	}
	if got := w.ExternalEffects(nil); !reflect.DeepEqual(got, []string{"forward", "webhook"}) {
		t.Fatal(got)
	}
}

func TestDisabledAndDeletedWorkflowsAreSkipped(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S"))
	if err := e.SetEnabled(ctx, "invoices", false, false); err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunSkipped || len(r.Calls()) != 0 {
		t.Fatalf("run %+v calls %v", run, r.Calls())
	}
	// Manual runs work on disabled workflows and bypass trigger/condition.
	ev := mailEvent("k9", "x@y.de", "S")
	ev.Mail.IsInvoice = false
	id, err := e.RunManual(ctx, "invoices", ev)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	if run, _ := st.GetWorkflowRun(ctx, id); run.Status != store.RunSucceeded {
		t.Fatalf("manual run %+v", run)
	}
	if err := e.Delete(ctx, "invoices"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get("invoices"); ok {
		t.Fatal("still loaded")
	}
	if runs, _ := st.ListWorkflowRuns(ctx, "invoices", 0); len(runs) != 0 {
		t.Fatal("runs not deleted")
	}
	if _, err := e.RunManual(ctx, "invoices", ev); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRunLocation(t *testing.T) {
	ctx := context.Background()
	e, _, _ := newEngine(t, &recorder{}) // local engine
	w := mustParse(t, invoiceWF)
	w.Options.RunLocation = RunServer
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "S")); len(ids) != 0 {
		t.Fatal("server workflow ran in local engine")
	}
}

func TestBeforeSendWorkflows(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	for _, y := range []string{`id: no-gmail
name: No Gmail
enabled: true
trigger: {event: mail.before_send}
condition: any(mail.recipients, {domain(#) == "gmail.com"})
actions:
  - {type: block, message: "No private addresses ({{empfaenger}})", message_de: "Keine privaten Adressen"}
`, `id: big
name: Big
enabled: true
trigger: {event: mail.before_send}
condition: mail.maxAttachmentSize > mb(10)
actions:
  - {type: upload_attachments, target: nextcloud, dir: "/Anhänge/{{jahr}}", min_size: 10MB, replace_with_link: true}
  - {type: warn, message: "Uploaded: {{var \"links\"}}"}
`} {
		if err := e.Save(ctx, mustParse(t, y), false); err != nil {
			t.Fatal(err)
		}
	}
	draft := events.Event{ID: "d1", Type: events.MailBeforeSend, Ref: "draft-1", Draft: &events.Draft{
		Key: "draft-1", Identity: events.Identity{Email: "me@firma.de"}, Subject: "Angebot",
		To: []domain.Address{{Email: "kunde@gmail.com"}}, Attachments: []events.Attachment{{Name: "a.zip", Size: 20 << 20}},
	}}
	bus := events.New()
	bus.RegisterHook(events.MailBeforeSend, "workflow", e.BeforeSend)
	res := bus.RunHooks(ctx, draft)
	if !res.Vetoed || len(res.Findings) != 2 {
		t.Fatalf("result %+v", res)
	}
	if f := res.Findings[0]; f.Severity != events.SeverityBlock || f.EN != "No private addresses (kunde@gmail.com)" || f.DE != "Keine privaten Adressen" || f.Check != "workflow:no-gmail" {
		t.Fatalf("finding %+v", f)
	}
	if f := res.Findings[1]; f.EN != "Uploaded: https://cloud.example/s/abc" {
		t.Fatalf("finding %+v", f)
	}
	if got := r.Calls(); len(got) != 1 || got[0] != "Upload draft-1 nextcloud:/Anhänge/2026 min=10485760 link=true" {
		t.Fatalf("calls %v", got)
	}
	runs, _ := st.ListWorkflowRuns(ctx, "", 0)
	if len(runs) != 2 || runs[0].OpID != 0 {
		t.Fatalf("runs %+v", runs)
	}
	// HandleEvent ignores hook events.
	if ids, _ := e.HandleEvent(ctx, draft); len(ids) != 0 {
		t.Fatal("before_send queued")
	}
}

func TestWebhookAction(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var got map[string]any
	var hdr http.Header
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		_ = json.Unmarshal(b, &got)
		hdr = r.Header.Clone()
		w.WriteHeader(status)
	}))
	defer srv.Close()

	r := &recorder{}
	e, st, _ := newEngine(t, r)
	e.cfg.Deps.HTTPClient = srv.Client()
	w := mustParse(t, `id: hook
name: Hook
enabled: true
trigger: {event: mail.received}
actions:
  - type: webhook
    url: `+srv.URL+`
    headers: {X-Subject: "{{betreff}}"}
    timeout: 2s
`)
	if err := e.Save(ctx, w, true); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.HandleEvent(ctx, mailEvent("k1", "x@y.de", "Hallo"))
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunSucceeded {
		t.Fatalf("run %+v", run)
	}
	mu.Lock()
	if got["workflow"] != "hook" || got["event"] != "mail.received" || hdr.Get("X-Subject") != "Hallo" || hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("payload %v headers %v", got, hdr)
	}
	if m := got["mail"].(map[string]any); m["subject"] != "Hallo" || m["body"] != nil {
		t.Fatalf("mail part %v (body must not be sent by default)", m)
	}
	status = http.StatusNotFound
	mu.Unlock()
	ids, _ = e.HandleEvent(ctx, mailEvent("k2", "x@y.de", "Hallo"))
	drain(t, e)
	run, _ = st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunFailed || !strings.Contains(run.Error, "HTTP 404") {
		t.Fatalf("4xx must fail permanently: %+v", run)
	}
}

func TestPGPForwardGuard(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	w := mustParse(t, `id: fw
name: FW
enabled: true
trigger: {event: mail.received}
actions: [{type: forward, to: [a@example.com]}]
`)
	if err := e.Save(ctx, w, true); err != nil {
		t.Fatal(err)
	}
	ev := mailEvent("k1", "x@y.de", "S")
	ev.Mail.PGP.Encrypted = true
	ids, _ := e.HandleEvent(ctx, ev)
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunFailed || !strings.Contains(run.Error, "allow_decrypted_pgp") || len(r.Calls()) != 0 {
		t.Fatalf("run %+v calls %v", run, r.Calls())
	}
	w.Options.AllowDecryptedPGP = true
	if err := e.Save(ctx, w, true); err != nil {
		t.Fatal(err)
	}
	ev.Ref = "k2"
	ids, _ = e.HandleEvent(ctx, ev)
	drain(t, e)
	if run, _ := st.GetWorkflowRun(ctx, ids[0]); run.Status != store.RunSucceeded {
		t.Fatalf("run %+v", run)
	}
}

func TestSubWorkflowAndOtherActions(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	e, st, _ := newEngine(t, r)
	for _, y := range []string{`id: child
name: Child
enabled: true
trigger: {event: manual}
condition: vars.who != ""
actions:
  - {type: notify, channel: "signal:me", title: "T", text: "Hallo {{var \"who\"}}"}
  - {type: create_task, title: "Antworten: {{betreff}}", due_in: 2d}
  - {type: create_event, title: "Call {{absender}}", start_in: 1h, duration: 30m}
`, `id: parent
name: Parent
enabled: true
trigger: {event: mail.sent}
actions:
  - {type: set_var, name: who, value: "{{absender}}"}
  - {type: run_workflow, id: child}
  - {type: remind_no_reply, after: 3d}
  - {type: reply_template, body: "Danke, {{absender}}!"}
  - {type: vacation_reply}
`, `id: selfref
name: Self
enabled: true
trigger: {event: manual}
actions: [{type: run_workflow, id: selfref}]
`} {
		w := mustParse(t, y)
		if err := e.Save(ctx, w, true); err != nil && !strings.Contains(err.Error(), "vacation_reply") {
			t.Fatal(err)
		}
	}
	// vacation_reply is not allowed for mail.sent:
	if _, ok := e.Get("parent"); ok {
		t.Fatal("parent with invalid action saved")
	}
	p := mustParse(t, `id: parent
name: Parent
enabled: true
trigger: {event: mail.sent}
actions:
  - {type: set_var, name: who, value: "{{absender}}"}
  - {type: run_workflow, id: child}
  - {type: remind_no_reply, after: 3d}
  - {type: reply_template, body: "Danke, {{absender}}!"}
`)
	if err := e.Save(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	ev := mailEvent("k1", "x@y.de", "Frage")
	ev.Type = events.MailSent
	ids, _ := e.HandleEvent(ctx, ev)
	drain(t, e)
	run, _ := st.GetWorkflowRun(ctx, ids[0])
	if run.Status != store.RunSucceeded {
		t.Fatalf("run %+v", run)
	}
	want := []string{
		"Notify signal:me T|Hallo Telekom Rechnung",
		"CreateTask Antworten: Frage due=2026-11-05T10:30:00Z",
		"CreateEvent Call Telekom Rechnung 30m0s",
		"Followup k1 72h0m0s",
		"ReplyTemplate k1 |Danke, Telekom Rechnung!",
	}
	if got := r.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	// A switched-off rule does not act through another rule.
	offChild := mustParse(t, `id: offchild
name: Off
enabled: false
trigger: {event: manual}
actions: [{type: notify, channel: "signal:me", title: "Off", text: "x"}]
`)
	if err := e.Save(ctx, offChild, true); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(r.Calls())
	callerWf := mustParse(t, `id: caller
name: Caller
enabled: true
trigger: {event: manual}
actions: [{type: run_workflow, id: offchild}]
`)
	if err := e.Save(ctx, callerWf, true); err != nil {
		t.Fatal(err)
	}
	cid, err := e.RunManual(ctx, "caller", events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	if run, _ := st.GetWorkflowRun(ctx, cid); run.Status != store.RunFailed || !strings.Contains(run.Error, "switched off") {
		t.Fatalf("run of a switched-off rule: %+v", run)
	}
	if got := r.Calls(); len(got) != callsBefore {
		t.Fatalf("the switched-off rule acted: %v", got[callsBefore:])
	}
	// Recursion is stopped.
	id, err := e.RunManual(ctx, "selfref", events.Event{})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	if run, _ := st.GetWorkflowRun(ctx, id); run.Status != store.RunFailed || !strings.Contains(run.Error, "loop protection") {
		t.Fatalf("recursive run %+v", run)
	}
}

func TestEngineRunWithBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &recorder{}
	st := openStore(t)
	bus := events.New()
	e, err := New(Config{Store: st, Bus: bus, Deps: fakeDeps(r), PollInterval: 10 * time.Millisecond, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Save(ctx, mustParse(t, invoiceWF), false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	// Wait for the subscription.
	deadline := time.Now().Add(5 * time.Second)
	for bus.Stats().Subscribers == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ev := mailEvent("k1", "x@y.de", "S")
	ev.Time = time.Time{}
	bus.Publish(ev)
	for len(r.Calls()) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(r.Calls()) != 3 {
		t.Fatalf("calls %v", r.Calls())
	}
	res := bus.RunHooks(ctx, events.Event{Type: events.MailBeforeSend, Draft: &events.Draft{}})
	if res.Vetoed {
		t.Fatal("unexpected veto")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLoadReportsInvalidWorkflows(t *testing.T) {
	ctx := context.Background()
	e, st, _ := newEngine(t, &recorder{})
	if err := st.PutWorkflow(ctx, store.WorkflowRecord{ID: "bad", Name: "Bad", YAML: "id: bad\nname: Bad\ntrigger: {event: nope}\nactions: []\n"}); err != nil {
		t.Fatal(err)
	}
	good := mustParse(t, invoiceWF)
	y, _ := good.YAML()
	if err := st.PutWorkflow(ctx, store.WorkflowRecord{ID: "invoices", Name: "Invoices", Enabled: true, YAML: string(y)}); err != nil {
		t.Fatal(err)
	}
	err := e.Load(ctx)
	if err == nil || !strings.Contains(err.Error(), "workflow bad") {
		t.Fatalf("err = %v", err)
	}
	if l := e.List(); len(l) != 1 || l[0].ID != "invoices" || !l[0].Enabled {
		t.Fatalf("list = %+v", l)
	}
}
