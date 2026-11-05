// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Client is an OAuth client registration (docs/AUFGABEN-PROJEKTINHABER.md).
type Client struct {
	ID string `json:"clientId"`
	// Secret is required for Google desktop clients (Google treats it as
	// public) and for confidential web clients (server mode).
	Secret string `json:"clientSecret,omitempty"`
	// Tenant is the Microsoft authority: "common" (default: work/school and
	// personal accounts), "organizations", "consumers" or a tenant id.
	Tenant string `json:"tenant,omitempty"`
	// Source tells where the client comes from: "env", "admin", "user", "builtin".
	Source string `json:"source,omitempty"`
}

// Configured reports whether a client ID is set.
func (c Client) Configured() bool { return c.ID != "" }

// Clients holds the client registrations per provider.
type Clients map[string]Client

// ClientResolver returns the client of a provider (user override → admin →
// environment → built-in).
type ClientResolver interface {
	Client(ctx context.Context, provider string) (Client, error)
}

// StaticClients is a ClientResolver on a fixed map.
type StaticClients Clients

// Client implements ClientResolver.
func (s StaticClients) Client(_ context.Context, provider string) (Client, error) {
	if _, ok := Endpoints[provider]; !ok {
		return Client{}, ErrUnknownProvider
	}
	return s[provider], nil
}

// Environment variables with client registrations.
const (
	EnvMSClientID         = "TUXMAIL_MS_CLIENT_ID"
	EnvMSClientSecret     = "TUXMAIL_MS_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a credential
	EnvMSTenant           = "TUXMAIL_MS_TENANT"
	EnvGoogleClientID     = "TUXMAIL_GOOGLE_CLIENT_ID"
	EnvGoogleClientSecret = "TUXMAIL_GOOGLE_CLIENT_SECRET" // #nosec G101 -- environment variable name, not a credential
)

// BuiltinClients are compiled-in registrations of the TuxMail project. They
// are empty until the project owner registered the apps
// (docs/AUFGABEN-PROJEKTINHABER.md 3 and 4); release builds can set them with
// -ldflags "-X github.com/Midas1989/Mailclient/internal/oauth.builtinMSClientID=…".
//
// builtinGoogleClientSecret is the secret of Google's "installed app"
// registration. Google documents that such a secret is not confidential, so
// compiling it into DESKTOP release binaries is by design (NC-08); Microsoft
// is a public client without a secret. Server and Docker builds must not
// carry it: build/lib.sh go_ldflags_server omits it (docs/MICROSOFT-GOOGLE.md).
var (
	builtinMSClientID         = ""
	builtinGoogleClientID     = ""
	builtinGoogleClientSecret = ""
)

// ClientsFromEnv reads the client registrations from the environment (and
// the built-in defaults). get is os.Getenv when nil.
func ClientsFromEnv(get func(string) string) Clients {
	if get == nil {
		get = os.Getenv
	}
	out := Clients{}
	ms := Client{ID: builtinMSClientID, Source: "builtin"}
	if v := get(EnvMSClientID); v != "" {
		ms = Client{ID: v, Secret: get(EnvMSClientSecret), Source: "env"}
	}
	ms.Tenant = get(EnvMSTenant)
	if ms.ID != "" {
		out[Microsoft] = ms
	}
	g := Client{ID: builtinGoogleClientID, Secret: builtinGoogleClientSecret, Source: "builtin"}
	if v := get(EnvGoogleClientID); v != "" {
		g = Client{ID: v, Secret: get(EnvGoogleClientSecret), Source: "env"}
	}
	if g.ID != "" {
		out[Google] = g
	}
	return out
}

// Merge returns c with the configured entries of over replacing its own.
func (c Clients) Merge(over Clients) Clients {
	out := Clients{}
	for k, v := range c {
		out[k] = v
	}
	for k, v := range over {
		if v.ID != "" {
			out[k] = v
		}
	}
	return out
}

// Endpoint describes the endpoints and scopes of a provider.
type Endpoint struct {
	ID          string
	DisplayName string
	// AuthURL, TokenURL and DeviceURL may contain "{tenant}".
	AuthURL, TokenURL, DeviceURL string
	// UserInfoURL is used when the id_token lacks an e-mail address.
	UserInfoURL string
	// DefaultTenant replaces "{tenant}" when the client has none.
	DefaultTenant string
	// Scopes per resource; Base scopes are added to every request.
	Base      []string
	Resources map[string][]string
	// LoopbackHost/LoopbackPath form the desktop redirect URI.
	LoopbackHost string
	LoopbackPath string
	// AuthParams are added to the authorization request.
	AuthParams map[string]string
	// MultiResource: tokens are issued per resource (Microsoft).
	MultiResource bool
}

