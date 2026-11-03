// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ==== PKCE (RFC 7636) =========================================================

// NewVerifier returns a random PKCE code verifier (43 characters).
func NewVerifier() string { return randomString(32) }

// Challenge returns the S256 code challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ==== token endpoint =========================================================

// tokenResponse is the JSON answer of a token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	Error
}

// postForm posts a form to an OAuth endpoint and decodes the JSON answer.
func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("oauth: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("oauth: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var e Error
		if json.Unmarshal(body, &e) == nil && e.Code != "" {
			e.Status = resp.StatusCode
			return &e
		}
		return fmt.Errorf("oauth: %s: %s", resp.Status, firstLine(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("oauth: decode response: %w", err)
	}
	return nil
}

// idClaims are the claims of an id_token used for the account identity.
type idClaims struct {
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	UPN               string `json:"upn"`
	Name              string `json:"name"`
	Sub               string `json:"sub"`
	TID               string `json:"tid"`
}

// parseIDToken decodes the payload of an id_token. The signature is not
// checked: the token was received directly from the token endpoint over TLS
// (OpenID Connect Core 3.1.3.7).
func parseIDToken(tok string) (idClaims, error) {
	var c idClaims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return c, fmt.Errorf("oauth: malformed id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c, fmt.Errorf("oauth: id_token: %w", err)
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

// applyIdentity fills the identity of g from a token response.
func (g *Grant) applyIdentity(tr tokenResponse) {
	if tr.IDToken == "" {
		return
	}
	c, err := parseIDToken(tr.IDToken)
	if err != nil {
		return
	}
	email := c.Email
	if email == "" && strings.Contains(c.PreferredUsername, "@") {
		email = c.PreferredUsername
	}
	if email == "" && strings.Contains(c.UPN, "@") {
		email = c.UPN
	}
	if email != "" {
		g.Email = strings.ToLower(email)
	}
	if c.Name != "" {
		g.Name = c.Name
	}
	if c.Sub != "" {
		g.Subject = c.Sub
	}
	if c.TID != "" {
		g.TenantID = c.TID
	}
}

// resourceOfScope maps a Microsoft scope to its resource ("" = base scope).
func resourceOfScope(s string) string {
	switch {
	case strings.HasPrefix(s, "https://graph.microsoft.com/"):
		return ResourceGraph
	case strings.HasPrefix(s, "https://outlook.office.com/"), strings.HasPrefix(s, "https://outlook.office365.com/"):
		return ResourceOutlook
	}
	return ""
}

// refreshScopes returns the scope parameter for a token of resource.
func refreshScopes(ep Endpoint, g Grant, resource string) string {
	if !ep.MultiResource {
		return "" // Google: refresh keeps the granted scopes
	}
	out := []string{"offline_access"}
	found := false
	for _, s := range g.Scopes {
		if resourceOfScope(s) == resource {
			out = append(out, s)
			found = true
		}
	}
	if !found {
		out = append(out, ep.Resources[resource]...)
	}
	return strings.Join(out, " ")
}

// primaryResource returns the resource of the first resource scope.
func primaryResource(ep Endpoint, scopes []string) string {
	if !ep.MultiResource {
		for r := range ep.Resources {
			return r
		}
	}
	for _, s := range scopes {
		if r := resourceOfScope(s); r != "" {
			return r
		}
	}
	return ResourceGraph
}

// storeToken caches the access token of tr for resource.
func (g *Grant) storeToken(resource string, tr tokenResponse, now time.Time) {
	if g.Tokens == nil {
		g.Tokens = map[string]AccessToken{}
	}
	t := AccessToken{Token: tr.AccessToken}
	if tr.ExpiresIn > 0 {
		t.Expiry = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	g.Tokens[resource] = t
	if tr.RefreshToken != "" {
		g.RefreshToken = tr.RefreshToken
	}
}
