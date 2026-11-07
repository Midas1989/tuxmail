// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth/passkeytest"
	"github.com/Midas1989/Mailclient/internal/config"
)

const (
	pkOrigin = "https://mail.example.org"
	pkRPID   = "mail.example.org"
)

func newPasskeyService(t *testing.T, mod ...func(*config.Config)) (*Service, *clock) {
	t.Helper()
	return newTestService(t, append([]func(*config.Config){func(c *config.Config) { c.PublicURL = pkOrigin }}, mod...)...)
}

// registerPasskey runs a complete registration ceremony for u.
func registerPasskey(t *testing.T, s *Service, u *User, name string) (*passkeytest.Authenticator, *Passkey) {
	t.Helper()
	a := passkeytest.New(pkOrigin, pkRPID)
	pk, err := tryRegister(s, u, name, a)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, pk
}

func tryRegister(s *Service, u *User, name string, a *passkeytest.Authenticator) (*Passkey, error) {
	ctx := context.Background()
	id, creation, err := s.BeginPasskeyRegistration(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	js, _ := json.Marshal(creation)
	return s.FinishPasskeyRegistration(ctx, u.ID, id, name, a.Create(js))
}

// loginPasskey runs a login ceremony with authenticator a from ip.
func loginPasskey(s *Service, a *passkeytest.Authenticator, ip string) (*LoginResult, error) {
	ctx := context.Background()
	ci := ClientInfo{IP: ip, UserAgent: "test"}
	id, assertion, err := s.BeginPasskeyLogin(ctx, ci)
	if err != nil {
		return nil, err
	}
	js, _ := json.Marshal(assertion)
	return s.FinishPasskeyLogin(ctx, id, a.Get(js), KindWeb, ci)
}

func auditHas(t *testing.T, s *Service, action, detail string, success bool) bool {
	t.Helper()
	entries, err := s.ListAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action && e.Success == success && strings.Contains(e.Details, detail) {
			return true
		}
	}
	return false
}

