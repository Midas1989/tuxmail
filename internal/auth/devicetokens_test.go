// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
)

func deviceLogin(t *testing.T, s *Service, name, scopes string) *LoginResult {
	t.Helper()
	res, err := s.Login(context.Background(), LoginRequest{Login: name, Password: goodPW, Kind: KindDevice,
		Client: ClientInfo{IP: "192.0.2.1", DeviceName: "Handy", PubKey: make([]byte, 32), Scopes: scopes}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestWebSessionIdleAndAbsoluteLimit(t *testing.T) {
	s, clk := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	login := func() string {
		res, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "192.0.2.1"}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Token
	}
	// Idle: 2 hours (Basis spec/PARAMETER.md), counted from the last request.
	tok := login()
	clk.Add(2*time.Hour - time.Minute)
	sess, _, err := s.Authenticate(ctx, tok, "192.0.2.1")
	if err != nil {
		t.Fatal("active session ended:", err)
	}
	clk.Add(2*time.Hour - time.Minute)
	if err := s.CheckSession(ctx, sess.UserID, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, tok, "192.0.2.1"); err != nil {
		t.Fatal("session with activity ended:", err)
	}
	clk.Add(2 * time.Hour)
	if err := s.CheckSession(ctx, sess.UserID, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stream check after idle limit: %v", err)
	}
	if list, _ := s.ListSessions(ctx, sess.UserID); len(list) != 0 {
		t.Fatal("idle session listed")
	}
	if _, _, err := s.Authenticate(ctx, tok, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after idle limit: %v", err)
	}
	// Absolute: 12 hours, however active.
	tok = login()
	for range 11 {
		clk.Add(time.Hour)
		if _, _, err := s.Authenticate(ctx, tok, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	clk.Add(time.Hour)
	if _, _, err := s.Authenticate(ctx, tok, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after absolute limit: %v", err)
	}
}

func TestConfigCannotExtendSessionLimits(t *testing.T) {
	s, clk := newTestService(t, func(c *config.Config) {
		c.Auth.SessionLifetime = config.Duration(720 * time.Hour)
		c.Auth.SessionIdleTimeout = config.Duration(100 * time.Hour)
	})
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	res, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "192.0.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Session.ExpiresAt.Sub(clk.Now()); d != 12*time.Hour {
		t.Fatalf("expires after %v", d)
	}
	clk.Add(2 * time.Hour)
	if _, _, err := s.Authenticate(ctx, res.Token, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("idle limit was extended: %v", err)
	}
	// Shorter is allowed.
	s2, clk2 := newTestService(t, func(c *config.Config) { c.Auth.SessionIdleTimeout = config.Duration(15 * time.Minute) })
	mustUser(t, s2, "ida", RoleUser, goodPW)
	res, err = s2.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "192.0.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	clk2.Add(15 * time.Minute)
	if _, _, err := s2.Authenticate(ctx, res.Token, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("short idle limit ignored: %v", err)
	}
}

// Web sessions from before the limits end by them; Cleanup removes them.
func TestExistingLongWebSessionEnds(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	if _, _, err := s.Authenticate(ctx, li.webToken, "192.0.2.1"); err != nil {
		t.Fatal("fresh old session ended:", err)
	}
	s.Now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = 12`).Scan(&n)
	if n != 0 {
		t.Fatal("web session past its limit kept by Cleanup")
	}
	if _, _, err := s.Authenticate(ctx, li.webToken, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDeviceTokenHasNoIdleLimitButEnds(t *testing.T) {
	s, clk := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	res := deviceLogin(t, s, "ida", "")
	clk.Add(89 * 24 * time.Hour)
	if _, _, err := s.Authenticate(ctx, res.Token, ""); err != nil {
		t.Fatal(err)
	}
	clk.Add(24 * time.Hour)
	if _, _, err := s.Authenticate(ctx, res.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestRotationAndReuse(t *testing.T) {
	s, clk := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	res := deviceLogin(t, s, "ida", "")
	t1 := res.Token
	if _, err := s.db.Exec(`INSERT INTO push_subscriptions(user_id, session_id, endpoint, p256dh, auth, created_at) VALUES (?, ?, 'https://e/1', 'k', 'a', 1)`, res.User.ID, res.Session.ID); err != nil {
		t.Fatal(err)
	}
	clk.Add(30 * 24 * time.Hour)
	sess, _, err := s.Authenticate(ctx, t1, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := s.RotateDeviceToken(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	// T1 stays good until T2 is used: an answer lost on the way harms nobody.
	if _, _, err := s.Authenticate(ctx, t1, "192.0.2.1"); err != nil {
		t.Fatal("old token before first use of the new one:", err)
	}
	sess2, _, err := s.Authenticate(ctx, t2, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	// Same family: same row, same client key, same scopes; lifetime starts anew.
	if sess2.ID != res.Session.ID || len(sess2.ClientPubKey) != 32 || !sess2.HasScope(ScopeData) || !sess2.HasScope(ScopeProvisioning) {
		t.Fatalf("%+v", sess2)
	}
	if d := sess2.ExpiresAt.Sub(clk.Now()); d != DeviceTokenLifetime {
		t.Fatalf("new token lives %v", d)
	}
	// Requests still on their way pass inside the leeway, but cannot rotate.
	clk.Add(DeviceTokenLeeway)
	old, _, err := s.Authenticate(ctx, t1, "192.0.2.1")
	if err != nil {
		t.Fatal("in-flight request:", err)
	}
	if _, _, err := s.RotateDeviceToken(ctx, old, t1); !errors.Is(err, ErrTokenSuperseded) {
		t.Fatalf("rotate with a superseded token: %v", err)
	}
	// After it: reuse. The whole family ends, also for whoever holds T2.
	clk.Add(time.Second)
	if _, _, err := s.Authenticate(ctx, t1, "198.51.100.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reused token: %v", err)
	}
	if !auditHas(t, s, "device.token_reuse", "family revoked", false) {
		t.Fatal("reuse not audited")
	}
	if _, _, err := s.Authenticate(ctx, t2, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("family member after reuse: %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM device_retired_tokens) + (SELECT COUNT(*) FROM push_subscriptions)`).Scan(&n)
	if n != 0 {
		t.Fatal("rows of the revoked family left:", n)
	}
	// Another use of the old token is now simply unknown.
	if _, _, err := s.Authenticate(ctx, t1, "198.51.100.1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStolenTokenUsedFirstRevokesFamily(t *testing.T) {
	s, clk := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	t1 := deviceLogin(t, s, "ida", "").Token
	// The thief rotates with the stolen T1 and uses the successor.
	sess, _, _ := s.Authenticate(ctx, t1, "203.0.113.5")
	stolen, _, err := s.RotateDeviceToken(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, stolen, "203.0.113.5"); err != nil {
		t.Fatal(err)
	}
	clk.Add(DeviceTokenLeeway + time.Second)
	// The real device still has T1: detected, and the thief is out as well.
	if _, _, err := s.Authenticate(ctx, t1, "192.0.2.1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, stolen, "203.0.113.5"); !errors.Is(err, ErrNotFound) {
		t.Fatal("thief's token survived")
	}
}

func TestRotateAnswerLostThenRepeated(t *testing.T) {
	s, _ := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	t1 := deviceLogin(t, s, "ida", "").Token
	sess, _, _ := s.Authenticate(ctx, t1, "")
	lost, _, err := s.RotateDeviceToken(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := s.RotateDeviceToken(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, lost, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("replaced successor accepted", err)
	}
	if _, _, err := s.Authenticate(ctx, t1, ""); err != nil {
		t.Fatal("failed rotation must not lock the device out:", err)
	}
	if _, _, err := s.Authenticate(ctx, t2, ""); err != nil {
		t.Fatal(err)
	}
	// Chain: T3 from T2 still works and T1 stays superseded.
	sess2, _, _ := s.Authenticate(ctx, t2, "")
	t3, _, err := s.RotateDeviceToken(ctx, sess2, t2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, t3, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM device_retired_tokens`).Scan(&n)
	if n != 2 {
		t.Fatalf("retired tokens: %d", n)
	}
}

func TestParallelFirstUseOfSuccessor(t *testing.T) {
	s, _ := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	t1 := deviceLogin(t, s, "ida", "").Token
	sess, _, _ := s.Authenticate(ctx, t1, "")
	t2, _, err := s.RotateDeviceToken(ctx, sess, t1)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			_, _, err := s.Authenticate(ctx, t2, "")
			errs <- err
		}()
	}
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogoutWithAnyTokenOfTheFamily(t *testing.T) {
	s, _ := newTestService(t)
	mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	t1 := deviceLogin(t, s, "ida", "").Token
	sess, _, _ := s.Authenticate(ctx, t1, "")
	t2, _, _ := s.RotateDeviceToken(ctx, sess, t1)
	s.Authenticate(ctx, t2, "")
	if err := s.Logout(ctx, t1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, t2, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("token alive after logout: %v", err)
	}
}

// Tokens of the previous release keep working without a new login; their next
// use puts them on the new lifetime. A client that never rotates runs until then.
func TestExistingDeviceTokenUpgradedOnNextUse(t *testing.T) {
	li := newLegacyInstall(t)
	s := li.open(t)
	ctx := context.Background()
	var exp, unusedExp int64
	s.db.QueryRow(`SELECT expires_at FROM sessions WHERE id = 10`).Scan(&exp)
	s.db.QueryRow(`SELECT expires_at FROM sessions WHERE id = 11`).Scan(&unusedExp)
	sess, u, err := s.Authenticate(ctx, li.deviceToken, "192.0.2.1")
	if err != nil {
		t.Fatal("existing device token refused:", err)
	}
	if u.ID != 1 || sess.ID != 10 || len(sess.ClientPubKey) != 32 || !sess.HasScope(ScopeData) || !sess.HasScope(ScopeProvisioning) {
		t.Fatalf("%+v", sess)
	}
	if d := time.Until(sess.ExpiresAt); d > DeviceTokenLifetime || d < DeviceTokenLifetime-time.Minute {
		t.Fatalf("remaining life %v", d)
	}
	if !auditHas(t, s, "device.token_upgrade", "session=10", true) {
		t.Fatal("upgrade not audited")
	}
	// A shorter remaining life is not extended.
	sess2, _, err := s.Authenticate(ctx, li.unusedDevice, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if sess2.ExpiresAt.Unix() != unusedExp {
		t.Fatal("remaining life changed")
	}
	// Reuse detection works for them after a rotation.
	next, _, err := s.RotateDeviceToken(ctx, sess, li.deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, next, ""); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Now().Add(DeviceTokenLeeway + time.Minute) }
	if _, _, err := s.Authenticate(ctx, li.deviceToken, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, next, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("family survived the reuse")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE session_id = 10`).Scan(&n)
	if n != 0 {
		t.Fatal("push subscription of the revoked family kept")
	}
}

func TestDeviceScopes(t *testing.T) {
	for in, want := range map[string]string{
		"":                  DefaultDeviceScopes,
		"data":              "data",
		"provisioning data": "data provisioning",
		" data  data ":      "data",
	} {
		if got, err := ParseDeviceScopes(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"admin", "data admin", "*", "verwaltung"} {
		if _, err := ParseDeviceScopes(bad); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// The device grant carries the scopes to the token.
	s, clk := newTestService(t)
	u := mustUser(t, s, "ida", RoleUser, goodPW)
	ctx := context.Background()
	g, err := s.StartDeviceAuthorization(ctx, ClientInfo{DeviceName: "x", Scopes: "data"})
	if err != nil {
		t.Fatal(err)
	}
	if req, err := s.LookupDeviceRequest(ctx, g.UserCode); err != nil || len(req.Scopes) != 1 {
		t.Fatal(req, err)
	}
	if err := s.ApproveDevice(ctx, u.ID, g.UserCode, true); err != nil {
		t.Fatal(err)
	}
	clk.Add(10 * time.Second)
	res, err := s.PollDeviceToken(ctx, g.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if res.Session.HasScope(ScopeProvisioning) || !res.Session.HasScope(ScopeData) {
		t.Fatal(res.Session.Scopes)
	}
}

// Basis spec/PARAMETER.md, spec/AUTH.md §6.
func TestLoginLimitsInDatabase(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	mustUser(t, s, "ida", RoleUser, goodPW)
	throttled := func(err error) bool { return errors.Is(err, ErrThrottled) }
	// 11th failure at the same account: refused (the per-network lock is even earlier).
	seen := false
	for range 11 {
		_, err := s.Login(ctx, LoginRequest{Login: "ida", Password: "falsch-falsch-falsch", Client: ClientInfo{IP: "192.0.2.50"}})
		seen = seen || throttled(err)
	}
	if !seen {
		t.Fatal("11th failure not limited")
	}
	// 20 failures per address in 5 minutes, across names, kept in the database.
	for i := range 20 {
		s.Login(ctx, LoginRequest{Login: "name" + string(rune('a'+i)), Password: "falsch-falsch-falsch", Client: ClientInfo{IP: "2001:db8:1:2::1"}})
	}
	_, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "2001:db8:1:2:ffff::9"}})
	if !throttled(err) {
		t.Fatalf("21st attempt from the same /64: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "2001:db8:1:3::1"}}); err != nil {
		t.Fatalf("other network: %v", err)
	}
	clk.Add(5 * time.Minute)
	if _, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "2001:db8:1:2::1"}}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// Working logins do not use up an address (ADR 0007 no. 9).
	for range 30 {
		if _, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, Client: ClientInfo{IP: "192.0.2.77"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTOTPLimitPerAccount(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	u := mustUser(t, s, "ida", RoleUser, goodPW)
	enrollTOTPForTest(t, s, u.ID)
	// From changing networks, so the per-network lock does not interfere.
	for i := range 5 {
		_, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, TOTPCode: "000000", Client: ClientInfo{IP: "192.0.2." + string(rune('1'+i))}})
		if errors.Is(err, ErrThrottled) {
			t.Fatalf("try %d limited early", i)
		}
	}
	_, err := s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, TOTPCode: "000000", Client: ClientInfo{IP: "192.0.2.9"}})
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("6th wrong code: %v", err)
	}
	clk.Add(5 * time.Minute)
	_, err = s.Login(ctx, LoginRequest{Login: "ida", Password: goodPW, TOTPCode: "000000", Client: ClientInfo{IP: "192.0.2.9"}})
	if errors.Is(err, ErrThrottled) {
		t.Fatalf("limit did not end: %v", err)
	}
}

func TestPasswordLengthLimit(t *testing.T) {
	s, _ := newTestService(t)
	long := make([]rune, 129)
	for i := range long {
		long[i] = rune('a' + i%26)
	}
	if _, err := s.CreateUser(context.Background(), NewUser{Username: "x", Role: RoleUser, Password: string(long)}); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("129 characters: %v", err)
	}
	if _, err := s.CreateUser(context.Background(), NewUser{Username: "y", Role: RoleUser, Password: string(long[:128])}); err != nil {
		t.Fatalf("128 characters: %v", err)
	}
}

func enrollTOTPForTest(t *testing.T, s *Service, userID int64) {
	t.Helper()
	ctx := context.Background()
	enr, err := s.BeginTOTP(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := b32.DecodeString(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmTOTP(ctx, userID, TOTP{Secret: sec}.Code(s.now())); err != nil {
		t.Fatal(err)
	}
}
