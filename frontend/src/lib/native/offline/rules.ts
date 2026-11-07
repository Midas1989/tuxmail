// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Rules of the Android app's offline store, without any storage or network:
// what may be kept, under which key, which mails keep their content, how a
// search reads them, how noted actions show on what is kept. The routes and
// the matching follow the desktop app in server mode (internal/app/remote_cache.go,
// remote_outbox.go), so both behave the same.

import type { MessageSummary, OutboxEntry } from '../../types';

/** What may be kept: mail, calendar, contacts and what the interface needs to start; never sessions, keys or administration. */
export const CACHE_ROUTES = new RegExp(
  '^/api/(' +
    'accounts(/\\d+/(folders|folder-config))?|identities|signatures|persons(/frequent)?|' +
    'messages(/\\d+(/conversation|/attachments/\\d+|/cid/[^/]+|/invitations|/remote-content)?)?|' +
    'conversations(/\\d+(/items|/draft)?)?|tags|projects|search-folders|search/profiles|preferences|' +
    'locales(/[\\w-]+)?|calendars|calendar-events|tasks|addressbooks|contacts(/\\d+(/photo)?)?|contact-groups|' +
    'chat-folders|snippets|focus|starred|scheduled|workflows|auth/me|auth/config|autocomplete' +
    ')$',
);

export const RE_MESSAGE_DETAIL = /^\/api\/messages\/(\d+)$/;
export const RE_MESSAGE_PART = /^\/api\/messages\/(\d+)\/(attachments\/\d+|cid\/[^/]+)$/;
export const RE_CONV_ITEMS = /^\/api\/conversations\/\d+\/items$/;
export const RE_CONV_READ = /^\/api\/conversations\/(\d+)\/read$/;

export function cacheable(path: string): boolean {
  return CACHE_ROUTES.test(path);
}

/** Path + sorted query: the order of the parameters does not matter. */
export function cacheKey(u: URL): string {
  const q = new URLSearchParams(u.search);
  q.sort();
  const s = q.toString();
  return s ? `${u.pathname}?${s}` : u.pathname;
}

/** The message a kept answer belongs to (detail, attachment, inline part). */
export function messageOfPath(path: string): number | undefined {
  const m = RE_MESSAGE_DETAIL.exec(path) ?? RE_MESSAGE_PART.exec(path);
  return m ? Number(m[1]) : undefined;
}

// ---------------------------------------------------------------------------
// Settings (per server profile)
// ---------------------------------------------------------------------------

/** Same name as the desktop setting in mode "Auf diesem Gerät" (package t-kopfzeilen). */
export const HEADERS_ONLY_KEY = 'mail.headersOnlyOlderThanDays';

export interface OfflineSettings {
  /** Upper limit of the store in bytes. */
  maxBytes: number;
  /** Mails older than this many days keep only their header lines on the device; null = everything in full. */
  headersOnlyOlderThanDays: number | null;
  /** Ahead-of-time loading (bodies, attachments, background work): on any network or only on Wi-Fi. */
  network: 'any' | 'wifi';
  /** Background synchronisation every n minutes; 0 = off. Android allows 15 at the least. */
  intervalMinutes: number;
  /** Attachments up to this size are loaded ahead of time; 0 = only when opened. */
  attachmentMaxBytes: number;
}

const MiB = 1024 * 1024;
export const SIZE_CHOICES_MB = [100, 250, 512, 1024, 2048, 4096];
export const INTERVAL_CHOICES = [0, 15, 30, 60, 120, 360];
export const ATTACHMENT_CHOICES_MB = [0, 1, 2, 5, 10, 25];

export const DEFAULT_SETTINGS: OfflineSettings = {
  maxBytes: 512 * MiB, // desktop: defaultRemoteCacheMax
  headersOnlyOlderThanDays: null, // default: everything in full
  network: 'any',
  intervalMinutes: 30,
  attachmentMaxBytes: 2 * MiB,
};

/** Day count from a text field: empty, 0 or nonsense = everything in full. */
export function parseDays(input: unknown): number | null {
  if (input === null || input === undefined) return null;
  const n = typeof input === 'number' ? input : Number(String(input).trim());
  if (!Number.isFinite(n) || n <= 0) return null;
  return Math.min(Math.floor(n), 36500) || null;
}

