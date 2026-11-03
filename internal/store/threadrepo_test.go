// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// addMail stores a message and runs it through the engine like the sync
// engine does.
func addMail(t *testing.T, s *Store, e *threading.Engine, acc int64, h domain.MessageHeaderInfo, flags Flags) (int64, threading.Result) {
	t.Helper()
	ctx := context.Background()
	m := Message{AccountID: acc, MessageIDHdr: h.MessageID, Fingerprint: "fp-" + h.MessageID + h.Subject,
		ServerObjectID: h.ServerObjectID, Subject: h.Subject, SentAt: h.Date, Flags: flags}
	if len(h.From) > 0 {
		m.From = h.From[0].Email
	}
	id, _, err := s.UpsertMessage(ctx, m)
	noErr(t, err)
	noErr(t, s.SetMessageAddresses(ctx, id, AddressesFromHeader(h)))
	got, err := s.GetMessage(ctx, id)
	noErr(t, err)
	res, err := e.AddMessage(ctx, threading.MessageInput{Header: h, Key: MessageKey(got),
		Namespace: strconv.FormatInt(acc, 10)})
	noErr(t, err)
	return id, res
}

func convOf(t *testing.T, s *Store, id int64) int64 {
	t.Helper()
	m, err := s.GetMessage(context.Background(), id)
	noErr(t, err)
	return m.ConversationID
}

