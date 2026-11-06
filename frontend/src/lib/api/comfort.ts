// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mail comfort features (KONZEPT 8.6–8.11, 9.1): spam filter, newsletter
// subscriptions, smart categories, e-invoices, print/PDF, spell checking,
// mail merge, video conference links and S/MIME. A separate API module so
// that it does not collide with the main Api interface; the transport is
// derived from the main one (HTTP, Wails bindings `Comfort*`, or the mock).
//
// REST routes (internal/server/api/comfort_routes.go):
//   GET /api/comfort/settings                       -> ComfortSettings
//   PUT /api/comfort/settings                       ComfortSettings -> ComfortSettings
//   GET /api/comfort/messages/{id}/insights         -> MessageInsights
//   POST /api/comfort/messages/{id}/unsubscribe     -> UnsubscribeResult
//   POST /api/comfort/messages/{id}/invoice-task    -> {id}
//   GET /api/comfort/messages/{id}/pdf              archive PDF (PDF/A-3, ?download=1)
//   GET /api/comfort/print                          ?messageId=|conversationId=&layout=mail|chat&lang= -> text/html
//   POST /api/comfort/pdf/export                    ExportPdfInput -> application/zip
//   POST /api/comfort/spam                          {ids, spam, move}
//   POST /api/comfort/category                      {ids, category}
//   GET /api/comfort/categories                     ?role=inbox -> CategoryInfo[]
//   GET /api/comfort/categories/{category}/messages ?role=&limit= -> MessageSummary[]
//   POST /api/comfort/message-categories            {ids} -> {[id]: category}
//   GET /api/comfort/subscriptions                  -> Subscription[]
//   POST /api/comfort/subscriptions/unsubscribe     {key} -> UnsubscribeResult
//   POST /api/comfort/subscriptions/archive         {key} -> {moved}
//   POST /api/comfort/subscriptions/auto-archive    {key} -> Workflow
//   POST /api/comfort/spell/check                   {text, language} -> SpellResult
//   GET /api/comfort/spell/words                    -> string[]
//   POST /api/comfort/spell/words                   {word}
//   DELETE /api/comfort/spell/words/{word}
//   POST /api/comfort/merge/table                   raw CSV/XLSX body -> MergeTable
//   GET /api/comfort/merge/group                    ?name= -> MergeTable
//   POST /api/comfort/merge/preview                 MergeJobInput -> MergeRendered[]
//   GET /api/comfort/merge/jobs                     -> MergeJob[]
//   POST /api/comfort/merge/jobs                    MergeJobInput -> MergeJob
//   GET /api/comfort/merge/jobs/{jobId}             -> MergeJob
//   POST /api/comfort/merge/jobs/{jobId}/retry      -> MergeJob
//   POST /api/comfort/merge/jobs/{jobId}/cancel
//   POST /api/comfort/meeting                       MeetingInput -> MeetingLink
//   POST /api/comfort/meeting/ics                   {ics, link, lang} -> {ics}
//   GET /api/comfort/smime/certificates             -> SmimeCertificate[]
//   POST /api/comfort/smime/certificates            {data (base64), password, filename} -> SmimeCertificate
//   PUT /api/comfort/smime/certificates/{id}/trust  {trusted}
//   DELETE /api/comfort/smime/certificates/{id}
//   GET /api/comfort/smime/status                   ?from=&recipients=a,b -> SmimeStatus
//   GET /api/comfort/pgp/keys                       -> PgpKey[]
//   POST /api/comfort/pgp/keys                      {data (base64), passphrase} -> PgpKey[]
//   POST /api/comfort/pgp/keys/generate             {name, email, passphrase} -> PgpKey
//   POST /api/comfort/pgp/keys/{id}/unlock          {passphrase} -> PgpKey
//   POST /api/comfort/pgp/keys/{id}/passphrase      {current, new} -> PgpKey (new empty: remove)
//   POST /api/comfort/pgp/lock
//   POST /api/comfort/pgp/keys/lookup               {email, source: wkd|keyserver} -> PgpKey (only on a click)
//   GET /api/comfort/pgp/keys/{id}/public           -> {armored}
//   POST /api/comfort/pgp/keys/{id}/secret          {passphrase, current} -> {armored}
//   PUT /api/comfort/pgp/keys/{id}/trust            {verified}
//   DELETE /api/comfort/pgp/keys/{id}
//   GET /api/comfort/pgp/peers                      -> PgpPeer[]
//   POST /api/comfort/pgp/peers/verify              {address, fingerprint} -> PgpKey
//   POST /api/comfort/pgp/peers/delete              {address}
//   GET /api/comfort/pgp/status                     ?from=&recipients=a,b -> PgpStatus

