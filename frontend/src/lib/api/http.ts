// SPDX-License-Identifier: AGPL-3.0-or-later
//
// HttpTransport: web client in server mode (`tuxmail serve`, KONZEPT 4.5).
//
// REST routes (all JSON, session cookie auth, CSRF token echoed in a header):
//   GET  /api/accounts                          -> Account[]
//   POST /api/accounts                          NewAccountInput -> Account
//   POST /api/accounts/discover                 {email,password} -> DiscoveryResult
//   GET  /api/accounts/{id}/folders             -> Folder[]
//   GET  /api/identities                        -> Identity[]
//   GET  /api/signatures                        -> Signature[]
//   GET  /api/persons                           -> Person[]
//   GET  /api/messages?scope=folder&folderId=&offset=&limit=&q=
//   GET  /api/messages?scope=unified&role=inbox&offset=&limit=&q=   -> Page<MessageSummary>
//   GET  /api/messages/{id}                     -> MessageDetail
//   GET  /api/messages/{id}/conversation?grouping=thread|person -> ConversationRef
//   POST /api/messages/move                     {ids, folderId}
//   POST /api/messages/flags                    {ids, add, remove}
//   GET  /api/conversations?scope=all|account|folder&accountId=&folderId=&grouping=&offset=&limit=&q=
//   GET  /api/conversations/{id}                -> Conversation
//   GET  /api/conversations/{id}/items?before=&limit= -> ConversationItem[]
//   POST /api/conversations/{id}/read
//   PATCH /api/conversations/{id}               ConversationPatch -> Conversation
//   DELETE /api/conversations/{id}
//   POST /api/conversations/{id}/items/{itemId}/reactions  {emoji} -> {reactions, sentAsRfc9078}
//   DELETE /api/conversations/{id}/items/{itemId}
//   POST /api/conversations/{id}/items/{itemId}/create     {kind: task|event} -> {id}
//   POST /api/items/{itemId}/forward            {conversationId} -> SendResult
//   PUT  /api/persons/{id}/card                 ContactCard
//   PUT  /api/identities/{id}                   Identity -> Identity
//   GET/PUT/DELETE /api/chat-folders[/{id}]     ChatFolder (with unreadCount)
//   PUT  /api/conversations/{id}/items/{itemId}/star|pin   {starred}|{pinned}
//   GET  /api/starred?conversationId=           -> StarredItem[]
//   GET/PUT /api/focus                          FocusSettings;  GET /api/persons/frequent -> number[]
//   GET/PUT/DELETE /api/conversations/{id}/draft  ChatDraft
//   GET  /api/scheduled?conversationId=  PATCH/DELETE /api/scheduled/{id}  POST /api/scheduled/{id}/send
//   GET/PUT/DELETE /api/snippets[/{id}]         Snippet
//   GET/PUT /api/ai/settings; POST /api/ai/conversations/{id}/suggest|summarize, …/items/{itemId}/translate
//   GET  /api/calendar/free-slots?from=&to=&durationMin=   -> TimeSlot[]
//   POST /api/conversations/{id}/meeting-proposals          -> SendResult
//   POST /api/conversations/{id}/meeting-proposals/{pid}/accept  {slotStart}
//   POST /api/send                              OutgoingMessage -> SendResult
//   POST /api/uploads                           raw body, X-Filename header -> {uploadToken}
//   GET  /api/push/vapid-public-key             -> {key}
//   POST /api/push/subscribe                    PushSubscriptionInput
//   POST /api/push/unsubscribe                  {endpoint}
//   POST /api/push/test
//   GET  /api/notifications/settings            -> NotificationSettings
//   PUT  /api/notifications/settings            NotificationSettings -> NotificationSettings
//   POST /api/provisioning/import               ProvisioningImport
//   POST /api/search                            SearchQuery -> SearchResult
//   GET  /api/search/profiles                   -> SearchProfile[]
//   PUT  /api/search/profiles/{id}              SearchProfile -> SearchProfile
//   DELETE /api/search/profiles/{id}
//   GET  /api/search/history                    -> SearchHistoryEntry[]
//   DELETE /api/search/history
//   GET  /api/preferences                       -> UserPreferences
//   PUT  /api/preferences                       UserPreferences -> UserPreferences
//   GET  /api/locales                           -> LocaleInfo[]
//   GET  /api/locales/{code}                    -> nested JSON (same shape as locales/en.json)
//   WS   /api/events                          server -> client: one ApiEvent JSON per frame
//
// Device flow (RFC 8628) against a *remote* TuxMail server (`serverUrl`):
//   POST {serverUrl}/api/device/authorize       {client} -> DeviceAuthorization
//   POST {serverUrl}/api/device/token           {deviceCode} -> DeviceTokenResult
//   POST {serverUrl}/api/auth/login             ServerLoginInput -> DeviceTokenResult
// In the desktop build these run inside the Go core (WailsTransport), which
// keeps the tokens in the secret store; the HTTP variant exists for completeness.

