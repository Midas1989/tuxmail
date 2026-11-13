// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
)

type (
	w = http.ResponseWriter
	r = *http.Request
	A = *app.App
)

// Routes returns all endpoints of http.ts (plus attachment, cid, proxy and
// vault endpoints). Keep in sync with the route list at the top of http.ts;
// the contract test in internal/server checks every documented route.
func Routes(opts Options) []Route {
	routes := []Route{
		// Accounts & identities
		{"GET /api/accounts", func(w w, r r, a A) { send(w)(a.ListAccounts(r.Context())) }},
		{"POST /api/accounts", jsonHandler(func(w w, r r, a A, in app.NewAccountInput) {
			acc, err := a.CreateAccount(r.Context(), in)
			if err == nil {
				writeJSON(w, http.StatusCreated, acc)
				return
			}
			writeErr(w, err)
		})},
		{"POST /api/accounts/discover", jsonHandler(func(w w, r r, a A, in struct{ Email, Password string }) {
			send(w)(a.DiscoverAccount(r.Context(), in.Email, in.Password))
		})},
		{"GET /api/accounts/{id}/folders", withID(func(w w, r r, a A, id int64) { send(w)(a.ListFolders(r.Context(), id)) })},
		{"GET /api/identities", func(w w, r r, a A) { send(w)(a.ListIdentities(r.Context())) }},
		{"PUT /api/identities/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.Identity
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.UpdateIdentity(r.Context(), in))
			}
		})},
		{"GET /api/signatures", func(w w, r r, a A) { send(w)(a.ListSignatures(r.Context())) }},
		{"POST /api/signatures", func(w w, r r, a A) {
			var in app.Signature
			if readJSON(w, r, &in) {
				in.ID = 0
				send(w)(a.SaveSignature(r.Context(), in))
			}
		}},
		{"PUT /api/signatures/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.Signature
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveSignature(r.Context(), in))
			}
		})},
		{"DELETE /api/signatures/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteSignature(r.Context(), id)) })},
		{"GET /api/persons", func(w w, r r, a A) { send(w)(a.ListPersons(r.Context())) }},
		{"GET /api/persons/frequent", func(w w, r r, a A) { send(w)(a.FrequentContacts(r.Context())) }},
		{"PUT /api/persons/{id}/card", withID(func(w w, r r, a A, id int64) {
			var in app.ContactCard
			if readJSON(w, r, &in) {
				done(w, a.SaveContactCard(r.Context(), id, in))
			}
		})},

		// Messages
		{"GET /api/messages", func(w w, r r, a A) {
			q := r.URL.Query()
			mq := app.MessageQuery{Scope: app.MessageScope{Kind: q.Get("scope"), FolderID: qInt64(r, "folderId"), Role: q.Get("role"),
				TagID: qInt64(r, "tagId"), SearchFolderID: qInt64(r, "searchFolderId")}, Offset: qInt(r, "offset"), Limit: qInt(r, "limit"), Search: q.Get("q")}
			send(w)(a.ListMessages(r.Context(), mq))
		}},
		{"GET /api/messages/{id}", withID(func(w w, r r, a A, id int64) { send(w)(a.GetMessage(r.Context(), id)) })},
		{"GET /api/messages/{id}/conversation", withID(func(w w, r r, a A, id int64) {
			send(w)(a.ConversationForMessage(r.Context(), id, r.URL.Query().Get("grouping")))
		})},
		{"GET /api/messages/{id}/attachments/{index}", withID(serveAttachment)},
		{"GET /api/messages/{id}/cid/{cid}", withID(serveCID)},
		{"POST /api/messages/move", jsonHandler(func(w w, r r, a A, in struct {
			IDs      []int64 `json:"ids"`
			FolderID int64   `json:"folderId"`
		}) {
			done(w, a.Move(r.Context(), in.IDs, in.FolderID))
		})},
		{"POST /api/messages/flags", jsonHandler(func(w w, r r, a A, in struct {
			IDs    []int64    `json:"ids"`
			Add    []app.Flag `json:"add"`
			Remove []app.Flag `json:"remove"`
		}) {
			done(w, a.SetFlags(r.Context(), in.IDs, in.Add, in.Remove))
		})},
		{"GET /api/proxy/image", serveImageProxy},

		// Conversations
		{"GET /api/conversations", func(w w, r r, a A) {
			q := r.URL.Query()
			cq := app.ConversationQuery{Scope: convScope(r), Grouping: q.Get("grouping"), Offset: qInt(r, "offset"), Limit: qInt(r, "limit"),
				Search: q.Get("q"), Filter: q.Get("filter"), Archived: qBool(r, "archived"), FolderID: q.Get("chatFolder"),
				Focus: q.Get("focus"), IncludeSnoozed: qBool(r, "includeSnoozed"), TagID: qInt64(r, "tag")}
			send(w)(a.ListConversations(r.Context(), cq))
		}},
		{"GET /api/conversations/{id}", withID(func(w w, r r, a A, id int64) { send(w)(a.GetConversation(r.Context(), id)) })},
		{"PATCH /api/conversations/{id}", withID(func(w w, r r, a A, id int64) {
			var p app.ConversationPatch
			if readJSON(w, r, &p) {
				send(w)(a.UpdateConversation(r.Context(), id, p))
			}
		})},
		{"DELETE /api/conversations/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteConversation(r.Context(), id)) })},
		{"GET /api/conversations/{id}/items", withID(func(w w, r r, a A, id int64) {
			q := app.ItemsQuery{Before: r.URL.Query().Get("before"), Limit: qInt(r, "limit")}
			if r.URL.Query().Get("scope") != "" {
				sc := convScope(r)
				q.Scope = &sc
			}
			send(w)(a.ListConversationItems(r.Context(), id, q))
		})},
		{"POST /api/conversations/{id}/read", withID(func(w w, r r, a A, id int64) { done(w, a.MarkConversationRead(r.Context(), id)) })},
		{"POST /api/conversations/{id}/items/{itemId}/reactions", withID(func(w w, r r, a A, id int64) {
			var in struct{ Emoji string }
			if readJSON(w, r, &in) {
				send(w)(a.ReactToItem(r.Context(), id, r.PathValue("itemId"), in.Emoji))
			}
		})},
		{"DELETE /api/conversations/{id}/items/{itemId}", withID(func(w w, r r, a A, id int64) {
			done(w, a.DeleteItem(r.Context(), id, r.PathValue("itemId")))
		})},
		{"POST /api/conversations/{id}/items/{itemId}/create", withID(func(w w, r r, a A, id int64) {
			var in struct{ Kind string }
			if readJSON(w, r, &in) {
				send(w)(a.CreateFromItem(r.Context(), id, r.PathValue("itemId"), in.Kind))
			}
		})},
		{"PUT /api/conversations/{id}/items/{itemId}/star", withID(func(w w, r r, a A, id int64) {
			var in struct{ Starred bool }
			if readJSON(w, r, &in) {
				done(w, a.SetItemStarred(r.Context(), id, r.PathValue("itemId"), in.Starred))
			}
		})},
		{"PUT /api/conversations/{id}/items/{itemId}/pin", withID(func(w w, r r, a A, id int64) {
			var in struct{ Pinned bool }
			if readJSON(w, r, &in) {
				done(w, a.SetItemPinned(r.Context(), id, r.PathValue("itemId"), in.Pinned))
			}
		})},
		{"POST /api/items/{itemId}/forward", jsonHandler(func(w w, r r, a A, in struct {
			ConversationID int64 `json:"conversationId"`
		}) {
			send(w)(a.ForwardItem(r.Context(), r.PathValue("itemId"), in.ConversationID))
		})},
		{"GET /api/conversations/{id}/draft", withID(func(w w, r r, a A, id int64) { send(w)(a.GetDraft(r.Context(), id)) })},
		{"PUT /api/conversations/{id}/draft", withID(func(w w, r r, a A, id int64) {
			var d app.ChatDraft
			if readJSON(w, r, &d) {
				done(w, a.SaveDraft(r.Context(), id, &d))
			}
		})},
		{"DELETE /api/conversations/{id}/draft", withID(func(w w, r r, a A, id int64) { done(w, a.SaveDraft(r.Context(), id, nil)) })},
		{"POST /api/conversations/{id}/meeting-proposals", withID(func(w w, r r, a A, id int64) {
			var in app.MeetingProposalInput
			if readJSON(w, r, &in) {
				send(w)(a.ProposeMeeting(r.Context(), id, in))
			}
		})},
		{"POST /api/conversations/{id}/meeting-proposals/{pid}/accept", withID(func(w w, r r, a A, id int64) {
			var in struct {
				SlotStart string `json:"slotStart"`
			}
			if readJSON(w, r, &in) {
				done(w, a.AcceptMeetingSlot(r.Context(), id, r.PathValue("pid"), in.SlotStart))
			}
		})},

		// Chat folders, starred, focus, scheduled, snippets
		{"GET /api/chat-folders", func(w w, r r, a A) { send(w)(a.ListChatFolders(r.Context())) }},
		{"PUT /api/chat-folders/{id}", func(w w, r r, a A) {
			var f app.ChatFolder
			if readJSON(w, r, &f) {
				if id := r.PathValue("id"); id != "new" {
					f.ID = id
				}
				send(w)(a.SaveChatFolder(r.Context(), f))
			}
		}},
		{"DELETE /api/chat-folders/{id}", func(w w, r r, a A) { done(w, a.DeleteChatFolder(r.Context(), r.PathValue("id"))) }},
		{"GET /api/starred", func(w w, r r, a A) { send(w)(a.ListStarredItems(r.Context(), qInt64(r, "conversationId"))) }},
		{"GET /api/focus", func(w w, r r, a A) { send(w)(a.GetFocusSettings(r.Context())) }},
		{"PUT /api/focus", jsonHandler(func(w w, r r, a A, in app.FocusSettings) { send(w)(a.SaveFocusSettings(r.Context(), in)) })},
		{"GET /api/scheduled", func(w w, r r, a A) { send(w)(a.ListScheduled(r.Context(), qInt64(r, "conversationId"))) }},
		{"PATCH /api/scheduled/{id}", jsonHandler(func(w w, r r, a A, in app.ScheduledPatch) {
			send(w)(a.UpdateScheduled(r.Context(), r.PathValue("id"), in))
		})},
		{"DELETE /api/scheduled/{id}", func(w w, r r, a A) { done(w, a.CancelScheduled(r.Context(), r.PathValue("id"))) }},
		{"POST /api/scheduled/{id}/send", func(w w, r r, a A) { done(w, a.SendScheduledNow(r.Context(), r.PathValue("id"))) }},
		{"GET /api/snippets", func(w w, r r, a A) { send(w)(a.ListSnippets(r.Context())) }},
		{"PUT /api/snippets/{id}", jsonHandler(func(w w, r r, a A, in app.Snippet) {
			if id := r.PathValue("id"); id != "new" {
				in.ID = id
			}
			send(w)(a.SaveSnippet(r.Context(), in))
		})},
		{"DELETE /api/snippets/{id}", func(w w, r r, a A) { done(w, a.DeleteSnippet(r.Context(), r.PathValue("id"))) }},

		// AI
		{"GET /api/ai/settings", func(w w, r r, a A) { send(w)(a.GetAiSettings(r.Context())) }},
		{"PUT /api/ai/settings", jsonHandler(func(w w, r r, a A, in app.AiSettings) { send(w)(a.SaveAiSettings(r.Context(), in)) })},
		{"POST /api/ai/conversations/{id}/suggest", withID(func(w w, r r, a A, id int64) { send(w)(a.AiSuggestReplies(r.Context(), id)) })},
		{"POST /api/ai/conversations/{id}/summarize", withID(func(w w, r r, a A, id int64) { send(w)(a.AiSummarize(r.Context(), id)) })},
		{"POST /api/ai/conversations/{id}/items/{itemId}/translate", withID(func(w w, r r, a A, id int64) {
			var in struct {
				TargetLang string `json:"targetLang"`
			}
			if readJSON(w, r, &in) {
				send(w)(a.AiTranslate(r.Context(), id, r.PathValue("itemId"), in.TargetLang))
			}
		})},

		// Calendar
		{"GET /api/calendar/free-slots", func(w w, r r, a A) {
			q := r.URL.Query()
			send(w)(a.FindFreeSlots(r.Context(), app.FreeSlotQuery{From: q.Get("from"), To: q.Get("to"), DurationMin: qInt(r, "durationMin")}))
		}},
		{"PUT /api/tasks/{id}/done", withID(func(w w, r r, a A, id int64) {
			var in struct{ Done bool }
			if readJSON(w, r, &in) {
				done(w, a.SetTaskDone(r.Context(), id, in.Done))
			}
		})},

		// Sending
		{"GET /api/idempotency/{key}", idempotencyStatus},
		{"POST /api/send", idempotent("send", jsonHandler(func(w w, r r, a A, in app.OutgoingMessage) { send(w)(a.Send(r.Context(), in)) }))},
		{"POST /api/uploads", func(w w, r r, a A) {
			name, err := url.QueryUnescape(r.Header.Get("X-Filename"))
			if err != nil || name == "" {
				badRequest(w, "X-Filename header required")
				return
			}
			ct := r.Header.Get("Content-Type")
			if mt, _, err := mime.ParseMediaType(ct); err == nil {
				ct = mt
			}
			res, err := a.UploadAttachment(r.Context(), name, ct, http.MaxBytesReader(w, r.Body, 101<<20))
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, res)
		}},

		// Push & notifications (the server serves the Web Push routes itself)
		{"GET /api/push/vapid-public-key", func(w w, r r, a A) {
			k, err := a.GetPushVapidKey(r.Context())
			reply(w, map[string]string{"key": k, "publicKey": k}, err)
		}},
		{"POST /api/push/subscribe", jsonHandler(func(w w, r r, a A, in app.PushSubscriptionInput) { done(w, a.SubscribePush(r.Context(), in)) })},
		{"POST /api/push/unsubscribe", jsonHandler(func(w w, r r, a A, in struct{ Endpoint string }) {
			done(w, a.UnsubscribePush(r.Context(), in.Endpoint))
		})},
		{"POST /api/push/test", func(w w, r r, a A) { done(w, a.SendTestNotification(r.Context())) }},
		{"GET /api/notifications/settings", func(w w, r r, a A) { send(w)(a.GetNotificationSettings(r.Context())) }},
		{"PUT /api/notifications/settings", jsonHandler(func(w w, r r, a A, in app.NotificationSettings) {
			send(w)(a.SaveNotificationSettings(r.Context(), in))
		})},

		// Provisioning (desktop client importing from a remote server)
		{"POST /api/provisioning/import", jsonHandler(func(w w, r r, a A, in app.ProvisioningImport) {
			done(w, a.ImportProvisioning(r.Context(), in))
		})},

		// Search
		{"POST /api/search", jsonHandler(func(w w, r r, a A, in app.SearchQuery) { send(w)(a.Search(r.Context(), in)) })},
		{"GET /api/search/profiles", func(w w, r r, a A) { send(w)(a.ListSearchProfiles(r.Context())) }},
		{"PUT /api/search/profiles/{id}", jsonHandler(func(w w, r r, a A, in app.SearchProfile) {
			if id := r.PathValue("id"); id != "new" {
				in.ID = id
			}
			send(w)(a.SaveSearchProfile(r.Context(), in))
		})},
		{"DELETE /api/search/profiles/{id}", func(w w, r r, a A) { done(w, a.DeleteSearchProfile(r.Context(), r.PathValue("id"))) }},
		{"GET /api/search/history", func(w w, r r, a A) { send(w)(a.GetSearchHistory(r.Context())) }},
		{"DELETE /api/search/history", func(w w, r r, a A) { done(w, a.ClearSearchHistory(r.Context())) }},

		// Preferences & locales
		{"GET /api/preferences", func(w w, r r, a A) { send(w)(a.GetPreferences(r.Context())) }},
		{"PUT /api/preferences", jsonHandler(func(w w, r r, a A, in app.UserPreferences) { send(w)(a.SetPreferences(r.Context(), in)) })},
		{"GET /api/locales", func(w w, r r, a A) { send(w)(a.ListLocales(r.Context())) }},
		{"GET /api/locales/{code}", func(w w, r r, a A) { send(w)(a.GetLocale(r.Context(), r.PathValue("code"))) }},

		// Tags
		{"GET /api/tags", func(w w, r r, a A) { send(w)(a.ListTags(r.Context())) }},
		{"POST /api/tags", jsonHandler(func(w w, r r, a A, in app.TagInput) {
			in.ID = 0
			t, err := a.SaveTag(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, t)
		})},
		{"PUT /api/tags/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.TagInput
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveTag(r.Context(), in))
			}
		})},
		{"DELETE /api/tags/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteTag(r.Context(), id)) })},
		{"POST /api/tags/merge", jsonHandler(func(w w, r r, a A, in struct {
			SourceIDs []int64 `json:"sourceIds"`
			TargetID  int64   `json:"targetId"`
		}) {
			done(w, a.MergeTags(r.Context(), in.SourceIDs, in.TargetID))
		})},
		{"POST /api/items/tags", jsonHandler(func(w w, r r, a A, in struct {
			Items  []app.ItemRef `json:"items"`
			Add    []int64       `json:"add"`
			Remove []int64       `json:"remove"`
		}) {
			done(w, a.SetItemTags(r.Context(), in.Items, in.Add, in.Remove))
		})},

		// Projects
		{"GET /api/projects", func(w w, r r, a A) {
			var st []string
			if s := r.URL.Query().Get("status"); s != "" {
				st = strings.Split(s, ",")
			}
			send(w)(a.ListProjects(r.Context(), st))
		}},
		{"POST /api/projects", jsonHandler(func(w w, r r, a A, in app.Project) {
			in.ID = 0
			p, err := a.SaveProject(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, p)
		})},
		{"GET /api/projects/{id}", withID(func(w w, r r, a A, id int64) { send(w)(a.GetProject(r.Context(), id)) })},
		{"PUT /api/projects/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.Project
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveProject(r.Context(), in))
			}
		})},
		{"DELETE /api/projects/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteProject(r.Context(), id)) })},
		{"GET /api/project-templates", func(w w, r r, a A) { send(w)(a.ListProjectTemplates(r.Context())) }},
		{"POST /api/projects/{id}/items", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Items []app.ItemRef `json:"items"`
			}
			if readJSON(w, r, &in) {
				done(w, a.AddToProject(r.Context(), id, in.Items))
			}
		})},
		{"POST /api/projects/{id}/items/remove", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Items []app.ItemRef `json:"items"`
			}
			if readJSON(w, r, &in) {
				done(w, a.RemoveFromProject(r.Context(), id, in.Items))
			}
		})},
		{"GET /api/projects/{id}/timeline", withID(func(w w, r r, a A, id int64) { send(w)(a.ProjectTimeline(r.Context(), id)) })},
		{"POST /api/projects/{id}/apply-rules", withID(func(w w, r r, a A, id int64) {
			n, err := a.ApplyProjectRules(r.Context(), id)
			reply(w, map[string]int{"added": n}, err)
		})},

		// Search folders
		{"GET /api/search-folders", func(w w, r r, a A) { send(w)(a.ListSearchFolders(r.Context())) }},
		{"POST /api/search-folders", jsonHandler(func(w w, r r, a A, in app.SearchFolderInput) {
			in.ID = 0
			f, err := a.SaveSearchFolder(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, f)
		})},
		{"PUT /api/search-folders/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.SearchFolderInput
			if readJSON(w, r, &in) {
				in.ID = id
				send(w)(a.SaveSearchFolder(r.Context(), in))
			}
		})},
		{"DELETE /api/search-folders/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteSearchFolder(r.Context(), id)) })},

		// Settings synchronisation of the desktop app: deletions on purpose.
		{"GET /api/settings-sync/tombstones", func(w w, r r, a A) { send(w)(a.Store().ListSettingsTombstones(r.Context())) }},

		// Workflows
		{"GET /api/workflows/schema", func(w w, r r, a A) { send(w)(a.DescribeWorkflows(r.Context())) }},
		{"GET /api/workflows", func(w w, r r, a A) { send(w)(a.ListWorkflows(r.Context(), qInt64(r, "projectId"))) }},
		{"POST /api/workflows", jsonHandler(func(w w, r r, a A, in app.Workflow) {
			in.ID = ""
			wf, err := a.SaveWorkflow(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, wf)
		})},
		{"GET /api/workflows/{id}", func(w w, r r, a A) { send(w)(a.GetWorkflow(r.Context(), r.PathValue("id"))) }},
		{"PUT /api/workflows/{id}", jsonHandler(func(w w, r r, a A, in app.Workflow) {
			in.ID = r.PathValue("id")
			send(w)(a.SaveWorkflow(r.Context(), in))
		})},
		{"DELETE /api/workflows/{id}", func(w w, r r, a A) { done(w, a.DeleteWorkflow(r.Context(), r.PathValue("id"))) }},
		{"PUT /api/workflows/{id}/enabled", jsonHandler(func(w w, r r, a A, in struct {
			Enabled         bool `json:"enabled"`
			ConfirmExternal bool `json:"confirmExternal"`
		}) {
			send(w)(a.SetWorkflowEnabled(r.Context(), r.PathValue("id"), in.Enabled, in.ConfirmExternal))
		})},
		{"GET /api/workflow-templates", func(w w, r r, a A) { send(w)(a.ListWorkflowTemplates(r.Context())) }},
		{"POST /api/workflows/dry-run", jsonHandler(func(w w, r r, a A, in struct {
			Workflow app.Workflow `json:"workflow"`
			Limit    int          `json:"limit"`
		}) {
			send(w)(a.DryRunWorkflow(r.Context(), in.Workflow, in.Limit))
		})},
		{"GET /api/workflows/{id}/runs", func(w w, r r, a A) {
			send(w)(a.ListWorkflowRuns(r.Context(), r.PathValue("id"), qInt(r, "limit")))
		}},
		{"POST /api/workflows/validate", jsonHandler(func(w w, r r, a A, in struct{ Src, Mode string }) {
			send(w)(a.ValidateWorkflow(r.Context(), in.Src, in.Mode))
		})},
		{"POST /api/workflows/complete", jsonHandler(func(w w, r r, a A, in struct {
			Src    string `json:"src"`
			Offset int    `json:"offset"`
			Mode   string `json:"mode"`
		}) {
			send(w)(a.CompleteWorkflow(r.Context(), in.Src, in.Offset, in.Mode))
		})},

		// Folder config (routes_folderconfig.go)
		{"GET /api/accounts/{id}/folder-config", getFolderConfig},
		{"PUT /api/accounts/{id}/folder-config/roles/{role}", putFolderRole},
		{"POST /api/accounts/{id}/folder-config/reset", resetFolderRoles},
		{"POST /api/accounts/{id}/folder-config/create", createRoleFolder},
		{"PUT /api/accounts/{id}/folder-config/behaviour", putFolderBehaviour},
		{"PUT /api/identities/{id}/folders", putIdentityFolders},
		{"POST /api/folders/{id}/expunge", expungeFolder},

		// Portable vault (desktop; in server mode always unlocked)
		{"GET /api/vault/status", func(w w, r r, a A) { send(w)(a.VaultStatus(r.Context())) }},
		{"POST /api/vault/unlock", jsonHandler(func(w w, r r, a A, in struct{ Password string }) {
			done(w, a.UnlockVault(r.Context(), in.Password))
		})},

		// Live events
		{"GET /api/events", eventsHandler(opts)},
	}
	routes = append(routes, cloudRoutes()...)       // cloud routes
	routes = append(routes, ioRoutes()...)          // io_routes.go
	routes = append(routes, mailProtoRoutes()...)   // mailproto_routes.go
	routes = append(routes, oauthRoutes()...)       // oauth_routes.go
	routes = append(routes, MessengerRoutes()...)   // messenger_routes.go
	routes = append(routes, comfortRoutes()...)     // comfort_routes.go
	routes = append(routes, pimRoutes()...)         // pim_routes.go
	routes = append(routes, diagRoutes()...)        // diag_routes.go
	routes = append(routes, accountEditRoutes()...) // account_routes.go
	return append(routes, teamRoutes(routes)...)    // team_routes.go (wraps mailbox routes of the groups above)
}

