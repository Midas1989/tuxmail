// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"path/filepath"
	"strings"
)

// Api is the struct bound to the Wails runtime in the desktop build: the
// frontend calls window.go.app.Api.<Method>(...) (frontend/src/lib/api/wails.ts).
// Method names and argument order match wails.ts exactly; every exported
// method of Api is exposed to JavaScript, so Api has no other exported
// methods. Errors are returned as JSON {"code","message"} strings, which
// wails.ts turns into ApiError(code).
type Api struct {
	ctx context.Context
	app *App
}

// NewApi binds the application layer for the desktop shell. ctx is the
// lifetime of the window (canceled on shutdown).
func NewApi(ctx context.Context, a *App) *Api { return &Api{ctx: ctx, app: a} }

// apiError is the JSON form of an error for Wails.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func werr(err error) error {
	if err == nil {
		return nil
	}
	e := AsError(err)
	b, _ := json.Marshal(apiError{Code: e.Code, Message: e.Error()})
	return errors.New(string(b))
}

func r1[T any](v T, err error) (T, error) { return v, werr(err) }

// Accounts & identities

func (x *Api) ListAccounts() ([]Account, error)    { return r1(x.app.ListAccounts(x.ctx)) }
func (x *Api) ListIdentities() ([]Identity, error) { return r1(x.app.ListIdentities(x.ctx)) }
func (x *Api) ListSignatures() ([]Signature, error) {
	return r1(x.app.ListSignatures(x.ctx))
}
func (x *Api) ListPersons() ([]Person, error) { return r1(x.app.ListPersons(x.ctx)) }
func (x *Api) DiscoverAccount(email, password string) (DiscoveryResult, error) {
	return r1(x.app.DiscoverAccount(x.ctx, email, password))
}
func (x *Api) CreateAccount(in NewAccountInput) (Account, error) {
	return r1(x.app.CreateAccount(x.ctx, in))
}

// Folders & classic list

func (x *Api) ListFolders(accountID int64) ([]Folder, error) {
	return r1(x.app.ListFolders(x.ctx, accountID))
}
func (x *Api) ListMessages(q MessageQuery) (Page[MessageSummary], error) {
	return r1(x.app.ListMessages(x.ctx, q))
}
func (x *Api) GetMessage(id int64) (MessageDetail, error) { return r1(x.app.GetMessage(x.ctx, id)) }

// Conversations

func (x *Api) ListConversations(q ConversationQuery) (Page[Conversation], error) {
	return r1(x.app.ListConversations(x.ctx, q))
}
func (x *Api) GetConversation(id int64) (Conversation, error) {
	return r1(x.app.GetConversation(x.ctx, id))
}
func (x *Api) ListConversationItems(id int64, q *ItemsQuery) ([]ConversationItem, error) {
	if q == nil {
		q = &ItemsQuery{}
	}
	return r1(x.app.ListConversationItems(x.ctx, id, *q))
}
func (x *Api) ConversationForMessage(messageID int64, grouping string) (ConversationRef, error) {
	return r1(x.app.ConversationForMessage(x.ctx, messageID, grouping))
}
func (x *Api) MarkConversationRead(id int64) error {
	return werr(x.app.MarkConversationRead(x.ctx, id))
}
func (x *Api) UpdateConversation(id int64, p ConversationPatch) (Conversation, error) {
	return r1(x.app.UpdateConversation(x.ctx, id, p))
}
func (x *Api) DeleteConversation(id int64) error { return werr(x.app.DeleteConversation(x.ctx, id)) }
func (x *Api) ReactToItem(conv int64, item, emoji string) (ReactionResult, error) {
	return r1(x.app.ReactToItem(x.ctx, conv, item, emoji))
}
func (x *Api) DeleteItem(conv int64, item string) error {
	return werr(x.app.DeleteItem(x.ctx, conv, item))
}
func (x *Api) ForwardItem(item string, target int64) (SendResult, error) {
	return r1(x.app.ForwardItem(x.ctx, item, target))
}
func (x *Api) CreateFromItem(conv int64, item, kind string) (IDResult, error) {
	return r1(x.app.CreateFromItem(x.ctx, conv, item, kind))
}
func (x *Api) SaveContactCard(personID int64, card ContactCard) error {
	return werr(x.app.SaveContactCard(x.ctx, personID, card))
}
func (x *Api) SaveSignature(s Signature) (Signature, error) {
	return r1(x.app.SaveSignature(x.ctx, s))
}
func (x *Api) DeleteSignature(id int64) error { return werr(x.app.DeleteSignature(x.ctx, id)) }
func (x *Api) UpdateIdentity(i Identity) (Identity, error) {
	return r1(x.app.UpdateIdentity(x.ctx, i))
}

