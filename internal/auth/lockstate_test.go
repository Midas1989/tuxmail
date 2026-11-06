// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The administration shows which accounts are locked (an attacking network is
// locked out) or under pressure (distributed guessing) and unlock clears both.
func TestLockStates(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	s.PressureDelay = 0
	mustUser(t, s, "Victim", RoleUser, goodPW)
	mustUser(t, s, "calm", RoleUser, goodPW)
	names := []string{"Victim", "calm"}

	if m, err := s.LockStates(ctx, names); err != nil || len(m) != 0 {
		t.Fatalf("fresh accounts are not locked: %v %v", m, err)
	}

	// Five wrong passwords from one network lock that network (not the owner).
	for range throttleFreeFails {
		loginFrom(s, "victim", "wrong password!", "203.0.113.1")
	}
	m, err := s.LockStates(ctx, names)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := m["Victim"]
	if !ok || !st.Locked || st.UnderPressure || !st.LockedUntil.After(clk.Now()) {
		t.Fatalf("victim must be locked, not under pressure: %+v", m)
	}
	if _, ok := m["calm"]; ok {
		t.Fatal("an untouched account must not appear")
	}
	if one, _ := s.LockStateOf(ctx, "Victim"); one != st {
		t.Fatalf("LockStateOf differs: %+v vs %+v", one, st)
	}

	// The lock ends by itself.
	clk.Add(throttleMaxLock + time.Second)
	if m, _ := s.LockStates(ctx, names); len(m) != 0 {
		t.Fatalf("an expired lock must not be reported: %v", m)
	}

	// Distributed guessing raises the soft signal.
	attack(s, "victim", 12)
	st, _ = s.LockStateOf(ctx, "Victim")
	if !st.UnderPressure || !st.PressureUntil.After(clk.Now()) {
		t.Fatalf("12 networks of failures must put the account under pressure: %+v", st)
	}

	// Unlock clears the network locks and the pressure signal.
	s.UnlockLogin(ctx, "Victim")
	if m, _ := s.LockStates(ctx, names); len(m) != 0 {
		t.Fatalf("unlock left state behind: %v", m)
	}
}

// A guest typing "name|1.2.3.4" as login name must not make the real account
// look locked or under pressure: the lookup uses the name before the LAST "|"
// only for network keys, and an exact match for the user-wide key.
func TestLockStatesIgnoresForgedKeys(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	mustUser(t, s, "alice", RoleUser, goodPW)
	for range 60 {
		s.recordFailure(ctx, throttleKey("alice|198.51.100.1"), userPolicy)
	}
	for n := range 10 {
		loginFrom(s, fmt.Sprintf("alice|x%d", n), "wrong password!", "203.0.113.9")
	}
	if m, _ := s.LockStates(ctx, []string{"alice"}); len(m) != 0 {
		t.Fatalf("forged login names must not mark alice: %v", m)
	}
}

func TestQueryAuditFilters(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	root := WithActor(ctx, Actor{Name: "root"})
	other := WithActor(ctx, Actor{Name: "other_user"})
	s.Audit(root, "user.create", "a", true, "")
	s.Audit(root, "user.delete", "a", true, "")
	s.Audit(other, "login", "b", false, "wrong password")
	s.Audit(other, "login", "b", true, "")

	count := func(f AuditFilter) int {
		t.Helper()
		l, err := s.QueryAudit(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	if n := count(AuditFilter{}); n != 4 {
		t.Fatalf("all: %d", n)
	}
	if n := count(AuditFilter{Action: "user."}); n != 2 {
		t.Fatalf("action prefix: %d", n)
	}
	if n := count(AuditFilter{Action: "USER.CREATE"}); n != 1 {
		t.Fatalf("action is case-insensitive: %d", n)
	}
	if n := count(AuditFilter{Actor: "other_"}); n != 2 {
		t.Fatalf("actor prefix: %d", n)
	}
	// "_" and "%" are literal, not LIKE wildcards.
	if n := count(AuditFilter{Actor: "other%"}); n != 0 {
		t.Fatalf("%% must be literal: %d", n)
	}
	if n := count(AuditFilter{Actor: "o_her"}); n != 0 {
		t.Fatalf("_ must be literal: %d", n)
	}
	if n := count(AuditFilter{OnlyFailed: true}); n != 1 {
		t.Fatalf("only failed: %d", n)
	}
	// Paging backwards.
	first, _ := s.QueryAudit(ctx, AuditFilter{Limit: 2})
	if len(first) != 2 {
		t.Fatal(first)
	}
	next, _ := s.QueryAudit(ctx, AuditFilter{Limit: 2, BeforeID: first[1].ID})
	if len(next) != 2 || next[0].ID >= first[1].ID {
		t.Fatalf("paging: %+v", next)
	}
}
