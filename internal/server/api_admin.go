// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request, a *authInfo) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if users == nil {
		users = []*auth.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Username    string    `json:"username"`
		Email       string    `json:"email"`
		DisplayName string    `json:"displayName"`
		Role        auth.Role `json:"role"`
		Password    string    `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	generated := ""
	if req.Password == "" {
		generated = auth.GeneratePassword(max(s.cfg.Auth.PasswordMinLength, 20))
		req.Password = generated
	}
	u, err := s.auth.CreateUser(r.Context(), auth.NewUser{
		Username: req.Username, Email: req.Email, DisplayName: req.DisplayName, Role: req.Role, Password: req.Password,
	})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	resp := map[string]any{"user": u}
	if generated != "" {
		resp["password"] = generated
	}
	writeJSON(w, http.StatusCreated, resp)
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	groups, _ := s.auth.UserGroupIDs(r.Context(), id)
	if groups == nil {
		groups = []int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "groups": groups, "storageBytes": dirSize(s.auth.UserDir(id))})
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		auth.UserUpdate
		Role     *auth.Role `json:"role"`
		Disabled *bool      `json:"disabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Email != nil || req.DisplayName != nil {
		if _, err := s.auth.UpdateUser(ctx, id, req.UserUpdate); err != nil {
			s.writeAuthError(w, err)
			return
		}
	}
	if req.Role != nil {
		if err := s.auth.SetRole(ctx, id, *req.Role); err != nil {
			s.writeAuthError(w, err)
			return
		}
	}
	if req.Disabled != nil {
		if err := s.auth.SetDisabled(ctx, id, *req.Disabled); err != nil {
			s.writeAuthError(w, err)
			return
		}
		if *req.Disabled {
			s.streams.revokeUser(id, 0)
		}
	}
	u, err := s.auth.GetUser(ctx, id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	keep := r.URL.Query().Get("keepData") == "true"
	if _, err := s.auth.GetUser(r.Context(), id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Stop the user's App (sync, timers, open store) before the account and
	// its data go away; a cached App must never outlive its user (A-01).
	s.apps.drop(id)
	if err := s.auth.DeleteUser(r.Context(), id, !keep); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.apps.drop(id) // a request may have reopened it meanwhile
	s.streams.revokeUser(id, 0)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Password != "" {
		if err := s.auth.SetPassword(r.Context(), id, req.Password, 0); err != nil {
			s.writeAuthError(w, err)
			return
		}
		s.streams.revokeUser(id, 0)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	pw, err := s.auth.ResetPassword(r.Context(), id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeUser(id, 0)
	writeJSON(w, http.StatusOK, map[string]string{"password": pw})
}

func (s *Server) handleAdminReset2FA(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.auth.ResetTOTP(r.Context(), id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminUnlock(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.auth.UnlockLogin(r.Context(), u.Username)
	s.auth.Audit(r.Context(), "user.unlock", u.Username, true, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	list, err := s.auth.ListSessions(r.Context(), id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessionViews(list, a.Session.ID)})
}

func (s *Server) handleAdminRevokeSessions(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.auth.RevokeAllSessions(r.Context(), id, 0); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeUser(id, 0)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminRevokeSession(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	sid, ok := pathID(w, r, "sid")
	if !ok {
		return
	}
	if err := s.auth.RevokeSession(r.Context(), id, sid); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeSession(id, sid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminListGroups(w http.ResponseWriter, r *http.Request, a *authInfo) {
	gs, err := s.auth.ListGroups(r.Context())
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": gs})
}

func (s *Server) handleAdminCreateGroup(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	g, err := s.auth.CreateGroup(r.Context(), req.Name, req.Description)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"group": g})
}

func (s *Server) handleAdminDeleteGroup(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.auth.DeleteGroup(r.Context(), id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminAddMember(w http.ResponseWriter, r *http.Request, a *authInfo) {
	gid, ok1 := pathID(w, r, "id")
	if !ok1 {
		return
	}
	uid, ok2 := pathID(w, r, "uid")
	if !ok2 {
		return
	}
	if err := s.auth.AddGroupMember(r.Context(), gid, uid); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminRemoveMember(w http.ResponseWriter, r *http.Request, a *authInfo) {
	gid, ok1 := pathID(w, r, "id")
	if !ok1 {
		return
	}
	uid, ok2 := pathID(w, r, "uid")
	if !ok2 {
		return
	}
	if err := s.auth.RemoveGroupMember(r.Context(), gid, uid); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminListInvites(w http.ResponseWriter, r *http.Request, a *authInfo) {
	list, err := s.auth.ListInvites(r.Context())
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": list})
}

func (s *Server) handleAdminCreateInvite(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Email          string    `json:"email"`
		Role           auth.Role `json:"role"`
		ExpiresInHours int       `json:"expiresInHours"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if s.cfg.Auth.Registration != "invite" {
		writeError(w, http.StatusConflict, "registration_disabled", "set auth.registration to invite to use invite links")
		return
	}
	inv, tok, err := s.auth.CreateInvite(r.Context(), req.Email, req.Role, time.Duration(req.ExpiresInHours)*time.Hour)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": inv, "token": tok, "url": s.cfg.PublicURLJoin("/register?invite=" + tok)})
}

func (s *Server) handleAdminDeleteInvite(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.auth.DeleteInvite(r.Context(), id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request, a *authInfo) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.auth.ListAudit(r.Context(), limit, before)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if list == nil {
		list = []auth.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": list})
}

type templateReq struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	GroupID     int64           `json:"groupId"`
	Body        json.RawMessage `json:"body"`
}

func (s *Server) handleAdminListTemplates(w http.ResponseWriter, r *http.Request, a *authInfo) {
	list, err := s.auth.ListTemplates(r.Context())
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

func (s *Server) handleAdminCreateTemplate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req templateReq
	if !readJSON(w, r, &req) {
		return
	}
	t, err := s.auth.CreateTemplate(r.Context(), auth.Template{Name: req.Name, Description: req.Description, GroupID: req.GroupID, Body: req.Body})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"template": t})
}

func (s *Server) handleAdminGetTemplate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	t, err := s.auth.GetTemplate(r.Context(), id)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": t})
}

func (s *Server) handleAdminUpdateTemplate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req templateReq
	if !readJSON(w, r, &req) {
		return
	}
	t, err := s.auth.UpdateTemplate(r.Context(), auth.Template{ID: id, Name: req.Name, Description: req.Description, GroupID: req.GroupID, Body: req.Body})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": t})
}

func (s *Server) handleAdminDeleteTemplate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.auth.DeleteTemplate(r.Context(), id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