// Actions

func (x *Api) Send(m OutgoingMessage) (SendResult, error) { return r1(x.app.Send(x.ctx, m)) }

// UploadAttachment receives the file base64-encoded over the IPC bridge.
func (x *Api) UploadAttachment(filename, data string) (UploadResult, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return UploadResult{}, werr(invalid("invalid upload data"))
	}
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	return r1(x.app.UploadAttachment(x.ctx, filename, typ, bytes.NewReader(raw)))
}
func (x *Api) Move(ids []int64, folderID int64) error { return werr(x.app.Move(x.ctx, ids, folderID)) }
func (x *Api) SetFlags(ids []int64, add, remove []Flag) error {
	return werr(x.app.SetFlags(x.ctx, ids, add, remove))
}

// Push notifications

func (x *Api) GetPushVapidKey() (string, error) { return r1(x.app.GetPushVapidKey(x.ctx)) }
func (x *Api) SubscribePush(s PushSubscriptionInput) error {
	return werr(x.app.SubscribePush(x.ctx, s))
}
func (x *Api) UnsubscribePush(endpoint string) error {
	return werr(x.app.UnsubscribePush(x.ctx, endpoint))
}
func (x *Api) GetNotificationSettings() (NotificationSettings, error) {
	return r1(x.app.GetNotificationSettings(x.ctx))
}
func (x *Api) SaveNotificationSettings(s NotificationSettings) (NotificationSettings, error) {
	return r1(x.app.SaveNotificationSettings(x.ctx, s))
}
func (x *Api) SendTestNotification() error { return werr(x.app.SendTestNotification(x.ctx)) }

// Server connection

func (x *Api) DeviceAuthorize(serverURL string) (DeviceAuthorization, error) {
	return r1(x.app.DeviceAuthorize(x.ctx, serverURL))
}
func (x *Api) DeviceToken(serverURL, deviceCode string) (DeviceTokenResult, error) {
	return r1(x.app.DeviceToken(x.ctx, serverURL, deviceCode))
}
func (x *Api) ServerLogin(serverURL string, in ServerLoginInput) (DeviceTokenResult, error) {
	return r1(x.app.ServerLogin(x.ctx, serverURL, in))
}
func (x *Api) ImportProvisioning(in ProvisioningImport) error {
	return werr(x.app.ImportProvisioning(x.ctx, in))
}

// Search

func (x *Api) Search(q SearchQuery) (SearchResult, error) { return r1(x.app.Search(x.ctx, q)) }
func (x *Api) ListSearchProfiles() ([]SearchProfile, error) {
	return r1(x.app.ListSearchProfiles(x.ctx))
}
func (x *Api) SaveSearchProfile(p SearchProfile) (SearchProfile, error) {
	return r1(x.app.SaveSearchProfile(x.ctx, p))
}
func (x *Api) DeleteSearchProfile(id string) error {
	return werr(x.app.DeleteSearchProfile(x.ctx, id))
}
func (x *Api) GetSearchHistory() ([]SearchHistoryEntry, error) {
	return r1(x.app.GetSearchHistory(x.ctx))
}
func (x *Api) ClearSearchHistory() error { return werr(x.app.ClearSearchHistory(x.ctx)) }

// Preferences & locales

func (x *Api) GetPreferences() (UserPreferences, error) { return r1(x.app.GetPreferences(x.ctx)) }
func (x *Api) SetPreferences(p UserPreferences) (UserPreferences, error) {
	return r1(x.app.SetPreferences(x.ctx, p))
}
func (x *Api) ListLocales() ([]LocaleInfo, error) { return r1(x.app.ListLocales(x.ctx)) }
func (x *Api) GetLocale(code string) (map[string]any, error) {
	return r1(x.app.GetLocale(x.ctx, code))
}

// Chat folders, stars, pins, focus