func TestThreadRepoEngine(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc1, acc2 := newAccount(t, s), newAccount(t, s)
	repo := s.ThreadRepository()
	e := threading.NewEngine(repo, threading.Config{})
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	alice := []domain.Address{{Name: "Alice", Email: "alice@example.org"}}
	bob := []domain.Address{{Email: "bob@example.org"}}

	// A reply arriving before its parent.
	reply, _ := addMail(t, s, e, acc1, domain.MessageHeaderInfo{MessageID: "r1@x", InReplyTo: []string{"p@x"},
		References: []string{"p@x"}, Subject: "Re: Plan", From: bob, To: alice, Date: t0.Add(time.Hour)}, 0)
	parent, _ := addMail(t, s, e, acc1, domain.MessageHeaderInfo{MessageID: "p@x", Subject: "Plan", From: alice,
		To: bob, Date: t0}, FlagSeen)
	c := convOf(t, s, reply)
	if c == 0 || convOf(t, s, parent) != c {
		t.Fatalf("reply and parent not grouped: %d %d", c, convOf(t, s, parent))
	}
	conv := must[Conversation](t)(s.GetConversation(ctx, c))
	if conv.UnreadCount != 1 || !conv.LastActivity.Equal(t0.Add(time.Hour)) || conv.Title == "" {
		t.Errorf("stats/title not maintained: %+v", conv)
	}

	// The same mail in a second account joins the same conversation.
	dup, _ := addMail(t, s, e, acc2, domain.MessageHeaderInfo{MessageID: "p@x", Subject: "Plan", From: alice,
		To: bob, Date: t0}, 0)
	// The engine sees the key as already present; the new row is aligned
	// like internal/sync does.
	if got := must[int64](t)(s.AlignKeyConversation(ctx, "p@x")); got != c {
		t.Fatalf("AlignKeyConversation = %d want %d", got, c)
	}
	if convOf(t, s, dup) != c {
		t.Fatalf("duplicate row not aligned")
	}
	members := must[[]string](t)(repo.Members(ctx, c))
	if !reflect.DeepEqual(members, []string{"p@x", "r1@x"}) {
		t.Errorf("members = %v", members)
	}

	// Relations keep the higher confidence.
	noErr(t, repo.SaveRelations(ctx, []threading.Relation{{SourceKey: "a", TargetKey: "b", Kind: threading.KindForward,
		Origin: threading.OriginInlineForward, Confidence: 0.8}}, t0))
	noErr(t, repo.SaveRelations(ctx, []threading.Relation{{SourceKey: "a", TargetKey: "b", Kind: threading.KindForward,
		Origin: threading.OriginAttachment, Confidence: 1}}, t0))
	noErr(t, repo.SaveRelations(ctx, []threading.Relation{{SourceKey: "a", TargetKey: "b", Kind: threading.KindForward,
		Origin: threading.OriginInlineForward, Confidence: 0.5}}, t0))
	rels := must[[]threading.Relation](t)(repo.RelationsOf(ctx, "b"))
	if len(rels) != 1 || rels[0].Confidence != 1 || rels[0].Origin != threading.OriginAttachment {
		t.Errorf("relations = %+v", rels)
	}

	// Split keeps an override; the split message gets its own conversation.
	res, err := e.Split(ctx, "r1@x")
	noErr(t, err)
	if res.ConversationID == c || convOf(t, s, reply) != res.ConversationID {
		t.Fatalf("split: %+v", res)
	}
	o, ok, err := repo.Override(ctx, "r1@x")
	noErr(t, err)
	if !ok || o.Action != threading.OverrideSplit {
		t.Fatalf("override = %+v %v", o, ok)
	}
	// Removing the split message deletes its conversation but keeps the override.
	res, err = e.RemoveMessage(ctx, "r1@x")
	noErr(t, err)
	if len(res.Deleted) != 1 {
		t.Fatalf("remove: %+v", res)
	}
	if _, ok, _ := repo.Override(ctx, "r1@x"); !ok {
		t.Fatal("override lost with its conversation")
	}
	if _, err := repo.GetConversation(ctx, res.Deleted[0]); !errors.Is(err, threading.ErrNotFound) {
		t.Errorf("deleted conversation: %v", err)
	}
	// Re-adding re-creates a conversation for the override.
	res, err = e.AddMessage(ctx, threading.MessageInput{Key: "r1@x", Header: domain.MessageHeaderInfo{MessageID: "r1@x",
		InReplyTo: []string{"p@x"}, Subject: "Re: Plan"}})
	noErr(t, err)
	if res.ConversationID == 0 || res.ConversationID == c || convOf(t, s, reply) != res.ConversationID {
		t.Errorf("re-add after split: %+v", res)
	}
	_, err = e.ClearOverride(ctx, "r1@x")
	noErr(t, err)
	if convOf(t, s, reply) != c {
		t.Errorf("clear override did not regroup: %d want %d", convOf(t, s, reply), c)
	}

	// Messages without Message-ID use the server object id / fingerprint key.
	obj, _ := addMail(t, s, e, acc1, domain.MessageHeaderInfo{ServerObjectID: "OBJ1", Subject: "x", Date: t0}, 0)
	m := must[Message](t)(s.GetMessage(ctx, obj))
	key := MessageKey(m)
	if key != "~obj:"+strconv.FormatInt(acc1, 10)+":OBJ1" {
		t.Errorf("key = %q", key)
	}
	if got := must[int64](t)(repo.ConversationOf(ctx, key)); got == 0 || got != m.ConversationID {
		t.Errorf("ConversationOf(obj key) = %d", got)
	}
	fp, _ := addMail(t, s, e, acc1, domain.MessageHeaderInfo{Subject: "nofp", Date: t0}, 0)
	fm := must[Message](t)(s.GetMessage(ctx, fp))
	if k := MessageKey(fm); k != "~fp:"+fm.Fingerprint || must[int64](t)(repo.ConversationOf(ctx, k)) == 0 {
		t.Errorf("fingerprint key %q not resolved", k)
	}
	if rows := must[[]Message](t)(s.MessagesByKey(ctx, "p@x")); len(rows) != 2 {
		t.Errorf("MessagesByKey = %d rows", len(rows))
	}
	if ok := must[bool](t)(s.KeyHasRows(ctx, "~thread:1:x")); ok {
		t.Error("virtual key must not match")
	}
}