// send returns a function writing (value, error) results:
// send(w)(a.ListAccounts(ctx)).
func send(w http.ResponseWriter) func(any, error) {
	return func(v any, err error) { reply(w, v, err) }
}

func serveBlob(w http.ResponseWriter, c app.AttachmentContent, inline bool) {
	// Only an exact allowlist is rendered inline from the API origin; anything
	// else (also malformed types like "image/png,text/html") is a download.
	ct, safeInline := app.BlobContentType(c.MIMEType)
	safeInline = safeInline && inline
	disp := "attachment"
	if safeInline {
		disp = "inline"
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": c.Filename}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(c.Data)))
	_, _ = w.Write(c.Data)
}

func serveAttachment(w http.ResponseWriter, r *http.Request, a *app.App, id int64) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		badRequest(w, "invalid attachment index")
		return
	}
	c, err := a.MessageAttachment(r.Context(), id, idx)
	if err != nil {
		writeErr(w, err)
		return
	}
	serveBlob(w, c, r.URL.Query().Get("download") == "")
}

func serveCID(w http.ResponseWriter, r *http.Request, a *app.App, id int64) {
	c, err := a.MessageCID(r.Context(), id, r.PathValue("cid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	serveBlob(w, c, true)
}

// serveImageProxy is the remote-image proxy for HTML mails
// (internal/imageproxy): signed URLs work without a session (sandboxed mail
// frames); routes mounted here sit behind the embedding server's
// authentication, so unsigned requests are accepted as authenticated.
func serveImageProxy(w http.ResponseWriter, r *http.Request, a *app.App) {
	a.ServeImageProxy(w, r, true)
}
