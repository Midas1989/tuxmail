// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WailsTransport: desktop build. Wails v2 exposes bound Go structs as
// `window.go.<package>.<Struct>.<Method>(...args): Promise<T>` and runtime
// events as `window.runtime.EventsOn(name, cb) => unsubscribe`.
//
// The bound Go struct is internal/app.Api (package app, struct Api); its
// method names are exactly the ones called below. Errors arrive as JSON
// {"code","message"}. The core emits every ApiEvent on the runtime event
// "tuxmail:event". Server-relative URLs (/api/messages/{id}/attachments/…)
// are served by the desktop asset handler.

import type { Api, ConversationQuery, ConversationRef, ItemsQuery, MessageQuery, TransportKind, Unsubscribe } from './api';
import { ApiError, EventHub } from './api';
// --- OAuth/Cloud accounts ---
import type {
  CloudPimResult,
  IdentityDelegation,
  OAuthClientInput,
  OAuthFlowStatus,
  OAuthProviderInfo,
  OAuthStartInput,
  SharedMailboxInput,
} from '../types';
import type {
  ItemRef,
  Project,
  ProjectInput,
  ProjectStatus,
  ProjectTemplate,
  ProjectTimelineEntry,
  SearchFolder,
  SearchFolderInput,
  Tag,
  TagCount,
  TagInput,
  Workflow,
  WorkflowCompletion,
  WorkflowDryRunResult,
  WorkflowRun,
  WorkflowSchema,
  WorkflowTemplate,
  WorkflowValidation,
Account,
  ApiEvent,
  Channel,
  Conversation,
  ConversationGrouping,
  ConversationItem,
  ContactCard,
  AiSettings,
  AiSummary,
  ChatDraft,
  ChatFolder,
  FocusSettings,
  ScheduledMessage,
  Snippet,
  StarredItem,
  TimeSlot,
  ConversationPatch,
  Reaction,
  DeviceAuthorization,
  DeviceTokenResult,
  DiscoveryResult,
  Flag,
  Folder,
  Identity,
  LocaleInfo,
  MessageDetail,
  MessageSummary,
  NewAccountInput,
  NotificationSettings,
  OutgoingMessage,
  Page,
  Person,
  ProvisioningImport,
  VaultStatus,
  PushSubscriptionInput,
  SearchHistoryEntry,
  SearchProfile,
  SearchQuery,
  SearchResult,
  SendResult,
  ServerLoginInput,
  Signature,
  UserPreferences,
} from '../types';

// --- Cloud ---
import type {
  CloudAccount,
  CloudEntry,
  CloudImportResult,
  CloudListing,
  CloudLoginFlow,
  CloudLoginInput,
  CloudQuota,
  CloudSaveInput,
  CloudShare,
  CloudShareInput,
} from '../types';

type BoundFn = (...args: unknown[]) => Promise<unknown>;

export interface WailsWindow {
  go?: Record<string, Record<string, Record<string, BoundFn>>>;
  runtime?: {
    EventsOn?: (name: string, cb: (...data: unknown[]) => void) => () => void;
    BrowserOpenURL?: (url: string) => void;
  };
}

export const WAILS_EVENT = 'tuxmail:event';

export class WailsTransport implements Api {
  readonly kind: TransportKind = 'wails';
  private readonly hub = new EventHub();
  private offRuntime: (() => void) | null = null;

  constructor(
    private readonly win: WailsWindow = globalThis as unknown as WailsWindow,
    private readonly pkg = 'app',
    private readonly struct = 'Api',
  ) {}

  private async call<T>(method: string, ...args: unknown[]): Promise<T> {
    const fn = this.win.go?.[this.pkg]?.[this.struct]?.[method];
    if (typeof fn !== 'function') {
      throw new ApiError(`Wails binding ${this.pkg}.${this.struct}.${method} not available`, 0, 'not_bound');
    }
    try {
      return (await fn(...args)) as T;
    } catch (err) {
      // Wails rejects with the Go error string; the core encodes it as {"code","message"}.
      const text = typeof err === 'string' ? err : String(err);
      try {
        const j = JSON.parse(text) as { code?: string; message?: string };
        if (j && typeof j.code === 'string') throw new ApiError(j.message ?? j.code, 0, j.code);
      } catch (e) {
        if (e instanceof ApiError) throw e;
      }
      throw new ApiError(text, 0, 'backend');
    }
  }

