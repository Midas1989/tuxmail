// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Storage of the Android app's offline store: one IndexedDB database per
// server profile ("tuxmail-offline-<profileId>") in the WebView's app-private
// data directory. Why IndexedDB and not an SQLite plugin: see
// docs/security/BASIS-ABWEICHUNGEN.md (Android offline store) - no native
// dependency, the same code runs in the tests and in the browser round trip,
// and an SQLCipher plugin could not use the Keystore key of the profile
// store directly anyway (that key never leaves the Keystore).
//
// Kept answers are split into a small meta record (size, last access) and the
// body, so measuring and cleaning up never has to read the bodies.

export interface EntryMeta {
  key: string;
  path: string;
  size: number;
  /** Last access (ms). */
  at: number;
  storedAt: number;
  ct?: string;
  disp?: string;
  /** Detail, attachment or inline part of this message. */
  msgId?: number;
  /** Date of that message (headers-only rule). */
  msgDate?: string;
}

export type Body = string | Blob;

export interface IndexedMessage {
  id: number;
  accountId: number;
  folderIds: number[];
  date: string;
  /** MessageSummary as JSON. */
  summary: string;
  search: string;
  /** The full detail is kept as well. */
  hasBody?: boolean;
}

export interface StoredUpload {
  token: string;
  filename: string;
  mime: string;
  size: number;
  data: Blob;
  createdAt: number;
}

export interface OutboxRecord {
  id: string;
  seq: number;
}

export interface OfflineDb {
  getEntry(key: string): Promise<{ meta: EntryMeta; body: Body } | undefined>;
  putEntry(meta: EntryMeta, body: Body): Promise<void>;
  touch(key: string, at: number): Promise<void>;
  deleteEntries(keys: string[]): Promise<void>;
  listEntryMetas(): Promise<EntryMeta[]>;
  /** Kept bodies whose key starts with `prefix`, newest first. */
  listBodies(prefix: string, limit: number): Promise<{ meta: EntryMeta; body: Body }[]>;

  putMessages(msgs: IndexedMessage[]): Promise<void>;
  getMessage(id: number): Promise<IndexedMessage | undefined>;
  deleteMessages(ids: number[]): Promise<void>;
  allMessages(): Promise<IndexedMessage[]>;

  listOutbox<T extends OutboxRecord>(): Promise<T[]>;
  putOutbox<T extends OutboxRecord>(item: T): Promise<void>;
  deleteOutbox(id: string): Promise<void>;

  putUpload(u: StoredUpload): Promise<void>;
  getUpload(token: string): Promise<StoredUpload | undefined>;
  listUploads(): Promise<Omit<StoredUpload, 'data'>[]>;
  deleteUploads(tokens: string[]): Promise<void>;

  getMeta<T>(k: string): Promise<T | undefined>;
  setMeta(k: string, v: unknown): Promise<void>;

  /** Deletes everything (the database stays). */
  clear(): Promise<void>;
}

export function bodySize(body: Body): number {
  return typeof body === 'string' ? body.length * 2 : body.size;
}

// ---------------------------------------------------------------------------
// In memory (tests)
// ---------------------------------------------------------------------------

export class MemoryDb implements OfflineDb {
  entries = new Map<string, EntryMeta>();
  bodies = new Map<string, Body>();
  messages = new Map<number, IndexedMessage>();
  outbox = new Map<string, OutboxRecord>();
  uploads = new Map<string, StoredUpload>();
  meta = new Map<string, unknown>();

