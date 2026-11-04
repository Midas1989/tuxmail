// SPDX-License-Identifier: AGPL-3.0-or-later

package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// ActAssign is the workflow action that assigns the conversation of the
// triggering mail (team workflows, KONZEPT 8.14: automatic assignment by
// keyword or round robin). Example:
//
//	trigger: {event: mail.received, folders: [inbox]}
//	condition: 'mail.subject contains "Rechnung"'
//	actions:
//	  - {type: assign, to: round_robin, users: [anna, ben]}
const ActAssign = "assign"

// Assignment targets of the action.
const (
	AssignRoundRobin = "round_robin" // next of `users` (or of all members who can reply)
	AssignLeastBusy  = "least_busy"  // the one with the fewest open conversations
	AssignNobody     = "nobody"      // unassign
)

// ConversationResolver finds the team-store conversation of a workflow item.
type ConversationResolver func(ctx context.Context, ref workflow.ItemRef) (int64, error)

type assignAction struct {
	To       string   `yaml:"to"`
	Users    []string `yaml:"users,omitempty"`
	Status   string   `yaml:"status,omitempty"`
	Priority string   `yaml:"priority,omitempty"`
	// Override replaces an existing assignee (default: only unassigned).
	Override bool `yaml:"override,omitempty"`

	svc       *Service
	mailboxID int64
	resolve   ConversationResolver
}

// AssignActionType returns the "assign" action bound to one team mailbox.
func AssignActionType(svc *Service, mailboxID int64, resolve ConversationResolver) workflow.ActionType {
	return workflow.ActionType{
		Name:        ActAssign,
		Description: "Assign the conversation in the team mailbox (user, round_robin, least_busy or nobody)",
		Triggers:    []events.Type{events.MailReceived, events.MailMoved, events.MailFlagged, events.NoReplyTimeout, events.Manual, events.ChatReceived},
		Params: []workflow.ParamInfo{
			{Name: "to", Type: "string", Required: true, Positional: 1, Example: "round_robin",
				Description: "username, round_robin, least_busy or nobody"},
			{Name: "users", Type: "list", Description: "candidates for round_robin/least_busy (default: all members who can reply)"},
			{Name: "status", Type: "enum", Enum: []string{"open", "in_progress", "waiting", "done"}},
			{Name: "priority", Type: "enum", Enum: []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent}},
			{Name: "override", Type: "bool", Description: "also reassign conversations that already have an assignee"},
		},
		New: func(p map[string]any) (workflow.Action, error) {
			a := &assignAction{svc: svc, mailboxID: mailboxID, resolve: resolve}
			if err := workflow.DecodeParams(p, a); err != nil {
				return nil, err
			}
			a.To = strings.TrimSpace(a.To)
			if a.To == "" {
				return nil, errors.New("to: required (username, round_robin, least_busy or nobody)")
			}
			if a.Status != "" && !Status(a.Status).Valid() {
				return nil, fmt.Errorf("status %q: use open, in_progress, waiting or done", a.Status)
			}
			if a.Priority != "" && !validPriority(a.Priority) {
				return nil, fmt.Errorf("priority %q: use low, normal, high or urgent", a.Priority)
			}
			return a, nil
		},
	}
}

func (a *assignAction) Plan(rc *workflow.RunContext) (workflow.Plan, error) {
	sum := "assign the conversation to " + a.To
	if len(a.Users) > 0 {
		sum += " (" + strings.Join(a.Users, ", ") + ")"
	}
	return workflow.Plan{Type: ActAssign, Summary: sum, Params: map[string]string{"to": a.To, "status": a.Status}}, nil
}

func (a *assignAction) Execute(ctx context.Context, rc *workflow.RunContext) (workflow.Output, error) {
	if a.svc == nil || a.resolve == nil {
		return nil, workflow.Permanent(errors.New("assign: only available in team mailboxes"))
	}
	conv, err := a.resolve(ctx, rc.Item())
	if err != nil {
		return nil, err
	}
	if conv == 0 {
		return nil, workflow.Permanent(errors.New("assign: the item has no conversation"))
	}
	cur, err := a.svc.Assignment(ctx, a.mailboxID, conv)
	if err != nil {
		return nil, err
	}
	next := cur
	if cur.AssigneeID == 0 || a.Override || a.To == AssignNobody {
		uid, err := a.pick(ctx)
		if err != nil {
			return nil, workflow.Permanent(err)
		}
		next.AssigneeID = uid
	}
	if a.Status != "" {
		next.Status = Status(a.Status)
	}
	if a.Priority != "" {
		next.Priority = a.Priority
	}
	actor := Actor{Name: "workflow"}
	if rc.Workflow != nil {
		actor.Name += ":" + rc.Workflow.ID
	}
	out, err := a.svc.store(ctx, actor, cur, next)
	if err != nil {
		return nil, err
	}
	return workflow.Output{"conversation": fmt.Sprint(conv), "assignee": out.AssigneeName, "var.assignee": out.AssigneeName}, nil
}

