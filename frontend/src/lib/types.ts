// SPDX-License-Identifier: AGPL-3.0-or-later
//
// TypeScript mirror of internal/domain/types.go plus the view-level DTOs the
// application layer exposes to the frontend (lists, details, conversations).
// JSON field names match the Go struct tags. time.Time is serialised as an
// RFC 3339 string.

export type AccountKind =
  | 'imap'
  | 'pop3'
  | 'smtp'
  | 'jmap'
  | 'eas'
  | 'graph'
  | 'ews'
  | 'gmail'
  | 'caldav'
  | 'carddav'
  | 'nextcloud'
  | 'opencloud'
  | 'vaultwarden'
  | 'whatsapp'
  | 'signal'
  | 'telegram'
  | 'matrix_bridge';

export const MAIL_KINDS: readonly AccountKind[] = ['imap', 'pop3', 'jmap', 'eas', 'graph', 'ews', 'gmail'];
export const MESSENGER_KINDS: readonly AccountKind[] = ['whatsapp', 'signal', 'telegram', 'matrix_bridge'];

/** Messenger channel of an account kind (null for mail/PIM accounts). */
export function messengerChannel(kind: AccountKind): 'whatsapp' | 'signal' | 'telegram' | null {
  return kind === 'whatsapp' || kind === 'signal' || kind === 'telegram' ? kind : null;
}

export type TLSMode = 'tls' | 'starttls' | 'none';
export type AuthMethod = 'password' | 'oauth2' | 'client_cert';

export interface ServerConfig {
  host: string;
  port: number;
  tls: TLSMode;
  auth: AuthMethod;
  username: string;
}

export interface Account {
  id: number;
  kind: AccountKind;
  displayName: string;
  color?: string;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
  /** Free-form provider data. Conventions used by the UI: `address` (primary e-mail or phone number). */
  extra?: Record<string, string>;
  secretRef?: string;
  enabled: boolean;
  sortOrder: number;
}

export interface Identity {
  id: number;
  accountId: number;
  name: string;
  email: string;
  replyTo?: string;
  bccSelf: boolean;
  smtpAccountId?: number;
  defaultSignatureId?: number;
  replySignatureId?: number;
  isDefault: boolean;
  /** Signature for mails sent from chat mode: full (reply/default), short, or none. Default 'full'. */
  chatSignatureMode?: 'full' | 'short' | 'off';
  /** Optional compact signature for chat mode (e.g. name + phone). */
  chatSignatureId?: number;
}

export interface Signature {
  id: number;
  name: string;
  html: string;
  plain: string;
  placement: 'below_reply' | 'above_quote';
}

export type FolderRole = '' | 'inbox' | 'sent' | 'drafts' | 'trash' | 'junk' | 'archive';

export interface Folder {
  id: number;
  accountId: number;
  parentId?: number;
  remoteId: string;
  name: string;
  delimiter?: string;
  role?: FolderRole;
  subscribed: boolean;
  unread: number;
  total: number;
}

export type Flag = '\\Seen' | '\\Answered' | '\\Flagged' | '\\Deleted' | '\\Draft' | '$Forwarded';
export const FLAG_SEEN: Flag = '\\Seen';
export const FLAG_FLAGGED: Flag = '\\Flagged';
export const FLAG_ANSWERED: Flag = '\\Answered';
export const FLAG_FORWARDED: Flag = '$Forwarded';

export interface Address {
  name?: string;
  email: string;
}

// ---------------------------------------------------------------------------
// View-level DTOs (application layer -> UI)
// ---------------------------------------------------------------------------

export type Channel = 'email' | 'whatsapp' | 'signal' | 'telegram';
export type HandleKind = 'email' | 'phone' | 'whatsapp' | 'signal' | 'telegram';

export interface PersonHandle {
  kind: HandleKind;
  value: string;
}

/** Cross-channel person (KONZEPT 10.2, table `persons` + `person_handles`). */
export interface Person {
  id: number;
  displayName: string;
  isMe: boolean;
  handles: PersonHandle[];
  /** Optional avatar URL (blob/data URL); the UI falls back to initials. */
  avatarUrl?: string;
  /** Messenger presence ("last seen"), if the channel shares it. */
  lastSeen?: string;
}

export type CloudProvider = 'nextcloud' | 'opencloud' | 'vaultwarden';

export interface CloudLinkInfo {
  provider: CloudProvider;
  url: string;
  expiresAt?: string;
}

