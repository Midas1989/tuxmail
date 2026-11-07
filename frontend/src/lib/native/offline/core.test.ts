// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { OfflineCore, HEADER_OFFLINE, type CoreEvent } from './core';
import { MemoryDb } from './db';
import { DEFAULT_SETTINGS, HEADERS_ONLY_KEY, fullContentAllowed, parseDays, settingsFromJson, settingsToJson, type OfflineSettings, type StoredOutboxItem } from './rules';

const BASE = 'https://mail.example.org';
const NOW = Date.parse('2026-11-07T12:00:00Z');
const DAY = 86400000;

interface Msg {
  id: number;
  folder: number;
  subject: string;
  date: string;
  body: string;
}

function summary(m: Msg) {
  return { id: m.id, accountId: 1, folderIds: [m.folder], conversationId: m.id, messageId: `<${m.id}@x>`, subject: m.subject, from: [{ name: 'Maria', email: 'maria@example.org' }], to: [{ email: 'anna@example.org' }], cc: [], date: m.date, preview: m.body.slice(0, 20), flags: [] as string[], hasAttachments: m.id === 1, size: 100 };
}

/** A TuxMail server in memory, with the Idempotency-Key semantics of internal/server/api/idempotency.go. */
function fakeServer() {
  const s = {
    offline: false,
    /** Next send: the server takes it, but the answer is lost on the way. */
    loseNextAnswer: false,
    /** Next send fails with 500 after the relay took it. */
    fail500: false,
    blockNext: false,
    tokenOk: true,
    /** While set, /api/send waits for it. */
    hold: null as Promise<void> | null,
    delivered: [] as { key: string | null; subject: string; attachments: unknown[] }[],
    idem: new Map<string, { status: number; body: string }>(),
    uploads: new Map<string, string>(),
    calls: [] as string[],
    msgs: [
      { id: 1, folder: 11, subject: 'Projektplan Q3', date: new Date(NOW - 1 * DAY).toISOString(), body: 'Der Plan für das dritte Quartal mit Budget' },
      { id: 2, folder: 11, subject: 'Rechnung Oktober', date: new Date(NOW - 2 * DAY).toISOString(), body: 'Bitte überweisen' },
      { id: 3, folder: 11, subject: 'Altes Protokoll', date: new Date(NOW - 400 * DAY).toISOString(), body: 'Protokoll der Sitzung von damals' },
    ] as Msg[],
  };
  const json = (status: number, v: unknown) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const fetchImpl: typeof fetch = async (input, init) => {
    if (s.offline) throw new TypeError('Failed to fetch');
    const u = new URL(String(input));
    const method = init?.method ?? 'GET';
    const h = new Headers(init?.headers);
    s.calls.push(`${method} ${u.pathname}${u.search}`);
    if (!s.tokenOk) return json(401, { error: 'unauthenticated' });
    const p = u.pathname;
    if (p === '/api/auth/me') return json(200, { user: { username: 'anna' } });
    if (p === '/api/accounts') return json(200, [{ id: 1 }]);
    if (p === '/api/accounts/1/folders') return json(200, [{ id: 11, role: 'inbox' }, { id: 12, role: 'sent' }, { id: 13, role: 'trash' }]);
    if (p === '/api/messages' && method === 'GET') {
      const q = u.searchParams;
      const folder = q.get('scope') === 'folder' ? Number(q.get('folderId')) : q.get('role') === 'inbox' ? 11 : q.get('role') === 'sent' ? 12 : 0;
      const items = s.msgs.filter((m) => m.folder === folder).map(summary);
      return json(200, { items, total: items.length, offset: 0 });
    }
    let m = /^\/api\/messages\/(\d+)$/.exec(p);
    if (m) {
      const msg = s.msgs.find((x) => x.id === Number(m![1]));
      if (!msg) return json(404, { error: 'not_found' });
      return json(200, { ...summary(msg), inReplyTo: [], references: [], text: msg.body, attachments: msg.id === 1 ? [{ id: 0, filename: 'plan.pdf', mimeType: 'application/pdf', size: 1000, url: `${BASE}/api/messages/1/attachments/0` }] : [] });
    }
    if (/^\/api\/messages\/\d+\/attachments\/\d+$/.test(p)) return new Response(new Blob(['%PDF-1.4 plan']), { status: 200, headers: { 'Content-Type': 'application/pdf' } });
    if (p === '/api/uploads') {
      const tok = 'srv_' + (s.uploads.size + 1);
      s.uploads.set(tok, decodeURIComponent(h.get('X-Filename') ?? ''));
      return json(201, { uploadToken: tok });
    }
    if (p === '/api/messages/flags' || p === '/api/messages/move') {
      const b = JSON.parse(String(init?.body)) as { ids: number[]; folderId?: number };
      if (b.ids.some((id) => !s.msgs.find((x) => x.id === id))) return json(404, { error: 'not_found', message: 'message not found' });
      if (p === '/api/messages/move') for (const x of s.msgs) if (b.ids.includes(x.id)) x.folder = b.folderId!;
      return new Response(null, { status: 204 });
    }
    const idem = /^\/api\/idempotency\/(.+)$/.exec(p);
    if (idem) {
      const r = s.idem.get(idem[1]);
      return json(200, { state: !r ? 'unknown' : r.status >= 500 ? 'doubt' : 'done' });
    }
    if (p === '/api/send') {
      if (s.hold) await s.hold;
      const key = h.get('Idempotency-Key');
      const body = String(init?.body);
      if (key && s.idem.has(key)) {
        const r = s.idem.get(key)!;
        if (r.status >= 500) return json(409, { error: 'idempotency_in_doubt' });
        return new Response(r.body, { status: r.status, headers: { 'Content-Type': 'application/json', 'Idempotent-Replayed': 'true' } });
      }
      const doc = JSON.parse(body) as { subject: string; attachments?: { uploadToken: string }[]; confirmChecks?: string[] };
      if (s.blockNext && !doc.confirmChecks?.length) {
        s.blockNext = false;
        return json(200, { queued: false, blocked: true, findings: [{ check: 'attachment_missing', severity: 'warn', message: 'Anhang fehlt?' }] });
      }
      for (const a of doc.attachments ?? []) if (!s.uploads.has(a.uploadToken)) return json(400, { error: 'invalid_upload', message: 'unknown upload' });
      s.delivered.push({ key, subject: doc.subject, attachments: doc.attachments ?? [] });
      if (s.fail500) {
        s.fail500 = false;
        if (key) s.idem.set(key, { status: 500, body: '' });
        return json(500, { error: 'internal', message: 'relay failed' });
      }
      const res = JSON.stringify({ queued: false, messageId: 100 + s.delivered.length });
      if (key) s.idem.set(key, { status: 200, body: res });
      if (s.loseNextAnswer) {
        s.loseNextAnswer = false;
        throw new TypeError('connection reset');
      }
      return new Response(res, { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    return json(200, []);
  };
  return { s, fetchImpl };
}

function setup(settings: Partial<OfflineSettings> = {}) {
  const { s, fetchImpl } = fakeServer();
  const db = new MemoryDb();
  const events: CoreEvent[] = [];
  let now = NOW;
  const conf = { ...DEFAULT_SETTINGS, ...settings };
  const core = new OfflineCore({
    db,
    baseUrl: BASE,
    net: fetchImpl,
    token: () => 'tok',
    settings: () => conf,
    texts: { offline: 'Ohne Verbindung nicht verfügbar.', unclear: 'Vielleicht wurde die Mail schon verschickt.' },
    now: () => now,
    timeouts: { read: 1000, write: 1000 },
    onEvent: (e) => events.push(e),
  });
  const get = (path: string) => core.fetch(BASE + path, { headers: { Authorization: 'Bearer tok', Accept: 'application/json' } });
  const post = (path: string, body: unknown, headers: Record<string, string> = {}) =>
    core.fetch(BASE + path, { method: 'POST', headers: { Authorization: 'Bearer tok', 'Content-Type': 'application/json', ...headers }, body: typeof body === 'string' || body instanceof Blob ? body : JSON.stringify(body) });
  const goOffline = () => {
    s.offline = true;
    core.markOffline();
  };
  const goOnline = async () => {
    s.offline = false;
    await core.probe();
  };
  return { s, db, core, events, get, post, goOffline, goOnline, conf, setNow: (t: number) => (now = t) };
}

const mail = (subject: string, extra: Record<string, unknown> = {}) => ({ identityId: 1, to: [{ email: 'ben@example.org' }], cc: [], bcc: [], subject, text: 'Hallo Ben', html: '<p>Hallo Ben</p>', attachments: [], ...extra });

describe('offline store: reading', () => {
  it('keeps what was loaded and serves it without a network, marked as offline', async () => {
    const t = setup();
    expect((await (await t.get('/api/messages/1')).json()).subject).toBe('Projektplan Q3');
    t.goOffline();
    const res = await t.get('/api/messages/1');
    expect(res.status).toBe(200);
    expect(res.headers.get(HEADER_OFFLINE)).toBe('1');
    expect((await res.json()).text).toContain('Budget');
    // Never kept: what is not on the list of routes.
    expect((await t.get('/api/diagnostics')).status).toBe(503);
  });

  it('the query order does not matter, other pages of a folder come from the index', async () => {
    const t = setup();
    await t.get('/api/accounts/1/folders');
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    t.goOffline();
    const same = await (await t.get('/api/messages?limit=50&offset=0&folderId=11&scope=folder')).json();
    expect(same.items).toHaveLength(3);
    const other = await (await t.get('/api/messages?scope=folder&folderId=11&offset=1&limit=1')).json();
    expect(other.items.map((m: { id: number }) => m.id)).toEqual([2]);
    expect(other.total).toBe(3);
    const unified = await (await t.get('/api/messages?scope=unified&role=inbox&offset=0&limit=10')).json();
    expect(unified.items).toHaveLength(3);
  });

  it('searches the loaded mails, also in the content of opened ones', async () => {
    const t = setup();
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    await t.get('/api/messages/1');
    t.goOffline();
    const r = await (await t.post('/api/search', { text: 'budget quartal', mode: 'simple', scopes: [], accountIds: [], folderIds: [] })).json();
    expect(r.hits.map((h: { messageId: number }) => h.messageId)).toEqual([1]);
    expect(r.fallback).toBe(true);
    const bySender = await (await t.post('/api/search', { text: 'maria', accountIds: [], folderIds: [] })).json();
    expect(bySender.total).toBe(3);
    const filtered = await (await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50&q=rechnung')).json();
    expect(filtered.items.map((m: { id: number }) => m.id)).toEqual([2]);
  });

  it('a mail deleted at the server leaves the folder at the next load (delta)', async () => {
    const t = setup();
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    await t.get('/api/messages/2');
    t.s.msgs = t.s.msgs.filter((m) => m.id !== 2);
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    expect(await t.db.getMessage(2)).toBeUndefined();
    expect(await t.db.getEntry('/api/messages/2')).toBeUndefined();
  });

  it('a rejected device token: state "expired", the kept content stays readable', async () => {
    const t = setup();
    await t.get('/api/messages/1');
    t.s.tokenOk = false;
    const res = await t.get('/api/messages/1');
    expect(res.status).toBe(200);
    expect(res.headers.get(HEADER_OFFLINE)).toBe('1');
    expect(t.core.state).toBe('expired');
    expect(t.events.some((e) => e.type === 'state' && e.state === 'expired')).toBe(true);
  });

  it('keeps the store below its limit, least recently used first, the start-up lists stay', async () => {
    const t = setup({ maxBytes: 60 * 1024 * 1024 });
    await t.get('/api/accounts');
    const big = 'x'.repeat(8 * 1024 * 1024); // 16 MiB as UTF-16
    for (let i = 0; i < 5; i++) await t.core.keep(new URL(`${BASE}/api/contacts/${i}`), 'application/json', '', big);
    await t.core.prune();
    expect(await t.core.cacheBytes()).toBeLessThanOrEqual(60 * 1024 * 1024);
    expect(await t.db.getEntry('/api/accounts')).toBeDefined();
    expect(await t.db.getEntry('/api/contacts/4')).toBeDefined();
    expect(await t.db.getEntry('/api/contacts/0')).toBeUndefined();
  });
});

describe('offline store: outbox', () => {
  it('a mail written offline goes out exactly once on reconnecting, with its id as Idempotency-Key', async () => {
    const t = setup();
    t.goOffline();
    const res = await t.post('/api/send', mail('Offline geschrieben'));
    const r = await res.json();
    expect(r).toMatchObject({ queued: true, offline: true });
    const [item] = await t.core.outbox();
    expect(item).toMatchObject({ id: r.outboxId, kind: 'send', state: 'pending', title: 'Offline geschrieben', recipients: 'ben@example.org' });
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toEqual([{ key: r.outboxId, subject: 'Offline geschrieben', attachments: [] }]);
    expect(await t.core.outbox()).toEqual([]);
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
    expect(t.events.some((e) => e.type === 'sent')).toBe(true);
  });

  it('a lost answer: the replay with the same key does not send twice', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Antwort verloren'));
    t.s.offline = false;
    t.s.loseNextAnswer = true;
    await t.core.probe();
    await t.core.flush(); // the server took it, the answer got lost: stays pending
    expect((await t.core.outbox())[0].state).toBe('pending');
    expect(t.core.state).toBe('offline');
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
    expect(await t.core.outbox()).toEqual([]);
  });

  it('online send carries a key; a server error makes it "unclear" and the same mail goes nowhere until decided', async () => {
    const t = setup();
    t.s.fail500 = true;
    const res = await t.post('/api/send', mail('Unklar'));
    expect(res.status).toBe(409);
    const doc = await res.json();
    expect(doc).toMatchObject({ error: 'send_unclear', message: 'Vielleicht wurde die Mail schon verschickt.' });
    expect(t.s.delivered[0].key).toBe(doc.outboxId);
    expect((await t.core.outbox())[0]).toMatchObject({ id: doc.outboxId, state: 'in_doubt' });
    // Pressing send again on the same mail: no second delivery.
    const again = await t.post('/api/send', mail('Unklar'));
    expect(again.status).toBe(409);
    expect(t.s.delivered).toHaveLength(1);
    // "Trotzdem noch einmal senden": a new key, sent once more.
    await t.core.outboxRetry(doc.outboxId);
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(2);
    expect(t.s.delivered[1].key).not.toBe(doc.outboxId);
  });

  it('a network failure while sending online notes the mail with the key already used', async () => {
    const t = setup();
    t.s.loseNextAnswer = true;
    const r = await (await t.post('/api/send', mail('Halb'))).json();
    expect(r.queued).toBe(true);
    const key = t.s.delivered[0].key;
    expect(r.outboxId).toBe(key);
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1); // the replay got the stored answer
    expect(await t.core.outbox()).toEqual([]);
  });

  it('attachments written offline are uploaded first, then the mail goes out', async () => {
    const t = setup();
    t.goOffline();
    const up = await (await t.post('/api/uploads', new Blob(['%PDF data']), { 'X-Filename': encodeURIComponent('Bericht März.pdf'), 'Content-Type': 'application/pdf' })).json();
    expect(up.uploadToken).toMatch(/^offline_/);
    await t.post('/api/send', mail('Mit Anhang', { attachments: [{ uploadToken: up.uploadToken, filename: 'Bericht März.pdf', mimeType: 'application/pdf' }] }));
    await t.goOnline();
    await t.core.flush();
    expect(t.s.uploads.get('srv_1')).toBe('Bericht März.pdf');
    expect(t.s.delivered[0].attachments).toEqual([{ uploadToken: 'srv_1', localToken: up.uploadToken, filename: 'Bericht März.pdf', mimeType: 'application/pdf' }]);
    expect(await t.db.listUploads()).toEqual([]);
  });

  it('too large for the device: refused before it is kept', async () => {
    const t = setup();
    t.goOffline();
    const res = await t.post('/api/uploads', new Blob([new Uint8Array(26 * 1024 * 1024)]), { 'X-Filename': 'gross.bin' });
    expect(res.status).toBe(413);
  });

  it('a mail stopped by the send checks comes back as a draft; "send anyway" confirms the checks', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Geprüft'));
    t.s.blockNext = true;
    await t.goOnline();
    await t.core.flush();
    const [it] = await t.core.outbox();
    expect(it.state).toBe('blocked');
    expect(t.events.some((e) => e.type === 'blocked')).toBe(true);
    expect((await t.core.outboxMessage(it.id)).subject).toBe('Geprüft');
    await t.core.outboxRetry(it.id);
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
  });

  it('marks and moves noted offline show at once and are replayed in order', async () => {
    const t = setup();
    await t.get('/api/accounts/1/folders');
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    t.goOffline();
    expect((await t.post('/api/messages/flags', { ids: [2], add: ['\\Seen'], remove: [] })).status).toBe(204);
    expect((await t.post('/api/messages/move', { ids: [1], folderId: 13 })).status).toBe(204);
    const inbox = await (await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50')).json();
    expect(inbox.items.map((m: { id: number }) => m.id)).toEqual([2, 3]);
    expect(inbox.items[0].flags).toContain('\\Seen');
    const trash = await (await t.get('/api/messages?scope=folder&folderId=13&offset=0&limit=50')).json();
    expect(trash.items.map((m: { id: number }) => m.id)).toEqual([1]);
    const items = await t.core.outbox();
    expect(items.map((i) => [i.kind, i.title])).toEqual([
      ['action', 'Rechnung Oktober'],
      ['action', 'Projektplan Q3'],
    ]);
    await t.goOnline();
    await t.core.flush();
    expect(t.s.calls.filter((c) => c.startsWith('POST'))).toEqual(['POST /api/messages/flags', 'POST /api/messages/move']);
    expect(t.s.msgs.find((m) => m.id === 1)!.folder).toBe(13);
    expect(await t.core.outbox()).toEqual([]);
  });

  it('conflict: the mail was deleted at the server meanwhile - said clearly, not hidden', async () => {
    const t = setup();
    await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
    await t.get('/api/messages/2');
    t.goOffline();
    await t.post('/api/messages/move', { ids: [2], folderId: 13 });
    t.s.msgs = t.s.msgs.filter((m) => m.id !== 2);
    await t.goOnline();
    await t.core.flush();
    const [it] = await t.core.outbox();
    expect(it).toMatchObject({ kind: 'action', state: 'conflict', error: 'gone', title: 'Rechnung Oktober' });
    expect(t.events.find((e) => e.type === 'conflict')).toBeDefined();
    expect(await t.db.getMessage(2)).toBeUndefined();
    await t.core.outboxDiscard(it.id);
    expect(await t.core.outbox()).toEqual([]);
  });

  it('offline: what cannot wait is refused with a sentence for people', async () => {
    const t = setup();
    t.goOffline();
    const res = await t.core.fetch(BASE + '/api/accounts/1/sync', { method: 'POST' });
    expect(res.status).toBe(503);
    expect(await res.json()).toEqual({ error: 'offline', message: 'Ohne Verbindung nicht verfügbar.' });
  });

  it('expired while replaying: the outbox stays untouched', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Wartet'));
    t.s.offline = false;
    t.s.tokenOk = false;
    await t.core.probe();
    expect(t.core.state).toBe('expired');
    await t.core.flush();
    expect((await t.core.outbox())[0].state).toBe('pending');
    expect(t.s.delivered).toEqual([]);
  });
});

describe('outbox: review findings', () => {
  it('dead spots are no attempts: after many network failures the mail is still simply waiting', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Funkloch'));
    for (let i = 0; i < 15; i++) {
      t.s.offline = false;
      await t.core.probe();
      t.s.offline = true;
      await t.core.flush();
    }
    const [it] = await t.core.outbox();
    expect(it).toMatchObject({ state: 'pending', attempts: 0 });
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
  });

  it('"send again" on an unclear mail asks the server first: a key it has done keeps the key, nothing goes out twice', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Schon raus'));
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
    // The client lost track (e.g. an old outbox) and holds it as unclear with the same key.
    const key = t.s.delivered[0].key!;
    await t.db.putOutbox({ id: key, seq: 1, kind: 'send', method: 'POST', path: '/api/send', body: JSON.stringify(mail('Schon raus')), state: 'in_doubt', attempts: 3, createdAt: '' });
    await t.core.outboxRetry(key);
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1);
    expect(t.s.calls).toContain(`GET /api/idempotency/${key}`);
    expect(await t.core.outbox()).toEqual([]);
  });

  it('only a key the server itself holds in doubt gets a new one on "send anyway"', async () => {
    const t = setup();
    t.s.fail500 = true;
    const doc = await (await t.post('/api/send', mail('Zweifel'))).json();
    expect(await t.core.keyStatus(doc.outboxId)).toBe('doubt');
    await t.core.outboxRetry(doc.outboxId);
    await t.core.flush();
    expect(t.s.delivered.map((d) => d.key)).toEqual([doc.outboxId, expect.not.stringMatching(doc.outboxId)]);
  });

  it('offline, "send again" keeps the key: the server answers when the connection is back', async () => {
    const t = setup();
    t.s.fail500 = true;
    const doc = await (await t.post('/api/send', mail('Später'))).json();
    t.goOffline();
    await t.core.outboxRetry(doc.outboxId);
    expect((await t.core.outbox())[0]).toMatchObject({ id: doc.outboxId, state: 'pending' });
    await t.goOnline();
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(1); // the server keeps its doubt: 409, back to the user
    expect((await t.core.outbox())[0].state).toBe('in_doubt');
  });

  it('discarded while on its way: a server error says "maybe sent", nothing comes back', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Unterwegs'));
    let release!: () => void;
    t.s.hold = new Promise((r) => (release = r));
    t.s.fail500 = true;
    t.s.offline = false;
    await t.core.probe();
    const flushing = t.core.flush();
    await new Promise((r) => setTimeout(r, 10));
    const [it] = await t.core.outbox();
    await t.core.outboxDiscard(it.id);
    release();
    await flushing;
    expect(await t.core.outbox()).toEqual([]);
    expect(t.events).toContainEqual({ type: 'discarded', id: it.id, outcome: 'unclear' });
  });
});

