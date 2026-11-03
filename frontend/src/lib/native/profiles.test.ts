// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { ACTIVE_KEY, PROFILES_KEY, ProfileStore, profileLabel, tokenKey, type KeyValueStore, type ServerProfile } from './profiles';
import { ACTIVE_MIRROR_KEY, hasNativeSession, nativeHttpOptions, readActiveMirror, setSessionToken, setUnauthorizedHandler, writeActiveMirror } from './session';

function memory(): KeyValueStore & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    get: async (k) => data.get(k) ?? null,
    set: async (k, v) => void data.set(k, v),
    remove: async (k) => void data.delete(k),
  };
}

function memoryStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

describe('ProfileStore', () => {
  it('stores profile metadata in preferences and the token only in secure storage', async () => {
    const prefs = memory();
    const secure = memory();
    const mirrored: (ServerProfile | null)[] = [];
    const store = new ProfileStore(prefs, secure, (p) => mirrored.push(p));
    const p = await store.upsert({ serverUrl: 'https://mail.example.org', username: 'anna', sessionId: 3 }, 'secret-token');
    expect(p.label).toBe('mail.example.org');
    expect(prefs.data.get(PROFILES_KEY)).not.toContain('secret-token');
    expect(secure.data.get(tokenKey(p.id))).toBe('secret-token');
    expect(prefs.data.get(ACTIVE_KEY)).toBe(p.id);
    expect(await store.active()).toMatchObject({ id: p.id, serverUrl: 'https://mail.example.org', needsLogin: false });
    expect(await store.token(p.id)).toBe('secret-token');
    expect(mirrored.at(-1)?.id).toBe(p.id);
  });

  it('keeps several servers and updates an existing one in place', async () => {
    const store = new ProfileStore(memory(), memory(), () => {});
    const a = await store.upsert({ serverUrl: 'https://a.example', username: 'anna' }, 't1');
    const b = await store.upsert({ serverUrl: 'https://b.example', username: 'ben' }, 't2');
    const a2 = await store.upsert({ serverUrl: 'https://a.example', username: 'anna', displayName: 'Anna' }, 't3');
    expect(a2.id).toBe(a.id);
    expect(a2.createdAt).toBe(a.createdAt);
    expect((await store.list()).map((p) => p.serverUrl).sort()).toEqual(['https://a.example', 'https://b.example']);
    expect(await store.token(a.id)).toBe('t3');
    expect((await store.active())?.id).toBe(a.id);
    await store.activate(b);
    expect((await store.active())?.id).toBe(b.id);
  });

  it('markNeedsLogin forgets the token but keeps the server', async () => {
    const secure = memory();
    const store = new ProfileStore(memory(), secure, () => {});
    const p = await store.upsert({ serverUrl: 'https://a.example' }, 't1');
    await store.markNeedsLogin(p.id);
    expect(await store.token(p.id)).toBeNull();
    expect((await store.active())?.needsLogin).toBe(true);
  });

  it('remove deletes token and deactivates', async () => {
    const mirrored: (ServerProfile | null)[] = [];
    const store = new ProfileStore(memory(), memory(), (p) => mirrored.push(p));
    const p = await store.upsert({ serverUrl: 'https://a.example' }, 't1');
    await store.remove(p.id);
    expect(await store.list()).toEqual([]);
    expect(await store.active()).toBeNull();
    expect(await store.token(p.id)).toBeNull();
    expect(mirrored.at(-1)).toBeNull();
  });

  it('survives corrupt preference data', async () => {
    const prefs = memory();
    prefs.data.set(PROFILES_KEY, '{nope');
    expect(await new ProfileStore(prefs, memory(), () => {}).list()).toEqual([]);
    prefs.data.set(PROFILES_KEY, JSON.stringify([{ id: 1 }, null, { id: 'x', serverUrl: 'https://x' }]));
    expect((await new ProfileStore(prefs, memory(), () => {}).list()).map((p) => p.id)).toEqual(['x']);
  });

  it('labels by host', () => {
    expect(profileLabel('https://mail.example.org:8443/tux')).toBe('mail.example.org:8443');
    expect(profileLabel('nope')).toBe('nope');
  });
});

describe('session mirror', () => {
  it('round-trips the active profile and drives the HTTP options', () => {
    const s = memoryStorage();
    expect(hasNativeSession(s)).toBe(false);
    writeActiveMirror({ profileId: 'p1', serverUrl: 'https://a.example' }, s);
    expect(readActiveMirror(s)).toEqual({ profileId: 'p1', serverUrl: 'https://a.example' });
    expect(hasNativeSession(s)).toBe(true);
    setSessionToken('tok');
    let called = 0;
    setUnauthorizedHandler(() => called++);
    const o = nativeHttpOptions(s);
    expect(o.baseUrl).toBe('https://a.example');
    expect(o.authToken?.()).toBe('tok');
    o.onUnauthorized?.();
    expect(called).toBe(1);
    writeActiveMirror(null, s);
    expect(s.getItem(ACTIVE_MIRROR_KEY)).toBeNull();
    setSessionToken(undefined);
    setUnauthorizedHandler(undefined);
  });

  it('ignores malformed mirrors', () => {
    const s = memoryStorage();
    s.setItem(ACTIVE_MIRROR_KEY, '{"serverUrl":""}');
    expect(readActiveMirror(s)).toBeNull();
    s.setItem(ACTIVE_MIRROR_KEY, 'garbage');
    expect(readActiveMirror(s)).toBeNull();
  });
});
