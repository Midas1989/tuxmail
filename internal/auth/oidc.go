// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Midas1989/Mailclient/internal/netguard"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDC errors.
var (
	ErrOIDCDisabled = errors.New("auth: OIDC login is not enabled")
	ErrOIDCState    = errors.New("auth: invalid or expired OIDC state")
	ErrOIDCDenied   = errors.New("auth: OIDC user is not allowed to use this server")
)

const oidcStateTTL = 10 * time.Minute

// oidcHTTPClient talks to the administrator-configured identity provider,
// which often runs in the internal network: it is exempt from the
// server-mode SSRF policy (internal/netguard) that applies to user input.
var oidcHTTPClient = netguard.TrustedClient(30 * time.Second)

func oidcHTTP(ctx context.Context) context.Context {
	return context.WithValue(oidc.ClientContext(ctx, oidcHTTPClient), oauth2.HTTPClient, oidcHTTPClient)
}

// OIDCCallbackPath is the redirect path registered at the identity provider.
const OIDCCallbackPath = "/api/auth/oidc/callback"

type oidcClient struct {
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	issuer   string
}

// oidcClient performs provider discovery on first use and caches the result,
// so a temporarily unreachable IdP does not prevent the server from starting.
func (s *Service) oidcClient(ctx context.Context) (*oidcClient, error) {
	oc := s.cfg.Auth.OIDC
	if !oc.Enabled {
		return nil, ErrOIDCDisabled
	}
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidc != nil {
		return s.oidc, nil
	}
	p, err := oidc.NewProvider(oidcHTTP(ctx), oc.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery: %w", err)
	}
	secret, err := s.cfg.OIDCClientSecret()
	if err != nil {
		return nil, err
	}
	s.oidc = &oidcClient{
		verifier: p.Verifier(&oidc.Config{ClientID: oc.ClientID}),
		issuer:   oc.Issuer,
		oauth: oauth2.Config{
			ClientID:     oc.ClientID,
			ClientSecret: secret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  s.cfg.PublicURLJoin(OIDCCallbackPath),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
	}
	return s.oidc, nil
}

// OIDCAuthURL starts an OIDC login (authorization code flow with PKCE, state
// and nonce) and returns the URL to redirect the browser to. returnTo must be
// a local path; anything else is replaced by "/".
func (s *Service) OIDCAuthURL(ctx context.Context, returnTo string) (string, error) {
	c, err := s.oidcClient(ctx)
	if err != nil {
		return "", err
	}
	state, nonce, verifier := randomToken(24), randomToken(24), oauth2.GenerateVerifier()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oidc_states(state_hash, nonce, verifier, return_to, expires_at) VALUES (?,?,?,?,?)`,
		hashToken(state), nonce, verifier, SafeReturnPath(returnTo), s.now().Add(oidcStateTTL).Unix()); err != nil {
		return "", err
	}
	return c.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// SafeReturnPath accepts only local absolute paths (no scheme, no "//host").
func SafeReturnPath(p string) string {
	if p == "" || p[0] != '/' || len(p) > 2048 || strings.ContainsRune(p, '\\') {
		return "/"
	}
	// Browsers drop TAB/CR/LF and other control characters inside URLs
	// ("/\t/evil" becomes "//evil"), so any control or space character is
	// refused outright.
	for _, r := range p {
		if r < 0x21 || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "/"
		}
	}
	if strings.HasPrefix(p, "//") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return p
}

type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Nonce             string `json:"nonce"`
}

// OIDCCallback completes an OIDC login: it validates state, exchanges the
// code (PKCE), verifies the ID token and nonce, maps groups to roles,
// provisions or updates the user and creates a web session.
func (s *Service) OIDCCallback(ctx context.Context, state, code string, ci ClientInfo) (*LoginResult, string, error) {
	c, err := s.oidcClient(ctx)
	if err != nil {
		return nil, "", err
	}
	h := hashToken(state)
	var nonce, verifier, returnTo string
	var exp int64
	err = s.db.QueryRowContext(ctx, `SELECT nonce, verifier, return_to, expires_at FROM oidc_states WHERE state_hash = ?`, h).
		Scan(&nonce, &verifier, &returnTo, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrOIDCState
	}
	if err != nil {
		return nil, "", err
	}
	s.db.ExecContext(ctx, `DELETE FROM oidc_states WHERE state_hash = ?`, h)
	if s.now().Unix() >= exp {
		return nil, "", ErrOIDCState
	}
	tok, err := c.oauth.Exchange(oidcHTTP(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", fmt.Errorf("auth: OIDC code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, "", errors.New("auth: OIDC response without id_token")
	}
	idt, err := c.verifier.Verify(oidcHTTP(ctx), raw)
	if err != nil {
		return nil, "", fmt.Errorf("auth: OIDC id_token: %w", err)
	}
	var cl oidcClaims
	var all map[string]any
	if err := idt.Claims(&cl); err != nil {
		return nil, "", err
	}
	if err := idt.Claims(&all); err != nil {
		return nil, "", err
	}
	if cl.Nonce != nonce {
		return nil, "", errors.New("auth: OIDC nonce mismatch")
	}
	groups := claimStrings(all[s.cfg.Auth.OIDC.GroupsClaim])
	u, err := s.provisionOIDCUser(ctx, c.issuer, cl, groups)
	if err != nil {
		s.Audit(WithActor(ctx, Actor{Name: cl.PreferredUsername, IP: ci.IP}), "login.oidc", cl.Subject, false, err.Error())
		return nil, "", err
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ci.IP})
	if u.Disabled {
		s.Audit(ctx, "login.oidc", u.Username, false, "disabled")
		return nil, "", ErrUserDisabled
	}
	res, err := s.finishLogin(ctx, u, KindWeb, true, ci)
	return res, returnTo, err
}

func claimStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// oidcRole maps group claims to a role. ok=false means access is denied.
// keep reports that the existing role should be kept (no admin_group set).
func (s *Service) oidcRole(groups []string) (role Role, keep, ok bool) {
	oc := s.cfg.Auth.OIDC
	if oc.AdminGroup != "" && slices.Contains(groups, oc.AdminGroup) {
		return RoleAdmin, false, true
	}
	if oc.UserGroup != "" && !slices.Contains(groups, oc.UserGroup) {
		return "", false, false
	}
	return RoleUser, oc.AdminGroup == "", true
}

func (s *Service) provisionOIDCUser(ctx context.Context, issuer string, cl oidcClaims, groups []string) (*User, error) {
	if cl.Subject == "" {
		return nil, errors.New("auth: OIDC id_token without subject")
	}
	role, keep, ok := s.oidcRole(groups)
	if !ok {
		return nil, ErrOIDCDenied
	}
	ctx = WithActor(ctx, Actor{Name: "oidc"})
	var id int64
	verified := cl.EmailVerified != nil && *cl.EmailVerified
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE oidc_issuer = ? AND oidc_subject = ?`, issuer, cl.Subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && cl.Email != "" && verified {
		// Link an existing local account with the same, IdP-verified address
		// if that address was verified on our side too (set by an admin), so
		// nobody can pre-register a victim's address and capture the
		// victim's first OIDC login (A-05).
		err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ? AND oidc_subject IS NULL AND email_verified = 1`,
			cl.Email).Scan(&id)
		if err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE users SET oidc_issuer = ?, oidc_subject = ? WHERE id = ?`, issuer, cl.Subject, id)
			s.auditUser(ctx, "user.oidc_link", id, err, "")
		}
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s.createOIDCUser(ctx, issuer, cl, role)
	case err != nil:
		return nil, err
	}
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if !keep && u.Role != role && !(role == RoleUser && u.Role == RoleReadonly) {
		if err := s.SetRole(ctx, id, role); err != nil && !errors.Is(err, ErrLastAdmin) {
			return nil, err
		}
	}
	name := cl.Name
	if name == "" {
		name = u.DisplayName
	}
	s.db.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ?`, name, id)
	if cl.Email != "" && verified && !strings.EqualFold(cl.Email, u.Email) {
		e := cl.Email
		s.UpdateUser(ctx, id, UserUpdate{Email: &e}) // ignore conflicts with other users
	}
	return s.GetUser(ctx, id)
}

func (s *Service) createOIDCUser(ctx context.Context, issuer string, cl oidcClaims, role Role) (*User, error) {
	base := cl.PreferredUsername
	if ValidateUsername(base) != nil {
		base, _, _ = strings.Cut(cl.Email, "@")
	}
	if ValidateUsername(base) != nil {
		base = "oidc-user"
	}
	email := cl.Email
	if _, err := normalizeEmail(email); err != nil {
		email = ""
	}
	unverified := cl.EmailVerified == nil || !*cl.EmailVerified
	for i := 0; i < 50; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", truncate(base, 58), i+1)
		}
		u, err := s.CreateUser(ctx, NewUser{Username: name, Email: email, DisplayName: cl.Name, Role: role,
			oidcIssuer: issuer, oidcSubject: cl.Subject, emailUnverified: unverified})
		if errors.Is(err, ErrExists) {
			if _, e := s.GetUserByName(ctx, name); e == nil {
				continue // username taken
			}
			email = "" // email taken by another account
			i--
			continue
		}
		return u, err
	}
	return nil, errors.New("auth: could not allocate a username for OIDC user")
}