it('revoked by the server: what is readable can be wiped, waiting mails stay for the next sign-in', async () => {
  const t = setup();
  await t.get('/api/messages?scope=folder&folderId=11&offset=0&limit=50');
  await t.get('/api/messages/1');
  t.goOffline();
  await t.post('/api/send', mail('Bleibt'));
  t.s.offline = false;
  t.s.tokenOk = false;
  await t.core.probe();
  expect(t.events).toContainEqual({ type: 'state', state: 'expired', was: 'offline' });
  await t.core.clearCache(); // bridge.ts on "expired"
  expect(await t.db.listEntryMetas()).toEqual([]);
  expect(await t.db.allMessages()).toEqual([]);
  expect((await t.core.outbox()).map((i) => i.title)).toEqual(['Bleibt']);
});

describe('outbox: second review', () => {
  it('the server holds the key in doubt (e.g. a run that died): "send anyway" takes a new key, no endless loop', async () => {
    const t = setup();
    t.s.fail500 = true;
    const doc = await (await t.post('/api/send', mail('Abgestürzt'))).json();
    // A status that (wrongly) still says "pending": the POST's in_doubt answer decides from then on.
    const net = (t.core as unknown as { net: (...a: unknown[]) => Promise<unknown> }).net.bind(t.core);
    (t.core as unknown as { net: unknown }).net = async (m: string, p: string, ...rest: unknown[]) =>
      p.startsWith('/api/idempotency/') ? { status: 200, body: '{"state":"pending"}', headers: new Headers() } : net(m, p, ...rest);
    await t.core.outboxRetry(doc.outboxId);
    await t.core.flush();
    expect((await t.core.outbox())[0]).toMatchObject({ id: doc.outboxId, state: 'in_doubt' });
    await t.core.outboxRetry(doc.outboxId);
    await t.core.flush();
    expect(t.s.delivered).toHaveLength(2);
    expect(await t.core.outbox()).toEqual([]);
  });

  it('discarded on its way and the answer got lost: once connected the server is asked and the user told it went out', async () => {
    const t = setup();
    t.goOffline();
    await t.post('/api/send', mail('Trotzdem raus'));
    let release!: () => void;
    t.s.hold = new Promise((r) => (release = r));
    t.s.loseNextAnswer = true;
    t.s.offline = false;
    await t.core.probe();
    const flushing = t.core.flush();
    await new Promise((r) => setTimeout(r, 10));
    const [it] = await t.core.outbox();
    await t.core.outboxDiscard(it.id);
    release();
    await flushing;
    expect(t.s.delivered).toHaveLength(1);
    expect(t.events.some((e) => e.type === 'discarded')).toBe(false); // nothing known yet
    await t.goOnline();
    await t.core.flush();
    expect(t.events).toContainEqual({ type: 'discarded', id: it.id, outcome: 'sent' });
    expect(await t.core.outbox()).toEqual([]);
    await t.core.flush();
    expect(t.events.filter((e) => e.type === 'discarded')).toHaveLength(1); // told once
  });
});

