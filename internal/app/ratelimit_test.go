// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newTokenBucket(6, now)
	for i := 0; i < 6; i++ {
		if ok, _ := b.take(now); !ok {
			t.Fatalf("take %d refused", i)
		}
	}
	ok, wait := b.take(now)
	if ok || wait <= 0 {
		t.Fatalf("7th take: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := b.take(now.Add(11 * time.Second)); !ok {
		t.Fatal("token not refilled")
	}
}

// Audit M-8: account tests and discovery are limited per user.
func TestLoginProbeRateLimited(t *testing.T) {
	e := newEditEnv(t)
	ctx := context.Background()
	var got error
	for i := 0; i < loginProbePerMinute+3 && got == nil; i++ {
		_, got = e.a.DiscoverAccount(ctx, "nobody@invalid.invalid", "")
		if i < loginProbePerMinute && errCode(got) == CodeRateLimited {
			t.Fatalf("refused too early (%d)", i)
		}
		if i < loginProbePerMinute {
			got = nil
		}
	}
	var ae *Error
	if !errors.As(got, &ae) || ae.Code != CodeRateLimited || ae.Status != 429 || ae.RetryAfter <= 0 {
		t.Fatalf("want rate_limited with Retry-After, got %v", got)
	}
	if _, err := e.a.TestAccountConnection(ctx, e.acc, ConnectionTestInput{}); errCode(err) != CodeRateLimited {
		t.Fatalf("account test not limited: %v", err)
	}
}

// Audit M-7: manual sync requests of an account are limited.
func TestSyncNowRateLimited(t *testing.T) {
	e := newEditEnv(t)
	ctx := context.Background()
	n := 0
	for i := 0; i < 20; i++ {
		if err := e.a.SyncNow(ctx, e.acc); err != nil {
			if errCode(err) != CodeRateLimited {
				t.Fatal(err)
			}
			break
		}
		n++
	}
	if n != syncNowPerMinute {
		t.Fatalf("accepted %d sync requests, want %d", n, syncNowPerMinute)
	}
}
