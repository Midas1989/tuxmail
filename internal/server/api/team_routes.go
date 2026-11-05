// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/team"
)

// Team mailbox routes (server mode, KONZEPT 8.14; see app/team_source.go).
//
// Collaboration:
//
//	GET    /api/team/mailboxes                                  -> Access[] (role, counters)
//	GET    /api/team/mentions?unread=1                          -> Mention[]
//	POST   /api/team/mentions/read                              {mailboxId, conversationId}
//	GET    /api/team/{teamId}/members                           -> Member[]
//	GET    /api/team/{teamId}/stats                             -> Stats
//	GET    /api/team/{teamId}/conversations?view=mine|unassigned|open|done&…  -> Page<TeamConversation>
//	GET    /api/team/{teamId}/conversations/{id}                -> TeamConversationDetail
//	GET    /api/team/{teamId}/conversations/{id}/assignment     -> Assignment
//	PATCH  /api/team/{teamId}/conversations/{id}/assignment     AssignmentPatch -> Assignment
//	GET    /api/team/{teamId}/conversations/{id}/comments       -> Comment[]
//	POST   /api/team/{teamId}/conversations/{id}/comments       {body} -> Comment
//	DELETE /api/team/{teamId}/comments/{commentId}
//	POST   /api/team/{teamId}/conversations/{id}/presence       {state: viewing|replying|""} -> Presence[]
//	GET    /api/team/{teamId}/conversations/{id}/drafts         -> Draft[] (other members)
//	GET/PUT/DELETE /api/team/{teamId}/conversations/{id}/draft  ChatDraft (own, per user)
//	POST   /api/team/{teamId}/send                              OutgoingMessage -> SendResult (team identity)
//
// Read-through: the mailbox routes of the personal API listed in
// teamProxied are mounted again under /api/team/{teamId}/… and run on the
// team runtime App after the role check; URLs to /api/messages/… in their
// responses are rewritten to the team prefix.

// teamProxied maps base routes to the role they need on a team mailbox.
var teamProxied = map[string]team.Role{
	"GET /api/accounts":                                     team.RoleRead,
	"GET /api/accounts/{id}/folders":                        team.RoleRead,
	"GET /api/identities":                                   team.RoleRead,
	"GET /api/signatures":                                   team.RoleRead,
	"GET /api/persons":                                      team.RoleRead,
	"GET /api/persons/frequent":                             team.RoleRead,
	"GET /api/messages":                                     team.RoleRead,
	"GET /api/messages/{id}":                                team.RoleRead,
	"GET /api/messages/{id}/conversation":                   team.RoleRead,
	"GET /api/messages/{id}/attachments/{index}":            team.RoleRead,
	"GET /api/messages/{id}/cid/{cid}":                      team.RoleRead,
	"GET /api/conversations/{id}/items":                     team.RoleRead,
	"GET /api/tags":                                         team.RoleRead,
	"GET /api/snippets":                                     team.RoleRead,
	"POST /api/search":                                      team.RoleRead,
	"GET /api/workflows/schema":                             team.RoleRead,
	"GET /api/workflows":                                    team.RoleRead,
	"GET /api/workflows/{id}":                               team.RoleRead,
	"GET /api/workflows/{id}/runs":                          team.RoleRead,
	"GET /api/workflow-templates":                           team.RoleRead,
	"POST /api/workflows/validate":                          team.RoleRead,
	"POST /api/workflows/complete":                          team.RoleRead,
	"POST /api/messages/move":                               team.RoleReply,
	"POST /api/messages/flags":                              team.RoleReply,
	"POST /api/conversations/{id}/read":                     team.RoleReply,
	"PATCH /api/conversations/{id}":                         team.RoleReply,
	"POST /api/conversations/{id}/items/{itemId}/reactions": team.RoleReply,
	"POST /api/uploads":                                     team.RoleReply,
	"POST /api/items/tags":                                  team.RoleReply,
	"PUT /api/snippets/{id}":                                team.RoleManage,
	"DELETE /api/snippets/{id}":                             team.RoleManage,
	"POST /api/workflows":                                   team.RoleManage,
	"PUT /api/workflows/{id}":                               team.RoleManage,
	"DELETE /api/workflows/{id}":                            team.RoleManage,
	"PUT /api/workflows/{id}/enabled":                       team.RoleManage,
	"POST /api/workflows/dry-run":                           team.RoleManage,
	"POST /api/tags":                                        team.RoleManage,
	"PUT /api/tags/{id}":                                    team.RoleManage,
	"DELETE /api/tags/{id}":                                 team.RoleManage,
	"PUT /api/identities/{id}":                              team.RoleManage,
}

