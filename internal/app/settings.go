// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// ==== Preferences ==============================================================

const keyPreferences = "preferences"

// DefaultPreferences follow the system/browser.
func DefaultPreferences() UserPreferences {
	return UserPreferences{Locale: "system", Region: RegionSettings{TimeZone: "system", DateFormat: "locale", TimeFormat: "locale",
		WeekStart: json.RawMessage(`"locale"`), NumberFormat: "locale", RelativeTimes: true}}
}

// GetPreferences returns the user preferences.
func (a *App) GetPreferences(ctx context.Context) (UserPreferences, error) {
	p := DefaultPreferences()
	if _, err := a.st.GetAppSetting(ctx, keyPreferences, &p); err != nil {
		return p, err
	}
	if len(p.Region.WeekStart) == 0 {
		p.Region.WeekStart = json.RawMessage(`"locale"`)
	}
	if at, err := a.st.AppSettingTime(ctx, keyPreferences); err == nil && !at.IsZero() {
		p.UpdatedAt = at.Unix()
	}
	return p, nil
}

// SetPreferences stores the user preferences.
func (a *App) SetPreferences(ctx context.Context, p UserPreferences) (UserPreferences, error) {
	p.UpdatedAt = 0 // computed from the stored document
	if p.Locale == "" {
		p.Locale = "system"
	}
	if len(p.Region.WeekStart) == 0 {
		p.Region.WeekStart = json.RawMessage(`"locale"`)
	}
	var ws any
	if err := json.Unmarshal(p.Region.WeekStart, &ws); err != nil {
		return UserPreferences{}, invalid("invalid weekStart")
	}
	switch v := ws.(type) {
	case string:
		if v != "locale" {
			return UserPreferences{}, invalid("invalid weekStart")
		}
	case float64:
		if v != 0 && v != 1 && v != 6 {
			return UserPreferences{}, invalid("invalid weekStart")
		}
	default:
		return UserPreferences{}, invalid("invalid weekStart")
	}
	if len(p.View) == 0 { // web and Android do not know the desktop's view settings: keep them
		old := UserPreferences{}
		if _, err := a.st.GetAppSetting(ctx, keyPreferences, &old); err != nil {
			return UserPreferences{}, err
		}
		p.View = old.View
	}
	if err := a.st.SetAppSetting(ctx, keyPreferences, p); err != nil {
		return UserPreferences{}, err
	}
	return p, nil
}

// ==== Locales ==================================================================

var localeCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func (a *App) readLocale(code string) (map[string]any, bool, error) {
	var base map[string]any
	found := false
	if a.opts.Locales != nil {
		if b, err := fs.ReadFile(a.opts.Locales, code+".json"); err == nil {
			if err := json.Unmarshal(b, &base); err != nil {
				return nil, false, err
			}
			found = true
		}
	}
	if a.opts.LocaleDir != "" {
		if b, err := os.ReadFile(filepath.Join(a.opts.LocaleDir, code+".json")); err == nil {
			var over map[string]any
			if err := json.Unmarshal(b, &over); err != nil {
				return nil, false, invalid("locale override %s.json is not valid JSON: %v", code, err)
			}
			if base == nil {
				base = map[string]any{}
			}
			mergeJSON(base, over)
			found = true
		}
	}
	return base, found, nil
}

