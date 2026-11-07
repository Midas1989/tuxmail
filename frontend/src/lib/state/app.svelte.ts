// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Central UI state (Svelte 5 runes). All data comes from the Api; this class
// only orchestrates loading, selection and the classic <-> chat switch.

import type { Api, ConversationScope, MessageScope } from '../api/api';
import { classicSelectionFromChat, forwardSubject, messageIdFromItemId, pickFolderForMessage, replySubject } from '../conversation';
import { fmt, t, type MessageKey } from '../i18n/index.svelte';
import { SETTINGS_HOME, settingsHash, type SettingsCategoryId, type SettingsLocation } from '../settings/catalog';
import {
  FLAG_FLAGGED,
  FLAG_SEEN,
  MAIL_KINDS,
  messengerChannel,
  type Account,
  type Address,
  type ApiEvent,
  type Attachment,
  type Channel,
  type ChatFilter,
  type ChatFolder,
  type FocusSettings,
  type AiSettings,
  type Snippet,
  type TimeSlot,
  type ContactCard,
  type ConversationPatch,
  type OutgoingAttachment,
  type Conversation,
  type ConversationItem,
  type Folder,
  type FolderRole,
  type Identity,
  type MessageDetail,
  type MessageSummary,
  type Person,
  type Signature,
  type SendResult,
  type VaultStatus,
  type ItemRef,
  type Project,
  type SearchFolder,
  type SearchFolderInput,
  type TagCount,
  type Tag,
} from '../types';
import { prefs } from './prefs.svelte';
import { connection } from './connection.svelte';
import { ApiError } from '../api/api';
import { comfortFor } from '../api/comfort';
import { isFocusActive } from '../chatfeatures';
import { folderViews, type FolderView } from '../folders';
import type { AccountSyncStatus } from '../types';
import type { SuggestData } from '../search/suggest';
import type { WorkflowContext } from '../workflow/schema';

export type Route = 'mail' | 'search' | 'projects' | 'calendar' | 'contacts' | 'files' | 'settings';
export const ROUTES: Route[] = ['mail', 'search', 'projects', 'calendar', 'contacts', 'files', 'settings'];
export type { ViewMode } from './prefs.svelte';
import type { ViewMode } from './prefs.svelte';
export const VIEW_MODES: ViewMode[] = ['classic', 'hybrid', 'chat', 'terminal'];
export type UnifiedRole = 'inbox' | 'flagged' | 'unread';

export type SidebarSel =
  | { kind: 'unified'; role: UnifiedRole }
  | { kind: 'folder'; accountId: number; folderId: number }
  | { kind: 'account'; accountId: number }
  | { kind: 'tag'; tagId: number }
  | { kind: 'searchFolder'; searchFolderId: number };

/** Stable key of a sidebar selection (per-folder view defaults). */
export function selKey(s: SidebarSel): string {
  switch (s.kind) {
    case 'unified':
      return 'u:' + s.role;
    case 'folder':
      return 'f:' + s.folderId;
    case 'account':
      return 'a:' + s.accountId;
    case 'tag':
      return 't:' + s.tagId;
    case 'searchFolder':
      return 's:' + s.searchFolderId;
  }
}

/** Dialogs for tags, projects and search folders (rendered by OrganizeDialogs). */
export type OrganizeDialog =
  | { kind: 'tags'; items: ItemRef[]; current: number[] }
  | { kind: 'project'; items: ItemRef[] }
  | { kind: 'searchFolder'; folder?: SearchFolder; query?: string; projectId?: number }
  | { kind: 'newProject' };

export type ComposeMode = 'new' | 'reply' | 'replyAll' | 'forward';

export interface ComposerInit {
  mode: ComposeMode;
  identityId?: number;
  to?: Address[];
  cc?: Address[];
  subject?: string;
  /** User text (HTML) already typed, e.g. from the chat composer. */
  bodyHtml?: string;
  /** Quoted original (HTML), appended below the signature. */
  quoteHtml?: string;
  inReplyToMessageId?: number;
  forwardOfMessageId?: number;
  forwardAttachments?: Attachment[];
  /** A mail from the outbox of the desktop app, continued as a draft. */
  outbox?: {
    id: string;
    reasons: string[];
    bcc?: Address[];
    attachments?: { uploadToken: string; filename: string; mimeType: string; size?: number; delivery?: string }[];
  };
}

/** An in-app mail window (MailWindows.svelte). */
export interface MailWindow {
  key: number;
  messageId: number;
  message: MessageDetail | null;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
}

export interface Toast {
  id: number;
  text: string;
  actionLabel?: string;
  action?: () => void;
  kind?: 'info' | 'error';
}

const PAGE = 200;

/**
 * Trailing debounce with an upper bound: a steady stream of calls (e.g. the
 * events of a large initial sync) still runs fn at least every maxWait ms.
 * Without the bound the list never reloaded while a sync was running and
 * showed "Dieser Ordner ist leer" for minutes.
 */