  listAccounts(): Promise<Account[]> {
    return this.call('ListAccounts');
  }
  listIdentities(): Promise<Identity[]> {
    return this.call('ListIdentities');
  }
  listSignatures(): Promise<Signature[]> {
    return this.call('ListSignatures');
  }
  listPersons(): Promise<Person[]> {
    return this.call('ListPersons');
  }
  discoverAccount(email: string, password: string): Promise<DiscoveryResult> {
    return this.call('DiscoverAccount', email, password);
  }
  createAccount(input: NewAccountInput): Promise<Account> {
    return this.call('CreateAccount', input);
  }
  listFolders(accountId: number): Promise<Folder[]> {
    return this.call('ListFolders', accountId);
  }
  listMessages(query: MessageQuery): Promise<Page<MessageSummary>> {
    return this.call('ListMessages', query);
  }
  getMessage(id: number): Promise<MessageDetail> {
    return this.call('GetMessage', id);
  }
  listConversations(query: ConversationQuery): Promise<Page<Conversation>> {
    return this.call('ListConversations', query);
  }
  getConversation(id: number): Promise<Conversation> {
    return this.call('GetConversation', id);
  }
  listConversationItems(conversationId: number, query: ItemsQuery = {}): Promise<ConversationItem[]> {
    return this.call('ListConversationItems', conversationId, query);
  }
  conversationForMessage(messageId: number, grouping: ConversationGrouping): Promise<ConversationRef> {
    return this.call('ConversationForMessage', messageId, grouping);
  }
  markConversationRead(conversationId: number): Promise<void> {
    return this.call('MarkConversationRead', conversationId);
  }
  updateConversation(conversationId: number, patch: ConversationPatch): Promise<Conversation> {
    return this.call('UpdateConversation', conversationId, patch);
  }
  deleteConversation(conversationId: number): Promise<void> {
    return this.call('DeleteConversation', conversationId);
  }
  reactToItem(conversationId: number, itemId: string, emoji: string): Promise<{ reactions: Reaction[]; sentAsRfc9078?: boolean }> {
    return this.call('ReactToItem', conversationId, itemId, emoji);
  }
  deleteItem(conversationId: number, itemId: string): Promise<void> {
    return this.call('DeleteItem', conversationId, itemId);
  }
  forwardItem(itemId: string, targetConversationId: number): Promise<SendResult> {
    return this.call('ForwardItem', itemId, targetConversationId);
  }
  createFromItem(conversationId: number, itemId: string, kind: 'task' | 'event'): Promise<{ id: string }> {
    return this.call('CreateFromItem', conversationId, itemId, kind);
  }
  saveContactCard(personId: number, card: ContactCard): Promise<void> {
    return this.call('SaveContactCard', personId, card);
  }
  saveSignature(signature: Signature): Promise<Signature> {
    return this.call('SaveSignature', signature);
  }
  deleteSignature(id: number): Promise<void> {
    return this.call('DeleteSignature', id);
  }
  updateIdentity(identity: Identity): Promise<Identity> {
    return this.call('UpdateIdentity', identity);
  }
  send(message: OutgoingMessage): Promise<SendResult> {
    return this.call('Send', message);
  }
  async uploadAttachment(file: Blob, filename: string): Promise<{ uploadToken: string }> {
    // TODO(wails): large files should use the native file dialog + path
    // instead of shipping bytes over the IPC bridge.
    const buf = new Uint8Array(await file.arrayBuffer());
    let bin = '';
    for (let i = 0; i < buf.length; i += 0x8000) bin += String.fromCharCode(...buf.subarray(i, i + 0x8000));
    return this.call('UploadAttachment', filename, btoa(bin));
  }
  move(messageIds: number[], toFolderId: number): Promise<void> {
    return this.call('Move', messageIds, toFolderId);
  }
  setFlags(messageIds: number[], add: Flag[], remove: Flag[]): Promise<void> {
    return this.call('SetFlags', messageIds, add, remove);
  }