import type { Api, ConversationQuery, ConversationRef, ItemsQuery, MessageQuery, TransportKind, Unsubscribe } from './api';
import { ApiError, EventHub } from './api';
import type {
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

export interface HttpTransportOptions {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
  webSocketImpl?: typeof WebSocket;
  /** Initial reconnect delay in ms (doubles up to maxReconnectDelay). */
  reconnectDelay?: number;
  maxReconnectDelay?: number;
  /** Device token (native app): sent as `Authorization: Bearer`, replaces cookies + CSRF. */
  authToken?: () => string | undefined;
  /** Called on HTTP 401 (expired/revoked device token). */
  onUnauthorized?: () => void;
}

function readCsrfToken(): string | undefined {
  if (typeof document === 'undefined') return undefined;
  const meta = document.querySelector<HTMLMetaElement>('meta[name="tuxmail-csrf"]');
  if (meta?.content) return meta.content;
  const m = document.cookie.match(/(?:^|;\s*)tuxmail_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : undefined;
}

export function buildQuery(params: Record<string, string | number | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

export class HttpTransport implements Api {
  readonly kind: TransportKind = 'http';
  private readonly base: string;
  private readonly fetchImpl: typeof fetch;
  private readonly wsImpl: typeof WebSocket | undefined;
  private readonly hub = new EventHub();
  private socket: WebSocket | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private delay: number;
  private readonly initialDelay: number;
  private readonly maxDelay: number;
  private readonly authToken: () => string | undefined;
  private readonly onUnauthorized: (() => void) | undefined;

  constructor(opts: HttpTransportOptions = {}) {
    this.base = (opts.baseUrl ?? '').replace(/\/$/, '');
    this.fetchImpl = opts.fetchImpl ?? ((...args) => fetch(...args));
    this.wsImpl = opts.webSocketImpl ?? (typeof WebSocket !== 'undefined' ? WebSocket : undefined);
    this.initialDelay = this.delay = opts.reconnectDelay ?? 1000;
    this.maxDelay = opts.maxReconnectDelay ?? 30000;
    this.authToken = opts.authToken ?? (() => undefined);
    this.onUnauthorized = opts.onUnauthorized;
  }

  private async request<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
    const h: Record<string, string> = { Accept: 'application/json', ...headers };
    const bearer = this.authToken();
    const csrf = bearer ? undefined : readCsrfToken();
    if (bearer) h.Authorization = `Bearer ${bearer}`;
    if (csrf) h['X-TuxMail-CSRF'] = csrf;
    let payload: BodyInit | undefined;
    if (body instanceof Blob) {
      payload = body;
    } else if (body !== undefined) {
      h['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    let res: Response;
    try {
      res = await this.fetchImpl(this.base + path, { method, headers: h, body: payload, credentials: bearer ? 'omit' : 'same-origin' });
    } catch (err) {
      throw new ApiError(err instanceof Error ? err.message : 'network error', 0, 'network');
    }
    if (!res.ok) {
      if (res.status === 401 && bearer) this.onUnauthorized?.();
      let msg = res.statusText;
      let code = 'http_' + res.status;
      try {
        const j = (await res.json()) as { error?: string; code?: string };
        if (j.error) msg = j.error;
        if (j.code) code = j.code;
      } catch {
        /* body not JSON */
      }
      throw new ApiError(msg, res.status, code);
    }
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  listAccounts(): Promise<Account[]> {
    return this.request('GET', '/api/accounts');
  }
  listIdentities(): Promise<Identity[]> {
    return this.request('GET', '/api/identities');
  }
  listSignatures(): Promise<Signature[]> {
    return this.request('GET', '/api/signatures');
  }
  listPersons(): Promise<Person[]> {
    return this.request('GET', '/api/persons');
  }
  discoverAccount(email: string, password: string): Promise<DiscoveryResult> {
    return this.request('POST', '/api/accounts/discover', { email, password });
  }
  createAccount(input: NewAccountInput): Promise<Account> {
    return this.request('POST', '/api/accounts', input);
  }
  listFolders(accountId: number): Promise<Folder[]> {
    return this.request('GET', `/api/accounts/${accountId}/folders`);
  }
  listMessages(q: MessageQuery): Promise<Page<MessageSummary>> {
    const scope = q.scope;
    const qs = buildQuery({
      scope: scope.kind,
      folderId: scope.kind === 'folder' ? scope.folderId : undefined,
      role: scope.kind === 'unified' ? scope.role : undefined,
      offset: q.offset,
      limit: q.limit,
      q: q.search,
    });
    return this.request('GET', `/api/messages${qs}`);
  }
  getMessage(id: number): Promise<MessageDetail> {
    return this.request('GET', `/api/messages/${id}`);
  }
  listConversations(q: ConversationQuery): Promise<Page<Conversation>> {
    const s = q.scope;
    const qs = buildQuery({
      scope: s.kind,
      accountId: s.kind === 'account' ? s.accountId : undefined,
      folderId: s.kind === 'folder' ? s.folderId : undefined,
      grouping: q.grouping,
      filter: q.filter,
      archived: q.archived ? 1 : undefined,
      offset: q.offset,
      limit: q.limit,
      q: q.search,
    });
    return this.request('GET', `/api/conversations${qs}`);
  }
  getConversation(id: number): Promise<Conversation> {
    return this.request('GET', `/api/conversations/${id}`);
  }
  listConversationItems(conversationId: number, query: ItemsQuery = {}): Promise<ConversationItem[]> {
    return this.request('GET', `/api/conversations/${conversationId}/items${buildQuery({ before: query.before, limit: query.limit })}`);
  }
  conversationForMessage(messageId: number, grouping: ConversationGrouping): Promise<ConversationRef> {
    return this.request('GET', `/api/messages/${messageId}/conversation${buildQuery({ grouping })}`);
  }
  markConversationRead(conversationId: number): Promise<void> {
    return this.request('POST', `/api/conversations/${conversationId}/read`);
  }
  updateConversation(conversationId: number, patch: ConversationPatch): Promise<Conversation> {
    return this.request('PATCH', `/api/conversations/${conversationId}`, patch);
  }
  deleteConversation(conversationId: number): Promise<void> {
    return this.request('DELETE', `/api/conversations/${conversationId}`);
  }
  reactToItem(conversationId: number, itemId: string, emoji: string): Promise<{ reactions: Reaction[]; sentAsRfc9078?: boolean }> {
    return this.request('POST', `/api/conversations/${conversationId}/items/${encodeURIComponent(itemId)}/reactions`, { emoji });
  }
  deleteItem(conversationId: number, itemId: string): Promise<void> {
    return this.request('DELETE', `/api/conversations/${conversationId}/items/${encodeURIComponent(itemId)}`);
  }
  forwardItem(itemId: string, targetConversationId: number): Promise<SendResult> {
    return this.request('POST', `/api/items/${encodeURIComponent(itemId)}/forward`, { conversationId: targetConversationId });
  }
  createFromItem(conversationId: number, itemId: string, kind: 'task' | 'event'): Promise<{ id: string }> {
    return this.request('POST', `/api/conversations/${conversationId}/items/${encodeURIComponent(itemId)}/create`, { kind });
  }
  saveContactCard(personId: number, card: ContactCard): Promise<void> {
    return this.request('PUT', `/api/persons/${personId}/card`, card);
  }
  updateIdentity(identity: Identity): Promise<Identity> {
    return this.request('PUT', `/api/identities/${identity.id}`, identity);
  }
  send(message: OutgoingMessage): Promise<SendResult> {
    return this.request('POST', '/api/send', message);
  }
  uploadAttachment(file: Blob, filename: string): Promise<{ uploadToken: string }> {
    return this.request('POST', '/api/uploads', file, { 'X-Filename': encodeURIComponent(filename) });
  }
  move(messageIds: number[], toFolderId: number): Promise<void> {
    return this.request('POST', '/api/messages/move', { ids: messageIds, folderId: toFolderId });
  }
  setFlags(messageIds: number[], add: Flag[], remove: Flag[]): Promise<void> {
    return this.request('POST', '/api/messages/flags', { ids: messageIds, add, remove });
  }

  async getPushVapidKey(): Promise<string> {
    return (await this.request<{ key: string }>('GET', '/api/push/vapid-public-key')).key;
  }
  subscribePush(subscription: PushSubscriptionInput): Promise<void> {
    return this.request('POST', '/api/push/subscribe', subscription);
  }
  unsubscribePush(endpoint: string): Promise<void> {
    return this.request('POST', '/api/push/unsubscribe', { endpoint });
  }
  getNotificationSettings(): Promise<NotificationSettings> {
    return this.request('GET', '/api/notifications/settings');
  }
  saveNotificationSettings(settings: NotificationSettings): Promise<NotificationSettings> {
    return this.request('PUT', '/api/notifications/settings', settings);
  }
  sendTestNotification(): Promise<void> {
    return this.request('POST', '/api/push/test');
  }

  private remote(serverUrl: string, path: string): string {
    return serverUrl.replace(/\/+$/, '') + path;
  }
  private async remotePost<T>(url: string, body: unknown): Promise<T> {
    let res: Response;
    try {
      res = await this.fetchImpl(url, { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' }, body: JSON.stringify(body) });
    } catch (err) {
      throw new ApiError(err instanceof Error ? err.message : 'network error', 0, 'network');
    }
    const data = (await res.json().catch(() => ({}))) as T & { error?: string; status?: string };
    if (!res.ok && !data.status) throw new ApiError(data.error ?? res.statusText, res.status, 'http_' + res.status);
    return data;
  }
  deviceAuthorize(serverUrl: string): Promise<DeviceAuthorization> {
    return this.remotePost(this.remote(serverUrl, '/api/device/authorize'), { client: 'tuxmail-web' });
  }
  deviceToken(serverUrl: string, deviceCode: string): Promise<DeviceTokenResult> {
    return this.remotePost(this.remote(serverUrl, '/api/device/token'), { deviceCode });
  }
  serverLogin(serverUrl: string, credentials: ServerLoginInput): Promise<DeviceTokenResult> {
    return this.remotePost(this.remote(serverUrl, '/api/auth/login'), credentials);
  }
  importProvisioning(input: ProvisioningImport): Promise<void> {
    return this.request('POST', '/api/provisioning/import', input);
  }
  search(query: SearchQuery): Promise<SearchResult> {
    return this.request('POST', '/api/search', query);
  }
  listSearchProfiles(): Promise<SearchProfile[]> {
    return this.request('GET', '/api/search/profiles');
  }
  saveSearchProfile(profile: SearchProfile): Promise<SearchProfile> {
    return this.request('PUT', `/api/search/profiles/${encodeURIComponent(profile.id || 'new')}`, profile);
  }
  deleteSearchProfile(id: string): Promise<void> {
    return this.request('DELETE', `/api/search/profiles/${encodeURIComponent(id)}`);
  }
  getSearchHistory(): Promise<SearchHistoryEntry[]> {
    return this.request('GET', '/api/search/history');
  }
  clearSearchHistory(): Promise<void> {
    return this.request('DELETE', '/api/search/history');
  }
  getPreferences(): Promise<UserPreferences> {
    return this.request('GET', '/api/preferences');
  }
  setPreferences(prefs: UserPreferences): Promise<UserPreferences> {
    return this.request('PUT', '/api/preferences', prefs);
  }
  listLocales(): Promise<LocaleInfo[]> {
    return this.request('GET', '/api/locales');
  }
  getLocale(code: string): Promise<Record<string, unknown>> {
    return this.request('GET', `/api/locales/${encodeURIComponent(code)}`);
  }

  listChatFolders(): Promise<ChatFolder[]> {
    return this.request('GET', '/api/chat-folders');
  }
  saveChatFolder(folder: ChatFolder): Promise<ChatFolder> {
    return this.request('PUT', `/api/chat-folders/${encodeURIComponent(folder.id || 'new')}`, folder);
  }
  deleteChatFolder(id: string): Promise<void> {
    return this.request('DELETE', `/api/chat-folders/${encodeURIComponent(id)}`);
  }
  setItemStarred(conversationId: number, itemId: string, starred: boolean): Promise<void> {
    return this.request('PUT', `/api/conversations/${conversationId}/items/${encodeURIComponent(itemId)}/star`, { starred });
  }
  setItemPinned(conversationId: number, itemId: string, pinned: boolean): Promise<void> {
    return this.request('PUT', `/api/conversations/${conversationId}/items/${encodeURIComponent(itemId)}/pin`, { pinned });
  }
  listStarredItems(conversationId?: number): Promise<StarredItem[]> {
    return this.request('GET', `/api/starred${buildQuery({ conversationId })}`);
  }
  getFocusSettings(): Promise<FocusSettings> {
    return this.request('GET', '/api/focus');
  }
  saveFocusSettings(settings: FocusSettings): Promise<FocusSettings> {
    return this.request('PUT', '/api/focus', settings);
  }
  frequentContacts(): Promise<number[]> {
    return this.request('GET', '/api/persons/frequent');
  }
  getDraft(conversationId: number): Promise<ChatDraft | null> {
    return this.request('GET', `/api/conversations/${conversationId}/draft`);
  }
  saveDraft(conversationId: number, draft: ChatDraft | null): Promise<void> {
    return draft ? this.request('PUT', `/api/conversations/${conversationId}/draft`, draft) : this.request('DELETE', `/api/conversations/${conversationId}/draft`);
  }
  listScheduled(conversationId?: number): Promise<ScheduledMessage[]> {
    return this.request('GET', `/api/scheduled${buildQuery({ conversationId })}`);
  }
  updateScheduled(id: string, patch: { text?: string; sendAt?: string }): Promise<ScheduledMessage> {
    return this.request('PATCH', `/api/scheduled/${encodeURIComponent(id)}`, patch);
  }
  cancelScheduled(id: string): Promise<void> {
    return this.request('DELETE', `/api/scheduled/${encodeURIComponent(id)}`);
  }
  sendScheduledNow(id: string): Promise<void> {
    return this.request('POST', `/api/scheduled/${encodeURIComponent(id)}/send`);
  }
  listSnippets(): Promise<Snippet[]> {
    return this.request('GET', '/api/snippets');
  }
  saveSnippet(snippet: Snippet): Promise<Snippet> {
    return this.request('PUT', `/api/snippets/${encodeURIComponent(snippet.id || 'new')}`, snippet);
  }
  deleteSnippet(id: string): Promise<void> {
    return this.request('DELETE', `/api/snippets/${encodeURIComponent(id)}`);
  }
  getAiSettings(): Promise<AiSettings> {
    return this.request('GET', '/api/ai/settings');
  }
  saveAiSettings(settings: AiSettings): Promise<AiSettings> {
    return this.request('PUT', '/api/ai/settings', settings);
  }
  aiSuggestReplies(conversationId: number): Promise<string[]> {
    return this.request('POST', `/api/ai/conversations/${conversationId}/suggest`);
  }
  aiTranslate(conversationId: number, itemId: string, targetLang: string): Promise<{ text: string; sourceLang: string }> {
    return this.request('POST', `/api/ai/conversations/${conversationId}/items/${encodeURIComponent(itemId)}/translate`, { targetLang });
  }
  aiSummarize(conversationId: number): Promise<AiSummary> {
    return this.request('POST', `/api/ai/conversations/${conversationId}/summarize`);
  }
  findFreeSlots(query: { from: string; to: string; durationMin: number }): Promise<TimeSlot[]> {
    return this.request('GET', `/api/calendar/free-slots${buildQuery(query)}`);
  }
  proposeMeeting(conversationId: number, proposal: { title: string; durationMin: number; slots: TimeSlot[]; channel: Channel; identityId?: number; accountId?: number }): Promise<SendResult> {
    return this.request('POST', `/api/conversations/${conversationId}/meeting-proposals`, proposal);
  }
  acceptMeetingSlot(conversationId: number, proposalId: string, slotStart: string): Promise<void> {
    return this.request('POST', `/api/conversations/${conversationId}/meeting-proposals/${encodeURIComponent(proposalId)}/accept`, { slotStart });
  }
  subscribe(handler: (event: ApiEvent) => void): Unsubscribe {
    const off = this.hub.add(handler);
    this.connect();
    return () => {
      off();
      if (this.hub.size === 0) this.disconnect();
    };
  }

  private wsUrl(): string {
    if (this.base.startsWith('http')) return this.base.replace(/^http/, 'ws') + '/api/events';
    const loc = window.location;
    return `${loc.protocol === 'https:' ? 'wss' : 'ws'}://${loc.host}${this.base}/api/events`;
  }

  private connect(): void {
    if (this.socket || !this.wsImpl) return;
    let ws: WebSocket;
    try {
      // Browsers cannot set headers on WebSockets: the device token travels as
      // subprotocol (server echoes "tuxmail.v1"), never in the URL/logs.
      const bearer = this.authToken();
      ws = bearer ? new this.wsImpl(this.wsUrl(), ['tuxmail.v1', `tuxmail.bearer.${bearer}`]) : new this.wsImpl(this.wsUrl());
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.socket = ws;
    ws.onopen = () => {
      this.delay = this.initialDelay;
      this.hub.emit({ type: 'connection', state: 'online' });
    };
    ws.onmessage = (ev: MessageEvent) => {
      try {
        const data = JSON.parse(typeof ev.data === 'string' ? ev.data : '') as ApiEvent;
        if (data && typeof data.type === 'string') this.hub.emit(data);
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onclose = () => {
      this.socket = null;
      if (this.hub.size > 0) {
        this.hub.emit({ type: 'connection', state: 'reconnecting' });
        this.scheduleReconnect();
      }
    };
    ws.onerror = () => ws.close();
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return;
    // Jitter avoids thundering herds after a server restart.
    const wait = this.delay * (0.75 + Math.random() * 0.5);
    this.delay = Math.min(this.delay * 2, this.maxDelay);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, wait);
  }

  private disconnect(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    const s = this.socket;
    this.socket = null;
    if (s) {
      s.onclose = null;
      s.close();
    }
  }
}
