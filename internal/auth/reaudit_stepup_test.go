// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func enableTOTP(t *testing.T, s *Service, clk *clock, id int64) TOTP {
	t.Helper()
	ctx := context.Background()
	enr, err := s.BeginTOTP(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b32.DecodeString(enr.Secret)
	tp := TOTP{Secret: secret}
	if _, err := s.ConfirmTOTP(ctx, id, tp.Code(clk.Now())); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Minute) // fresh TOTP step for the next code
	return tp
}

// NA-05: wrong step-up codes lock the step-up functions of the user
// persistently, independent of the client network; the normal login stays
// available.
func TestReauditNA05StepUpSecondFactorLockout(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "carol", RoleUser, goodPW)
	tp := enableTOTP(t, s, clk, u.ID)

	for i := range stepUpFreeFails {
		if err := s.VerifyStepUpSecondFactor(ctx, u.ID, "000000", ""); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	// Locked: even the correct code is refused now (fail closed).
	err := s.VerifyStepUpSecondFactor(ctx, u.ID, tp.Code(clk.Now()), "")
	var te *ThrottleError
	if !errors.As(err, &te) {
		t.Fatalf("step-up not locked after %d wrong codes: %v", stepUpFreeFails, err)
	}
	var rows int
	s.db.QueryRow(`SELECT COUNT(*) FROM login_throttle WHERE key = ?`, stepUpKey(u.ID)).Scan(&rows)
	if rows != 1 {
		t.Fatal("step-up failures are not persisted")
	}
	// The normal login path is not locked by it.
	if res, err := s.Login(ctx, LoginRequest{Login: "carol", Password: goodPW, TOTPCode: tp.Code(clk.Now()), Client: ClientInfo{IP: "192.0.2.9"}}); err != nil || res.Token == "" {
		t.Fatalf("normal login blocked by step-up lockout: %v", err)
	}
	clk.Add(time.Minute)
	if err := s.VerifyStepUpSecondFactor(ctx, u.ID, tp.Code(clk.Now()), ""); !errors.Is(err, ErrThrottled) {
		t.Fatalf("lock ended too early: %v", err)
	}
	// After the lock the owner can continue; ClearStepUp resets the counter.
	clk.Add(stepUpBaseLock + time.Second)
	if err := s.VerifyStepUpSecondFactor(ctx, u.ID, tp.Code(clk.Now()), ""); err != nil {
		t.Fatalf("owner locked out for good: %v", err)
	}
	s.ClearStepUp(ctx, u.ID)
	s.db.QueryRow(`SELECT COUNT(*) FROM login_throttle WHERE key = ?`, stepUpKey(u.ID)).Scan(&rows)
	if rows != 0 {
		t.Fatal("ClearStepUp left the counter")
	}
}

// NA-05: the reverse direction: failed LOGINS do not lock step-up.
func TestReauditNA05LoginFailuresDoNotLockStepUp(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "dora", RoleUser, goodPW)
	tp := enableTOTP(t, s, clk, u.ID)
	for range throttleFreeFails + 2 {
		s.Login(ctx, LoginRequest{Login: "dora", Password: "wrong password!", Client: ClientInfo{IP: "192.0.2.10"}})
	}
	if err := s.VerifyStepUpSecondFactor(ctx, u.ID, tp.Code(clk.Now()), ""); err != nil {
		t.Fatalf("login lockout blocks step-up: %v", err)
	}
}

// NA-05: guessing the current password through ChangePassword is counted too.
func TestReauditNA05ChangePasswordGuessingLocks(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	u := mustUser(t, s, "erik", RoleUser, goodPW)
	for range stepUpFreeFails {
		if err := s.ChangePassword(ctx, u.ID, "wrong password!", "a new good password", 0); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if err := s.ChangePassword(ctx, u.ID, goodPW, "a new good password", 0); !errors.Is(err, ErrThrottled) {
		t.Fatalf("password guessing via ChangePassword is not locked: %v", err)
	}
}

// t-102: an empty request (an opened form) neither counts nor extends a lock;
// a wrong password while locked does not extend it either; UnlockLogin lifts
// the step-up lock together with the sign-in lock.
func TestStepUpEmptyRequestAndUnlock(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	u := mustUser(t, s, "dora", RoleUser, goodPW)
	sess := &Session{LoginMethod: MethodPassword, CreatedAt: s.now()}
	counter := func() (fails int, until int64) {
		s.db.QueryRow(`SELECT failures, locked_until FROM login_throttle WHERE key = ?`, stepUpKey(u.ID)).Scan(&fails, &until)
		return
	}
	// Empty requests: never counted.
	for range 20 {
		if err := s.verifyStepUp(ctx, u, sess, "", "", ""); !errors.Is(err, ErrReauthRequired) {
			t.Fatal(err)
		}
	}
	if f, _ := counter(); f != 0 {
		t.Fatalf("empty requests were counted: %d", f)
	}
	for range stepUpFreeFails {
		s.verifyStepUp(ctx, u, sess, "wrong password!", "", "")
	}
	f, until := counter()
	if until == 0 {
		t.Fatalf("not locked after %d wrong passwords (%d)", stepUpFreeFails, f)
	}
	// Locked: empty and wrong requests change nothing.
	for _, pw := range []string{"", "wrong password!"} {
		err := s.verifyStepUp(ctx, u, sess, pw, "", "")
		if pw == "" && !errors.Is(err, ErrReauthRequired) || pw != "" && !errors.Is(err, ErrThrottled) {
			t.Fatalf("pw=%q: %v", pw, err)
		}
	}
	if f2, u2 := counter(); f2 != f || u2 != until {
		t.Fatalf("lock changed while locked: %d/%d -> %d/%d", f, until, f2, u2)
	}
	s.UnlockLogin(ctx, "DORA")
	if f, _ := counter(); f != 0 {
		t.Fatal("UnlockLogin left the step-up counter")
	}
	if err := s.verifyStepUp(ctx, u, sess, goodPW, "", ""); err != nil {
		t.Fatalf("owner still locked: %v", err)
	}
}