  getPushVapidKey(): Promise<string> {
    // Desktop uses native notifications; Web Push is only meaningful in server mode.
    return this.call('GetPushVapidKey');
  }
  subscribePush(subscription: PushSubscriptionInput): Promise<void> {
    return this.call('SubscribePush', subscription);
  }
  unsubscribePush(endpoint: string): Promise<void> {
    return this.call('UnsubscribePush', endpoint);
  }
  getNotificationSettings(): Promise<NotificationSettings> {
    return this.call('GetNotificationSettings');
  }
  saveNotificationSettings(settings: NotificationSettings): Promise<NotificationSettings> {
    return this.call('SaveNotificationSettings', settings);
  }
  sendTestNotification(): Promise<void> {
    return this.call('SendTestNotification');
  }
  deviceAuthorize(serverUrl: string): Promise<DeviceAuthorization> {
    return this.call('DeviceAuthorize', serverUrl);
  }
  deviceToken(serverUrl: string, deviceCode: string): Promise<DeviceTokenResult> {
    return this.call('DeviceToken', serverUrl, deviceCode);
  }
  serverLogin(serverUrl: string, credentials: ServerLoginInput): Promise<DeviceTokenResult> {
    return this.call('ServerLogin', serverUrl, credentials);
  }
  importProvisioning(input: ProvisioningImport): Promise<void> {
    return this.call('ImportProvisioning', input);
  }
  vaultStatus(): Promise<VaultStatus> {
    return this.call('VaultStatus');
  }
  unlockVault(password: string): Promise<void> {
    return this.call('UnlockVault', password);
  }
  search(query: SearchQuery): Promise<SearchResult> {
    return this.call('Search', query);
  }
  listSearchProfiles(): Promise<SearchProfile[]> {
    return this.call('ListSearchProfiles');
  }
  saveSearchProfile(profile: SearchProfile): Promise<SearchProfile> {
    return this.call('SaveSearchProfile', profile);
  }
  deleteSearchProfile(id: string): Promise<void> {
    return this.call('DeleteSearchProfile', id);
  }
  getSearchHistory(): Promise<SearchHistoryEntry[]> {
    return this.call('GetSearchHistory');
  }
  clearSearchHistory(): Promise<void> {
    return this.call('ClearSearchHistory');
  }
  getPreferences(): Promise<UserPreferences> {
    return this.call('GetPreferences');
  }
  setPreferences(prefs: UserPreferences): Promise<UserPreferences> {
    return this.call('SetPreferences', prefs);
  }
  listLocales(): Promise<LocaleInfo[]> {
    return this.call('ListLocales');
  }
  getLocale(code: string): Promise<Record<string, unknown>> {
    return this.call('GetLocale', code);
  }