// pick chooses the assignee.
func (a *assignAction) pick(ctx context.Context) (int64, error) {
	if a.To == AssignNobody {
		return 0, nil
	}
	members, err := a.svc.Members(ctx, a.mailboxID)
	if err != nil {
		return 0, err
	}
	var cands []Member
	for _, m := range members {
		if !m.Role.Allows(RoleReply) {
			continue
		}
		if len(a.Users) > 0 && !slices.ContainsFunc(a.Users, func(u string) bool { return strings.EqualFold(u, m.Username) }) {
			continue
		}
		cands = append(cands, m)
	}
	switch a.To {
	case AssignRoundRobin:
		if len(cands) == 0 {
			return 0, errors.New("assign: no member can take the conversation")
		}
		return a.svc.nextRoundRobin(ctx, a.mailboxID, cands)
	case AssignLeastBusy:
		if len(cands) == 0 {
			return 0, errors.New("assign: no member can take the conversation")
		}
		best, bestN := cands[0].UserID, -1
		for _, c := range cands {
			var n int
			_ = a.svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_conversations WHERE mailbox_id = ? AND assignee_id = ?
				AND status <> 'done'`, a.mailboxID, c.UserID).Scan(&n)
			if bestN < 0 || n < bestN {
				best, bestN = c.UserID, n
			}
		}
		return best, nil
	}
	for _, m := range members {
		if strings.EqualFold(m.Username, a.To) && m.Role.Allows(RoleReply) {
			return m.UserID, nil
		}
	}
	return 0, fmt.Errorf("assign: %q is not a member of this team mailbox", a.To)
}

type mailboxSettings struct {
	RoundRobinLast int64 `json:"roundRobinLast,omitempty"`
}

// nextRoundRobin returns the candidate after the last one chosen and
// remembers it in team_mailboxes.settings_json.
func (s *Service) nextRoundRobin(ctx context.Context, mailboxID int64, cands []Member) (int64, error) {
	s.rrMu.Lock()
	defer s.rrMu.Unlock()
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT settings_json FROM team_mailboxes WHERE id = ?`, mailboxID).Scan(&raw); err != nil {
		return 0, ErrNotFound
	}
	var st mailboxSettings
	_ = json.Unmarshal([]byte(raw), &st)
	slices.SortFunc(cands, func(a, b Member) int { return int(a.UserID - b.UserID) })
	next := cands[0].UserID
	for _, c := range cands {
		if c.UserID > st.RoundRobinLast {
			next = c.UserID
			break
		}
	}
	st.RoundRobinLast = next
	b, _ := json.Marshal(st)
	if _, err := s.db.ExecContext(ctx, `UPDATE team_mailboxes SET settings_json = ? WHERE id = ?`, string(b), mailboxID); err != nil {
		return 0, err
	}
	return next, nil
}

// AssignedEvent builds the bus event "team.assigned" for team workflows
// (trigger team.assigned): Data holds mailboxId, conversationId, assignee
// (username), assigneeId, previousAssigneeId and status.
func AssignedEvent(ev Event, assigneeUsername string) events.Event {
	data := map[string]string{
		"mailboxId":      fmt.Sprint(ev.MailboxID),
		"conversationId": fmt.Sprint(ev.ConversationID),
		"assignee":       assigneeUsername,
	}
	if ev.Assignment != nil {
		data["assigneeId"] = fmt.Sprint(ev.Assignment.AssigneeID)
		data["assigneeName"] = ev.Assignment.AssigneeName
		data["status"] = string(ev.Assignment.Status)
	}
	data["previousAssigneeId"] = fmt.Sprint(ev.PreviousAssigneeID)
	return events.Event{ID: events.NewID(), Type: events.TeamAssigned, Source: "team",
		Ref: fmt.Sprintf("team:%d:%d", ev.MailboxID, ev.ConversationID), Data: data}
}
