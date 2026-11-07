// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Offline core of the Android app ("Über den Server" like the desktop app,
// internal/app/remote_proxy.go, remote_cache.go, remote_outbox.go): every
// fetch of the interface goes through here. Online it goes to the server and
// what may be kept is kept; without a connection the kept answers are served,
// lists and searches come from the index of loaded mails, and writes that can
// wait go into the outbox. A mail keeps its outbox id as Idempotency-Key for
// all attempts, and the server keeps the result of a key: a lost answer can
// not send it twice. When the server fails after taking a mail, it stays
// "unclear" until the user decides (same texts as on the desktop).

import type { ConnectionStateName, MessageDetail, MessageSummary, OutgoingMessage, SearchHit, SearchQuery, SearchResult } from '../../types';
import { bodySize, type Body, type EntryMeta, type IndexedMessage, type OfflineDb } from './db';
import {
  MAX_OFFLINE_UPLOAD,
  MAX_OFFLINE_UPLOADS,
  MAX_OUTBOX_ATTEMPTS,
  OFFLINE_TOKEN_PREFIX,
  OPENED_OLD_KEEP_MS,
  RE_CONV_ITEMS,
  RE_MESSAGE_DETAIL,
  RE_MESSAGE_PART,
  actionMessageIds,
  cacheKey,
  cacheable,
  describeMail,
  fullContentAllowed,
  matchesWords,
  messageOfPath,
  messageSearchText,
  newOutboxId,
  newUploadToken,
  outboxUploads,
  overlaps,
  overlayPending,
  pendingChanges,
  queueable,
  stripTags,
  textFilter,
  withLocalUploads,
  type OfflineSettings,
  type QueueKind,
  type StoredOutboxItem,
} from './rules';

export type CoreState = Extract<ConnectionStateName, 'online' | 'offline' | 'expired'>;

export type CoreEvent =
  | { type: 'state'; state: CoreState; was: CoreState }
  | { type: 'conflict'; item: StoredOutboxItem }
  | { type: 'blocked'; item: StoredOutboxItem }
  | { type: 'sent'; id: string }
  /** Discarded while on its way: what became of it. */
  | { type: 'discarded'; id: string; outcome: 'sent' | 'unclear' | 'not_sent' }
  /** What is on screen came from the store or changed at the server: load it again. */
  | { type: 'reload' };

export interface CoreDeps {
  db: OfflineDb;
  /** https origin of the server, without trailing slash. */
  baseUrl: string;
  /** The network (in the app: fetch patched by CapacitorHttp). */
  net: typeof fetch;
  token: () => string | undefined;
  settings: () => OfflineSettings;
  /** Wi-Fi or another unmetered network (ahead-of-time loading with setting "wifi"). */
  unmetered?: () => Promise<boolean>;
  texts: { offline: string; unclear: string };
  now?: () => number;
  timeouts?: { read: number; write: number };
  onChange?: () => void;
  onEvent?: (e: CoreEvent) => void;
  /** Initial state, e.g. "offline" when the device reports no network at the start. */
  initial?: CoreState;
}

export const HEADER_OFFLINE = 'X-Tuxmail-Offline';
const MAX_ENTRY = 20 * 1024 * 1024;
const MAX_INDEXED = 20000;
const AHEAD_LISTS = ['/api/messages?scope=unified&role=inbox&offset=0&limit=100', '/api/messages?scope=unified&role=sent&offset=0&limit=50'];
const BASE_ROUTES = [
  '/api/auth/me',
  '/api/identities',
  '/api/signatures',
  '/api/persons',
  '/api/preferences',
  '/api/tags',
  '/api/projects',
  '/api/search-folders',
  '/api/search/profiles',
  '/api/calendars',
  '/api/tasks',
  '/api/addressbooks',
  '/api/contacts',
  '/api/contact-groups',
  '/api/snippets',
  '/api/focus',
  '/api/chat-folders',
];
/** Kept even when space runs short: without them the app cannot start offline. */
const ESSENTIAL = /^\/api\/(accounts(\/\d+\/folders)?|identities|signatures|preferences|auth\/me|auth\/config|locales(\/[\w-]+)?|chat-folders|tags)$/;

class NetError extends Error {}

interface NetResult {
  status: number;
  headers: Headers;
  body: Body;
}

function isTextual(ct: string | null): boolean {
  return !ct || /json|^text\//i.test(ct);
}

function jsonResponse(status: number, v: unknown, offline: boolean): Response {
  const h: Record<string, string> = { 'Content-Type': 'application/json; charset=utf-8' };
  if (offline) h[HEADER_OFFLINE] = '1';
  return new Response(JSON.stringify(v), { status, headers: h });
}

function isServerAnswer(body: Body): boolean {
  if (typeof body !== 'string') return false;
  try {
    const e = JSON.parse(body) as { error?: unknown };
    return typeof e.error === 'string' && e.error !== '';
  } catch {
    return false;
  }
}

function idemConflict(body: Body): string | null {
  if (typeof body !== 'string') return null;
  try {
    const e = JSON.parse(body) as { error?: unknown };
    return typeof e.error === 'string' && e.error.startsWith('idempotency_') ? e.error : null;
  } catch {
    return null;
  }
}

const gatewayDown = (status: number) => status === 502 || status === 503 || status === 504;

export class OfflineCore {
  private st: CoreState;
  private flushing: Promise<void> | null = null;
  /** The outbox entry on its way to the server, and entries discarded meanwhile. */
  private sending: string | null = null;
  private readonly discarded = new Set<string>();
  private syncing: Promise<void> | null = null;
  private seq = 0;
  private written = 0;
  private readonly now: () => number;
  private readonly timeouts: { read: number; write: number };

  constructor(private readonly d: CoreDeps) {
    this.st = d.initial ?? 'online';
    this.now = d.now ?? Date.now;
    this.timeouts = d.timeouts ?? { read: 15000, write: 60000 };
  }

  get state(): CoreState {
    return this.st;
  }

  private setState(next: CoreState): void {
    if (next === this.st) return;
    const was = this.st;
    this.st = next;
    this.d.onEvent?.({ type: 'state', state: next, was });
    this.d.onChange?.();
  }

  markOffline(): void {
    if (this.st === 'online') this.setState('offline');
  }

  markExpired(): void {
    this.setState('expired');
  }