import type { Api } from './api';
import { ApiError } from './api';
import type { MessageSummary, Workflow } from '../types';
import { comfortMock } from './comfortMock';

// ---- types ---------------------------------------------------------------------

export type CategoryId = 'personal' | 'newsletter' | 'invoice' | 'notification' | 'social' | 'appointment';
export const CATEGORIES: CategoryId[] = ['personal', 'newsletter', 'invoice', 'notification', 'social', 'appointment'];

export interface ComfortSettings {
  spam: { enabled: boolean; unsure: number; spam: number; moveToJunk: boolean; serverLearn: boolean };
  categories: { enabled: boolean; tabs?: CategoryId[]; aiRefine: boolean };
  spell: { enabled: boolean; language: 'auto' | 'de' | 'en' | string };
  meeting: { defaultProvider: MeetingProvider; jitsiBaseUrl: string; nextcloudAccountId?: number };
  merge: { perMinute: number };
  smime: { signByDefault: boolean; encryptAutomatically: boolean; prefer: 'pgp' | 'smime' };
  pgp: { signByDefault: boolean; sendAutocrypt: boolean; preferEncrypt: boolean };
  pdf: { chromiumPath?: string; useChromium: boolean; nameTemplate: string };
  newsletter: { showBar: boolean };
}

export interface UnsubscribeInfo {
  http?: string;
  mailto?: string;
  oneClick: boolean;
  listId?: string;
  method: 'one_click' | 'mailto' | 'link' | '';
}

export interface UnsubscribeResult {
  method: UnsubscribeInfo['method'];
  done: boolean;
  openUrl?: string;
  status?: number;
  error?: string;
}

export interface Invoice {
  format: 'facturx' | 'zugferd1' | 'cii' | 'ubl';
  profile?: string;
  number?: string;
  type?: string;
  issued?: string;
  due?: string;
  amount?: string;
  currency?: string;
  seller?: string;
  buyer?: string;
  iban?: string;
  bic?: string;
  reference?: string;
  source?: string;
}

export interface SmimeInfo {
  present: boolean;
  signed: boolean;
  encrypted: boolean;
  decrypted: boolean;
  signature?: 'trusted' | 'untrusted' | 'mismatch' | 'expired' | 'invalid';
  signerEmail?: string;
  signerName?: string;
  issuer?: string;
  fingerprint?: string;
  error?: string;
}

export interface PgpMessageStatus {
  present: boolean;
  encrypted: boolean;
  decrypted: boolean;
  signed: boolean;
  signature?: 'valid' | 'invalid' | 'unknown_key' | 'expired_key' | 'revoked_key' | 'mismatch';
  signerFingerprint?: string;
  signerKeyId?: string;
  signerTrust?: 'unknown' | 'tofu' | 'verified';
  protectedSubject?: string;
  partiallyProtected?: boolean;
  lockedKeys?: { id: number; email: string; fingerprint: string }[];
}

export interface MessageInsights {
  messageId: number;
  spam: { score: number; level: 'ham' | 'unsure' | 'spam'; server?: boolean; trained?: boolean; reasons?: string[] };
  category?: CategoryId;
  categorySource?: string;
  unsubscribe?: UnsubscribeInfo;
  sender?: string;
  senderName?: string;
  unsubscribed?: boolean;
  invoice?: Invoice;
  smime?: SmimeInfo;
  pgp?: PgpMessageStatus;
}

export interface CategoryInfo {
  id: CategoryId;
  total: number;
  unread: number;
}