func TestPasskeyRegisterListLogin(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u := mustUser(t, s, "anna", RoleUser, goodPW)
	a, pk := registerPasskey(t, s, u, "  Anna's laptop ")
	if pk.Name != "Anna's laptop" || !pk.Usable || !pk.BackupEligible || pk.ID == 0 || !pk.LastUsedAt.IsZero() {
		t.Fatalf("registered passkey: %+v", pk)
	}
	if len(pk.Transports) != 2 {
		t.Fatalf("transports: %v", pk.Transports)
	}
	list, err := s.ListPasskeys(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].ID != pk.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.Passkeys != 1 {
		t.Fatalf("user.Passkeys = %d", got.Passkeys)
	}
	if !auditHas(t, s, "passkey.register", "Anna's laptop", true) {
		t.Fatal("registration not audited")
	}

	clk.Add(time.Hour)
	res, err := loginPasskey(s, a, "192.0.2.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.Token == "" || res.User.ID != u.ID || res.Session.LoginMethod != MethodPasskey || !res.Session.MFA || res.Session.Kind != KindWeb {
		t.Fatalf("session: %+v", res.Session)
	}
	if _, u2, err := s.Authenticate(ctx, res.Token, "192.0.2.1"); err != nil || u2.ID != u.ID {
		t.Fatalf("session does not authenticate: %v", err)
	}
	list, _ = s.ListPasskeys(ctx, u.ID)
	if !list[0].LastUsedAt.Equal(clk.Now()) {
		t.Fatalf("last used not updated: %+v", list[0])
	}
	var count int64
	s.db.QueryRow(`SELECT sign_count FROM passkeys WHERE id = ?`, pk.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("sign count = %d", count)
	}
	if !auditHas(t, s, "login", "method=passkey mfa=true", true) {
		t.Fatal("login not audited")
	}
	// A second login works too (counter 2) ...
	if _, err := loginPasskey(s, a, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	// ... and the credential id / public key never appear in the API view.
	js, _ := json.Marshal(list)
	if strings.Contains(strings.ToLower(string(js)), "credential") || strings.Contains(string(js), "publicKey") {
		t.Fatalf("view leaks credential data: %s", js)
	}
}

func TestPasskeyLoginBinding(t *testing.T) {
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "bob", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "k")

	for name, mod := range map[string]func(){
		"wrong origin (phishing site)": func() { a.Origin = "https://mail.example.org.evil.test" },
		"wrong origin scheme":          func() { a.Origin = "http://mail.example.org" },
		"wrong rp id":                  func() { a.RPID = "example.org" },
		"user verification missing":    func() { a.UV = false },
	} {
		orig, origRP, origUV := a.Origin, a.RPID, a.UV
		mod()
		if _, err := loginPasskey(s, a, "192.0.2.2"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
		a.Origin, a.RPID, a.UV = orig, origRP, origUV
	}
	// The genuine authenticator still works (counter state was not advanced by the failures).
	if _, err := loginPasskey(s, a, "192.0.2.9"); err != nil {
		t.Fatalf("genuine login after failures: %v", err)
	}
}

func TestPasskeyChallengeSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u := mustUser(t, s, "cara", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "k")
	ci := ClientInfo{IP: "192.0.2.3"}

	begin := func() (string, string) {
		id, assertion, err := s.BeginPasskeyLogin(ctx, ci)
		if err != nil {
			t.Fatal(err)
		}
		js, _ := json.Marshal(assertion)
		return id, string(js)
	}
	// Replay of an accepted response.
	id, opts := begin()
	resp := a.Get([]byte(opts))
	if _, err := s.FinishPasskeyLogin(ctx, id, resp, KindWeb, ci); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishPasskeyLogin(ctx, id, resp, KindWeb, ci); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replayed response accepted: %v", err)
	}
	// A failed attempt burns the challenge: the right answer afterwards is too late.
	id, opts = begin()
	if _, err := s.FinishPasskeyLogin(ctx, id, []byte(`{"id":"x"}`), KindWeb, ci); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := s.FinishPasskeyLogin(ctx, id, a.Get([]byte(opts)), KindWeb, ci); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("burnt challenge accepted: %v", err)
	}
	// A response to one ceremony does not complete another one.
	id1, _ := begin()
	_, opts2 := begin()
	if _, err := s.FinishPasskeyLogin(ctx, id1, a.Get([]byte(opts2)), KindWeb, ci); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("response for another ceremony accepted: %v", err)
	}
	// Expiry after 5 minutes.
	id, opts = begin()
	clk.Add(passkeyChallengeTTL + time.Second)
	if _, err := s.FinishPasskeyLogin(ctx, id, a.Get([]byte(opts)), KindWeb, ci); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired challenge accepted: %v", err)
	}
	// Unknown / empty ids.
	clk.Add(time.Hour) // the failures above locked ci's network
	for _, bad := range []string{"", "nonsense", strings.Repeat("x", 500)} {
		if _, err := s.FinishPasskeyLogin(ctx, bad, a.Get([]byte(opts)), KindWeb, ClientInfo{IP: "192.0.2.99"}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("id %q: %v", bad, err)
		}
	}
	// Cleanup removes what expired.
	begin()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM passkey_challenges`).Scan(&n)
	if n == 0 {
		t.Fatal("no challenges stored")
	}
	clk.Add(time.Hour)
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM passkey_challenges`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d challenges left after cleanup", n)
	}
}

