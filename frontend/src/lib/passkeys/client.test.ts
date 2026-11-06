// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/api';
import { createPasskeyClient, HttpPasskeyClient } from './client';

function fakeFetch(status: number, body?: unknown) {
  return vi.fn(async (_url: string | URL | Request, _init?: RequestInit) =>
    new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status, headers: { 'Content-Type': 'application/json' } }),
  ) as unknown as typeof fetch & ReturnType<typeof vi.fn>;
}

describe('HttpPasskeyClient', () => {
  it('uses the right routes, methods and bodies with the cookie session', async () => {
    const f = fakeFetch(200, { passkeys: [], enabled: true, max: 10, reauth: { password: true, code: false } });
    const c = new HttpPasskeyClient({ fetchImpl: f, baseUrl: 'https://mail.example.org/' });
    await c.list();
    await c.beginRegistration({ password: 'pw' });
    await c.rename(4, 'New');
    await c.remove(4, { password: 'pw', code: '123456' });
    const calls = (f as unknown as { mock: { calls: [string, RequestInit][] } }).mock.calls;
    expect(calls.map(([u, i]) => `${i.method} ${u}`)).toEqual([
      'GET https://mail.example.org/api/auth/passkeys',
      'POST https://mail.example.org/api/auth/passkeys/register/begin',
      'PATCH https://mail.example.org/api/auth/passkeys/4',
      'DELETE https://mail.example.org/api/auth/passkeys/4',
    ]);
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ password: 'pw' });
    expect(JSON.parse(calls[2][1].body as string)).toEqual({ name: 'New' });
    expect(JSON.parse(calls[3][1].body as string)).toEqual({ password: 'pw', code: '123456' });
    expect(calls[0][1].body).toBeUndefined();
    for (const [, init] of calls) expect(init.credentials).toBe('same-origin');
  });

  it('finish returns the passkey of the response', async () => {
    const f = fakeFetch(201, { passkey: { id: 3, name: 'k' } });
    const c = new HttpPasskeyClient({ fetchImpl: f });
    const p = await c.finishRegistration({ challengeId: 'x', name: 'k', credential: {} as never });
    expect(p).toMatchObject({ id: 3, name: 'k' });
  });

  it('turns error responses into ApiError with the server code', async () => {
    const c = new HttpPasskeyClient({ fetchImpl: fakeFetch(429, { error: 'throttled', message: 'later' }) });
    await expect(c.list()).rejects.toMatchObject({ status: 429, code: 'throttled', message: 'later' });
    const gone = new HttpPasskeyClient({ fetchImpl: (async () => Promise.reject(new TypeError('Failed to fetch'))) as unknown as typeof fetch });
    const err = await gone.list().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: 'network', status: 0 });
    const plain = new HttpPasskeyClient({ fetchImpl: (async () => new Response('oops', { status: 502 })) as unknown as typeof fetch });
    await expect(plain.list()).rejects.toMatchObject({ status: 502, code: 'http_502' });
  });

  it('204 answers resolve', async () => {
    const c = new HttpPasskeyClient({ fetchImpl: fakeFetch(204) });
    await expect(c.rename(1, 'x')).resolves.toBeUndefined();
  });

  it('only the HTTP transport (web client) gets a client', () => {
    expect(createPasskeyClient({ kind: 'http' } as never)).toBeInstanceOf(HttpPasskeyClient);
    expect(createPasskeyClient({ kind: 'mock' } as never)).toBeNull();
    expect(createPasskeyClient({ kind: 'wails' } as never)).toBeNull();
  });
});
