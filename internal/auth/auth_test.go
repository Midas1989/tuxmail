// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
)

var testArgon = ArgonParams{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 32}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestService(t *testing.T, mod ...func(*config.Config)) (*Service, *clock) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	for _, m := range mod {
		m(&cfg)
	}
	s, err := Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	s.Now = c.Now
	s.PasswordParams = testArgon
	return s, c
}

func mustUser(t *testing.T, s *Service, name string, role Role, pw string) *User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), NewUser{Username: name, Email: name + "@example.de", Role: role, Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

const goodPW = "correct horse battery"

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("geheim-passwort", testArgon)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatal(h)
	}
	ok, rehash, err := VerifyPassword("geheim-passwort", h, testArgon)
	if err != nil || !ok || rehash {
		t.Fatal(ok, rehash, err)
	}
	if ok, _, _ := VerifyPassword("falsch", h, testArgon); ok {
		t.Fatal("wrong password accepted")
	}
	if _, rehash, _ := VerifyPassword("geheim-passwort", h, DefaultArgonParams); !rehash {
		t.Fatal("expected rehash with different params")
	}
	for _, bad := range []string{"", "$2a$10$abc", "$argon2id$v=19$m=1,t=1,p=1$AAAA$AAAA", "$argon2id$v=18$m=64,t=1,p=1$AAAA$AAAA"} {
		if _, _, err := VerifyPassword("x", bad, testArgon); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	h2, _ := HashPassword("geheim-passwort", testArgon)
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestTOTPRFC6238Vectors(t *testing.T) {
	seed20 := []byte("12345678901234567890")
	seed32 := []byte("12345678901234567890123456789012")
	seed64 := []byte("1234567890123456789012345678901234567890123456789012345678901234")
	times := []int64{59, 1111111109, 1111111111, 1234567890, 2000000000, 20000000000}
	want := map[string][]string{
		"SHA1":   {"94287082", "07081804", "14050471", "89005924", "69279037", "65353130"},
		"SHA256": {"46119246", "68084774", "67062674", "91819424", "90698825", "77737706"},
		"SHA512": {"90693936", "25091201", "99943326", "93441116", "38618901", "47863826"},
	}
	algs := map[string]TOTP{
		"SHA1":   {Secret: seed20, Digits: 8, Hash: TOTPSHA1},
		"SHA256": {Secret: seed32, Digits: 8, Hash: TOTPSHA256},
		"SHA512": {Secret: seed64, Digits: 8, Hash: TOTPSHA512},
	}
	for name, totp := range algs {
		for i, ts := range times {
			if got := totp.Code(time.Unix(ts, 0)); got != want[name][i] {
				t.Errorf("%s t=%d: got %s want %s", name, ts, got, want[name][i])
			}
		}
	}
	// 6-digit default and verification window / replay protection.
	tt := TOTP{Secret: seed20}
	at := time.Unix(1111111111, 0)
	code := tt.Code(at)
	if code != "050471" {
		t.Fatal(code)
	}
	step, ok := tt.Verify(code, at.Add(30*time.Second), 1, 0)
	if !ok || step != tt.Step(at) {
		t.Fatal("skew not accepted")
	}
	if _, ok := tt.Verify(code, at.Add(90*time.Second), 1, 0); ok {
		t.Fatal("outside window accepted")
	}
	if _, ok := tt.Verify(code, at, 1, step); ok {
		t.Fatal("replay accepted")
	}
	if u := TOTPURL("TuxMail", "a@b.de", seed20); !strings.HasPrefix(u, "otpauth://totp/TuxMail:a@b.de?") || !strings.Contains(u, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ") {
		t.Fatal(u)
	}
}

func TestMasterKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateMasterKey(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	k2, err := LoadOrCreateMasterKey(p)
	if err != nil || string(k1) != string(k2) {
		t.Fatal("key not stable")
	}
	os.Chmod(p, 0o644)
	if _, err := LoadOrCreateMasterKey(p); err == nil {
		t.Fatal("world-readable key accepted")
	}
}

func TestUserLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	admin := mustUser(t, s, "admin", RoleAdmin, goodPW)
	if _, err := os.Stat(s.UserDir(admin.ID)); err != nil {
		t.Fatal("user dir missing", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "ADMIN"}); !errors.Is(err, ErrExists) {
		t.Fatal("case-insensitive duplicate accepted", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "x", Password: "short"}); !errors.Is(err, ErrWeakPassword) {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "bad name"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Username: "y", Role: "root"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if u, err := s.GetUserByName(ctx, "Admin@Example.de"); err != nil || u.ID != admin.ID {
		t.Fatal("lookup by email", err)
	}
	// Last admin protection.
	if err := s.SetRole(ctx, admin.ID, RoleUser); !errors.Is(err, ErrLastAdmin) {
		t.Fatal(err)
	}
	if err := s.SetDisabled(ctx, admin.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, admin.ID, true); !errors.Is(err, ErrLastAdmin) {
		t.Fatal(err)
	}
	bob := mustUser(t, s, "bob", RoleUser, goodPW)
	name := "Bob B."
	if u, err := s.UpdateUser(ctx, bob.ID, UserUpdate{DisplayName: &name}); err != nil || u.DisplayName != name {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, bob.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, admin.ID, RoleReadonly); err != nil {
		t.Fatal(err)
	}
	pw, err := s.ResetPassword(ctx, bob.ID)
	if err != nil || len(pw) < 20 {
		t.Fatal(pw, err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "bob", Password: pw}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, admin.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.UserDir(admin.ID)); !os.IsNotExist(err) {
		t.Fatal("user dir not removed")
	}
	users, _ := s.ListUsers(ctx)
	if len(users) != 1 {
		t.Fatal(len(users))
	}
	audit, err := s.ListAudit(ctx, 100, 0)
	if err != nil || len(audit) < 5 {
		t.Fatal(len(audit), err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "alice", RoleUser, goodPW)
	if _, err := s.Login(ctx, LoginRequest{Login: "alice", Password: "wrong password!"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	res, err := s.Login(ctx, LoginRequest{Login: "alice", Password: goodPW, Client: ClientInfo{IP: "192.0.2.1", UserAgent: "test"}})
	if err != nil || res.MFARequired || res.Token == "" {
		t.Fatal(res, err)
	}
	// Only the hash is stored.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, []byte(res.Token)).Scan(&n)
	if n != 0 {
		t.Fatal("plain token stored")
	}
	sess, au, err := s.Authenticate(ctx, res.Token, "192.0.2.2")
	if err != nil || au.ID != u.ID || sess.Kind != KindWeb {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, res.Token+"x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	res2, _ := s.Login(ctx, LoginRequest{Login: "alice", Password: goodPW})
	list, _ := s.ListSessions(ctx, u.ID)
	if len(list) != 2 {
		t.Fatal(len(list))
	}
	if err := s.RevokeSession(ctx, u.ID, res2.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, res2.Token, ""); err == nil {
		t.Fatal("revoked session valid")
	}
	// Password change keeps current session, revokes others.
	res3, _ := s.Login(ctx, LoginRequest{Login: "alice", Password: goodPW})
	if err := s.ChangePassword(ctx, u.ID, goodPW, "another good password", res.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, res3.Token, ""); err == nil {
		t.Fatal("other session survived password change")
	}
	if _, _, err := s.Authenticate(ctx, res.Token, ""); err != nil {
		t.Fatal(err)
	}
	// Disable revokes, expiry works.
	clk.Add(s.cfg.Auth.SessionLifetime.D() + time.Second)
	if _, _, err := s.Authenticate(ctx, res.Token, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session valid")
	}
	res4, _ := s.Login(ctx, LoginRequest{Login: "alice", Password: "another good password"})
	mustUser(t, s, "root", RoleAdmin, goodPW)
	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, res4.Token, ""); err == nil {
		t.Fatal("disabled user's session valid")
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "alice", Password: "another good password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestThrottle(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	mustUser(t, s, "carol", RoleUser, goodPW)
	for i := 0; i < throttleFreeFails; i++ {
		if _, err := s.Login(ctx, LoginRequest{Login: "carol", Password: "nope nope nope"}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(i, err)
		}
	}
	_, err := s.Login(ctx, LoginRequest{Login: "carol", Password: goodPW})
	var te *ThrottleError
	if !errors.As(err, &te) || !errors.Is(err, ErrThrottled) {
		t.Fatal("expected throttle", err)
	}
	clk.Add(throttleBaseLock + time.Second)
	if _, err := s.Login(ctx, LoginRequest{Login: "carol", Password: goodPW}); err != nil {
		t.Fatal(err)
	}
	// Success clears the counter.
	if _, err := s.Login(ctx, LoginRequest{Login: "carol", Password: "nope nope nope"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	// Unknown users are throttled too (no user enumeration).
	for i := 0; i < throttleFreeFails; i++ {
		s.Login(ctx, LoginRequest{Login: "ghost", Password: "x"})
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "ghost", Password: "x"}); !errors.Is(err, ErrThrottled) {
		t.Fatal(err)
	}
}

func TestTOTPLoginAndRecovery(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "dave", RoleAdmin, goodPW)
	enr, err := s.BeginTOTP(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b32.DecodeString(enr.Secret)
	totp := TOTP{Secret: secret}
	if _, err := s.ConfirmTOTP(ctx, u.ID, "000000"); !errors.Is(err, ErrInvalidCode) && totp.Code(clk.Now()) != "000000" {
		t.Fatal(err)
	}
	codes, err := s.ConfirmTOTP(ctx, u.ID, totp.Code(clk.Now()))
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatal(codes, err)
	}
	if _, err := s.BeginTOTP(ctx, u.ID); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	// Step 1 returns a challenge.
	res, err := s.Login(ctx, LoginRequest{Login: "dave", Password: goodPW})
	if err != nil || !res.MFARequired || res.Token != "" {
		t.Fatal(res, err)
	}
	// Same code as used for confirmation is a replay.
	if _, err := s.CompleteMFA(ctx, res.Challenge, totp.Code(clk.Now()), "", "", ClientInfo{}); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("replay accepted", err)
	}
	clk.Add(30 * time.Second)
	done, err := s.CompleteMFA(ctx, res.Challenge, totp.Code(clk.Now()), "", "", ClientInfo{})
	if err != nil || done.Token == "" || !done.Session.MFA {
		t.Fatal(err)
	}
	if _, err := s.CompleteMFA(ctx, res.Challenge, totp.Code(clk.Now()), "", "", ClientInfo{}); err == nil {
		t.Fatal("challenge reusable")
	}
	// One-shot login with recovery code; codes are single-use.
	if _, err := s.Login(ctx, LoginRequest{Login: "dave", Password: goodPW, RecoveryCode: strings.ToUpper(codes[0])}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "dave", Password: goodPW, RecoveryCode: codes[0]}); !errors.Is(err, ErrInvalidCode) {
		t.Fatal(err)
	}
	if n, _ := s.RecoveryCodesLeft(ctx, u.ID); n != recoveryCodeCount-1 {
		t.Fatal(n)
	}
	if err := s.ResetTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	res, err = s.Login(ctx, LoginRequest{Login: "dave", Password: goodPW})
	if err != nil || res.MFARequired {
		t.Fatal(err)
	}
	s.cfg.Auth.TOTPRequiredForAdmins = true
	if !s.MFASetupRequired(res.User, res.Session) {
		t.Fatal("admin without TOTP must enrol")
	}
}

func TestInvites(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	_, tok, err := s.CreateInvite(ctx, "new@example.de", RoleUser, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, tok, NewUser{Username: "new", Email: "new@example.de", Password: goodPW}); !errors.Is(err, ErrRegistration) {
		t.Fatal(err)
	}
	s.cfg.Auth.Registration = "invite"
	if _, err := s.Register(ctx, tok, NewUser{Username: "new", Email: "other@example.de", Password: goodPW}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	u, err := s.Register(ctx, tok, NewUser{Username: "new", Email: "new@example.de", Password: goodPW})
	if err != nil || u.Role != RoleUser {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, tok, NewUser{Username: "new2", Password: goodPW}); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("invite reused", err)
	}
	_, tok2, _ := s.CreateInvite(ctx, "", RoleReadonly, time.Hour)
	clk.Add(2 * time.Hour)
	if _, err := s.LookupInvite(ctx, tok2); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("expired invite usable")
	}
	list, _ := s.ListInvites(ctx)
	if len(list) != 2 || list[1].UsedBy != u.ID {
		t.Fatal(list)
	}
}

func TestUserSecrets(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	u := mustUser(t, s, "erin", RoleUser, "")
	st, err := s.UserSecrets(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("account:1", []byte("imap-pass")); err != nil {
		t.Fatal(err)
	}
	s.dropVault(u.ID)
	st2, err := s.UserSecrets(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := st2.Get("account:1"); err != nil || string(v) != "imap-pass" {
		t.Fatal(v, err)
	}
	data, _ := os.ReadFile(filepath.Join(s.UserDir(u.ID), "secrets.vault"))
	if strings.Contains(string(data), "imap-pass") {
		t.Fatal("secret stored in plain text")
	}
	// A different user's DEK cannot open the vault.
	v := mustUser(t, s, "frank", RoleUser, "")
	if _, err := s.UserSecrets(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsTemplatesSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	u := mustUser(t, s, "gina", RoleUser, "")
	g, err := s.CreateGroup(ctx, "sales", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, 999); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	other, _ := s.CreateGroup(ctx, "it", "")
	body := `{"accounts":[{"kind":"imap","displayName":"Firma %DISPLAYNAME%","incoming":{"host":"imap.%DOMAIN%","username":"%USERNAME%"}}]}`
	if _, err := s.CreateTemplate(ctx, Template{Name: "global", Body: json.RawMessage(body)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, Template{Name: "sales", GroupID: g.ID, Body: json.RawMessage(`{"x":"%EMAIL%"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, Template{Name: "it", GroupID: other.ID, Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, Template{Name: "bad", Body: json.RawMessage(`{`)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	dn := `Gina "Quote"`
	u, _ = s.UpdateUser(ctx, u.ID, UserUpdate{DisplayName: &dn})
	rt, err := s.TemplatesForUser(ctx, u)
	if err != nil || len(rt) != 2 {
		t.Fatal(rt, err)
	}
	var got struct {
		Accounts []struct {
			DisplayName string
			Incoming    struct{ Host, Username string }
		}
	}
	if err := json.Unmarshal(rt[0].Body, &got); err != nil {
		t.Fatal(err, string(rt[0].Body))
	}
	if got.Accounts[0].DisplayName != `Firma Gina "Quote"` || got.Accounts[0].Incoming.Host != "imap.example.de" || got.Accounts[0].Incoming.Username != "gina" {
		t.Fatal(got)
	}
	if string(rt[1].Body) != `{"x":"gina@example.de"}` {
		t.Fatal(string(rt[1].Body))
	}
	if err := s.SetSetting(ctx, u.ID, "ui", json.RawMessage(`{"theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, u.ID, "UI!", json.RawMessage(`{}`)); err == nil {
		t.Fatal("bad key accepted")
	}
	all, _ := s.Settings(ctx, u.ID)
	if string(all["ui"]) != `{"theme":"dark"}` {
		t.Fatal(all)
	}
}

func TestDeviceFlow(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "henry", RoleUser, goodPW)
	pub := make([]byte, 32)
	g, err := s.StartDeviceAuthorization(ctx, ClientInfo{DeviceName: "Laptop", PubKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.UserCode) != 9 || g.UserCode[4] != '-' {
		t.Fatal(g.UserCode)
	}
	if _, err := s.PollDeviceToken(ctx, g.DeviceCode); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatal(err)
	}
	if _, err := s.PollDeviceToken(ctx, g.DeviceCode); !errors.Is(err, ErrSlowDown) {
		t.Fatal(err)
	}
	req, err := s.LookupDeviceRequest(ctx, strings.ToLower(g.UserCode))
	if err != nil || req.ClientName != "Laptop" {
		t.Fatal(err)
	}
	if err := s.ApproveDevice(ctx, u.ID, g.UserCode, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveDevice(ctx, u.ID, g.UserCode, true); !errors.Is(err, ErrInvalidCode) {
		t.Fatal("double approve")
	}
	clk.Add(20 * time.Second)
	res, err := s.PollDeviceToken(ctx, g.DeviceCode)
	if err != nil || res.Session.Kind != KindDevice || len(res.Session.ClientPubKey) != 32 {
		t.Fatal(err)
	}
	if res.Session.ExpiresAt.Sub(clk.Now()) != DeviceTokenLifetime {
		t.Fatal("device token lifetime", res.Session.ExpiresAt)
	}
	if _, err := s.PollDeviceToken(ctx, g.DeviceCode); !errors.Is(err, ErrExpiredToken) {
		t.Fatal("token issued twice", err)
	}
	// Denied and expired grants.
	g2, _ := s.StartDeviceAuthorization(ctx, ClientInfo{})
	s.ApproveDevice(ctx, u.ID, g2.UserCode, false)
	if _, err := s.PollDeviceToken(ctx, g2.DeviceCode); !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	g3, _ := s.StartDeviceAuthorization(ctx, ClientInfo{})
	clk.Add(DeviceCodeLifetime)
	if _, err := s.PollDeviceToken(ctx, g3.DeviceCode); !errors.Is(err, ErrExpiredToken) {
		t.Fatal(err)
	}
	if _, err := s.StartDeviceAuthorization(ctx, ClientInfo{PubKey: []byte{1}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}

func TestSafeReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"/mail": "/mail", "": "/", "//evil.com": "/", "https://evil.com": "/", "/\\evil.com": "/", "mail": "/",
	} {
		if got := SafeReturnPath(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