export interface Subscription {
  key: string;
  sender: string;
  name?: string;
  count: number;
  unread: number;
  inInbox: number;
  perMonth: number;
  firstMail: string;
  lastMail: string;
  unsubscribe: UnsubscribeInfo;
  unsubscribed?: string;
  autoArchive: boolean;
  workflowId?: string;
}

export interface Misspelling {
  word: string;
  start: number;
  end: number;
  byteStart: number;
  suggestions: string[];
}

export interface SpellResult {
  language: string;
  detected: boolean;
  misspellings: Misspelling[];
}

export interface MergeRecipient {
  email: string;
  name?: string;
  fields?: Record<string, string>;
}

export interface MergeTemplate {
  subject: string;
  text?: string;
  html?: string;
}

export interface MergeTable {
  recipients: MergeRecipient[];
  columns: string[];
}

export interface MergeRendered {
  recipient: MergeRecipient;
  subject: string;
  text?: string;
  html?: string;
  missing?: string[];
}

export interface MergeJobInput {
  identityId: number;
  template: MergeTemplate;
  recipients: MergeRecipient[];
  perMinute?: number;
  unsubscribe?: string;
  encrypt?: boolean;
  attachments?: string[];
  cloudAccount?: string;
}

export interface MergeLogEntry {
  index: number;
  email: string;
  status: 'pending' | 'sent' | 'failed' | 'skipped';
  error?: string;
  attempts: number;
  sentAt?: string;
}

export interface MergeJob {
  id: string;
  identityId: number;
  template: MergeTemplate;
  recipients: MergeRecipient[];
  perMinute: number;
  log: MergeLogEntry[];
  state: 'draft' | 'running' | 'done' | 'cancelled';
  created: string;
  sent: number;
  failed: number;
  pending: number;
}

export type MeetingProvider = 'jitsi' | 'nextcloud_talk' | 'teams';

export interface MeetingInput {
  provider?: MeetingProvider;
  title: string;
  participants?: string[];
  start?: string;
  end?: string;
  public?: boolean;
  password?: string;
  accountId?: number;
  lang?: string;
}

export interface MeetingLink {
  provider: MeetingProvider;
  url: string;
  label: string;
  password?: string;
  token?: string;
  description: string;
  conference: string;
}

export interface SmimeCertificate {
  id: number;
  email: string;
  subject: string;
  issuer: string;
  fingerprint: string;
  notBefore: string;
  notAfter: string;
  own: boolean;
  trusted: boolean;
  source: 'own' | 'imported' | 'collected';
  expired: boolean;
  canEncrypt: boolean;
}

export interface SmimeStatus {
  canSign: boolean;
  canEncrypt: boolean;
  missing?: string[];
}

export interface PgpKey {
  id: number;
  fingerprint: string;
  email: string;
  emails?: string[];
  userId: string;
  algorithm: string;
  created: string;
  expires?: string;
  status: string;
  own: boolean;
  canEncrypt: boolean;
  canSign: boolean;
  source: 'own' | 'manual' | 'wkd' | 'keyserver' | 'autocrypt';
  trust: 'unknown' | 'tofu' | 'verified' | 'revoked' | 'expired';
  protected: boolean;
  locked: boolean;
  unlockedUntil?: string;
}

export interface PgpPeer {
  address: string;
  fingerprint: string;
  gossip: boolean;
  preferEncrypt: boolean;
  lastSeen?: string;
  keyChanged: boolean;
  trust: 'unknown' | 'tofu' | 'verified' | 'revoked' | 'expired';
}

export interface PgpStatus {
  canSign: boolean;
  canEncrypt: boolean;
  missing?: string[];
  recipients?: { address: string; fingerprint?: string; source?: string; trust: string }[];
  recommendation?: string;
  ownLocked?: boolean;
  ownKeyId?: number;
}

export interface OutgoingCrypto {
  method: 'auto' | 'smime' | 'pgp' | 'none';
  sign?: boolean;
  encrypt?: boolean;
}

export interface ExportPdfInput {
  ids?: number[];
  folderId?: number;
  query?: string;
  limit?: number;
}

