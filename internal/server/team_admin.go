// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"net/http"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/team"
)

// Administration of team mailboxes (Settings → Team):
//
//	GET    /api/admin/team/mailboxes                     -> [{mailbox, grants, accounts}]   (admin)
//	POST   /api/admin/team/mailboxes                     {mailbox: MailboxInput, account?: NewAccountInput} (admin)
//	PATCH  /api/admin/team/mailboxes/{id}                MailboxInput                         (admin)
//	DELETE /api/admin/team/mailboxes/{id}                                                     (admin)
//	POST   /api/admin/team/mailboxes/{id}/accounts       NewAccountInput -> Account          (admin)
//	GET    /api/team/{teamId}/grants                     -> Grant[]                           (manage)
//	PUT    /api/team/{teamId}/grants                     {userId|groupId, role ("" removes)}  (manage)
//	GET    /api/team/directory                           -> {users, groups} for the grant editor (manage on any mailbox)

func (s *Server) mountTeamAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/team/mailboxes", s.admin(s.handleTeamAdminList))
	mux.HandleFunc("POST /api/admin/team/mailboxes", s.admin(s.handleTeamAdminCreate))
	mux.HandleFunc("PATCH /api/admin/team/mailboxes/{id}", s.admin(s.handleTeamAdminUpdate))
	mux.HandleFunc("DELETE /api/admin/team/mailboxes/{id}", s.admin(s.handleTeamAdminDelete))
	mux.HandleFunc("POST /api/admin/team/mailboxes/{id}/accounts", s.admin(s.handleTeamAdminAddAccount))
	mux.HandleFunc("GET /api/team/{teamId}/grants", s.user(s.handleTeamGrants))
	mux.HandleFunc("PUT /api/team/{teamId}/grants", s.session(s.handleTeamSetGrant))
	mux.HandleFunc("GET /api/team/directory", s.user(s.handleTeamDirectory))
}

func (s *Server) writeTeamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, team.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "team mailbox not found")
	case errors.Is(err, team.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "manage permission required")
	case errors.Is(err, team.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, team.ErrExists):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	default:
		e := app.AsError(err)
		if e.Status >= 500 {
			s.log.Error("team request failed", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
		writeError(w, e.Status, e.Code, e.Error())
	}
}

func teamActor(a *authInfo) team.Actor {
	n := a.User.DisplayName
	if n == "" {
		n = a.User.Username
	}
	return team.Actor{UserID: a.User.ID, Name: n}
}

type teamAdminEntry struct {
	Mailbox  team.Mailbox  `json:"mailbox"`
	Grants   []team.Grant  `json:"grants"`
	Accounts []app.Account `json:"accounts"`
	Running  bool          `json:"running"`
}

