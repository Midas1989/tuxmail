// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Transport-independent API contract between the frontend and the Go core
// (KONZEPT 4.2 #4, 4.5). The desktop build calls these methods through Wails
// bindings, the web client through HTTPS/JSON + a WebSocket for events.
// Method names map 1:1 to Go methods on the bound application service and to
// the REST routes documented in http.ts.

import type { AssignableRole, FolderBehaviour, FolderConfig } from '../types';
import type {
  Account,
  ApiEvent,
  Conversation,
  ConversationGrouping,
  ConversationItem,
  AiSettings,
  AiSummary,
  ChatDraft,
  ChatFilter,
  ChatFolder,
  FocusSettings,
  ScheduledMessage,
  Snippet,
  StarredItem,
  TimeSlot,
  Channel,
  ContactCard,
  ConversationPatch,
  Reaction,
  DeviceAuthorization,
  DeviceTokenResult,
  DiscoveryResult,
  Flag,
  LocaleInfo,
  NotificationSettings,
  SearchHistoryEntry,
  SearchProfile,
  SearchQuery,
  SearchResult,
  UserPreferences,
  VaultStatus,
  ProvisioningImport,
  PushSubscriptionInput,
  ServerLoginInput,
  Folder,
  Identity,
  MessageDetail,
  MessageSummary,
  NewAccountInput,
  OutgoingMessage,
  Page,
  Person,
  SendResult,
  Signature,
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
} from '../types';
import type * as pim from '../types';
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

export type TransportKind = 'wails' | 'http' | 'mock';

/** What the message list shows. */
export type MessageScope =
  | { kind: 'all' }
  | { kind: 'folder'; folderId: number }
  /** Virtual folder across all mail accounts (e.g. unified inbox). */
  | { kind: 'unified'; role: 'inbox' | 'sent' | 'drafts' | 'archive' | 'trash' | 'junk' | 'flagged' | 'unread' }
  /** Mails with this tag (or a descendant tag). */
  | { kind: 'tag'; tagId: number }
  /** Saved search shown as a virtual folder (read-only for moves). */
  | { kind: 'searchFolder'; searchFolderId: number };

export interface MessageQuery {
  scope: MessageScope;
  offset: number;
  limit: number;
  search?: string;
}

export type ConversationScope =
  | { kind: 'all' }
  | { kind: 'account'; accountId: number }
  | { kind: 'folder'; folderId: number }
  /** Hybrid view: conversations with at least one mail in a unified folder (all inboxes ...). */
  | { kind: 'unified'; role: 'inbox' | 'flagged' | 'unread' }
  | { kind: 'tag'; tagId: number }
  | { kind: 'searchFolder'; searchFolderId: number };

export interface ConversationQuery {
  scope: ConversationScope;
  grouping: ConversationGrouping;
  offset: number;
  limit: number;
  search?: string;
  /** Messenger filter pills. */
  filter?: ChatFilter;
  /** true = only archived chats, false/undefined = only non-archived. */
  archived?: boolean;
  /** User-defined chat folder. */
  folderId?: string;
  /** Focus mode: 'vip' = only important chats, 'waiting' = the others. */
  focus?: 'vip' | 'waiting';
  /** Include snoozed chats (normally hidden until they wake up). */
  includeSnoozed?: boolean;
  /** Tag filter pill in the messenger view (conversations with a tagged item). */
  tagId?: number;
}

export interface ItemsQuery {
  /** Return items older than this item id (paging backwards). */
  before?: string;
  limit?: number;
  /** Hybrid view: mark items inside this scope (ConversationItem.inScope). */
  scope?: ConversationScope;
}

export interface ConversationRef {
  conversationId: number;
  itemId: string;
}

export type Unsubscribe = () => void;

export interface Api {
  readonly kind: TransportKind;

  // Accounts & identities
  listAccounts(): Promise<Account[]>;
  listIdentities(): Promise<Identity[]>;
  listSignatures(): Promise<Signature[]>;
  listPersons(): Promise<Person[]>;
  discoverAccount(email: string, password: string): Promise<DiscoveryResult>;
  createAccount(input: NewAccountInput): Promise<Account>;

  // Folders & classic list
  listFolders(accountId: number): Promise<Folder[]>;
  listMessages(query: MessageQuery): Promise<Page<MessageSummary>>;
  getMessage(id: number): Promise<MessageDetail>;

