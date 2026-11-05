// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// attack fails logins for name from n different networks (the per-network
// budget each) so that the user-wide soft signal of NA-06 is raised.
func attack(s *Service, name string, networks int) {
	ctx := context.Background()
	for n := 1; n <= networks; n++ {
		for range throttleFreeFails {
			s.Login(ctx, LoginRequest{Login: name, Password: "wrong password!", Client: ClientInfo{IP: fmt.Sprintf("203.0.113.%d", n)}})
		}
	}
}

func loginFrom(s *Service, name, pw, ip string) error {
	_, err := s.Login(context.Background(), LoginRequest{Login: name, Password: pw, Client: ClientInfo{IP: ip}})
	return err
}

// NA-06 (E-03 residual): ten or more source networks must not be able to lock
// the owner out; guessing from unknown networks stays refused.
func TestReauditNA06DistributedFailuresDoNotLockOwner(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	s.PressureDelay = 0
	mustUser(t, s, "victim", RoleUser, goodPW)
	// The owner logged in from home before.
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}

	attack(s, "victim", 12) // the PoC of the audit used 10 networks

	var lockedUntil int64
	if err := s.db.QueryRow(`SELECT locked_until FROM login_throttle WHERE key = 'u:victim'`).Scan(&lockedUntil); err != nil {
		t.Fatal(err)
	}
	if lockedUntil != 0 {
		t.Fatal("the user-wide counter must be a soft signal and never lock")
	}
	// The owner at home (same /24, other address) and with the old address.
	for _, ip := range []string{"198.51.100.7", "198.51.100.99"} {
		if err := loginFrom(s, "victim", goodPW, ip); err != nil {
			t.Fatalf("NA-06: owner on a known network locked out by a distributed attack (%s): %v", ip, err)
		}
	}
	// The owner on a brand-new network has to wait: refused without checking
	// the password, audited.
	if err := loginFrom(s, "victim", goodPW, "192.0.2.77"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("unknown network accepted during the attack: %v", err)
	}
	// Distributed guessing stays bounded: even the right password from 100
	// fresh networks is refused (the attacker cannot get a verification).
	for n := 0; n < 100; n++ {
		if err := loginFrom(s, "victim", goodPW, fmt.Sprintf("100.64.%d.%d", n/250, n%250+1)); !errors.Is(err, ErrThrottled) {
			t.Fatalf("network %d got a verification during the attack: %v", n, err)
		}
	}
	audit, _ := s.ListAudit(ctx, 500, 0)
	refused := false
	for _, a := range audit {
		if a.Action == "login" && a.Target == "victim" && a.Details == "refused: account under distributed attack, unknown network" {
			refused = true
		}
	}
	if !refused {
		t.Fatal("refusals are not audited")
	}
	// The attack went on until now, so the signal is still up; after a quiet
	// period everything is back to normal (no administrator needed).
	clk.Add(throttleWindow + time.Second)
	if err := loginFrom(s, "victim", goodPW, "192.0.2.77"); err != nil {
		t.Fatalf("still locked after the attack ended: %v", err)
	}
}

// NA-06: an administrator can end the signal at once.
func TestReauditNA06AdminUnlockEndsPressure(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	s.PressureDelay = 0
	mustUser(t, s, "victim", RoleUser, goodPW)
	attack(s, "victim", 12)
	if err := loginFrom(s, "victim", goodPW, "192.0.2.77"); !errors.Is(err, ErrThrottled) {
		t.Fatal(err)
	}
	s.UnlockLogin(ctx, "victim")
	if err := loginFrom(s, "victim", goodPW, "192.0.2.77"); err != nil {
		t.Fatalf("unlock did not end the signal: %v", err)
	}
}

