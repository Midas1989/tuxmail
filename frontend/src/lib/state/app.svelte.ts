// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Central UI state (Svelte 5 runes). All data comes from the Api; this class
// only orchestrates loading, selection and the classic <-> chat switch.

import type { Api, ConversationScope, MessageScope } from '../api/api';
import { classicSelectionFromChat, forwardSubject, messageIdFromItemId, pickFolderForMessage, replySubject } from '../conversation';
import { fmt, t, type MessageKey } from '../i18n/index.svelte';
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
} from '../types';
import { prefs } from './prefs.svelte';

export type Route = 'mail' | 'search' | 'calendar' | 'contacts' | 'files' | 'settings';
export const ROUTES: Route[] = ['mail', 'search', 'calendar', 'contacts', 'files', 'settings'];
export type ViewMode = 'classic' | 'chat';
export type UnifiedRole = 'inbox' | 'flagged' | 'unread';

export type SidebarSel =
  | { kind: 'unified'; role: UnifiedRole }
  | { kind: 'folder'; accountId: number; folderId: number }
  | { kind: 'account'; accountId: number };

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
}

export interface Toast {
  id: number;
  text: string;
  actionLabel?: string;
  action?: () => void;
  kind?: 'info' | 'error';
}

const PAGE = 200;

function debounce<A extends unknown[]>(fn: (...a: A) => void, ms: number) {
  let h: ReturnType<typeof setTimeout> | null = null;
  return (...a: A) => {
    if (h) clearTimeout(h);
    h = setTimeout(() => {
      h = null;
      fn(...a);
    }, ms);
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

  // overlays
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

  person(id: number): Person | undefined {
    return this.persons.find((p) => p.id === id);
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

  async init(): Promise<void> {
    try {
      await prefs.connect(this.api);
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
      await Promise.all(this.mailAccounts.map((a) => this.reloadFolders(a.id)));
      this.unsubscribe = this.api.subscribe((e) => this.onEvent(e));
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

  private onEvent(e: ApiEvent): void {
    switch (e.type) {
      case 'connection':
        this.connection = e.state;
        break;
      case 'folder.updated':
        this.pendingFolderAccounts.push(e.accountId);
        this.reloadFoldersDebounced([...this.pendingFolderAccounts]);
        this.pendingFolderAccounts = [];
        break;
      case 'account.updated':
        void this.api.listAccounts().then((a) => {
          this.accounts = a.sort((x, y) => x.sortOrder - y.sortOrder);
          return this.reloadFolders(e.accountId);
        });
        void this.api.listIdentities().then((i) => (this.identities = i));
        break;
      case 'message.added':
      case 'message.updated':
      case 'message.removed':
        this.reloadListDebounced();
        if (this.selectedMessageId === e.messageId && e.type === 'message.updated') void this.refreshMessage();
        break;
      case 'conversation.updated':
        if (this.viewMode === 'chat') this.reloadListDebounced();
        if (this.selectedConversationId === e.conversationId) this.reloadItemsDebounced(e.itemId);
        break;
    }
  }

  // -------------------------------------------------------------------------
  // Navigation
  // -------------------------------------------------------------------------

  navigate(route: Route): void {
    this.route = route;
    if (typeof location !== 'undefined' && location.hash !== '#/' + route) history.replaceState(null, '', '#/' + route);
  }

  messageScope(): MessageScope {
    const s = this.sel;
    if (s.kind === 'folder') return { kind: 'folder', folderId: s.folderId };
    if (s.kind === 'unified') return { kind: 'unified', role: s.role };
    return { kind: 'unified', role: 'inbox' };
  }

  conversationScope(): ConversationScope {
    // The messenger view never shows folders: chats aggregate all of them.
    if (this.viewMode === 'chat') return { kind: 'all' };
    const s = this.sel;
    if (s.kind === 'folder') return { kind: 'folder', folderId: s.folderId };
    if (s.kind === 'account') return { kind: 'account', accountId: s.accountId };
    return { kind: 'all' };
  }

  async select(sel: SidebarSel): Promise<void> {
    this.sel = sel;
    this.sidebarOpen = false;
    this.selectedMessageId = null;
    this.message = null;
    this.selectedConversationId = null;
    this.conversation = null;
    this.items = [];
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
    if (this.viewMode === 'classic') await this.loadMessages(opts.keepSelection);
    else await this.loadConversations(opts.keepSelection);
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
      const page = await this.api.listConversations({
        scope: this.conversationScope(),
        grouping: prefs.grouping,
        offset: 0,
        limit: 500,
        search: (this.viewMode === 'chat' ? this.chatQuery : this.search) || undefined,
        filter: this.viewMode === 'chat' ? this.chatFilter : undefined,
        archived: this.viewMode === 'chat' ? this.showArchived : undefined,
      });
      this.conversations = page.items;
      if (this.viewMode === 'chat' && !this.showArchived) {
        void this.api
          .listConversations({ scope: { kind: 'all' }, grouping: prefs.grouping, offset: 0, limit: 1, archived: true })
          .then((p) => (this.archivedCount = p.total));
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

  async selectMessage(id: number | null, opts: { scroll?: boolean } = {}): Promise<void> {
    this.selectedMessageId = id;
    if (this.seenTimer) clearTimeout(this.seenTimer);
    if (id == null) {
      this.message = null;
      return;
    }
    if (opts.scroll) this.listScrollTick++;
    const detail = await this.api.getMessage(id);
    if (this.selectedMessageId !== id) return;
    this.message = detail;
    if (!detail.flags.includes(FLAG_SEEN)) {
      this.seenTimer = setTimeout(() => {
        if (this.selectedMessageId === id) void this.api.setFlags([id], [FLAG_SEEN], []);
      }, 700);
    }
  }

  private async refreshMessage(): Promise<void> {
    if (this.selectedMessageId == null) return;
    const id = this.selectedMessageId;
    const d = await this.api.getMessage(id);
    if (this.selectedMessageId === id) this.message = d;
  }

  moveSelection(delta: 1 | -1): void {
    if (this.viewMode === 'classic') {
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
    const items = await this.api.listConversationItems(id);
    if (this.selectedConversationId !== id) return;
    this.items = items;
    const firstUnread = items.find((i) => i.unread && !i.isOwn);
    this.unreadFromId = firstUnread?.id ?? null;
    this.timelineScrollTarget = focusItemId ?? firstUnread?.id ?? null;
    this.timelineScrollTick++;
    if (this.conversation && (this.conversation.unreadCount > 0 || this.conversation.markedUnread)) {
      void this.api.markConversationRead(id);
    }
  }

  private async reloadItems(newItemId?: string): Promise<void> {
    const id = this.selectedConversationId;
    if (id == null) return;
    const items = await this.api.listConversationItems(id);
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

  // -------------------------------------------------------------------------
  // Classic <-> chat (KONZEPT 6.6): the selection survives the switch.
  // -------------------------------------------------------------------------

  async setViewMode(mode: ViewMode): Promise<void> {
    if (mode === this.viewMode) return;
    if (mode === 'chat') await this.toChat();
    else await this.toClassic();
    prefs.save();
  }

  toggleViewMode(): Promise<void> {
    return this.setViewMode(this.viewMode === 'classic' ? 'chat' : 'classic');
  }

  private async toChat(): Promise<void> {
    const msgId = this.selectedMessageId;
    prefs.viewMode = 'chat';
    if (msgId == null) {
      await this.loadConversations();
      return;
    }
    const ref = await this.api.conversationForMessage(msgId, prefs.grouping);
    await this.loadConversations(true);
    await this.selectConversation(ref.conversationId, ref.itemId);
  }

  private async toClassic(): Promise<void> {
    const msgId = classicSelectionFromChat(this.items, this.focusedItemId);
    prefs.viewMode = 'classic';
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
    if (s.kind === 'account') return false;
    if (s.role === 'flagged') return m.flags.includes(FLAG_FLAGGED);
    if (s.role === 'unread') return true;
    return m.folderIds.some((id) => this.folder(id)?.role === 'inbox');
  }

  /** Open a mail in the classic view (search results, notifications). */
  async revealMessage(messageId: number): Promise<void> {
    this.navigate('mail');
    prefs.viewMode = 'classic';
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
  async sendChat(text: string, option: ChannelOption, extra: { replyTo?: ConversationItem | null; signature?: Signature | null; attachments?: OutgoingAttachment[] } = {}): Promise<void> {
    const conv = this.conversation;
    const attachments = extra.attachments ?? [];
    if (!conv || (!text.trim() && attachments.length === 0)) return;
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
        const res = await this.api.send({ channel: 'email', identityId: option.identityId, to, cc: [], bcc: [], subject: text.split('\n')[0].slice(0, 60), text: textWithSig, html, attachments });
        await this.reloadItems(res.itemId);
        return;
      }
      const original = await this.api.getMessage(target.messageId);
      const init = this.replyInit(original, conv.isGroup ? 'replyAll' : 'reply');
      const res = await this.api.send({
        channel: 'email',
        identityId: option.identityId,
        to: init.to ?? [],
        cc: init.cc ?? [],
        bcc: [],
        subject: init.subject ?? '',
        text: textWithSig,
        html,
        inReplyToMessageId: original.id,
        replyToItemId: extra.replyTo?.id,
        signatureId: sig?.id ?? null,
        attachments,
      });
      await this.reloadItems(res.itemId);
      return;
    }
    const res = await this.api.send({
      channel: option.channel,
      accountId: option.accountId,
      conversationId: conv.id,
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
      for (const k of Object.keys(patch) as (keyof ConversationPatch)[]) undo[k] = before ? (before[k] as boolean) : !patch[k];
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
