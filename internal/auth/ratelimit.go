// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// Login limits in the database (Basis spec/PARAMETER.md), on top of the
// per-account counters of login.go and the in-memory limiter of the HTTP layer:
// they survive a restart and hold across processes. Only failures count, so a
// shared address does not lock itself out by working logins (ADR 0007 no. 9).
type rateRule struct {
	limit  int
	window time.Duration
}

var (
	ipFailLimit   = rateRule{limit: 20, window: 5 * time.Minute}
	totpFailLimit = rateRule{limit: 5, window: 5 * time.Minute}
)

func ipFailBucket(ip string) string { return "ip:" + clientNet(ip) }

func totpBucket(userID int64) string { return "totp:" + strconv.FormatInt(userID, 10) }

func windowStart(now time.Time, r rateRule) int64 {
	w := int64(r.window / time.Second)
	return now.Unix() - now.Unix()%w
}

// checkRate fails with a ThrottleError once the bucket's budget is used up. A
// database error counts as "limited" (ADR 0007 no. 8: fail closed).
func (s *Service) checkRate(ctx context.Context, bucket string, r rateRule) error {
	if bucket == "ip:?" {
		return nil // no client address: command line, unix socket without a proxy header
	}
	now := s.now()
	start := windowStart(now, r)
	var hits int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(hits), 0) FROM rate_limit WHERE bucket = ? AND window_start = ?`, bucket, start).Scan(&hits)
	if err != nil {
		slog.Error("auth: rate limit unavailable", "err", err)
	}
	if err != nil || hits >= r.limit {
		return &ThrottleError{Until: time.Unix(start, 0).Add(r.window).UTC()}
	}
	return nil
}

// hitRate counts one failure.
func (s *Service) hitRate(ctx context.Context, bucket string, r rateRule) {
	if bucket == "ip:?" {
		return
	}
	_, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO rate_limit(bucket, window_start, hits) VALUES (?, ?, 1)
		ON CONFLICT(bucket, window_start) DO UPDATE SET hits = hits + 1`, bucket, windowStart(s.now(), r))
	if err != nil {
		slog.Error("auth: rate limit not recorded", "err", err)
	}
}
