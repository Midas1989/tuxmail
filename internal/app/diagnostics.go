// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Settings → "Info & Diagnose": version, data directory, the sync status
// of every account and a diagnostics archive for bug reports. The archive
// holds the log files (desktop) and a redacted configuration: no
// passwords, tokens, mail content or full addresses.

// DiagnosticsOptions are set by the host (cmd/tuxmail) after New.
type DiagnosticsOptions struct {
	// LogFile is the current log file (desktop: <data_dir>/logs/tuxmail.log);
	// rotated files next to it (LogFile.1, ...) are exported too. Empty in
	// server mode, where logs are server-wide and not handed to users.
	LogFile string
	// Portable reports the portable desktop mode.
	Portable bool
	// Config returns the redacted host configuration for the archive.
	Config func() any
}

type diagState struct {
	mu   sync.Mutex
	opts DiagnosticsOptions
}

// SetDiagnostics configures the diagnostics page and export.
func (a *App) SetDiagnostics(o DiagnosticsOptions) {
	a.diag.mu.Lock()
	a.diag.opts = o
	a.diag.mu.Unlock()
}

func (a *App) diagOptions() DiagnosticsOptions {
	a.diag.mu.Lock()
	defer a.diag.mu.Unlock()
	return a.diag.opts
}

// DiagnosticsAccount is one account on the diagnostics page.
type DiagnosticsAccount struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Incoming string         `json:"incoming,omitempty"` // host:port (tls)
	Outgoing string         `json:"outgoing,omitempty"`
	Folders  int            `json:"folders"`
	Messages int            `json:"messages"`
	Status   *AccountStatus `json:"status,omitempty"`
}

// DiagnosticsInfo is the content of Settings → Info & Diagnose.
type DiagnosticsInfo struct {
	Version   string               `json:"version"`
	Mode      string               `json:"mode"`
	Portable  bool                 `json:"portable"`
	DataDir   string               `json:"dataDir,omitempty"`
	LogFile   string               `json:"logFile,omitempty"`
	OS        string               `json:"os"`
	GoVersion string               `json:"goVersion"`
	Accounts  []DiagnosticsAccount `json:"accounts"`
}

// Diagnostics returns version, paths and the sync status of all accounts.
func (a *App) Diagnostics(ctx context.Context) (DiagnosticsInfo, error) {
	o := a.diagOptions()
	info := DiagnosticsInfo{Version: a.opts.Version, Mode: a.opts.Mode, Portable: o.Portable, LogFile: o.LogFile,
		OS: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: runtime.Version(), Accounts: []DiagnosticsAccount{}}
	if info.Version == "" {
		info.Version = "dev"
	}
	if a.opts.Mode == ModeDesktop {
		info.DataDir = a.opts.DataDir // the server's paths are not the user's business
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return info, err
	}
	sts, _ := a.sync.Status(ctx)
	byID := map[int64]AccountStatus{}
	for _, s := range sts {
		byID[s.AccountID] = s
	}
	for _, acc := range accs {
		d := DiagnosticsAccount{ID: acc.ID, Name: acc.DisplayName, Kind: string(acc.Kind)}
		if s := acc.Incoming; s != nil && s.Host != "" {
			d.Incoming = fmt.Sprintf("%s:%d (%s)", s.Host, s.Port, s.TLS)
		}
		if s := acc.Outgoing; s != nil && s.Host != "" {
			d.Outgoing = fmt.Sprintf("%s:%d (%s)", s.Host, s.Port, s.TLS)
		}
		if fs, err := a.st.ListFolders(ctx, acc.ID); err == nil {
			d.Folders = len(fs)
			for _, f := range fs {
				d.Messages += f.Total
			}
		}
		if s, ok := byID[acc.ID]; ok {
			s.Error = redactText(s.Error)
			d.Status = &s
		}
		info.Accounts = append(info.Accounts, d)
	}
	return info, nil
}

// DiagnosticsArchive is the exported zip.
type DiagnosticsArchive struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"` // base64 in JSON (Wails)
	// SavedPath (desktop): the archive was also written to
	// <data_dir>/diagnostics/, since the desktop WebView cannot always
	// save downloads.
	SavedPath string `json:"savedPath,omitempty"`
}

// maxLogExport bounds the exported bytes per log file (the newest part).
const maxLogExport = 8 << 20