describe('headers-only rule (mail.headersOnlyOlderThanDays)', () => {
  it('decides by the date of the mail; empty or 0 means everything in full', () => {
    expect(fullContentAllowed(new Date(NOW - 10 * DAY).toISOString(), 30, NOW)).toBe(true);
    expect(fullContentAllowed(new Date(NOW - 31 * DAY).toISOString(), 30, NOW)).toBe(false);
    expect(fullContentAllowed(new Date(NOW - 3000 * DAY).toISOString(), null, NOW)).toBe(true);
    expect(fullContentAllowed('kaputt', 30, NOW)).toBe(true);
    expect(parseDays('')).toBeNull();
    expect(parseDays('0')).toBeNull();
    expect(parseDays(' 90 ')).toBe(90);
    expect(parseDays('-3')).toBeNull();
    expect(parseDays('abc')).toBeNull();
  });

  it('is stored under the shared name and read back', () => {
    const raw = settingsToJson({ ...DEFAULT_SETTINGS, headersOnlyOlderThanDays: 60, network: 'wifi' });
    expect(JSON.parse(raw)[HEADERS_ONLY_KEY]).toBe(60);
    expect(settingsFromJson(raw)).toMatchObject({ headersOnlyOlderThanDays: 60, network: 'wifi' });
    expect(settingsFromJson(null)).toEqual(DEFAULT_SETTINGS);
    expect(settingsFromJson(JSON.stringify({ [HEADERS_ONLY_KEY]: 0, 'offline.intervalMinutes': 5 }))).toMatchObject({ headersOnlyOlderThanDays: null, intervalMinutes: 15 });
  });

  it('synchronising loads content and attachments only of newer mails; older ones show their header lines offline', async () => {
    const t = setup({ headersOnlyOlderThanDays: 30, attachmentMaxBytes: 2 * 1024 * 1024 });
    await t.core.sync();
    expect(t.s.calls).toContain('GET /api/messages/1');
    expect(t.s.calls).toContain('GET /api/messages/2');
    expect(t.s.calls).not.toContain('GET /api/messages/3');
    expect(t.s.calls).toContain('GET /api/messages/1/attachments/0');
    t.goOffline();
    const old = await (await t.get('/api/messages/3')).json();
    expect(old).toMatchObject({ id: 3, subject: 'Altes Protokoll', offlineHeadersOnly: true, attachments: [] });
    const recent = await (await t.get('/api/messages/1')).json();
    expect(recent.offlineHeadersOnly).toBeUndefined();
    expect(recent.text).toContain('Budget');
    const att = await t.get('/api/messages/1/attachments/0');
    expect(await (await att.blob()).text()).toBe('%PDF-1.4 plan');
  });

  it('without the option everything is loaded in full (default)', async () => {
    const t = setup();
    await t.core.sync();
    expect(t.s.calls).toContain('GET /api/messages/3');
  });

  it('an older mail opened online stays a week after the last look, then only its header lines remain', async () => {
    const t = setup({ headersOnlyOlderThanDays: 30 });
    await t.core.sync();
    await t.get('/api/messages/3'); // opened online
    await t.core.prune();
    expect(await t.db.getEntry('/api/messages/3')).toBeDefined();
    t.setNow(NOW + 8 * DAY);
    await t.core.prune();
    expect(await t.db.getEntry('/api/messages/3')).toBeUndefined();
    expect(await t.db.getEntry('/api/messages/1')).toBeDefined(); // 9 days old: still within 30
    t.goOffline();
    expect((await (await t.get('/api/messages/3')).json()).offlineHeadersOnly).toBe(true);
  });

  it('with "Wi-Fi only" on mobile data only the lists are refreshed', async () => {
    const { s, fetchImpl } = fakeServer();
    const core = new OfflineCore({ db: new MemoryDb(), baseUrl: BASE, net: fetchImpl, token: () => 't', settings: () => ({ ...DEFAULT_SETTINGS, network: 'wifi' }), unmetered: async () => false, texts: { offline: '', unclear: '' }, now: () => NOW });
    await core.sync();
    expect(s.calls).toContain('GET /api/messages?scope=unified&role=inbox&offset=0&limit=100');
    expect(s.calls.some((c) => /^GET \/api\/messages\/\d+$/.test(c))).toBe(false);
  });
});

