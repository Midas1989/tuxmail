// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Idempotency-Key for POST routes that cause an effect outside the system
// (spec guidelines 6). The desktop client keeps a mail written offline in its
// outbox and may repeat the request when an answer got lost; the key stored
// with the user's data makes the repeat return the first answer.
//
//   - first use: the request runs to its end, also if the client disconnects;
//   - key known and finished: the stored answer, marked Idempotent-Replayed;
//   - key known and still running: 409 idempotency_in_progress (try later);
//   - a run that died midway (crash) or failed with a server error (a mail
//     relay may have taken the mail and then lost the connection): 409
//     idempotency_in_doubt, because nobody knows whether the mail went out.
//     Only the user may decide to send again;
//   - refused (4xx), nothing was accepted: the key is free again;
//   - same key, other request: 422 idempotency_key_reused;
//   - without the header: nothing changes.
const (
	idempotencyRetain = 30 * 24 * time.Hour
	// idempotencyStale: longer than any send can take.
	idempotencyStale = 10 * time.Minute
)

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type idemRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *idemRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *idemRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

func idempotent(route string, next AppHandler) AppHandler {
	return func(w http.ResponseWriter, r *http.Request, a *app.App) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r, a)
			return
		}
		if !idempotencyKeyRe.MatchString(key) {
			badRequest(w, "invalid Idempotency-Key")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			writeErr(w, err)
			return
		}
		sum := sha256.Sum256(body)
		// Survives the client going away: otherwise the mail could leave
		// without the answer being recorded, the very case this is for.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), idempotencyStale)
		defer cancel()
		st := a.Store()
		now := time.Now()
		rec, err := st.BeginIdempotency(ctx, key, route+" "+r.URL.Path, hex.EncodeToString(sum[:]), now, idempotencyRetain)
		if err != nil {
			writeErr(w, err)
			return
		}
		switch rec.State {
		case store.IdempotencyDone:
			w.Header().Set("Idempotent-Replayed", "true")
			if rec.ContentType != "" {
				w.Header().Set("Content-Type", rec.ContentType)
			}
			w.WriteHeader(rec.Status)
			_, _ = w.Write(rec.Body)
			return
		case store.IdempotencyMismatch:
			writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: "idempotency_key_reused", Message: "this key was used for a different request"})
			return
		case store.IdempotencyDoubt:
			writeJSON(w, http.StatusConflict, errorBody{Error: "idempotency_in_doubt", Message: "an earlier attempt failed; it may have been sent"})
			return
		case store.IdempotencyPending:
			if now.Sub(rec.CreatedAt) > idempotencyStale {
				writeJSON(w, http.StatusConflict, errorBody{Error: "idempotency_in_doubt", Message: "an earlier attempt ended unexpectedly; it may have been sent"})
				return
			}
			w.Header().Set("Retry-After", "10")
			writeJSON(w, http.StatusConflict, errorBody{Error: "idempotency_in_progress", Message: "an earlier attempt is still running"})
			return
		}
		rw := &idemRecorder{ResponseWriter: w}
		r2 := r.WithContext(ctx)
		r2.Body = io.NopCloser(bytes.NewReader(body))
		next(rw, r2, a)
		switch {
		case rw.status/100 == 2:
			err = st.FinishIdempotency(ctx, key, rw.status, rw.Header().Get("Content-Type"), rw.buf.Bytes())
		case rw.status >= 500 && rw.status != http.StatusNotImplemented:
			err = st.DoubtIdempotency(ctx, key)
		default:
			err = st.ReleaseIdempotency(ctx, key) // refused: nothing happened, a retry may run
		}
		if err != nil {
			slog.Error("api: recording an idempotency key failed", "err", err)
		}
	}
}

// idempotencyStatus answers GET /api/idempotency/{key}: what became of a key
// (done, pending, doubt, unknown) - the same state a POST with this key would
// meet, a run that died midway included (doubt). A client whose mail ended
// unclear asks this before it decides between repeating the same key and a
// new one; the answer carries no content. Limited per user like other status
// queries.
func idempotencyStatus(w http.ResponseWriter, r *http.Request, a *app.App) {
	if wait, ok := idemStatusLimit.allow(fmt.Sprintf("%p", a.Store())); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: "rate_limited", Message: "too many status queries, try again later"})
		return
	}
	key := r.PathValue("key")
	if !idempotencyKeyRe.MatchString(key) {
		badRequest(w, "invalid Idempotency-Key")
		return
	}
	state, err := a.Store().IdempotencyStatus(r.Context(), key, time.Now(), idempotencyStale)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state})
}

// idemStatusPerMinute: an outbox asks once per unclear mail and decision; 30 a minute is plenty.
const (
	idemStatusPerMinute = 30
	idemStatusBurst     = 10
)

var idemStatusLimit = &statusLimiter{rate: idemStatusPerMinute / 60.0, burst: idemStatusBurst, buckets: map[string]*statusBucket{}}

type statusBucket struct {
	tokens float64
	last   time.Time
}

// statusLimiter is a token bucket per user (key: the user's store).
type statusLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*statusBucket
	now     func() time.Time
}

func (l *statusLimiter) allow(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 10000 {
			l.buckets = map[string]*statusBucket{}
		}
		b = &statusBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / l.rate * float64(time.Second)), false
}