export function debounce<A extends unknown[]>(fn: (...a: A) => void, ms: number, maxWait = Math.max(ms * 6, 600)) {
  let h: ReturnType<typeof setTimeout> | null = null;
  let first = 0;
  return (...a: A) => {
    if (h) clearTimeout(h);
    const now = Date.now();
    if (!first) first = now;
    const wait = Math.max(0, Math.min(ms, first + maxWait - now));
    h = setTimeout(() => {
      h = null;
      first = 0;
      fn(...a);
    }, wait);
  };
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

export function textToHtml(s: string): string {
  return escapeHtml(s).replace(/\n/g, '<br>');
}

export class AppState {
  readonly api: Api;

  route = $state<Route>('mail');
  sel = $state<SidebarSel>({ kind: 'unified', role: 'inbox' });
  search = $state('');
  connection = $state<'online' | 'offline' | 'reconnecting'>('online');
  ready = $state(false);
  loadError = $state<string | null>(null);

  accounts = $state<Account[]>([]);
  folders = $state<Record<number, Folder[]>>({});
  /** Sync status per mail account (sync.status events). */
  syncStatus = $state<Record<number, AccountSyncStatus>>({});
  /** Accounts whose initial sync was seen in this session (for the "Synchronisiert" toast). */
  private syncSeenInitial = new Set<number>();
  identities = $state<Identity[]>([]);
  signatures = $state<Signature[]>([]);
  persons = $state<Person[]>([]);

  // classic
  messages = $state<MessageSummary[]>([]);
  messagesTotal = $state(0);
  messagesLoading = $state(false);
  selectedMessageId = $state<number | null>(null);
  message = $state<MessageDetail | null>(null);
  /** Incremented to ask the list to scroll the selected row into view. */
  listScrollTick = $state(0);

  // chat
  conversations = $state<Conversation[]>([]);
  conversationsLoading = $state(false);
  selectedConversationId = $state<number | null>(null);
  conversation = $state<Conversation | null>(null);
  items = $state<ConversationItem[]>([]);
  focusedItemId = $state<string | null>(null);
  /** Incremented to ask the timeline to scroll `focusedItemId` (or the end) into view. */
  timelineScrollTick = $state(0);
  /** Bubble to centre on the next scroll request; null = newest item. */
  timelineScrollTarget = $state<string | null>(null);

  // messenger (chat mode)
  chatFilter = $state<ChatFilter>('all');
  showArchived = $state(false);
  archivedCount = $state(0);
  chatQuery = $state('');
  infoOpen = $state(false);
  /** Bubble being answered in the composer (WhatsApp-style reply). */
  replyingTo = $state<ConversationItem | null>(null);
  /** First unread bubble when the chat was opened ("Unread messages" marker). */
  unreadFromId = $state<string | null>(null);
  /** Mail shown in the "original e-mail" overlay. */
  originalMessage = $state<MessageDetail | null>(null);
  /** Bubble chosen for "Forward". */
  forwarding = $state<ConversationItem | null>(null);

  // chat folders, focus mode, AI, overlays of the chat features
  chatFolders = $state<ChatFolder[]>([]);
  activeFolderId = $state<string | null>(null);
  focus = $state<FocusSettings>({ enabled: false, vipPersonIds: [], vipConversationIds: [], includeFrequent: true });
  frequentIds = $state<number[]>([]);
  waitingCount = $state(0);
  waitingOpen = $state(false);
  waitingConversations = $state<Conversation[]>([]);
  aiSettings = $state<AiSettings | null>(null);
  snippets = $state<Snippet[]>([]);
  /** Open overlay of the chat features. */
  chatDialog = $state<
    | null
    | { kind: 'folder'; folder: ChatFolder | null }
    | { kind: 'snooze'; conversation: Conversation }
    | { kind: 'starred' }
    | { kind: 'scheduled' }
    | { kind: 'focus' }
    | { kind: 'meeting' }
  >(null);
  /** Clock tick (1/min) so schedule-dependent UI (focus hours) updates. */
  now = $state(Date.now());

  // overlays
  // Tags, projects, search folders
  tags = $state<TagCount[]>([]);
  projects = $state<Project[]>([]);
  searchFolders = $state<SearchFolder[]>([]);
  selectedProjectId = $state<number | null>(null);
  organizeDialog = $state<OrganizeDialog | null>(null);
  /** Messenger view: tag filter pill. */
  chatTagId = $state<number | null>(null);
  /** Classic list: checked mails for bulk actions. */
  checked = $state<number[]>([]);
  /**
   * Legacy: old settings anchor (e.g. "set-workflows") to open after
   * navigating; the settings screen redirects it to the new category/section.
   * Prefer openSettings(category, section).
   */
  settingsAnchor = $state<string | null>(null);
  /** Settings page shown (#/settings/<category>/<section>); null category = overview. */
  settingsLoc = $state<SettingsLocation>({ ...SETTINGS_HOME });

  composer = $state<ComposerInit | null>(null);
  paletteOpen = $state(false);
  sidebarOpen = $state(false);
  toasts = $state<Toast[]>([]);

  private toastSeq = 0;
  private unsubscribe: (() => void) | null = null;
  private seenTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(api: Api) {
    this.api = api;
  }

  get viewMode(): ViewMode {
    return prefs.viewMode;
  }

  /** Classic and terminal view show the same message list (not conversations). */
  get messageListView(): boolean {
    return prefs.viewMode === 'classic' || prefs.viewMode === 'terminal';
  }

  get mailAccounts(): Account[] {
    return this.accounts.filter((a) => MAIL_KINDS.includes(a.kind));
  }

  get messengerAccounts(): Account[] {
    return this.accounts.filter((a) => messengerChannel(a.kind) !== null);
  }

  get allFolders(): Folder[] {
    return Object.values(this.folders).flat();
  }

  get unifiedUnread(): number {
    return this.allFolders.filter((f) => f.role === 'inbox').reduce((n, f) => n + f.unread, 0);
  }

  tag(id: number): Tag | undefined {
    return this.tags.find((t) => t.tag.id === id)?.tag;
  }

  /** Tags of an item, ordered by path. */
  tagsOf(ids: number[] | undefined): Tag[] {
    if (!ids?.length) return [];
    return this.tags.filter((t) => ids.includes(t.tag.id)).map((t) => t.tag);
  }

  project(id: number): Project | undefined {
    return this.projects.find((p) => p.id === id);
  }

  searchFolder(id: number): SearchFolder | undefined {
    return this.searchFolders.find((s) => s.id === id);
  }

  /** "Gesendet", "Archiv/2026": localized role name plus sub path. */
  /** Display data of the folders of an account (labels, effective roles; folders.ts). */
  folderViewsOf(accountId: number): Map<number, FolderView> {
    return folderViews(this.folders[accountId] ?? [], (r) => t(`mail.folder.role.${r}` as 'mail.folder.role.inbox'));
  }

  /** Display name of a folder (role name, or "Archiv (Archive)" on collisions). */
  folderLabel(id: number): string {
    const f = this.folder(id);
    if (!f) return '';
    return this.folderViewsOf(f.accountId).get(id)?.label ?? f.name;
  }

  folderPath(id: number): string {
    const f = this.folder(id);
    if (!f) return '';
    const own = this.folderLabel(id);
    return f.parentId ? `${this.folderPath(f.parentId)}/${own}` : own;
  }

  // -------------------------------------------------------------------------
  // Sync status (top bar indicator, folder tree, empty list)
  // -------------------------------------------------------------------------

  async loadSyncStatus(): Promise<void> {
    try {
      const list = await this.api.syncStatus();
      const next: Record<number, AccountSyncStatus> = {};
      for (const s of list) next[s.accountId] = s;
      this.syncStatus = next;
    } catch {
      /* older servers: no sync status */
    }
  }

  private onSyncStatus(s: AccountSyncStatus): void {
    const prev = this.syncStatus[s.accountId];
    this.syncStatus = { ...this.syncStatus, [s.accountId]: s };
    if (s.initial) this.syncSeenInitial.add(s.accountId);
    if (prev && prev.state === 'syncing' && s.state === 'idle' && this.syncSeenInitial.has(s.accountId)) {
      this.syncSeenInitial.delete(s.accountId);
      this.toast(t('sync.done', { account: this.account(s.accountId)?.displayName ?? '' }));
      this.reloadListDebounced();
    }
  }

  /** Busy accounts (connecting or syncing), for the top bar indicator. */
  get syncActive(): AccountSyncStatus[] {
    return this.mailAccounts.map((a) => this.syncStatus[a.id]).filter((s): s is AccountSyncStatus => !!s && (s.state === 'syncing' || s.state === 'connecting'));
  }

  get syncErrors(): AccountSyncStatus[] {
    return this.mailAccounts.map((a) => this.syncStatus[a.id]).filter((s): s is AccountSyncStatus => !!s && s.state === 'error');
  }

  /** Human readable "Synchronisiere <Konto> · <Ordner> 450 / 2036". */
  syncLabel(s: AccountSyncStatus): string {
    const acc = this.account(s.accountId)?.displayName ?? '';
    if (s.state === 'connecting') return t('sync.connecting', { account: acc });
    const folder = s.folderId ? this.folderLabel(s.folderId) : s.folder ?? '';
    if (folder && s.total) return t('sync.progress', { account: acc, folder, done: Math.min(s.done ?? 0, s.total), total: s.total });
    if (folder) return t('sync.folder', { account: acc, folder });
    return t('sync.syncing', { account: acc });
  }

  /** Is folder id (or, with folderId 0, any folder of the account) being synced right now? */
  folderSyncing(folderId: number): boolean {
    return Object.values(this.syncStatus).some((s) => s.state === 'syncing' && s.folderId === folderId);
  }

  /**
   * Sync state relevant for the current list: progress of the selected
   * folder, or of any account still in its first sync (unified views). Used
   * instead of "Dieser Ordner ist leer" while mail is still arriving.
   */
  get listSync(): AccountSyncStatus | null {
    const s = this.sel;
    const active = Object.values(this.syncStatus).filter((x) => x.state === 'syncing' || x.state === 'connecting');
    if (s.kind === 'folder') {
      return active.find((x) => x.folderId === s.folderId) ?? active.find((x) => x.accountId === s.accountId && !x.lastSync) ?? null;
    }
    return active.find((x) => x.initial || !x.lastSync) ?? null;
  }

  /** Names the workflow editors resolve values against. */
  get workflowContext(): WorkflowContext {
    const all = this.allFolders;
    const path = (f: Folder): string => {
      const p = all.find((x) => x.id === f.parentId);
      return p ? path(p) + '/' + f.name : f.name;
    };
    return {
      accounts: this.accounts.map((a) => ({ id: a.id, name: a.displayName })),
      folders: ['inbox', ...new Set(all.filter((f) => f.role !== 'junk' && f.role !== 'trash').map(path))],
      tags: this.tags.map((x) => x.tag.path),
      projects: this.projects.map((p) => p.name),
      templates: ['Danke', 'Eingangsbestätigung', 'Abwesend'],
    };
  }

  /** Autocomplete data for expert query inputs (search, search folders, project rules). */
  get suggestData(): SuggestData {
    return {
      accounts: this.accounts.map((a) => ({ id: a.id, name: a.displayName, address: a.extra?.address })),
      folders: this.allFolders.map((f) => ({ id: f.id, name: f.role ? t(`mail.folder.role.${f.role}` as 'mail.folder.role.inbox') : f.name, account: this.account(f.accountId)?.displayName ?? '' })),
      people: this.persons.filter((p) => !p.isMe).flatMap((p) => p.handles.filter((h) => h.kind === 'email').map((h) => ({ name: p.displayName, email: h.value }))),
      tags: this.tags.map((x) => ({ path: x.tag.path, color: x.tag.color })),
      projects: this.projects.map((p) => ({ key: p.key, name: p.name })),
    };
  }

  person(id: number): Person | undefined {
    return this.persons.find((p) => p.id === id);
  }

  /**
   * Avatar of a conversation: mail threads are titled by their subject, so
   * their avatar shows the counterpart (initials of the sender), never the
   * subject ("R4" for "Rechnung Nr. 4683"); the group icon only for threads
   * with several participants.
   */
  convAvatar(c: Conversation): { name: string; seed: string; group: boolean } {
    const first = c.participantIds[0];
    const seed = this.person(first)?.handles[0]?.value ?? String(first ?? c.id);
    if (c.kind === 'chat') return { name: c.title, seed: c.isGroup ? c.title : seed, group: !!c.isGroup };
    if (c.kind === 'email_thread') return { name: this.personName(first) ?? '', seed, group: c.participantIds.length > 1 };
    return { name: c.title, seed, group: false };
  }

  /** Display name of a person; schedules a reload when the sync created persons the UI does not know yet. */
  personName(id: number | undefined): string | undefined {
    if (id == null) return undefined;
    const p = this.person(id);
    if (!p) this.reloadPersonsDebounced();
    return p?.displayName;
  }

  account(id: number): Account | undefined {
    return this.accounts.find((a) => a.id === id);
  }

  folder(id: number): Folder | undefined {
    return this.allFolders.find((f) => f.id === id);
  }

  personByEmail(email: string): Person | undefined {
    const e = email.toLowerCase();
    return this.persons.find((p) => p.handles.some((h) => h.kind === 'email' && h.value === e));
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  /** Portable desktop: master-password vault state (VaultUnlock dialog). */
  vault = $state<VaultStatus | null>(null);

  async refreshVault(): Promise<void> {
    try {
      this.vault = await this.api.vaultStatus();
    } catch {
      this.vault = null;
    }
  }

  /** Unlocks (or on first use creates) the vault; throws ApiError "invalid_password". */
  async unlockVault(password: string): Promise<void> {
    await this.api.unlockVault(password);
    await this.refreshVault();
    const accounts = await this.api.listAccounts();
    this.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
  }

  async init(): Promise<void> {
    void this.refreshVault();
    try {
      await prefs.connect(this.api);
      void this.initChatFeatures();
      void this.showFailedSends();
      const [accounts, identities, signatures, persons] = await Promise.all([
        this.api.listAccounts(),
        this.api.listIdentities(),
        this.api.listSignatures(),
        this.api.listPersons(),
      ]);
      this.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
      this.identities = identities;
      this.signatures = signatures;
      this.persons = persons;
      await Promise.all([...this.mailAccounts.map((a) => this.reloadFolders(a.id)), this.loadTags(), this.loadProjects(), this.loadSearchFolders()]);
      this.unsubscribe = this.api.subscribe((e) => this.onEvent(e));
      void this.loadSyncStatus();
      await this.reloadList();
      this.ready = true;
    } catch (err) {
      this.loadError = err instanceof Error ? err.message : String(err);
    }
  }

  destroy(): void {
    this.unsubscribe?.();
  }

  async reloadFolders(accountId: number): Promise<void> {
    this.folders[accountId] = await this.api.listFolders(accountId);
  }

  private reloadFoldersDebounced = debounce((ids: number[]) => {
    for (const id of new Set(ids)) void this.reloadFolders(id);
  }, 120);
  private pendingFolderAccounts: number[] = [];

  private reloadListDebounced = debounce(() => void this.reloadList({ keepSelection: true }), 150);
  private reloadItemsDebounced = debounce((focus?: string) => void this.reloadItems(focus), 80);
  private reloadSearchFoldersDebounced = debounce(() => void this.loadSearchFolders(), 400);
  // Sync creates persons for new senders/recipients (KONZEPT 10.2).
  private reloadPersonsDebounced = debounce(() => void this.api.listPersons().then((p) => (this.persons = p)), 700);

  /** Loads again what is shown, keeping the selection. Never fails loudly: the connection may be gone again. */
  async reloadAfterReconnect(): Promise<void> {
    try {
      const [accounts, identities, signatures] = await Promise.all([this.api.listAccounts(), this.api.listIdentities(), this.api.listSignatures()]);
      this.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
      this.identities = identities;
      this.signatures = signatures;
      await Promise.all(this.mailAccounts.map((a) => this.reloadFolders(a.id)));
      this.reloadPersonsDebounced();
      this.reloadListDebounced();
      if (this.selectedConversationId != null) this.reloadItemsDebounced();
    } catch {
      /* the next event tries again */
    }
  }

  private onEvent(e: ApiEvent): void {
    switch (e.type) {
      case 'connection': {
        const was = this.connection;
        this.connection = e.state;
        // Back from offline (desktop app in server mode): what is on screen came from the read cache.
        if (was !== 'online' && e.state === 'online') void this.reloadAfterReconnect();
        break;
      }
      case 'vault.locked':
        void this.refreshVault();
        break;
      case 'mail.send_failed':
        void this.showFailedSends();
        break;
      case 'tags.changed':
        void this.loadTags();
        void this.loadSearchFolders();
        this.reloadListDebounced();
        if (this.selectedConversationId != null) this.reloadItemsDebounced();
        if (this.selectedMessageId != null) void this.refreshMessage();
        break;
      case 'project.updated':
        void this.loadProjects();
        break;
      case 'searchfolders.changed':
        void this.loadSearchFolders();
        void this.loadChatFolders();
        break;
      case 'folder.updated':
        this.pendingFolderAccounts.push(e.accountId);
        this.reloadFoldersDebounced([...this.pendingFolderAccounts]);
        this.pendingFolderAccounts = [];
        // The initial sync announces new mail per batch with folder.updated
        // (no message.added per mail): refresh the list as it fills.
        this.reloadListDebounced();
        break;
      case 'sync.status':
        this.onSyncStatus(e.status);
        break;
      case 'account.updated':
        // accountId 0: settings shared by all accounts changed (e.g. signatures).
        void this.api.listAccounts().then((a) => {
          this.accounts = a.sort((x, y) => x.sortOrder - y.sortOrder);
          // A removed account (account edit dialog) has no folders to reload.
          return e.accountId && this.accounts.some((x) => x.id === e.accountId) ? this.reloadFolders(e.accountId).catch(() => undefined) : undefined;
        });
        void this.api.listIdentities().then((i) => (this.identities = i));
        void this.api.listSignatures().then((s) => (this.signatures = s));
        break;
      case 'message.added':
      case 'message.updated':
      case 'message.removed':
        if (e.type === 'message.added') this.reloadPersonsDebounced();
        this.reloadListDebounced();
        this.reloadSearchFoldersDebounced();
        if (this.selectedMessageId === e.messageId && e.type === 'message.updated') void this.refreshMessage();
        break;
      case 'conversation.updated':
        this.reloadPersonsDebounced();
        if (!this.messageListView) this.reloadListDebounced();
        if (this.selectedConversationId === e.conversationId) this.reloadItemsDebounced(e.itemId);
        break;
    }
  }

  // -------------------------------------------------------------------------
  // Navigation
  // -------------------------------------------------------------------------

  navigate(route: Route): void {
    this.route = route;
    if (route === 'settings') this.settingsLoc = { ...SETTINGS_HOME };
    if (typeof location !== 'undefined' && location.hash !== '#/' + route) history.replaceState(null, '', '#/' + route);
  }

  /**
   * Open a settings page (#/settings/<category>/<section>). Without a
   * category: the overview (phones: the category list). Pages get their own
   * history entry so the browser/back button returns to the previous page.
   */
  openSettings(category: SettingsCategoryId | null = null, section: string | null = null): void {
    this.route = 'settings';
    this.settingsLoc = { category, section: category ? section : null };
    const hash = settingsHash(this.settingsLoc);
    if (typeof location !== 'undefined' && location.hash !== hash) {
      if (location.hash.startsWith('#/settings')) history.pushState(null, '', hash);
      else history.replaceState(null, '', hash);
    }
  }

  messageScope(): MessageScope {
    const s = this.sel;
    if (s.kind === 'folder') return { kind: 'folder', folderId: s.folderId };
    if (s.kind === 'unified') return { kind: 'unified', role: s.role };
    if (s.kind === 'tag') return { kind: 'tag', tagId: s.tagId };
    if (s.kind === 'searchFolder') return { kind: 'searchFolder', searchFolderId: s.searchFolderId };
    return { kind: 'unified', role: 'inbox' };
  }

  conversationScope(): ConversationScope {
    // The messenger view never shows folders: chats aggregate all of them.
    if (this.viewMode === 'chat') return { kind: 'all' };
    const s = this.sel;
    if (s.kind === 'folder') return { kind: 'folder', folderId: s.folderId };
    if (s.kind === 'account') return { kind: 'account', accountId: s.accountId };
    if (s.kind === 'tag') return { kind: 'tag', tagId: s.tagId };
    if (s.kind === 'searchFolder') return { kind: 'searchFolder', searchFolderId: s.searchFolderId };
    return { kind: 'unified', role: s.role };
  }

  async select(sel: SidebarSel): Promise<void> {
    this.sel = sel;
    this.sidebarOpen = false;
    this.selectedMessageId = null;
    this.message = null;
    this.selectedConversationId = null;
    this.conversation = null;
    this.items = [];
    this.checked = [];
    const preferred = prefs.folderViewModes[selKey(sel)];
    if (preferred && sel.kind !== 'account') prefs.viewMode = preferred;
    else if (prefs.viewMode === 'chat' && sel.kind !== 'account') prefs.viewMode = 'classic';
    if (sel.kind === 'account') {
      prefs.viewMode = 'chat';
      const acc = this.account(sel.accountId);
      this.chatFilter = (acc && messengerChannel(acc.kind)) || 'all';
    }
    if (this.route !== 'mail') this.navigate('mail');
    await this.reloadList();
  }

  async setSearch(q: string): Promise<void> {
    this.search = q;
    await this.reloadList({ keepSelection: true });
  }

  async reloadList(opts: { keepSelection?: boolean } = {}): Promise<void> {
    if (this.messageListView) await this.loadMessages(opts.keepSelection);
    else await this.loadConversations(opts.keepSelection);
    this.checked = this.checked.filter((id) => this.messages.some((m) => m.id === id));
  }

  async loadMessages(keepSelection = false): Promise<void> {
    this.messagesLoading = true;
    try {
      const page = await this.api.listMessages({ scope: this.messageScope(), offset: 0, limit: Math.max(PAGE, this.messages.length), search: this.search || undefined });
      this.messages = page.items;
      this.messagesTotal = page.total;
      if (!keepSelection && this.selectedMessageId != null && !page.items.some((m) => m.id === this.selectedMessageId)) {
        this.selectedMessageId = null;
        this.message = null;
      }
    } finally {
      this.messagesLoading = false;
    }
  }

  async loadMoreMessages(): Promise<void> {
    if (this.messagesLoading || this.messages.length >= this.messagesTotal) return;
    this.messagesLoading = true;
    try {
      const page = await this.api.listMessages({ scope: this.messageScope(), offset: this.messages.length, limit: PAGE, search: this.search || undefined });
      this.messages = [...this.messages, ...page.items];
      this.messagesTotal = page.total;
    } finally {
      this.messagesLoading = false;
    }
  }

  async loadConversations(keepSelection = false): Promise<void> {
    this.conversationsLoading = true;
    try {
      const chat = this.viewMode === 'chat';
      const page = await this.api.listConversations({
        scope: this.conversationScope(),
        grouping: chat ? prefs.grouping : 'thread',
        tagId: chat && this.chatTagId != null ? this.chatTagId : undefined,
        offset: 0,
        limit: 500,
        search: (this.viewMode === 'chat' ? this.chatQuery : this.search) || undefined,
        filter: this.viewMode === 'chat' ? this.chatFilter : undefined,
        archived: this.viewMode === 'chat' ? this.showArchived : undefined,
        folderId: this.viewMode === 'chat' && !this.showArchived ? (this.activeFolderId ?? undefined) : undefined,
        focus: this.viewMode === 'chat' && this.focusActive && !this.showArchived ? 'vip' : undefined,
      });
      this.conversations = page.items;
      if (this.viewMode === 'chat' && !this.showArchived) {
        void this.api
          .listConversations({ scope: { kind: 'all' }, grouping: prefs.grouping, offset: 0, limit: 1, archived: true })
          .then((p) => (this.archivedCount = p.total));
        void this.loadChatFolders();
        if (this.focusActive) void this.loadWaiting();
      }
      if (this.selectedConversationId != null) {
        const c = page.items.find((x) => x.id === this.selectedConversationId);
        if (c) this.conversation = c;
        else if (!keepSelection) {
          this.selectedConversationId = null;
          this.conversation = null;
          this.items = [];
        }
      }
    } finally {
      this.conversationsLoading = false;
    }
  }

  // -------------------------------------------------------------------------
  // Classic selection
  // -------------------------------------------------------------------------

  /** Message whose content is being loaded on opening (kept with header only, "nur Kopf"). */
  contentLoadingId = $state<number | null>(null);

  async selectMessage(id: number | null, opts: { scroll?: boolean } = {}): Promise<void> {
    this.selectedMessageId = id;
    if (this.seenTimer) clearTimeout(this.seenTimer);
    if (id == null) {
      this.message = null;
      this.contentLoadingId = null;
      return;
    }
    if (opts.scroll) this.listScrollTick++;
    const summary = this.messages.find((m) => m.id === id);
    if (summary?.headersOnly) {
      // Header and preview right away; the content follows (may take a moment).
      this.message = { ...summary, inReplyTo: [], references: [], attachments: [], text: summary.preview };
      this.contentLoadingId = id;
    }
    let detail: MessageDetail;
    try {
      detail = await this.api.getMessage(id);
    } finally {
      if (this.contentLoadingId === id) this.contentLoadingId = null;
    }
    if (this.selectedMessageId !== id) return;
    this.message = detail;
    this.prefetchNeighbours(id);
    if (!detail.flags.includes(FLAG_SEEN)) {
      this.seenTimer = setTimeout(() => {
        if (this.selectedMessageId === id) void this.api.setFlags([id], [FLAG_SEEN], []);
      }, 700);
    }
  }

  /** "Erneut laden" of a mail whose content was not available. */
  async reloadMessage(): Promise<void> {
    const id = this.selectedMessageId;
    if (id == null) return;
    this.contentLoadingId = id;
    try {
      const detail = await this.api.getMessage(id);
      if (this.selectedMessageId === id) this.message = detail;
    } finally {
      if (this.contentLoadingId === id) this.contentLoadingId = null;
    }
  }

  /**
   * Warm the next and previous mail of the list (backend render cache and
   * on-demand body download) so that J/K is instant.
   */
  private prefetchNeighbours(id: number): void {
    const i = this.messages.findIndex((m) => m.id === id);
    if (i < 0) return;
    for (const n of [this.messages[i + 1], this.messages[i - 1]]) {
      // Mails kept with header only load their content when opened, not ahead.
      if (n && !n.headersOnly && !this.prefetched.has(n.id)) {
        this.prefetched.add(n.id);
        void this.api.getMessage(n.id).catch(() => this.prefetched.delete(n.id));
      }
    }
  }
  private prefetched = new Set<number>();

  // -------------------------------------------------------------------------
  // Mail windows: detachable, movable, resizable in-app windows with the
  // full mail (Wails v2 has a single native window; web/PWA can also open
  // #/mail/<id> in a real browser window).
  // -------------------------------------------------------------------------

  mailWindows = $state<MailWindow[]>([]);
  private mailWindowSeq = 0;

  async openMailWindow(messageId: number): Promise<void> {
    const open = this.mailWindows.find((w) => w.messageId === messageId);
    if (open) {
      this.focusMailWindow(open.key);
      return;
    }
    const n = this.mailWindows.length;
    const vw = typeof innerWidth === 'number' ? innerWidth : 1280;
    const vh = typeof innerHeight === 'number' ? innerHeight : 800;
    const w = Math.min(820, vw - 40);
    const h = Math.min(640, vh - 60);
    const key = ++this.mailWindowSeq;
    const z = Math.max(0, ...this.mailWindows.map((x) => x.z)) + 1;
    this.mailWindows = [...this.mailWindows, { key, messageId, message: null, x: Math.max(8, (vw - w) / 2 + n * 28), y: Math.max(8, 60 + n * 28), w, h, z }];
    try {
      const m = await this.api.getMessage(messageId);
      this.mailWindows = this.mailWindows.map((x) => (x.key === key ? { ...x, message: m } : x));
    } catch (err) {
      this.closeMailWindow(key);
      this.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }

  focusMailWindow(key: number): void {
    const z = Math.max(0, ...this.mailWindows.map((x) => x.z)) + 1;
    this.mailWindows = this.mailWindows.map((x) => (x.key === key ? { ...x, z } : x));
  }

  moveMailWindow(key: number, patch: Partial<Pick<MailWindow, 'x' | 'y' | 'w' | 'h'>>): void {
    this.mailWindows = this.mailWindows.map((x) => (x.key === key ? { ...x, ...patch } : x));
  }

  closeMailWindow(key: number): void {
    this.mailWindows = this.mailWindows.filter((x) => x.key !== key);
  }

  /** Reply/forward from a mail window (or any loaded mail). */
  replyToMessage(m: MessageDetail, mode: Exclude<ComposeMode, 'new'>): void {
    this.openCompose(this.replyInit(m, mode));
  }

  private async refreshMessage(): Promise<void> {
    if (this.selectedMessageId == null) return;
    const id = this.selectedMessageId;
    const d = await this.api.getMessage(id);
    if (this.selectedMessageId === id) this.message = d;
  }

  moveSelection(delta: 1 | -1): void {
    if (this.messageListView) {
      const list = this.messages;
      if (list.length === 0) return;
      const idx = list.findIndex((m) => m.id === this.selectedMessageId);
      const next = idx === -1 ? 0 : Math.min(list.length - 1, Math.max(0, idx + delta));
      void this.selectMessage(list[next].id, { scroll: true });
    } else {
      const list = this.conversations;
      if (list.length === 0) return;
      const idx = list.findIndex((c) => c.id === this.selectedConversationId);
      const next = idx === -1 ? 0 : Math.min(list.length - 1, Math.max(0, idx + delta));
      void this.selectConversation(list[next].id);
    }
  }

  // -------------------------------------------------------------------------
  // Chat selection
  // -------------------------------------------------------------------------

  async selectConversation(id: number | null, focusItemId?: string): Promise<void> {
    this.selectedConversationId = id;
    this.focusedItemId = focusItemId ?? null;
    if (id == null) {
      this.conversation = null;
      this.items = [];
      return;
    }
    if (this.replyingTo?.conversationId !== id) this.replyingTo = null;
    this.conversation = this.conversations.find((c) => c.id === id) ?? (await this.api.getConversation(id));
    const items = await this.api.listConversationItems(id, this.itemsQuery());
    if (this.selectedConversationId !== id) return;
    this.items = items;
    const firstUnread = items.find((i) => i.unread && !i.isOwn);
    this.unreadFromId = firstUnread?.id ?? null;
    // Hybrid: jump to the first bubble of the selected folder (an unread one if any).
    const firstInScope = this.viewMode === 'hybrid' ? (items.find((i) => i.inScope && i.unread) ?? items.find((i) => i.inScope)) : undefined;
    this.timelineScrollTarget = focusItemId ?? firstInScope?.id ?? firstUnread?.id ?? null;
    this.timelineScrollTick++;
    if (this.conversation && (this.conversation.unreadCount > 0 || this.conversation.markedUnread)) {
      void this.api.markConversationRead(id);
    }
  }

  async reloadItems(newItemId?: string): Promise<void> {
    const id = this.selectedConversationId;
    if (id == null) return;
    const items = await this.api.listConversationItems(id, this.itemsQuery());
    if (this.selectedConversationId !== id) return;
    const grew = items.length > this.items.length;
    this.items = items;
    // Own messages always scroll to the end; incoming ones are handled by the
    // timeline (it only follows when the user is already at the bottom).
    if (grew && newItemId && items.find((i) => i.id === newItemId)?.isOwn) {
      this.timelineScrollTarget = null;
      this.timelineScrollTick++;
    }
    if (items.some((i) => i.unread && !i.isOwn) && typeof document !== 'undefined' && document.hasFocus()) {
      void this.api.markConversationRead(id);
    }
  }

  focusItem(itemId: string): void {
    this.focusedItemId = itemId;
  }

  private itemsQuery(): { scope?: ConversationScope } | undefined {
    return this.viewMode === 'hybrid' ? { scope: this.conversationScope() } : undefined;
  }

  // -------------------------------------------------------------------------
  // Classic <-> chat (KONZEPT 6.6): the selection survives the switch.
  // -------------------------------------------------------------------------

  async setViewMode(mode: ViewMode): Promise<void> {
    if (mode === this.viewMode) return;
    // The selection survives every switch: classic mail <-> bubble.
    const msgId = this.messageListView ? this.selectedMessageId : (messageIdFromItemId(this.focusedItemId) ?? classicSelectionFromChat(this.items, this.focusedItemId));
    if (mode === 'classic' || mode === 'terminal') await this.toClassic(msgId, mode);
    else await this.toConversations(mode, msgId);
    prefs.save();
  }

  /** Ctrl+Shift+V: classic -> hybrid -> chat -> terminal -> classic. */
  toggleViewMode(): Promise<void> {
    return this.setViewMode(VIEW_MODES[(VIEW_MODES.indexOf(this.viewMode) + 1) % VIEW_MODES.length]);
  }

  /** Remember the current view as default for the selected folder (or forget it). */
  setFolderDefaultView(on: boolean): void {
    const k = selKey(this.sel);
    if (on) prefs.folderViewModes = { ...prefs.folderViewModes, [k]: this.viewMode };
    else {
      const next = { ...prefs.folderViewModes };
      delete next[k];
      prefs.folderViewModes = next;
    }
    prefs.save();
  }

  get folderDefaultView(): ViewMode | undefined {
    return prefs.folderViewModes[selKey(this.sel)];
  }

  private async toConversations(mode: 'hybrid' | 'chat', msgId: number | null): Promise<void> {
    prefs.viewMode = mode;
    if (mode === 'hybrid' && this.sel.kind === 'account') this.sel = { kind: 'unified', role: 'inbox' };
    if (msgId == null) {
      this.selectedConversationId = null;
      this.items = [];
      await this.loadConversations();
      return;
    }
    const ref = await this.api.conversationForMessage(msgId, mode === 'hybrid' ? 'thread' : prefs.grouping);
    await this.loadConversations(true);
    await this.selectConversation(ref.conversationId, ref.itemId);
  }

  private async toClassic(msgId: number | null, mode: 'classic' | 'terminal' = 'classic'): Promise<void> {
    prefs.viewMode = mode;
    if (msgId == null) {
      if (this.sel.kind === 'account') this.sel = { kind: 'unified', role: 'inbox' };
      await this.loadMessages();
      return;
    }
    const detail = await this.api.getMessage(msgId);
    if (!this.scopeContains(detail)) {
      const folders = this.folders[detail.accountId] ?? [];
      const current = this.sel.kind === 'folder' ? this.sel.folderId : null;
      const fid = pickFolderForMessage(detail.folderIds, folders, current);
      const inboxHit = folders.some((f) => f.role === 'inbox' && detail.folderIds.includes(f.id));
      this.sel = inboxHit ? { kind: 'unified', role: 'inbox' } : fid != null ? { kind: 'folder', accountId: detail.accountId, folderId: fid } : { kind: 'unified', role: 'inbox' };
      this.search = '';
    }
    await this.loadMessages(true);
    this.selectedMessageId = msgId;
    this.message = detail;
    this.listScrollTick++;
  }

  private scopeContains(m: MessageSummary): boolean {
    const s = this.sel;
    if (s.kind === 'folder') return m.folderIds.includes(s.folderId);
    if (s.kind === 'account' || s.kind === 'searchFolder') return false;
    if (s.kind === 'tag') return !!m.tagIds?.includes(s.tagId);
    if (s.role === 'flagged') return m.flags.includes(FLAG_FLAGGED);
    if (s.role === 'unread') return true;
    return m.folderIds.some((id) => this.folder(id)?.role === 'inbox');
  }

  /** Open a mail in the classic view (search results, notifications). */
  async revealMessage(messageId: number): Promise<void> {
    this.navigate('mail');
    if (prefs.viewMode !== 'terminal') prefs.viewMode = 'classic';
    const detail = await this.api.getMessage(messageId);
    if (!this.scopeContains(detail)) {
      const folders = this.folders[detail.accountId] ?? [];
      const fid = pickFolderForMessage(detail.folderIds, folders, null);
      this.sel = fid != null ? { kind: 'folder', accountId: detail.accountId, folderId: fid } : { kind: 'unified', role: 'inbox' };
    }
    this.search = '';
    await this.loadMessages(true);
    this.selectedMessageId = messageId;
    this.message = detail;
    this.listScrollTick++;
  }

  /** Open a conversation bubble in the chat view. */
  async revealItem(conversationId: number, itemId?: string): Promise<void> {
    this.navigate('mail');
    prefs.viewMode = 'chat';
    if (this.sel.kind === 'folder') this.sel = { kind: 'unified', role: 'inbox' };
    this.search = '';
    await this.loadConversations(true);
    await this.selectConversation(conversationId, itemId);
  }

  async setGrouping(g: 'thread' | 'person'): Promise<void> {
    if (prefs.grouping === g) return;
    // Keep the focused mail visible across the regrouping.
    const msgId = messageIdFromItemId(this.focusedItemId) ?? classicSelectionFromChat(this.items, this.focusedItemId);
    prefs.grouping = g;
    prefs.save();
    await this.loadConversations();
    if (msgId != null) {
      const ref = await this.api.conversationForMessage(msgId, g);
      await this.selectConversation(ref.conversationId, ref.itemId);
    } else {
      this.selectedConversationId = null;
      this.items = [];
    }
  }

  // -------------------------------------------------------------------------
  // Actions
  // -------------------------------------------------------------------------

  toast(text: string, action?: { label: string; run: () => void }, kind: 'info' | 'error' = 'info'): void {
    const id = ++this.toastSeq;
    this.toasts = [...this.toasts, { id, text, actionLabel: action?.label, action: action?.run, kind }];
    setTimeout(() => this.dismissToast(id), 6000);
  }

  dismissToast(id: number): void {
    this.toasts = this.toasts.filter((t) => t.id !== id);
  }

  async toggleFlag(id: number): Promise<void> {
    const m = this.messages.find((x) => x.id === id) ?? this.message;
    if (!m) return;
    const flagged = m.flags.includes(FLAG_FLAGGED);
    await this.api.setFlags([id], flagged ? [] : [FLAG_FLAGGED], flagged ? [FLAG_FLAGGED] : []);
  }

  async toggleRead(id: number): Promise<void> {
    const m = this.messages.find((x) => x.id === id) ?? this.message;
    if (!m) return;
    const seen = m.flags.includes(FLAG_SEEN);
    if (this.seenTimer) clearTimeout(this.seenTimer);
    await this.api.setFlags([id], seen ? [] : [FLAG_SEEN], seen ? [FLAG_SEEN] : []);
  }

  async moveTo(id: number, folderId: number): Promise<void> {
    const m = this.messages.find((x) => x.id === id) ?? this.message;
    if (!m) return;
    const target = this.folder(folderId);
    const previous = m.folderIds.find((f) => this.folder(f)?.accountId === target?.accountId);
    const idx = this.messages.findIndex((x) => x.id === id);
    await this.api.move([id], folderId);
    if (this.selectedMessageId === id) {
      const next = this.messages[idx + 1] ?? this.messages[idx - 1];
      void this.selectMessage(next && next.id !== id ? next.id : null);
    }
    this.toast(t('mail.toast.moved', { folder: target?.name ?? '' }), previous != null ? { label: t('common.action.undo'), run: () => void this.api.move([id], previous) } : undefined);
  }

  async moveToRole(id: number, role: FolderRole): Promise<void> {
    if (connection.offline) return this.toast(t('connection.notAvailableOffline')); // keyboard: the buttons are switched off
    const m = this.messages.find((x) => x.id === id) ?? this.message;
    if (!m) return;
    const f = (this.folders[m.accountId] ?? []).find((x) => x.role === role);
    if (f) await this.moveTo(id, f.id);
  }

  // -------------------------------------------------------------------------
  // Compose
  // -------------------------------------------------------------------------

  defaultIdentity(accountId?: number): Identity | undefined {
    return (
      this.identities.find((i) => i.accountId === accountId && i.isDefault) ??
      this.identities.find((i) => i.accountId === accountId) ??
      this.identities.find((i) => i.isDefault) ??
      this.identities[0]
    );
  }

  /** Identity that received the original mail (alias aware). */
  identityFor(m: MessageDetail): Identity | undefined {
    const rcpts = [...m.to, ...m.cc, ...m.from].map((a) => a.email.toLowerCase());
    return this.identities.find((i) => rcpts.includes(i.email.toLowerCase())) ?? this.defaultIdentity(m.accountId);
  }

  openCompose(init: ComposerInit = { mode: 'new' }): void {
    this.composer = init;
  }

  /** Delayed sends that failed for good: shown once, also after being away. */
  async showFailedSends(): Promise<void> {
    const c = comfortFor(this.api);
    const failed = await c.failedSends().catch(() => []);
    for (const f of failed) {
      this.toast(t('comfort.crypto.sendFailed', { subject: f.subject, error: f.error }), undefined, 'error');
      await c.dismissFailedSend(f.id).catch(() => {});
    }
  }

  replyInit(m: MessageDetail, mode: Exclude<ComposeMode, 'new'>, bodyHtml = ''): ComposerInit {
    const ident = this.identityFor(m);
    const own = new Set(this.identities.map((i) => i.email.toLowerCase()));
    const quoteHeader = t('compose.quoteHeader', { date: fmt().dateTime(m.date), name: m.from[0]?.name || m.from[0]?.email || '' });
    const original = m.text ?? '';
    if (mode === 'forward') {
      return {
        mode,
        identityId: ident?.id,
        subject: forwardSubject(m.subject),
        bodyHtml,
        quoteHtml: `<p>${escapeHtml(t('compose.forwardHeader'))}<br>${escapeHtml(t('compose.from'))}: ${escapeHtml(m.from.map((a) => `${a.name ?? ''} <${a.email}>`).join(', '))}<br>${escapeHtml(t('compose.subject'))}: ${escapeHtml(m.subject)}</p><blockquote>${textToHtml(original)}</blockquote>`,
        forwardOfMessageId: m.id,
        forwardAttachments: m.attachments,
      };
    }
    const fromOwn = m.from.some((a) => own.has(a.email.toLowerCase()));
    const to = fromOwn ? m.to : (m.replyTo?.length ? m.replyTo : m.from);
    const cc = mode === 'replyAll' ? [...(fromOwn ? [] : m.to), ...m.cc].filter((a) => !own.has(a.email.toLowerCase()) && !to.some((x) => x.email === a.email)) : [];
    return {
      mode,
      identityId: ident?.id,
      to,
      cc,
      subject: replySubject(m.subject),
      bodyHtml,
      quoteHtml: `<p>${escapeHtml(quoteHeader)}</p><blockquote>${textToHtml(original)}</blockquote>`,
      inReplyToMessageId: m.id,
    };
  }

  async replyTo(mode: Exclude<ComposeMode, 'new'>): Promise<void> {
    const m = this.message;
    if (!m) return;
    this.openCompose(this.replyInit(m, mode));
  }

  /** Channels the chat composer can use for the current conversation. */
  channelOptions(): ChannelOption[] {
    const conv = this.conversation;
    if (!conv) return [];
    const opts: ChannelOption[] = [];
    const lastMail = [...this.items].reverse().find((i) => i.channel === 'email');
    const participants = conv.participantIds.map((id) => this.person(id)).filter((p): p is Person => !!p);
    if (lastMail || participants.some((p) => p.handles.some((h) => h.kind === 'email'))) {
      const preferredAccount = lastMail?.accountId;
      const idents = [...this.identities].sort((a, b) => Number(b.accountId === preferredAccount) - Number(a.accountId === preferredAccount));
      for (const i of idents) opts.push({ key: `email:${i.id}`, channel: 'email', identityId: i.id, label: i.email });
    }
    for (const acc of this.messengerAccounts) {
      const ch: Channel | null = messengerChannel(acc.kind);
      if (!ch) continue;
      const reachable = conv.kind === 'chat' ? conv.accountIds.includes(acc.id) : participants.some((p) => p.handles.some((h) => h.kind === ch));
      if (reachable) opts.push({ key: `${ch}:${acc.id}`, channel: ch, accountId: acc.id, label: acc.extra?.address ?? acc.displayName });
    }
    // Default: last used channel in this conversation (KONZEPT 6.6).
    const last = this.items[this.items.length - 1];
    if (last) {
      const idx = opts.findIndex((o) => (last.channel === 'email' ? o.channel === 'email' && this.identities.find((i) => i.id === o.identityId)?.accountId === last.accountId : o.accountId === last.accountId));
      if (idx > 0) opts.unshift(...opts.splice(idx, 1));
    }
    return opts;
  }

  /**
   * Send from the messenger composer. Mail: a proper reply (In-Reply-To /
   * References via inReplyToMessageId, "Re:" subject generated and hidden,
   * signature as chosen in the composer).
   */
  async sendChat(
    text: string,
    option: ChannelOption,
    extra: {
      replyTo?: ConversationItem | null;
      signature?: Signature | null;
      attachments?: OutgoingAttachment[];
      sendAt?: string;
      cc?: Address[];
      /** Send checks confirmed by the user after a blocked result. */
      confirmChecks?: string[];
    } = {},
  ): Promise<SendResult | undefined> {
    const conv = this.conversation;
    const attachments = extra.attachments ?? [];
    if (!conv || (!text.trim() && attachments.length === 0)) return undefined;
    const sig = extra.signature ?? null;
    if (option.channel === 'email') {
      const target = extra.replyTo?.messageId != null ? extra.replyTo : [...this.items].reverse().find((i) => i.messageId != null);
      const textWithSig = text + (sig ? `\n\n-- \n${sig.plain}` : '');
      const html = `<p>${textToHtml(text)}</p>${sig ? `<div data-signature>-- <br>${sig.html}</div>` : ''}`;
      if (!target?.messageId) {
        const to = conv.participantIds
          .map((id) => this.person(id))
          .map((p) => ({ name: p?.displayName, email: p?.handles.find((h) => h.kind === 'email')?.value ?? '' }))
          .filter((a) => a.email);
        const res = await this.api.send({ channel: 'email', identityId: option.identityId, conversationId: conv.id, sendAt: extra.sendAt, to, cc: extra.cc ?? [], bcc: [], subject: text.split('\n')[0].slice(0, 60), text: textWithSig, html, attachments, confirmChecks: extra.confirmChecks });
        if (res.blocked) return res;
        await this.reloadItems(res.itemId);
        return res;
      }
      const original = await this.api.getMessage(target.messageId);
      const init = this.replyInit(original, conv.isGroup ? 'replyAll' : 'reply');
      const res = await this.api.send({
        channel: 'email',
        identityId: option.identityId,
        conversationId: conv.id,
        sendAt: extra.sendAt,
        to: init.to ?? [],
        cc: [...(init.cc ?? []), ...(extra.cc ?? []).filter((a) => !(init.cc ?? []).some((c) => c.email === a.email) && !(init.to ?? []).some((c) => c.email === a.email))],
        bcc: [],
        subject: init.subject ?? '',
        text: textWithSig,
        html,
        inReplyToMessageId: original.id,
        replyToItemId: extra.replyTo?.id,
        signatureId: sig?.id ?? null,
        attachments,
        confirmChecks: extra.confirmChecks,
      });
      if (res.blocked) return res;
      await this.reloadItems(res.itemId);
      if (res.scheduledId) this.toast(t('chat.schedule.scheduled', { when: fmt().dateTime(extra.sendAt!) }));
      return res;
    }
    const res = await this.api.send({
      channel: option.channel,
      accountId: option.accountId,
      conversationId: conv.id,
      sendAt: extra.sendAt,
      to: [],
      cc: [],
      bcc: [],
      subject: '',
      text,
      html: '',
      replyToItemId: extra.replyTo?.id,
      attachments,
    });
    await this.reloadItems(res.itemId);
    if (res.scheduledId) this.toast(t('chat.schedule.scheduled', { when: fmt().dateTime(extra.sendAt!) }));
    return res;
  }

  /** Signature for mails written in chat mode (identity setting: full / short / off). */
  chatSignatureFor(identityId: number): Signature | undefined {
    const ident = this.identities.find((i) => i.id === identityId);
    const mode = ident?.chatSignatureMode ?? 'full';
    if (mode === 'off') return undefined;
    if (mode === 'short' && ident?.chatSignatureId != null) {
      const short = this.signatures.find((s) => s.id === ident.chatSignatureId);
      if (short) return short;
    }
    return this.signatureFor(identityId, true);
  }

  // ----- chat folders, focus, snooze ----------------------------------------------

  get focusActive(): boolean {
    void this.now;
    return isFocusActive(this.focus, new Date(this.now));
  }

  async initChatFeatures(): Promise<void> {
    const [focus, freq, ai, snippets] = await Promise.all([
      this.api.getFocusSettings(),
      this.api.frequentContacts(),
      this.api.getAiSettings(),
      this.api.listSnippets(),
    ]);
    this.focus = focus;
    this.frequentIds = freq;
    this.aiSettings ??= ai; // a save that raced this initial load wins
    this.snippets = snippets;
    await this.loadChatFolders();
    setInterval(() => (this.now = Date.now()), 60_000);
  }

  async loadChatFolders(): Promise<void> {
    this.chatFolders = await this.api.listChatFolders();
  }

  async setActiveFolder(id: string | null): Promise<void> {
    this.activeFolderId = id;
    await this.loadConversations(true);
  }

  async saveChatFolder(folder: ChatFolder): Promise<void> {
    const saved = await this.api.saveChatFolder(folder);
    await this.loadChatFolders();
    this.activeFolderId = saved.id;
    await this.loadConversations(true);
  }

  async deleteChatFolder(id: string): Promise<void> {
    await this.api.deleteChatFolder(id);
    if (this.activeFolderId === id) this.activeFolderId = null;
    await this.loadChatFolders();
    await this.loadConversations(true);
  }

  /** Drag & drop a chat onto a folder tab. */
  async addToFolder(conversationId: number, folderId: string): Promise<void> {
    const f = this.chatFolders.find((x) => x.id === folderId);
    if (!f) return;
    const rules = { ...f.rules, includeConversationIds: [...new Set([...f.rules.includeConversationIds, conversationId])], excludeConversationIds: f.rules.excludeConversationIds.filter((i) => i !== conversationId) };
    await this.api.saveChatFolder({ ...f, rules });
    await this.loadChatFolders();
    if (this.activeFolderId === folderId) await this.loadConversations(true);
    this.toast(t('chat.folders.added', { name: f.name }));
  }

  async saveFocus(next: FocusSettings): Promise<void> {
    this.focus = await this.api.saveFocusSettings(next);
    await this.loadConversations(true);
  }

  async toggleFocus(): Promise<void> {
    await this.saveFocus({ ...this.focus, enabled: !this.focus.enabled });
    this.toast(this.focus.enabled ? t('chat.focus.on') : t('chat.focus.off'));
  }

  async loadWaiting(): Promise<void> {
    const page = await this.api.listConversations({ scope: { kind: 'all' }, grouping: prefs.grouping, offset: 0, limit: 500, focus: 'waiting', filter: this.chatFilter, folderId: this.activeFolderId ?? undefined });
    this.waitingCount = page.total;
    this.waitingConversations = page.items;
  }

  async snooze(conversationId: number, until: Date): Promise<void> {
    await this.api.updateConversation(conversationId, { snoozedUntil: until.toISOString() });
    if (this.selectedConversationId === conversationId) await this.selectConversation(null);
    await this.loadConversations(true);
    this.toast(t('chat.snooze.done', { when: fmt().dateTime(until) }), { label: t('common.action.undo'), run: () => void this.api.updateConversation(conversationId, { snoozedUntil: null }).then(() => this.loadConversations(true)) });
  }

  // ----- starred & pinned messages ----------------------------------------------------

  async toggleStar(item: ConversationItem): Promise<void> {
    await this.api.setItemStarred(item.conversationId, item.id, !item.starred);
    this.items = this.items.map((i) => (i.id === item.id ? { ...i, starred: !item.starred } : i));
  }

  async togglePin(item: ConversationItem): Promise<void> {
    try {
      await this.api.setItemPinned(item.conversationId, item.id, !item.pinned);
      await this.reloadItems();
      if (this.conversation) this.conversation = await this.api.getConversation(this.conversation.id);
    } catch (err) {
      this.toast(err instanceof ApiError && err.code === 'pin_limit' ? t('chat.pin.limit') : String(err), undefined, 'error');
    }
  }

  // ----- drafts & scheduled messages ---------------------------------------------------

  async scheduledAction(item: ConversationItem, action: 'now' | 'cancel'): Promise<void> {
    if (!item.scheduledId) return;
    if (action === 'now') await this.api.sendScheduledNow(item.scheduledId);
    else await this.api.cancelScheduled(item.scheduledId);
    await this.reloadItems();
    this.toast(action === 'now' ? t('chat.schedule.sentNow') : t('chat.schedule.cancelled'));
  }

  // ----- AI -------------------------------------------------------------------------------

  get aiReady(): boolean {
    return !!this.aiSettings && this.aiSettings.provider !== 'none';
  }

  async saveAiSettings(next: AiSettings): Promise<void> {
    this.aiSettings = await this.api.saveAiSettings(next);
  }

  /** Open Settings and scroll to the AI section. */
  openAiSettings(): void {
    this.openSettings('ai', 'assistant');
  }

  // ----- meeting proposals ----------------------------------------------------------------

  async proposeMeeting(p: { title: string; durationMin: number; slots: TimeSlot[] }, option: ChannelOption): Promise<void> {
    const conv = this.conversation;
    if (!conv) return;
    const res = await this.api.proposeMeeting(conv.id, { ...p, channel: option.channel, identityId: option.identityId, accountId: option.accountId });
    await this.reloadItems(res.itemId);
    this.toast(t('chat.meeting.sent'));
  }

  async acceptMeetingSlot(item: ConversationItem, slotStart: string): Promise<void> {
    if (!item.meeting) return;
    await this.api.acceptMeetingSlot(item.conversationId, item.meeting.id, slotStart);
    await this.reloadItems();
    this.toast(t('chat.meeting.booked', { when: fmt().full(slotStart) }));
  }

  // ----- messenger actions ------------------------------------------------------

  async setChatFilter(f: ChatFilter): Promise<void> {
    this.chatFilter = f;
    await this.loadConversations(true);
  }

  async setChatQuery(q: string): Promise<void> {
    this.chatQuery = q;
    await this.loadConversations(true);
  }

  async toggleArchivedView(show = !this.showArchived): Promise<void> {
    this.showArchived = show;
    await this.loadConversations(true);
  }

  async updateConversation(id: number, patch: ConversationPatch, toastKey?: MessageKey): Promise<void> {
    const before = this.conversations.find((c) => c.id === id) ?? (this.conversation?.id === id ? this.conversation : undefined);
    const updated = await this.api.updateConversation(id, patch);
    if (this.conversation?.id === id) this.conversation = updated;
    await this.loadConversations(true);
    if (toastKey) {
      const undo: ConversationPatch = {};
      for (const k of Object.keys(patch) as (keyof ConversationPatch)[]) (undo as Record<string, unknown>)[k] = before ? ((before as unknown as Record<string, unknown>)[k] ?? null) : !patch[k];
      this.toast(t(toastKey), { label: t('common.action.undo'), run: () => void this.updateConversation(id, undo) });
    }
  }

  async deleteConversation(id: number): Promise<void> {
    await this.api.deleteConversation(id);
    if (this.selectedConversationId === id) await this.selectConversation(null);
    await this.loadConversations(true);
    this.toast(t('chat.deleted'));
  }

  async react(item: ConversationItem, emoji: string): Promise<void> {
    const res = await this.api.reactToItem(item.conversationId, item.id, emoji);
    this.items = this.items.map((i) => (i.id === item.id ? { ...i, reactions: res.reactions } : i));
  }

  async deleteItem(item: ConversationItem): Promise<void> {
    await this.api.deleteItem(item.conversationId, item.id);
    this.items = this.items.filter((i) => i.id !== item.id);
    this.toast(item.channel === 'email' ? t('chat.mailMovedToTrash') : t('chat.messageDeleted'));
  }

  async forwardTo(item: ConversationItem, targetConversationId: number): Promise<void> {
    await this.api.forwardItem(item.id, targetConversationId);
    this.forwarding = null;
    const target = this.conversations.find((c) => c.id === targetConversationId);
    this.toast(t('chat.forwardedTo', { name: target?.title ?? '' }), { label: t('chat.open'), run: () => void this.selectConversation(targetConversationId) });
  }

  async createFromItem(item: ConversationItem, kind: 'task' | 'event'): Promise<void> {
    await this.api.createFromItem(item.conversationId, item.id, kind);
    this.toast(kind === 'task' ? t('chat.taskCreated') : t('chat.eventCreated'));
  }

  // -------------------------------------------------------------------------
  // Tags, projects, search folders
  // -------------------------------------------------------------------------

  async loadTags(): Promise<void> {
    this.tags = await this.api.listTags();
  }

  async loadProjects(): Promise<void> {
    this.projects = await this.api.listProjects();
  }

  async loadSearchFolders(): Promise<void> {
    this.searchFolders = await this.api.listSearchFolders();
  }

  /** Item refs for a conversation item (mail or messenger message). */
  itemRef(item: ConversationItem): ItemRef {
    if (item.messageId != null) return { kind: 'message', id: item.messageId };
    return { kind: 'chat_message', id: Number(item.id.replace(/^\w:/, '')) };
  }

  openTagPicker(items: ItemRef[], current: number[] = []): void {
    if (!items.length) return;
    this.organizeDialog = { kind: 'tags', items, current };
  }

  /** "t": tag the selection (checked mails, the open mail or the focused bubble). */
  tagSelection(): void {
    if (this.messageListView) {
      const ids = this.checked.length ? this.checked : this.selectedMessageId != null ? [this.selectedMessageId] : [];
      const current = ids.length === 1 ? (this.messages.find((m) => m.id === ids[0])?.tagIds ?? this.message?.tagIds ?? []) : [];
      this.openTagPicker(ids.map((id) => ({ kind: 'message' as const, id })), current);
    } else {
      const item = this.items.find((i) => i.id === this.focusedItemId) ?? this.items[this.items.length - 1];
      if (item) this.openTagPicker([this.itemRef(item)], item.tagIds ?? []);
    }
  }

  async applyTags(items: ItemRef[], add: number[], remove: number[]): Promise<void> {
    if (!add.length && !remove.length) return;
    await this.api.setItemTags(items, add, remove);
    const names = this.tagsOf(add).map((x) => x.path).join(', ');
    this.toast(add.length ? t('organize.tags.applied', { count: items.length, tags: names }) : t('organize.tags.removed', { count: items.length }), {
      label: t('common.action.undo'),
      run: () => void this.api.setItemTags(items, remove, add),
    });
  }

  async createTag(path: string, color: string): Promise<Tag> {
    const tag = await this.api.saveTag({ path, color });
    await this.loadTags();
    return tag;
  }

  openProjectPicker(items: ItemRef[]): void {
    if (items.length) this.organizeDialog = { kind: 'project', items };
  }

  async addToProject(projectId: number, items: ItemRef[]): Promise<void> {
    await this.api.addToProject(projectId, items);
    await this.loadProjects();
    const p = this.project(projectId);
    this.toast(t('organize.project.added', { count: items.length, project: p?.name ?? '' }), {
      label: t('organize.project.open'),
      run: () => this.openProject(projectId),
    });
  }

  openProject(id: number | null): void {
    this.selectedProjectId = id;
    this.navigate('projects');
  }

  async saveSearchFolder(input: SearchFolderInput): Promise<SearchFolder> {
    const saved = await this.api.saveSearchFolder(input);
    await this.loadSearchFolders();
    void this.loadChatFolders();
    this.toast(t('organize.searchFolder.saved', { name: saved.name }));
    return saved;
  }

  async deleteSearchFolder(id: number): Promise<void> {
    await this.api.deleteSearchFolder(id);
    if (this.sel.kind === 'searchFolder' && this.sel.searchFolderId === id) await this.select({ kind: 'unified', role: 'inbox' });
    await this.loadSearchFolders();
    void this.loadChatFolders();
  }

  async setChatTag(id: number | null): Promise<void> {
    this.chatTagId = id;
    await this.loadConversations(true);
  }

  toggleChecked(id: number, on = !this.checked.includes(id)): void {
    this.checked = on ? [...new Set([...this.checked, id])] : this.checked.filter((x) => x !== id);
  }

  async bulk(action: 'archive' | 'trash' | 'read' | 'unread' | 'flag'): Promise<void> {
    if (connection.offline && (action === 'archive' || action === 'trash')) return this.toast(t('connection.notAvailableOffline'));
    const ids = [...this.checked];
    if (!ids.length) return;
    if (action === 'read' || action === 'unread') await this.api.setFlags(ids, action === 'read' ? [FLAG_SEEN] : [], action === 'unread' ? [FLAG_SEEN] : []);
    else if (action === 'flag') await this.api.setFlags(ids, [FLAG_FLAGGED], []);
    else {
      for (const id of ids) {
        const m = this.messages.find((x) => x.id === id);
        const f = m && (this.folders[m.accountId] ?? []).find((x) => x.role === action);
        if (f) await this.api.move([id], f.id);
      }
      this.toast(t(action === 'archive' ? 'organize.bulk.archived' : 'organize.bulk.trashed', { count: ids.length }));
    }
    this.checked = [];
  }

  /** Hybrid view: drag a bubble onto a folder in the tree. */
  async moveItemToFolder(messageId: number, folderId: number): Promise<void> {
    const target = this.folder(folderId);
    const detail = await this.api.getMessage(messageId);
    const previous = detail.folderIds.find((f) => this.folder(f)?.accountId === target?.accountId);
    if (previous === folderId) return;
    await this.api.move([messageId], folderId);
    await this.reloadItems();
    this.toast(t('mail.toast.moved', { folder: this.folderPath(folderId) }), previous != null ? { label: t('common.action.undo'), run: () => void this.api.move([messageId], previous).then(() => this.reloadItems()) } : undefined);
  }

  async showOriginal(item: ConversationItem): Promise<void> {
    if (item.messageId == null) return;
    this.originalMessage = await this.api.getMessage(item.messageId);
  }

  /** "In klassischer Ansicht öffnen": focus the bubble/mail and switch. */
  async openInClassic(item?: ConversationItem | null): Promise<void> {
    if (item) this.focusItem(item.id);
    await this.setViewMode('classic');
  }

  async saveContactCard(personId: number, card: ContactCard): Promise<void> {
    await this.api.saveContactCard(personId, card);
    this.persons = await this.api.listPersons();
    this.toast(t('chat.info.contactSaved'));
  }

  async updateIdentity(identity: Identity): Promise<void> {
    const saved = await this.api.updateIdentity(identity);
    this.identities = this.identities.map((i) => (i.id === saved.id ? saved : i));
  }

  signatureFor(identityId: number, reply: boolean): Signature | undefined {
    const ident = this.identities.find((i) => i.id === identityId);
    const sid = (reply ? ident?.replySignatureId : undefined) ?? ident?.defaultSignatureId;
    return this.signatures.find((s) => s.id === sid);
  }
}

export interface ChannelOption {
  key: string;
  channel: Channel;
  identityId?: number;
  accountId?: number;
  label: string;
}
