// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/server/push"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/team"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Shared team mailboxes (KONZEPT 8.14). The server owns
//
//   - team.Service on server.db (grants, assignments, comments, presence),
//   - team.Manager, which opens data_dir/teams/<id>/tuxmail.db and starts a
//     team-level app.App per mailbox (sync + send enabled: the mailbox is
//     synced once for the whole team; its workflows are the team workflows
//     and get the action "assign" and the trigger "team.assigned"),
//   - the fan-out of team events to the Apps (WebSocket) of all users with
//     access, and the notifications for mentions, assignments and new mail
//     in assigned conversations.
//
// Users reach a mailbox through app.TeamSource (configureUserApp), which
// checks the role before every access.

type teamState struct {
	svc *team.Service
	mgr *team.Manager
	// stopSweep ends the presence sweeper started by Serve.
	stopSweep context.CancelFunc
}

// teamRuntime is the runtime of one mailbox: the team App plus the
// cancellation of its event bridge.
type teamRuntime struct {
	*app.App
	cancel context.CancelFunc
}

func (r *teamRuntime) Close() error {
	r.cancel()
	return r.App.Close()
}

// authDirectory adapts auth.Service to team.Directory.
type authDirectory struct{ a *auth.Service }

func (d authDirectory) User(ctx context.Context, id int64) (team.DirUser, error) {
	u, err := d.a.GetUser(ctx, id)
	if err != nil {
		return team.DirUser{}, team.ErrNotFound
	}
	return team.DirUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Admin: u.IsAdmin(), Disabled: u.Disabled, ReadOnly: u.Role == auth.RoleReadonly}, nil
}

func (d authDirectory) Users(ctx context.Context) ([]team.DirUser, error) {
	us, err := d.a.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]team.DirUser, 0, len(us))
	for _, u := range us {
		out = append(out, team.DirUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Admin: u.IsAdmin(), Disabled: u.Disabled, ReadOnly: u.Role == auth.RoleReadonly})
	}
	return out, nil
}

func (d authDirectory) UserGroupIDs(ctx context.Context, id int64) ([]int64, error) {
	return d.a.UserGroupIDs(ctx, id)
}

func (d authDirectory) GroupName(ctx context.Context, id int64) (string, error) {
	g, err := d.a.GetGroup(ctx, id)
	if err != nil {
		return "", err
	}
	return g.Name, nil
}

func (d authDirectory) GroupMembers(ctx context.Context, id int64) ([]int64, error) {
	g, err := d.a.GetGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	return g.Members, nil
}

// initTeam creates the team service and manager (called by New).
func (s *Server) initTeam(opts Options) {
	audit := func(ctx context.Context, a team.Actor, action, target, details string) {
		if auth.ActorFrom(ctx).UserID == 0 && a.UserID != 0 {
			ctx = auth.WithActor(ctx, auth.Actor{UserID: a.UserID, Name: a.Name, IP: a.IP})
		} else if auth.ActorFrom(ctx).Name == "" {
			ctx = auth.WithActor(ctx, auth.Actor{Name: a.Name})
		}
		_ = s.auth.Audit(ctx, action, target, true, details)
	}
	svc := team.New(s.auth.DB(), authDirectory{s.auth}, team.Options{Audit: audit, Now: s.auth.Now})
	s.team = &teamState{svc: svc}
	s.teamAppOptions = opts.TeamAppOptions
	s.team.mgr = team.NewManager(svc, s.cfg.DataDir, s.newTeamRuntime, s.log)
	svc.Subscribe(s.onTeamEvent)
}

// TeamService returns the team mailbox service (CLI, tests).
func (s *Server) TeamService() *team.Service { return s.team.svc }

// TeamApp returns the running team App of a mailbox (tests, CLI).
func (s *Server) TeamApp(ctx context.Context, id int64) (*app.App, error) {
	return s.teamRuntimeApp(ctx, id)
}

// startTeam starts all team mailboxes and the presence sweeper (Serve).
func (s *Server) startTeam(ctx context.Context) {
	if err := s.team.mgr.Start(ctx); err != nil {
		s.log.Warn("team: starting mailboxes failed", "err", err)
	}
	sctx, cancel := context.WithCancel(ctx)
	s.team.stopSweep = cancel
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				s.team.svc.SweepPresence()
			}
		}
	}()
}

// stopTeam stops the team runtimes (after the HTTP server shut down).
func (s *Server) stopTeam() {
	if s.team.stopSweep != nil {
		s.team.stopSweep()
	}
	s.team.mgr.Stop()
}

func (s *Server) teamRuntimeApp(ctx context.Context, id int64) (*app.App, error) {
	rt, err := s.team.mgr.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	tr, ok := rt.(*teamRuntime)
	if !ok {
		return nil, errors.New("team: unexpected runtime")
	}
	return tr.App, nil
}

