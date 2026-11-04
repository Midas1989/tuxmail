// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/Midas1989/Mailclient/internal/team"
)

// Shared team mailboxes (server mode, KONZEPT 8.14; package internal/team).
//
// A team mailbox is synced once into its own store data_dir/teams/<id>/
// tuxmail.db by a team-level App (the "runtime", created and started by
// the server's team.Manager). A user's App does not copy that data: it
// reads through to the runtime App via TeamSource, which is the single
// place where permissions are checked (team.Service.Require) before any
// list, read or action. Folder trees, conversation lists, conversation
// timelines and messages of all three views therefore come from the same
// code that serves personal mailboxes; the HTTP layer
// (internal/server/api/team_routes.go) mounts these routes again under
// /api/team/{teamId}/... and the frontend switches its transport to that
// prefix while a team mailbox is open. Collaboration data (assignment,
// comments, mentions, drafts, presence) comes from team.Service.

// TeamSource gives a user's App access to the team mailboxes. The server
// implements it (one per user); it is nil on the desktop.
type TeamSource interface {
	// UserID is the user the App belongs to.
	UserID() int64
	// Service is the team metadata and collaboration service.
	Service() *team.Service
	// Runtime returns the (started) team-level App of a mailbox. It does
	// not check permissions; callers use App.TeamApp.
	Runtime(ctx context.Context, mailboxID int64) (*App, error)
}

// StaticTeamSource is a TeamSource with fixed values (server wiring, tests).
type StaticTeamSource struct {
	User int64
	Svc  *team.Service
	Open func(ctx context.Context, mailboxID int64) (*App, error)
}

// UserID implements TeamSource.
func (s StaticTeamSource) UserID() int64 { return s.User }

// Service implements TeamSource.
func (s StaticTeamSource) Service() *team.Service { return s.Svc }

// Runtime implements TeamSource.
func (s StaticTeamSource) Runtime(ctx context.Context, id int64) (*App, error) {
	return s.Open(ctx, id)
}

// CodeForbidden is returned when the user lacks the role for an action on
// a team mailbox.
const CodeForbidden = "forbidden"

// teamErr maps internal/team errors to application errors.
func teamErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, team.ErrNotFound):
		return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: "team mailbox or conversation not found", Err: err}
	case errors.Is(err, team.ErrForbidden):
		return &Error{Code: CodeForbidden, Status: http.StatusForbidden, Message: "you do not have the permission for this action in the team mailbox", Err: err}
	case errors.Is(err, team.ErrInvalidInput):
		return &Error{Code: CodeInvalidInput, Status: http.StatusBadRequest, Message: err.Error(), Err: err}
	case errors.Is(err, team.ErrExists):
		return &Error{Code: CodeConflict, Status: http.StatusConflict, Message: err.Error(), Err: err}
	}
	return err
}

func (a *App) teamSource() (TeamSource, error) {
	if a.opts.Team == nil {
		return nil, notSupported("team mailboxes (server mode only)")
	}
	return a.opts.Team, nil
}

// TeamApp checks that the user has at least role need on the mailbox and
// returns its runtime App together with the user's role. Users without
// access get not_found.
func (a *App) TeamApp(ctx context.Context, mailboxID int64, need team.Role) (*App, team.Role, error) {
	src, err := a.teamSource()
	if err != nil {
		return nil, team.RoleNone, err
	}
	role, err := src.Service().Require(ctx, src.UserID(), mailboxID, need)
	if err != nil {
		return nil, role, teamErr(err)
	}
	ta, err := src.Runtime(ctx, mailboxID)
	if err != nil {
		return nil, role, teamErr(err)
	}
	return ta, role, nil
}

// teamActor returns the acting user.
func (a *App) teamActor(ctx context.Context) (team.Actor, *team.Service, error) {
	src, err := a.teamSource()
	if err != nil {
		return team.Actor{}, nil, err
	}
	return src.Service().Actor(ctx, src.UserID()), src.Service(), nil
}