  // Conversations (KONZEPT 6)
  listConversations(query: ConversationQuery): Promise<Page<Conversation>>;
  getConversation(id: number): Promise<Conversation>;
  listConversationItems(conversationId: number, query?: ItemsQuery): Promise<ConversationItem[]>;
  /** Map a mail to the conversation (and bubble) it belongs to for the given grouping. */
  conversationForMessage(messageId: number, grouping: ConversationGrouping): Promise<ConversationRef>;
  markConversationRead(conversationId: number): Promise<void>;
  /** Pin, mute, archive (archives the underlying mails), favorite, mark unread, block. */
  updateConversation(conversationId: number, patch: ConversationPatch): Promise<Conversation>;
  /** Delete a chat: mails go to the trash of their account, messenger messages are deleted. */
  deleteConversation(conversationId: number): Promise<void>;
  /** Toggle an emoji reaction of the current user. Mail: stored locally, sent as RFC 9078 reaction when supported. */
  reactToItem(conversationId: number, itemId: string, emoji: string): Promise<{ reactions: Reaction[]; sentAsRfc9078?: boolean }>;
  deleteItem(conversationId: number, itemId: string): Promise<void>;
  /** Forward a bubble to another conversation (core picks the channel). */
  forwardItem(itemId: string, targetConversationId: number): Promise<SendResult>;
  /** "Als Aufgabe / Termin". */
  createFromItem(conversationId: number, itemId: string, kind: 'task' | 'event'): Promise<{ id: string }>;
  /** Create/update the contact of a person from a parsed signature. */
  saveContactCard(personId: number, card: ContactCard): Promise<void>;
  /** Create (id 0) or update a signature. */
  saveSignature(signature: Signature): Promise<Signature>;
  deleteSignature(id: number): Promise<void>;
  /** Update chat-mode signature settings of an identity. */
  updateIdentity(identity: Identity): Promise<Identity>;

  // Actions (local first, queued by the core's op log)
  send(message: OutgoingMessage): Promise<SendResult>;
  uploadAttachment(file: Blob, filename: string): Promise<{ uploadToken: string }>;
  move(messageIds: number[], toFolderId: number): Promise<void>;
  setFlags(messageIds: number[], add: Flag[], remove: Flag[]): Promise<void>;

  // Push notifications (web/PWA; desktop uses native notifications)
  getPushVapidKey(): Promise<string>;
  subscribePush(subscription: PushSubscriptionInput): Promise<void>;
  unsubscribePush(endpoint: string): Promise<void>;
  getNotificationSettings(): Promise<NotificationSettings>;
  saveNotificationSettings(settings: NotificationSettings): Promise<NotificationSettings>;
  /** Ask the server to send a test push to all subscriptions of this user. */
  sendTestNotification(): Promise<void>;

  // Connect this client to a TuxMail server (desktop auto-setup)
  deviceAuthorize(serverUrl: string): Promise<DeviceAuthorization>;
  deviceToken(serverUrl: string, deviceCode: string): Promise<DeviceTokenResult>;
  serverLogin(serverUrl: string, credentials: ServerLoginInput): Promise<DeviceTokenResult>;
  importProvisioning(input: ProvisioningImport): Promise<void>;

  // Portable mode (KONZEPT 15.2): secrets live in a master-password vault
  /** End the server session (POST /api/auth/logout; cookie or device token). No-op where no server session exists (desktop, demo). */
  logout(): Promise<void>;
  vaultStatus(): Promise<VaultStatus>;
  /** Unlock (or on first use create) the vault; error code "invalid_password". */
  unlockVault(password: string): Promise<void>;

  // Search (KONZEPT 8.4)
  search(query: SearchQuery): Promise<SearchResult>;
  listSearchProfiles(): Promise<SearchProfile[]>;
  saveSearchProfile(profile: SearchProfile): Promise<SearchProfile>;
  deleteSearchProfile(id: string): Promise<void>;
  getSearchHistory(): Promise<SearchHistoryEntry[]>;
  clearSearchHistory(): Promise<void>;

  // User preferences synced with the core (language, time zone, formats)
  getPreferences(): Promise<UserPreferences>;
  setPreferences(prefs: UserPreferences): Promise<UserPreferences>;