// newTeamRuntime is the team.Factory: an App on the team store with its
// own secrets vault, sync and send, the "assign" workflow action and a
// notifier that reaches the members.
func (s *Server) newTeamRuntime(ctx context.Context, mb team.Mailbox, st *store.Store, dir string) (team.Runtime, error) {
	secrets, err := s.auth.TeamSecrets(mb.ID)
	if err != nil {
		return nil, err
	}
	resolve := func(ctx context.Context, ref workflow.ItemRef) (int64, error) {
		m, err := st.FindMessageByKey(ctx, ref.AccountID, ref.Key)
		if err != nil {
			return 0, err
		}
		if m.ConversationID == 0 {
			return 0, errors.New("team: the mail has no conversation yet")
		}
		return m.ConversationID, nil
	}
	opts := app.Options{Store: st, DataDir: dir, Mode: app.ModeServer, Version: s.version, Secrets: secrets,
		Logger: s.log.With("team", mb.ID), Locales: s.locales, LocaleDir: filepath.Join(s.cfg.DataDir, "locales"),
		Sync: &app.SyncOptions{}, Send: &app.SendOptions{},
		WorkflowActions: []workflow.ActionType{team.AssignActionType(s.team.svc, mb.ID, resolve)},
		Notifier:        &teamWorkflowNotifier{s: s, mailboxID: mb.ID}}
	opts.OAuth = s.oauthAppOptions()
	if s.teamAppOptions != nil {
		s.teamAppOptions(mb.ID, &opts)
	}
	a, err := app.New(ctx, opts)
	if err != nil {
		return nil, err
	}
	a.Start()
	bctx, cancel := context.WithCancel(context.Background())
	go s.bridgeTeamApp(bctx, mb.ID, a)
	return &teamRuntime{App: a, cancel: cancel}, nil
}

// configureUserApp wires the team source and the workflow notifier into a
// user's App options (called by the app pool).
func (s *Server) configureUserApp(userID int64, o *app.Options) {
	o.Team = app.StaticTeamSource{User: userID, Svc: s.team.svc, Open: s.teamRuntimeApp}
	o.Notifier = &userWorkflowNotifier{s: s, userID: userID}
}

// loadedApp returns the App of a user if it is open (events are only
// delivered to users with a live App; nothing is opened for fan-out).
func (p *appPool) loadedApp(userID int64) (*app.App, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.m[userID]
	if !ok {
		return nil, false
	}
	return e.app, true
}

// publishTeam sends a UI event to the given users.
func (s *Server) publishTeam(users []int64, typ string, payload any) {
	for _, uid := range users {
		if a, ok := s.apps.loadedApp(uid); ok {
			a.PublishTeam(typ, payload)
		}
	}
}

// bridgeTeamApp forwards the UI events of a team App to the members and
// keeps the collaboration state in step with new mail.
func (s *Server) bridgeTeamApp(ctx context.Context, mailboxID int64, a *app.App) {
	ch, cancel := a.Events().Subscribe(ctx, 512)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.handleTeamAppEvent(ctx, mailboxID, a, ev)
		}
	}
}

func (s *Server) handleTeamAppEvent(ctx context.Context, mailboxID int64, a *app.App, ev app.Event) {
	ch := app.TeamChange{MailboxID: mailboxID, ConversationID: ev.ConversationID, FolderID: ev.FolderID}
	switch ev.Type {
	case app.EventMessageAdded, app.EventMessageUpdated, app.EventMessageRemoved:
		ch.Kind = "message"
	case app.EventConversationUpdated:
		ch.Kind = "conversation"
	case app.EventFolderUpdated, app.EventAccountUpdated:
		ch.Kind = "folder"
	default:
		return
	}
	if ev.Type == app.EventMessageAdded && ev.ConversationID != 0 {
		s.teamMailArrived(ctx, mailboxID, a, ev)
	}
	users, err := s.team.svc.AudienceIDs(ctx, mailboxID)
	if err != nil {
		return
	}
	s.publishTeam(users, app.EventTeamChanged, ch)
}

// teamMailArrived opens (or reopens) the conversation of an incoming mail
// and notifies its assignee.
func (s *Server) teamMailArrived(ctx context.Context, mailboxID int64, a *app.App, ev app.Event) {
	inbox := false
	for _, fid := range ev.FolderIDs {
		if f, err := a.Store().GetFolder(ctx, fid); err == nil && f.Role == domain.RoleInbox {
			inbox = true
		}
	}
	if !inbox {
		return
	}
	if err := s.team.svc.Touch(ctx, mailboxID, ev.ConversationID, true); err != nil {
		s.log.Warn("team: tracking conversation failed", "team", mailboxID, "err", err)
		return
	}
	st, err := s.team.svc.Assignment(ctx, mailboxID, ev.ConversationID)
	if err != nil || st.AssigneeID == 0 {
		return
	}
	m, err := a.Store().GetMessage(ctx, ev.MessageID)
	if err != nil {
		return
	}
	mb, _ := s.team.svc.GetMailbox(ctx, mailboxID)
	from := displayFrom(m.From)
	s.notify(ctx, st.AssigneeID, push.Notification{Payload: push.Payload{Type: "mail", Title: from, Body: m.Subject,
		Sender: from, TeamID: fmt.Sprint(mailboxID), ConversationID: fmt.Sprint(ev.ConversationID),
		URL: teamURL(mailboxID, ev.ConversationID), Tag: fmt.Sprintf("team-%d-%d", mailboxID, ev.ConversationID),
		AccountID: "team:" + mb.Name}, Urgency: push.UrgencyNormal})
}