export interface Attachment {
  id: number;
  filename: string;
  mimeType: string;
  size: number;
  contentId?: string;
  inline?: boolean;
  /** Set when the "attachment" is actually a cloud share link. */
  cloudLink?: CloudLinkInfo;
  /** Thumbnail/preview (images, videos); data: or blob: URL or core-served URL. */
  previewUrl?: string;
  width?: number;
  height?: number;
  /** Voice notes / audio. */
  durationSec?: number;
  waveform?: number[];
  /** Playable source, if available. */
  url?: string;
}

/** Row of the classic message list. */
export interface MessageSummary {
  id: number;
  accountId: number;
  /** A message may live in several folders (message_locations). */
  folderIds: number[];
  conversationId: number;
  messageId: string;
  subject: string;
  from: Address[];
  to: Address[];
  cc: Address[];
  date: string;
  preview: string;
  flags: Flag[];
  hasAttachments: boolean;
  size: number;
}

export interface MessageDetail extends MessageSummary {
  inReplyTo: string[];
  references: string[];
  bcc?: Address[];
  replyTo?: Address[];
  /** Sanitised by the backend; still rendered only inside a sandboxed iframe. */
  html?: string;
  text?: string;
  attachments: Attachment[];
  /** Whether the HTML references remote content that was blocked. */
  hasRemoteContent?: boolean;
}

export type ConversationKind = 'email_thread' | 'chat' | 'person_view';
export type ConversationGrouping = 'thread' | 'person';

export interface Conversation {
  id: number;
  kind: ConversationKind;
  title: string;
  lastActivity: string;
  unreadCount: number;
  pinned: boolean;
  muted: boolean;
  archived: boolean;
  /** Channels that occur in the conversation (for the icons in the list). */
  channels: Channel[];
  /** Person ids of all participants except "me". */
  participantIds: number[];
  /** Messenger accounts / mail accounts involved. */
  accountIds: number[];
  snippet: string;
  lastSenderName?: string;
  isGroup: boolean;
  favorite: boolean;
  /** Manually marked unread ("Als ungelesen markieren"). */
  markedUnread: boolean;
  blocked?: boolean;
  /** Last item info for the chat list preview. */
  lastIsOwn: boolean;
  lastStatus?: DeliveryStatus;
  lastChannel?: Channel;
  /** Avatar (photo) of the person/group, if known. */
  avatarUrl?: string;
  /** Last item had no text but media: the UI shows a localized label. */
  lastMedia?: 'photo' | 'voice' | 'file';
  lastMediaName?: string;
}

export type DeliveryStatus = 'pending' | 'sent' | 'delivered' | 'read' | 'failed';

/** One bubble in the chat timeline. Mail and chat messages share this shape. */
export interface ConversationItem {
  /** Stable item id: `m:<messageId>` for mails, `c:<chatMessageId>` for messenger messages. */
  id: string;
  conversationId: number;
  channel: Channel;
  accountId: number;
  senderPersonId: number;
  senderName: string;
  isOwn: boolean;
  sentAt: string;
  /** Only the new text (quotes and signatures removed). */
  body: string;
  /** The collapsed quoted history / signature, if any. */
  quoted?: string;
  subject?: string;
  relation?: 'reply' | 'forward';
  attachments: Attachment[];
  /** Set for mail items: the MessageSummary id. */
  messageId?: number;
  status?: DeliveryStatus;
  reactions?: Reaction[];
  unread?: boolean;
  /** 'card' = compact rendering for newsletters / HTML-heavy mails. */
  display?: 'bubble' | 'card';
  card?: { title: string; excerpt: string };
  /** The message this one answers (quote shown above the text). */
  replyTo?: { itemId: string; senderName: string; excerpt: string };
  linkPreview?: LinkPreview;
  /** Incoming mail signature (hidden in the bubble, shown as contact card in the info panel). */
  signature?: string;
  deleted?: boolean;
}

export interface Reaction {
  emoji: string;
  count: number;
  /** Includes a reaction of the current user. */
  mine?: boolean;
}

export interface LinkPreview {
  url: string;
  title: string;
  description?: string;
  siteName?: string;
  imageUrl?: string;
}

export type ChatFilter = 'all' | 'unread' | 'mail' | 'whatsapp' | 'signal' | 'telegram' | 'groups' | 'favorites';

export interface ConversationPatch {
  pinned?: boolean;
  muted?: boolean;
  archived?: boolean;
  favorite?: boolean;
  markedUnread?: boolean;
  blocked?: boolean;
}

/** Fields parsed from a mail signature ("Visitenkarte"). */
export interface ContactCard {
  name?: string;
  title?: string;
  company?: string;
  phones: string[];
  emails: string[];
  address?: string;
  website?: string;
}

export interface Page<T> {
  items: T[];
  total: number;
  offset: number;
}

// ---------------------------------------------------------------------------
// Compose / send
// ---------------------------------------------------------------------------