/** Stored form: flat keys, the headers-only option under its shared name. */
export function settingsToJson(s: OfflineSettings): string {
  return JSON.stringify({
    'offline.maxBytes': s.maxBytes,
    [HEADERS_ONLY_KEY]: s.headersOnlyOlderThanDays ?? 0,
    'offline.network': s.network,
    'offline.intervalMinutes': s.intervalMinutes,
    'offline.attachmentMaxBytes': s.attachmentMaxBytes,
  });
}

export function settingsFromJson(raw: string | null | undefined): OfflineSettings {
  let o: Record<string, unknown> = {};
  try {
    const v = raw ? JSON.parse(raw) : {};
    if (v && typeof v === 'object') o = v as Record<string, unknown>;
  } catch {
    /* defaults */
  }
  const num = (v: unknown, def: number, min: number, max: number) => (typeof v === 'number' && Number.isFinite(v) ? Math.min(Math.max(Math.floor(v), min), max) : def);
  const interval = num(o['offline.intervalMinutes'], DEFAULT_SETTINGS.intervalMinutes, 0, 24 * 60);
  return {
    maxBytes: num(o['offline.maxBytes'], DEFAULT_SETTINGS.maxBytes, 50 * MiB, 16 * 1024 * MiB),
    headersOnlyOlderThanDays: parseDays(o[HEADERS_ONLY_KEY]),
    network: o['offline.network'] === 'wifi' ? 'wifi' : 'any',
    intervalMinutes: interval === 0 ? 0 : Math.max(interval, 15),
    attachmentMaxBytes: num(o['offline.attachmentMaxBytes'], DEFAULT_SETTINGS.attachmentMaxBytes, 0, 25 * MiB),
  };
}

const DAY = 24 * 60 * 60 * 1000;

/** Whether a mail of this date keeps its content and attachments ahead of time (the headers-only rule). */
export function fullContentAllowed(date: string | undefined, days: number | null, now: number): boolean {
  if (!days) return true;
  const t = Date.parse(date ?? '');
  if (!Number.isFinite(t)) return true; // unknown date: rather keep it
  return t >= now - days * DAY;
}

/** A mail beyond the limit that was opened stays this long after the last look (while there is room). */
export const OPENED_OLD_KEEP_MS = 7 * DAY;

// ---------------------------------------------------------------------------
// Searching what is kept
// ---------------------------------------------------------------------------

const SEARCH_TEXT_MAX = 64 * 1024;

export function stripTags(s: string): string {
  return s.replace(/<[^>]*>/g, ' ');
}

export function messageSearchText(m: Pick<MessageSummary, 'subject' | 'from' | 'to' | 'cc' | 'preview'>, body = ''): string {
  const parts = [m.subject ?? ''];
  for (const list of [m.from, m.to, m.cc]) for (const a of list ?? []) parts.push(a.name ?? '', a.email ?? '');
  parts.push(m.preview ?? '', body);
  return parts.join(' ').toLowerCase().slice(0, SEARCH_TEXT_MAX);
}

export function matchesWords(haystack: string, query: string | undefined | null): boolean {
  const words = (query ?? '').toLowerCase().split(/\s+/).filter(Boolean);
  return words.every((w) => haystack.includes(w));
}

/** Filter for kept lists of other kinds (tasks, contacts): every word somewhere in the JSON. */
export function textFilter(text: string | null): (o: Record<string, unknown>) => boolean {
  const q = (text ?? '').trim();
  if (!q) return () => true;
  return (o) => matchesWords(JSON.stringify(o).toLowerCase(), q);
}

/** ISO times of one family; unreadable values count as overlapping. */
export function overlaps(start: unknown, end: unknown, from: string | null, to: string | null): boolean {
  const p = (v: unknown) => (typeof v === 'string' ? Date.parse(v) : NaN);
  const st = p(start);
  let en = p(end);
  if (!Number.isFinite(en)) en = st;
  if (!Number.isFinite(st)) return true;
  const f = p(from);
  const t = p(to);
  return (!Number.isFinite(t) || st < t) && (!Number.isFinite(f) || en >= f);
}

