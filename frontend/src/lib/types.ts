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
  | 'matrix_bridge';

export const MAIL_KINDS: readonly AccountKind[] = ['imap', 'pop3', 'jmap', 'eas', 'graph', 'ews', 'gmail'];
export const MESSENGER_KINDS: readonly AccountKind[] = ['whatsapp', 'signal', 'matrix_bridge'];

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

export type Channel = 'email' | 'whatsapp' | 'signal';
export type HandleKind = 'email' | 'phone' | 'whatsapp' | 'signal';

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
  reactions?: { emoji: string; count: number }[];
  unread?: boolean;
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