  async getEntry(key: string) {
    const meta = this.entries.get(key);
    const body = this.bodies.get(key);
    return meta && body !== undefined ? { meta: { ...meta }, body } : undefined;
  }
  async putEntry(meta: EntryMeta, body: Body) {
    this.entries.set(meta.key, { ...meta });
    this.bodies.set(meta.key, body);
  }
  async touch(key: string, at: number) {
    const m = this.entries.get(key);
    if (m) m.at = at;
  }
  async deleteEntries(keys: string[]) {
    for (const k of keys) {
      this.entries.delete(k);
      this.bodies.delete(k);
    }
  }
  async listEntryMetas() {
    return [...this.entries.values()].map((m) => ({ ...m }));
  }
  async listBodies(prefix: string, limit: number) {
    return [...this.entries.values()]
      .filter((m) => m.key.startsWith(prefix))
      .sort((a, b) => b.storedAt - a.storedAt)
      .slice(0, limit)
      .map((meta) => ({ meta: { ...meta }, body: this.bodies.get(meta.key)! }));
  }
  async putMessages(msgs: IndexedMessage[]) {
    for (const m of msgs) this.messages.set(m.id, { ...m });
  }
  async getMessage(id: number) {
    const m = this.messages.get(id);
    return m ? { ...m } : undefined;
  }
  async deleteMessages(ids: number[]) {
    for (const id of ids) this.messages.delete(id);
  }
  async allMessages() {
    return [...this.messages.values()].map((m) => ({ ...m }));
  }
  async listOutbox<T extends OutboxRecord>() {
    return [...this.outbox.values()].sort((a, b) => a.seq - b.seq).map((x) => structuredClone(x) as T);
  }
  async putOutbox<T extends OutboxRecord>(item: T) {
    this.outbox.set(item.id, structuredClone(item));
  }
  async deleteOutbox(id: string) {
    this.outbox.delete(id);
  }
  async putUpload(u: StoredUpload) {
    this.uploads.set(u.token, u);
  }
  async getUpload(token: string) {
    return this.uploads.get(token);
  }
  async listUploads() {
    return [...this.uploads.values()].map(({ data: _d, ...rest }) => rest);
  }
  async deleteUploads(tokens: string[]) {
    for (const t of tokens) this.uploads.delete(t);
  }
  async getMeta<T>(k: string) {
    return this.meta.get(k) as T | undefined;
  }
  async setMeta(k: string, v: unknown) {
    this.meta.set(k, v);
  }
  async clear() {
    for (const m of [this.entries, this.bodies, this.messages, this.outbox, this.uploads, this.meta]) m.clear();
  }
}

// ---------------------------------------------------------------------------
// IndexedDB (the app)
// ---------------------------------------------------------------------------

export const DB_PREFIX = 'tuxmail-offline-';
const STORES = ['entries', 'bodies', 'messages', 'outbox', 'uploads', 'meta'] as const;
type StoreName = (typeof STORES)[number];

function req<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

function done(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error ?? new Error('transaction aborted'));
  });
}

export function dbName(profileId: string): string {
  return DB_PREFIX + profileId.replace(/[^A-Za-z0-9_-]/g, '_');
}

export async function openIdb(profileId: string, factory: IDBFactory = indexedDB): Promise<IdbDb> {
  const open = factory.open(dbName(profileId), 1);
  open.onupgradeneeded = () => {
    const db = open.result;
    db.createObjectStore('entries', { keyPath: 'key' });
    db.createObjectStore('bodies');
    db.createObjectStore('messages', { keyPath: 'id' });
    db.createObjectStore('outbox', { keyPath: 'id' });
    db.createObjectStore('uploads', { keyPath: 'token' });
    db.createObjectStore('meta');
  };
  return new IdbDb(await req(open));
}

/** A store of another profile that still holds waiting mails or actions. */
export interface ForeignStore {
  name: string;
  profileId: string;
  waiting: { id: string; kind: string; title?: string; recipients?: string; state?: string }[];
}

/**
 * Stores of every profile except `keep`: what was kept for reading goes, a
 * store without waiting items goes entirely. Waiting items are never deleted
 * without the user's confirmation: their stores stay (reading data removed)
 * and are returned, to be listed and discarded on purpose.
 */
export async function sweepOtherStores(keep: string | null, factory: IDBFactory = indexedDB): Promise<ForeignStore[]> {
  const list = typeof factory.databases === 'function' ? await factory.databases().catch(() => []) : [];
  const keepName = keep ? dbName(keep) : null;
  const out: ForeignStore[] = [];
  for (const d of list) {
    if (!d.name?.startsWith(DB_PREFIX) || d.name === keepName) continue;
    const profileId = d.name.slice(DB_PREFIX.length);
    let db: IdbDb | null = null;
    try {
      db = await openIdb(profileId, factory);
      const waiting = await db.listOutbox<OutboxRecord & ForeignStore['waiting'][number]>();
      if (!waiting.length) {
        db.db.close();
        await deleteStore(d.name, factory);
        continue;
      }
      await db.clearReading();
      out.push({ name: d.name, profileId, waiting: waiting.map(({ id, kind, title, recipients, state }) => ({ id, kind, title, recipients, state })) });
    } catch {
      /* unreadable: leave it */
    } finally {
      db?.db.close();
    }
  }
  return out;
}

export function deleteStore(name: string, factory: IDBFactory = indexedDB): Promise<void> {
  return new Promise((resolve) => {
    const r = factory.deleteDatabase(name);
    r.onsuccess = r.onerror = r.onblocked = () => resolve();
  });
}

export class IdbDb implements OfflineDb {
  constructor(readonly db: IDBDatabase) {}

  private tx(stores: StoreName | StoreName[], mode: IDBTransactionMode = 'readonly') {
    return this.db.transaction(stores, mode);
  }