export type AttachmentDelivery = 'attach' | 'nextcloud' | 'opencloud' | 'vaultwarden';
export type PasswordDeliveryMode = 'inline' | 'separate_mail' | 'messenger' | 'none';

export interface OutgoingAttachment {
  /** Token returned by Api.uploadAttachment. */
  uploadToken: string;
  filename: string;
  mimeType: string;
  size: number;
  delivery: AttachmentDelivery;
  durationSec?: number;
}

export interface PasswordDelivery {
  mode: PasswordDeliveryMode;
  /** Messenger account used when mode === 'messenger'. */
  messengerAccountId?: number;
  rememberForRecipient: boolean;
}

export interface OutgoingMessage {
  channel: Channel;
  /** Mail: sending identity. */
  identityId?: number;
  /** Messenger: account to send from. */
  accountId?: number;
  /** Messenger: target conversation (chat). */
  conversationId?: number;
  to: Address[];
  cc: Address[];
  bcc: Address[];
  subject: string;
  html: string;
  text: string;
  /** Mail being replied to / forwarded (sets In-Reply-To/References, KONZEPT 6.3). */
  inReplyToMessageId?: number;
  forwardOfMessageId?: number;
  attachments: OutgoingAttachment[];
  passwordDelivery?: PasswordDelivery;
  /** Messenger-style reply: the bubble being answered. */
  replyToItemId?: string;
  /** Mail from chat mode: signature to append (resolved by the UI), null = none. */
  signatureId?: number | null;
}

export interface SendResult {
  /** Mail: id of the stored sent message. */
  messageId?: number;
  conversationId?: number;
  itemId?: string;
  queued: boolean;
  /** Generated share passwords; returned so the UI can copy them when mode === 'none'. */
  sharePasswords?: { filename: string; password: string }[];
}

// ---------------------------------------------------------------------------
// Account setup (KONZEPT 7.5)
// ---------------------------------------------------------------------------

export interface DiscoveryResult {
  source: 'autoconfig' | 'autodiscover' | 'srv' | 'guess' | 'none';
  kind: AccountKind;
  displayName: string;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
  oauthAvailable?: boolean;
}

export interface NewAccountInput {
  kind: AccountKind;
  displayName: string;
  email: string;
  name: string;
  password?: string;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
}

// ---------------------------------------------------------------------------
// Events (KONZEPT 4.2 #5)
// ---------------------------------------------------------------------------

export type ApiEvent =
  | { type: 'message.added'; messageId: number; conversationId: number; folderIds: number[] }
  | { type: 'message.updated'; messageId: number }
  | { type: 'message.removed'; messageId: number }
  | { type: 'conversation.updated'; conversationId: number; itemId?: string }
  | { type: 'folder.updated'; folderId: number; accountId: number }
  | { type: 'account.updated'; accountId: number }
  | { type: 'connection'; state: 'online' | 'offline' | 'reconnecting' };

export type ApiEventType = ApiEvent['type'];

// ---------------------------------------------------------------------------
// Push notifications (server/web mode, PWA)
// ---------------------------------------------------------------------------

/** Payload the server sends via Web Push; handled in public/sw.js. */
export interface PushPayload {
  type: string; // 'message' | 'chat' | 'calendar' | 'test' | ...
  title: string;
  body?: string;
  /** App-relative URL to open on click, e.g. "./#/mail". */
  url?: string;
  tag?: string;
  accountId?: number;
  conversationId?: number;
}

export interface PushSubscriptionInput {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  userAgent?: string;
  deviceName?: string;
}

export type NotificationPrivacy = 'full' | 'generic';

export interface NotificationSettings {
  /** 'full' = sender + preview, 'generic' = only "Neue Nachricht". */
  privacy: NotificationPrivacy;
  quietHours: { enabled: boolean; start: string; end: string };
  /** Per account on/off (missing = on). JSON object keys are account ids. */
  accounts: Record<string, boolean>;
  channels: Record<Channel, boolean>;
}

// ---------------------------------------------------------------------------
// Connect a desktop client to a TuxMail server (device flow / provisioning)
// ---------------------------------------------------------------------------

/** RFC 8628 device authorization (server route POST /api/device/authorize). */
export interface DeviceAuthorization {
  deviceCode: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete: string;
  expiresIn: number;
  interval: number;
}

export interface ProvisioningWorkflow {
  id: string;
  name: string;
  description?: string;
}

/** What the server offers to set up on this device. Secrets stay in the core. */
export interface ProvisioningProfile {
  server: { url: string; name: string; version: string };
  user: { name: string; email: string };
  accounts: Account[];
  identities: Identity[];
  signatures: Signature[];
  workflows: ProvisioningWorkflow[];
}