// teamRoutes returns the collaboration routes and the read-through
// routes derived from base.
func teamRoutes(base []Route) []Route {
	out := []Route{
		{"GET /api/team/mailboxes", func(w w, r r, a A) { send(w)(a.TeamMailboxes(r.Context())) }},
		{"GET /api/team/mentions", func(w w, r r, a A) { send(w)(a.TeamMentions(r.Context(), qBool(r, "unread"))) }},
		{"POST /api/team/mentions/read", jsonHandler(func(w w, r r, a A, in struct {
			MailboxID      int64 `json:"mailboxId"`
			ConversationID int64 `json:"conversationId"`
		}) {
			done(w, a.TeamMarkMentionsRead(r.Context(), in.MailboxID, in.ConversationID))
		})},
		{"GET /api/team/{teamId}/members", withTeam(func(w w, r r, a A, tid int64) { send(w)(a.TeamMembers(r.Context(), tid)) })},
		{"GET /api/team/{teamId}/stats", withTeam(func(w w, r r, a A, tid int64) { send(w)(a.TeamStats(r.Context(), tid)) })},
		{"GET /api/team/{teamId}/conversations", withTeam(func(w w, r r, a A, tid int64) {
			send(w)(a.TeamConversations(r.Context(), tid, convQuery(r), r.URL.Query().Get("view")))
		})},
		{"GET /api/team/{teamId}/conversations/{id}", withTeamID(func(w w, r r, a A, tid, id int64) {
			sendTeam(w, tid)(a.TeamConversation(r.Context(), tid, id))
		})},
		{"GET /api/team/{teamId}/conversations/{id}/assignment", withTeamID(func(w w, r r, a A, tid, id int64) {
			d, err := a.TeamConversation(r.Context(), tid, id)
			reply(w, d.Team, err)
		})},
		{"PATCH /api/team/{teamId}/conversations/{id}/assignment", withTeamID(func(w w, r r, a A, tid, id int64) {
			var p team.AssignmentPatch
			if readJSON(w, r, &p) {
				send(w)(a.TeamUpdateAssignment(r.Context(), tid, id, p))
			}
		})},
		{"GET /api/team/{teamId}/conversations/{id}/comments", withTeamID(func(w w, r r, a A, tid, id int64) {
			send(w)(a.TeamComments(r.Context(), tid, id))
		})},
		{"POST /api/team/{teamId}/conversations/{id}/comments", withTeamID(func(w w, r r, a A, tid, id int64) {
			var in struct {
				Body string `json:"body"`
			}
			if readJSON(w, r, &in) {
				c, err := a.TeamAddComment(r.Context(), tid, id, in.Body)
				if err != nil {
					writeErr(w, err)
					return
				}
				writeJSON(w, http.StatusCreated, c)
			}
		})},
		{"DELETE /api/team/{teamId}/comments/{commentId}", withTeam(func(w w, r r, a A, tid int64) {
			cid, err := strconv.ParseInt(r.PathValue("commentId"), 10, 64)
			if err != nil || cid <= 0 {
				badRequest(w, "invalid commentId")
				return
			}
			done(w, a.TeamDeleteComment(r.Context(), tid, cid))
		})},
		{"POST /api/team/{teamId}/conversations/{id}/presence", withTeamID(func(w w, r r, a A, tid, id int64) {
			var in struct {
				State string `json:"state"`
			}
			if readJSON(w, r, &in) {
				send(w)(a.TeamHeartbeat(r.Context(), tid, id, in.State))
			}
		})},
		{"GET /api/team/{teamId}/conversations/{id}/drafts", withTeamID(func(w w, r r, a A, tid, id int64) {
			send(w)(a.TeamDrafts(r.Context(), tid, id))
		})},
		{"GET /api/team/{teamId}/conversations/{id}/draft", withTeamID(func(w w, r r, a A, tid, id int64) {
			send(w)(a.TeamGetDraft(r.Context(), tid, id))
		})},
		{"PUT /api/team/{teamId}/conversations/{id}/draft", withTeamID(func(w w, r r, a A, tid, id int64) {
			var d app.ChatDraft
			if readJSON(w, r, &d) {
				done(w, a.TeamSaveDraft(r.Context(), tid, id, &d))
			}
		})},
		{"DELETE /api/team/{teamId}/conversations/{id}/draft", withTeamID(func(w w, r r, a A, tid, id int64) {
			done(w, a.TeamSaveDraft(r.Context(), tid, id, nil))
		})},
		{"POST /api/team/{teamId}/send", withTeam(func(w w, r r, a A, tid int64) {
			var in app.OutgoingMessage
			if readJSON(w, r, &in) {
				send(w)(a.TeamSend(r.Context(), tid, in))
			}
		})},
	}
	for _, rt := range base {
		need, ok := teamProxied[rt.Pattern]
		if !ok {
			continue
		}
		method, path, _ := strings.Cut(rt.Pattern, " ")
		out = append(out, Route{method + " /api/team/{teamId}/" + strings.TrimPrefix(path, "/api/"), proxyTeam(need, rt.Handler)})
	}
	return out
}