func TestPasskeyCounterRegression(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "dan", RoleUser, goodPW)
	a, pk := registerPasskey(t, s, u, "k")
	ci := ClientInfo{IP: "192.0.2.4"}
	login := func(count uint32) error {
		id, assertion, err := s.BeginPasskeyLogin(ctx, ci)
		if err != nil {
			t.Fatal(err)
		}
		js, _ := json.Marshal(assertion)
		_, err = s.FinishPasskeyLogin(ctx, id, a.GetWithCounter(passkeytest.ParseChallenge(js), count), KindWeb, ci)
		return err
	}
	if err := login(5); err != nil {
		t.Fatal(err)
	}
	for _, c := range []uint32{5, 4, 1} { // equal and lower counters: a clone, or a replay
		if err := login(c); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("counter %d after 5 accepted: %v", c, err)
		}
	}
	if !auditHas(t, s, "login", "signature counter did not increase", false) {
		t.Fatal("regression not audited")
	}
	var stored int64
	s.db.QueryRow(`SELECT sign_count FROM passkeys WHERE id = ?`, pk.ID).Scan(&stored)
	if stored != 5 {
		t.Fatalf("counter changed by a rejected assertion: %d", stored)
	}
	if err := login(6); err != nil {
		t.Fatalf("a higher counter is accepted again: %v", err)
	}
}

func TestPasskeySyncedZeroCounter(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "eva", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "synced")
	a.CountStep, a.SignCount = 0, 0 // iCloud/Google style: always 0
	for i := range 3 {
		if _, err := loginPasskey(s, a, "192.0.2.5"); err != nil {
			t.Fatalf("login %d with counter 0: %v", i, err)
		}
	}
	_ = ctx
}

func TestPasskeyDisabledUserIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "finn", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "k")
	if err := s.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	_, errDisabled := loginPasskey(s, a, "192.0.2.6")
	b := passkeytest.New(pkOrigin, pkRPID) // never registered
	b.UserHandle = a.UserHandle
	_, errUnknown := loginPasskey(s, b, "192.0.2.6")
	if !errors.Is(errDisabled, ErrInvalidCredentials) || errDisabled.Error() != errUnknown.Error() || !errors.Is(errUnknown, ErrInvalidCredentials) {
		t.Fatalf("disabled: %v / unknown: %v", errDisabled, errUnknown)
	}
	if !auditHas(t, s, "login", "passkey: disabled", false) {
		t.Fatal("reason missing in the audit log")
	}
	var sessions int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Fatal("a session was created for a disabled user")
	}
	var count int64
	s.db.QueryRow(`SELECT sign_count FROM passkeys`).Scan(&count)
	if count != 0 {
		t.Fatal("counter advanced for a disabled user")
	}
	s.SetDisabled(ctx, u.ID, false)
	if _, err := loginPasskey(s, a, "192.0.2.7"); err != nil {
		t.Fatalf("re-enabled user: %v", err)
	}
}

func TestPasskeyUnknownCredentialAndHandleMismatch(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "gina", RoleUser, goodPW)
	v := mustUser(t, s, "hans", RoleUser, goodPW)
	a, pk := registerPasskey(t, s, u, "k")
	b, _ := registerPasskey(t, s, v, "k")

	// Credential of gina with the user handle of hans.
	a.UserHandle = b.UserHandle
	if _, err := loginPasskey(s, a, "192.0.2.8"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("foreign user handle accepted: %v", err)
	}
	a.UserHandle = nil
	if _, err := loginPasskey(s, a, "192.0.2.8"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("missing user handle accepted: %v", err)
	}
	// A deleted passkey stops working immediately.
	a2 := passkeytest.New(pkOrigin, pkRPID)
	pk2, err := tryRegister(s, u, "second", a2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loginPasskey(s, a2, "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePasskey(ctx, u.ID, pk2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loginPasskey(s, a2, "192.0.2.21"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("deleted passkey accepted: %v", err)
	}
	// Another user cannot delete or rename it.
	if err := s.DeletePasskey(ctx, v.ID, pk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if err := s.RenamePasskey(ctx, v.ID, pk.ID, "mine now"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign rename: %v", err)
	}
	if err := s.DeletePasskey(ctx, u.ID, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown delete: %v", err)
	}
	if !auditHas(t, s, "passkey.delete", "second", true) {
		t.Fatal("delete not audited")
	}
}

func TestPasskeyStaleRelyingParty(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "ida", RoleUser, goodPW)
	a, pk := registerPasskey(t, s, u, "old domain")
	s.db.Exec(`UPDATE passkeys SET rp_id = 'old.example.org'`) // public_url's host changed
	list, _ := s.ListPasskeys(ctx, u.ID)
	if list[0].Usable {
		t.Fatal("passkey of another RP ID reported usable")
	}
	if _, err := loginPasskey(s, a, "192.0.2.30"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale passkey accepted: %v", err)
	}
	if err := s.DeletePasskey(ctx, u.ID, pk.ID); err != nil {
		t.Fatalf("stale passkey cannot be deleted: %v", err)
	}
}

