// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Per-account overrides of the sync configuration, stored in
// domain.Account.Extra and edited in the account settings ("Konto
// bearbeiten", internal/app/account_edit.go). The worker reads them when it
// starts; changing them restarts the worker (Manager.ReloadAccount).
const (
	// ExtraBodyPolicy overrides Config.Bodies: recent | all | on_demand.
	ExtraBodyPolicy = "bodyPolicy"
	// ExtraHeadersOnlyDays overrides the global default of
	// mail.headersOnlyOlderThanDays for the account: mails older than this
	// many days are kept with header and preview only ("0" = all mails
	// completely). It takes precedence over ExtraBodyPolicy and is only
	// honoured when Config.HeadersOnlyDays is set (desktop, not the server).
	ExtraHeadersOnlyDays = "headersOnlyOlderThanDays"
	// ExtraPollMinutes overrides Config.PollInterval (whole minutes, > 0).
	ExtraPollMinutes = "pollMinutes"
	// ExtraIdleFolders overrides Config.WatchRoles: "off" (poll only) or a
	// comma-separated list of folder ids watched with IDLE/push.
	ExtraIdleFolders = "idleFolders"
)

// IdleFoldersOff disables IDLE/push watching for an account.
const IdleFoldersOff = "off"

// ValidBodyPolicy reports whether p is a known body policy.
func ValidBodyPolicy(p BodyPolicy) bool {
	return p == BodiesRecent || p == BodiesAll || p == BodiesOnDemand
}

func (w *Worker) extra(key string) string {
	if w.acc.Extra == nil {
		return ""
	}
	return strings.TrimSpace(w.acc.Extra[key])
}

// MaxHeadersOnlyDays bounds mail.headersOnlyOlderThanDays (100 years).
const MaxHeadersOnlyDays = 36500

// ParseHeadersOnlyDays reads a stored day count; ok is false for an empty
// or invalid value.
func ParseHeadersOnlyDays(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 || n > MaxHeadersOnlyDays {
		return 0, false
	}
	return n, true
}

// contentRule says which bodies the worker keeps.
type contentRule struct {
	policy BodyPolicy
	// days is the age limit of BodiesRecent.
	days int
	// release: the rule comes from mail.headersOnlyOlderThanDays set by the
	// user. Only then are previews requested, bodies of older mails released
	// and the server searched; the older body policies (and the default)
	// behave exactly as before.
	release bool
}

// cutoff is the date before which mails are kept with header only (zero:
// none, BodiesAll).
func (r contentRule) cutoff(now time.Time) time.Time {
	switch r.policy {
	case BodiesRecent:
		return now.AddDate(0, 0, -r.days)
	case BodiesOnDemand:
		return now.Add(time.Hour) // every mail already there
	}
	return time.Time{}
}

// headersOnlyKind reports whether bodies of the account can be loaded again
// at any time, so that a mail may be kept with its header only.
func headersOnlyKind(k domain.AccountKind) bool {
	for _, x := range store.HeadersOnlyKinds {
		if string(k) == x {
			return true
		}
	}
	return false
}

// contentRule is the body policy of the worker's account: the account's day
// count, else its (older) body policy, else the global day count of this
// device, else Config.Bodies (the behaviour before the day count existed:
// nothing changes until the user sets it). The day count only applies to
// accounts whose bodies can be loaded again; POP3 and local accounts keep
// their previous behaviour and are never released.
func (w *Worker) contentRule() contentRule {
	legacy := contentRule{policy: w.m.cfg.Bodies, days: w.m.cfg.RecentBodyDays}
	if p := BodyPolicy(w.extra(ExtraBodyPolicy)); ValidBodyPolicy(p) {
		legacy.policy = p
	}
	global := w.m.cfg.HeadersOnlyDays
	if global == nil || !headersOnlyKind(w.acc.Kind) {
		return legacy
	}
	days := func(n int) contentRule {
		if n == 0 {
			return contentRule{policy: BodiesAll}
		}
		return contentRule{policy: BodiesRecent, days: n, release: true}
	}
	if n, ok := ParseHeadersOnlyDays(w.extra(ExtraHeadersOnlyDays)); ok {
		return days(n)
	}
	if ValidBodyPolicy(BodyPolicy(w.extra(ExtraBodyPolicy))) {
		return legacy
	}
	if n := global(); n >= 0 && n <= MaxHeadersOnlyDays {
		return days(n)
	}
	return legacy
}

// bodyPolicy is the body download policy of the worker's account.
func (w *Worker) bodyPolicy() BodyPolicy { return w.contentRule().policy }

// pollInterval is how often the worker syncs every folder.
func (w *Worker) pollInterval() time.Duration {
	if n, err := strconv.Atoi(w.extra(ExtraPollMinutes)); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	return w.m.cfg.PollInterval
}

// watchesFolders reports whether the worker watches any folder.
func (w *Worker) watchesFolders() bool {
	switch w.extra(ExtraIdleFolders) {
	case "":
		return len(w.m.cfg.WatchRoles) > 0
	case IdleFoldersOff:
		return false
	default:
		return true
	}
}

// watchFolder reports whether f is watched with IDLE/push.
func (w *Worker) watchFolder(f domain.Folder) bool {
	switch v := w.extra(ExtraIdleFolders); v {
	case "":
		for _, r := range w.m.cfg.WatchRoles {
			if r == f.Role && r != domain.RoleNone {
				return true
			}
		}
		return false
	case IdleFoldersOff:
		return false
	default:
		for _, s := range strings.Split(v, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && id == f.ID {
				return true
			}
		}
		return false
	}
}