func TestThreadRepoHeuristicAndForwardCandidates(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	e := threading.NewEngine(s.ThreadRepository(), threading.Config{Heuristic: threading.HeuristicConfig{Enabled: true}})
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ab := []domain.Address{{Email: "a@x.org"}}
	cd := []domain.Address{{Email: "c@x.org"}, {Email: "d@x.org"}}
	first, _ := addMail(t, s, e, acc, domain.MessageHeaderInfo{MessageID: "h1@x", Subject: "Quarterly numbers", From: ab, To: cd, Date: t0}, 0)
	second, _ := addMail(t, s, e, acc, domain.MessageHeaderInfo{MessageID: "h2@x", Subject: "AW: Quarterly numbers", From: cd[:1],
		To: append([]domain.Address{}, ab[0], cd[1]), Date: t0.Add(48 * time.Hour)}, 0)
	if convOf(t, s, first) == 0 || convOf(t, s, first) != convOf(t, s, second) {
		t.Errorf("heuristic did not group: %d %d", convOf(t, s, first), convOf(t, s, second))
	}
	cands := must[[]threading.Candidate](t)(s.ForwardCandidates(ctx, "A@x.org", t0.Add(-time.Hour), t0.Add(time.Hour)))
	if len(cands) != 1 || cands[0].Key != "h1@x" || cands[0].From != "a@x.org" || len(cands[0].Participants) != 3 {
		t.Errorf("candidates = %+v", cands)
	}
}

func TestSyncSupport(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	parent := must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: "Work", Name: "Work", Delimiter: "/"}))
	child := must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: "Work/Sub", Name: "Sub", Delimiter: "/", ParentID: parent}))
	noErr(t, s.UpdateFolderPath(ctx, parent, "Job", "Job", 0))
	if f := must[domain.Folder](t)(s.GetFolder(ctx, child)); f.RemoteID != "Job/Sub" {
		t.Errorf("child remote id = %q", f.RemoteID)
	}
	if f := must[domain.Folder](t)(s.FindFolder(ctx, acc, "job")); f.ID != parent {
		t.Errorf("FindFolder by name = %+v", f)
	}

	id, _, err := s.UpsertMessage(ctx, testMessage(acc, "m@x", "fp"))
	noErr(t, err)
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: child, RemoteRef: "7"}))
	total, unread, err := s.RefreshFolderCounts(ctx, child)
	noErr(t, err)
	if total != 1 || unread != 1 {
		t.Errorf("counts = %d/%d", total, unread)
	}
	tasks := must[[]BodyTask](t)(s.MessagesWithoutBody(ctx, acc, time.Time{}, 0, "~", 10))
	if len(tasks) != 1 || tasks[0].RemoteRef != "7" || tasks[0].FolderRemoteID != "Job/Sub" {
		t.Errorf("body tasks = %+v", tasks)
	}
	noErr(t, s.SetMessageAttachments(ctx, id, []MessageAttachment{{Filename: "a.pdf", MIMEType: "application/pdf", Size: 3}}))
	noErr(t, s.SetMessageContent(ctx, id, "", "snip", true))
	if atts := must[[]AttachmentInfo](t)(s.MessageAttachments(ctx, id)); len(atts) != 1 || atts[0].Filename != "a.pdf" {
		t.Errorf("attachments = %+v", atts)
	}

	// Ops of one account only, release and retry.
	op1 := must[int64](t)(s.EnqueueOp(ctx, acc, "sync.move", "{}"))
	now := time.Now()
	op, ok, err := s.ClaimNextAccountOp(ctx, now, acc+1)
	noErr(t, err)
	if ok {
		t.Fatalf("claimed foreign op %+v", op)
	}
	op, ok, err = s.ClaimNextAccountOp(ctx, now, acc, "sync.move")
	noErr(t, err)
	if !ok || op.ID != op1 || op.Attempts != 1 {
		t.Fatalf("claim = %+v %v", op, ok)
	}
	noErr(t, s.ReleaseOp(ctx, op1, errors.New("offline"), now))
	if o := must[Op](t)(s.GetOp(ctx, op1)); o.State != OpPending || o.Attempts != 0 {
		t.Errorf("released op = %+v", o)
	}
	op, _, _ = s.ClaimNextAccountOp(ctx, now, acc)
	st, err := s.MarkOpRetry(ctx, op.ID, errors.New("boom"), now.Add(time.Second), 1, false)
	noErr(t, err)
	if st != OpFailed {
		t.Errorf("state = %s", st)
	}

	noErr(t, s.MarkOrphaned(ctx, id))
	gone := must[[]Message](t)(s.GCOrphanedMessages(ctx, 0, time.Now().Add(time.Second)))
	if len(gone) != 1 || gone[0].ID != id || gone[0].State != StateDeleted {
		t.Errorf("gc = %+v", gone)
	}
}
