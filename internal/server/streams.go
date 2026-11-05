// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/server/api"
)

// Event streams (GET /api/events, WebSocket). A stream authenticates once at
// the handshake, so it has to notice later that its session or device token
// ended (NH-08), and one user must not hold an unbounded number of them
// (NA-08).
//
// Policy: at most maxStreamsPerUser concurrent streams per user; further
// connections are REFUSED with 429 too_many_streams (the oldest are not
// closed: a client that reconnects in a loop would otherwise evict its own
// other tabs in turn). A stream is closed with the policy-violation code 1008
//   - at once when the session is revoked through the HTTP API (logout, session
//     revocation, password change, administrator actions: streams.revoke*), and
//   - at the latest after api.Options.RevalidateEvery (30 s) for every other
//     way a session can end (expiry, user disabled or deleted, cleanup).
const maxStreamsPerUser = 20

type streamEntry struct {
	userID, sessionID int64
	revoked           chan struct{}
	once              sync.Once
}

func (e *streamEntry) revoke() { e.once.Do(func() { close(e.revoked) }) }

type streamHub struct {
	mu  sync.Mutex
	max int // 0 = maxStreamsPerUser
	m   map[int64]map[*streamEntry]struct{}
}

func (h *streamHub) limit() int {
	if h.max > 0 {
		return h.max
	}
	return maxStreamsPerUser
}

// open registers a stream; nil if the user already has the maximum.
func (h *streamHub) open(userID, sessionID int64) *streamEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[int64]map[*streamEntry]struct{}{}
	}
	if len(h.m[userID]) >= h.limit() {
		return nil
	}
	e := &streamEntry{userID: userID, sessionID: sessionID, revoked: make(chan struct{})}
	if h.m[userID] == nil {
		h.m[userID] = map[*streamEntry]struct{}{}
	}
	h.m[userID][e] = struct{}{}
	return e
}

func (h *streamHub) release(e *streamEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.m[e.userID], e)
	if len(h.m[e.userID]) == 0 {
		delete(h.m, e.userID)
	}
}

func (h *streamHub) revokeMatching(userID int64, match func(*streamEntry) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for e := range h.m[userID] {
		if match(e) {
			e.revoke()
		}
	}
}

// revokeSession closes the streams opened with one session.
func (h *streamHub) revokeSession(userID, sessionID int64) {
	h.revokeMatching(userID, func(e *streamEntry) bool { return e.sessionID == sessionID })
}

// revokeUser closes every stream of the user except those of keepSessionID
// (0 = none kept).
func (h *streamHub) revokeUser(userID, keepSessionID int64) {
	h.revokeMatching(userID, func(e *streamEntry) bool { return keepSessionID == 0 || e.sessionID != keepSessionID })
}

// openEventStream is api.Options.OpenStream.
func (s *Server) openEventStream(w http.ResponseWriter, r *http.Request) *api.StreamControl {
	a := authFrom(r)
	if a == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
		return nil
	}
	e := s.streams.open(a.User.ID, a.Session.ID)
	if e == nil {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "too many open event connections for this account")
		return nil
	}
	return &api.StreamControl{
		Valid: func(ctx context.Context) bool {
			err := s.auth.CheckSession(ctx, e.userID, e.sessionID)
			if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrUserDisabled) {
				return false
			}
			if err != nil { // transient database error: keep the stream, check again later
				s.log.Warn("event stream session check", "err", err)
			}
			return true
		},
		Revoked: e.revoked,
		Done:    func() { s.streams.release(e) },
	}
}