export interface ComfortApi {
  getSettings(): Promise<ComfortSettings>;
  saveSettings(s: ComfortSettings): Promise<ComfortSettings>;
  insights(messageId: number): Promise<MessageInsights>;
  markSpam(ids: number[], spam: boolean, move: boolean): Promise<void>;
  setCategory(ids: number[], category: CategoryId): Promise<void>;
  listCategories(role?: string): Promise<CategoryInfo[]>;
  categoryMessages(category: CategoryId, role?: string, limit?: number): Promise<MessageSummary[]>;
  messageCategories(ids: number[]): Promise<Record<number, CategoryId>>;
  listSubscriptions(): Promise<Subscription[]>;
  unsubscribe(key: string): Promise<UnsubscribeResult>;
  unsubscribeMessage(messageId: number): Promise<UnsubscribeResult>;
  archiveSubscription(key: string): Promise<number>;
  autoArchiveSubscription(key: string): Promise<Workflow>;
  createInvoiceTask(messageId: number): Promise<{ id: string }>;
  spellCheck(text: string, language?: string): Promise<SpellResult>;
  spellWords(): Promise<string[]>;
  addSpellWord(word: string): Promise<void>;
  deleteSpellWord(word: string): Promise<void>;
  /** URL of the print view (opened in a window or iframe). */
  printUrl(opts: { messageId?: number; conversationId?: number; layout?: 'mail' | 'chat'; lang?: string }): string;
  /** URL of the archive PDF of a mail. */
  pdfUrl(messageId: number, download?: boolean): string;
  /** Batch export; the browser downloads a ZIP, the desktop writes a folder (returned). */
  exportPdfs(input: ExportPdfInput): Promise<{ folder?: string; blob?: Blob }>;
  parseMergeTable(file: File): Promise<MergeTable>;
  mergeFromGroup(group: string): Promise<MergeTable>;
  previewMerge(input: MergeJobInput): Promise<MergeRendered[]>;
  startMerge(input: MergeJobInput): Promise<MergeJob>;
  listMergeJobs(): Promise<MergeJob[]>;
  getMergeJob(id: string): Promise<MergeJob>;
  retryMergeJob(id: string): Promise<MergeJob>;
  cancelMergeJob(id: string): Promise<void>;
  createMeeting(input: MeetingInput): Promise<MeetingLink>;
  listCertificates(): Promise<SmimeCertificate[]>;
  importCertificate(file: File, password?: string): Promise<SmimeCertificate>;
  trustCertificate(id: number, trusted: boolean): Promise<void>;
  deleteCertificate(id: number): Promise<void>;
  smimeStatus(from: string, recipients: string[]): Promise<SmimeStatus>;
  listPgpKeys(): Promise<PgpKey[]>;
  generatePgpKey(input: { name: string; email: string; passphrase?: string }): Promise<PgpKey>;
  unlockPgpKey(id: number, passphrase: string): Promise<PgpKey>;
  changePgpPassphrase(id: number, current: string, next: string): Promise<PgpKey>;
  lockPgpKeys(): Promise<void>;
  importPgpKeys(file: File, passphrase?: string): Promise<PgpKey[]>;
  exportPgpPublicKey(id: number): Promise<string>;
  exportPgpSecretKey(id: number, passphrase: string, current?: string): Promise<string>;
  verifyPgpKey(id: number, verified: boolean): Promise<void>;
  deletePgpKey(id: number): Promise<void>;
  lookupPgpKey(email: string, source: 'wkd' | 'keyserver'): Promise<PgpKey>;
  listPgpPeers(): Promise<PgpPeer[]>;
  verifyPgpPeer(address: string, fingerprint: string): Promise<PgpKey>;
  deletePgpPeer(address: string): Promise<void>;
  pgpStatus(from: string, recipients: string[]): Promise<PgpStatus>;
}

// ---- helpers ---------------------------------------------------------------------

export async function fileToBase64(file: Blob): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer());
  let s = '';
  for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode(...buf.subarray(i, i + 0x8000));
  return btoa(s);
}

function query(params: Record<string, string | number | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') sp.set(k, String(v));
  const s = sp.toString();
  return s ? `?${s}` : '';
}

