// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

type fakeReplyLog struct{ seen map[string]bool }

func (f *fakeReplyLog) ClaimVacationReply(_ context.Context, scope, sender string, _ time.Time, _ time.Duration) (bool, error) {
	k := scope + "|" + sender
	if f.seen[k] {
		return false, nil
	}
	f.seen[k] = true
	return true, nil
}

func (f *fakeReplyLog) ForgetVacationReply(_ context.Context, scope, sender string) error {
	delete(f.seen, scope+"|"+sender)
	return nil
}

// Audit M-10: automatic mail gets the RFC 3834/5436 marking, automated
// originals are not answered or forwarded, senders get one reply per period.
func TestActionsAutomaticMailSafety(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	replies := &fakeReplyLog{seen: map[string]bool{}}
	acts := &Actions{Service: e.svc, Loader: fakeLoader{raw: []byte(origRaw)}, Replies: replies}
	ref := workflow.ItemRef{Kind: "mail", AccountID: e.account.ID, Key: "orig-1@example.net"}

	if err := acts.Forward(ctx, ref, workflow.ForwardRequest{To: []string{"kollege@kanzlei.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := acts.ReplyTemplate(ctx, ref, workflow.ReplyRequest{Body: "x", Send: true}); err != nil {
		t.Fatal(err)
	}
	msgs := e.smtp.messages()
	if len(msgs) != 2 {
		t.Fatalf("messages %d", len(msgs))
	}
	if got := e.parseMsg(msgs[0].data, parse.Options{}).AutoSubmitted; got != "auto-forwarded" {
		t.Errorf("forward Auto-Submitted = %q", got)
	}
	if got := e.parseMsg(msgs[1].data, parse.Options{}).AutoSubmitted; got != "auto-replied" {
		t.Errorf("template reply Auto-Submitted = %q", got)
	}

	// The same sender is answered automatically only once per period.
	if err := acts.ReplyAutomatic(ctx, ref, workflow.ReplyRequest{Body: "x", Send: true}); err != nil {
		t.Fatal(err)
	}
	if err := acts.ReplyAutomatic(ctx, ref, workflow.ReplyRequest{Body: "x", Send: true}); !errors.Is(err, ErrAutoReplyRepeated) {
		t.Errorf("second auto reply: %v", err)
	}

	// Automated originals: list mail and loops.
	listOrig := strings.Replace(origRaw, "Subject: Vertrag", "Subject: Vertrag\r\nList-Id: <l.example.net>", 1)
	acts.Loader = fakeLoader{raw: []byte(listOrig)}
	if err := acts.ReplyTemplate(ctx, ref, workflow.ReplyRequest{Body: "x", Send: true}); !errors.Is(err, ErrAutoReplyBlocked) {
		t.Errorf("reply to list mail: %v", err)
	}
	loopOrig := strings.Replace(origRaw, "Subject: Vertrag", "Subject: Vertrag\r\nAuto-Submitted: auto-forwarded", 1)
	acts.Loader = fakeLoader{raw: []byte(loopOrig)}
	if err := acts.Forward(ctx, ref, workflow.ForwardRequest{To: []string{"kollege@kanzlei.example"}}); !errors.Is(err, ErrAutoForwardBlocked) {
		t.Errorf("forward of auto-forwarded mail: %v", err)
	}
	if n := len(e.smtp.messages()); n != 3 {
		t.Errorf("messages after blocked actions: %d", n)
	}
}