// ---------------------------------------------------------------------------
// The outbox
// ---------------------------------------------------------------------------

export type QueueKind = 'send' | 'action' | null;

/**
 * Which writes wait for the connection. Like the desktop app, plus moving
 * (archive, delete = move to the trash): on a phone that is the daily work.
 */
export function queueable(method: string, path: string): QueueKind {
  if (method !== 'POST') return null;
  if (path === '/api/send') return 'send';
  if (path === '/api/messages/flags' || path === '/api/messages/move' || RE_CONV_READ.test(path)) return 'action';
  return null;
}

export const OFFLINE_TOKEN_PREFIX = 'offline_';
export const MAX_OFFLINE_UPLOAD = 25 * MiB;
export const MAX_OFFLINE_UPLOADS = 100 * MiB;
/** Answers of the server (never network failures) that leave a mail waiting count as attempts; after this many the user decides. */
export const MAX_OUTBOX_ATTEMPTS = 10;

export interface StoredOutboxItem {
  id: string;
  seq: number;
  kind: 'send' | 'action';
  method: string;
  path: string;
  body: string;
  title?: string;
  recipients?: string;
  /** pending | failed | in_doubt | blocked | conflict */
  state: OutboxEntry['state'];
  error?: string;
  attempts: number;
  createdAt: string;
}

interface Addr {
  email?: string;
}

/** What the status shows of a mail (subject and recipients, no content). */
export function describeMail(body: string): { title?: string; recipients?: string } {
  try {
    const m = JSON.parse(body) as { subject?: string; text?: string; to?: Addr[]; cc?: Addr[]; bcc?: Addr[] };
    const to = [...(m.to ?? []), ...(m.cc ?? []), ...(m.bcc ?? [])].map((a) => a.email ?? '').filter(Boolean);
    let title = m.subject ?? '';
    if (!title && m.text) title = [...m.text].length > 60 ? [...m.text].slice(0, 60).join('') + '…' : m.text;
    return { title, recipients: to.join(', ') };
  } catch {
    return {};
  }
}

interface KeptAttachment {
  uploadToken?: string;
  localToken?: string;
}

/** Kept attachments a waiting mail refers to. */
export function outboxUploads(body: string): string[] {
  try {
    const m = JSON.parse(body) as { attachments?: KeptAttachment[] };
    const out: string[] = [];
    for (const a of m.attachments ?? []) {
      if (a.localToken) out.push(a.localToken);
      else if (a.uploadToken?.startsWith(OFFLINE_TOKEN_PREFIX)) out.push(a.uploadToken);
    }
    return out;
  } catch {
    return [];
  }
}

/** Points a mail back to its kept attachments: the next attempt uploads them anew. */
export function withLocalUploads(body: string): string {
  try {
    const doc = JSON.parse(body) as { attachments?: Record<string, unknown>[] };
    let changed = false;
    for (const a of doc.attachments ?? []) {
      if (typeof a.localToken === 'string' && a.localToken) {
        a.uploadToken = a.localToken;
        delete a.localToken;
        changed = true;
      }
    }
    return changed ? JSON.stringify(doc) : body;
  } catch {
    return body;
  }
}

/** The message ids a noted action is about (for conflicts and the display). */
export function actionMessageIds(item: Pick<StoredOutboxItem, 'path' | 'body'>): number[] {
  if (RE_CONV_READ.test(item.path)) return [];
  try {
    const b = JSON.parse(item.body) as { ids?: unknown };
    return Array.isArray(b.ids) ? b.ids.filter((x): x is number => typeof x === 'number') : [];
  } catch {
    return [];
  }
}

export interface PendingChanges {
  flags: Map<number, { add: string[]; remove: string[] }>;
  readConvs: Set<number>;
  /** message id -> target folder */
  moves: Map<number, number>;
}