describe('staged answers of the background work', () => {
  it('an older background answer does not replace a newer one', async () => {
    const t = setup();
    await t.get('/api/accounts'); // stored now
    await t.core.importStaged([{ key: '/api/accounts', body: '[{"id":99}]', storedAt: NOW - DAY }]);
    t.goOffline();
    expect(await (await t.get('/api/accounts')).json()).toEqual([{ id: 1 }]);
  });

  it('are taken over into the store', async () => {
    const t = setup();
    const n = await t.core.importStaged([
      { key: '/api/accounts', body: '[{"id":1}]', storedAt: NOW },
      { key: '/api/auth/sessions', body: '[]' }, // never kept
    ]);
    expect(n).toBe(1);
    t.goOffline();
    expect(await (await t.get('/api/accounts')).json()).toEqual([{ id: 1 }]);
    expect((await t.core.lastTimes()).lastDataSyncAt).toBe(NOW);
  });
});

it('the outbox shows a waiting action with what it does', async () => {
  const { outboxEntries } = await import('./rules');
  const items: StoredOutboxItem[] = [{ id: 'out_1', seq: 1, kind: 'action', method: 'POST', path: '/api/messages/move', body: '{"ids":[1],"folderId":13}', title: 'X', state: 'conflict', error: 'gone', attempts: 1, createdAt: '' }];
  expect(outboxEntries(items)[0]).toMatchObject({ action: 'move', state: 'conflict', error: 'gone' });
});