  listChatFolders(): Promise<ChatFolder[]> {
    return this.call('ListChatFolders');
  }
  saveChatFolder(folder: ChatFolder): Promise<ChatFolder> {
    return this.call('SaveChatFolder', folder);
  }
  deleteChatFolder(id: string): Promise<void> {
    return this.call('DeleteChatFolder', id);
  }
  setItemStarred(conversationId: number, itemId: string, starred: boolean): Promise<void> {
    return this.call('SetItemStarred', conversationId, itemId, starred);
  }
  setItemPinned(conversationId: number, itemId: string, pinned: boolean): Promise<void> {
    return this.call('SetItemPinned', conversationId, itemId, pinned);
  }
  listStarredItems(conversationId?: number): Promise<StarredItem[]> {
    return this.call('ListStarredItems', conversationId ?? 0);
  }
  getFocusSettings(): Promise<FocusSettings> {
    return this.call('GetFocusSettings');
  }
  saveFocusSettings(settings: FocusSettings): Promise<FocusSettings> {
    return this.call('SaveFocusSettings', settings);
  }
  frequentContacts(): Promise<number[]> {
    return this.call('FrequentContacts');
  }
  getDraft(conversationId: number): Promise<ChatDraft | null> {
    return this.call('GetDraft', conversationId);
  }
  saveDraft(conversationId: number, draft: ChatDraft | null): Promise<void> {
    return this.call('SaveDraft', conversationId, draft);
  }
  listScheduled(conversationId?: number): Promise<ScheduledMessage[]> {
    return this.call('ListScheduled', conversationId ?? 0);
  }
  updateScheduled(id: string, patch: { text?: string; sendAt?: string }): Promise<ScheduledMessage> {
    return this.call('UpdateScheduled', id, patch);
  }
  cancelScheduled(id: string): Promise<void> {
    return this.call('CancelScheduled', id);
  }
  sendScheduledNow(id: string): Promise<void> {
    return this.call('SendScheduledNow', id);
  }
  listSnippets(): Promise<Snippet[]> {
    return this.call('ListSnippets');
  }
  saveSnippet(snippet: Snippet): Promise<Snippet> {
    return this.call('SaveSnippet', snippet);
  }
  deleteSnippet(id: string): Promise<void> {
    return this.call('DeleteSnippet', id);
  }
  getAiSettings(): Promise<AiSettings> {
    return this.call('GetAiSettings');
  }
  saveAiSettings(settings: AiSettings): Promise<AiSettings> {
    return this.call('SaveAiSettings', settings);
  }
  aiSuggestReplies(conversationId: number): Promise<string[]> {
    return this.call('AiSuggestReplies', conversationId);
  }
  aiTranslate(conversationId: number, itemId: string, targetLang: string): Promise<{ text: string; sourceLang: string }> {
    return this.call('AiTranslate', conversationId, itemId, targetLang);
  }
  aiSummarize(conversationId: number): Promise<AiSummary> {
    return this.call('AiSummarize', conversationId);
  }
  findFreeSlots(query: { from: string; to: string; durationMin: number }): Promise<TimeSlot[]> {
    return this.call('FindFreeSlots', query);
  }
  proposeMeeting(conversationId: number, proposal: { title: string; durationMin: number; slots: TimeSlot[]; channel: Channel; identityId?: number; accountId?: number }): Promise<SendResult> {
    return this.call('ProposeMeeting', conversationId, proposal);
  }
  acceptMeetingSlot(conversationId: number, proposalId: string, slotStart: string): Promise<void> {
    return this.call('AcceptMeetingSlot', conversationId, proposalId, slotStart);
  }
  listTags(): Promise<TagCount[]> {
    return this.call('ListTags');
  }
  saveTag(tag: TagInput): Promise<Tag> {
    return this.call('SaveTag', tag);
  }
  deleteTag(id: number): Promise<void> {
    return this.call('DeleteTag', id);
  }
  mergeTags(sourceIds: number[], targetId: number): Promise<void> {
    return this.call('MergeTags', sourceIds, targetId);
  }
  setItemTags(items: ItemRef[], add: number[], remove: number[]): Promise<void> {
    return this.call('SetItemTags', items, add, remove);
  }
  listProjects(query: { status?: ProjectStatus[] } = {}): Promise<Project[]> {
    return this.call('ListProjects', query);
  }
  getProject(id: number): Promise<Project> {
    return this.call('GetProject', id);
  }
  saveProject(project: ProjectInput): Promise<Project> {
    return this.call('SaveProject', project);
  }
  deleteProject(id: number): Promise<void> {
    return this.call('DeleteProject', id);
  }
  listProjectTemplates(): Promise<ProjectTemplate[]> {
    return this.call('ListProjectTemplates');
  }
  addToProject(projectId: number, items: ItemRef[]): Promise<void> {
    return this.call('AddToProject', projectId, items);
  }
  removeFromProject(projectId: number, items: ItemRef[]): Promise<void> {
    return this.call('RemoveFromProject', projectId, items);
  }
  projectTimeline(projectId: number): Promise<ProjectTimelineEntry[]> {
    return this.call('ProjectTimeline', projectId);
  }
  applyProjectRules(projectId: number): Promise<{ added: number }> {
    return this.call('ApplyProjectRules', projectId);
  }
  setTaskDone(taskId: number, done: boolean): Promise<void> {
    return this.call('SetTaskDone', taskId, done);
  }
  listSearchFolders(): Promise<SearchFolder[]> {
    return this.call('ListSearchFolders');
  }
  saveSearchFolder(folder: SearchFolderInput): Promise<SearchFolder> {
    return this.call('SaveSearchFolder', folder);
  }
  deleteSearchFolder(id: number): Promise<void> {
    return this.call('DeleteSearchFolder', id);
  }
  describeWorkflows(): Promise<WorkflowSchema> {
    return this.call('DescribeWorkflows');
  }
  listWorkflows(query: { projectId?: number } = {}): Promise<Workflow[]> {
    return this.call('ListWorkflows', query);
  }
  getWorkflow(id: string): Promise<Workflow> {
    return this.call('GetWorkflow', id);
  }
  saveWorkflow(workflow: Workflow): Promise<Workflow> {
    return this.call('SaveWorkflow', workflow);
  }
  deleteWorkflow(id: string): Promise<void> {
    return this.call('DeleteWorkflow', id);
  }
  setWorkflowEnabled(id: string, enabled: boolean): Promise<Workflow> {
    return this.call('SetWorkflowEnabled', id, enabled);
  }
  listWorkflowTemplates(): Promise<WorkflowTemplate[]> {
    return this.call('ListWorkflowTemplates');
  }
  dryRunWorkflow(workflow: Workflow, options: { limit?: number } = {}): Promise<WorkflowDryRunResult> {
    return this.call('DryRunWorkflow', workflow, options);
  }
  listWorkflowRuns(id: string, options: { limit?: number } = {}): Promise<WorkflowRun[]> {
    return this.call('ListWorkflowRuns', id, options);
  }
  validateWorkflow(src: string, mode: 'rule' | 'yaml'): Promise<WorkflowValidation> {
    return this.call('ValidateWorkflow', src, mode);
  }
  completeWorkflow(src: string, offset: number, mode: 'rule' | 'yaml'): Promise<WorkflowCompletion> {
    return this.call('CompleteWorkflow', src, offset, mode);
  }

