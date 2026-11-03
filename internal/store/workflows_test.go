// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkflowRecords(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	noErr(t, s.PutWorkflow(ctx, WorkflowRecord{ID: "b", Name: "Beta", YAML: "id: b"}))
	noErr(t, s.PutWorkflow(ctx, WorkflowRecord{ID: "a", Name: "Alpha", Enabled: true, YAML: "id: a", ExternalConfirmed: true}))
	first := must[WorkflowRecord](t)(s.GetWorkflow(ctx, "a"))
	noErr(t, s.PutWorkflow(ctx, WorkflowRecord{ID: "a", Name: "Alpha 2", Enabled: true, YAML: "id: a\n"}))
	w := must[WorkflowRecord](t)(s.GetWorkflow(ctx, "a"))
	if w.Name != "Alpha 2" || w.ExternalConfirmed || !w.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("updated %+v", w)
	}
	list := must[[]WorkflowRecord](t)(s.ListWorkflows(ctx))
	if len(list) != 2 || list[0].ID != "a" {
		t.Fatalf("list %+v", list)
	}
	noErr(t, s.SetWorkflowEnabled(ctx, "b", true, true))
	if w := must[WorkflowRecord](t)(s.GetWorkflow(ctx, "b")); !w.Enabled || !w.ExternalConfirmed {
		t.Fatalf("enabled %+v", w)
	}
	if err := s.SetWorkflowEnabled(ctx, "zz", true, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(ctx, WorkflowRecord{}); err == nil {
		t.Fatal("empty id accepted")
	}

	// Runs + ops atomically.
	now := time.Unix(1_800_000_000, 0)
	r, err := s.EnqueueWorkflowRun(ctx, WorkflowRun{WorkflowID: "a", EventType: "mail.received", EventRef: "k1", CreatedAt: now},
		"workflow.run", func(id int64) (string, error) { return `{"run":1}`, nil })
	noErr(t, err)
	if r.ID == 0 || r.OpID == 0 {
		t.Fatalf("run %+v", r)
	}
	_, err = s.EnqueueWorkflowRun(ctx, WorkflowRun{WorkflowID: "a", EventType: "x", CreatedAt: now}, "workflow.run",
		func(int64) (string, error) { return "", errors.New("payload failed") })
	if err == nil {
		t.Fatal("payload error ignored")
	}
	if runs := must[[]WorkflowRun](t)(s.ListWorkflowRuns(ctx, "a", 0)); len(runs) != 1 {
		t.Fatalf("rollback failed: %d runs", len(runs))
	}
	other := must[int64](t)(s.EnqueueOp(ctx, 0, "sync", ""))
	op, ok, err := s.ClaimNextOpOfKind(ctx, now.Add(time.Hour), "workflow.run")
	if err != nil || !ok || op.ID != r.OpID {
		t.Fatalf("claimed %+v %v %v (other op %d)", op, ok, err, other)
	}
	if _, ok, _ := s.ClaimNextOpOfKind(ctx, now.Add(time.Hour), "workflow.run"); ok {
		t.Fatal("claimed twice")
	}

	r.Status, r.StepsJSON, r.Error, r.StartedAt, r.FinishedAt, r.Attempts = RunFailed, `[{"index":0}]`, "boom", now, now.Add(time.Second), 2
	noErr(t, s.UpdateWorkflowRun(ctx, r))
	got := must[WorkflowRun](t)(s.GetWorkflowRun(ctx, r.ID))
	if got.Status != RunFailed || got.Error != "boom" || got.Attempts != 2 || !got.FinishedAt.Equal(now.Add(time.Second)) || got.OpID != r.OpID {
		t.Fatalf("run %+v", got)
	}
	sync := must[int64](t)(s.InsertWorkflowRun(ctx, WorkflowRun{WorkflowID: "b", EventType: "mail.before_send", Status: RunSucceeded, CreatedAt: now}))
	if all := must[[]WorkflowRun](t)(s.ListWorkflowRuns(ctx, "", 0)); len(all) != 2 || all[0].ID != sync {
		t.Fatalf("all runs %+v", all)
	}
	if n := must[int64](t)(s.PurgeWorkflowRuns(ctx, now.Add(time.Hour))); n != 2 {
		t.Fatalf("purged %d", n)
	}
	noErr(t, s.DeleteWorkflow(ctx, "a"))
	if _, err := s.GetWorkflow(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteWorkflow(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestFollowupRecords(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	sent := time.Unix(1_800_000_000, 0).UTC()
	id := must[int64](t)(s.UpsertFollowup(ctx, Followup{AccountID: acc, MessageKey: "m1", Subject: "S",
		Recipients: []string{"a@b.de"}, SentAt: sent, DueAt: sent.Add(time.Hour)}))
	id2 := must[int64](t)(s.UpsertFollowup(ctx, Followup{AccountID: acc, MessageKey: "m1", SentAt: sent, DueAt: sent.Add(2 * time.Hour)}))
	if id != id2 {
		t.Fatal("upsert created a second pending row")
	}
	f := must[Followup](t)(s.GetFollowup(ctx, id))
	if f.State != FollowupPending || !f.DueAt.Equal(sent.Add(2*time.Hour)) || f.Recipients == nil {
		t.Fatalf("followup %+v", f)
	}
	next, ok, err := s.NextFollowupDue(ctx)
	if err != nil || !ok || !next.Equal(f.DueAt) {
		t.Fatal(next, ok, err)
	}
	if due := must[[]Followup](t)(s.DueFollowups(ctx, sent.Add(time.Hour), 0)); len(due) != 0 {
		t.Fatal("due too early")
	}
	if due := must[[]Followup](t)(s.DueFollowups(ctx, sent.Add(3*time.Hour), 0)); len(due) != 1 {
		t.Fatal("not due")
	}
	noErr(t, s.MarkFollowupFired(ctx, id, sent.Add(3*time.Hour)))
	if err := s.MarkFollowupFired(ctx, id, sent); !errors.Is(err, ErrNotFound) {
		t.Fatal("fired twice")
	}
	if _, ok, _ := s.NextFollowupDue(ctx); ok {
		t.Fatal("no pending expected")
	}
	// A new reminder for the same message is allowed after the old one closed.
	id3 := must[int64](t)(s.UpsertFollowup(ctx, Followup{MessageKey: "m1", SentAt: sent, DueAt: sent}))
	if id3 == id {
		t.Fatal("reused closed row")
	}
	if n := must[int64](t)(s.CancelFollowups(ctx, "m1", "r1", sent)); n != 1 {
		t.Fatal(n)
	}
	if err := s.CancelFollowup(ctx, id3, sent); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if l := must[[]Followup](t)(s.ListFollowups(ctx, FollowupCancelled)); len(l) != 1 || l[0].ReplyKey != "r1" {
		t.Fatalf("cancelled %+v", l)
	}
}

func TestVacationRecords(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.GetVacationSettings(ctx, "default"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	noErr(t, s.PutVacationSettings(ctx, "default", `{"enabled":true}`))
	noErr(t, s.PutVacationSettings(ctx, "default", `{"enabled":false}`))
	if js := must[string](t)(s.GetVacationSettings(ctx, "default")); js != `{"enabled":false}` {
		t.Fatal(js)
	}
	now := time.Unix(1_800_000_000, 0)
	win := 4 * 24 * time.Hour
	claim := func(at time.Time, sender string) bool {
		return must[bool](t)(s.ClaimVacationReply(ctx, "account:1", sender, at, win))
	}
	if !claim(now, "A@b.de") || claim(now.Add(time.Hour), "a@b.de") || !claim(now, "c@d.de") {
		t.Fatal("claims within window")
	}
	if !claim(now.Add(win), "a@b.de") {
		t.Fatal("claim after window")
	}
	noErr(t, s.ForgetVacationReply(ctx, "account:1", "a@b.de"))
	if !claim(now.Add(win+time.Hour), "a@b.de") {
		t.Fatal("forget")
	}
	noErr(t, s.ResetVacationReplies(ctx, ""))
	if !claim(now.Add(win+2*time.Hour), "a@b.de") {
		t.Fatal("reset")
	}
}
