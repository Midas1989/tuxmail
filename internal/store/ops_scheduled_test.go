// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

func TestEnqueueOpAtAndCancel(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	id, err := s.EnqueueOpAt(ctx, 0, "send.scheduled", `{"x":1}`, now.Add(time.Hour))
	noErr(t, err)
	if _, ok, err := s.ClaimNextOp(ctx, now); err != nil || ok {
		t.Fatalf("future op claimed: ok=%v err=%v", ok, err)
	}
	payload, ok, err := s.CancelPendingOp(ctx, id)
	noErr(t, err)
	if !ok || payload != `{"x":1}` {
		t.Fatalf("cancel = %q %v", payload, ok)
	}
	if _, ok, _ := s.CancelPendingOp(ctx, id); ok {
		t.Fatal("cancelled twice")
	}

	id, err = s.EnqueueOpAt(ctx, 0, "send.scheduled", "p", now.Add(-time.Second))
	noErr(t, err)
	op, ok, err := s.ClaimNextOp(ctx, now)
	noErr(t, err)
	if !ok || op.ID != id {
		t.Fatalf("due op not claimed: %+v %v", op, ok)
	}
	if _, ok, _ := s.CancelPendingOp(ctx, id); ok {
		t.Fatal("running op cancelled")
	}
}