// ExportDiagnostics builds the diagnostics archive: info.json (as on the
// page, redacted), config.json (redacted host configuration and accounts
// without credentials) and the log files with e-mail addresses masked.
func (a *App) ExportDiagnostics(ctx context.Context) (DiagnosticsArchive, error) {
	info, err := a.Diagnostics(ctx)
	if err != nil {
		return DiagnosticsArchive{}, err
	}
	for i := range info.Accounts {
		info.Accounts[i].Name = redactText(info.Accounts[i].Name)
	}
	o := a.diagOptions()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := a.now()
	add := func(name string, v any) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	if err := add("info.json", info); err != nil {
		return DiagnosticsArchive{}, err
	}
	cfg := map[string]any{"accounts": a.redactedAccounts(ctx)}
	if o.Config != nil {
		cfg["host"] = o.Config()
	}
	if err := add("config.json", cfg); err != nil {
		return DiagnosticsArchive{}, err
	}
	if o.LogFile != "" {
		files, _ := filepath.Glob(o.LogFile + "*")
		sort.Strings(files)
		for _, f := range files {
			if err := addLog(zw, f, now); err != nil {
				a.log.Warn("diagnostics: log file", "file", f, "err", err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		return DiagnosticsArchive{}, err
	}
	z := DiagnosticsArchive{Filename: "tuxmail-diagnose-" + now.Format("20060102-150405") + ".zip", MIMEType: "application/zip",
		Data: buf.Bytes()}
	if a.opts.Mode == ModeDesktop && a.opts.DataDir != "" {
		dir := filepath.Join(a.opts.DataDir, "diagnostics")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			p := filepath.Join(dir, z.Filename)
			if err := os.WriteFile(p, z.Data, 0o600); err == nil {
				z.SavedPath = p
			}
		}
	}
	return z, nil
}

func addLog(zw *zip.Writer, path string, now time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxLogExport {
		if _, err := f.Seek(st.Size()-maxLogExport, io.SeekStart); err != nil {
			return err
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "logs/" + filepath.Base(path), Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, redactText(string(b)))
	return err
}

func (a *App) redactedAccounts(ctx context.Context) []map[string]any {
	accs, err := a.st.ListAccounts(ctx)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	srv := func(s *ServerConfig) map[string]any {
		if s == nil {
			return nil
		}
		return map[string]any{"host": s.Host, "port": s.Port, "tls": s.TLS, "auth": s.Auth, "username": maskEmail(s.Username)}
	}
	for _, acc := range accs {
		extra := map[string]string{}
		for k, v := range acc.Extra {
			lk := strings.ToLower(k)
			switch {
			case strings.Contains(lk, "token"), strings.Contains(lk, "secret"), strings.Contains(lk, "password"), strings.Contains(lk, "key"):
				extra[k] = "[redacted]"
			default:
				extra[k] = redactText(v)
			}
		}
		out = append(out, map[string]any{"id": acc.ID, "kind": acc.Kind, "enabled": acc.Enabled,
			"incoming": srv(acc.Incoming), "outgoing": srv(acc.Outgoing), "extra": extra})
	}
	return out
}

var (
	emailRe = regexp.MustCompile(`([A-Za-z0-9._%+\-])[A-Za-z0-9._%+\-]*@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
	// secretRe matches key=value / key: value / "key": "value" pairs whose
	// key names a credential, including compound keys (access_token,
	// client_secret, x-api-key, refresh-token, sessionid), OAuth codes
	// (code, device_code, user_code) and an auth scheme before the value
	// ("Authorization: Bearer <token>") (AUDIT-C).
	secretRe = regexp.MustCompile(`(?i)\b([a-z0-9_\-]*(?:password|passwd|passphrase|pwd|token|secret|api[_\-]?key|apikey|cookie|session[_\-]?id|sessionid)[a-z0-9_\-]*|authorization|auth|pass|code|device_code|user_code|auth_code)(["']?\s*[=:]\s*["']?)((?:bearer|basic|token|negotiate|digest)\s+)?[^\s"',}&;]+`)
	// userinfoRe matches the password of URL userinfo (https://user:pw@host).
	userinfoRe = regexp.MustCompile(`(://[^/\s:@]*:)[^@\s/]+@`)
	// jwtRe matches JSON Web Tokens anywhere in the text.
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]*`)
	// imapLoginRe matches the IMAP command LOGIN <user> <password> (atoms or
	// quoted strings; protocol traces write the command in upper case, which
	// keeps prose such as "login failed for user" untouched) (NC-05).
	imapLoginRe = regexp.MustCompile(`\bLOGIN[ \t]+("(?:[^"\\]|\\.)*"|[^\s"]+)[ \t]+(?:"(?:[^"\\]|\\.)*"|[^\s"]+)`)
	// smtpAuthRe matches SMTP AUTH / IMAP AUTHENTICATE with an initial
	// response (AUTH PLAIN <base64>, AUTH XOAUTH2 <base64>, ...) (NC-05).
	smtpAuthRe = regexp.MustCompile(`\b(AUTH(?:ENTICATE)?[ \t]+(?:PLAIN|LOGIN|XOAUTH2|OAUTHBEARER|CRAM-MD5|SCRAM-SHA-[0-9]+(?:-PLUS)?))[ \t]+[A-Za-z0-9+/=_\-]{4,}`)
	// privateKeyRe matches armored private key blocks (PGP, PKCS#8, RSA, EC,
	// OpenSSH); a block without END marker (truncated log) is cut at the end
	// of the text (NC-05).
	privateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`)
)

// maskEmail turns anna.berger@example.org into a***@example.org.
func maskEmail(s string) string { return emailRe.ReplaceAllString(s, "$1***@$2") }

// redactText masks e-mail addresses and key=value secrets in free text.
func redactText(s string) string {
	s = privateKeyRe.ReplaceAllString(s, "[redacted-private-key]")
	s = smtpAuthRe.ReplaceAllString(s, "$1 [redacted]")
	s = imapLoginRe.ReplaceAllString(s, `LOGIN $1 "[redacted]"`)
	s = userinfoRe.ReplaceAllString(s, "$1[redacted]@")
	s = jwtRe.ReplaceAllString(s, "[redacted-jwt]")
	return secretRe.ReplaceAllString(maskEmail(s), "$1$2$3[redacted]")
}

// SyncNow asks the sync engine to sync an account right away (also cuts a
// retry backoff after an error short).
func (a *App) SyncNow(ctx context.Context, accountID int64) error {
	if _, err := a.st.GetAccount(ctx, accountID); err != nil {
		return err
	}
	if a.mgr != nil {
		a.mgr.SyncNow(accountID)
	}
	return nil
}
