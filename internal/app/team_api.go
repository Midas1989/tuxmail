// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Midas1989/Mailclient/internal/team"
)

// TeamConversation is a conversation of a team mailbox with its
// collaboration state.
type TeamConversation struct {
	Conversation
	Team team.Assignment `json:"team"`
}

// TeamConversationDetail adds live presence and the drafts of the other
// members (draft lock hints) to a team conversation.
type TeamConversationDetail struct {
	TeamConversation
	Presence []team.Presence `json:"presence"`
	Drafts   []team.Draft    `json:"drafts"`
	Role     team.Role       `json:"role"`
}

// TeamMailboxes lists the team mailboxes the user can see (sidebar).
func (a *App) TeamMailboxes(ctx context.Context) ([]team.Access, error) {
	src, err := a.teamSource()
	if err != nil {
		return nil, err
	}
	out, err := src.Service().Accessible(ctx, src.UserID())
	return out, teamErr(err)
}

// TeamRole returns the user's role on a mailbox (not_found without access).
func (a *App) TeamRole(ctx context.Context, mailboxID int64) (team.Role, error) {
	src, err := a.teamSource()
	if err != nil {
		return team.RoleNone, err
	}
	r, err := src.Service().Require(ctx, src.UserID(), mailboxID, team.RoleRead)
	return r, teamErr(err)
}

const teamScanLimit = 5000

// TeamConversations lists the conversations of a team mailbox. Team
// conversations are always grouped by thread (assignments belong to
// threads). view is "", mine, unassigned, open or done.
func (a *App) TeamConversations(ctx context.Context, mailboxID int64, q ConversationQuery, view string) (Page[TeamConversation], error) {
	ta, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead)
	if err != nil {
		return Page[TeamConversation]{}, err
	}
	src, _ := a.teamSource()
	svc, uid := src.Service(), src.UserID()
	q.Grouping = "thread"
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	var convs []Conversation
	total := 0
	if ids, ok, err := svc.ViewIDs(ctx, uid, mailboxID, view); err != nil {
		return Page[TeamConversation]{}, err
	} else if ok {
		// Assignment based views: the ids come from server.db.
		for _, id := range ids {
			c, err := ta.GetConversation(ctx, id)
			if err != nil {
				continue // conversation merged or deleted in the team store
			}
			if q.Search != "" && !strings.Contains(strings.ToLower(c.Title+" "+c.Snippet), strings.ToLower(q.Search)) {
				continue
			}
			convs = append(convs, c)
		}
		total = len(convs)
		convs = window(convs, q.Offset, q.Limit)
	} else if view == team.ViewUnassigned || view == team.ViewOpen {
		// Filter all conversations of the scope by their state.
		scan := q
		scan.Offset, scan.Limit = 0, 500
		var all []Conversation
		for len(all) < teamScanLimit {
			p, err := ta.ListConversations(ctx, scan)
			if err != nil {
				return Page[TeamConversation]{}, err
			}
			all = append(all, p.Items...)
			if len(p.Items) < scan.Limit || len(all) >= p.Total {
				break
			}
			scan.Offset += len(p.Items)
		}
		states, err := svc.Assignments(ctx, mailboxID, convIDs(all))
		if err != nil {
			return Page[TeamConversation]{}, err
		}
		for _, c := range all {
			var st *team.Assignment
			if s, ok := states[c.ID]; ok {
				st = &s
			}
			if team.MatchesView(view, uid, st) {
				convs = append(convs, c)
			}
		}
		total = len(convs)
		convs = window(convs, q.Offset, q.Limit)
	} else {
		p, err := ta.ListConversations(ctx, q)
		if err != nil {
			return Page[TeamConversation]{}, err
		}
		convs, total = p.Items, p.Total
	}
	items, err := a.withTeamState(ctx, svc, mailboxID, convs)
	if err != nil {
		return Page[TeamConversation]{}, err
	}
	return Page[TeamConversation]{Items: items, Total: total, Offset: q.Offset}, nil
}

func window[T any](l []T, off, limit int) []T {
	if off >= len(l) {
		return []T{}
	}
	return l[off:min(len(l), off+limit)]
}

func convIDs(cs []Conversation) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func (a *App) withTeamState(ctx context.Context, svc *team.Service, mailboxID int64, convs []Conversation) ([]TeamConversation, error) {
	states, err := svc.Assignments(ctx, mailboxID, convIDs(convs))
	if err != nil {
		return nil, err
	}
	out := make([]TeamConversation, 0, len(convs))
	for _, c := range convs {
		st, ok := states[c.ID]
		if !ok {
			st, err = svc.Assignment(ctx, mailboxID, c.ID)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, TeamConversation{Conversation: c, Team: st})
	}
	return out, nil
}