func (x *Api) ListChatFolders() ([]ChatFolder, error) { return r1(x.app.ListChatFolders(x.ctx)) }
func (x *Api) SaveChatFolder(f ChatFolder) (ChatFolder, error) {
	return r1(x.app.SaveChatFolder(x.ctx, f))
}
func (x *Api) DeleteChatFolder(id string) error { return werr(x.app.DeleteChatFolder(x.ctx, id)) }
func (x *Api) SetItemStarred(conv int64, item string, starred bool) error {
	return werr(x.app.SetItemStarred(x.ctx, conv, item, starred))
}
func (x *Api) SetItemPinned(conv int64, item string, pinned bool) error {
	return werr(x.app.SetItemPinned(x.ctx, conv, item, pinned))
}
func (x *Api) ListStarredItems(conv int64) ([]StarredItem, error) {
	return r1(x.app.ListStarredItems(x.ctx, conv))
}
func (x *Api) GetFocusSettings() (FocusSettings, error) { return r1(x.app.GetFocusSettings(x.ctx)) }
func (x *Api) SaveFocusSettings(s FocusSettings) (FocusSettings, error) {
	return r1(x.app.SaveFocusSettings(x.ctx, s))
}
func (x *Api) FrequentContacts() ([]int64, error) { return r1(x.app.FrequentContacts(x.ctx)) }

// Drafts & scheduled

func (x *Api) GetDraft(conv int64) (*ChatDraft, error) { return r1(x.app.GetDraft(x.ctx, conv)) }
func (x *Api) SaveDraft(conv int64, d *ChatDraft) error {
	return werr(x.app.SaveDraft(x.ctx, conv, d))
}
func (x *Api) ListScheduled(conv int64) ([]ScheduledMessage, error) {
	return r1(x.app.ListScheduled(x.ctx, conv))
}
func (x *Api) UpdateScheduled(id string, p ScheduledPatch) (ScheduledMessage, error) {
	return r1(x.app.UpdateScheduled(x.ctx, id, p))
}
func (x *Api) CancelScheduled(id string) error  { return werr(x.app.CancelScheduled(x.ctx, id)) }
func (x *Api) SendScheduledNow(id string) error { return werr(x.app.SendScheduledNow(x.ctx, id)) }

// Snippets

func (x *Api) ListSnippets() ([]Snippet, error)       { return r1(x.app.ListSnippets(x.ctx)) }
func (x *Api) SaveSnippet(s Snippet) (Snippet, error) { return r1(x.app.SaveSnippet(x.ctx, s)) }
func (x *Api) DeleteSnippet(id string) error          { return werr(x.app.DeleteSnippet(x.ctx, id)) }
func (x *Api) GetAiSettings() (AiSettings, error)     { return r1(x.app.GetAiSettings(x.ctx)) }
func (x *Api) SaveAiSettings(s AiSettings) (AiSettings, error) {
	return r1(x.app.SaveAiSettings(x.ctx, s))
}
func (x *Api) AiSuggestReplies(conv int64) ([]string, error) {
	return r1(x.app.AiSuggestReplies(x.ctx, conv))
}
func (x *Api) AiTranslate(conv int64, item, lang string) (AiTranslation, error) {
	return r1(x.app.AiTranslate(x.ctx, conv, item, lang))
}
func (x *Api) AiSummarize(conv int64) (AiSummary, error) { return r1(x.app.AiSummarize(x.ctx, conv)) }

// Meetings & tasks

func (x *Api) FindFreeSlots(q FreeSlotQuery) ([]TimeSlot, error) {
	return r1(x.app.FindFreeSlots(x.ctx, q))
}
func (x *Api) ProposeMeeting(conv int64, p MeetingProposalInput) (SendResult, error) {
	return r1(x.app.ProposeMeeting(x.ctx, conv, p))
}
func (x *Api) AcceptMeetingSlot(conv int64, proposalID, slotStart string) error {
	return werr(x.app.AcceptMeetingSlot(x.ctx, conv, proposalID, slotStart))
}

// Tags

func (x *Api) ListTags() ([]TagCount, error)   { return r1(x.app.ListTags(x.ctx)) }
func (x *Api) SaveTag(t TagInput) (Tag, error) { return r1(x.app.SaveTag(x.ctx, t)) }
func (x *Api) DeleteTag(id int64) error        { return werr(x.app.DeleteTag(x.ctx, id)) }
func (x *Api) MergeTags(src []int64, target int64) error {
	return werr(x.app.MergeTags(x.ctx, src, target))
}
func (x *Api) SetItemTags(items []ItemRef, add, remove []int64) error {
	return werr(x.app.SetItemTags(x.ctx, items, add, remove))
}

// ProjectListQuery filters ListProjects.
type ProjectListQuery struct {
	Status []string `json:"status,omitempty"`
}

// Projects