  // Additional / override language files delivered by the core
  listLocales(): Promise<LocaleInfo[]>;
  /** Nested JSON in the same shape as locales/de.json. */
  getLocale(code: string): Promise<Record<string, unknown>>;

  // Chat folders (Telegram-style lists; independent of mail folders)
  listChatFolders(): Promise<ChatFolder[]>;
  saveChatFolder(folder: ChatFolder): Promise<ChatFolder>;
  deleteChatFolder(id: string): Promise<void>;

  // Starred & pinned messages
  setItemStarred(conversationId: number, itemId: string, starred: boolean): Promise<void>;
  /** Max. three pinned messages per chat (error code "pin_limit"). */
  setItemPinned(conversationId: number, itemId: string, pinned: boolean): Promise<void>;
  listStarredItems(conversationId?: number): Promise<StarredItem[]>;

  // Focus mode
  getFocusSettings(): Promise<FocusSettings>;
  saveFocusSettings(settings: FocusSettings): Promise<FocusSettings>;
  /** People I write with frequently (focus mode option). */
  frequentContacts(): Promise<number[]>;

  // Drafts & scheduled sending (OutgoingMessage.sendAt)
  getDraft(conversationId: number): Promise<ChatDraft | null>;
  saveDraft(conversationId: number, draft: ChatDraft | null): Promise<void>;
  listScheduled(conversationId?: number): Promise<ScheduledMessage[]>;
  updateScheduled(id: string, patch: { text?: string; sendAt?: string }): Promise<ScheduledMessage>;
  cancelScheduled(id: string): Promise<void>;
  sendScheduledNow(id: string): Promise<void>;

  // Quick replies ("/")
  listSnippets(): Promise<Snippet[]>;
  saveSnippet(snippet: Snippet): Promise<Snippet>;
  deleteSnippet(id: string): Promise<void>;

  // AI assistance (KONZEPT 8.10); errors with code "ai_not_configured" when no provider is set
  getAiSettings(): Promise<AiSettings>;
  saveAiSettings(settings: AiSettings): Promise<AiSettings>;
  aiSuggestReplies(conversationId: number): Promise<string[]>;
  aiTranslate(conversationId: number, itemId: string, targetLang: string): Promise<{ text: string; sourceLang: string }>;
  aiSummarize(conversationId: number): Promise<AiSummary>;

  // Meeting proposals
  findFreeSlots(query: { from: string; to: string; durationMin: number }): Promise<TimeSlot[]>;
  proposeMeeting(conversationId: number, proposal: { title: string; durationMin: number; slots: TimeSlot[]; channel: Channel; identityId?: number; accountId?: number }): Promise<SendResult>;
  /** The recipient chose a slot (in the demo: click on the slot in your own proposal). Creates the event. */
  acceptMeetingSlot(conversationId: number, proposalId: string, slotStart: string): Promise<void>;

  // Tags (hierarchical "Kunde/Müller"; synced as IMAP keywords / labels / categories)
  listTags(): Promise<TagCount[]>;
  /** Create (no id) or update/rename (with id; a changed path moves the subtree). */
  saveTag(tag: TagInput): Promise<Tag>;
  /** Deletes the tag and its descendants; assignments are removed. */
  deleteTag(id: number): Promise<void>;
  /** Re-tag everything tagged with a source tag with the target, then delete the sources. */
  mergeTags(sourceIds: number[], targetId: number): Promise<void>;
  /** Bulk (un)assign tags to items (mails, chat messages, conversations, events ...). */
  setItemTags(items: ItemRef[], add: number[], remove: number[]): Promise<void>;

  // Projects
  listProjects(query?: { status?: ProjectStatus[] }): Promise<Project[]>;
  getProject(id: number): Promise<Project>;
  /** Create (no id) or update. Creating from a template binds its workflows. */
  saveProject(project: ProjectInput): Promise<Project>;
  deleteProject(id: number): Promise<void>;
  listProjectTemplates(): Promise<ProjectTemplate[]>;
  addToProject(projectId: number, items: ItemRef[]): Promise<void>;
  removeFromProject(projectId: number, items: ItemRef[]): Promise<void>;
  /** All linked mails, chats, events, tasks and files, newest first. */
  projectTimeline(projectId: number): Promise<ProjectTimelineEntry[]>;
  /** Run the auto-assign rule now; returns the number of newly linked items. */
  applyProjectRules(projectId: number): Promise<{ added: number }>;
  setTaskDone(taskId: number, done: boolean): Promise<void>;

