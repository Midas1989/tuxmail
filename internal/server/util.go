// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}

// writeAuthError maps service errors to HTTP responses.
func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	var te *auth.ThrottleError
	switch {
	case errors.As(err, &te):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(te.Until).Seconds()))))
		writeError(w, http.StatusTooManyRequests, "throttled", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case errors.Is(err, auth.ErrUserDisabled):
		writeError(w, http.StatusForbidden, "user_disabled", err.Error())
	case errors.Is(err, auth.ErrQuota):
		writeError(w, http.StatusRequestEntityTooLarge, "quota_exceeded", err.Error())
	case errors.Is(err, auth.ErrReauthRequired):
		writeError(w, http.StatusForbidden, "reauth_required", err.Error())
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, auth.ErrExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", err.Error())
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusBadRequest, "invalid_code", err.Error())
	case errors.Is(err, auth.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, auth.ErrRegistration):
		writeError(w, http.StatusForbidden, "registration_disabled", err.Error())
	case errors.Is(err, auth.ErrOIDCDisabled):
		writeError(w, http.StatusNotFound, "oidc_disabled", err.Error())
	case errors.Is(err, auth.ErrOIDCDenied):
		writeError(w, http.StatusForbidden, "oidc_denied", err.Error())
	default:
		s.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

const maxJSONBody = 1 << 20

// readJSON decodes a JSON request body (max 1 MiB) into v.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "expected application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid "+name)
		return 0, false
	}
	return id, true
}

// rateLimiter is a token bucket per key (in memory, per process).
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute, burst float64) *rateLimiter {
	return &rateLimiter{rate: perMinute / 60, burst: max(burst, 1), buckets: map[string]*bucket{}, now: time.Now}
}

// allow takes a token for key; if none is available it returns the wait time.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 100000 {
			l.pruneLocked(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func (l *rateLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(l.now())
}

// pruneLocked drops buckets that have refilled completely.
func (l *rateLimiter) pruneLocked(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// limiterKey groups IPv6 clients by /64: a single host usually controls a
// whole /64, so per-address buckets would not limit it at all (A-06).
func limiterKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is6() || a.Is4In6() {
		return ip
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// limitLogin applies the per-IP and per-IP+username login rate limits and
// writes a 429 response if exceeded.
func (s *Server) limitLogin(w http.ResponseWriter, r *http.Request, username string) bool {
	ip := limiterKey(clientFrom(r).IP)
	ok, wait := s.ipLimiter.allow(ip)
	if ok {
		ok, wait = s.userLimiter.allow(ip + "|" + strings.ToLower(strings.TrimSpace(username)))
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts, try again later")
		return false
	}
	return true
}