func teamID(w http.ResponseWriter, r *http.Request) (int64, bool) { return pathID(w, r, "teamId") }

func withTeam(fn func(w http.ResponseWriter, r *http.Request, a *app.App, teamID int64)) AppHandler {
	return func(w http.ResponseWriter, r *http.Request, a *app.App) {
		if tid, ok := teamID(w, r); ok {
			fn(w, r, a, tid)
		}
	}
}

func withTeamID(fn func(w http.ResponseWriter, r *http.Request, a *app.App, teamID, id int64)) AppHandler {
	return withTeam(func(w http.ResponseWriter, r *http.Request, a *app.App, tid int64) {
		if id, ok := pathID(w, r, "id"); ok {
			fn(w, r, a, tid, id)
		}
	})
}

// convQuery parses the query of GET …/conversations (as the base route).
func convQuery(r *http.Request) app.ConversationQuery {
	q := r.URL.Query()
	return app.ConversationQuery{Scope: convScope(r), Grouping: q.Get("grouping"), Offset: qInt(r, "offset"), Limit: qInt(r, "limit"),
		Search: q.Get("q"), Filter: q.Get("filter"), Archived: qBool(r, "archived"), FolderID: q.Get("chatFolder"),
		Focus: q.Get("focus"), IncludeSnoozed: qBool(r, "includeSnoozed"), TagID: qInt64(r, "tag")}
}

// proxyTeam runs a base handler on the team runtime App after the role
// check. JSON responses get their /api/messages/… URLs rewritten to the
// team prefix so attachments and inline images load from the team store.
func proxyTeam(need team.Role, h AppHandler) AppHandler {
	return withTeam(func(w http.ResponseWriter, r *http.Request, a *app.App, tid int64) {
		ta, _, err := a.TeamApp(r.Context(), tid, need)
		if err != nil {
			writeErr(w, err)
			return
		}
		if need.Allows(team.RoleManage) {
			// Changes of the shared mailbox (workflows ...) run on the team
			// App, which does not know who asks: hand it the acting member
			// for the audit log, the author and the notice to the other
			// managers (re-audit NH-03).
			wa, err := a.TeamWorkflowActor(r.Context(), tid)
			if err != nil {
				writeErr(w, err)
				return
			}
			r = r.WithContext(app.WithWorkflowActor(r.Context(), wa))
		}
		rw := &teamRewriter{ResponseWriter: w, prefix: teamPrefix(tid)}
		h(rw, r, ta)
		rw.flush()
	})
}

func teamPrefix(tid int64) []byte { return []byte(fmt.Sprintf("/api/team/%d/", tid)) }

func sendTeam(w http.ResponseWriter, tid int64) func(any, error) {
	return func(v any, err error) {
		rw := &teamRewriter{ResponseWriter: w, prefix: teamPrefix(tid)}
		reply(rw, v, err)
		rw.flush()
	}
}

var apiMessages = []byte(`"/api/messages/`)

// teamRewriter buffers JSON bodies to rewrite URLs; other content types
// (attachments) stream through unchanged.
type teamRewriter struct {
	http.ResponseWriter
	prefix  []byte
	status  int
	buf     bytes.Buffer
	isJSON  bool
	started bool
}

func (t *teamRewriter) WriteHeader(code int) {
	if t.started {
		return
	}
	t.started = true
	t.status = code
	t.isJSON = strings.HasPrefix(t.Header().Get("Content-Type"), "application/json")
	if !t.isJSON {
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *teamRewriter) Write(b []byte) (int, error) {
	if !t.started {
		t.WriteHeader(http.StatusOK)
	}
	if t.isJSON {
		return t.buf.Write(b)
	}
	return t.ResponseWriter.Write(b)
}

func (t *teamRewriter) flush() {
	if !t.isJSON || !t.started {
		return
	}
	repl := append([]byte(`"`), t.prefix...)
	repl = append(repl, "messages/"...)
	body := bytes.ReplaceAll(t.buf.Bytes(), apiMessages, repl)
	// Inside HTML attributes the URL follows src=\" (JSON-escaped quote).
	body = bytes.ReplaceAll(body, []byte(`\"/api/messages/`), append([]byte(`\"`), repl[1:]...))
	t.Header().Del("Content-Length")
	t.ResponseWriter.WriteHeader(t.status)
	_, _ = t.ResponseWriter.Write(body)
}