// Endpoints of the supported providers. Tests replace the URLs.
var Endpoints = map[string]Endpoint{
	// #nosec G101 -- public OAuth endpoint/client metadata, not a credential
	Microsoft: {
		ID: Microsoft, DisplayName: "Microsoft",
		AuthURL:       "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
		TokenURL:      "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
		DeviceURL:     "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/devicecode",
		UserInfoURL:   "https://graph.microsoft.com/oidc/userinfo",
		DefaultTenant: "common",
		Base:          []string{"offline_access", "openid", "email", "profile"},
		Resources: map[string][]string{
			ResourceGraph: {
				"https://graph.microsoft.com/User.Read",
				"https://graph.microsoft.com/Mail.ReadWrite",
				"https://graph.microsoft.com/Mail.ReadWrite.Shared",
				"https://graph.microsoft.com/Mail.Send",
				"https://graph.microsoft.com/Mail.Send.Shared",
				"https://graph.microsoft.com/MailboxSettings.ReadWrite",
				"https://graph.microsoft.com/Calendars.ReadWrite",
				"https://graph.microsoft.com/Calendars.ReadWrite.Shared",
				"https://graph.microsoft.com/Contacts.ReadWrite",
				"https://graph.microsoft.com/Contacts.ReadWrite.Shared",
				"https://graph.microsoft.com/Tasks.ReadWrite",
			},
			ResourceOutlook: {
				"https://outlook.office.com/IMAP.AccessAsUser.All",
				"https://outlook.office.com/SMTP.Send",
				"https://outlook.office.com/EAS.AccessAsUser.All",
				"https://outlook.office.com/EWS.AccessAsUser.All",
			},
		},
		// Microsoft accepts any port for http://localhost registrations.
		LoopbackHost:  "localhost",
		LoopbackPath:  "/",
		AuthParams:    map[string]string{"prompt": "select_account"},
		MultiResource: true,
	},
	// #nosec G101 -- public OAuth endpoint/client metadata, not a credential
	Google: {
		ID: Google, DisplayName: "Google",
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		Base:        []string{"openid", "email", "profile"},
		Resources: map[string][]string{
			ResourceGoogle: {
				"https://mail.google.com/",
				"https://www.googleapis.com/auth/calendar",
				"https://www.googleapis.com/auth/contacts",
			},
		},
		LoopbackHost: "127.0.0.1",
		LoopbackPath: "/callback",
		// offline + consent: Google only returns a refresh token on consent.
		AuthParams: map[string]string{"access_type": "offline", "prompt": "consent", "include_granted_scopes": "true"},
	},
}

// endpoint returns the endpoint with "{tenant}" filled in.
func endpoint(provider, tenant string) (Endpoint, error) {
	ep, ok := Endpoints[provider]
	if !ok {
		return Endpoint{}, ErrUnknownProvider
	}
	if tenant == "" {
		tenant = ep.DefaultTenant
	}
	// The tenant is inserted into URL paths: only a GUID, a domain or one
	// of the well-known names is accepted (no '/', '?', '#', '@', AUDIT-C).
	if !validTenant(tenant) {
		return Endpoint{}, fmt.Errorf("oauth: invalid tenant %q", tenant)
	}
	r := strings.NewReplacer("{tenant}", tenant)
	ep.AuthURL, ep.TokenURL, ep.DeviceURL = r.Replace(ep.AuthURL), r.Replace(ep.TokenURL), r.Replace(ep.DeviceURL)
	return ep, nil
}

func validTenant(t string) bool {
	if len(t) > 253 {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return false
		}
	}
	return !strings.Contains(t, "..")
}

// ScopesFor returns the scopes of the given resources plus the base scopes.
func (ep Endpoint) ScopesFor(resources ...string) []string {
	out := append([]string{}, ep.Base...)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s] = true
	}
	for _, r := range resources {
		for _, s := range ep.Resources[r] {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// ResourcesForKind returns the resources a sign-in for an account kind
// requests. Microsoft: Graph accounts also get Exchange scopes for nothing
// (they would need separate admin consent in some tenants), so only the
// resource of the access method is requested. Google: everything.
func ResourcesForKind(provider string, kind domain.AccountKind) []string {
	if provider == Google {
		return []string{ResourceGoogle}
	}
	return []string{ResourceForKind(provider, kind)}
}

// MicrosoftOutlookScopesFor narrows the Exchange scopes to the protocol in
// use (IMAP+SMTP, EAS or EWS), so that consent shows only what is needed.
func MicrosoftOutlookScopesFor(kind domain.AccountKind) []string {
	all := Endpoints[Microsoft].Resources[ResourceOutlook]
	want := map[domain.AccountKind][]string{
		domain.KindIMAP: {"IMAP.AccessAsUser.All", "SMTP.Send"},
		domain.KindSMTP: {"SMTP.Send"},
		domain.KindEAS:  {"EAS.AccessAsUser.All"},
		domain.KindEWS:  {"EWS.AccessAsUser.All"},
	}[kind]
	if want == nil {
		return all
	}
	var out []string
	for _, s := range all {
		for _, w := range want {
			if strings.HasSuffix(s, "/"+w) {
				out = append(out, s)
			}
		}
	}
	return out
}
