// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"

	"github.com/Midas1989/Mailclient/internal/server/push"
)

func (s *Server) handleVAPIDKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.push.Keys.PublicKey})
}

// subscribeReq mirrors PushSubscription.toJSON() plus a device name.
type subscribeReq struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	DeviceName string `json:"deviceName"`
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req subscribeReq
	if !readJSON(w, r, &req) {
		return
	}
	sub := push.Subscription{
		UserID: a.User.ID, SessionID: a.Session.ID, Endpoint: req.Endpoint,
		P256dh: req.Keys.P256dh, Auth: req.Keys.Auth, DeviceName: req.DeviceName, UserAgent: r.UserAgent(),
	}
	if err := push.ValidateSubscription(sub); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_subscription", err.Error())
		return
	}
	id, err := s.pushStore.Save(r.Context(), sub)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.pushStore.Delete(r.Context(), a.User.ID, req.Endpoint); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushList(w http.ResponseWriter, r *http.Request, a *authInfo) {
	subs, err := s.pushStore.List(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (s *Server) handlePushGetPrefs(w http.ResponseWriter, r *http.Request, a *authInfo) {
	p, err := s.pushStore.Prefs(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePushPutPrefs(w http.ResponseWriter, r *http.Request, a *authInfo) {
	p := push.DefaultPrefs()
	if !readJSON(w, r, &p) {
		return
	}
	if err := s.pushStore.SetPrefs(r.Context(), a.User.ID, p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePushTest sends a test notification to all of the user's devices,
// ignoring quiet hours and toggles.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request, a *authInfo) {
	subs, err := s.pushStore.List(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	n := push.Notification{Payload: push.Payload{Type: "system", Title: "TuxMail", Body: "Test-Benachrichtigung", URL: "/", Tag: "test"},
		Urgency: push.UrgencyNormal}
	sent, failed := 0, 0
	for _, sub := range subs {
		if err := s.push.SendTo(r.Context(), sub, n); err != nil {
			s.log.Warn("test push failed", "err", err)
			failed++
		} else {
			sent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent, "failed": failed})
}
