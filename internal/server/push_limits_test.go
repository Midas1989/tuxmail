package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/server/push"
)

// L-8: a user's subscriptions are capped, another user's endpoint cannot be
// taken over, and test notifications are rate limited.
func TestPushSubscriptionLimits(t *testing.T) {
	e, _ := bgEnv(t)
	e.user("hal", auth.RoleUser)
	e.user("ida", auth.RoleUser)
	hal, ida := e.browser(), e.browser()
	hal.login("hal")
	ida.login("ida")
	fcmSub := func(n int) map[string]any {
		return map[string]any{"type": "fcm", "token": fmt.Sprintf("fcm-token-%02d-0123456789abcdefghij", n), "deviceName": "d"}
	}
	for i := range push.MaxSubscriptionsPerUser + 3 {
		if resp, m := hal.do("POST", "/api/push/subscribe", fcmSub(i)); resp.StatusCode != http.StatusCreated {
			t.Fatal(i, resp.Status, m)
		}
	}
	_, m := hal.do("GET", "/api/push/subscriptions", nil)
	subs := m["subscriptions"].([]any)
	if len(subs) != push.MaxSubscriptionsPerUser {
		t.Fatalf("%d subscriptions kept, want %d", len(subs), push.MaxSubscriptionsPerUser)
	}
	// The newest survive, the oldest made room.
	if got := subs[len(subs)-1].(map[string]any)["endpoint"]; got != push.FCMEndpoint(fcmSub(push.MaxSubscriptionsPerUser + 2)["token"].(string)) {
		t.Errorf("newest subscription missing: %v", got)
	}
	// Re-registering an own endpoint is an update, not a new entry.
	if resp, _ := hal.do("POST", "/api/push/subscribe", fcmSub(push.MaxSubscriptionsPerUser+2)); resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	// Another user cannot take over hal's endpoint.
	if resp, _ := ida.do("POST", "/api/push/subscribe", fcmSub(push.MaxSubscriptionsPerUser+2)); resp.StatusCode != http.StatusConflict {
		t.Fatalf("takeover: %s", resp.Status)
	}
	_, m = ida.do("GET", "/api/push/subscriptions", nil)
	if len(m["subscriptions"].([]any)) != 0 {
		t.Fatal("ida got a subscription", m)
	}
	_, m = hal.do("GET", "/api/push/subscriptions", nil)
	if len(m["subscriptions"].([]any)) != push.MaxSubscriptionsPerUser {
		t.Fatal("hal lost subscriptions", m)
	}
	// Test notifications: a short burst, then 429.
	limited := false
	for range 10 {
		if resp, _ := hal.do("POST", "/api/push/test", nil); resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("push test is not rate limited")
	}
}

// I-18: confirming the TOTP enrolment is rate limited like the other MFA endpoints.
func TestTOTPConfirmRateLimited(t *testing.T) {
	e := newEnv(t, Options{})
	e.user("tom", auth.RoleUser)
	b := e.browser()
	b.login("tom")
	for range 200 {
		if resp, _ := b.do("POST", "/api/auth/totp/confirm", map[string]string{"code": "000000"}); resp.StatusCode == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatal("totp confirm is not rate limited")
}