// TeamConversation returns one conversation with assignment, presence and
// the other members' drafts, and records the read access in the audit log.
func (a *App) TeamConversation(ctx context.Context, mailboxID, convID int64) (TeamConversationDetail, error) {
	ta, role, err := a.TeamApp(ctx, mailboxID, team.RoleRead)
	if err != nil {
		return TeamConversationDetail{}, err
	}
	c, err := ta.GetConversation(ctx, convID)
	if err != nil {
		return TeamConversationDetail{}, err
	}
	actor, svc, _ := a.teamActor(ctx)
	st, err := svc.Assignment(ctx, mailboxID, convID)
	if err != nil {
		return TeamConversationDetail{}, err
	}
	drafts, err := svc.Drafts(ctx, actor.UserID, mailboxID, convID)
	if err != nil {
		return TeamConversationDetail{}, err
	}
	svc.RecordRead(ctx, actor, mailboxID, convID)
	_ = svc.MarkMentionsRead(ctx, actor.UserID, mailboxID, convID)
	return TeamConversationDetail{TeamConversation: TeamConversation{Conversation: c, Team: st},
		Presence: svc.Presence(mailboxID, convID), Drafts: drafts, Role: role}, nil
}

// TeamUpdateAssignment changes assignee, status, priority or due date.
func (a *App) TeamUpdateAssignment(ctx context.Context, mailboxID, convID int64, p team.AssignmentPatch) (team.Assignment, error) {
	ta, role, err := a.TeamApp(ctx, mailboxID, team.RoleReply)
	if err != nil {
		return team.Assignment{}, err
	}
	if _, err := ta.st.GetConversation(ctx, convID); err != nil {
		return team.Assignment{}, err
	}
	actor, svc, _ := a.teamActor(ctx)
	out, err := svc.Update(ctx, actor, role, mailboxID, convID, p)
	return out, teamErr(err)
}

// TeamComments lists the internal comments of a conversation.
func (a *App) TeamComments(ctx context.Context, mailboxID, convID int64) ([]team.Comment, error) {
	if _, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead); err != nil {
		return nil, err
	}
	_, svc, _ := a.teamActor(ctx)
	out, err := svc.Comments(ctx, mailboxID, convID)
	return out, teamErr(err)
}

// TeamAddComment adds an internal comment (@username mentions notify).
func (a *App) TeamAddComment(ctx context.Context, mailboxID, convID int64, body string) (team.Comment, error) {
	ta, role, err := a.TeamApp(ctx, mailboxID, team.RoleReply)
	if err != nil {
		return team.Comment{}, err
	}
	if _, err := ta.st.GetConversation(ctx, convID); err != nil {
		return team.Comment{}, err
	}
	actor, svc, _ := a.teamActor(ctx)
	out, err := svc.AddComment(ctx, actor, role, mailboxID, convID, body)
	return out, teamErr(err)
}

// TeamDeleteComment deletes a comment (own comments, or any as manager).
func (a *App) TeamDeleteComment(ctx context.Context, mailboxID, commentID int64) error {
	_, role, err := a.TeamApp(ctx, mailboxID, team.RoleRead)
	if err != nil {
		return err
	}
	actor, svc, _ := a.teamActor(ctx)
	return teamErr(svc.DeleteComment(ctx, actor, role, mailboxID, commentID))
}

// TeamMembers lists the users with access (assignee picker, @mentions).
func (a *App) TeamMembers(ctx context.Context, mailboxID int64) ([]team.Member, error) {
	if _, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead); err != nil {
		return nil, err
	}
	_, svc, _ := a.teamActor(ctx)
	out, err := svc.Members(ctx, mailboxID)
	return out, teamErr(err)
}

// TeamStats returns the simple team statistics.
func (a *App) TeamStats(ctx context.Context, mailboxID int64) (team.Stats, error) {
	ta, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead)
	if err != nil {
		return team.Stats{}, err
	}
	p, err := ta.ListConversations(ctx, ConversationQuery{Scope: ConversationScope{Kind: "all"}, Grouping: "thread", Limit: 1})
	if err != nil {
		return team.Stats{}, err
	}
	_, svc, _ := a.teamActor(ctx)
	out, err := svc.Stats(ctx, mailboxID, p.Total)
	return out, teamErr(err)
}

// TeamHeartbeat records that the user views (or replies to, state
// "replying") a conversation; state "" leaves. It returns who else is there.
func (a *App) TeamHeartbeat(ctx context.Context, mailboxID, convID int64, state string) ([]team.Presence, error) {
	need := team.RoleRead
	if state == team.PresenceReplying {
		need = team.RoleReply
	}
	if _, _, err := a.TeamApp(ctx, mailboxID, need); err != nil {
		return nil, err
	}
	actor, svc, _ := a.teamActor(ctx)
	return svc.Heartbeat(actor, mailboxID, convID, state), nil
}

