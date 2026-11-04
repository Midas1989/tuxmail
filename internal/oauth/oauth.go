// SPDX-License-Identifier: AGPL-3.0-or-later

// Package oauth implements the OAuth2 sign-in flows of TuxMail for
// Microsoft (Entra ID, v2 endpoints) and Google (KONZEPT 7.3, 7.4):
//
//   - authorization code flow with PKCE (RFC 7636) and a temporary loopback
//     listener http://127.0.0.1:<port>/callback (desktop, RFC 8252); the
//     system browser is opened through an injectable Opener (the Wails shell
//     passes runtime.BrowserOpenURL);
//   - the same flow with a web redirect /api/oauth/{provider}/callback for
//     the server mode (public URL of the server);
//   - the device authorization grant (RFC 8628) as fallback (Microsoft only;
//     Google does not allow mail scopes for device clients).
//
// The refresh token of an account is stored as a JSON Grant in the secret
// store (never in the database). TokenSource returns valid access tokens and
// refreshes them automatically; Microsoft issues tokens per resource, so
// Graph and Exchange (IMAP/SMTP/EAS/EWS on outlook.office.com) tokens are
// cached separately and obtained from the same refresh token.
//
// Client IDs are not compiled in yet (docs/AUFGABEN-PROJEKTINHABER.md 3/4):
// they come from the environment (TUXMAIL_MS_CLIENT_ID,
// TUXMAIL_GOOGLE_CLIENT_ID, TUXMAIL_GOOGLE_CLIENT_SECRET ...), from the
// server configuration (admin) or from the user's own client ("Anmeldedienste"
// in the settings). Without one, flows fail with ErrNotConfigured.
package oauth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Provider ids (match autoconfig.OAuthGoogle / OAuthMicrosoft).
const (
	Microsoft = "microsoft"
	Google    = "google"
)

// Resources a token can be issued for. Google issues one token for all
// scopes; Microsoft issues one token per resource.
const (
	ResourceGraph   = "graph"   // https://graph.microsoft.com
	ResourceOutlook = "outlook" // https://outlook.office.com (IMAP, SMTP, EAS, EWS)
	ResourceGoogle  = "google"
)

// Errors.
var (
	// ErrNotConfigured: no client ID for the provider (see package doc).
	ErrNotConfigured = errors.New("oauth: no client ID configured for this provider")
	// ErrUnknownProvider is returned for provider ids other than Microsoft/Google.
	ErrUnknownProvider = errors.New("oauth: unknown provider")
	// ErrReauthRequired: the refresh token was revoked or expired (Google
	// testing mode: after 7 days); the user has to sign in again.
	ErrReauthRequired = errors.New("oauth: sign-in expired, please sign in again")
	// ErrConsentRequired: the organization requires admin consent.
	ErrConsentRequired = errors.New("oauth: an administrator of the organization must approve TuxMail")
	// ErrAccessDenied: the user canceled or denied the consent.
	ErrAccessDenied = errors.New("oauth: access denied")
	// ErrFlowExpired: the device code or pending flow expired.
	ErrFlowExpired = errors.New("oauth: sign-in timed out")
	// ErrDeviceUnsupported: the provider has no device flow for mail scopes.
	ErrDeviceUnsupported = errors.New("oauth: device sign-in is not supported by this provider")
	// ErrStateMismatch: the callback does not belong to a pending flow.
	ErrStateMismatch = errors.New("oauth: unknown or expired sign-in request")
	// ErrClientChanged: the grant was issued to another client ID.
	ErrClientChanged = errors.New("oauth: the client ID changed since sign-in, please sign in again")
)

