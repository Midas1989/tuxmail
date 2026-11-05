// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Audit NA-01: an unauthenticated client must not be able to persist huge
// login names in login_throttle and audit_log.
func TestAuditNA01OversizedLoginNotPersisted(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	mustUser(t, s, "admin", RoleAdmin, goodPW)

	huge := strings.Repeat("a", 500<<10)
	for i := 0; i < 5; i++ {
		_, err := s.Login(ctx, LoginRequest{Login: huge, Password: "x", Client: ClientInfo{IP: "203.0.113.9"}})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("oversized login: got %v, want ErrInvalidCredentials", err)
		}
	}
	var keyBytes, targetBytes int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(key)),0) FROM login_throttle`).Scan(&keyBytes); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(target)),0) FROM audit_log`).Scan(&targetBytes); err != nil {
		t.Fatal(err)
	}
	if keyBytes > 1024 || targetBytes > 4096 {
		t.Fatalf("oversized login names persisted: throttle key bytes %d, audit target bytes %d", keyBytes, targetBytes)
	}

	// Normal failures still count against the throttle and are audited.
	_, err := s.Login(ctx, LoginRequest{Login: "admin", Password: "wrong", Client: ClientInfo{IP: "203.0.113.9"}})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM login_throttle WHERE key LIKE 'u:admin%'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("regular failed login no longer throttled: n=%d err=%v", n, err)
	}
}

// Audit entries never store unbounded strings, whoever calls Audit.
func TestAuditTargetTruncated(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{Name: strings.Repeat("n", 5000), IP: "198.51.100.1"})
	s, _ := newTestService(t)
	_ = s.Audit(ctx, "test", strings.Repeat("t", 100000), false, strings.Repeat("d", 100000))
	var actor, target, details int
	if err := s.db.QueryRow(`SELECT LENGTH(actor), LENGTH(target), LENGTH(details) FROM audit_log WHERE action = 'test'`).Scan(&actor, &target, &details); err != nil {
		t.Fatal(err)
	}
	if actor > 100 || target > 320 || details > 1000 {
		t.Fatalf("untruncated audit entry: actor %d target %d details %d", actor, target, details)
	}
}