  private seen(): void {
    void this.d.db.setMeta('lastConnectedAt', this.now()).catch(() => {});
    if (this.st === 'offline') this.setState('online');
  }

  // ==== network =============================================================

  private async net(method: string, path: string, headers: Record<string, string>, body: BodyInit | undefined, timeout: number): Promise<NetResult> {
    const h: Record<string, string> = { ...headers };
    if (!Object.keys(h).some((k) => k.toLowerCase() === 'authorization')) {
      const t = this.d.token();
      if (t) h.Authorization = `Bearer ${t}`;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const res = await Promise.race([
        this.d.net(this.d.baseUrl + path, { method, headers: h, body, credentials: 'omit' }),
        new Promise<never>((_, reject) => (timer = setTimeout(() => reject(new NetError('timeout')), timeout))),
      ]);
      const ct = res.headers.get('Content-Type');
      const out: Body = res.status === 204 ? '' : isTextual(ct) ? await res.text() : await res.blob();
      return { status: res.status, headers: res.headers, body: out };
    } catch (err) {
      throw err instanceof NetError ? err : new NetError(err instanceof Error ? err.message : String(err));
    } finally {
      clearTimeout(timer);
    }
  }

  private toResponse(r: NetResult): Response {
    const h = new Headers();
    for (const k of ['Content-Type', 'Content-Disposition', 'Retry-After', 'ETag', 'Last-Modified', 'Content-Range', 'Accept-Ranges']) {
      const v = r.headers.get(k);
      if (v) h.set(k, v);
    }
    const empty = r.status === 204 || r.status === 205 || r.status === 304;
    return new Response(empty ? null : r.body, { status: r.status, headers: h });
  }

  private offlineUnavailable(): Response {
    return jsonResponse(503, { error: 'offline', message: this.d.texts.offline }, true);
  }

