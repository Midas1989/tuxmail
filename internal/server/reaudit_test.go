// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// Regression tests of the 2026-11-05 re-audit (docs/security/REAUDIT-2026-11-05.md),
// server findings NA-02 ... NA-08, NH-08.

import (
	"context"
	"encoding/base32"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// totpUser creates a user with an active TOTP enrolment and returns its code
// generator. The service clock is moved one minute back during the enrolment
// so that the codes of "now" are fresh (replay protection).
func (e *env) totpUser(name string, role auth.Role) (*auth.User, auth.TOTP) {
	e.t.Helper()
	u := e.user(name, role)
	enr, err := e.svc.BeginTOTP(context.Background(), u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	tp := auth.TOTP{Secret: secret}
	e.svc.Now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	if _, err := e.svc.ConfirmTOTP(context.Background(), u.ID, tp.Code(e.svc.Now())); err != nil {
		e.t.Fatal(err)
	}
	e.svc.Now = time.Now
	return u, tp
}

// NA-05: the second factor of device approval (and TOTP disable) cannot be
// guessed from many source networks: wrong codes lock the step-up persistently
// per user, no matter which address they come from.
func TestReauditNA05StepUpLockoutAcrossNetworks(t *testing.T) {
	e := newEnv(t, Options{})
	u, tp := e.totpUser("carol", auth.RoleUser)
	// A stolen session: log in once with the second factor, then attack from
	// many addresses with that cookie.
	web := e.browser()
	web.fetchCSRF()
	_, m := web.do("POST", "/api/auth/login", map[string]string{"username": "carol", "password": pw})
	if m["status"] != "mfa_required" {
		t.Fatal(m)
	}
	if resp, m := web.do("POST", "/api/auth/login/totp", map[string]string{"challenge": m["challenge"].(string), "code": tp.Code(time.Now())}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, m)
	}
	web.fetchCSRF()
	_, dm := e.browser().do("POST", "/api/device/authorize", map[string]string{"client_name": "x"})
	userCode := dm["user_code"].(string)

	statuses := map[int]int{}
	for i := 0; i < 30; i++ {
		web.header = http.Header{"X-Forwarded-For": {fmt.Sprintf("203.0.113.%d", i+1)}}
		resp, _ := web.do("POST", "/api/device/approve", map[string]any{"user_code": userCode, "totp_code": "000000"})
		statuses[resp.StatusCode]++
	}
	if statuses[http.StatusBadRequest] > 5 {
		t.Fatalf("%d wrong codes reached the TOTP verification from 30 networks (statuses %v)", statuses[http.StatusBadRequest], statuses)
	}
	if statuses[http.StatusTooManyRequests] < 25 {
		t.Fatalf("no persistent lockout: %v", statuses)
	}
	// Even the right code is refused while locked (fail closed) ...
	web.header = http.Header{"X-Forwarded-For": {"198.51.100.5"}}
	if resp, _ := web.do("POST", "/api/device/approve", map[string]any{"user_code": userCode, "totp_code": tp.Code(time.Now())}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked step-up accepted a code: %s", resp.Status)
	}
	// ... for TOTP disable as well (shared per-user counter) ...
	if resp, _ := web.do("POST", "/api/auth/totp/disable", map[string]string{"password": pw, "code": tp.Code(time.Now())}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("TOTP disable not locked: %s", resp.Status)
	}
	// ... but the normal login path still works.
	login := e.browser()
	login.fetchCSRF()
	if _, m := login.do("POST", "/api/auth/login", map[string]string{"username": "carol", "password": pw, "totpCode": tp.Code(time.Now().Add(30 * time.Second))}); m["status"] != "ok" {
		t.Fatalf("step-up lockout blocks the regular login: %v", m)
	}
	_ = u
}
