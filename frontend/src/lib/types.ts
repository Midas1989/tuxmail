// SPDX-License-Identifier: AGPL-3.0-or-later
//
// TypeScript mirror of internal/domain/types.go plus the view-level DTOs the
// application layer exposes to the frontend (lists, details, conversations).
// JSON field names match the Go struct tags. time.Time is serialised as an
// RFC 3339 string.

export type AccountKind =
  | 'imap'
  /** Local Folders: mail stored only in TuxMail (imports, archives). */
  | 'local'
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
  | 'matrix'
  | 'matrix_bridge';

export const MAIL_KINDS: readonly AccountKind[] = ['imap', 'pop3', 'jmap', 'eas', 'graph', 'ews', 'gmail'];
export const MESSENGER_KINDS: readonly AccountKind[] = ['whatsapp', 'signal', 'telegram', 'matrix', 'matrix_bridge'];

/** Messenger channel of an account kind (null for mail/PIM accounts). */
export function messengerChannel(kind: AccountKind): 'whatsapp' | 'signal' | 'telegram' | 'matrix' | null {
  if (kind === 'matrix_bridge') return 'matrix';
  return kind === 'whatsapp' || kind === 'signal' || kind === 'telegram' || kind === 'matrix' ? kind : null;
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
  /** Keep `plain` as written instead of generating it from `html`. */
  plainOverride?: boolean;
  /** Stored inline images (the UI receives them as data: URIs inside `html`). */
  images?: SignatureImage[];
}

export interface SignatureImage {
  contentId: string;
  mimeType: string;
  filename?: string;
  blobHash: string;
  size: number;
}

export type FolderRole = '' | 'inbox' | 'sent' | 'drafts' | 'trash' | 'junk' | 'archive' | 'templates';
/** How a folder's role was determined (see FolderConfig). */
export type RoleSource = 'server' | 'name' | 'user';