  // Search folders
  listSearchFolders(): Promise<SearchFolder[]>;
  saveSearchFolder(folder: SearchFolderInput): Promise<SearchFolder>;
  deleteSearchFolder(id: number): Promise<void>;

  // Workflows (KONZEPT 8.5)
  /** Triggers, fields, operators, actions and variables for the editors. */
  describeWorkflows(): Promise<WorkflowSchema>;
  listWorkflows(query?: { projectId?: number }): Promise<Workflow[]>;
  getWorkflow(id: string): Promise<Workflow>;
  saveWorkflow(workflow: Workflow): Promise<Workflow>;
  deleteWorkflow(id: string): Promise<void>;
  /** Enabling a workflow with external effects needs confirmExternal (else `confirmation_required`). */
  setWorkflowEnabled(id: string, enabled: boolean, confirmExternal?: boolean): Promise<Workflow>;
  listWorkflowTemplates(): Promise<WorkflowTemplate[]>;
  /** Evaluate against recent sample items without side effects. */
  dryRunWorkflow(workflow: Workflow, options?: { limit?: number }): Promise<WorkflowDryRunResult>;
  listWorkflowRuns(id: string, options?: { limit?: number }): Promise<WorkflowRun[]>;
  /** Parse + check source text ("rule" one-line syntax or YAML); diagnostics carry line/col. */
  validateWorkflow(src: string, mode: 'rule' | 'yaml'): Promise<WorkflowValidation>;
  /** Context-aware completion at a 0-based offset. */
  completeWorkflow(src: string, offset: number, mode: 'rule' | 'yaml'): Promise<WorkflowCompletion>;

  // --- Folder config ---
  getFolderConfig(accountId: number): Promise<FolderConfig>;
  /** folderId 0 removes the override (automatic detection applies). */
  setFolderRole(accountId: number, role: AssignableRole, folderId: number): Promise<FolderConfig>;
  /** Remove all overrides ("detect automatically"). */
  resetFolderRoles(accountId: number): Promise<FolderConfig>;
  /** Create a missing role folder on the server and assign the role. */
  createRoleFolder(accountId: number, role: AssignableRole, name: string): Promise<FolderConfig>;
  saveFolderBehaviour(accountId: number, behaviour: FolderBehaviour): Promise<FolderConfig>;
  /** 0 = the account's Sent / Drafts folder. */
  setIdentityFolders(identityId: number, sentFolderId: number, draftsFolderId: number): Promise<FolderConfig>;
  /** Permanently remove messages marked as deleted; returns their number. */
  expungeFolder(folderId: number): Promise<number>;

  // Live events
  subscribe(handler: (event: ApiEvent) => void): Unsubscribe;