// TeamGetDraft returns the user's own reply draft of a team conversation.
func (a *App) TeamGetDraft(ctx context.Context, mailboxID, convID int64) (*ChatDraft, error) {
	if _, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead); err != nil {
		return nil, err
	}
	actor, svc, _ := a.teamActor(ctx)
	d, err := svc.Draft(ctx, actor.UserID, mailboxID, convID)
	if err != nil || d == nil {
		return nil, teamErr(err)
	}
	var out ChatDraft
	if err := json.Unmarshal(d.Body, &out); err != nil {
		return nil, nil
	}
	out.UpdatedAt = rfc3339(d.UpdatedAt)
	return &out, nil
}

// TeamSaveDraft stores (d != nil) or deletes the user's reply draft. Saving
// a non-empty draft also marks the user as replying (collision hint).
func (a *App) TeamSaveDraft(ctx context.Context, mailboxID, convID int64, d *ChatDraft) error {
	if _, _, err := a.TeamApp(ctx, mailboxID, team.RoleReply); err != nil {
		return err
	}
	actor, svc, _ := a.teamActor(ctx)
	var body json.RawMessage
	if d != nil && strings.TrimSpace(d.Text) != "" {
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		body = b
		svc.Heartbeat(actor, mailboxID, convID, team.PresenceReplying)
	}
	return teamErr(svc.SaveDraft(ctx, actor, mailboxID, convID, body))
}

// TeamDrafts lists the drafts other members keep for a conversation
// (content only if the mailbox shares drafts).
func (a *App) TeamDrafts(ctx context.Context, mailboxID, convID int64) ([]team.Draft, error) {
	if _, _, err := a.TeamApp(ctx, mailboxID, team.RoleRead); err != nil {
		return nil, err
	}
	actor, svc, _ := a.teamActor(ctx)
	out, err := svc.Drafts(ctx, actor.UserID, mailboxID, convID)
	return out, teamErr(err)
}

// TeamSend sends a reply as the team mailbox. Only identities of the team
// store can be used (send-as the team identity); the first response time
// and the audit entry "team.replied" are recorded, and the user's draft and
// "replying" presence are cleared.
func (a *App) TeamSend(ctx context.Context, mailboxID int64, msg OutgoingMessage) (SendResult, error) {
	ta, _, err := a.TeamApp(ctx, mailboxID, team.RoleReply)
	if err != nil {
		return SendResult{}, err
	}
	if msg.Channel == "" || msg.Channel == "email" {
		ids, err := ta.ListIdentities(ctx)
		if err != nil {
			return SendResult{}, err
		}
		if len(ids) == 0 {
			return SendResult{}, Errorf(409, CodeNotConfigured, "the team mailbox has no sender identity")
		}
		if msg.IdentityID == 0 {
			msg.IdentityID = ids[0].ID
		} else if !slices.ContainsFunc(ids, func(i Identity) bool { return i.ID == msg.IdentityID }) {
			return SendResult{}, Errorf(403, CodeForbidden, "replies from a team mailbox must use its identity")
		}
	}
	res, err := ta.Send(ctx, msg)
	if err != nil || res.Blocked {
		return res, err
	}
	actor, svc, _ := a.teamActor(ctx)
	conv := res.ConversationID
	if conv == 0 {
		conv = msg.ConversationID
	}
	if conv != 0 {
		if err := svc.RecordReply(ctx, actor, mailboxID, conv); err != nil {
			a.log.Warn("team: recording the reply failed", "team", mailboxID, "err", err)
		}
		_ = svc.SaveDraft(ctx, actor, mailboxID, conv, nil)
		svc.Heartbeat(actor, mailboxID, conv, "")
	}
	return res, nil
}

// TeamMentions returns the user's mentions across all team mailboxes.
func (a *App) TeamMentions(ctx context.Context, unreadOnly bool) ([]team.Mention, error) {
	src, err := a.teamSource()
	if err != nil {
		return nil, err
	}
	all, err := src.Service().Mentions(ctx, src.UserID(), unreadOnly, 100)
	if err != nil {
		return nil, err
	}
	// Drop mentions of mailboxes the user lost access to.
	out := []team.Mention{}
	for _, m := range all {
		if r, _ := src.Service().RoleOf(ctx, src.UserID(), m.MailboxID); r != team.RoleNone {
			out = append(out, m)
		}
	}
	return out, nil
}

// TeamMarkMentionsRead marks mentions of a mailbox (conversation 0: all) read.
func (a *App) TeamMarkMentionsRead(ctx context.Context, mailboxID, convID int64) error {
	src, err := a.teamSource()
	if err != nil {
		return err
	}
	return src.Service().MarkMentionsRead(ctx, src.UserID(), mailboxID, convID)
}