  // --- Import/Export/Backup ---
  // Uploads and downloads use the desktop asset handler (/api/io/...),
  // which serves the same routes as the web server.
  listIOFormats(): Promise<IO.IOFormats> {
    return this.call('ListIOFormats');
  }
  async uploadImportFile(file: Blob, filename: string): Promise<IO.IOUpload> {
    const res = await fetch('/api/io/uploads', { method: 'POST', body: file, headers: { 'X-Filename': encodeURIComponent(filename) } });
    if (!res.ok) throw new ApiError(res.statusText, res.status, 'upload_failed');
    return (await res.json()) as IO.IOUpload;
  }
  inspectImport(source: IO.IOSource): Promise<IO.ImportPreview> {
    return this.call('InspectImport', source);
  }
  startImport(request: IO.ImportRequest): Promise<IO.IOJob> {
    return this.call('StartImport', request);
  }
  startExport(request: IO.ExportRequest): Promise<IO.IOJob> {
    return this.call('StartExport', request);
  }
  listIOJobs(): Promise<IO.IOJob[]> {
    return this.call('ListIOJobs');
  }
  getIOJob(id: string): Promise<IO.IOJob> {
    return this.call('GetIOJob', id);
  }
  cancelIOJob(id: string): Promise<void> {
    return this.call('CancelIOJob', id);
  }
  ioDownloadUrl(token: string): string {
    return `/api/io/downloads/${encodeURIComponent(token)}`;
  }
  createBackup(request: IO.BackupRequest): Promise<IO.IOJob> {
    return this.call('CreateBackup', request);
  }
  inspectBackup(source: IO.BackupSource, passphrase = ''): Promise<IO.BackupInspection> {
    return this.call('InspectBackup', source, passphrase);
  }
  startRestore(request: IO.RestoreRequest): Promise<IO.IOJob> {
    return this.call('StartRestore', request);
  }
  listBackupSchedules(): Promise<IO.BackupSchedule[]> {
    return this.call('ListBackupSchedules');
  }
  saveBackupSchedule(schedule: IO.BackupScheduleInput): Promise<IO.BackupSchedule> {
    return this.call('SaveBackupSchedule', schedule);
  }
  deleteBackupSchedule(id: string): Promise<void> {
    return this.call('DeleteBackupSchedule', id);
  }
  runBackupSchedule(id: string): Promise<IO.IOJob> {
    return this.call('RunBackupSchedule', id);
  }
  listBackupArchives(scheduleId: string): Promise<IO.BackupArchive[]> {
    return this.call('ListBackupArchives', scheduleId);
  }

  subscribe(handler: (event: ApiEvent) => void): Unsubscribe {
    const off = this.hub.add(handler);
    if (!this.offRuntime && this.win.runtime?.EventsOn) {
      this.offRuntime = this.win.runtime.EventsOn(WAILS_EVENT, (data: unknown) => {
        if (data && typeof data === 'object' && 'type' in data) this.hub.emit(data as ApiEvent);
      });
    }
    return () => {
      off();
      if (this.hub.size === 0 && this.offRuntime) {
        this.offRuntime();
        this.offRuntime = null;
      }
    };
  }