  // --- PIM --- calendars, tasks, contacts (KONZEPT 9, 10)
  listCalendars(): Promise<pim.PimCalendar[]>;
  /** Create (no id: local calendar or ICS subscription) or update name/color/visibility/default. */
  saveCalendar(input: pim.CalendarInput): Promise<pim.PimCalendar>;
  deleteCalendar(id: number): Promise<void>;
  /** Sync CalDAV/CardDAV accounts and subscriptions now. */
  syncCalendars(): Promise<void>;
  /** Import an .ics file into a calendar; returns the number of objects. */
  importCalendarFile(calendarId: number, ics: string): Promise<{ added: number }>;
  /** Occurrences in [from, to) of visible calendars (recurrences expanded, birthdays, unanswered invitations). */
  listEvents(query: pim.EventQuery): Promise<pim.CalendarEvent[]>;
  getEvent(id: string): Promise<pim.CalendarEvent>;
  createEvent(input: pim.EventInput): Promise<pim.CalendarEvent>;
  /** scope applies to recurring events (this occurrence, this and following, the whole series). */
  updateEvent(id: string, input: pim.EventInput, scope: pim.RecurrenceScope): Promise<pim.CalendarEvent>;
  deleteEvent(id: string, scope: pim.RecurrenceScope): Promise<void>;
  /** Keep (push as new event) or discard a local conflict copy. */
  resolveEventConflict(objectId: number, keep: boolean): Promise<void>;
  getFreeBusy(query: { attendees: string[]; from: string; to: string }): Promise<pim.FreeBusy[]>;
  /** minutes <= 0 dismisses the reminder. */
  snoozeReminder(eventId: string, trigger: string, minutes: number): Promise<void>;
  addDavAccount(input: pim.DavAccountInput): Promise<Account>;
  listTasks(query: pim.TaskQuery): Promise<pim.PimTask[]>;
  saveTask(input: pim.TaskInput): Promise<pim.PimTask>;
  deleteTask(id: number): Promise<void>;
  /** iTIP parts of a mail (invitation card in the reading pane / bubbles). */
  getMessageInvitations(messageId: number): Promise<pim.Invitation[]>;
  listInvitations(): Promise<pim.Invitation[]>;
  respondInvitation(id: number, response: pim.InvitationResponse): Promise<pim.Invitation>;
  listAddressBooks(): Promise<pim.PimAddressBook[]>;
  saveAddressBook(input: pim.AddressBookInput): Promise<pim.PimAddressBook>;
  deleteAddressBook(id: number): Promise<void>;
  listContacts(query: pim.ContactQuery): Promise<pim.PimContact[]>;
  getContact(id: number): Promise<pim.PimContact>;
  /** Create (id 0) or update. */
  saveContact(contact: pim.PimContact): Promise<pim.PimContact>;
  deleteContact(id: number): Promise<void>;
  listContactGroups(): Promise<pim.ContactGroup[]>;
  findDuplicateContacts(): Promise<pim.DuplicateGroup[]>;
  /** Merge into ids[0]; merged replaces the computed preview (edited in the dialog). */
  mergeContacts(ids: number[], merged?: pim.PimContact): Promise<pim.PimContact>;
  /** Composer recipient suggestions from contacts and recently used addresses. */
  autocompleteAddresses(query: string, limit?: number): Promise<pim.AddressSuggestion[]>;
  // --- Import/Export/Backup --- (KONZEPT 13/14; Go: internal/app/io_*.go)
  listIOFormats(): Promise<IO.IOFormats>;
  /** Upload an import file or backup archive (web mode; desktop may use local paths). */
  uploadImportFile(file: Blob, filename: string): Promise<IO.IOUpload>;
  /** Wizard step "mapping/options": counts, CSV header + suggested mapping, Thunderbird profile. */
  inspectImport(source: IO.IOSource): Promise<IO.ImportPreview>;
  startImport(request: IO.ImportRequest): Promise<IO.IOJob>;
  startExport(request: IO.ExportRequest): Promise<IO.IOJob>;
  listIOJobs(): Promise<IO.IOJob[]>;
  getIOJob(id: string): Promise<IO.IOJob>;
  /** Cancel a running job (imports can be resumed with resumeJobId). */
  cancelIOJob(id: string): Promise<void>;
  /** URL of an export result or backup download. */
  ioDownloadUrl(token: string): string;
  createBackup(request: IO.BackupRequest): Promise<IO.IOJob>;
  inspectBackup(source: IO.BackupSource, passphrase?: string): Promise<IO.BackupInspection>;
  startRestore(request: IO.RestoreRequest): Promise<IO.IOJob>;
  listBackupSchedules(): Promise<IO.BackupSchedule[]>;
  saveBackupSchedule(schedule: IO.BackupScheduleInput): Promise<IO.BackupSchedule>;
  deleteBackupSchedule(id: string): Promise<void>;
  runBackupSchedule(id: string): Promise<IO.IOJob>;
  listBackupArchives(scheduleId: string): Promise<IO.BackupArchive[]>;
  // --- OAuth/Cloud accounts --- (KONZEPT 7.3–7.5; error codes oauth_not_configured, oauth_consent_required ...)
  /** Sign-in services (Microsoft, Google) and their client configuration. */
  listOAuthProviders(): Promise<OAuthProviderInfo[]>;
  /** Own client ID ("Anmeldedienste"); may be forbidden by the server admin. */
  saveOAuthClient(input: OAuthClientInput): Promise<void>;
  deleteOAuthClient(provider: string): Promise<void>;
  /** Start a sign-in: the desktop opens the browser (loopback), the web client opens status.authUrl. */
  startOAuth(input: OAuthStartInput): Promise<OAuthFlowStatus>;
  /** Poll until status !== 'pending'; on 'done' the account exists (accountId). */
  getOAuthFlow(id: string): Promise<OAuthFlowStatus>;
  cancelOAuth(id: string): Promise<void>;
  /** Microsoft: add a shared/delegated mailbox of a Graph/EWS account. */
  addSharedMailbox(input: SharedMailboxInput): Promise<Account>;
  listIdentityDelegations(): Promise<IdentityDelegation[]>;
  /** Change Send-As / Send-on-Behalf of an identity (mode '' removes it). */
  setIdentityDelegation(delegation: { identityId: number; mode: string; mailbox?: string }): Promise<void>;
  /** Import calendars, contacts and tasks of a Graph/Gmail/EWS account. */
  syncCloudPim(accountId: number): Promise<CloudPimResult>;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** Seconds from the Retry-After header of a 429 answer (server mode), when present. */
  readonly retryAfter?: number;
  /** Set at start: the wording of codes that users see in every dialog (server texts are English). */
  static translate: ((code: string) => string | undefined) | undefined;
  constructor(message: string, status = 0, code = 'error', retryAfter?: number) {
    super(ApiError.translate?.(code) ?? message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    if (retryAfter !== undefined) this.retryAfter = retryAfter;
  }
}

/** Small helper all transports use to fan out events. */
export class EventHub {
  private handlers = new Set<(event: ApiEvent) => void>();