  /** fetch() for HttpTransport. */
  fetch: typeof fetch = async (input, init) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const base = this.d.baseUrl;
    if (!raw.startsWith(base + '/api/')) return this.d.net(input, init);
    const u = new URL(raw);
    const method = (init?.method ?? 'GET').toUpperCase();
    const headers: Record<string, string> = {};
    new Headers(init?.headers).forEach((v, k) => (headers[k] = v));
    if (method === 'GET' || method === 'HEAD') return this.read(method, u, headers);
    return this.write(method, u, headers, init?.body ?? undefined);
  };

  // ==== reading =============================================================

  private async read(method: string, u: URL, headers: Record<string, string>): Promise<Response> {
    const keep = method === 'GET' && cacheable(u.pathname) && !headers.range;
    if (this.st === 'online') {
      try {
        const r = await this.net(method, u.pathname + u.search, headers, undefined, this.timeouts.read);
        if (gatewayDown(r.status) && !isServerAnswer(r.body)) this.markOffline();
        else if (r.status === 401) this.markExpired();
        else {
          this.seen();
          if (keep && r.status === 200) await this.keep(u, r.headers.get('Content-Type') ?? '', r.headers.get('Content-Disposition') ?? '', r.body).catch(() => {});
          return this.toResponse(r);
        }
      } catch {
        this.markOffline();
      }
    }
    return this.offlineRead(u);
  }

  /** Keeps an answer and indexes the mails in it. */
  async keep(u: URL, ct: string, disp: string, body: Body, storedAt?: number): Promise<void> {
    const size = bodySize(body);
    if (size > MAX_ENTRY) return;
    const now = this.now();
    const key = cacheKey(u);
    if (storedAt !== undefined) {
      // An answer loaded in the background earlier must not replace a newer one.
      const have = await this.d.db.getEntry(key);
      if (have && have.meta.storedAt >= storedAt) return;
    }
    const msgId = messageOfPath(u.pathname);
    let msgDate: string | undefined;
    if (msgId !== undefined) msgDate = (await this.d.db.getMessage(msgId))?.date;
    if (RE_MESSAGE_DETAIL.test(u.pathname) && typeof body === 'string') {
      try {
        msgDate = (JSON.parse(body) as MessageDetail).date ?? msgDate;
      } catch {
        /* other shape */
      }
    }
    const meta: EntryMeta = { key, path: u.pathname, size, at: now, storedAt: storedAt ?? now, ct: ct || undefined, disp: disp || undefined, msgId, msgDate };
    await this.d.db.putEntry(meta, body);
    if (typeof body === 'string') await this.index(u, body);
    this.written += size;
    if (this.written > Math.min(this.d.settings().maxBytes / 8, 16 << 20)) {
      this.written = 0;
      await this.prune();
    }
  }

  private async index(u: URL, body: string): Promise<void> {
    if (u.pathname === '/api/messages') {
      let page: { items?: MessageSummary[] };
      try {
        page = JSON.parse(body);
      } catch {
        return;
      }
      const items = page.items ?? [];
      const existing = new Map((await Promise.all(items.map((m) => this.d.db.getMessage(m.id)))).flatMap((m) => (m ? [[m.id, m] as const] : [])));
      await this.d.db.putMessages(items.map((m) => ({ ...indexed(m, ''), search: existing.get(m.id)?.search ?? messageSearchText(m), hasBody: existing.get(m.id)?.hasBody })));
      await this.reconcileFolder(u, items);
      return;
    }
    const m = RE_MESSAGE_DETAIL.exec(u.pathname);
    if (m) {
      let d: MessageDetail;
      try {
        d = JSON.parse(body);
      } catch {
        return;
      }
      if (!d.id) return;
      await this.d.db.putMessages([{ ...indexed(d, d.text || stripTags(d.html ?? '')), hasBody: true }]);
    }
  }

  /**
   * Delta check of a folder: the first page of a folder covers everything from
   * its oldest mail on. A kept mail of that time span that is no longer in it
   * was moved or deleted at the server: it leaves the folder here as well.
   */
  private async reconcileFolder(u: URL, items: MessageSummary[]): Promise<void> {
    const q = u.searchParams;
    if (q.get('scope') !== 'folder' || (q.get('offset') ?? '0') !== '0' || q.get('q')) return;
    const folderId = Number(q.get('folderId'));
    const limit = Number(q.get('limit') || 0);
    if (!folderId) return;
    const ids = new Set(items.map((m) => m.id));
    // A full page: only its time span is known. Fewer than asked for: the whole folder.
    const oldest = limit > 0 && items.length >= limit ? Math.min(...items.map((m) => Date.parse(m.date)).filter(Number.isFinite)) : -Infinity;
    const all = await this.d.db.allMessages();
    const gone: number[] = [];
    const moved: IndexedMessage[] = [];
    for (const m of all) {
      if (!m.folderIds.includes(folderId) || ids.has(m.id)) continue;
      const t = Date.parse(m.date);
      if (Number.isFinite(t) && t < oldest) continue;
      const rest = m.folderIds.filter((f) => f !== folderId);
      if (rest.length) moved.push({ ...m, folderIds: rest });
      else gone.push(m.id);
    }
    if (moved.length) await this.d.db.putMessages(moved);
    if (gone.length) await this.forgetMessages(gone);
  }

  /** Mails the server no longer has: index entry, content and attachments go. */
  async forgetMessages(ids: number[]): Promise<void> {
    if (!ids.length) return;
    const set = new Set(ids);
    const metas = await this.d.db.listEntryMetas();
    await this.d.db.deleteEntries(metas.filter((m) => m.msgId !== undefined && set.has(m.msgId)).map((m) => m.key));
    await this.d.db.deleteMessages(ids);
  }

  private async offlineRead(u: URL): Promise<Response> {
    if (!cacheable(u.pathname)) return this.offlineUnavailable();
    const key = cacheKey(u);
    const e = await this.d.db.getEntry(key);
    if (e) {
      await this.d.db.touch(key, this.now());
      const h: Record<string, string> = { [HEADER_OFFLINE]: '1' };
      if (e.meta.ct) h['Content-Type'] = e.meta.ct;
      if (e.meta.disp) h['Content-Disposition'] = e.meta.disp;
      const body = typeof e.body === 'string' ? await this.overlay(u, e.body) : e.body;
      return new Response(body, { status: 200, headers: h });
    }
    return this.offlineFallback(u);
  }

  private async overlay(u: URL, body: string): Promise<string> {
    if (u.pathname !== '/api/messages' && u.pathname !== '/api/conversations' && !RE_MESSAGE_DETAIL.test(u.pathname)) return body;
    const items = await this.d.db.listOutbox<StoredOutboxItem>();
    const p = pendingChanges(items);
    const inList = await this.listMembership(u);
    return overlayPending(u.pathname, body, p, inList);
  }

  /** For a message list: does a mail in folder f belong to it? */
  private async listMembership(u: URL): Promise<((f: number) => boolean) | undefined> {
    if (u.pathname !== '/api/messages') return undefined;
    const q = u.searchParams;
    if (q.get('scope') === 'folder') {
      const id = Number(q.get('folderId'));
      return (f) => f === id;
    }
    if (q.get('scope') === 'unified') {
      const role = q.get('role') ?? '';
      const folders = new Set(await this.roleFolders(role));
      return (f) => folders.has(f);
    }
    return undefined;
  }

  private async roleFolders(role: string): Promise<number[]> {
    const out: number[] = [];
    for (const { meta, body } of await this.d.db.listBodies('/api/accounts/', 500)) {
      if (!meta.path.endsWith('/folders') || typeof body !== 'string') continue;
      try {
        for (const f of JSON.parse(body) as { id: number; role?: string }[]) if (f.role === role) out.push(f.id);
      } catch {
        /* other shape */
      }
    }
    return out;
  }

  /** Lists for questions never asked online (other page, other week, a search). */
  private async offlineFallback(u: URL): Promise<Response> {
    const q = u.searchParams;
    const path = u.pathname;
    if (path === '/api/messages') return this.offlineMessages(u);
    if (path === '/api/conversations') return this.offlineConversations(u);
    if (RE_CONV_ITEMS.test(path)) return this.offlineUnion(path);
    if (path === '/api/calendar-events') {
      const ids = (q.get('calendarIds') ?? '').split(',').map(Number).filter(Boolean);
      return this.offlineUnion(path, (o) => (!ids.length || ids.includes(Number(o.calendarId))) && overlaps(o.start, o.end, q.get('from'), q.get('to')));
    }
    if (path === '/api/tasks') return this.offlineUnion(path, textFilter(q.get('q')));
    if (path === '/api/contacts') {
      const book = Number(q.get('addressBookId') || 0);
      const txt = textFilter(q.get('q'));
      return this.offlineUnion(path, (o) => (!book || Number(o.addressBookId) === book) && txt(o));
    }
    if (path === '/api/autocomplete') return this.offlineAutocomplete(q.get('q') ?? '', Number(q.get('limit') || 10));
    const detail = RE_MESSAGE_DETAIL.exec(path);
    if (detail) return this.headersOnly(Number(detail[1]));
    if (RE_MESSAGE_PART.test(path) || path.startsWith('/api/messages/')) return this.offlineUnavailable();
    if (['/api/preferences', '/api/focus', '/api/auth/me', '/api/auth/config'].includes(path)) return this.offlineUnavailable();
    if (path.split('/').length === 3 || path === '/api/search/profiles' || path === '/api/persons/frequent' || path.endsWith('/folders')) return jsonResponse(200, [], true);
    return this.offlineUnavailable();
  }

  /** A mail whose content is not on the device: its header lines, and the note "content only online". */
  private async headersOnly(id: number): Promise<Response> {
    const m = await this.d.db.getMessage(id);
    if (!m) return this.offlineUnavailable();
    const s = JSON.parse(m.summary) as MessageSummary;
    const items = await this.d.db.listOutbox<StoredOutboxItem>();
    const body = overlayPending(`/api/messages/${id}`, JSON.stringify({ ...s, inReplyTo: [], references: [], attachments: [], text: '', html: '', offlineHeadersOnly: true }), pendingChanges(items));
    return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8', [HEADER_OFFLINE]: '1' } });
  }

  private async offlineMessages(u: URL): Promise<Response> {
    const q = u.searchParams;
    const offset = Number(q.get('offset') || 0);
    const limit = Number(q.get('limit') || 50);
    const empty = () => jsonResponse(200, { items: [], total: 0, offset }, true);
    const pending = pendingChanges(await this.d.db.listOutbox<StoredOutboxItem>());
    let inFolder: (folders: number[]) => boolean = () => true;
    switch (q.get('scope')) {
      case 'folder': {
        const id = Number(q.get('folderId'));
        inFolder = (f) => f.includes(id);
        break;
      }
      case 'unified': {
        const ids = new Set(await this.roleFolders(q.get('role') ?? ''));
        if (!ids.size) return empty();
        inFolder = (f) => f.some((x) => ids.has(x));
        break;
      }
      case 'tag':
      case 'searchFolder':
        return empty();
    }
    const text = q.get('q');
    const rows = (await this.d.db.allMessages())
      .map((m) => (pending.moves.has(m.id) ? { ...m, folderIds: [pending.moves.get(m.id)!] } : m))
      .filter((m) => inFolder(m.folderIds) && matchesWords(m.search, text))
      .sort((a, b) => (a.date < b.date ? 1 : a.date > b.date ? -1 : b.id - a.id));
    const page = rows.slice(offset, offset + limit).map((m) => JSON.parse(m.summary) as MessageSummary);
    const body = overlayPending('/api/messages', JSON.stringify({ items: page, total: rows.length, offset }), { ...pending, moves: new Map() });
    return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8', [HEADER_OFFLINE]: '1' } });
  }

  /** The newest kept list of the same view, whatever its paging. */
  private async offlineConversations(u: URL): Promise<Response> {
    const want = u.searchParams;
    const empty = jsonResponse(200, { items: [], total: 0, offset: 0 }, true);
    if ((want.get('offset') ?? '0') !== '0') return empty;
    for (const { meta, body } of await this.d.db.listBodies('/api/conversations?', 100)) {
      if (meta.path !== '/api/conversations' || typeof body !== 'string') continue;
      const q = new URL('http://x' + meta.key).searchParams;
      const same = ['scope', 'accountId', 'folderId', 'grouping', 'role', 'tagId', 'searchFolderId', 'q', 'chatFolder', 'focus', 'tag', 'filter', 'archived'].every((k) => (q.get(k) ?? '') === (want.get(k) ?? ''));
      if (same) return new Response(await this.overlay(u, body), { status: 200, headers: { 'Content-Type': 'application/json', [HEADER_OFFLINE]: '1' } });
    }
    return empty;
  }

  /** All kept list answers of a route, without duplicates. */
  private async offlineUnion(path: string, keep?: (o: Record<string, unknown>) => boolean): Promise<Response> {
    const seen = new Set<string>();
    const out: Record<string, unknown>[] = [];
    for (const { meta, body } of await this.d.db.listBodies(path, 200)) {
      if (meta.path !== path || typeof body !== 'string') continue;
      let list: unknown;
      try {
        list = JSON.parse(body);
      } catch {
        continue;
      }
      if (!Array.isArray(list)) continue;
      for (const o of list as Record<string, unknown>[]) {
        const id = String(o.id);
        if (seen.has(id) || (keep && !keep(o))) continue;
        seen.add(id);
        out.push(o);
      }
    }
    return jsonResponse(200, out, true);
  }

  private async offlineAutocomplete(text: string, limit: number): Promise<Response> {
    const contacts = (await (await this.offlineUnion('/api/contacts', textFilter(text))).json()) as { id: number; displayName?: string; emails?: { value?: string }[] }[];
    const out: { name: string; email: string; contactId: number; source: string }[] = [];
    for (const c of contacts) for (const e of c.emails ?? []) if (out.length < (limit || 10) && e.value) out.push({ name: c.displayName ?? '', email: e.value, contactId: c.id, source: 'contact' });
    return jsonResponse(200, out, true);
  }

  /** Searches the loaded mails. */
  async offlineSearch(body: string): Promise<Response> {
    let q: Partial<SearchQuery>;
    try {
      q = JSON.parse(body);
    } catch {
      return jsonResponse(400, { error: 'bad_request', message: 'invalid search' }, true);
    }
    const limit = q.limit && q.limit > 0 ? q.limit : 50;
    const offset = Number(q.cursor || 0) || 0;
    const accounts = new Set(q.accountIds ?? []);
    const folders = new Set(q.folderIds ?? []);
    const rows = (await this.d.db.allMessages())
      .filter((m) => (!accounts.size || accounts.has(m.accountId)) && (!folders.size || m.folderIds.some((f) => folders.has(f))) && matchesWords(m.search, q.text))
      .sort((a, b) => (a.date < b.date ? 1 : a.date > b.date ? -1 : 0));
    const hits: SearchHit[] = rows.slice(offset, offset + limit).map((r) => {
      const m = JSON.parse(r.summary) as MessageSummary;
      const from = m.from?.[0];
      return {
        id: `mail:${m.id}`,
        kind: 'mail',
        title: m.subject,
        snippet: m.preview,
        highlights: [],
        titleHighlights: [],
        date: m.date,
        accountId: m.accountId,
        folderId: m.folderIds?.[0],
        sender: from ? from.name || from.email : undefined,
        size: m.size,
        hasAttachments: m.hasAttachments,
        unread: !(m.flags ?? []).includes('\\Seen'),
        score: 0,
        messageId: m.id,
        conversationId: m.conversationId,
        itemId: `m:${m.id}`,
      };
    });
    const e = () => [];
    const out: SearchResult = {
      hits,
      total: rows.length,
      facets: { account: e(), folder: e(), sender: e(), domain: e(), channel: e(), type: e(), attachment: e() },
      histogram: [],
      fallback: true,
      tookMs: 0,
      nextCursor: offset + hits.length < rows.length ? String(offset + hits.length) : undefined,
    };
    return jsonResponse(200, out, true);
  }

  // ==== writing =============================================================

  private async write(method: string, u: URL, headers: Record<string, string>, body: BodyInit | undefined): Promise<Response> {
    const path = u.pathname;
    const q = queueable(method, path);
    const text = typeof body === 'string' ? body : body == null ? '' : null;
    if (path === '/api/search' && method === 'POST' && this.st !== 'online') return this.offlineSearch(text ?? '');
    if (this.st !== 'online') {
      if (path === '/api/uploads' && method === 'POST') return this.offlineUpload(headers, body);
      if (!q || text === null) return this.offlineUnavailable();
      return this.enqueue(method, u, text, q, '');
    }
    if (q === 'send' && text !== null) {
      // Attachments kept while offline first have to reach the server: the outbox knows how.
      if (outboxUploads(text).length) {
        const r = await this.enqueue(method, u, text, q, '');
        void this.flush();
        return r;
      }
      // A mail in doubt: the same mail again goes nowhere until the user has decided.
      const unclear = (await this.d.db.listOutbox<StoredOutboxItem>()).find((it) => it.kind === 'send' && it.state === 'in_doubt' && it.path === path && it.body === text);
      if (unclear) return this.unclearResponse(unclear.id);
    }
    const id = q === 'send' ? newOutboxId() : '';
    const h = { ...headers };
    if (id) h['Idempotency-Key'] = id;
    const size = typeof body === 'string' ? body.length : body instanceof Blob ? body.size : 0;
    let r: NetResult;
    try {
      r = await this.net(method, path + u.search, h, body, this.timeouts.write * (size > 8 << 20 ? 5 : 1));
    } catch {
      this.markOffline();
      return this.afterNetworkLoss(method, u, text, q, id, headers, body);
    }
    if (gatewayDown(r.status) && !isServerAnswer(r.body)) {
      this.markOffline();
      return this.afterNetworkLoss(method, u, text, q, id, headers, body);
    }
    if (r.status === 401) {
      this.markExpired();
      return this.toResponse(r);
    }
    this.seen();
    if (q === 'send' && text !== null && (r.status >= 500 || idemConflict(r.body))) {
      // The server failed after taking the mail, perhaps after the relay took it:
      // "try again" could deliver it twice. It stays, with its key, as unclear.
      await this.d.db.putOutbox(this.item(id, 'send', method, u, text, 'in_doubt', `server error (${r.status})`));
      this.d.onChange?.();
      return this.unclearResponse(id);
    }
    return this.toResponse(r);
  }

  private async afterNetworkLoss(method: string, u: URL, text: string | null, q: QueueKind, id: string, headers: Record<string, string>, body: BodyInit | undefined): Promise<Response> {
    if (u.pathname === '/api/search') return this.offlineSearch(text ?? '');
    if (u.pathname === '/api/uploads' && method === 'POST') return this.offlineUpload(headers, body);
    if (!q || text === null) return this.offlineUnavailable();
    return this.enqueue(method, u, text, q, id);
  }

  private unclearResponse(id: string): Response {
    return jsonResponse(409, { error: 'send_unclear', message: this.d.texts.unclear, outboxId: id }, false);
  }

  private item(id: string, kind: 'send' | 'action', method: string, u: URL, body: string, state: StoredOutboxItem['state'], error?: string): StoredOutboxItem {
    const it: StoredOutboxItem = { id: id || newOutboxId(), seq: this.now() * 1000 + (this.seq++ % 1000), kind, method, path: u.pathname + u.search, body, state, attempts: 0, createdAt: new Date(this.now()).toISOString() };
    if (error) it.error = error;
    if (kind === 'send') Object.assign(it, describeMail(body));
    return it;
  }

  /** Notes an action; `id` is the key of a first attempt whose answer got lost. */
  private async enqueue(method: string, u: URL, body: string, q: 'send' | 'action', id: string): Promise<Response> {
    const it = this.item(id, q, method, u, body, 'pending');
    if (q === 'action') it.title = await this.actionTitle(it);
    await this.d.db.putOutbox(it);
    this.d.onChange?.();
    if (q === 'send') return jsonResponse(200, { queued: true, offline: true, outboxId: it.id }, true);
    return new Response(null, { status: 204, headers: { [HEADER_OFFLINE]: '1' } });
  }

  /** The subject of the (first) mail an action is about, for the status and for conflicts. */
  private async actionTitle(it: StoredOutboxItem): Promise<string | undefined> {
    const ids = actionMessageIds(it);
    if (!ids.length) return undefined;
    const m = await this.d.db.getMessage(ids[0]);
    if (!m) return undefined;
    try {
      const subject = (JSON.parse(m.summary) as MessageSummary).subject;
      return ids.length > 1 ? `${subject} (+${ids.length - 1})` : subject;
    } catch {
      return undefined;
    }
  }

  private async offlineUpload(headers: Record<string, string>, body: BodyInit | undefined): Promise<Response> {
    let name = '';
    try {
      name = decodeURIComponent(headers['x-filename'] ?? '');
    } catch {
      name = '';
    }
    if (!name) return jsonResponse(400, { error: 'bad_request', message: 'X-Filename header required' }, true);
    const data = body instanceof Blob ? body : new Blob([typeof body === 'string' ? body : body instanceof ArrayBuffer ? body : '']);
    const used = (await this.d.db.listUploads()).reduce((n, u) => n + u.size, 0);
    if (data.size > MAX_OFFLINE_UPLOAD || used + data.size > MAX_OFFLINE_UPLOADS) {
      return jsonResponse(413, { error: 'offline_too_large', message: 'this attachment is too large to keep until the connection is back' }, true);
    }
    const token = newUploadToken();
    await this.d.db.putUpload({ token, filename: name, mime: headers['content-type'] ?? data.type ?? '', size: data.size, data, createdAt: this.now() });
    return jsonResponse(201, { uploadToken: token }, true);
  }

  // ==== the outbox ============================================================

  async outbox(): Promise<StoredOutboxItem[]> {
    return this.d.db.listOutbox<StoredOutboxItem>();
  }

  /** Kept attachments no other waiting mail needs (a draft made from a mail that did not go out uses the same ones). */
  private async freeUploads(id: string, tokens: string[]): Promise<string[]> {
    if (!tokens.length) return [];
    const used = new Set((await this.outbox()).filter((it) => it.id !== id).flatMap((it) => outboxUploads(it.body)));
    return tokens.filter((t) => !used.has(t));
  }

  private async drop(it: StoredOutboxItem, extra: string[] = []): Promise<void> {
    const free = await this.freeUploads(it.id, [...extra, ...outboxUploads(it.body)]);
    await this.d.db.deleteOutbox(it.id);
    await this.d.db.deleteUploads(free);
  }

  /** Replays waiting actions in order. Only one replay runs at a time. */
  flush(): Promise<void> {
    if (this.st !== 'online') return Promise.resolve();
    this.flushing ??= this.flushAll().finally(() => (this.flushing = null));
    return this.flushing;
  }

  private async flushAll(): Promise<void> {
    const items = await this.outbox();
    // Attachments of mails that were never finished do not stay for ever.
    const inUse = new Set(items.flatMap((it) => outboxUploads(it.body)));
    const old = (await this.d.db.listUploads()).filter((u) => u.createdAt < this.now() - 30 * 86400000 && !inUse.has(u.token)).map((u) => u.token);
    await this.d.db.deleteUploads(old);
    let changed = false;
    for (const it of items) {
      if (it.state !== 'pending') continue;
      const go = await this.flushOne(it);
      changed = true;
      if (!go) break;
    }
    if (changed) {
      this.d.onChange?.();
      this.d.onEvent?.({ type: 'reload' });
    }
  }

  private async keepState(it: StoredOutboxItem, state: StoredOutboxItem['state'], error?: string): Promise<void> {
    if (this.discarded.has(it.id)) return;
    it.state = state;
    if (error) it.error = error;
    else delete it.error;
    await this.d.db.putOutbox(it);
  }

  /** Uploads kept attachments and stores the rewritten mail, so that retries repeat exactly the same request. */
  private async replaceUploads(it: StoredOutboxItem): Promise<{ uploaded: string[]; status: number }> {
    if (!it.body.includes(`"uploadToken":"${OFFLINE_TOKEN_PREFIX}`)) return { uploaded: [], status: 0 };
    const doc = JSON.parse(it.body) as { attachments?: Record<string, unknown>[] };
    const uploaded: string[] = [];
    for (const at of doc.attachments ?? []) {
      const tok = typeof at.uploadToken === 'string' ? at.uploadToken : '';
      if (!tok.startsWith(OFFLINE_TOKEN_PREFIX)) continue;
      const up = await this.d.db.getUpload(tok);
      if (!up) return { uploaded, status: 410 };
      const h: Record<string, string> = { 'X-Filename': encodeURIComponent(up.filename), Accept: 'application/json' };
      if (up.mime) h['Content-Type'] = up.mime;
      const r = await this.net('POST', '/api/uploads', h, up.data, this.timeouts.write * 5);
      if (r.status < 200 || r.status >= 300) return { uploaded, status: r.status };
      const res = JSON.parse(typeof r.body === 'string' ? r.body : '{}') as { uploadToken?: string };
      at.uploadToken = res.uploadToken;
      at.localToken = tok;
      uploaded.push(tok);
    }
    it.body = JSON.stringify(doc);
    if (!this.discarded.has(it.id)) await this.d.db.putOutbox(it);
    return { uploaded, status: 0 };
  }

  /** Replays one action; false when the replay has to stop. */
  private async flushOne(it: StoredOutboxItem): Promise<boolean> {
    // Only answers of the server count as attempts: a dead spot is no reason to give up on a mail.
    if (it.kind === 'send' && it.attempts >= MAX_OUTBOX_ATTEMPTS) {
      await this.keepState(it, 'in_doubt', 'too many attempts');
      return true;
    }
    this.sending = it.id;
    try {
      return await this.flushSend(it);
    } finally {
      this.sending = null;
      if (this.discarded.has(it.id)) {
        this.discarded.delete(it.id);
        await this.d.db.deleteOutbox(it.id).catch(() => {});
      }
    }
  }

  private async flushSend(it: StoredOutboxItem): Promise<boolean> {
    const h: Record<string, string> = { Accept: 'application/json', 'Content-Type': 'application/json' };
    let uploaded: string[] = [];
    try {
      if (it.kind === 'send') {
        h['Idempotency-Key'] = it.id;
        const up = await this.replaceUploads(it);
        uploaded = up.uploaded;
        if (up.status === 401) {
          this.markExpired();
          return false;
        }
        if (gatewayDown(up.status)) {
          this.markOffline();
          return false;
        }
        if (up.status) {
          await this.keepState(it, 'failed', `attachment refused (${up.status})`);
          return true;
        }
      }
      const r = await this.net(it.method, it.path, h, it.body, this.timeouts.write);
      return await this.settle(it, r, uploaded);
    } catch {
      // Network: try again later, same key.
      this.markOffline();
      return false;
    }
  }

  private async settle(it: StoredOutboxItem, r: NetResult, uploaded: string[]): Promise<boolean> {
    if (gatewayDown(r.status) && !isServerAnswer(r.body)) {
      // From a proxy in front of the server: the key answers the next try.
      this.markOffline();
      return false;
    }
    if (r.status === 401) {
      this.markExpired();
      return false;
    }
    this.seen();
    it.attempts++;
    if (this.discarded.has(it.id)) {
      // Discarded while it was on its way: say what became of it, keep nothing.
      const sent = r.status >= 200 && r.status < 300 && !(typeof r.body === 'string' && /"blocked"\s*:\s*true/.test(r.body));
      const unclear = it.kind === 'send' && (r.status >= 500 || idemConflict(r.body) !== null);
      if (it.kind === 'send') this.d.onEvent?.({ type: 'discarded', id: it.id, outcome: sent ? 'sent' : unclear ? 'unclear' : 'not_sent' });
      return true;
    }
    if (r.status >= 200 && r.status < 300) {
      if (it.kind === 'send' && typeof r.body === 'string') {
        let res: { blocked?: boolean; findings?: unknown[] } = {};
        try {
          res = JSON.parse(r.body);
        } catch {
          /* not JSON: sent */
        }
        if (res.blocked) {
          // The send checks want a decision: nothing was sent. It comes back as a draft.
          await this.keepState(it, 'blocked', JSON.stringify(res.findings ?? []));
          this.d.onEvent?.({ type: 'blocked', item: it });
          return true;
        }
      }
      await this.drop(it, uploaded);
      if (it.kind === 'send') this.d.onEvent?.({ type: 'sent', id: it.id });
      return true;
    }
    const idem = r.status === 409 ? idemConflict(r.body) : null;
    if (idem === 'idempotency_in_doubt') {
      await this.keepState(it, 'in_doubt', 'the server may have sent this mail already');
      return true;
    }
    if (idem) {
      // Still running at the server: next round, counted (only server answers count).
      await this.keepState(it, 'pending');
      return false;
    }
    if (r.status >= 500 && it.kind === 'send') {
      await this.keepState(it, 'in_doubt', `server error (${r.status})`);
      return true;
    }
    if (r.status >= 500) {
      if (it.attempts >= MAX_OUTBOX_ATTEMPTS) {
        await this.keepState(it, 'failed', `server error (${r.status})`);
        return true;
      }
      await this.keepState(it, 'pending', `server error (${r.status})`);
      return false;
    }
    if (it.kind === 'action') {
      // The mail is gone at the server or the action no longer applies: say so instead of hiding it.
      const gone = r.status === 404 || r.status === 410;
      await this.keepState(it, 'conflict', gone ? 'gone' : `refused:${r.status}`);
      if (gone) await this.forgetMessages(actionMessageIds(it));
      this.d.onEvent?.({ type: 'conflict', item: it });
      return true;
    }
    let msg = `refused by the server (${r.status})`;
    try {
      const e = JSON.parse(typeof r.body === 'string' ? r.body : '{}') as { message?: string };
      if (e.message) msg = e.message;
    } catch {
      /* keep the status */
    }
    await this.keepState(it, 'failed', msg);
    return true;
  }

  /** A waiting mail, to continue it as a draft. */
  async outboxMessage(id: string): Promise<OutgoingMessage> {
    const it = (await this.outbox()).find((x) => x.id === id && x.kind === 'send');
    if (!it) throw new Error('not found');
    return JSON.parse(withLocalUploads(it.body)) as OutgoingMessage;
  }

  async outboxDiscard(id: string): Promise<void> {
    const it = (await this.outbox()).find((x) => x.id === id);
    if (!it) return;
    // On its way right now: it cannot be called back; the answer decides what the user is told.
    if (this.sending === id) this.discarded.add(id);
    await this.drop(it);
    this.d.onChange?.();
  }

  /**
   * Tries again what needed a decision. A mail in doubt keeps its key unless
   * the server itself reports the key as doubtful: then only a new key can
   * send it, and the user decided to risk a second delivery. Done or still
   * running at the server: the same key replays the server's answer, nothing
   * goes out twice. A mail stopped by the send checks goes with those
   * warnings confirmed (nothing was sent; the changed mail needs a new key).
   */
  async outboxRetry(id: string): Promise<void> {
    const it = (await this.outbox()).find((x) => x.id === id);
    if (!it) return;
    let body = it.body;
    let key = it.id;
    if (it.state === 'blocked') {
      let checks: string[] = [];
      try {
        checks = (JSON.parse(it.error ?? '[]') as { check: string }[]).map((f) => f.check);
      } catch {
        /* none */
      }
      const doc = JSON.parse(withLocalUploads(body)) as Record<string, unknown>;
      doc.confirmChecks = checks;
      body = JSON.stringify(doc);
      key = newOutboxId();
    } else if (it.kind === 'send' && it.state === 'in_doubt') {
      const st = await this.keyStatus(it.id);
      if (st === 'doubt' || st === 'unsupported') {
        body = withLocalUploads(body);
        key = newOutboxId();
      } else if (st === 'unknown') body = withLocalUploads(body); // refused and freed: nothing went out
      // done / pending / no answer (offline): same key, same request - the server decides.
    } else body = withLocalUploads(body);
    if (key !== it.id) await this.d.db.deleteOutbox(it.id);
    await this.d.db.putOutbox({ ...it, id: key, body, state: 'pending', error: undefined, attempts: 0 });
    this.d.onChange?.();
    void this.flush();
  }

  /** What the server knows of an Idempotency-Key (GET /api/idempotency/{key}); "offline" without an answer. */
  async keyStatus(key: string): Promise<'done' | 'pending' | 'doubt' | 'unknown' | 'unsupported' | 'offline'> {
    if (this.st !== 'online') return 'offline';
    try {
      const r = await this.net('GET', `/api/idempotency/${encodeURIComponent(key)}`, { Accept: 'application/json' }, undefined, this.timeouts.read);
      if (r.status === 404 || r.status === 405) return 'unsupported'; // a server before this check
      if (r.status !== 200 || typeof r.body !== 'string') return 'offline';
      const s = (JSON.parse(r.body) as { state?: string }).state;
      return s === 'done' || s === 'pending' || s === 'doubt' || s === 'unknown' ? s : 'offline';
    } catch {
      return 'offline';
    }
  }

  // ==== reconnecting ============================================================

  /** Asks the server whether it is there and the device token still valid. */
  async probe(): Promise<CoreState> {
    if (this.st === 'expired') return this.st;
    try {
      const r = await this.net('GET', '/api/auth/me', { Accept: 'application/json' }, undefined, this.timeouts.read);
      if (r.status === 401) this.markExpired();
      else if (gatewayDown(r.status) && !isServerAnswer(r.body)) this.markOffline();
      else if (r.status < 500) {
        const was = this.st;
        this.seen();
        if (r.status === 200) await this.keep(new URL(this.d.baseUrl + '/api/auth/me'), r.headers.get('Content-Type') ?? 'application/json', '', r.body).catch(() => {});
        if (was !== 'online') this.d.onEvent?.({ type: 'reload' });
      }
    } catch {
      this.markOffline();
    }
    return this.st;
  }

  // ==== loading ahead =============================================================

  /** Delta synchronisation: lists again, content only for what is not on the device yet. */
  sync(): Promise<void> {
    if (this.st !== 'online') return Promise.resolve();
    this.syncing ??= this.syncAll().finally(() => (this.syncing = null));
    return this.syncing;
  }

  private async get(path: string): Promise<Body | null> {
    if (this.st !== 'online') return null;
    const u = new URL(this.d.baseUrl + path);
    try {
      const r = await this.net('GET', u.pathname + u.search, { Accept: 'application/json' }, undefined, this.timeouts.read);
      if (r.status === 401) {
        this.markExpired();
        return null;
      }
      if (gatewayDown(r.status) && !isServerAnswer(r.body)) {
        this.markOffline();
        return null;
      }
      if (r.status !== 200) return null;
      this.seen();
      await this.keep(u, r.headers.get('Content-Type') ?? '', r.headers.get('Content-Disposition') ?? '', r.body);
      return r.body;
    } catch {
      this.markOffline();
      return null;
    }
  }

  private async syncAll(): Promise<void> {
    const s = this.d.settings();
    await this.flush();
    for (const p of BASE_ROUTES) await this.get(p);
    const accounts = parse<{ id: number }[]>(await this.get('/api/accounts')) ?? [];
    for (const a of accounts) await this.get(`/api/accounts/${a.id}/folders`);
    const now = this.now();
    const from = new Date(now - 62 * 86400000).toISOString();
    const to = new Date(now + 366 * 86400000).toISOString();
    await this.get(`/api/calendar-events?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`);
    const lists: MessageSummary[] = [];
    for (const p of AHEAD_LISTS) lists.push(...(parse<{ items?: MessageSummary[] }>(await this.get(p))?.items ?? []));
    // The lists are fresh: that is what "loaded last" means to the user; content follows.
    if (this.st === 'online') await this.d.db.setMeta('lastDataSyncAt', this.now());
    this.d.onChange?.();
    // Chats: the conversations of the newest kept list.
    const convs = (await this.d.db.listBodies('/api/conversations?', 1))[0];
    const convIds = (parse<{ items?: { id: number }[] }>(convs?.body ?? null)?.items ?? []).slice(0, 20).map((c) => c.id);
    for (const id of convIds) await this.get(`/api/conversations/${id}/items?limit=50`);

    if (s.network === 'wifi' && !(await (this.d.unmetered?.() ?? Promise.resolve(true)))) {
      await this.finishSync();
      return;
    }
    const seen = new Set<number>();
    const candidates = lists
      .filter((m) => !seen.has(m.id) && seen.add(m.id) && fullContentAllowed(m.date, s.headersOnlyOlderThanDays, now))
      .sort((a, b) => (a.date < b.date ? 1 : -1));
    for (const m of candidates) {
      if (this.st !== 'online' || (await this.cacheBytes()) > s.maxBytes * 0.8) break;
      const have = await this.d.db.getEntry(`/api/messages/${m.id}`);
      let detail = have && typeof have.body === 'string' ? parse<MessageDetail>(have.body) : null;
      if (!detail) detail = parse<MessageDetail>(await this.get(`/api/messages/${m.id}`));
      if (!detail || !s.attachmentMaxBytes) continue;
      for (const a of detail.attachments ?? []) {
        if (a.cloudLink || a.size > s.attachmentMaxBytes || !a.url) continue;
        const path = a.url.replace(this.d.baseUrl, '').split('?')[0];
        if (!RE_MESSAGE_PART.test(path) || (await this.d.db.getEntry(path))) continue;
        await this.get(path);
      }
    }
    await this.finishSync();
  }

  private async finishSync(): Promise<void> {
    if (this.st === 'online') await this.d.db.setMeta('lastDataSyncAt', this.now());
    await this.prune();
    this.d.onChange?.();
  }

  /** Answers loaded in the background (Android WorkManager) while the interface was not running. */
  async importStaged(entries: { key: string; contentType?: string; body: string; storedAt?: number }[]): Promise<number> {
    let n = 0;
    for (const e of entries) {
      let u: URL;
      try {
        u = new URL(this.d.baseUrl + e.key);
      } catch {
        continue;
      }
      if (!cacheable(u.pathname) || !u.pathname.startsWith('/api/')) continue;
      await this.keep(u, e.contentType ?? 'application/json', '', e.body, e.storedAt ?? 0);
      n++;
    }
    if (n) {
      await this.d.db.setMeta('lastDataSyncAt', Math.max(...entries.map((e) => e.storedAt ?? 0), (await this.d.db.getMeta<number>('lastDataSyncAt')) ?? 0));
      this.d.onChange?.();
    }
    return n;
  }

  // ==== cleaning up =============================================================

  async cacheBytes(): Promise<number> {
    const metas = await this.d.db.listEntryMetas();
    const uploads = await this.d.db.listUploads();
    return metas.reduce((n, m) => n + m.size, 0) + uploads.reduce((n, u) => n + u.size, 0);
  }

  /**
   * Keeps the store inside its limits: content of mails beyond the
   * headers-only limit goes a week after it was last opened, then the least
   * recently used answers until the store is below 90 % of its limit.
   */
  async prune(): Promise<void> {
    const s = this.d.settings();
    const now = this.now();
    const metas = await this.d.db.listEntryMetas();
    const drop = new Set<string>();
    const unbodied = new Set<number>();
    for (const m of metas) {
      if (m.msgId === undefined || fullContentAllowed(m.msgDate, s.headersOnlyOlderThanDays, now) || m.at > now - OPENED_OLD_KEEP_MS) continue;
      drop.add(m.key);
      unbodied.add(m.msgId);
    }
    let total = metas.reduce((n, m) => n + (drop.has(m.key) ? 0 : m.size), 0) + (await this.d.db.listUploads()).reduce((n, u) => n + u.size, 0);
    if (total > s.maxBytes) {
      for (const m of metas.filter((x) => !drop.has(x.key) && !ESSENTIAL.test(x.key)).sort((a, b) => a.at - b.at)) {
        if (total <= s.maxBytes * 0.9) break;
        drop.add(m.key);
        total -= m.size;
        if (m.msgId !== undefined && RE_MESSAGE_DETAIL.test(m.path)) unbodied.add(m.msgId);
      }
    }
    await this.d.db.deleteEntries([...drop]);
    if (unbodied.size) {
      const rows = (await Promise.all([...unbodied].map((id) => this.d.db.getMessage(id)))).filter((m): m is IndexedMessage => !!m);
      // The search keeps header lines and preview only.
      await this.d.db.putMessages(rows.map((m) => ({ ...m, hasBody: false, search: messageSearchText(JSON.parse(m.summary) as MessageSummary) })));
    }
    const all = await this.d.db.allMessages();
    if (all.length > MAX_INDEXED) {
      const old = all.filter((m) => !m.hasBody).sort((a, b) => (a.date < b.date ? -1 : 1)).slice(0, all.length - MAX_INDEXED);
      await this.d.db.deleteMessages(old.map((m) => m.id));
    }
  }

  async lastTimes(): Promise<{ lastConnectedAt?: number; lastDataSyncAt?: number }> {
    return { lastConnectedAt: await this.d.db.getMeta<number>('lastConnectedAt'), lastDataSyncAt: await this.d.db.getMeta<number>('lastDataSyncAt') };
  }

  /** Deletes what is kept for reading; waiting mails stay. */
  async clearCache(): Promise<void> {
    const metas = await this.d.db.listEntryMetas();
    await this.d.db.deleteEntries(metas.map((m) => m.key));
    await this.d.db.deleteMessages((await this.d.db.allMessages()).map((m) => m.id));
    this.d.onChange?.();
  }
}

function indexed(m: MessageSummary, body: string): IndexedMessage {
  return { id: m.id, accountId: m.accountId, folderIds: m.folderIds ?? [], date: m.date, summary: JSON.stringify(m), search: messageSearchText(m, body) };
}

function parse<T>(b: Body | null): T | null {
  if (typeof b !== 'string' || !b) return null;
  try {
    return JSON.parse(b) as T;
  } catch {
    return null;
  }
}