// Error is an error response of an authorization or token endpoint.
type Error struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"-"`
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth: %s: %s", e.Code, firstLine(e.Description))
	}
	return "oauth: " + e.Code
}

// Unwrap maps well-known codes to the package errors.
func (e *Error) Unwrap() error {
	switch e.Code {
	case "invalid_grant":
		// AADSTS65001: consent missing; AADSTS90094/AADSTS65004 admin consent.
		if strings.Contains(e.Description, "AADSTS65001") || strings.Contains(e.Description, "AADSTS90094") {
			return ErrConsentRequired
		}
		return ErrReauthRequired
	case "consent_required", "interaction_required":
		if strings.Contains(e.Description, "AADSTS90094") || strings.Contains(e.Description, "admin") {
			return ErrConsentRequired
		}
		return ErrReauthRequired
	case "access_denied":
		if strings.Contains(e.Description, "AADSTS90094") || strings.Contains(e.Description, "AADSTS65004") {
			return ErrConsentRequired
		}
		return ErrAccessDenied
	case "expired_token":
		return ErrFlowExpired
	case "unauthorized_client", "invalid_client":
		return ErrNotConfigured
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// AccessToken is a cached access token of one resource.
type AccessToken struct {
	Token  string    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

// Valid reports whether the token is usable at now (with a safety margin).
func (t AccessToken) Valid(now time.Time) bool {
	return t.Token != "" && (t.Expiry.IsZero() || now.Add(2*time.Minute).Before(t.Expiry))
}

// Grant is the persistent result of a sign-in, stored as JSON in the secret
// store under the account's OAuth secret reference.
type Grant struct {
	Provider string `json:"provider"`
	Tenant   string `json:"tenant,omitempty"` // Microsoft authority (common, organizations, <tenant id>)
	ClientID string `json:"clientId"`
	// RefreshToken is rotated on every refresh by Microsoft.
	RefreshToken string   `json:"refreshToken"`
	Scopes       []string `json:"scopes,omitempty"` // as requested at sign-in
	// Identity from the id_token.
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
	Subject string `json:"sub,omitempty"`
	// TenantID is the tenant of the signed-in user (Microsoft "tid").
	TenantID string `json:"tid,omitempty"`
	// Tokens caches access tokens per resource.
	Tokens   map[string]AccessToken `json:"tokens,omitempty"`
	IssuedAt time.Time              `json:"issuedAt"`
}

// Personal reports whether a Microsoft grant belongs to a personal
// (outlook.com) account.
func (g Grant) Personal() bool {
	return g.Provider == Microsoft && g.TenantID == "9188040d-6c67-4c5b-b112-36a304b66dad"
}

// ResourceForKind returns the token resource a provider of the given
// account kind needs.
func ResourceForKind(provider string, kind domain.AccountKind) string {
	if provider == Google {
		return ResourceGoogle
	}
	switch kind {
	case domain.KindGraph:
		return ResourceGraph
	default: // imap, smtp, eas, ews
		return ResourceOutlook
	}
}

// Account extras used by OAuth accounts.
const (
	// ExtraProvider names the OAuth provider of an account ("microsoft", "google").
	ExtraProvider = "oauthProvider"
	// ExtraSecretRef points to the grant (default Account.SecretRef); shared
	// mailboxes reuse the grant of their owner account.
	// #nosec G101 -- public OAuth endpoint/client metadata, not a credential
	ExtraSecretRef = "oauthSecretRef"
)

// SecretRef returns the secret reference of an account's grant.
func SecretRef(acc domain.Account) string {
	if r := acc.Extra[ExtraSecretRef]; r != "" {
		return r
	}
	return acc.SecretRef
}

// ProviderOf returns the OAuth provider of an account (from the extra, or
// guessed from the kind/host).
func ProviderOf(acc domain.Account) string {
	if p := acc.Extra[ExtraProvider]; p != "" {
		return p
	}
	switch acc.Kind {
	case domain.KindGmail:
		return Google
	case domain.KindGraph:
		return Microsoft
	}
	if acc.Incoming != nil {
		h := strings.ToLower(acc.Incoming.Host)
		switch {
		case strings.HasSuffix(h, "gmail.com"), strings.HasSuffix(h, "googlemail.com"):
			return Google
		case strings.HasSuffix(h, "office365.com"), strings.HasSuffix(h, "outlook.com"), strings.HasSuffix(h, "office.com"):
			return Microsoft
		}
	}
	return ""
}
