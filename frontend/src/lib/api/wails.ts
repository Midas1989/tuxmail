// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WailsTransport: desktop build. Wails v2 exposes bound Go structs as
// `window.go.<package>.<Struct>.<Method>(...args): Promise<T>` and runtime
// events as `window.runtime.EventsOn(name, cb) => unsubscribe`.
//
// TODO(wails): once the Go application service exists, generate typed
//   bindings (`wails generate module`) and replace the dynamic `call()` with
//   imports from `wailsjs/go/app/Api`. The method names below are the
//   contract: the bound Go struct must expose exactly these methods.
// TODO(wails): confirm package/struct name (`app.Api` assumed here).
// TODO(wails): the core emits every ApiEvent on the runtime event "tuxmail:event".

import type { Api, ConversationQuery, ConversationRef, ItemsQuery, MessageQuery, TransportKind, Unsubscribe } from './api';
import { ApiError, EventHub } from './api';
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
      // Wails rejects with the Go error string.
      throw new ApiError(typeof err === 'string' ? err : String(err), 0, 'backend');
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
}