export type DeviceTokenStatus = 'pending' | 'slow_down' | 'approved' | 'denied' | 'expired';

/** Result of POST /api/device/token or a direct login. Tokens are kept by the core, never by the UI. */
export interface DeviceTokenResult {
  status: DeviceTokenStatus;
  profile?: ProvisioningProfile;
  error?: string;
}

export interface ServerLoginInput {
  username: string;
  password: string;
  totp?: string;
}

/** thin = server syncs, desktop is a thin client; local = accounts sync on this device. */
export type ClientMode = 'thin' | 'local';

export interface ProvisioningImport {
  serverUrl: string;
  mode: ClientMode;
  accountIds: number[];
  identityIds: number[];
  signatureIds: number[];
  workflowIds: string[];
}

// ---------------------------------------------------------------------------
// Search (KONZEPT 8.4 / 8.4.1)
// ---------------------------------------------------------------------------

export type SearchScope = 'mail' | 'chat' | 'event' | 'task' | 'contact' | 'file';
export type SearchSort = 'relevance' | 'date' | 'sender' | 'size';
export type SearchLocation = 'local' | 'server' | 'auto';

export interface SearchQuery {
  /** Free text (simple mode) or a query in the expert language. */
  text: string;
  mode: 'simple' | 'expert';
  /** Empty = all scopes. */
  scopes: SearchScope[];
  /** Empty = all accounts. */
  accountIds: number[];
  /** Empty = all folders. */
  folderIds: number[];
  includeSubfolders: boolean;
  excludeTrashSpam: boolean;
  /** ISO dates (inclusive). */
  from?: string;
  to?: string;
  location: SearchLocation;
  sort: SearchSort;
  cursor?: string;
  limit?: number;
}

export type SearchHitKind = 'mail' | 'chat' | 'event' | 'task' | 'contact' | 'file';

export interface SearchHit {
  id: string;
  kind: SearchHitKind;
  title: string;
  /** Plain-text snippet; `highlights` are [start, end) ranges into it. */
  snippet: string;
  highlights: [number, number][];
  /** Ranges in `title` to highlight. */
  titleHighlights: [number, number][];
  date?: string;
  accountId?: number;
  folderId?: number;
  channel?: Channel;
  sender?: string;
  size?: number;
  hasAttachments?: boolean;
  unread?: boolean;
  score: number;
  /** Navigation targets. */
  messageId?: number;
  conversationId?: number;
  itemId?: string;
  personId?: number;
}

export interface FacetBucket {
  key: string;
  label: string;
  count: number;
  /** Clause the UI appends to the query when the bucket is clicked, e.g. account:"Kanzlei Sommer". */
  clause: string;
}

export interface SearchFacets {
  account: FacetBucket[];
  folder: FacetBucket[];
  sender: FacetBucket[];
  domain: FacetBucket[];
  channel: FacetBucket[];
  type: FacetBucket[];
  attachment: FacetBucket[];
}

export interface SearchResult {
  hits: SearchHit[];
  total: number;
  facets: SearchFacets;
  /** Per month, oldest first. */
  histogram: FacetBucket[];
  nextCursor?: string;
  /** Expert query error; the server then falls back to a simple search. */
  error?: { message: string; position: number; code?: string; token?: string };
  fallback?: boolean;
  tookMs: number;
}

export interface SearchProfile {
  id: string;
  name: string;
  query: SearchQuery;
  pinned: boolean;
}

export interface SearchHistoryEntry {
  text: string;
  mode: 'simple' | 'expert';
  at: string;
}

// ---------------------------------------------------------------------------
// Runtime-loadable languages
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// User preferences (Settings -> Language & region)
// ---------------------------------------------------------------------------

export interface RegionSettings {
  /** IANA zone or 'system' (always follow the device). */
  timeZone: string;
  dateFormat: 'locale' | 'DD.MM.YYYY' | 'YYYY-MM-DD' | 'MM/DD/YYYY' | 'DD/MM/YYYY' | 'D. MMMM YYYY' | 'custom';
  customDatePattern: string;
  timeFormat: 'locale' | '24' | '12';
  /** 'locale' or 0 = Sunday, 1 = Monday, 6 = Saturday. */
  weekStart: 'locale' | 0 | 1 | 6;
  numberFormat: 'locale' | '1.234,56' | '1,234.56' | '1 234,56';
  relativeTimes: boolean;
  /** Extra zone shown in the calendar ('' = none). */
  secondaryTimeZone: string;
}

export interface UserPreferences {
  /** UI language code or 'system' (navigator.languages). */
  locale: string;
  region: RegionSettings;
}

export interface LocaleInfo {
  code: string;
  name: string;
  nativeName: string;
  direction: 'ltr' | 'rtl';
}