export interface Folder {
  id: number;
  accountId: number;
  parentId?: number;
  remoteId: string;
  name: string;
  delimiter?: string;
  role?: FolderRole;
  roleSource?: RoleSource;
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

export type Channel = 'email' | 'whatsapp' | 'signal' | 'telegram' | 'matrix';
export type HandleKind = 'email' | 'phone' | 'whatsapp' | 'signal' | 'telegram' | 'matrix';

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
  /** Tags assigned to the mail. */
  tagIds?: number[];
  /** Projects the mail is linked to. */
  projectIds?: number[];
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
  /** Remote images are already loaded through the image proxy (sender/domain allowlisted). */
  remoteLoaded?: boolean;
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
  /** Tags of the conversation (its chats and items). */
  tagIds?: number[];
  lastMediaName?: string;
  /** Up to three pinned messages (banner at the top of the chat). */
  pinnedItemIds?: string[];
  /** Hidden until this time (snooze). */
  snoozedUntil?: string;
  /** Snooze ended: shown on top with a "back" marker until opened. */
  returnedFromSnooze?: boolean;
  /** Unsent text in the composer ("Draft: …" in the list). */
  draft?: string;
  /** Number of scheduled (not yet sent) messages. */
  scheduledCount?: number;
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
  display?: 'bubble' | 'card' | 'system';
  card?: { title: string; excerpt: string; kind?: 'newsletter' };
  /** The mail has an HTML part ("Ganze Mail anzeigen"). */
  hasHtml?: boolean;
  /** Only the headers are synced yet; the body is being fetched. */
  bodyMissing?: boolean;
  /** The message this one answers (quote shown above the text). */
  replyTo?: { itemId: string; senderName: string; excerpt: string };
  linkPreview?: LinkPreview;
  /** Incoming mail signature (hidden in the bubble, shown as contact card in the info panel). */
  signature?: string;
  deleted?: boolean;
  starred?: boolean;
  pinned?: boolean;
  /** Scheduled, not yet sent message ("Scheduled for …"). */
  scheduledFor?: string;
  scheduledId?: string;
  /** System line in the timeline (rendered localized by the UI). */
  system?: { kind: 'eventCreated'; title: string; start: string };
  /** Meeting proposal with selectable slots. */
  meeting?: MeetingProposal;
  /** Encrypted (PGP/S-MIME) items are never sent to cloud AI providers. */
  encrypted?: boolean;
  /** Mail items: folders the mail lives in (hybrid view folder chips). */
  folderIds?: number[];
  /** Tags assigned to the item. */
  tagIds?: number[];
  /** Set when listConversationItems was called with a scope: the item lies in that scope (hybrid view highlight). */
  inScope?: boolean;
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

export type ChatFilter = 'all' | 'unread' | 'mail' | 'whatsapp' | 'signal' | 'telegram' | 'matrix' | 'groups' | 'favorites';

export interface ConversationPatch {
  /** ISO time, null = wake up now. */
  snoozedUntil?: string | null;
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
  /** Schedule instead of sending now (ISO). */
  sendAt?: string;
  /** Send checks the user confirmed after a blocked result ("send anyway", KONZEPT 8.13). */
  confirmChecks?: string[];
  /** S/MIME / OpenPGP choice of the composer (comfort features). */
  crypto?: import('./api/comfort').OutgoingCrypto;
}

export interface SendResult {
  /** Mail: id of the stored sent message. */
  messageId?: number;
  conversationId?: number;
  itemId?: string;
  queued: boolean;
  /** Generated share passwords; returned so the UI can copy them when mode === 'none'. */
  sharePasswords?: { filename: string; password: string }[];
  /** Set when the message was scheduled (sendAt). */
  scheduledId?: string;
  /** Send checks (KONZEPT 8.13) blocked the message; nothing was sent. */
  blocked?: boolean;
  /** Send-check results (blocking findings or warnings). */
  findings?: SendFinding[];
}

export interface SendFinding {
  check: string;
  severity: 'info' | 'warn' | 'block';
  /** i18n key, e.g. "sendcheck.missing_subject". */
  key: string;
  /** English fallback text. */
  message: string;
  params?: Record<string, string>;
  /** Ready texts by locale ("de", "en"). */
  localized?: Record<string, string>;
}

/** Secret store of the desktop client: OS keyring (installed), master-password vault (portable) or the server. */
export interface VaultStatus {
  kind: 'keyring' | 'vault' | 'server';
  portable: boolean;
  /** Portable mode: the vault must be unlocked with the master password (Api.unlockVault). */
  locked: boolean;
  /** False until a master password was set (first unlock creates the vault). */
  initialized: boolean;
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
  /** ActiveSync endpoint found by Autodiscover (kind "eas"). */
  easUrl?: string;
  /** The proposed servers answered the probe (connection, TLS, greeting; login with the password when given). */
  verified?: boolean;
  /** All found configurations, best first (the first is the proposal), with probe results. */
  candidates?: DiscoveryCandidate[];
}

/** Probe result of one server of a discovery candidate. */
export interface DiscoveryCheck {
  reachable: boolean;
  /** "ok", "failed" (password rejected) or absent (not tried). */
  login?: 'ok' | 'failed';
  error?: string;
}

export interface DiscoveryCandidate {
  source: string;
  kind: AccountKind;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
  easUrl?: string;
  in: DiscoveryCheck;
  out: DiscoveryCheck;
  verified: boolean;
  insecure?: boolean;
  score: number;
}

export interface NewAccountInput {
  kind: AccountKind;
  displayName: string;
  email: string;
  name: string;
  password?: string;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
  /** ActiveSync server URL (kind "eas"); username may be DOMAIN\\user. */
  easUrl?: string;
  /** POP3: server deletion policy (default: leave on server). */
  pop3?: Pop3Options;
  /** JMAP: session URL (default https://<incoming host>/.well-known/jmap). */
  jmapUrl?: string;
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
  /** Sync state and progress of one account (throttled to ~4/s). */
  | { type: 'sync.status'; accountId: number; status: AccountSyncStatus }
  | { type: 'connection'; state: 'online' | 'offline' | 'reconnecting' }
  /** Tags were created, renamed, merged, deleted or (un)assigned. */
  | { type: 'tags.changed' }
  | { type: 'project.updated'; projectId: number }
  | { type: 'searchfolders.changed' }
  | { type: 'workflow.run'; workflowId: string; run: WorkflowRunSummary }
  /** Desktop portable mode: the secret vault needs the master password (Api.unlockVault). */
  | { type: 'vault.locked' }
  | PimEvent
  /** Import/export/backup job changed (poll Api.getIOJob). */
  | { type: 'io.job'; itemId: string; state: string }
  | MessengerApiEvent;

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

// ---------------------------------------------------------------------------
// Chat-mode features: folders, starred/pinned, focus, drafts, scheduling,
// snippets, AI, meeting proposals
// ---------------------------------------------------------------------------

export interface ChatFolderRules {
  includeConversationIds: number[];
  includePersonIds: number[];
  excludeConversationIds: number[];
  channels: Channel[];
  accountIds: number[];
  groups: 'any' | 'only' | 'exclude';
  unreadOnly: boolean;
  excludeMuted: boolean;
  excludeArchived: boolean;
}

/** User-defined chat list (Telegram-style folder). Unrelated to mail folders. */
export interface ChatFolder {
  id: string;
  name: string;
  emoji?: string;
  rules: ChatFolderRules;
  order: number;
  /** Filled by the core in listChatFolders. */
  unreadCount?: number;
  /** Virtual folder backed by a search folder ("Als Chat-Ordner verwenden"); not editable as a chat folder. */
  searchFolderId?: number;
}

export interface StarredItem {
  item: ConversationItem;
  conversationTitle: string;
}

export interface FocusSettings {
  enabled: boolean;
  vipPersonIds: number[];
  vipConversationIds: number[];
  /** Also treat people I write with frequently as VIP. */
  includeFrequent: boolean;
  schedule?: { enabled: boolean; start: string; end: string; days: number[] };
}

export interface ChatDraft {
  text: string;
  replyToItemId?: string;
  channelKey?: string;
  updatedAt?: string;
}

export interface ScheduledMessage {
  id: string;
  conversationId: number;
  conversationTitle: string;
  channel: Channel;
  sendAt: string;
  text: string;
  itemId: string;
}

export interface Snippet {
  id: string;
  /** Without the slash, e.g. "danke". */
  trigger: string;
  title: string;
  /** May contain {vorname}, {name}, {datum}, {uhrzeit}, {meinname}. */
  text: string;
}

export type AiProvider = 'none' | 'ollama' | 'openai' | 'gemini' | 'deepseek' | 'anthropic' | 'mistral' | 'custom';

export interface AiSettings {
  provider: AiProvider;
  baseUrl: string;
  model: string;
  /** Write-only: the core never returns the key, only apiKeySet. */
  apiKey?: string;
  apiKeySet: boolean;
  /** Per account: may content of this account be sent to the provider? */
  accountPermissions: Record<string, boolean>;
  maskPersonalData: boolean;
}

export interface AiSummary {
  summary: string;
  bullets: string[];
  /** Number of items that were left out (e.g. encrypted). */
  skipped: number;
}

export interface TimeSlot {
  start: string;
  end: string;
}

export interface MeetingProposal {
  id: string;
  title: string;
  durationMin: number;
  slots: TimeSlot[];
  status: 'open' | 'booked';
  chosen?: string;
}

// ---------------------------------------------------------------------------
// Items (generic references used by tags, projects and workflows)
// ---------------------------------------------------------------------------

/** Kind of an item that can be tagged / linked to a project (mirrors store.ItemKind). */
export type ItemKind = 'message' | 'conversation' | 'chat' | 'chat_message' | 'event' | 'task' | 'contact' | 'person' | 'file' | 'search_folder' | 'project';

export interface ItemRef {
  kind: ItemKind;
  id: number;
}

// ---------------------------------------------------------------------------
// Tags (hierarchical, coloured; synced as IMAP keywords / Gmail labels / Outlook categories)
// ---------------------------------------------------------------------------

export interface Tag {
  id: number;
  parentId?: number;
  /** Leaf name, e.g. "Müller". */
  name: string;
  /** Full path with "/" separators, e.g. "Kunde/Müller". */
  path: string;
  /** CSS colour (#rrggbb). */
  color: string;
  /** Icon name from the tag icon set (optional). */
  icon?: string;
  sortOrder: number;
}

export interface TagCount {
  tag: Tag;
  /** Items tagged with exactly this tag. */
  direct: number;
  /** Distinct items tagged with this tag or a descendant. */
  total: number;
  /** Directly tagged unread mails. */
  unread: number;
}

export interface TagInput {
  id?: number;
  /** Full path; missing parents are created. */
  path: string;
  color: string;
  icon?: string;
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

export type ProjectStatus = 'planned' | 'active' | 'paused' | 'done' | 'archived';

export type CustomFieldType = 'text' | 'number' | 'date' | 'url' | 'select';

export interface CustomField {
  key: string;
  label: string;
  type: CustomFieldType;
  value: string;
  options?: string[];
}

export interface ProjectBudget {
  amount: number;
  currency: string;
  spent?: number;
}

export interface Project {
  id: number;
  /** Short key, e.g. "HB-26" (also usable in search: project:HB-26). */
  key: string;
  name: string;
  status: ProjectStatus;
  color: string;
  description: string;
  customer?: string;
  /** Person of the customer contact, if known. */
  customerPersonId?: number;
  startDate?: string;
  dueDate?: string;
  budget?: ProjectBudget;
  customFields: CustomField[];
  memberPersonIds: number[];
  /** Linked cloud folder (Nextcloud/WebDAV path). */
  cloudFolder?: { provider: string; path: string; url?: string };
  notes: string;
  tagIds: number[];
  /** Auto-assignment: items matching the expert query are linked automatically. */
  rules: { enabled: boolean; query: string };
  /** 0..1, computed from done tasks. */
  progress: number;
  counts: { mail: number; chat: number; event: number; task: number; file: number };
  templateId?: string;
  createdAt: string;
  updatedAt: string;
}

export type ProjectInput = Omit<Project, 'id' | 'progress' | 'counts' | 'createdAt' | 'updatedAt'> & { id?: number };

export interface ProjectTemplate {
  id: string;
  name: string;
  description: string;
  color: string;
  /** Custom fields pre-filled for new projects. */
  fields: CustomField[];
  /** Workflow templates bound to a project created from this template. */
  workflowTemplateIds: string[];
  /** Default auto-assign query; "{key}" is replaced by the project key. */
  ruleQuery?: string;
}

export type TimelineKind = 'mail' | 'chat' | 'event' | 'task' | 'file' | 'note';

export interface ProjectTimelineEntry {
  item: ItemRef;
  kind: TimelineKind;
  date: string;
  title: string;
  snippet?: string;
  /** Sender / organizer / owner. */
  personId?: number;
  actorName?: string;
  channel?: Channel;
  /** Mail/chat: where to open it. */
  conversationId?: number;
  messageId?: number;
  itemId?: string;
  /** Tasks. */
  done?: boolean;
  due?: string;
  /** Events. */
  end?: string;
  location?: string;
  /** Files. */
  path?: string;
  size?: number;
  /** How it got into the project. */
  source: 'manual' | 'rule' | 'workflow';
}

// ---------------------------------------------------------------------------
// Search folders (saved searches shown as virtual folders)
// ---------------------------------------------------------------------------

export interface SearchFolder {
  id: number;
  name: string;
  /** Expert query (KONZEPT 8.4.1). */
  query: string;
  /** Shown inside an account or project in the tree; global when both are unset. */
  accountId?: number;
  projectId?: number;
  /** Also offered as a chat folder tab in the messenger view. */
  useAsChatFolder: boolean;
  icon?: string;
  color?: string;
  /** Computed by the core. */
  total: number;
  unread: number;
}

export type SearchFolderInput = Omit<SearchFolder, 'id' | 'total' | 'unread'> & { id?: number };

// ---------------------------------------------------------------------------
// Workflows (KONZEPT 8.5): trigger -> condition -> actions
// ---------------------------------------------------------------------------

export type WorkflowEditorMode = 'visual' | 'rule' | 'yaml';

export interface WorkflowTrigger {
  /** Event type, e.g. "mail.received". */
  event: string;
  accounts?: number[];
  folders?: string[];
  channels?: string[];
  schedule?: string;
  name?: string;
}

/** Structured condition (mirrors workflow.Rule): exactly one of all / any / not / field+op(+value). */
export interface WorkflowRule {
  all?: WorkflowRule[];
  any?: WorkflowRule[];
  not?: WorkflowRule;
  field?: string;
  op?: string;
  value?: string | number | boolean | string[];
}

/** Either an expr-lang expression or a structured rule. */
export type WorkflowCondition = { expr: string; rule?: undefined } | { rule: WorkflowRule; expr?: undefined };

export interface WorkflowAction {
  type: string;
  /** Type-specific parameters (inlined next to "type" in YAML). */
  params: Record<string, unknown>;
  /** Optional per-action condition (expression). */
  if?: string;
  continueOnError?: boolean;
}

export interface WorkflowRunSummary {
  id: string;
  startedAt: string;
  status: 'ok' | 'failed' | 'skipped' | 'running';
  message?: string;
}

export interface Workflow {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
  trigger: WorkflowTrigger;
  condition?: WorkflowCondition;
  actions: WorkflowAction[];
  options?: Record<string, unknown>;
  /** Bound to a project (shown in the project's Workflows tab). */
  projectId?: number;
  /** Template the workflow was created from. */
  templateId?: string;
  /** Computed by the core: at least one action has an external effect (sends mail, calls a webhook ...). */
  externalEffects?: boolean;
  lastRun?: WorkflowRunSummary;
  updatedAt?: string;
}

export interface WorkflowRunStep {
  action: string;
  status: 'ok' | 'failed' | 'skipped';
  summary: string;
  durationMs: number;
}

export interface WorkflowRun extends WorkflowRunSummary {
  workflowId: string;
  finishedAt?: string;
  trigger: string;
  item?: { kind: ItemKind; id: number; title: string };
  steps: WorkflowRunStep[];
}

export interface WorkflowPlannedAction {
  type: string;
  /** English fallback (core Plan.Summary); the UI renders type + params localized. */
  summary: string;
  external: boolean;
  /** Resolved parameters (templates filled). */
  params?: Record<string, string>;
}

export interface WorkflowDryRunSample {
  item: { kind: ItemKind; id: number; title: string; date: string; from?: string };
  matched: boolean;
  planned: WorkflowPlannedAction[];
}

export interface WorkflowDryRunResult {
  samples: WorkflowDryRunSample[];
  matched: number;
  checked: number;
}

/** A problem in workflow source text; positions are 1-based, end exclusive. */
export interface WorkflowDiagnostic {
  line: number;
  col: number;
  endLine: number;
  endCol: number;
  severity: 'error' | 'warning' | 'info';
  /** Stable code, e.g. "unknown_action", "missing_param", "syntax" (the UI localizes by code). */
  code: string;
  /** The offending name/value, used in localized messages. */
  detail?: string;
  /** English fallback text. */
  message: string;
}

export interface WorkflowValidation {
  diagnostics: WorkflowDiagnostic[];
  /** Parsed workflow when the source has no errors. */
  workflow?: Workflow;
}

export type WorkflowCompletionKind = 'keyword' | 'trigger' | 'scope' | 'field' | 'operator' | 'action' | 'param' | 'value' | 'variable';

export interface WorkflowCompletionItem {
  label: string;
  insert: string;
  kind: WorkflowCompletionKind;
  detail?: string;
}

export interface WorkflowCompletion {
  /** Replace src[from, to) (0-based UTF-16 offsets) with the chosen item's insert text. */
  from: number;
  to: number;
  items: WorkflowCompletionItem[];
}

export type WorkflowParamKind = 'string' | 'text' | 'list' | 'folder' | 'tag' | 'project' | 'channel' | 'number' | 'bool' | 'duration' | 'url' | 'path' | 'template';

export interface WorkflowParamSpec {
  name: string;
  kind: WorkflowParamKind;
  required?: boolean;
  /** Positional argument in the one-line rule syntax (e.g. `move "Archiv"`), in declaration order. */
  positional?: boolean;
  options?: string[];
}

export interface WorkflowActionSpec {
  type: string;
  /** Group for the action picker. */
  group: 'mail' | 'organize' | 'notify' | 'calendar' | 'files' | 'advanced';
  external: boolean;
  /** Allowed trigger events; empty = all. */
  triggers: string[];
  params: WorkflowParamSpec[];
}

export interface WorkflowTriggerSpec {
  event: string;
  group: 'mail' | 'chat' | 'calendar' | 'contact' | 'file' | 'time' | 'other';
  /** Trigger filters that apply. */
  filters: ('accounts' | 'folders' | 'channels' | 'schedule' | 'name')[];
}

export type WorkflowFieldKind = 'string' | 'list' | 'number' | 'bool';

export interface WorkflowFieldSpec {
  name: string;
  kind: WorkflowFieldKind;
  /** Short alias in the rule syntax, e.g. "from" for mail.from. */
  alias?: string;
}

/** Everything the editors need (Api.describeWorkflows). */
export interface WorkflowSchema {
  triggers: WorkflowTriggerSpec[];
  fields: WorkflowFieldSpec[];
  /** Operators per field kind. */
  operators: Record<WorkflowFieldKind, string[]>;
  actions: WorkflowActionSpec[];
  /** Template variables usable in texts: {{betreff}} ... */
  variables: string[];
}

export interface WorkflowTemplate {
  id: string;
  name: string;
  description: string;
  workflow: Workflow;
}

// --- Messenger ---
// Messenger plugins (KONZEPT 4.4, 11, 18.1): opt-in per plugin with risk
// notice, plugin process status, pairing (QR / code / password) and the
// plugin state of messenger accounts. Mirrors internal/app/messenger_api.go.

export type MessengerRisk = 'low' | 'medium' | 'high';
export type MessengerPluginState = 'missing' | 'stopped' | 'starting' | 'running' | 'crashed' | 'failed';
export type MessengerPairingMethod = 'qr' | 'phone' | 'password' | 'token' | 'bridge';
export type MessengerPairingState = 'waiting' | 'qr' | 'code' | 'input' | 'success' | 'error' | 'cancelled';

export interface MessengerPluginStatus {
  state: MessengerPluginState;
  version?: string;
  pid?: number;
  restarts: number;
  lastError?: string;
  startedAt?: string;
  lastCrash?: string;
  nextRetry?: string;
  /** Set when the plugin (or the network) reports that it is outdated. */
  updateHint?: string;
  capabilities: string[];
  path?: string;
}

export interface MessengerPlugin {
  name: string;
  displayName: string;
  channel: string;
  risk: MessengerRisk;
  /** Uses an API the network allows for third-party clients (Telegram, Matrix). */
  official: boolean;
  experimental: boolean;
  /** Enabling requires acknowledging the risk notice (WhatsApp). */
  needsRiskAck: boolean;
  pairingMethods: MessengerPairingMethod[];
  settingKeys: string[];
  installed: boolean;
  enabled: boolean;
  riskAcceptedAt?: string;
  settings: Record<string, string>;
  accounts: number;
  status: MessengerPluginStatus;
  /** External program the plugin needs (Signal: signal-cli); absent for others. */
  component?: MessengerComponent;
}

export type MessengerComponentState = 'ready' | 'missing' | 'installing' | 'failed' | 'unsupported';

/** External program of a plugin (internal/messenger/components.go). */
export interface MessengerComponent {
  name: string;
  state: MessengerComponentState;
  /** Where it was found: setting | bundled | installed | path. */
  source?: string;
  version?: string;
  path?: string;
  /** A pinned, checksum-verified download exists for this platform. */
  installable: boolean;
  installVersion?: string;
  error?: string;
  note?: string;
  progress?: { stage: 'download' | 'verify' | 'unpack' | 'done'; file?: string; done: number; total: number; step: number; steps: number };
}

export interface MessengerPluginPatch {
  enabled?: boolean;
  acceptRisk?: boolean;
  /** Empty values reset a setting. */
  settings?: Record<string, string>;
}

export interface MessengerPairingInput {
  plugin: string;
  method: MessengerPairingMethod;
  displayName?: string;
  phone?: string;
  homeserver?: string;
  username?: string;
  password?: string;
  token?: string;
  /** Bridge mode: network to link through the bridge of a Matrix account. */
  network?: string;
  bridgeAccountId?: number;
}

export interface MessengerPairing {
  id: string;
  plugin: string;
  method: MessengerPairingMethod;
  network?: string;
  state: MessengerPairingState;
  /** Data to render as QR code (state 'qr'). */
  qr?: string;
  /** Pairing code to type on the phone (state 'code'). */
  code?: string;
  /** Requested input (state 'input'): code, password, phone, homeserver ... */
  field?: string;
  message?: string;
  /** Error class (state 'error'): component_missing | unavailable | outdated. */
  reason?: string;
  expiresAt?: string;
  accountId?: number;
}

export interface MessengerAccountState {
  accountId: number;
  plugin: string;
  mode: 'direct' | 'bridge';
  state: string;
  lastError?: string;
  syncedAt?: string;
}

export type MessengerApiEvent =
  | { type: 'messenger.plugin'; name: string }
  | { type: 'messenger.pairing'; pairing: MessengerPairing }
  /** Ephemeral: somebody types in a conversation. */
  | { type: 'messenger.typing'; conversationId: number; accountId: number; personId?: number; name: string; active: boolean };
// --- Folder config ---
// Settings → account → "Ordner" (internal/app/folderconfig.go).

/** Roles the user can assign (the inbox is fixed). */
export type AssignableRole = 'sent' | 'drafts' | 'trash' | 'junk' | 'archive' | 'templates';
export type TrashMode = 'move' | 'mark' | 'delete';
export type ArchiveMode = 'single' | 'yearly' | 'monthly';

export interface FolderRoleEntry {
  role: AssignableRole;
  /** Folder that has the role now (absent = none). */
  folderId?: number;
  source?: RoleSource;
  /** What automatic detection chose. */
  serverFolderId?: number;
  serverSource?: RoleSource;
  overridden: boolean;
  /** The overridden folder vanished on the server (kept until it returns). */
  unresolved?: boolean;
  overrideRemoteId?: string;
}

export interface FolderBehaviour {
  saveSent: boolean;
  /** Follow the provider default (providerSavesSent). */
  saveSentAuto: boolean;
  /** EAS / Graph / Gmail file sent mail themselves. */
  providerSavesSent: boolean;
  trashMode: TrashMode;
  archiveMode: ArchiveMode;
}

export interface IdentityFolderConfig {
  identityId: number;
  name: string;
  email: string;
  sentFolderId?: number;
  draftsFolderId?: number;
}

export interface FolderConfig {
  accountId: number;
  roles: FolderRoleEntry[];
  behaviour: FolderBehaviour;
  identities: IdentityFolderConfig[];
  /** Missing role folders can be created on the server. */
  canCreate: boolean;
}

// --- PIM ---
// Calendars, tasks and contacts (KONZEPT 9, 10). Times are RFC 3339 (UTC);
// all-day values are dates "YYYY-MM-DD" with an exclusive end.

/** Live events of the PIM area. calendar.reminder: itemId = event id, state = trigger time. */
export type PimEvent =
  | { type: 'calendar.changed' }
  | { type: 'contacts.changed' }
  | { type: 'calendar.reminder'; itemId: string; state: string }
  | { type: 'calendar.invitation'; messageId: number };

export type CalendarKind = 'local' | 'caldav' | 'subscription' | 'birthdays';

export interface PimCalendar {
  id: number;
  accountId?: number;
  accountName?: string;
  name: string;
  color: string;
  kind: CalendarKind;
  readOnly: boolean;
  visible: boolean;
  isDefault: boolean;
  /** Supported components: 'VEVENT', 'VTODO'. */
  components: string[];
  url?: string;
  refreshMinutes?: number;
  description?: string;
  lastSync?: string;
  error?: string;
}

export interface CalendarInput {
  id?: number;
  name: string;
  color?: string;
  kind?: 'local' | 'subscription';
  url?: string;
  refreshMinutes?: number;
  visible?: boolean;
  isDefault?: boolean;
  components?: string[];
}

export type RecurrenceFreq = 'DAILY' | 'WEEKLY' | 'MONTHLY' | 'YEARLY';

export interface RecurrenceRule {
  freq: RecurrenceFreq | string;
  interval?: number;
  count?: number;
  /** Inclusive end date YYYY-MM-DD. */
  until?: string;
  /** "MO", "1MO", "-1FR" */
  byDay?: string[];
  byMonthDay?: number[];
  byMonth?: number[];
  bySetPos?: number[];
  /** Raw RRULE (read-only). */
  raw?: string;
}

export type PartStat = 'NEEDS-ACTION' | 'ACCEPTED' | 'TENTATIVE' | 'DECLINED' | 'DELEGATED';

export interface Attendee {
  email: string;
  name?: string;
  role?: string;
  partStat?: PartStat | string;
  rsvp?: boolean;
  cuType?: string;
}

export interface EventAlarm {
  minutesBefore: number;
  action?: string;
}

export type RecurrenceScope = 'this' | 'following' | 'all';

export interface CalendarEvent {
  /** "<objectId>", "<objectId>/<instance>", "bday/<contact>/<year>", "inv/<invitation>". */
  id: string;
  objectId: number;
  calendarId: number;
  uid: string;
  recurrenceId?: string;
  kind: 'event' | 'birthday' | 'invitation';
  title: string;
  description?: string;
  location?: string;
  url?: string;
  meetingUrl?: string;
  start: string;
  end: string;
  allDay: boolean;
  timeZone?: string;
  recurrence?: RecurrenceRule;
  recurring: boolean;
  exception?: boolean;
  organizer?: Attendee;
  attendees: Attendee[];
  alarms: EventAlarm[];
  isOrganizer?: boolean;
  myPartStat?: string;
  status?: string;
  busy: boolean;
  categories: string[];
  color?: string;
  readOnly?: boolean;
  conflict?: boolean;
  invitationId?: number;
  birthYear?: number;
}

export interface EventInput {
  calendarId: number;
  title: string;
  description?: string;
  location?: string;
  url?: string;
  meetingUrl?: string;
  start: string;
  end: string;
  allDay: boolean;
  timeZone?: string;
  /** null/absent keeps the rule on updates; { freq: '' } removes it. */
  recurrence?: RecurrenceRule | null;
  attendees?: Attendee[];
  alarms?: EventAlarm[];
  busy?: boolean;
  categories?: string[];
  status?: string;
  notifyAttendees?: boolean;
}

export interface EventQuery {
  from: string;
  to: string;
  calendarIds?: number[];
  includeHidden?: boolean;
}

export interface PimTask {
  id: number;
  calendarId: number;
  uid: string;
  title: string;
  description?: string;
  start?: string;
  due?: string;
  dueAllDay?: boolean;
  /** 0 = none, 1 = highest ... 9 = lowest. */
  priority: number;
  progress: number;
  status: string;
  done: boolean;
  completed?: string;
  parentId?: number;
  parentUid?: string;
  categories: string[];
  recurrence?: RecurrenceRule;
  overdue?: boolean;
  readOnly?: boolean;
  color?: string;
}

export interface TaskInput {
  id?: number;
  calendarId?: number;
  title: string;
  description?: string;
  due?: string;
  dueAllDay?: boolean;
  priority?: number;
  progress?: number;
  status?: string;
  done?: boolean;
  parentId?: number;
  categories?: string[];
  recurrence?: RecurrenceRule | null;
}

export type TaskFilter = 'open' | 'today' | 'overdue' | 'upcoming' | 'completed' | 'all';

export interface TaskQuery {
  filter?: TaskFilter;
  calendarIds?: number[];
  search?: string;
}

export interface Invitation {
  id: number;
  messageId?: number;
  method: 'REQUEST' | 'REPLY' | 'CANCEL' | 'COUNTER' | 'DECLINECOUNTER' | 'PUBLISH' | string;
  status: 'pending' | 'accepted' | 'tentative' | 'declined' | 'applied' | 'cancelled' | 'obsolete' | string;
  uid: string;
  title: string;
  description?: string;
  location?: string;
  meetingUrl?: string;
  start: string;
  end: string;
  allDay: boolean;
  recurring: boolean;
  organizer?: Attendee;
  attendees: Attendee[];
  myPartStat?: string;
  sender?: string;
  comment?: string;
  eventId?: string;
  conflicts: string[];
  canRespond: boolean;
  replyPartStat?: string;
}

export interface InvitationResponse {
  partStat: 'ACCEPTED' | 'TENTATIVE' | 'DECLINED';
  comment?: string;
  calendarId?: number;
  sendReply?: boolean;
  proposeStart?: string;
  proposeEnd?: string;
}

export interface FreeBusy {
  email: string;
  source: 'local' | 'caldav' | 'unknown';
  busy: TimeSlot[];
}

export interface DavAccountInput {
  displayName: string;
  url: string;
  carddavUrl?: string;
  username: string;
  password: string;
  email?: string;
}

export interface PimAddressBook {
  id: number;
  accountId?: number;
  accountName?: string;
  name: string;
  kind: 'local' | 'carddav';
  isDefault: boolean;
  count: number;
  url?: string;
  lastSync?: string;
  error?: string;
}

export interface AddressBookInput {
  id?: number;
  name: string;
  isDefault?: boolean;
}

export interface ContactValue {
  type?: string;
  label?: string;
  value: string;
  pref?: boolean;
}

export interface ContactAddress {
  type?: string;
  label?: string;
  poBox?: string;
  extended?: string;
  street?: string;
  locality?: string;
  region?: string;
  postalCode?: string;
  country?: string;
}

export interface ContactName {
  family?: string;
  given?: string;
  additional?: string;
  prefix?: string;
  suffix?: string;
}

export interface PimContact {
  id: number;
  addressBookId: number;
  personId?: number;
  displayName: string;
  hasPhoto: boolean;
  photoUrl?: string;
  /** Input only: data: URL sets the photo, "-" removes it. */
  photo?: string;
  /** Channels of the linked person (e-mail, phone, messengers). */
  channels: PersonHandle[];
  uid: string;
  version?: string;
  kind?: string;
  fn: string;
  n: ContactName;
  nickname?: string;
  org?: string;
  department?: string;
  title?: string;
  emails: ContactValue[];
  phones: ContactValue[];
  addresses: ContactAddress[];
  urls: ContactValue[];
  impp: ContactValue[];
  social: ContactValue[];
  /** YYYY-MM-DD or --MM-DD */
  birthday?: string;
  anniversary?: string;
  note?: string;
  categories: string[];
  custom: { name: string; value: string }[];
}

export interface ContactQuery {
  addressBookId?: number;
  search?: string;
  group?: string;
}

export interface ContactGroup {
  name: string;
  count: number;
}

export interface DuplicateGroup {
  contacts: PimContact[];
  preview: PimContact;
}

export interface AddressSuggestion {
  name: string;
  email: string;
  contactId?: number;
  personId?: number;
  source: 'contact' | 'recent';
}

// --- Import/Export/Backup ---
// KONZEPT 13 (import & export) and 14 (backup & restore); Go: internal/app/io_*.go.

export type ImportFormat = 'eml' | 'mbox' | 'maildir' | 'pst' | 'thunderbird' | 'ics' | 'vcf' | 'csv' | 'ldif';
export type ExportFormat = 'eml' | 'mbox' | 'maildir' | 'ics' | 'vcf' | 'csv' | 'ldif' | 'pdf';

export interface IOFormats {
  import: ImportFormat[];
  export: ExportFormat[];
  pst: boolean;
}

/** An import file: a local path (desktop) or an uploaded file (uploadImportFile). */
export interface IOSource {
  format: ImportFormat;
  path?: string;
  uploadToken?: string;
}

export interface IOUpload {
  token: string;
  name: string;
  size: number;
}

export interface ImportTarget {
  /** local: "Local Folders" account; folder: an existing server folder (IMAP APPEND). */
  kind: 'local' | 'folder';
  accountId?: number;
  folderId?: number;
  folderName?: string;
  calendarId?: number;
  addressbookId?: number;
}

export interface ImportOptions {
  keepStructure: boolean;
  dedupe: boolean;
  keepFlags: boolean;
  keepDates: boolean;
  throttleMs?: number;
  mboxVariant?: 'mboxrd' | 'mboxo';
  duplicateMode?: 'skip' | 'overwrite' | 'copy';
  csvMapping?: string[];
  csvPreset?: string;
  tbAccounts: boolean;
  tbMail: boolean;
  tbAddressBooks: boolean;
}

export interface ImportRequest {
  source: IOSource;
  target: ImportTarget;
  options: ImportOptions;
  resumeJobId?: string;
}

export interface ThunderbirdProfile {
  dir: string;
  accounts: {
    key: string;
    name: string;
    incoming: { kind: string; host?: string; port?: number; username?: string };
    identities: { name: string; email: string }[] | null;
    mailDir?: string;
  }[];
  addressBooks: { name: string; file: string; count: number }[] | null;
  tbsync?: string[];
  warnings?: string[];
}

export interface ImportPreview {
  format: ImportFormat;
  items: number;
  header?: string[];
  preset?: string;
  mapping?: string[];
  sample?: string[][];
  fields?: string[];
  problems?: string[];
  profile?: ThunderbirdProfile;
}

export interface MailSelection {
  folderIds?: number[];
  query?: string;
  conversationId?: number;
  projectId?: number;
  messageIds?: number[];
}

export interface ExportRequest {
  format: ExportFormat;
  selection?: MailSelection;
  calendarIds?: number[];
  from?: string;
  to?: string;
  addressbookIds?: number[];
  vcardVersion?: '3.0' | '4.0';
  csvPreset?: 'outlook' | 'google';
}

export interface IOProblem {
  item: string;
  error: string;
}

export interface IOJob {
  id: string;
  kind: 'import' | 'export' | 'backup' | 'restore';
  format: string;
  status: 'running' | 'done' | 'failed' | 'canceled';
  done: number;
  total: number;
  skipped: number;
  failed: number;
  current?: string;
  started: string;
  finished?: string;
  error?: string;
  problems?: IOProblem[];
  summary?: Record<string, number>;
  /** Export result: GET /api/io/downloads/{download}. */
  download?: string;
  downloadName?: string;
  result?: BackupManifest | RestoreResult;
  resumable?: boolean;
}

export type BackupMode = 'config' | 'local' | 'full' | 'custom';
export type BackupArea = 'mail' | 'calendar' | 'contacts' | 'chats' | 'settings';

export interface BackupScope {
  mode: BackupMode;
  accounts?: number[];
  areas?: BackupArea[];
  secrets?: boolean;
}

export interface BackupTarget {
  kind: 'download' | 'local' | 'webdav';
  path?: string;
  url?: string;
  username?: string;
  password?: string;
}

export interface BackupRequest {
  scope: BackupScope;
  passphrase?: string;
  target: BackupTarget;
}

export interface BackupAccountInfo {
  id: number;
  kind: string;
  name: string;
  address?: string;
  messenger?: boolean;
  messages: number;
}

export interface BackupManifest {
  format: number;
  id: string;
  created: string;
  appVersion?: string;
  schemaVersion: number;
  scope: BackupScope;
  encrypted: boolean;
  secrets: boolean;
  baseId?: string;
  files: { path: string; size: number; sha256: string }[];
  blobs: string[] | null;
  blobsIncluded: number;
  accounts: BackupAccountInfo[] | null;
  counts?: Record<string, number>;
  warnings?: string[];
  server?: boolean;
}

export interface BackupSource {
  uploadToken?: string;
  path?: string;
  scheduleId?: string;
  name?: string;
}

export interface BackupInspection {
  manifest: BackupManifest | null;
  encrypted: boolean;
  messenger: boolean;
}

export interface RestoreRequest {
  source: BackupSource;
  bases?: BackupSource[];
  passphrase?: string;
  mode: 'replace' | 'merge';
  accounts?: number[];
  configOnly?: boolean;
  secrets?: boolean;
}

export interface RestoreResult {
  mode: 'replace' | 'merge';
  accounts: number;
  messages: number;
  blobs: number;
  secrets: number;
  schemaFrom: number;
  schemaTo: number;
  warnings?: string[];
  restartRequired?: boolean;
}

export interface BackupSchedule {
  id: string;
  name: string;
  enabled: boolean;
  frequency: 'daily' | 'weekly';
  hour: number;
  minute: number;
  /** 0 = Sunday. */
  weekday: number;
  scope: BackupScope;
  target: { kind: 'local' | 'webdav'; path?: string; url?: string; username?: string; passwordRef?: string };
  passphraseRef?: string;
  incremental: boolean;
  fullEvery?: number;
  keepDaily: number;
  keepWeekly: number;
  lastRun?: string;
  lastResult?: string;
  lastError?: string;
  nextRun?: string;
}

export interface BackupScheduleInput extends BackupSchedule {
  passphrase?: string;
  targetPassword?: string;
}

export interface BackupArchive {
  name: string;
  size: number;
  modified: string;
}

// --- Cloud ---
// Cloud files (KONZEPT 12): Nextcloud, OpenCloud/oCIS, Vaultwarden Send, WebDAV.
// Mirrors internal/app/cloud_types.go.

export type CloudAccountProvider = CloudProvider | 'webdav';

export interface CloudCaps {
  /** Browse, upload, download. */
  files: boolean;
  /** Public links of stored files. */
  share: boolean;
  /** Encrypted Sends of local files or text (Vaultwarden). */
  send: boolean;
}

export interface CloudAccount {
  id: number;
  provider: CloudAccountProvider;
  displayName: string;
  server: string;
  user?: string;
  capabilities: CloudCaps;
  /** CalDAV/CardDAV of the same Nextcloud account (offered to calendar/contacts). */
  dav?: { caldav: string; carddav: string };
  /** Base directory of attachments sent as links. */
  uploadDir?: string;
}

export interface CloudLoginInput {
  provider: CloudAccountProvider;
  server: string;
  displayName?: string;
  /** OpenCloud in server mode: `<origin>/api/cloud/oauth/callback`. */
  redirectUrl?: string;
  clientId?: string;
  /** Vaultwarden: e-mail + master password (+ TOTP); WebDAV: username + password. */
  email?: string;
  username?: string;
  password?: string;
  totp?: string;
}

export type CloudLoginStatus = 'pending' | 'done' | 'error' | 'two_factor';

export interface CloudLoginFlow {
  id: string;
  provider: CloudAccountProvider;
  status: CloudLoginStatus;
  /** Open in the browser (Nextcloud Login Flow v2, OpenID Connect). */
  loginUrl?: string;
  error?: string;
  account?: CloudAccount;
}

export interface CloudEntry {
  name: string;
  path: string;
  dir: boolean;
  size: number;
  modified?: string;
  contentType?: string;
}

export interface CloudListing {
  accountId: number;
  path: string;
  entries: CloudEntry[];
}

/** -1 = unknown / unlimited. */
export interface CloudQuota {
  used: number;
  available: number;
}

export interface CloudShareInput {
  accountId: number;
  /** Stored file (Nextcloud, OpenCloud). */
  path?: string;
  /** Vaultwarden Send: a file (Api.uploadAttachment) or a text. */
  uploadToken?: string;
  text?: string;
  name?: string;
  password?: string;
  /** Generate a password following the server's policy. */
  generatePassword?: boolean;
  expiresAt?: string;
  note?: string;
  maxAccessCount?: number;
  hideEmail?: boolean;
}

export type CloudShareSource = 'mail' | 'files' | 'workflow' | 'chat';

export interface CloudShare {
  id: number;
  accountId: number;
  provider: CloudAccountProvider;
  fileName: string;
  remotePath?: string;
  url: string;
  /** Only returned right after creation. */
  password?: string;
  hasPassword: boolean;
  expiresAt?: string;
  createdAt: string;
  revokedAt?: string;
  active: boolean;
  source: CloudShareSource;
  size?: number;
  messageId?: number;
}

/** Save a mail/chat attachment (messageId + index) or an upload into a cloud folder. */
export interface CloudSaveInput {
  accountId: number;
  dir: string;
  messageId?: number;
  index?: number;
  uploadToken?: string;
  filename?: string;
}

/** A cloud file copied into an upload, ready to attach. */
export interface CloudImportResult {
  uploadToken: string;
  filename: string;
  mimeType: string;
  size: number;
}

// ---------------------------------------------------------------------------
// Sync status & diagnostics (Settings → Info & Diagnose)
// ---------------------------------------------------------------------------

export type SyncState = 'connecting' | 'syncing' | 'idle' | 'error' | 'stopped' | 'offline';

/** Sync status of one mail account (internal/app AccountStatus). */
export interface AccountSyncStatus {
  accountId: number;
  state: SyncState;
  error?: string;
  errorAt?: string;
  nextRetry?: string;
  lastSync?: string;
  /** Remote name and local id of the folder being synced. */
  folder?: string;
  folderId?: number;
  /** Progress of that folder: messages stored locally / on the server. */
  done?: number;
  total?: number;
  /** First sync of the folder. */
  initial?: boolean;
}

export interface DiagnosticsAccount {
  id: number;
  name: string;
  kind: string;
  incoming?: string;
  outgoing?: string;
  folders: number;
  messages: number;
  status?: AccountSyncStatus;
}

export interface DiagnosticsInfo {
  version: string;
  mode: 'desktop' | 'server';
  portable: boolean;
  dataDir?: string;
  logFile?: string;
  os: string;
  goVersion: string;
  accounts: DiagnosticsAccount[];
}

// --- OAuth/Cloud accounts ---
// Microsoft 365 / Google sign-in, own client IDs, shared mailboxes and
// identity delegation (internal/app/oauth_app.go, KONZEPT 7.3–7.5).

export type OAuthProviderId = 'microsoft' | 'google';

/** Declaration merging: discovery names the sign-in service. */
export interface DiscoveryResult {
  oauthProvider?: OAuthProviderId;
}

export interface OAuthClientInput {
  provider: OAuthProviderId;
  clientId: string;
  /** Write only (Google desktop clients, confidential web clients). */
  clientSecret?: string;
  hasSecret?: boolean;
  /** Microsoft authority: common | organizations | consumers | tenant id. */
  tenant?: string;
}

export interface OAuthProviderInfo {
  id: OAuthProviderId;
  displayName: string;
  configured: boolean;
  /** Where the active client comes from. */
  source?: 'env' | 'admin' | 'user' | 'builtin' | '';
  clientId?: string;
  tenant?: string;
  userClient?: OAuthClientInput;
  allowUserClients: boolean;
  deviceFlow: boolean;
  /** Access methods: graph | imap | eas | ews (Microsoft), gmail | imap (Google). */
  kinds: AccountKind[];
  /** Web redirect URI to register (server mode). */
  redirectUri?: string;
}

export interface OAuthStartInput {
  provider: OAuthProviderId;
  email: string;
  kind: AccountKind;
  displayName?: string;
  name?: string;
  tenant?: string;
  /** loopback (desktop default) | redirect (server default) | device. */
  mode?: 'loopback' | 'redirect' | 'device';
  /** Re-authenticate this account instead of creating one. */
  accountId?: number;
}

export interface OAuthFlowStatus {
  id: string;
  provider: OAuthProviderId;
  mode: 'loopback' | 'redirect' | 'device';
  status: 'pending' | 'done' | 'error' | 'canceled';
  authUrl?: string;
  userCode?: string;
  verificationUri?: string;
  expiresAt?: string;
  accountId?: number;
  email?: string;
  error?: string;
  /** oauth_not_configured | oauth_consent_required | oauth_denied | oauth_reauth_required ... */
  errorCode?: string;
}

export type SendMode = 'send_as' | 'send_on_behalf';

export interface SharedMailboxInput {
  accountId: number;
  address: string;
  sendMode?: SendMode | '';
  displayName?: string;
}

export interface IdentityDelegation {
  identityId: number;
  mode: SendMode;
  mailbox: string;
  ownerAccountId?: number;
}

export interface CloudPimResult {
  calendars: number;
  events: number;
  addressBooks: number;
  contacts: number;
  tasks: number;
  deleted: number;
}

// ---------------------------------------------------------------------------
// Mail protocols: POP3, JMAP, Sieve, remote image proxy (KONZEPT 7.1, 8.3, 8.5)
// ---------------------------------------------------------------------------

export interface Pop3Options {
  leaveOnServer: boolean;
  /** Delete on the server this many days after download (0 = never). */
  deleteAfterDays?: number;
  /** Delete on the server when the mail is deleted permanently in TuxMail. */
  deleteOnLocalDelete?: boolean;
}

export interface RemoteContent {
  /** Sanitized HTML with remote images routed through the image proxy. */
  html: string;
}

export interface RemoteAllowEntry {
  kind: 'sender' | 'domain';
  value: string;
  createdAt: string;
}

export interface SieveScriptInfo {
  name: string;
  active: boolean;
}

export interface SieveSkipped {
  id: string;
  name: string;
  reason: string;
}

export interface SieveStatus {
  accountId: number;
  /** ManageSieve reachable and logged in. */
  available: boolean;
  error?: string;
  extensions: string[];
  scripts: SieveScriptInfo[];
  /** "Workflows als Serverregeln übertragen". */
  syncWorkflows: boolean;
  /** Workflow ids that run on the server / that cannot. */
  included: string[];
  skipped: SieveSkipped[];
}

export interface SieveCommand {
  name: string;
  line: number;
  args?: string[];
  children?: SieveCommand[];
  comment?: string;
}

export interface SieveParse {
  commands: SieveCommand[];
  requires: string[];
  managed: boolean;
  error?: string;
  errorLine?: number;
}

export interface SieveScript {
  name: string;
  script: string;
  active: boolean;
  parse: SieveParse;
}

export interface SieveCheckResult {
  ok: boolean;
  warnings?: string;
  error?: string;
  line?: number;
}

/** ManageSieve settings of an IMAP account (advanced; defaults: IMAP host, 4190, STARTTLS). */
export interface SieveConfig {
  host: string;
  port: number;
  tls: TLSMode;
  disabled: boolean;
  /** Keep TuxMail's rules in an own script included from the active one. */
  separate: boolean;
}

/** Out-of-office settings ("Abwesenheitsnotiz", KONZEPT 8.5). Times RFC 3339; zero/empty = open. */
export interface VacationSettings {
  enabled: boolean;
  start?: string | null;
  end?: string | null;
  accountIds?: number[];
  identities?: string[];
  subject?: string;
  internalText?: string;
  externalText?: string;
  ownDomains?: string[];
  ownAddresses?: string[];
  oncePerDays?: number;
  forwardTo?: string[];
  chatAutoReply?: boolean;
  chatText?: string;
  chatChannels?: string[];
  /** Accounts whose mail server answers itself (read only). */
  serverAccounts?: number[];
}

// --- Account edit ---
// "Konto bearbeiten" (Settings -> Konten; Go: internal/app/account_edit.go).

export type BodyPolicy = 'recent' | 'all' | 'on_demand';
export type IdleMode = 'default' | 'custom' | 'off';

export interface AccountIndexOptions {
  content: boolean;
  attachments: boolean;
  encrypted: boolean;
}

export interface AccountSettings {
  account: Account;
  email: string;
  status?: AccountSyncStatus;
  hasPassword: boolean;
  /** "microsoft" | "google" (or "oauth2"): "Neu anmelden" instead of a password. */
  oauthProvider?: string;
  easUrl?: string;
  jmapUrl?: string;
  pop3?: Pop3Options;
  bodyPolicy: BodyPolicy;
  /** 0 = default poll interval. */
  pollMinutes: number;
  idleMode: IdleMode;
  idleFolders: number[];
  index: AccountIndexOptions;
  sieve?: SieveConfig;
  /** Connection test steps: imap | pop3 | jmap | eas | ews | graph | gmail | smtp | sieve | caldav | carddav. */
  tests: string[];
  editable: boolean;
}

/** Fields left out stay unchanged. */
export interface AccountUpdate {
  displayName?: string;
  color?: string;
  enabled?: boolean;
  email?: string;
  incoming?: ServerConfig;
  outgoing?: ServerConfig;
  easUrl?: string;
  jmapUrl?: string;
  pop3?: Pop3Options;
  bodyPolicy?: BodyPolicy;
  pollMinutes?: number;
  idleMode?: IdleMode;
  idleFolders?: number[];
  index?: AccountIndexOptions;
  sieve?: SieveConfig;
  /** Log in with the new settings first; error code "connection_failed" saves nothing. */
  verify?: boolean;
}

export interface ConnectionTestInput {
  kinds?: string[];
  /** Test unsaved settings of the dialog. */
  update?: AccountUpdate;
  /** Test a new password instead of the stored one. */
  password?: string;
}

export interface ConnectionTestStep {
  kind: string;
  target: string;
  ok: boolean;
  /** auth_failed | unreachable | tls | timeout | oauth_reauth_required | not_configured | error */
  code?: string;
  error?: string;
  millis: number;
}

export interface ConnectionTestResult {
  ok: boolean;
  steps: ConnectionTestStep[];
}

export interface AccountDiagnostics {
  generatedAt: string;
  version: string;
  mode: string;
  account: Account;
  status?: AccountSyncStatus;
  folders: { name: string; role?: string; total: number; unread: number }[];
  identities: number;
  pendingOps: number;
  failedOps: { kind: string; attempts: number; lastError: string; createdAt: string }[];
  index: AccountIndexOptions;
  settings: AccountSettings;
}