function printUrl(base: string, o: { messageId?: number; conversationId?: number; layout?: string; lang?: string }): string {
  return `${base}/api/comfort/print${query({ messageId: o.messageId, conversationId: o.conversationId, layout: o.layout, lang: o.lang })}`;
}

function readCsrf(): string | undefined {
  if (typeof document === 'undefined') return undefined;
  const m = document.cookie.match(/(?:^|;\s*)(?:__Host-)?tuxmail_csrf=([^;]+)/);
  if (m) return decodeURIComponent(m[1]);
  return document.querySelector<HTMLMetaElement>('meta[name="tuxmail-csrf"]')?.content || undefined;
}

// ---- HTTP ------------------------------------------------------------------------

export class ComfortHttp implements ComfortApi {
  constructor(
    private readonly base = '',
    private readonly fetchImpl: typeof fetch = (...a) => fetch(...a),
    private readonly bearer: () => string | undefined = () => undefined,
  ) {}

  private async raw(method: string, path: string, body?: unknown): Promise<Response> {
    const h: Record<string, string> = { Accept: 'application/json' };
    const tok = this.bearer();
    if (tok) h.Authorization = `Bearer ${tok}`;
    else {
      const c = readCsrf();
      if (c) h['X-CSRF-Token'] = c;
    }
    let payload: BodyInit | undefined;
    if (body instanceof Blob) payload = body;
    else if (body !== undefined) {
      h['Content-Type'] = 'application/json';
      payload = JSON.stringify(body);
    }
    let res: Response;
    try {
      res = await this.fetchImpl(this.base + path, { method, headers: h, body: payload, credentials: tok ? 'omit' : 'same-origin' });
    } catch (err) {
      throw new ApiError(err instanceof Error ? err.message : 'network error', 0, 'network');
    }
    if (!res.ok) {
      let msg = res.statusText;
      let code = 'http_' + res.status;
      try {
        const j = (await res.json()) as { error?: string; code?: string; message?: string };
        code = j.code ?? j.error ?? code;
        msg = j.message ?? j.error ?? msg;
      } catch {
        /* not JSON */
      }
      throw new ApiError(msg, res.status, code);
    }
    return res;
  }

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.raw(method, path, body);
    if (res.status === 204) return undefined as T;
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  getSettings = () => this.req<ComfortSettings>('GET', '/api/comfort/settings');
  saveSettings = (s: ComfortSettings) => this.req<ComfortSettings>('PUT', '/api/comfort/settings', s);
  insights = (id: number) => this.req<MessageInsights>('GET', `/api/comfort/messages/${id}/insights`);
  markSpam = (ids: number[], spam: boolean, move: boolean) => this.req<void>('POST', '/api/comfort/spam', { ids, spam, move });
  setCategory = (ids: number[], category: CategoryId) => this.req<void>('POST', '/api/comfort/category', { ids, category });
  listCategories = (role?: string) => this.req<CategoryInfo[]>('GET', `/api/comfort/categories${query({ role })}`);
  categoryMessages = (c: CategoryId, role?: string, limit?: number) =>
    this.req<MessageSummary[]>('GET', `/api/comfort/categories/${c}/messages${query({ role, limit })}`);
  messageCategories = (ids: number[]) => this.req<Record<number, CategoryId>>('POST', '/api/comfort/message-categories', { ids });
  listSubscriptions = () => this.req<Subscription[]>('GET', '/api/comfort/subscriptions');
  unsubscribe = (key: string) => this.req<UnsubscribeResult>('POST', '/api/comfort/subscriptions/unsubscribe', { key });
  unsubscribeMessage = (id: number) => this.req<UnsubscribeResult>('POST', `/api/comfort/messages/${id}/unsubscribe`);
  archiveSubscription = async (key: string) => (await this.req<{ moved: number }>('POST', '/api/comfort/subscriptions/archive', { key })).moved;
  autoArchiveSubscription = (key: string) => this.req<Workflow>('POST', '/api/comfort/subscriptions/auto-archive', { key });
  createInvoiceTask = (id: number) => this.req<{ id: string }>('POST', `/api/comfort/messages/${id}/invoice-task`);
  spellCheck = (text: string, language?: string) => this.req<SpellResult>('POST', '/api/comfort/spell/check', { text, language });
  spellWords = () => this.req<string[]>('GET', '/api/comfort/spell/words');
  addSpellWord = (word: string) => this.req<void>('POST', '/api/comfort/spell/words', { word });
  deleteSpellWord = (word: string) => this.req<void>('DELETE', `/api/comfort/spell/words/${encodeURIComponent(word)}`);
  printUrl = (o: { messageId?: number; conversationId?: number; layout?: 'mail' | 'chat'; lang?: string }) => printUrl(this.base, o);
  pdfUrl = (id: number, download?: boolean) => `${this.base}/api/comfort/messages/${id}/pdf${download ? '?download=1' : ''}`;
  exportPdfs = async (input: ExportPdfInput) => ({ blob: await (await this.raw('POST', '/api/comfort/pdf/export', input)).blob() });
  parseMergeTable = (file: File) => this.req<MergeTable>('POST', '/api/comfort/merge/table', file);
  mergeFromGroup = (group: string) => this.req<MergeTable>('GET', `/api/comfort/merge/group${query({ name: group })}`);
  previewMerge = (input: MergeJobInput) => this.req<MergeRendered[]>('POST', '/api/comfort/merge/preview', input);
  startMerge = (input: MergeJobInput) => this.req<MergeJob>('POST', '/api/comfort/merge/jobs', input);
  listMergeJobs = () => this.req<MergeJob[]>('GET', '/api/comfort/merge/jobs');
  getMergeJob = (id: string) => this.req<MergeJob>('GET', `/api/comfort/merge/jobs/${encodeURIComponent(id)}`);
  retryMergeJob = (id: string) => this.req<MergeJob>('POST', `/api/comfort/merge/jobs/${encodeURIComponent(id)}/retry`);
  cancelMergeJob = (id: string) => this.req<void>('POST', `/api/comfort/merge/jobs/${encodeURIComponent(id)}/cancel`);
  createMeeting = (input: MeetingInput) => this.req<MeetingLink>('POST', '/api/comfort/meeting', input);
  listCertificates = () => this.req<SmimeCertificate[]>('GET', '/api/comfort/smime/certificates');
  importCertificate = async (file: File, password?: string) =>
    this.req<SmimeCertificate>('POST', '/api/comfort/smime/certificates', { data: await fileToBase64(file), password, filename: file.name });
  trustCertificate = (id: number, trusted: boolean) => this.req<void>('PUT', `/api/comfort/smime/certificates/${id}/trust`, { trusted });
  deleteCertificate = (id: number) => this.req<void>('DELETE', `/api/comfort/smime/certificates/${id}`);
  smimeStatus = (from: string, recipients: string[]) =>
    this.req<SmimeStatus>('GET', `/api/comfort/smime/status${query({ from, recipients: recipients.join(',') })}`);
  listPgpKeys = () => this.req<PgpKey[]>('GET', '/api/comfort/pgp/keys');
  generatePgpKey = (input: { name: string; email: string; passphrase?: string }) => this.req<PgpKey>('POST', '/api/comfort/pgp/keys/generate', input);
  unlockPgpKey = (id: number, passphrase: string) => this.req<PgpKey>('POST', `/api/comfort/pgp/keys/${id}/unlock`, { passphrase });
  changePgpPassphrase = (id: number, current: string, next: string) => this.req<PgpKey>('POST', `/api/comfort/pgp/keys/${id}/passphrase`, { current, new: next });
  lockPgpKeys = () => this.req<void>('POST', '/api/comfort/pgp/lock');
  importPgpKeys = async (file: File, passphrase?: string) => this.req<PgpKey[]>('POST', '/api/comfort/pgp/keys', { data: await fileToBase64(file), passphrase });
  exportPgpPublicKey = async (id: number) => (await this.req<{ armored: string }>('GET', `/api/comfort/pgp/keys/${id}/public`)).armored;
  exportPgpSecretKey = async (id: number, passphrase: string, current?: string) => (await this.req<{ armored: string }>('POST', `/api/comfort/pgp/keys/${id}/secret`, { passphrase, current })).armored;
  verifyPgpKey = (id: number, verified: boolean) => this.req<void>('PUT', `/api/comfort/pgp/keys/${id}/trust`, { verified });
  deletePgpKey = (id: number) => this.req<void>('DELETE', `/api/comfort/pgp/keys/${id}`);
  lookupPgpKey = (email: string, source: 'wkd' | 'keyserver') => this.req<PgpKey>('POST', '/api/comfort/pgp/keys/lookup', { email, source });
  listPgpPeers = () => this.req<PgpPeer[]>('GET', '/api/comfort/pgp/peers');
  verifyPgpPeer = (address: string, fingerprint: string) => this.req<PgpKey>('POST', '/api/comfort/pgp/peers/verify', { address, fingerprint });
  deletePgpPeer = (address: string) => this.req<void>('POST', '/api/comfort/pgp/peers/delete', { address });
  pgpStatus = (from: string, recipients: string[]) =>
    this.req<PgpStatus>('GET', `/api/comfort/pgp/status${query({ from, recipients: recipients.join(',') })}`);
}