  // --- Cloud ---
  listCloudAccounts(): Promise<CloudAccount[]> {
    return this.call('ListCloudAccounts');
  }
  startCloudLogin(input: CloudLoginInput): Promise<CloudLoginFlow> {
    return this.call('StartCloudLogin', input);
  }
  pollCloudLogin(flowId: string): Promise<CloudLoginFlow> {
    return this.call('PollCloudLogin', flowId);
  }
  cancelCloudLogin(flowId: string): Promise<void> {
    return this.call('CancelCloudLogin', flowId);
  }
  deleteCloudAccount(id: number): Promise<void> {
    return this.call('DeleteCloudAccount', id);
  }
  listCloudFiles(accountId: number, path: string): Promise<CloudListing> {
    return this.call('ListCloudFiles', accountId, path);
  }
  cloudQuota(accountId: number): Promise<CloudQuota> {
    return this.call('CloudQuota', accountId);
  }
  createCloudFolder(accountId: number, path: string): Promise<CloudEntry> {
    return this.call('CreateCloudFolder', accountId, path);
  }
  moveCloudFile(accountId: number, from: string, to: string): Promise<void> {
    return this.call('MoveCloudFile', accountId, from, to);
  }
  deleteCloudFile(accountId: number, path: string): Promise<void> {
    return this.call('DeleteCloudFile', accountId, path);
  }
  async uploadCloudFile(accountId: number, dir: string, file: Blob, filename: string): Promise<CloudEntry> {
    const { uploadToken } = await this.uploadAttachment(file, filename);
    return this.call('UploadCloudFile', accountId, dir, uploadToken);
  }
  async downloadCloudFile(accountId: number, path: string): Promise<Blob> {
    const d = await this.call<{ filename: string; mimeType: string; data: string }>('DownloadCloudFile', accountId, path);
    const bin = atob(d.data);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new Blob([bytes], { type: d.mimeType || 'application/octet-stream' });
  }
  importCloudFile(accountId: number, path: string): Promise<CloudImportResult> {
    return this.call('ImportCloudFile', accountId, path);
  }
  saveAttachmentToCloud(input: CloudSaveInput): Promise<CloudEntry> {
    return this.call('SaveAttachmentToCloud', input);
  }
  createCloudShare(input: CloudShareInput): Promise<CloudShare> {
    return this.call('CreateCloudShare', input);
  }
  listCloudShares(): Promise<CloudShare[]> {
    return this.call('ListCloudShares');
  }
  revokeCloudShare(id: number): Promise<CloudShare> {
    return this.call('RevokeCloudShare', id);
  }
  cloudSharePassword(id: number): Promise<{ password: string }> {
    return this.call('CloudSharePassword', id);
  }
  generateCloudPassword(accountId: number): Promise<{ password: string }> {
    return this.call('GenerateCloudPassword', accountId);
  }

  // --- OAuth/Cloud accounts --- (internal/app/oauth_api.go)
  listOAuthProviders(): Promise<OAuthProviderInfo[]> {
    return this.call('ListOAuthProviders');
  }
  saveOAuthClient(input: OAuthClientInput): Promise<void> {
    return this.call('SaveOAuthClient', input);
  }
  deleteOAuthClient(provider: string): Promise<void> {
    return this.call('DeleteOAuthClient', provider);
  }
  startOAuth(input: OAuthStartInput): Promise<OAuthFlowStatus> {
    return this.call('StartOAuth', input);
  }
  getOAuthFlow(id: string): Promise<OAuthFlowStatus> {
    return this.call('GetOAuthFlow', id);
  }
  cancelOAuth(id: string): Promise<void> {
    return this.call('CancelOAuth', id);
  }
  addSharedMailbox(input: SharedMailboxInput): Promise<Account> {
    return this.call('AddSharedMailbox', input);
  }
  listIdentityDelegations(): Promise<IdentityDelegation[]> {
    return this.call('ListIdentityDelegations');
  }
  setIdentityDelegation(d: { identityId: number; mode: string; mailbox?: string }): Promise<void> {
    return this.call('SetIdentityDelegation', d);
  }
  syncCloudPim(accountId: number): Promise<CloudPimResult> {
    return this.call('SyncCloudPim', accountId);
  }
}

// --- Import/Export/Backup ---
import type * as IO from '../types';
