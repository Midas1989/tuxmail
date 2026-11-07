// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
)

// Device approval at the identity provider (device_oidc.go): a new sign-in
// there, started after the request was shown, bound to user and user code,
// with auth_time after the start.
func TestOIDCDeviceApproval(t *testing.T) {
	ctx := context.Background()
	idp := newFakeIdP(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("s3cret\n"), 0o600)
	s, _ := newTestService(t, func(c *config.Config) {
		c.Auth.OIDC = config.OIDC{Enabled: true, Issuer: idp.srv.URL, ClientID: "tuxmail", ClientSecretFile: secretFile, UserGroup: "tm-users", GroupsClaim: "groups"}
	})
	s.Now = time.Now
	ida := map[string]any{"sub": "ida-sub", "preferred_username": "ida", "groups": []string{"tm-users"}}
	au, _ := s.OIDCAuthURL(ctx, "/")
	st, code := idp.authorize(t, au, ida)
	res, _, err := s.OIDCCallback(ctx, st, code, ClientInfo{IP: "192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	user := res.User
	other := map[string]any{"sub": "eve-sub", "preferred_username": "eve", "groups": []string{"tm-users"}}
	au, _ = s.OIDCAuthURL(ctx, "/")
	st, code = idp.authorize(t, au, other)
	if _, _, err := s.OIDCCallback(ctx, st, code, ClientInfo{}); err != nil {
		t.Fatal(err)
	}

	grant := func() *DeviceGrant {
		g, err := s.StartDeviceAuthorization(ctx, ClientInfo{DeviceName: "Pixel"})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	approveWith := func(g *DeviceGrant, claims map[string]any) error {
		t.Helper()
		u, err := s.OIDCDeviceApprovalURL(ctx, user.ID, g.UserCode)
		if err != nil {
			t.Fatal(err)
		}
		q, _ := url.Parse(u)
		if q.Query().Get("prompt") != "login" || q.Query().Get("max_age") != "0" {
			t.Fatalf("no fresh authentication requested: %s", u)
		}
		st, code := idp.authorize(t, u, claims)
		res, ret, err := s.OIDCCallback(ctx, st, code, ClientInfo{})
		if res != nil {
			t.Fatal("a device approval created a session")
		}
		if err == nil && ret != DeviceApprovedPath {
			t.Fatalf("return path %q", ret)
		}
		return err
	}
	with := func(base map[string]any, k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range base {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	pending := func(g *DeviceGrant) bool {
		_, err := s.PollDeviceToken(ctx, g.DeviceCode)
		return errors.Is(err, ErrAuthorizationPending) || errors.Is(err, ErrSlowDown)
	}

	// The IdP did not ask again (auth_time before the start): refused.
	g := grant()
	if err := approveWith(g, with(ida, "auth_time", float64(time.Now().Add(-10*time.Minute).Unix()))); !errors.Is(err, ErrOIDCApproval) {
		t.Fatalf("old auth_time: %v", err)
	}
	// No auth_time at all: refused.
	if err := approveWith(g, with(ida, "auth_time", nil)); !errors.Is(err, ErrOIDCApproval) {
		t.Fatalf("no auth_time: %v", err)
	}
	// Signed in as somebody else: refused.
	if err := approveWith(g, with(other, "auth_time", float64(time.Now().Unix()))); !errors.Is(err, ErrOIDCApproval) {
		t.Fatalf("other account: %v", err)
	}
	if !pending(g) {
		t.Fatal("a refused confirmation approved the request")
	}
	s.ClearStepUp(ctx, user.ID)
	// A new sign-in of the same account: approved, this request only.
	g2 := grant()
	if err := approveWith(g, with(ida, "auth_time", float64(time.Now().Unix()))); err != nil {
		t.Fatal(err)
	}
	if res, err := s.PollDeviceToken(ctx, g.DeviceCode); err != nil || res.Token == "" {
		t.Fatalf("token: %v", err)
	}
	if !pending(g2) {
		t.Fatal("another request was approved too")
	}
	// The state is single use.
	u, _ := s.OIDCDeviceApprovalURL(ctx, user.ID, g2.UserCode)
	st, code = idp.authorize(t, u, with(ida, "auth_time", float64(time.Now().Unix())))
	if _, _, err := s.OIDCCallback(ctx, st, code, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OIDCCallback(ctx, st, code, ClientInfo{}); !errors.Is(err, ErrOIDCState) {
		t.Fatalf("state reused: %v", err)
	}
	// Unknown code and local accounts cannot start it.
	if _, err := s.OIDCDeviceApprovalURL(ctx, user.ID, "BCDF-GHJK"); err == nil {
		t.Fatal("unknown user code accepted")
	}
	local := mustUser(t, s, "lok", RoleUser, goodPW)
	if _, err := s.OIDCDeviceApprovalURL(ctx, local.ID, grant().UserCode); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("local account: %v", err)
	}
}