// ---- Wails ------------------------------------------------------------------------

type BoundFn = (...args: unknown[]) => Promise<unknown>;
interface WailsLike {
  go?: Record<string, Record<string, Record<string, BoundFn>>>;
}

export class ComfortWails implements ComfortApi {
  constructor(private readonly win: WailsLike = globalThis as unknown as WailsLike) {}

  private async call<T>(method: string, ...args: unknown[]): Promise<T> {
    const fn = this.win.go?.app?.Api?.[method];
    if (typeof fn !== 'function') throw new ApiError(`Wails binding app.Api.${method} not available`, 0, 'not_bound');
    try {
      return (await fn(...args)) as T;
    } catch (err) {
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

  getSettings = () => this.call<ComfortSettings>('ComfortGetSettings');
  saveSettings = (s: ComfortSettings) => this.call<ComfortSettings>('ComfortSaveSettings', s);
  insights = (id: number) => this.call<MessageInsights>('ComfortInsights', id);
  markSpam = (ids: number[], spam: boolean, move: boolean) => this.call<void>('ComfortMarkSpam', { ids, spam, move });
  setCategory = (ids: number[], category: CategoryId) => this.call<void>('ComfortSetCategory', { ids, category });
  listCategories = (role?: string) => this.call<CategoryInfo[]>('ComfortListCategories', role ?? '');
  categoryMessages = (c: CategoryId, role?: string, limit?: number) => this.call<MessageSummary[]>('ComfortCategoryMessages', c, role ?? '', limit ?? 0);
  messageCategories = (ids: number[]) => this.call<Record<number, CategoryId>>('ComfortMessageCategories', ids);
  listSubscriptions = () => this.call<Subscription[]>('ComfortListSubscriptions');
  unsubscribe = (key: string) => this.call<UnsubscribeResult>('ComfortUnsubscribe', key);
  unsubscribeMessage = (id: number) => this.call<UnsubscribeResult>('ComfortUnsubscribeMessage', id);
  archiveSubscription = (key: string) => this.call<number>('ComfortArchiveSubscription', key);
  autoArchiveSubscription = (key: string) => this.call<Workflow>('ComfortAutoArchiveSubscription', key);
  createInvoiceTask = (id: number) => this.call<{ id: string }>('ComfortInvoiceTask', id);
  spellCheck = (text: string, language?: string) => this.call<SpellResult>('ComfortSpellCheck', { text, language });
  spellWords = () => this.call<string[]>('ComfortSpellWords');
  addSpellWord = (word: string) => this.call<void>('ComfortAddSpellWord', word);
  deleteSpellWord = (word: string) => this.call<void>('ComfortDeleteSpellWord', word);
  printUrl = (o: { messageId?: number; conversationId?: number; layout?: 'mail' | 'chat'; lang?: string }) => printUrl('', o);
  pdfUrl = (id: number, download?: boolean) => `/api/comfort/messages/${id}/pdf${download ? '?download=1' : ''}`;
  exportPdfs = async (input: ExportPdfInput) => ({ folder: await this.call<string>('ComfortExportPDFs', input) });
  parseMergeTable = async (file: File) => this.call<MergeTable>('ComfortParseMergeTable', await fileToBase64(file));
  mergeFromGroup = (group: string) => this.call<MergeTable>('ComfortMergeFromGroup', group);
  previewMerge = (input: MergeJobInput) => this.call<MergeRendered[]>('ComfortPreviewMerge', input);
  startMerge = (input: MergeJobInput) => this.call<MergeJob>('ComfortStartMerge', input);
  listMergeJobs = () => this.call<MergeJob[]>('ComfortListMergeJobs');
  getMergeJob = (id: string) => this.call<MergeJob>('ComfortGetMergeJob', id);
  retryMergeJob = (id: string) => this.call<MergeJob>('ComfortRetryMergeJob', id);
  cancelMergeJob = (id: string) => this.call<void>('ComfortCancelMergeJob', id);
  createMeeting = (input: MeetingInput) => this.call<MeetingLink>('ComfortCreateMeeting', input);
  listCertificates = () => this.call<SmimeCertificate[]>('ComfortListCertificates');
  importCertificate = async (file: File, password?: string) =>
    this.call<SmimeCertificate>('ComfortImportCertificate', { data: await fileToBase64(file), password, filename: file.name });
  trustCertificate = (id: number, trusted: boolean) => this.call<void>('ComfortTrustCertificate', id, trusted);
  deleteCertificate = (id: number) => this.call<void>('ComfortDeleteCertificate', id);
  smimeStatus = (from: string, recipients: string[]) => this.call<SmimeStatus>('ComfortSMIMEStatus', from, recipients);
  listPgpKeys = () => this.call<PgpKey[]>('ComfortListPGPKeys');
  generatePgpKey = (input: { name: string; email: string; passphrase?: string }) => this.call<PgpKey>('ComfortGeneratePGPKey', input);
  unlockPgpKey = (id: number, passphrase: string) => this.call<PgpKey>('ComfortUnlockPGPKey', id, { passphrase });
  changePgpPassphrase = (id: number, current: string, next: string) => this.call<PgpKey>('ComfortChangePGPPassphrase', id, { current, new: next });
  lockPgpKeys = () => this.call<void>('ComfortLockPGPKeys');
  importPgpKeys = async (file: File, passphrase?: string) => this.call<PgpKey[]>('ComfortImportPGPKeys', { data: await fileToBase64(file), passphrase });
  exportPgpPublicKey = (id: number) => this.call<string>('ComfortExportPGPPublicKey', id);
  exportPgpSecretKey = (id: number, passphrase: string, current?: string) => this.call<string>('ComfortExportPGPSecretKey', id, { passphrase, current });
  verifyPgpKey = (id: number, verified: boolean) => this.call<void>('ComfortSetPGPKeyVerified', id, verified);
  deletePgpKey = (id: number) => this.call<void>('ComfortDeletePGPKey', id);
  lookupPgpKey = (email: string, source: 'wkd' | 'keyserver') => this.call<PgpKey>('ComfortLookupPGPKey', { email, source });
  listPgpPeers = () => this.call<PgpPeer[]>('ComfortListPGPPeers');
  verifyPgpPeer = (address: string, fingerprint: string) => this.call<PgpKey>('ComfortVerifyPGPPeer', { address, fingerprint });
  deletePgpPeer = (address: string) => this.call<void>('ComfortDeletePGPPeer', address);
  pgpStatus = (from: string, recipients: string[]) => this.call<PgpStatus>('ComfortPGPStatus', from, recipients);
}

// ---- selection -----------------------------------------------------------------------

const cache = new WeakMap<Api, ComfortApi>();

/** The comfort API matching the main transport. */
export function comfortFor(api: Api, opts: { baseUrl?: string; bearer?: () => string | undefined } = {}): ComfortApi {
  let c = cache.get(api);
  if (!c) {
    c = api.kind === 'wails' ? new ComfortWails() : api.kind === 'http' ? new ComfortHttp(opts.baseUrl ?? '', undefined, opts.bearer) : comfortMock();
    cache.set(api, c);
  }
  return c;
}
