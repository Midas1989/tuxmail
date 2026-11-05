// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Distributed-guessing defence (NA-06); the design is described at the top of
// login.go.
const (
	// throttlePressureFails failures of one login name within throttleWindow,
	// from any networks, switch the account into "under pressure" mode.
	throttlePressureFails = 50
	// knownNetworkTTL is how long a successful login keeps its network known.
	knownNetworkTTL = 90 * 24 * time.Hour
	// maxKnownNetworks bounds the rows kept per user (oldest are dropped).
	maxKnownNetworks = 100
	// pressureQueueMax is how many logins may wait for the serialized
	// verification of one account; more are refused at once.
	pressureQueueMax = 8
	// DefaultPressureDelay is the pause before each password verification of
	// an account under pressure.
	DefaultPressureDelay = time.Second

	pressureStripes = 64
)

// pressureGate serialises the password verifications of the accounts hashing
// to one stripe.
type pressureGate struct {
	sem     chan struct{}
	waiters atomic.Int32
}

func newPressureGates() *[pressureStripes]pressureGate {
	var g [pressureStripes]pressureGate
	for i := range g {
		g[i].sem = make(chan struct{}, 1)
	}
	return &g
}

// underPressure reports whether the account behind the user-wide counter is
// currently attacked from many networks, and until when the signal stays up
// without further failures.
func (s *Service) underPressure(ctx context.Context, userKey string) (bool, time.Time) {
	var fails int
	var last int64
	if err := s.db.QueryRowContext(ctx, `SELECT failures, first_failure FROM login_throttle WHERE key = ?`, userKey).Scan(&fails, &last); err != nil {
		return false, time.Time{}
	}
	end := time.Unix(last, 0).Add(throttleWindow)
	if fails >= throttlePressureFails && s.now().Before(end) {
		return true, end.UTC()
	}
	return false, time.Time{}
}

// enterPressureGate waits for the turn of the account (one verification at a
// time, each preceded by PressureDelay). The release function must be called
// when the attempt is complete. A full queue is refused immediately.
func (s *Service) enterPressureGate(ctx context.Context, userKey string) (func(), error) {
	sum := sha256.Sum256([]byte(userKey))
	g := &s.gates[int(sum[0])%pressureStripes]
	if g.waiters.Add(1) > pressureQueueMax {
		g.waiters.Add(-1)
		return nil, &ThrottleError{Until: s.now().Add(throttleBaseLock).UTC()}
	}
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		g.waiters.Add(-1)
		return nil, ctx.Err()
	}
	release := func() {
		<-g.sem
		g.waiters.Add(-1)
	}
	if d := s.PressureDelay; d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

// knownPrefix is the network granularity of the "known networks" list: IPv4
// /24, IPv6 /64.
func knownPrefix(ip string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	bits := 24
	if a.Is6() {
		bits = 64
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return "", false
	}
	return p.String(), true
}

// netHash is the keyed hash under which a network is stored for a user: the
// database never contains addresses, and a leaked table cannot be matched
// against address lists without the server key.
func (s *Service) netHash(userID int64, prefix string) []byte {
	m := hmac.New(sha256.New, s.master)
	m.Write([]byte("tuxmail known network|" + strconv.FormatInt(userID, 10) + "|" + prefix))
	return m.Sum(nil)[:16]
}

// recordKnownNetwork remembers the network of a successful login.
func (s *Service) recordKnownNetwork(ctx context.Context, userID int64, ip string) {
	prefix, ok := knownPrefix(ip)
	if !ok {
		return
	}
	ctx = context.WithoutCancel(ctx)
	now := s.now().Unix()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO login_known_networks(user_id, net_hash, first_seen, last_seen) VALUES (?,?,?,?)
		ON CONFLICT(user_id, net_hash) DO UPDATE SET last_seen = excluded.last_seen`,
		userID, s.netHash(userID, prefix), now, now); err != nil {
		return
	}
	s.db.ExecContext(ctx, `DELETE FROM login_known_networks WHERE user_id = ? AND net_hash NOT IN
		(SELECT net_hash FROM login_known_networks WHERE user_id = ? ORDER BY last_seen DESC LIMIT ?)`,
		userID, userID, maxKnownNetworks)
}

// networkKnown reports whether the user logged in from the network of ip
// within knownNetworkTTL. Networks of the user's live sessions count too (so
// the list works right after an upgrade, before anybody logged in again).
func (s *Service) networkKnown(ctx context.Context, userID int64, ip string) bool {
	prefix, ok := knownPrefix(ip)
	if !ok {
		return false
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_known_networks WHERE user_id = ? AND net_hash = ? AND last_seen > ?`,
		userID, s.netHash(userID, prefix), s.now().Add(-knownNetworkTTL).Unix()).Scan(&n)
	if err == nil && n > 0 {
		return true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ip FROM sessions WHERE user_id = ? AND expires_at > ?`, userID, s.now().Unix())
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var sip string
		if rows.Scan(&sip) == nil {
			if p, ok := knownPrefix(sip); ok && p == prefix {
				return true
			}
		}
	}
	return false
}
