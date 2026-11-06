// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config loads and validates the server-mode configuration file
// (KONZEPT 4.5, 15.5). The YAML layout is a contract shared with the Ansible
// playbook in deploy/ansible; keys must not be renamed without updating it.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from Go duration strings
// such as "720h" or "30m".
type Duration time.Duration

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: %w", n.Line, s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config is the complete server configuration.
type Config struct {
	Listen         string   `yaml:"listen"`
	PublicURL      string   `yaml:"public_url"`
	DataDir        string   `yaml:"data_dir"`
	Log            Log      `yaml:"log"`
	TLS            TLS      `yaml:"tls"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	Auth           Auth     `yaml:"auth"`
	Limits         Limits   `yaml:"limits"`
	OAuth          OAuth    `yaml:"oauth"`    // oauth.go
	Push           Push     `yaml:"push"`     // push.go
	Security       Security `yaml:"security"` // security.go
}

// Log configures logging.
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
	File   string `yaml:"file"`   // empty = stderr (journald)
}

// TLS configures the built-in TLS listener.
type TLS struct {
	Mode     string `yaml:"mode"` // none (behind a reverse proxy) | file
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Auth configures user authentication.
type Auth struct {
	SessionLifetime       Duration `yaml:"session_lifetime"`
	PasswordMinLength     int      `yaml:"password_min_length"`
	Registration          string   `yaml:"registration"` // disabled | invite
	TOTPRequiredForAdmins bool     `yaml:"totp_required_for_admins"`
	OIDC                  OIDC     `yaml:"oidc"`
	Passkeys              Passkeys `yaml:"passkeys"` // passkeys.go
}

// OIDC configures login through an external OpenID Connect provider.
type OIDC struct {
	Enabled          bool   `yaml:"enabled"`
	Issuer           string `yaml:"issuer"`
	ClientID         string `yaml:"client_id"`
	ClientSecretFile string `yaml:"client_secret_file"`
	AdminGroup       string `yaml:"admin_group"`
	UserGroup        string `yaml:"user_group"`
	GroupsClaim      string `yaml:"groups_claim"`
}

// Limits configures resource limits.
type Limits struct {
	MaxUploadMB        int `yaml:"max_upload_mb"`
	LoginRatePerMinute int `yaml:"login_rate_per_minute"`
	// BackgroundSync keeps the application layer (mail sync, timers,
	// workflows, out-of-office, push notifications) of users with at least
	// one enabled account running while they are not logged in.
	BackgroundSync bool `yaml:"background_sync"`
	// BackgroundSyncMaxUsers caps the number of users synced in the
	// background at the same time (UI sessions are not limited).
	BackgroundSyncMaxUsers int `yaml:"background_sync_max_users"`
}

// Default returns the configuration used for keys missing from the file.
func Default() Config {
	return Config{
		Listen:         "127.0.0.1:8080",
		PublicURL:      "http://127.0.0.1:8080",
		DataDir:        "/var/lib/tuxmail",
		Log:            Log{Level: "info", Format: "json"},
		TLS:            TLS{Mode: "none"},
		TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		Auth: Auth{
			SessionLifetime:   Duration(720 * time.Hour),
			PasswordMinLength: 12,
			Registration:      "disabled",
			OIDC:              OIDC{GroupsClaim: "groups"},
		},
		Limits: Limits{MaxUploadMB: 200, LoginRatePerMinute: 10, BackgroundSync: true, BackgroundSyncMaxUsers: 500},
	}
}

// Environment variables that override file values.
const (
	EnvListen  = "TUXMAIL_LISTEN"
	EnvDataDir = "TUXMAIL_DATA_DIR"
	EnvPublic  = "TUXMAIL_PUBLIC_URL"
	EnvLogLvl  = "TUXMAIL_LOG_LEVEL"
)

// Load reads the file at path (which must exist), applies defaults and
// environment overrides and validates the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Parse parses YAML data like Load. Unknown keys are rejected so typos in
// the playbook template are detected by `tuxmail check-config`.
func Parse(data []byte) (*Config, error) {
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if c.Auth.OIDC.GroupsClaim == "" {
		c.Auth.OIDC.GroupsClaim = "groups"
	}
	c.applyEnv(os.Getenv)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyEnv(get func(string) string) {
	if v := get(EnvListen); v != "" {
		c.Listen = v
	}
	if v := get(EnvDataDir); v != "" {
		c.DataDir = v
	}
	if v := get(EnvPublic); v != "" {
		c.PublicURL = v
	}
	if v := get(EnvLogLvl); v != "" {
		c.Log.Level = v
	}
}

// Validate checks all values and returns all problems joined.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Listen == "" {
		add("listen: must not be empty")
	} else if sock, ok := c.UnixSocket(); ok {
		if !filepath.IsAbs(sock) {
			add("listen: unix socket path must be absolute: %q", sock)
		}
	} else if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen: %v", err)
	}
	if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("public_url: must be an absolute http(s) URL, got %q", c.PublicURL)
	} else if u.RawQuery != "" || u.Fragment != "" {
		add("public_url: must not contain a query or fragment")
	}
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		add("data_dir: must be an absolute path, got %q", c.DataDir)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: must be debug, info, warn or error, got %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format: must be json or text, got %q", c.Log.Format)
	}
	switch c.TLS.Mode {
	case "none":
	case "file":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			add("tls: mode file requires cert_file and key_file")
		}
		if _, ok := c.UnixSocket(); ok {
			add("tls: mode file cannot be combined with a unix socket listener")
		}
	default:
		add("tls.mode: must be none or file, got %q", c.TLS.Mode)
	}
	if _, err := c.TrustedPrefixes(); err != nil {
		add("trusted_proxies: %v", err)
	}
	if c.Auth.SessionLifetime.D() < 5*time.Minute {
		add("auth.session_lifetime: must be at least 5m")
	}
	if c.Auth.PasswordMinLength < 8 || c.Auth.PasswordMinLength > 1024 {
		add("auth.password_min_length: must be between 8 and 1024")
	}
	switch c.Auth.Registration {
	case "disabled", "invite":
	default:
		add("auth.registration: must be disabled or invite, got %q", c.Auth.Registration)
	}
	if o := c.Auth.OIDC; o.Enabled {
		if u, err := url.Parse(o.Issuer); err != nil || u.Scheme == "" || u.Host == "" {
			add("auth.oidc.issuer: must be an absolute URL")
		}
		if o.ClientID == "" {
			add("auth.oidc.client_id: required when oidc is enabled")
		}
		if o.ClientSecretFile != "" && !filepath.IsAbs(o.ClientSecretFile) {
			add("auth.oidc.client_secret_file: must be an absolute path")
		}
	}
	errs = append(errs, c.validatePasskeys()...)
	if c.Limits.MaxUploadMB < 1 {
		add("limits.max_upload_mb: must be at least 1")
	}
	if c.Limits.LoginRatePerMinute < 1 {
		add("limits.login_rate_per_minute: must be at least 1")
	}
	if c.Limits.BackgroundSyncMaxUsers < 1 {
		add("limits.background_sync_max_users: must be at least 1")
	}
	if _, err := c.NetPolicy(); err != nil {
		add("security.allowed_networks: %v", err)
	}
	return errors.Join(errs...)
}

// UnixSocket returns the socket path if Listen has the form "unix:/path".
func (c *Config) UnixSocket() (string, bool) {
	return strings.CutPrefix(c.Listen, "unix:")
}

// TrustedPrefixes parses TrustedProxies. Plain addresses are accepted as
// single-host prefixes.
func (c *Config) TrustedPrefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(c.TrustedProxies))
	for _, s := range c.TrustedProxies {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("invalid address or prefix %q", s)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

// SecureCookies reports whether cookies must carry the Secure attribute.
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(strings.ToLower(c.PublicURL), "https://")
}

// PublicOrigin returns scheme://host of PublicURL (no path).
func (c *Config) PublicOrigin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// PublicURLJoin returns PublicURL with p appended (p starts with "/").
func (c *Config) PublicURLJoin(p string) string {
	return strings.TrimRight(c.PublicURL, "/") + p
}

// ServerDB is the path of the server-level database.
func (c *Config) ServerDB() string { return filepath.Join(c.DataDir, "server.db") }

// OIDCClientSecret reads the client secret file, if configured.
func (c *Config) OIDCClientSecret() (string, error) {
	if c.Auth.OIDC.ClientSecretFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.Auth.OIDC.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("config: oidc client secret: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