func teamURL(mailboxID, convID int64) string {
	return fmt.Sprintf("/#/mail?team=%d&conversation=%d", mailboxID, convID)
}

// onTeamEvent is the team.Service listener: UI fan-out, notifications and
// the bus event team.assigned for team workflows.
func (s *Server) onTeamEvent(ev team.Event) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		users, _ := s.team.svc.AudienceIDs(ctx, ev.MailboxID)
		change := app.TeamChange{MailboxID: ev.MailboxID, ConversationID: ev.ConversationID, Kind: ev.Kind,
			ActorID: ev.ActorID, ActorName: ev.ActorName}
		if ev.Assignment != nil {
			change.Assignment = ev.Assignment
		}
		switch ev.Kind {
		case team.EventMailbox:
			s.publishTeam(unionInt64(users, ev.UserIDs), app.EventTeamMailboxes, change)
			return
		case team.EventPresence:
			change.Presence = ev.Presence
			s.publishTeam(users, app.EventTeamPresence, change)
			return
		}
		s.publishTeam(users, app.EventTeamChanged, change)
		mb, err := s.team.svc.GetMailbox(ctx, ev.MailboxID)
		if err != nil {
			return
		}
		switch ev.Kind {
		case team.EventAssigned:
			if rt, ok := s.team.mgr.Get(ev.MailboxID); ok {
				if tr, ok := rt.(*teamRuntime); ok {
					assignee := ""
					if ev.Assignment != nil && ev.Assignment.AssigneeID != 0 {
						assignee = s.team.svc.Username(ctx, ev.Assignment.AssigneeID)
					}
					tr.Bus().Publish(team.AssignedEvent(ev, assignee))
				}
			}
			for _, uid := range ev.UserIDs {
				if uid == ev.ActorID {
					continue // self-assignment
				}
				s.teamNotice(ctx, uid, mb, ev, "assigned", "")
			}
		case team.EventMention:
			body := ""
			if ev.Comment != nil {
				body = ev.Comment.Body
			}
			for _, uid := range ev.UserIDs {
				s.teamNotice(ctx, uid, mb, ev, "mention", body)
			}
		}
	}()
}

// teamNotice sends the in-app notice and the push notification of a
// mention or assignment.
func (s *Server) teamNotice(ctx context.Context, userID int64, mb team.Mailbox, ev team.Event, kind, body string) {
	actor := ev.ActorName
	if actor == "" {
		actor = "TuxMail"
	}
	title := fmt.Sprintf("%s: %s", mb.Name, actor)
	text := body
	if kind == "assigned" {
		text = "Konversation zugewiesen"
		if ev.Assignment != nil && ev.Assignment.AssigneeName != "" {
			text += ": " + ev.Assignment.AssigneeName
		}
	}
	if a, ok := s.apps.loadedApp(userID); ok {
		a.PublishTeam(app.EventTeamNotice, app.TeamNotice{Kind: kind, MailboxID: mb.ID, MailboxName: mb.Name,
			ConversationID: ev.ConversationID, ActorName: actor, Text: text})
	}
	s.notify(ctx, userID, push.Notification{Payload: push.Payload{Type: "team", Title: title, Body: text, Sender: actor,
		TeamID: fmt.Sprint(mb.ID), ConversationID: fmt.Sprint(ev.ConversationID), URL: teamURL(mb.ID, ev.ConversationID),
		Tag: fmt.Sprintf("team-%s-%d-%d", kind, mb.ID, ev.ConversationID)}, Urgency: push.UrgencyHigh})
}

func unionInt64(a, b []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, l := range [][]int64{a, b} {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// teamWorkflowNotifier delivers the notify action of team workflows to
// every member who can reply (channel "push"; other channels are not
// available for team mailboxes).
type teamWorkflowNotifier struct {
	s         *Server
	mailboxID int64
}

func (n *teamWorkflowNotifier) Notify(ctx context.Context, channel, title, text string) error {
	if channel != "push" && channel != "desktop" {
		return fmt.Errorf("notify: channel %q is not available in team mailboxes", channel)
	}
	members, err := n.s.team.svc.Members(ctx, n.mailboxID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if !m.Role.Allows(team.RoleReply) {
			continue
		}
		if a, ok := n.s.apps.loadedApp(m.UserID); ok {
			a.PublishTeam(app.EventTeamNotice, app.TeamNotice{Kind: "workflow", MailboxID: n.mailboxID, ActorName: title, Text: text})
		}
		if channel == "push" {
			n.s.notify(ctx, m.UserID, push.Notification{Payload: push.Payload{Type: "workflow", Title: title, Body: text,
				TeamID: fmt.Sprint(n.mailboxID), URL: "/"}})
		}
	}
	return nil
}
