// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { HttpTransport } from './api/http';
import { clearTuxmailStorage, platformHttpOptions, sessionLost, signOutWeb } from './session';

function seed() {
  localStorage.setItem('tuxmail.pwdelivery.v1', JSON.stringify({ 'anna@example.org': 'link' }));
  localStorage.setItem('tuxmail.prefs.v2', '{}');
  localStorage.setItem('tuxmail.cloud.lastSave.v1', '{}');
  localStorage.setItem('tuxmail.fullMail.12', '1');
  localStorage.setItem('unrelated.key', 'stay');
  sessionStorage.setItem('tuxmail.v1.something', 'x');
  sessionStorage.setItem('other', 'stay');
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});

describe('clearTuxmailStorage (NG-07)', () => {
  it('removes all tuxmail.* keys from localStorage and sessionStorage, nothing else', () => {
    seed();
    expect(clearTuxmailStorage()).toBe(5);
    expect(Object.keys(localStorage)).toEqual(['unrelated.key']);
    expect(Object.keys(sessionStorage)).toEqual(['other']);
  });

  it('can keep named keys (mirror of the newly activated profile)', () => {
    seed();
    localStorage.setItem('tuxmail.native.active', '{}');
    clearTuxmailStorage(undefined, ['tuxmail.native.active']);
    expect(localStorage.getItem('tuxmail.native.active')).toBe('{}');
    expect(localStorage.getItem('tuxmail.pwdelivery.v1')).toBeNull();
  });

  it('survives blocked storage', () => {
    const broken = { length: 1, key: () => { throw new Error('blocked'); }, removeItem: () => {} } as unknown as Storage;
    expect(() => clearTuxmailStorage([broken])).not.toThrow();
  });
});

describe('signOutWeb', () => {
  it('logs out through the API, clears local data, then reloads', async () => {
    seed();
    const order: string[] = [];
    const api = { logout: vi.fn(async () => void order.push('logout')) };
    await signOutWeb({ api, reload: () => order.push('reload'), clear: () => { order.push('clear'); clearTuxmailStorage(); } });
    expect(order).toEqual(['logout', 'clear', 'reload']);
    expect(localStorage.getItem('tuxmail.pwdelivery.v1')).toBeNull();
  });

  it('clears local data even if the logout request fails, but does not reload', async () => {
    seed();
    const reload = vi.fn();
    const api = { logout: vi.fn(async () => { throw new Error('offline'); }) };
    await expect(signOutWeb({ api, reload })).rejects.toThrow('offline');
    expect(reload).not.toHaveBeenCalled();
    expect(localStorage.getItem('tuxmail.pwdelivery.v1')).toBeNull();
  });

  it('HttpTransport.logout posts to /api/auth/logout', async () => {
    const fetchImpl = vi.fn(async () => new Response(null, { status: 204 }));
    const api = new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch });
    await api.logout();
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/auth/logout');
    expect(init.method).toBe('POST');
  });
});

describe('401 ends the session (NG-07)', () => {
  it('sessionLost clears the data and reloads once per 30 s (no reload loop)', () => {
    seed();
    const reload = vi.fn();
    expect(sessionLost(reload)).toBe(true);
    expect(localStorage.getItem('tuxmail.prefs.v2')).toBeNull();
    seed();
    expect(sessionLost(reload)).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem('tuxmail.prefs.v2')).toBeNull(); // still cleared
  });

  function transport(status: number, body: unknown, onUnauthorized: () => void, authToken?: () => string | undefined) {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
    return new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch, onUnauthorized, authToken });
  }

  it('cookie session: "unauthenticated" triggers the handler, a wrong password prompt does not', async () => {
    const handler = vi.fn();
    await expect(transport(401, { error: 'unauthenticated', message: 'login required' }, handler).listAccounts()).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
    await expect(transport(401, { error: 'invalid_credentials', message: 'wrong password' }, handler).listAccounts()).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('device token: any 401 triggers the handler (unchanged)', async () => {
    const handler = vi.fn();
    await expect(transport(401, {}, handler, () => 'tok').listAccounts()).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('the default web transport options carry the session-loss handler', () => {
    expect(typeof platformHttpOptions().onUnauthorized).toBe('function');
  });
});