export function pendingChanges(items: StoredOutboxItem[]): PendingChanges {
  const out: PendingChanges = { flags: new Map(), readConvs: new Set(), moves: new Map() };
  for (const it of items) {
    if (it.kind !== 'action' || it.state !== 'pending') continue;
    const conv = RE_CONV_READ.exec(it.path);
    if (conv) {
      out.readConvs.add(Number(conv[1]));
      continue;
    }
    let b: { ids?: number[]; add?: string[]; remove?: string[]; folderId?: number };
    try {
      b = JSON.parse(it.body);
    } catch {
      continue;
    }
    for (const id of b.ids ?? []) {
      if (it.path === '/api/messages/move' && typeof b.folderId === 'number') out.moves.set(id, b.folderId);
      if (it.path === '/api/messages/flags') {
        const add = b.add ?? [];
        const remove = b.remove ?? [];
        const ch = out.flags.get(id) ?? { add: [], remove: [] };
        ch.add = [...ch.add, ...add].filter((f) => !remove.includes(f));
        ch.remove = [...ch.remove, ...remove].filter((f) => !add.includes(f));
        out.flags.set(id, ch);
      }
    }
  }
  return out;
}

export function hasPending(p: PendingChanges): boolean {
  return p.flags.size > 0 || p.readConvs.size > 0 || p.moves.size > 0;
}

/**
 * Applies noted marks and moves to a kept answer: a mail marked as read or
 * moved offline looks like that on screen until the server confirms it.
 * `inList(folderId)` tells whether a moved mail still belongs to the list.
 */
export function overlayPending(path: string, body: string, p: PendingChanges, inList?: (folderId: number) => boolean): string {
  const isMsgs = path === '/api/messages' || RE_MESSAGE_DETAIL.test(path);
  const isConvs = path === '/api/conversations';
  if ((!isMsgs && !isConvs) || !hasPending(p)) return body;
  let doc: Record<string, unknown>;
  try {
    doc = JSON.parse(body);
  } catch {
    return body;
  }
  const apply = (o: Record<string, unknown>): boolean => {
    const id = typeof o.id === 'number' ? o.id : NaN;
    if (isConvs) {
      if (p.readConvs.has(id)) {
        o.unreadCount = 0;
        o.markedUnread = false;
      }
      return true;
    }
    const ch = p.flags.get(id);
    if (ch) {
      const cur = (Array.isArray(o.flags) ? (o.flags as string[]) : []).filter((f) => !ch.remove.includes(f));
      for (const f of ch.add) if (!cur.includes(f)) cur.push(f);
      o.flags = cur;
    }
    const to = p.moves.get(id);
    if (to !== undefined) {
      o.folderIds = [to];
      if (inList && !inList(to)) return false;
    }
    return true;
  };
  if (Array.isArray(doc.items)) {
    const before = doc.items.length;
    doc.items = (doc.items as Record<string, unknown>[]).filter((o) => apply(o));
    if (typeof doc.total === 'number') doc.total = Math.max(0, doc.total - (before - (doc.items as unknown[]).length));
  } else apply(doc);
  return JSON.stringify(doc);
}

/** Outbox entries for the status (no message content beyond subject and recipients). */
export function outboxEntries(items: StoredOutboxItem[]): OutboxEntry[] {
  return items.map((it) => {
    const e: OutboxEntry = { id: it.id, kind: it.kind, title: it.title, recipients: it.recipients, state: it.state, createdAt: it.createdAt };
    if (it.kind === 'action') e.action = it.path === '/api/messages/move' ? 'move' : RE_CONV_READ.test(it.path) ? 'read' : 'flags';
    if (it.state === 'blocked') {
      try {
        e.findings = JSON.parse(it.error ?? '[]');
      } catch {
        e.findings = [];
      }
    } else if (it.error) e.error = it.error;
    return e;
  });
}

/** Desktop format of an outbox id: "out_" + random. */
export function newOutboxId(): string {
  const c = globalThis.crypto;
  const raw = c?.randomUUID ? c.randomUUID().replace(/-/g, '') : Date.now().toString(36) + Math.random().toString(36).slice(2);
  return 'out_' + raw.slice(0, 24);
}

export function newUploadToken(): string {
  return OFFLINE_TOKEN_PREFIX + newOutboxId().slice(4);
}
