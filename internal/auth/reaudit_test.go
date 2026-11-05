// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

// Regression tests of the 2026-11-05 re-audit (docs/security/REAUDIT-2026-11-05.md),
// server findings NA-02 ... NA-06.

import (
	"context"
	"errors"
	"testing"
)

// NA-04: a disabled account answers a correct and a wrong password alike.
func TestReauditNA04DisabledAccountIsNoPasswordOracle(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	mustUser(t, s, "root", RoleAdmin, goodPW)
	u := mustUser(t, s, "gone", RoleUser, goodPW)
	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	_, errWrong := s.Login(ctx, LoginRequest{Login: "gone", Password: "wrong password!", Client: ClientInfo{IP: "192.0.2.1"}})
	_, errRight := s.Login(ctx, LoginRequest{Login: "gone", Password: goodPW, Client: ClientInfo{IP: "192.0.2.2"}})
	if !errors.Is(errWrong, ErrInvalidCredentials) || !errors.Is(errRight, ErrInvalidCredentials) {
		t.Fatalf("wrong=%v right=%v: a disabled account must answer ErrInvalidCredentials in both cases", errWrong, errRight)
	}
	if errors.Is(errRight, ErrUserDisabled) {
		t.Fatal("correct password reveals the disabled state")
	}
	// The correct password counts as a failure, like a wrong one.
	for range throttleFreeFails {
		s.Login(ctx, LoginRequest{Login: "gone", Password: goodPW, Client: ClientInfo{IP: "192.0.2.3"}})
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "gone", Password: goodPW, Client: ClientInfo{IP: "192.0.2.3"}}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("guessing against a disabled account is not throttled: %v", err)
	}
	// The detail stays visible for administrators.
	audit, err := s.ListAudit(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range audit {
		if e.Action == "login" && e.Target == "gone" && e.Details == "disabled (correct password)" {
			found = true
		}
	}
	if !found {
		t.Fatal("audit log does not record that a disabled account presented the correct password")
	}
}
