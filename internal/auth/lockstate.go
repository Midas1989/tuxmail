// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strings"
	"time"
)

// LockState is what an administrator sees of the login throttling of an
// account (see the top of login.go): the hard locks of attacking networks and
// the soft "under pressure" signal of distributed guessing.
type LockState struct {
	// Locked is set while at least one network is locked out for this login
	// name; LockedUntil is the end of the longest running lock. The owner on
	// another network is not affected by such a lock.
	Locked      bool
	LockedUntil time.Time
	// UnderPressure is set while the user-wide failure counter says the
	// account is attacked from many networks (only known networks may log in);
	// PressureUntil is when the signal ends without further failures.
	UnderPressure bool
	PressureUntil time.Time
}

// Active reports whether anything is currently throttled for the account, so
// that "unlock" has an effect.
func (l LockState) Active() bool { return l.Locked || l.UnderPressure }

// LockStates returns the current throttling state of the given usernames
// (admin overview). Only accounts with an active lock or pressure appear in
// the result, keyed by the name as passed in. It reads the login_throttle rows
// of the "u:" namespace with a single indexed range scan; step-up counters
// ("s:<id>") are not part of the login lock and are ignored.
func (s *Service) LockStates(ctx context.Context, usernames []string) (map[string]LockState, error) {
	out := map[string]LockState{}
	if len(usernames) == 0 {
		return out, nil
	}
	// Throttle keys use the lower-cased login name (throttleKey).
	byKey := make(map[string][]string, len(usernames))
	for _, n := range usernames {
		k := strings.ToLower(strings.TrimSpace(n))
		byKey[k] = append(byKey[k], n)
	}
	now := s.now()
	// Keys are "u:<name>" (user-wide) and "u:<name>|<network>"; ';' sorts
	// right after ':' so this is exactly the "u:" prefix.
	rows, err := s.db.QueryContext(ctx, `SELECT key, failures, first_failure, locked_until FROM login_throttle
		WHERE key >= 'u:' AND key < 'u;' AND (locked_until > ?1 OR failures >= ?2)`, now.Unix(), throttlePressureFails)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	state := map[string]LockState{}
	for rows.Next() {
		var key string
		var fails int
		var last, until int64
		if err := rows.Scan(&key, &fails, &last, &until); err != nil {
			return nil, err
		}
		rest := strings.TrimPrefix(key, "u:")
		if _, ok := byKey[rest]; ok {
			// User-wide counter. Real usernames cannot contain "|", so an
			// exact match is never a network key.
			if active, end := pressureWindow(fails, last, now); active {
				st := state[rest]
				st.UnderPressure, st.PressureUntil = true, end
				state[rest] = st
			}
			continue
		}
		// Network counter: the network is the part after the LAST "|".
		i := strings.LastIndex(rest, "|")
		if i < 0 {
			continue
		}
		name := rest[:i]
		if _, ok := byKey[name]; !ok {
			continue
		}
		if t := time.Unix(until, 0); now.Before(t) {
			st := state[name]
			st.Locked = true
			if t.After(st.LockedUntil) {
				st.LockedUntil = t.UTC()
			}
			state[name] = st
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k, st := range state {
		for _, n := range byKey[k] {
			out[n] = st
		}
	}
	return out, nil
}

// LockStateOf is LockStates for one account.
func (s *Service) LockStateOf(ctx context.Context, username string) (LockState, error) {
	m, err := s.LockStates(ctx, []string{username})
	return m[username], err
}
