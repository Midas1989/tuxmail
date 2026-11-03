// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"strings"
)

// OAuth configures the Microsoft/Google sign-in of mail accounts in server
// mode (docs/MICROSOFT-GOOGLE.md). The redirect URI to register is
// <public_url>/api/oauth/{microsoft|google}/callback.
type OAuth struct {
	Microsoft OAuthClient `yaml:"microsoft"`
	Google    OAuthClient `yaml:"google"`
	// AllowUserClients lets users enter their own client IDs (default true).
	AllowUserClients *bool `yaml:"allow_user_clients"`
	// GraphNotifications enables Microsoft Graph change notifications to
	// <public_url>/api/graph/notifications (requires a public HTTPS URL).
	GraphNotifications bool `yaml:"graph_notifications"`
}

// OAuthClient is a client registration.
type OAuthClient struct {
	ClientID string `yaml:"client_id"`
	// ClientSecretFile holds the client secret (web clients); keep it out of the config file.
	ClientSecretFile string `yaml:"client_secret_file"`
	// Tenant (Microsoft): common | organizations | consumers | <tenant id>.
	Tenant string `yaml:"tenant"`
}

// Secret reads the client secret file ("" if none).
func (c OAuthClient) Secret() (string, error) {
	if c.ClientSecretFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("config: oauth client secret: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// UserClientsAllowed reports the allow_user_clients policy.
func (o OAuth) UserClientsAllowed() bool { return o.AllowUserClients == nil || *o.AllowUserClients }