func TestPasskeyLoginThrottle(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u := mustUser(t, s, "jan", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "k")
	bad := passkeytest.New(pkOrigin, pkRPID) // not registered
	for i := range throttleFreeFails {
		if _, err := loginPasskey(s, bad, "198.51.100.1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var te *ThrottleError
	if _, _, err := s.BeginPasskeyLogin(ctx, ClientInfo{IP: "198.51.100.1"}); !errors.As(err, &te) {
		t.Fatalf("network not locked after %d failures: %v", throttleFreeFails, err)
	}
	// Even the owner's authenticator is refused from the locked network (fail closed) ...
	if _, err := loginPasskey(s, a, "198.51.100.1"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("locked network accepted a login: %v", err)
	}
	// ... but not from another one, and password login of the same user is untouched.
	if _, err := loginPasskey(s, a, "198.51.100.2"); err != nil {
		t.Fatalf("other network affected: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "jan", Password: goodPW, Client: ClientInfo{IP: "198.51.100.1"}}); err != nil {
		t.Fatalf("password login affected by passkey failures: %v", err)
	}
	var key string
	s.db.QueryRow(`SELECT key FROM login_throttle WHERE key LIKE 'p:%'`).Scan(&key)
	if key != "p:198.51.100.1" || strings.Contains(key, "jan") {
		t.Fatalf("throttle key %q", key)
	}
	clk.Add(throttleBaseLock + time.Second)
	if _, err := loginPasskey(s, a, "198.51.100.1"); err != nil {
		t.Fatalf("lock does not end: %v", err)
	}
}

func TestPasskeyLimitAndNames(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "kai", RoleUser, goodPW)
	var first *Passkey
	for i := range MaxPasskeysPerUser {
		_, pk := registerPasskey(t, s, u, "")
		if pk.Name != "Passkey" {
			t.Fatalf("default name: %q", pk.Name)
		}
		if i == 0 {
			first = pk
		}
	}
	if _, _, err := s.BeginPasskeyRegistration(ctx, u.ID); !errors.Is(err, ErrPasskeyLimit) {
		t.Fatalf("11th passkey: %v", err)
	}
	other := mustUser(t, s, "lea", RoleUser, goodPW)
	registerPasskey(t, s, other, "mine") // limit is per user
	if err := s.DeletePasskey(ctx, u.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	registerPasskey(t, s, u, "again")

	// Rename.
	if err := s.RenamePasskey(ctx, u.ID, first.ID+1, "  Work phone "); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListPasskeys(ctx, u.ID)
	found := false
	for _, p := range list {
		found = found || p.Name == "Work phone"
	}
	if !found {
		t.Fatalf("rename lost: %+v", list)
	}
	for _, bad := range []string{strings.Repeat("x", 65), "a\nb", "tab\there", "\x00"} {
		if err := s.RenamePasskey(ctx, u.ID, first.ID+1, bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if err := s.RenamePasskey(ctx, u.ID, first.ID+1, strings.Repeat("ä", 64)); err != nil {
		t.Errorf("64 runes: %v", err)
	}
}

func TestPasskeyRegistrationChecks(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "max", RoleUser, goodPW)
	v := mustUser(t, s, "nora", RoleUser, goodPW)

	begin := func(user *User) (string, []byte) {
		id, creation, err := s.BeginPasskeyRegistration(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		js, _ := json.Marshal(creation)
		return id, js
	}
	// Properties of the options: UV, resident key, no attestation, exclusions, no PII in user.id.
	id, opts := begin(u)
	var o struct {
		PublicKey struct {
			RP                     struct{ ID, Name string } `json:"rp"`
			User                   struct{ ID, Name string } `json:"user"`
			Attestation            string                    `json:"attestation"`
			Timeout                int                       `json:"timeout"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				RequireResident  bool   `json:"requireResidentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &o); err != nil {
		t.Fatal(err)
	}
	as := o.PublicKey.AuthenticatorSelection
	if as.ResidentKey != "required" || !as.RequireResident || as.UserVerification != "required" || o.PublicKey.Attestation != "none" {
		t.Fatalf("options: %s", opts)
	}
	if o.PublicKey.RP.ID != pkRPID || o.PublicKey.RP.Name != "TuxMail" || o.PublicKey.User.Name != "max" || o.PublicKey.Timeout != 300000 {
		t.Fatalf("options: %s", opts)
	}
	if strings.Contains(o.PublicKey.User.ID, "max") || len(o.PublicKey.User.ID) < 40 { // 32 random bytes
		t.Fatalf("user handle: %q", o.PublicKey.User.ID)
	}

	a := passkeytest.New(pkOrigin, pkRPID)
	resp := a.Create(opts)
	// Another user cannot finish max's ceremony.
	if _, err := s.FinishPasskeyRegistration(ctx, v.ID, id, "x", resp); !errors.Is(err, ErrPasskeyChallenge) {
		t.Fatalf("foreign ceremony: %v", err)
	}
	// ... and the failed attempt did not consume it for the owner.
	if _, err := s.FinishPasskeyRegistration(ctx, u.ID, id, "x", resp); err != nil {
		t.Fatalf("owner: %v", err)
	}
	// Single use.
	if _, err := s.FinishPasskeyRegistration(ctx, u.ID, id, "x", resp); !errors.Is(err, ErrPasskeyChallenge) {
		t.Fatalf("second use: %v", err)
	}
	// Registering the same authenticator again is refused (excluded / unique).
	if _, err := tryRegister(s, u, "dup", a); err == nil {
		t.Fatal("duplicate credential accepted")
	}
	// Bad name does not burn the ceremony.
	id, opts = begin(v)
	b := passkeytest.New(pkOrigin, pkRPID)
	if _, err := s.FinishPasskeyRegistration(ctx, v.ID, id, strings.Repeat("x", 100), b.Create(opts)); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, err := s.FinishPasskeyRegistration(ctx, v.ID, id, "ok", b.Create(opts)); err != nil {
		t.Fatalf("ceremony burnt by a bad name: %v", err)
	}
	// Wrong origin, missing user verification, garbage.
	for name, mod := range map[string]func(*passkeytest.Authenticator){
		"wrong origin": func(a *passkeytest.Authenticator) { a.Origin = "https://evil.test" },
		"wrong rp id":  func(a *passkeytest.Authenticator) { a.RPID = "evil.test" },
		"no UV":        func(a *passkeytest.Authenticator) { a.UV = false },
	} {
		c := passkeytest.New(pkOrigin, pkRPID)
		mod(c)
		if _, err := tryRegister(s, u, name, c); !errors.Is(err, ErrPasskeyInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	id, _ = begin(u)
	if _, err := s.FinishPasskeyRegistration(ctx, u.ID, id, "x", []byte(`{"id":"AAAA"}`)); !errors.Is(err, ErrPasskeyInvalid) {
		t.Errorf("garbage: %v", err)
	}
	// Expired ceremony.
	s2, clk := newPasskeyService(t)
	w := mustUser(t, s2, "otto", RoleUser, goodPW)
	id, creation, _ := s2.BeginPasskeyRegistration(ctx, w.ID)
	js, _ := json.Marshal(creation)
	clk.Add(passkeyChallengeTTL + time.Second)
	if _, err := s2.FinishPasskeyRegistration(ctx, w.ID, id, "x", passkeytest.New(pkOrigin, pkRPID).Create(js)); !errors.Is(err, ErrPasskeyChallenge) {
		t.Errorf("expired: %v", err)
	}
}

func TestPasskeysDisabledByConfig(t *testing.T) {
	ctx := context.Background()
	for name, mod := range map[string]func(*config.Config){
		"explicitly off": func(c *config.Config) { f := false; c.Auth.Passkeys.Enabled = &f },
		"plain http":     func(c *config.Config) { c.PublicURL = "http://mail.example.org" },
	} {
		s, _ := newPasskeyService(t, mod)
		u := mustUser(t, s, "pia", RoleUser, goodPW)
		if s.PasskeysEnabled() {
			t.Errorf("%s: enabled", name)
		}
		if _, _, err := s.BeginPasskeyRegistration(ctx, u.ID); !errors.Is(err, ErrPasskeysDisabled) {
			t.Errorf("%s: register: %v", name, err)
		}
		if _, _, err := s.BeginPasskeyLogin(ctx, ClientInfo{IP: "192.0.2.1"}); !errors.Is(err, ErrPasskeysDisabled) {
			t.Errorf("%s: login: %v", name, err)
		}
		if _, err := s.FinishPasskeyLogin(ctx, "x", []byte("{}"), KindWeb, ClientInfo{}); !errors.Is(err, ErrPasskeysDisabled) {
			t.Errorf("%s: finish: %v", name, err)
		}
	}
}

func TestPasskeyAdminResetAndUserDeletion(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u := mustUser(t, s, "quin", RoleUser, goodPW)
	other := mustUser(t, s, "rosa", RoleUser, goodPW)
	a, _ := registerPasskey(t, s, u, "k1")
	registerPasskey(t, s, u, "k2")
	registerPasskey(t, s, other, "r")
	enableTOTP(t, s, clk, u.ID)

	// The user's own "disable TOTP" keeps passkeys; the administrator's reset removes everything.
	if err := s.ResetTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListPasskeys(ctx, u.ID); len(l) != 2 {
		t.Fatalf("ResetTOTP touched passkeys: %d", len(l))
	}
	enableTOTP(t, s, clk, u.ID)
	if err := s.ResetSecondFactors(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListPasskeys(ctx, u.ID); len(l) != 0 {
		t.Fatalf("passkeys left after the reset: %d", len(l))
	}
	if got, _ := s.GetUser(ctx, u.ID); got.TOTPEnabled || got.Passkeys != 0 {
		t.Fatalf("user after reset: %+v", got)
	}
	if l, _ := s.ListPasskeys(ctx, other.ID); len(l) != 1 {
		t.Fatal("reset touched another user")
	}
	if _, err := loginPasskey(s, a, "192.0.2.40"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login with a removed passkey: %v", err)
	}
	if !auditHas(t, s, "totp.reset", "passkeys_removed=2", true) {
		t.Fatal("reset not audited with the number of passkeys")
	}
	if err := s.ResetSecondFactors(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	// Deleting the user removes passkeys, handle and pending ceremonies.
	s.BeginPasskeyRegistration(ctx, other.ID)
	if err := s.DeleteUser(ctx, other.ID, true); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM passkeys) + (SELECT COUNT(*) FROM passkey_users WHERE user_id = ?) +
		(SELECT COUNT(*) FROM passkey_challenges WHERE kind = 'register')`, other.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after deleting the user", n)
	}
}

// A passkey session carries its own second factor; a password session of the
// same admin does not (auth.totp_required_for_admins).
func TestPasskeySessionSatisfiesMFAButPasswordSessionDoesNot(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	admin := mustUser(t, s, "admin", RoleAdmin, goodPW)
	user := mustUser(t, s, "plain", RoleUser, goodPW)

	pw, err := s.Login(ctx, LoginRequest{Login: "admin", Password: goodPW, Client: ClientInfo{IP: "192.0.2.50"}})
	if err != nil {
		t.Fatal(err)
	}
	if !s.MFASetupRequired(pw.User, pw.Session) {
		t.Fatal("password session of an admin without TOTP must still owe the TOTP enrolment")
	}
	// A stolen admin password must not be able to add a passkey and use it to skip the TOTP rule.
	if err := s.VerifyPasskeyStepUp(ctx, admin.ID, pw.Session.ID, goodPW, "", ""); !errors.Is(err, ErrMFASetupRequired) {
		t.Fatalf("passkey registration allowed in a session that owes the TOTP enrolment: %v", err)
	}
	// An admin who completed the enrolment may add one.
	s.db.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = ?`, admin.ID)
	a, _ := registerPasskey(t, s, admin, "yubikey")
	s.db.Exec(`UPDATE users SET totp_enabled = 0 WHERE id = ?`, admin.ID) // ... and (hypothetically) lost TOTP again

	pk, err := loginPasskey(s, a, "192.0.2.51")
	if err != nil {
		t.Fatal(err)
	}
	if s.MFASetupRequired(pk.User, pk.Session) {
		t.Fatal("passkey session of an admin must satisfy the MFA requirement")
	}
	// The password session of the very same account (passkey registered) still owes it.
	pw2, _ := s.Login(ctx, LoginRequest{Login: "admin", Password: goodPW, Client: ClientInfo{IP: "192.0.2.50"}})
	if !s.MFASetupRequired(pw2.User, pw2.Session) {
		t.Fatal("a registered passkey must not exempt password sessions")
	}
	if s.MFASetupRequired(pk.User, nil) != true {
		t.Fatal("a nil session is never exempt")
	}
	// Non-admins are unaffected either way.
	if s.MFASetupRequired(user, &Session{LoginMethod: MethodPassword}) {
		t.Fatal("non-admin")
	}
}

func TestPasskeyStepUp(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u := mustUser(t, s, "sam", RoleUser, goodPW)
	pw, _ := s.Login(ctx, LoginRequest{Login: "sam", Password: goodPW, Client: ClientInfo{IP: "192.0.2.60"}})

	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, "wrong password!", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, "", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("no password: %v", err)
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, goodPW, "", ""); err != nil {
		t.Fatalf("correct password: %v", err)
	}
	// Wrong answers count against the persistent per-user step-up lockout.
	for range stepUpFreeFails {
		s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, "wrong password!", "", "")
	}
	var te *ThrottleError
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, goodPW, "", ""); !errors.As(err, &te) {
		t.Fatalf("step-up not locked: %v", err)
	}
	s.ClearStepUp(ctx, u.ID)

	// With TOTP the second factor is required as well.
	tp := enableTOTP(t, s, clk, u.ID)
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, goodPW, "", ""); err == nil {
		t.Fatal("TOTP not required")
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, pw.Session.ID, goodPW, tp.Code(clk.Now()), ""); err != nil {
		t.Fatalf("password + TOTP: %v", err)
	}

	// A foreign session id is not accepted.
	v := mustUser(t, s, "tom", RoleUser, goodPW)
	if err := s.VerifyPasskeyStepUp(ctx, v.ID, pw.Session.ID, goodPW, "", ""); err == nil {
		t.Fatal("session of another user accepted")
	}
}

// Accounts without a local password (OIDC-provisioned) re-authenticate with a
// fresh identity-provider or passkey login of the calling session.
func TestPasskeyStepUpWithoutPassword(t *testing.T) {
	ctx := context.Background()
	s, clk := newPasskeyService(t)
	u, err := s.CreateUser(ctx, NewUser{Username: "oidcuser", Email: "o@example.de", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(method string) *Session {
		_, sess, err := s.createSession(ctx, u.ID, KindWeb, method, false, ClientInfo{IP: "192.0.2.70"})
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	fresh := mk(MethodOIDC)
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, fresh.ID, "", "", ""); err != nil {
		t.Fatalf("fresh OIDC session: %v", err)
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, mk(MethodPasskey).ID, "", "", ""); err != nil {
		t.Fatalf("fresh passkey session: %v", err)
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, mk(MethodDeviceCode).ID, "", "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("device-code session: %v", err)
	}
	clk.Add(FirstPasswordReauthWindow + time.Second)
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, fresh.ID, "", "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("stale OIDC session: %v", err)
	}
	// With TOTP a code replaces the freshness requirement.
	tp := enableTOTP(t, s, clk, u.ID)
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, fresh.ID, "", "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("missing code: %v", err)
	}
	if err := s.VerifyPasskeyStepUp(ctx, u.ID, fresh.ID, "", tp.Code(clk.Now()), ""); err != nil {
		t.Fatalf("TOTP: %v", err)
	}
	// OIDC users keep their link when they log in with a passkey.
	if _, _, err := s.createSession(ctx, u.ID, KindWeb, MethodPasskey, true, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeyLoginChallengeTableIsBounded(t *testing.T) {
	ctx := context.Background()
	s, _ := newPasskeyService(t)
	// Fill the table beyond the cap directly, then one more begin must evict the oldest ceremonies.
	s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO passkey_challenges(id_hash, kind, user_id, data, expires_at) SELECT randomblob(32), 'login', NULL, x'7b7d', 2000000000 + i FROM n`,
		maxPendingLoginChallenges)
	if _, _, err := s.BeginPasskeyLogin(ctx, ClientInfo{IP: "192.0.2.80"}); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM passkey_challenges`).Scan(&n)
	if n > maxPendingLoginChallenges {
		t.Fatalf("%d pending ceremonies", n)
	}
}

// Basis spec/PARAMETER.md: EdDSA, ES256, RS256 (RSA >= 2048) only.
func TestPasskeyAlgorithms(t *testing.T) {
	s, _ := newPasskeyService(t)
	u := mustUser(t, s, "algo", RoleUser, goodPW)
	_, creation, err := s.BeginPasskeyRegistration(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := creation.Response.Parameters; len(got) != 3 {
		t.Fatalf("offered algorithms: %+v", got)
	}
	cbor := func(major byte, n int) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 256:
			return []byte{major<<5 | 24, byte(n)}
		default:
			return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
		}
	}
	bstr := func(n int) []byte { return append(cbor(2, n), bytes.Repeat([]byte{0xab}, n)...) }
	neg := func(v int) []byte { return cbor(1, -1-v) }
	rsa := func(bits int) []byte {
		out := []byte{0xa4, 0x01, 0x03, 0x03}
		out = append(out, neg(-257)...)
		out = append(out, neg(-1)...)
		out = append(out, bstr(bits/8)...)
		out = append(out, neg(-2)...)
		return append(out, cbor(2, 3)[0], 1, 0, 1)
	}
	if err := checkPasskeyKey(rsa(2048)); err != nil {
		t.Errorf("RSA 2048: %v", err)
	}
	if err := checkPasskeyKey(rsa(1024)); err == nil {
		t.Error("RSA 1024 accepted")
	}
	// 256 bytes, but only 2041 bits: the length of the number counts.
	short := []byte{0xa4, 0x01, 0x03, 0x03}
	short = append(short, neg(-257)...)
	short = append(short, neg(-1)...)
	short = append(short, cbor(2, 256)...)
	short = append(short, 0x01)
	short = append(short, bytes.Repeat([]byte{0xab}, 255)...)
	short = append(short, neg(-2)...)
	short = append(short, cbor(2, 3)[0], 1, 0, 1)
	if err := checkPasskeyKey(short); err == nil {
		t.Error("2041-bit RSA key accepted")
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdhKey, err := ecKey.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	point := ecdhKey.Bytes() // 0x04 || X || Y
	es := func(alg int) []byte {
		out := []byte{0xa5, 0x01, 0x02, 0x03}
		out = append(out, neg(alg)...)
		out = append(out, neg(-1)...)
		out = append(out, 0x01)
		out = append(out, neg(-2)...)
		out = append(append(out, cbor(2, 32)...), point[1:33]...)
		out = append(out, neg(-3)...)
		return append(append(out, cbor(2, 32)...), point[33:]...)
	}
	if err := checkPasskeyKey(es(-7)); err != nil {
		t.Errorf("ES256: %v", err)
	}
	if err := checkPasskeyKey(es(-35)); err == nil {
		t.Error("ES384 accepted")
	}
	if err := checkPasskeyKey([]byte{0x01}); err == nil {
		t.Error("garbage accepted")
	}
}