func (x *Api) ListProjects(q *ProjectListQuery) ([]Project, error) {
	var st []string
	if q != nil {
		st = q.Status
	}
	return r1(x.app.ListProjects(x.ctx, st))
}
func (x *Api) GetProject(id int64) (Project, error)   { return r1(x.app.GetProject(x.ctx, id)) }
func (x *Api) SaveProject(p Project) (Project, error) { return r1(x.app.SaveProject(x.ctx, p)) }
func (x *Api) DeleteProject(id int64) error           { return werr(x.app.DeleteProject(x.ctx, id)) }
func (x *Api) ListProjectTemplates() ([]ProjectTemplate, error) {
	return r1(x.app.ListProjectTemplates(x.ctx))
}
func (x *Api) AddToProject(id int64, items []ItemRef) error {
	return werr(x.app.AddToProject(x.ctx, id, items))
}
func (x *Api) RemoveFromProject(id int64, items []ItemRef) error {
	return werr(x.app.RemoveFromProject(x.ctx, id, items))
}
func (x *Api) ProjectTimeline(id int64) ([]ProjectTimelineEntry, error) {
	return r1(x.app.ProjectTimeline(x.ctx, id))
}

// AddedResult is the result of ApplyProjectRules.
type AddedResult struct {
	Added int `json:"added"`
}

func (x *Api) ApplyProjectRules(id int64) (AddedResult, error) {
	n, err := x.app.ApplyProjectRules(x.ctx, id)
	return AddedResult{Added: n}, werr(err)
}
func (x *Api) SetTaskDone(id int64, done bool) error { return werr(x.app.SetTaskDone(x.ctx, id, done)) }

// Search folders

func (x *Api) ListSearchFolders() ([]SearchFolder, error) { return r1(x.app.ListSearchFolders(x.ctx)) }
func (x *Api) SaveSearchFolder(f SearchFolderInput) (SearchFolder, error) {
	return r1(x.app.SaveSearchFolder(x.ctx, f))
}
func (x *Api) DeleteSearchFolder(id int64) error { return werr(x.app.DeleteSearchFolder(x.ctx, id)) }

// WorkflowListQuery filters ListWorkflows.
type WorkflowListQuery struct {
	ProjectID int64 `json:"projectId,omitempty"`
}

// LimitOptions carries an optional limit.
type LimitOptions struct {
	Limit int `json:"limit,omitempty"`
}

// Workflows

func (x *Api) DescribeWorkflows() (WorkflowSchema, error) { return r1(x.app.DescribeWorkflows(x.ctx)) }
func (x *Api) ListWorkflows(q *WorkflowListQuery) ([]Workflow, error) {
	var p int64
	if q != nil {
		p = q.ProjectID
	}
	return r1(x.app.ListWorkflows(x.ctx, p))
}
func (x *Api) GetWorkflow(id string) (Workflow, error)   { return r1(x.app.GetWorkflow(x.ctx, id)) }
func (x *Api) SaveWorkflow(w Workflow) (Workflow, error) { return r1(x.app.SaveWorkflow(x.ctx, w)) }
func (x *Api) DeleteWorkflow(id string) error            { return werr(x.app.DeleteWorkflow(x.ctx, id)) }
func (x *Api) SetWorkflowEnabled(id string, enabled, confirmExternal bool) (Workflow, error) {
	return r1(x.app.SetWorkflowEnabled(x.ctx, id, enabled, confirmExternal))
}
func (x *Api) ListWorkflowTemplates() ([]WorkflowTemplate, error) {
	return r1(x.app.ListWorkflowTemplates(x.ctx))
}
func (x *Api) DryRunWorkflow(w Workflow, o *LimitOptions) (WorkflowDryRunResult, error) {
	n := 0
	if o != nil {
		n = o.Limit
	}
	return r1(x.app.DryRunWorkflow(x.ctx, w, n))
}
func (x *Api) ListWorkflowRuns(id string, o *LimitOptions) ([]WorkflowRun, error) {
	n := 0
	if o != nil {
		n = o.Limit
	}
	return r1(x.app.ListWorkflowRuns(x.ctx, id, n))
}
func (x *Api) ValidateWorkflow(src, mode string) (WorkflowValidation, error) {
	return r1(x.app.ValidateWorkflow(x.ctx, src, mode))
}
func (x *Api) CompleteWorkflow(src string, offset int, mode string) (WorkflowCompletion, error) {
	return r1(x.app.CompleteWorkflow(x.ctx, src, offset, mode))
}

// Portable vault

func (x *Api) VaultStatus() (VaultStatus, error) { return r1(x.app.VaultStatus(x.ctx)) }
func (x *Api) UnlockVault(password string) error { return werr(x.app.UnlockVault(x.ctx, password)) }