func (s *Server) handleTeamAdminList(w http.ResponseWriter, r *http.Request, a *authInfo) {
	mbs, err := s.team.svc.ListMailboxes(r.Context())
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	out := []teamAdminEntry{}
	for _, mb := range mbs {
		e := teamAdminEntry{Mailbox: mb, Accounts: []app.Account{}}
		if e.Grants, err = s.team.svc.ListGrants(r.Context(), mb.ID); err != nil {
			s.writeTeamError(w, err)
			return
		}
		if rt, ok := s.team.mgr.Get(mb.ID); ok {
			e.Running = true
			if tr, ok := rt.(*teamRuntime); ok {
				if accs, err := tr.ListAccounts(r.Context()); err == nil {
					e.Accounts = accs
				}
			}
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTeamAdminCreate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Mailbox team.MailboxInput    `json:"mailbox"`
		Account *app.NewAccountInput `json:"account"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	mb, err := s.team.svc.CreateMailbox(ctx, teamActor(a), req.Mailbox)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	resp := map[string]any{"mailbox": mb}
	if req.Account != nil {
		ta, err := s.teamRuntimeApp(ctx, mb.ID)
		var acc app.Account
		if err == nil {
			acc, err = ta.CreateAccount(ctx, *req.Account)
		}
		if err != nil {
			// Roll back: a mailbox without account is useless and the
			// admin will retry with corrected settings.
			_ = s.team.svc.DeleteMailbox(ctx, teamActor(a), mb.ID)
			_ = s.team.mgr.Remove(mb.ID)
			s.auth.DropTeamSecrets(mb.ID)
			s.writeTeamError(w, err)
			return
		}
		resp["account"] = acc
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleTeamAdminUpdate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	cur, err := s.team.svc.GetMailbox(r.Context(), id)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	in := team.MailboxInput{Name: cur.Name, Address: cur.Address, Description: cur.Description, Kind: cur.Kind,
		SharedDrafts: cur.SharedDrafts, Disabled: cur.Disabled}
	if !readJSON(w, r, &in) {
		return
	}
	mb, err := s.team.svc.UpdateMailbox(r.Context(), teamActor(a), id, in)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	if mb.Disabled {
		_ = s.team.mgr.Close(id) // stop syncing; data is kept
	}
	writeJSON(w, http.StatusOK, mb)
}

func (s *Server) handleTeamAdminDelete(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.team.svc.DeleteMailbox(r.Context(), teamActor(a), id); err != nil {
		s.writeTeamError(w, err)
		return
	}
	if err := s.team.mgr.Remove(id); err != nil {
		s.log.Warn("team: removing data failed", "team", id, "err", err)
	}
	s.auth.DropTeamSecrets(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTeamAdminAddAccount(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in app.NewAccountInput
	if !readJSON(w, r, &in) {
		return
	}
	ta, err := s.teamRuntimeApp(r.Context(), id)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	acc, err := ta.CreateAccount(r.Context(), in)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	_ = s.auth.Audit(r.Context(), "team.account.add", "team="+r.PathValue("id"), true, acc.DisplayName)
	writeJSON(w, http.StatusCreated, acc)
}

// requireManage checks the manage role on the {teamId} of the request.
func (s *Server) requireManage(w http.ResponseWriter, r *http.Request, a *authInfo) (int64, bool) {
	id, ok := pathID(w, r, "teamId")
	if !ok {
		return 0, false
	}
	if _, err := s.team.svc.Require(r.Context(), a.User.ID, id, team.RoleManage); err != nil {
		s.writeTeamError(w, err)
		return 0, false
	}
	return id, true
}

func (s *Server) handleTeamGrants(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := s.requireManage(w, r, a)
	if !ok {
		return
	}
	gs, err := s.team.svc.ListGrants(r.Context(), id)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

func (s *Server) handleTeamSetGrant(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := s.requireManage(w, r, a)
	if !ok {
		return
	}
	var req struct {
		UserID  int64     `json:"userId"`
		GroupID int64     `json:"groupId"`
		Role    team.Role `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.team.svc.SetGrant(r.Context(), teamActor(a), id, req.UserID, req.GroupID, req.Role); err != nil {
		s.writeTeamError(w, err)
		return
	}
	gs, err := s.team.svc.ListGrants(r.Context(), id)
	if err != nil {
		s.writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

// handleTeamDirectory lists users and groups for the grant editor; it is
// available to administrators and to users who manage a team mailbox.
func (s *Server) handleTeamDirectory(w http.ResponseWriter, r *http.Request, a *authInfo) {
	ctx := r.Context()
	allowed := a.User.IsAdmin()
	if !allowed {
		acc, err := s.team.svc.Accessible(ctx, a.User.ID)
		if err != nil {
			s.writeTeamError(w, err)
			return
		}
		for _, m := range acc {
			allowed = allowed || m.Role == team.RoleManage
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "manage permission required")
		return
	}
	type entry struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName,omitempty"`
	}
	users, err := s.auth.ListUsers(ctx)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	groups, err := s.auth.ListGroups(ctx)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	out := struct {
		Users  []entry `json:"users"`
		Groups []entry `json:"groups"`
	}{Users: []entry{}, Groups: []entry{}}
	for _, u := range users {
		if !u.Disabled {
			out.Users = append(out.Users, entry{ID: u.ID, Name: u.Username, DisplayName: u.DisplayName})
		}
	}
	for _, g := range groups {
		out.Groups = append(out.Groups, entry{ID: g.ID, Name: g.Name})
	}
	writeJSON(w, http.StatusOK, out)
}