// NA-06: unknown login names behave like existing ones during an attack (no
// enumeration through the new mode), and are untouched otherwise.
func TestReauditNA06PressureDoesNotRevealWhichNamesExist(t *testing.T) {
	s, _ := newTestService(t)
	s.PressureDelay = 0
	mustUser(t, s, "victim", RoleUser, goodPW)
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if err := loginFrom(s, "ghost", goodPW, "192.0.2.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ghost before attack: %v", err)
	}
	attack(s, "victim", 12)
	attack(s, "ghost", 12)
	e1 := loginFrom(s, "victim", goodPW, "192.0.2.77")
	e2 := loginFrom(s, "ghost", goodPW, "192.0.2.78")
	if !errors.Is(e1, ErrThrottled) || !errors.Is(e2, ErrThrottled) {
		t.Fatalf("existing=%v unknown=%v: both must be refused alike", e1, e2)
	}
	var te1, te2 *ThrottleError
	if !errors.As(e1, &te1) || !errors.As(e2, &te2) || !te1.Until.Equal(te2.Until) && te1.Until.Sub(te2.Until).Abs() > time.Second {
		t.Fatalf("Retry-After differs: %v / %v", e1, e2)
	}
}

// NA-06: under pressure verifications are serialised and slowed, and a flood
// of parallel attempts is refused instead of piling up.
func TestReauditNA06PressureSlowsAndBoundsParallelAttempts(t *testing.T) {
	s, _ := newTestService(t)
	mustUser(t, s, "victim", RoleUser, goodPW)
	s.PressureDelay = 0
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	attack(s, "victim", 12)
	s.PressureDelay = 120 * time.Millisecond

	start := time.Now()
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < s.PressureDelay {
		t.Fatalf("login under attack was not slowed (%v)", d)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	refused, verified := 0, 0
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := loginFrom(s, "victim", "wrong password!", fmt.Sprintf("198.51.100.%d", 100+i))
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrThrottled) {
				refused++
			} else if errors.Is(err, ErrInvalidCredentials) {
				verified++
			}
		}()
	}
	wg.Wait()
	if verified > pressureQueueMax+8 || refused < 40-verified-8 || refused == 0 {
		t.Fatalf("parallel attempts under attack: %d verified, %d refused (queue limit %d)", verified, refused, pressureQueueMax)
	}
}

// NA-06: known networks are stored hashed, bounded and expire after 90 days.
func TestReauditNA06KnownNetworks(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	s.PressureDelay = 0
	u := mustUser(t, s, "victim", RoleUser, goodPW)
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if err := loginFrom(s, "victim", goodPW, "2001:db8:1:2::5"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM login_known_networks WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("known networks: %d", n)
	}
	// No address material in the table.
	var leak int
	s.db.QueryRow(`SELECT COUNT(*) FROM login_known_networks WHERE CAST(net_hash AS TEXT) LIKE '%198.51.100%' OR CAST(net_hash AS TEXT) LIKE '%2001:db8%'`).Scan(&leak)
	if leak != 0 {
		t.Fatal("address stored in clear")
	}
	if !s.networkKnown(ctx, u.ID, "198.51.100.200") || !s.networkKnown(ctx, u.ID, "2001:db8:1:2:ffff::1") {
		t.Fatal("same /24 and /64 are not recognised")
	}
	if s.networkKnown(ctx, u.ID, "198.51.101.7") || s.networkKnown(ctx, u.ID, "2001:db8:1:3::5") {
		t.Fatal("other networks recognised")
	}
	// Expiry: logout first so that the live session does not keep it known.
	s.db.Exec(`DELETE FROM sessions`)
	clk.Add(knownNetworkTTL + time.Hour)
	if s.networkKnown(ctx, u.ID, "198.51.100.7") {
		t.Fatal("network still known after 90 days")
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM login_known_networks WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("expired networks not cleaned up: %d", n)
	}
	// Bounded per user.
	for i := range maxKnownNetworks + 20 {
		s.recordKnownNetwork(ctx, u.ID, fmt.Sprintf("10.%d.%d.1", i/200, i%200))
		clk.Add(time.Second)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM login_known_networks WHERE user_id = ?`, u.ID).Scan(&n)
	if n != maxKnownNetworks {
		t.Fatalf("known networks per user not bounded: %d", n)
	}
}

// NA-06: the success of the owner does not reset the attacker's budget.
func TestReauditNA06OwnerLoginKeepsUserCounter(t *testing.T) {
	s, _ := newTestService(t)
	s.PressureDelay = 0
	mustUser(t, s, "victim", RoleUser, goodPW)
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	attack(s, "victim", 12)
	if err := loginFrom(s, "victim", goodPW, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if err := loginFrom(s, "victim", goodPW, "192.0.2.77"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("owner login reset the attack signal: %v", err)
	}
}