  async getEntry(key: string) {
    const tx = this.tx(['entries', 'bodies']);
    const [meta, body] = await Promise.all([req<EntryMeta | undefined>(tx.objectStore('entries').get(key)), req<Body | undefined>(tx.objectStore('bodies').get(key))]);
    return meta && body !== undefined ? { meta, body } : undefined;
  }
  async putEntry(meta: EntryMeta, body: Body) {
    const tx = this.tx(['entries', 'bodies'], 'readwrite');
    tx.objectStore('entries').put(meta);
    tx.objectStore('bodies').put(body, meta.key);
    await done(tx);
  }
  async touch(key: string, at: number) {
    const tx = this.tx('entries', 'readwrite');
    const s = tx.objectStore('entries');
    const m = await req<EntryMeta | undefined>(s.get(key));
    if (m) s.put({ ...m, at });
    await done(tx);
  }
  async deleteEntries(keys: string[]) {
    if (!keys.length) return;
    const tx = this.tx(['entries', 'bodies'], 'readwrite');
    for (const k of keys) {
      tx.objectStore('entries').delete(k);
      tx.objectStore('bodies').delete(k);
    }
    await done(tx);
  }
  async listEntryMetas() {
    return req<EntryMeta[]>(this.tx('entries').objectStore('entries').getAll());
  }
  async listBodies(prefix: string, limit: number) {
    const range = IDBKeyRange.bound(prefix, prefix + '￿');
    const tx = this.tx(['entries', 'bodies']);
    const metas = (await req<EntryMeta[]>(tx.objectStore('entries').getAll(range))).sort((a, b) => b.storedAt - a.storedAt).slice(0, limit);
    const bodies = await Promise.all(metas.map((m) => req<Body | undefined>(tx.objectStore('bodies').get(m.key))));
    return metas.flatMap((meta, i) => (bodies[i] !== undefined ? [{ meta, body: bodies[i]! }] : []));
  }
  async putMessages(msgs: IndexedMessage[]) {
    if (!msgs.length) return;
    const tx = this.tx('messages', 'readwrite');
    for (const m of msgs) tx.objectStore('messages').put(m);
    await done(tx);
  }
  async getMessage(id: number) {
    return req<IndexedMessage | undefined>(this.tx('messages').objectStore('messages').get(id));
  }
  async deleteMessages(ids: number[]) {
    if (!ids.length) return;
    const tx = this.tx('messages', 'readwrite');
    for (const id of ids) tx.objectStore('messages').delete(id);
    await done(tx);
  }
  async allMessages() {
    return req<IndexedMessage[]>(this.tx('messages').objectStore('messages').getAll());
  }
  async listOutbox<T extends OutboxRecord>() {
    return (await req<T[]>(this.tx('outbox').objectStore('outbox').getAll())).sort((a, b) => a.seq - b.seq);
  }
  async putOutbox<T extends OutboxRecord>(item: T) {
    const tx = this.tx('outbox', 'readwrite');
    tx.objectStore('outbox').put(item);
    await done(tx);
  }
  async deleteOutbox(id: string) {
    const tx = this.tx('outbox', 'readwrite');
    tx.objectStore('outbox').delete(id);
    await done(tx);
  }
  async putUpload(u: StoredUpload) {
    const tx = this.tx('uploads', 'readwrite');
    tx.objectStore('uploads').put(u);
    await done(tx);
  }
  async getUpload(token: string) {
    return req<StoredUpload | undefined>(this.tx('uploads').objectStore('uploads').get(token));
  }
  async listUploads() {
    return (await req<StoredUpload[]>(this.tx('uploads').objectStore('uploads').getAll())).map(({ data: _d, ...rest }) => rest);
  }
  async deleteUploads(tokens: string[]) {
    if (!tokens.length) return;
    const tx = this.tx('uploads', 'readwrite');
    for (const t of tokens) tx.objectStore('uploads').delete(t);
    await done(tx);
  }
  async getMeta<T>(k: string) {
    return req<T | undefined>(this.tx('meta').objectStore('meta').get(k));
  }
  async setMeta(k: string, v: unknown) {
    const tx = this.tx('meta', 'readwrite');
    tx.objectStore('meta').put(v, k);
    await done(tx);
  }
  /** Everything kept for reading goes; waiting items and their attachments stay. */
  async clearReading() {
    const tx = this.tx(['entries', 'bodies', 'messages', 'meta'], 'readwrite');
    for (const s of ['entries', 'bodies', 'messages', 'meta'] as const) tx.objectStore(s).clear();
    await done(tx);
  }
  async clear() {
    const tx = this.tx([...STORES], 'readwrite');
    for (const s of STORES) tx.objectStore(s).clear();
    await done(tx);
  }
}