  get size(): number {
    return this.handlers.size;
  }

  add(handler: (event: ApiEvent) => void): Unsubscribe {
    this.handlers.add(handler);
    return () => this.handlers.delete(handler);
  }

  emit(event: ApiEvent): void {
    for (const h of this.handlers) {
      try {
        h(event);
      } catch (err) {
        console.error('event handler failed', err);
      }
    }
  }
}

// --- Messenger ---
// Messenger plugins (KONZEPT 11); merged into the Api interface above.
// Live updates arrive as ApiEvents messenger.plugin / messenger.pairing /
// messenger.typing; chats and messages use the conversation methods.
import type { MessengerAccountState, MessengerPairing, MessengerPairingInput, MessengerPlugin, MessengerPluginPatch } from '../types';

export interface Api {
  listMessengerPlugins(): Promise<MessengerPlugin[]>;
  /** Enable/disable (enabling WhatsApp needs acceptRisk once) or change settings. */
  updateMessengerPlugin(name: string, patch: MessengerPluginPatch): Promise<MessengerPlugin>;
  restartMessengerPlugin(name: string): Promise<void>;
  /** Downloads and installs the external program of a plugin (signal-cli) in the background; progress via messenger.plugin events. */
  installMessengerComponent(name: string): Promise<void>;
  startMessengerPairing(input: MessengerPairingInput): Promise<MessengerPairing>;
  getMessengerPairing(id: string): Promise<MessengerPairing>;
  submitMessengerPairingInput(id: string, field: string, value: string): Promise<void>;
  cancelMessengerPairing(id: string): Promise<void>;
  listMessengerAccounts(): Promise<MessengerAccountState[]>;
  removeMessengerAccount(accountId: number): Promise<void>;
  syncMessengerAccount(accountId: number): Promise<void>;
  setMessengerTyping(conversationId: number, active: boolean): Promise<void>;
}

/** Download URL of the media of a messenger chat message. */
export function messengerMediaUrl(chatMessageId: number): string {
  return `/api/messenger/media/${chatMessageId}`;
}

// --- Import/Export/Backup ---
import type * as IO from '../types';
// --- Cloud ---
// Cloud files (KONZEPT 12). Declared as a second part of the Api interface
// (declaration merging) so that parallel feature branches merge cleanly.

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


export interface Api {
  listCloudAccounts(): Promise<CloudAccount[]>;
  /** Nextcloud/OpenCloud: a pending flow with loginUrl (open in the browser, then poll). Vaultwarden/WebDAV: done or two_factor. */
  startCloudLogin(input: CloudLoginInput): Promise<CloudLoginFlow>;
  pollCloudLogin(flowId: string): Promise<CloudLoginFlow>;
  cancelCloudLogin(flowId: string): Promise<void>;
  deleteCloudAccount(id: number): Promise<void>;
  listCloudFiles(accountId: number, path: string): Promise<CloudListing>;
  cloudQuota(accountId: number): Promise<CloudQuota>;
  createCloudFolder(accountId: number, path: string): Promise<CloudEntry>;
  moveCloudFile(accountId: number, from: string, to: string): Promise<void>;
  deleteCloudFile(accountId: number, path: string): Promise<void>;
  /** Upload a local file into dir (unique name on conflicts). */
  uploadCloudFile(accountId: number, dir: string, file: Blob, filename: string): Promise<CloudEntry>;
  downloadCloudFile(accountId: number, path: string): Promise<Blob>;
  /** Copy a cloud file into an upload for the composer ("Attach from cloud"). */
  importCloudFile(accountId: number, path: string): Promise<CloudImportResult>;
  /** "Save to cloud" for attachments of mails and chats. */
  saveAttachmentToCloud(input: CloudSaveInput): Promise<CloudEntry>;
  createCloudShare(input: CloudShareInput): Promise<CloudShare>;
  /** "My shares": all links, newest first. */
  listCloudShares(): Promise<CloudShare[]>;
  revokeCloudShare(id: number): Promise<CloudShare>;
  cloudSharePassword(id: number): Promise<{ password: string }>;
  generateCloudPassword(accountId: number): Promise<{ password: string }>;
}

// --- Diagnostics ---
// Sync status and Settings → Info & Diagnose (declaration merging, see Cloud).

import type { AccountSyncStatus, DiagnosticsInfo } from '../types';

export interface Api {
  /** Sync status of all mail accounts (updates arrive as sync.status events). */
  syncStatus(): Promise<AccountSyncStatus[]>;
  /** Sync an account now (also cuts the retry wait after an error short). */
  syncNow(accountId: number): Promise<void>;
  diagnostics(): Promise<DiagnosticsInfo>;
  /** Raw RFC 5322 source of a mail (mail window "Quelltext"). */
  messageSource(id: number): Promise<string>;
  /** Zip with logs and the redacted configuration (no secrets, no mail content). */
  /** savedPath: desktop, where the archive was written (data_dir/diagnostics). */
  exportDiagnostics(): Promise<{ filename: string; blob: Blob; savedPath?: string }>;
}

// --- Mail protocols ---
// Remote content and server rules (KONZEPT 8.3, 8.5), another part of the
// Api interface (declaration merging).

import type { RemoteAllowEntry, RemoteContent, SieveCheckResult, SieveConfig, SieveScript, SieveStatus, VacationSettings } from '../types';

export interface Api {
  /** Re-render a mail with remote images through the image proxy ("Bilder laden"). */
  loadRemoteContent(messageId: number): Promise<RemoteContent>;
  listRemoteAllow(): Promise<RemoteAllowEntry[]>;
  /** "Immer für Absender laden": kind sender (address) or domain. */
  allowRemoteContent(kind: 'sender' | 'domain', value: string): Promise<void>;
  removeRemoteAllow(kind: 'sender' | 'domain', value: string): Promise<void>;
  getSieveStatus(accountId: number): Promise<SieveStatus>;
  getSieveScript(accountId: number, name: string): Promise<SieveScript>;
  checkSieveScript(accountId: number, script: string): Promise<SieveCheckResult>;
  putSieveScript(accountId: number, name: string, script: string, activate: boolean): Promise<void>;
  /** Activate a script ('' deactivates all). */
  activateSieveScript(accountId: number, name: string): Promise<void>;
  deleteSieveScript(accountId: number, name: string): Promise<void>;
  setSieveWorkflowSync(accountId: number, enabled: boolean): Promise<SieveStatus>;
  getSieveConfig(accountId: number): Promise<SieveConfig>;
  setSieveConfig(accountId: number, config: SieveConfig): Promise<SieveConfig>;
  /** Out-of-office: pushed to servers that can answer themselves, local replies otherwise. */
  getVacationSettings(): Promise<VacationSettings>;
  saveVacationSettings(settings: VacationSettings): Promise<VacationSettings>;
}

// --- Account edit ---
// "Konto bearbeiten" (Settings -> Konten; Go: internal/app/account_edit.go),
// another part of the Api interface (declaration merging).

import type { AccountDiagnostics, AccountSettings, AccountUpdate, ConnectionTestInput, ConnectionTestResult, Identity as AccountIdentity, MailStorageEstimate, MailStorageSettings } from '../types';

export interface Api {
  getAccountSettings(accountId: number): Promise<AccountSettings>;
  /** Validate and save; restarts the sync worker. verify=true logs in first (error code "connection_failed"). */
  updateAccount(accountId: number, update: AccountUpdate): Promise<AccountSettings>;
  /** Store a new password in the secret store (verify: test login first). */
  changeAccountPassword(accountId: number, password: string, verify: boolean): Promise<AccountSettings>;
  /** Log in to IMAP/POP3/JMAP/EAS/EWS/Graph/SMTP/Sieve/CalDAV/CardDAV without changing anything. */
  testAccountConnection(accountId: number, input: ConnectionTestInput): Promise<ConnectionTestResult>;
  /** "Jetzt synchronisieren" / "Erneut versuchen". */
  syncAccountNow(accountId: number): Promise<void>;
  /** Remove the account; keepData keeps folders and mails as a local account. */
  deleteAccount(accountId: number, options: { keepData: boolean }): Promise<void>;
  createIdentity(identity: AccountIdentity): Promise<AccountIdentity>;
  deleteIdentity(identityId: number): Promise<void>;
  setDefaultIdentity(identityId: number): Promise<void>;
  /** Support export of one account (no secrets). */
  accountDiagnostics(accountId: number): Promise<AccountDiagnostics>;
  /**
   * Einstellungen › Mail › Speicher: mail.headersOnlyOlderThanDays of this
   * device (local core only, never synced; the web client and the server
   * answer `available: false`).
   */
  getMailStorageSettings(): Promise<MailStorageSettings>;
  setMailStorageSettings(settings: Pick<MailStorageSettings, 'headersOnlyOlderThanDays'> & { downloadPaused?: boolean }): Promise<MailStorageSettings>;
  /** How many mails (bytes) a day count would download that are not stored yet. */
  estimateMailStorage(days: number): Promise<MailStorageEstimate>;
}

// --- Account security ---
// Two-factor authentication, password and sessions of the web client (types
// and routes: security.ts). Server mode only: the desktop shell has no server
// account (Wails: ApiError "not_supported"), the mock keeps the state in memory.

import type { AuthConfigInfo, AuthMe, AuthProfileUpdate, AuthSession, AuthUser, PasswordChange, Reauth, TotpEnrollment } from './security';

export interface Api {
  /** Signed-in user, current session and whether TOTP must be set up first (GET /api/auth/me). */
  getAuthMe(): Promise<AuthMe>;
  /** Change the own profile; returns the updated user. Errors: invalid_input, managed_by_idp (OIDC account). */
  updateAuthMe(update: AuthProfileUpdate): Promise<AuthUser>;
  /** Public server settings (password policy). */
  getAuthConfig(): Promise<AuthConfigInfo>;
  /** Start enrolment: new secret + otpauth URL (replaces a pending secret). Error "exists": TOTP already active. */
  /** Starts the enrolment (re-authentication: the password of accounts that have one). */
  totpSetup(reauth: Reauth): Promise<TotpEnrollment>;
  /** Activate TOTP with the first code; returns the recovery codes (shown once). Error "invalid_code". */
  totpConfirm(code: string): Promise<string[]>;
  /** Switch TOTP off (re-authentication). Errors: invalid_credentials, invalid_code, throttled. */
  totpDisable(reauth: Reauth): Promise<void>;
  /** Replace the recovery codes (re-authentication); returns the new ones. Error "totp_not_enabled". */
  regenerateRecoveryCodes(reauth: Reauth): Promise<string[]>;
  /** Change the password (or set a first local password); the server ends all other sessions. */
  changePassword(input: PasswordChange): Promise<void>;
  listAuthSessions(): Promise<AuthSession[]>;
  /** Ends another session (re-authentication); the own session needs none. */
  revokeAuthSession(id: number, reauth?: Reauth): Promise<void>;
  /** Sign out everywhere except this session (re-authentication). */
  revokeOtherSessions(reauth: Reauth): Promise<void>;
}
