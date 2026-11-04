// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Per-account overrides of the sync configuration, stored in
// domain.Account.Extra and edited in the account settings ("Konto
// bearbeiten", internal/app/account_edit.go). The worker reads them when it
// starts; changing them restarts the worker (Manager.ReloadAccount).
const (
	// ExtraBodyPolicy overrides Config.Bodies: recent | all | on_demand.
	ExtraBodyPolicy = "bodyPolicy"
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

// bodyPolicy is the body download policy of the worker's account.
func (w *Worker) bodyPolicy() BodyPolicy {
	if p := BodyPolicy(w.extra(ExtraBodyPolicy)); ValidBodyPolicy(p) {
		return p
	}
	return w.m.cfg.Bodies
}

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
