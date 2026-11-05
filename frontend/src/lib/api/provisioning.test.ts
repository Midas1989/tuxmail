// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { HttpTransport } from './http';

interface Call {
  method: string;
  url: string;
  auth?: string;
}

function remote(opts: { profileStatus?: number; profileThrows?: boolean; logoutThrows?: boolean } = {}) {
  const calls: Call[] = [];
  const fetchImpl = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url);
    const headers = (init?.headers ?? {}) as Record<string, string>;
    calls.push({ method: init?.method ?? 'GET', url: u, auth: headers.Authorization });
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    if (u.endsWith('/api/device/login') || u.endsWith('/api/device/token')) return json(200, { access_token: 'remote-device-token' });
    if (u.endsWith('/api/provisioning/profile')) {
      if (opts.profileThrows) throw new Error('offline');
      return json(opts.profileStatus ?? 200, { user: { username: 'anna' }, server: { version: '1' }, accounts: [], identities: [], signatures: [], templates: [] });
    }
    if (u.endsWith('/api/auth/logout')) {
      if (opts.logoutThrows) throw new Error('offline');
      return new Response(null, { status: 204 });
    }
    return json(404, {});
  }) as unknown as typeof fetch;
  return { calls, api: new HttpTransport({ fetchImpl }) };
}

const logouts = (calls: Call[]) => calls.filter((c) => c.url.endsWith('/api/auth/logout'));

describe('web provisioning revokes the remote device token (NG-04)', () => {
  it('password login: logout with the bearer after the profile was loaded', async () => {
    const { api, calls } = remote();
    const r = await api.serverLogin('https://mail.example.org', { username: 'anna', password: 'pw' });
    expect(r.status).toBe('approved');
    const out = logouts(calls);
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ method: 'POST', url: 'https://mail.example.org/api/auth/logout', auth: 'Bearer remote-device-token' });
    // The revoke happens after the single use of the token.
    expect(calls.findIndex((c) => c.url.endsWith('/api/provisioning/profile'))).toBeLessThan(calls.findIndex((c) => c.url.endsWith('/api/auth/logout')));
  });

  it('device flow: logout after approval', async () => {
    const { api, calls } = remote();
    const r = await api.deviceToken('https://mail.example.org', 'dc');
    expect(r.status).toBe('approved');
    expect(logouts(calls)).toHaveLength(1);
  });

  it('also revokes when the profile request is denied', async () => {
    const { api, calls } = remote({ profileStatus: 403 });
    const r = await api.serverLogin('https://mail.example.org', { username: 'anna', password: 'pw' });
    expect(r.status).toBe('denied');
    expect(logouts(calls)).toHaveLength(1);
  });

  it('also revokes when the profile request fails with a network error', async () => {
    const { api, calls } = remote({ profileThrows: true });
    await expect(api.serverLogin('https://mail.example.org', { username: 'anna', password: 'pw' })).rejects.toMatchObject({ code: 'network' });
    expect(logouts(calls)).toHaveLength(1);
  });

  it('a failing revoke does not break the flow', async () => {
    const { api } = remote({ logoutThrows: true });
    const r = await api.serverLogin('https://mail.example.org', { username: 'anna', password: 'pw' });
    expect(r.status).toBe('approved');
  });

  it('the token is not part of the result', async () => {
    const { api } = remote();
    const r = await api.serverLogin('https://mail.example.org', { username: 'anna', password: 'pw' });
    expect(JSON.stringify(r)).not.toContain('remote-device-token');
  });
});
