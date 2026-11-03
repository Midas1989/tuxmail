// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Transport-independent API contract between the frontend and the Go core
// (KONZEPT 4.2 #4, 4.5). The desktop build calls these methods through Wails
// bindings, the web client through HTTPS/JSON + a WebSocket for events.
// Method names map 1:1 to Go methods on the bound application service and to
// the REST routes documented in http.ts.

import type {
  Account,
  ApiEvent,
  Conversation,
  ConversationGrouping,
  ConversationItem,
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
} from '../types';

export type TransportKind = 'wails' | 'http' | 'mock';

/** What the message list shows. */
export type MessageScope =
  | { kind: 'folder'; folderId: number }
  /** Virtual folder across all mail accounts (e.g. unified inbox). */
  | { kind: 'unified'; role: 'inbox' | 'sent' | 'drafts' | 'archive' | 'trash' | 'junk' | 'flagged' | 'unread' };

export interface MessageQuery {
  scope: MessageScope;
  offset: number;
  limit: number;
  search?: string;
}

export type ConversationScope =
  | { kind: 'all' }
  | { kind: 'account'; accountId: number }
  | { kind: 'folder'; folderId: number };

export interface ConversationQuery {
  scope: ConversationScope;
  grouping: ConversationGrouping;
  offset: number;
  limit: number;
  search?: string;
}

export interface ItemsQuery {
  /** Return items older than this item id (paging backwards). */
  before?: string;
  limit?: number;
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

  // Live events
  subscribe(handler: (event: ApiEvent) => void): Unsubscribe;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(message: string, status = 0, code = 'error') {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
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