// mergeJSON deep-merges src into dst (override files change single keys).
func mergeJSON(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeJSON(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func localeCodes(fsys fs.FS) []string {
	var out []string
	if fsys == nil {
		return out
	}
	ents, _ := fs.ReadDir(fsys, ".")
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".json") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	return out
}

// ListLocales returns the available languages (built-in and data dir).
func (a *App) ListLocales(ctx context.Context) ([]LocaleInfo, error) {
	codes := map[string]bool{}
	for _, c := range localeCodes(a.opts.Locales) {
		codes[c] = true
	}
	if a.opts.LocaleDir != "" {
		for _, c := range localeCodes(os.DirFS(a.opts.LocaleDir)) {
			codes[c] = true
		}
	}
	out := []LocaleInfo{}
	for c := range codes {
		if !localeCode.MatchString(c) {
			continue
		}
		m, ok, err := a.readLocale(c)
		if err != nil || !ok {
			continue
		}
		info := LocaleInfo{Code: c, Name: c, NativeName: c, Direction: "ltr"}
		if meta, ok := m["meta"].(map[string]any); ok {
			if v, ok := meta["name"].(string); ok {
				info.Name = v
			}
			if v, ok := meta["nativeName"].(string); ok {
				info.NativeName = v
			}
			if v, ok := meta["direction"].(string); ok && v == "rtl" {
				info.Direction = "rtl"
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// GetLocale returns a language file (nested JSON) with data dir overrides.
func (a *App) GetLocale(ctx context.Context, code string) (map[string]any, error) {
	if !localeCode.MatchString(code) {
		return nil, invalid("invalid locale code")
	}
	m, ok, err := a.readLocale(code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotFound
	}
	return m, nil
}

// ==== Notifications and Web Push ===============================================

const keyNotifications = "notifications"

// DefaultNotificationSettings shows sender and preview, no quiet hours.
func DefaultNotificationSettings() NotificationSettings {
	return NotificationSettings{Privacy: "full", QuietHours: QuietHours{Start: "22:00", End: "07:00"},
		Accounts: map[string]bool{}, Channels: map[string]bool{"email": true, "whatsapp": true, "signal": true, "telegram": true}}
}

func noPush() *Error {
	return Errorf(http.StatusNotImplemented, CodeNotSupported, "Web Push is only available in server mode; the desktop uses native notifications")
}

// GetPushVapidKey returns the VAPID public key (server mode).
func (a *App) GetPushVapidKey(context.Context) (string, error) {
	if a.opts.Push == nil {
		return "", noPush()
	}
	return a.opts.Push.VAPIDPublicKey(), nil
}

// SubscribePush registers a Web Push subscription (server mode).
func (a *App) SubscribePush(ctx context.Context, s PushSubscriptionInput) error {
	if a.opts.Push == nil {
		return noPush()
	}
	return a.opts.Push.Subscribe(ctx, s)
}

// UnsubscribePush removes a Web Push subscription (server mode).
func (a *App) UnsubscribePush(ctx context.Context, endpoint string) error {
	if a.opts.Push == nil {
		return noPush()
	}
	return a.opts.Push.Unsubscribe(ctx, endpoint)
}

// SendTestNotification sends a test push (server mode) or shows a native
// operating system notification (desktop).
func (a *App) SendTestNotification(ctx context.Context) error {
	if a.opts.Push == nil {
		if a.opts.Mode == ModeDesktop {
			return a.notifyDesktop(ctx, "TuxMail", "Test-Benachrichtigung: Benachrichtigungen funktionieren.")
		}
		return noPush()
	}
	return a.opts.Push.Test(ctx)
}

// GetNotificationSettings returns the notification settings.
func (a *App) GetNotificationSettings(ctx context.Context) (NotificationSettings, error) {
	if a.opts.Push != nil {
		return a.opts.Push.Settings(ctx)
	}
	s := DefaultNotificationSettings()
	_, err := a.st.GetAppSetting(ctx, keyNotifications, &s)
	return normalizeNotifications(s), err
}

func normalizeNotifications(s NotificationSettings) NotificationSettings {
	if s.Privacy != "generic" {
		s.Privacy = "full"
	}
	if s.Accounts == nil {
		s.Accounts = map[string]bool{}
	}
	if s.Channels == nil {
		s.Channels = map[string]bool{}
	}
	return s
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// SaveNotificationSettings stores the notification settings.
func (a *App) SaveNotificationSettings(ctx context.Context, s NotificationSettings) (NotificationSettings, error) {
	s = normalizeNotifications(s)
	if s.QuietHours.Enabled && (!hhmm.MatchString(s.QuietHours.Start) || !hhmm.MatchString(s.QuietHours.End)) {
		return NotificationSettings{}, invalid("quiet hours must be HH:MM")
	}
	if a.opts.Push != nil {
		return a.opts.Push.SaveSettings(ctx, s)
	}
	if err := a.st.SetAppSetting(ctx, keyNotifications, s); err != nil {
		return NotificationSettings{}, err
	}
	return s, nil
}

// ==== Secret vault (portable desktop) ==========================================

// VaultControl is the master-password vault of the portable desktop mode
// (KONZEPT 15.2, 17.2).
type VaultControl interface {
	Status() VaultStatus
	// Unlock opens the vault (creating it with this password on first use).
	Unlock(password []byte) error
}

// VaultStatus reports the secret store state.
func (a *App) VaultStatus(context.Context) (VaultStatus, error) {
	if a.opts.Vault != nil {
		return a.opts.Vault.Status(), nil
	}
	if a.opts.Mode == ModeServer {
		return VaultStatus{Kind: "server", Initialized: true}, nil
	}
	return VaultStatus{Kind: "keyring", Initialized: true}, nil
}

// MinVaultPasswordLen is the minimum length of a new master password.
const MinVaultPasswordLen = 8

// UnlockVault unlocks the portable vault with the master password.
func (a *App) UnlockVault(ctx context.Context, password string) error {
	if a.opts.Vault == nil {
		return nil // keyring / server: nothing to unlock
	}
	if password == "" {
		return invalid("password required")
	}
	// A new vault (portable mode, e.g. on a USB stick that can be copied
	// and attacked offline) needs a master password of at least
	// MinVaultPasswordLen characters; the UI checks the same (AUDIT-C).
	if !a.opts.Vault.Status().Initialized && utf8.RuneCountInString(password) < MinVaultPasswordLen {
		return invalid("the master password must have at least %d characters", MinVaultPasswordLen)
	}
	pw := []byte(password)
	defer clear(pw)
	if err := a.opts.Vault.Unlock(pw); err != nil {
		return err
	}
	for _, acc := range mustAccounts(ctx, a) {
		if err := a.sync.AddAccount(ctx, acc); err != nil {
			a.log.Debug("app: restarting account after unlock", "account", acc, "err", err)
		}
	}
	return nil
}

func mustAccounts(ctx context.Context, a *App) []int64 {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(accs))
	for _, acc := range accs {
		ids = append(ids, acc.ID)
	}
	return ids
}
