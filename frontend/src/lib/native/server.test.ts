// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { ServerError, directLogin, pollDeviceToken, safeVerificationUrl, startDeviceAuthorization, validateServer, type DeviceGrant } from './server';

type Route = (url: string, init?: RequestInit) => { status: number; body?: unknown } | Promise<{ status: number; body?: unknown }>;

function fakeFetch(route: Route) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const r = await route(String(input), init);
    return new Response(r.body === undefined ? 'not json' : JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } });
  }) as unknown as typeof fetch & ReturnType<typeof vi.fn>;
}

describe('validateServer', () => {
  it('normalises the URL and checks /healthz + /api/auth/config', async () => {
    const f = fakeFetch((url) => {
      if (url === 'https://mail.example.org/healthz') return { status: 200, body: { status: 'ok', version: '1.2.0' } };
      if (url === 'https://mail.example.org/api/auth/config') return { status: 200, body: { version: '1.2.0', oidcEnabled: true, registration: 'invite', maxUploadMB: 50 } };
      return { status: 404 };
    });
    const info = await validateServer('mail.example.org/', f);
    expect(info).toEqual({ serverUrl: 'https://mail.example.org', version: '1.2.0', oidcEnabled: true, registration: 'invite', maxUploadMB: 50 });
    expect(f).toHaveBeenCalledTimes(2);
    expect(f.mock.calls[0][1]).toMatchObject({ credentials: 'omit' });
  });

  it('recognises a server that does not disclose its version (security.expose_version off)', async () => {
    const f = fakeFetch((url) => {
      if (url.endsWith('/healthz')) return { status: 200, body: { status: 'ok', product: 'tuxmail' } };
      if (url.endsWith('/api/auth/config')) return { status: 200, body: { product: 'tuxmail', oidcEnabled: false, registration: 'disabled', maxUploadMB: 50 } };
      return { status: 404 };
    });
    expect(await validateServer('mail.example.org', f)).toEqual({ serverUrl: 'https://mail.example.org', version: '', oidcEnabled: false, registration: 'disabled', maxUploadMB: 50 });
    // A foreign server without marker and version is still refused.
    const foreign = fakeFetch((url) => (url.endsWith('/healthz') ? { status: 200, body: { status: 'ok' } } : { status: 200, body: { hello: 'world' } }));
    await expect(validateServer('other.example', foreign)).rejects.toMatchObject({ code: 'not_tuxmail' });
  });

  it('rejects invalid and plain-http URLs without a request', async () => {
    const f = fakeFetch(() => ({ status: 200, body: {} }));
    await expect(validateServer('', f)).rejects.toMatchObject({ code: 'invalid_url' });
    await expect(validateServer('http://mail.example.org', f)).rejects.toMatchObject({ code: 'invalid_url' });
    expect(f).not.toHaveBeenCalled();
  });

  it('distinguishes unreachable, foreign and unhealthy servers', async () => {
    const down = vi.fn(async () => {
      throw new TypeError('Failed to fetch');
    }) as unknown as typeof fetch;
    await expect(validateServer('down.example', down)).rejects.toMatchObject({ code: 'unreachable' });
    await expect(validateServer('nextcloud.example', fakeFetch(() => ({ status: 404 })))).rejects.toMatchObject({ code: 'not_tuxmail' });
    await expect(validateServer('sick.example', fakeFetch(() => ({ status: 503, body: { status: 'error', error: 'database unavailable' } })))).rejects.toMatchObject({ code: 'unhealthy' });
    const noConfig = fakeFetch((url) => (url.endsWith('/healthz') ? { status: 200, body: { status: 'ok' } } : { status: 404, body: { error: 'not_found' } }));
    await expect(validateServer('half.example', noConfig)).rejects.toMatchObject({ code: 'not_tuxmail' });
  });
});

const grant: DeviceGrant = { deviceCode: 'dc', userCode: 'ABCD-EFGH', verificationUri: 'https://s/device', verificationUriComplete: 'https://s/device?user_code=ABCD-EFGH', interval: 5, expiresIn: 600 };

describe('device flow', () => {
  it('starts the authorization with snake_case fields', async () => {
    const f = fakeFetch((_url, init) => {
      expect(JSON.parse(String(init?.body))).toEqual({ client_name: 'TuxMail Android', scope: 'data' });
      return { status: 200, body: { device_code: 'dc', user_code: 'ABCD-EFGH', verification_uri: 'https://s/device', verification_uri_complete: 'https://s/device?user_code=ABCD-EFGH', interval: 5, expires_in: 600 } };
    });
    expect(await startDeviceAuthorization('https://s', 'TuxMail Android', f)).toEqual(grant);
  });

  it('polls until approved, honouring slow_down', async () => {
    const answers = [
      { status: 400, body: { error: 'authorization_pending' } },
      { status: 400, body: { error: 'slow_down' } },
      { status: 200, body: { access_token: 'tok', token_type: 'Bearer', expires_in: 3600, session_id: 7, user: { username: 'anna' } } },
    ];
    const f = fakeFetch((url, init) => {
      expect(url).toBe('https://s/api/device/token');
      expect(JSON.parse(String(init?.body))).toEqual({ device_code: 'dc' });
      return answers.shift()!;
    });
    const waits: number[] = [];
    const res = await pollDeviceToken('https://s', grant, { fetch: f, sleep: async (ms) => void waits.push(ms), now: () => 0 });
    expect(res).toEqual({ token: 'tok', expiresIn: 3600, sessionId: 7, user: { username: 'anna' } });
    expect(waits).toEqual([5000, 5000, 10000]);
  });

  it('stops on access_denied and on expiry', async () => {
    const denied = fakeFetch(() => ({ status: 400, body: { error: 'access_denied' } }));
    await expect(pollDeviceToken('https://s', grant, { fetch: denied, sleep: async () => {}, now: () => 0 })).rejects.toMatchObject({ code: 'access_denied' });
    let t = 0;
    const pending = fakeFetch(() => ({ status: 400, body: { error: 'authorization_pending' } }));
    await expect(
      pollDeviceToken('https://s', { ...grant, expiresIn: 10 }, { fetch: pending, sleep: async (ms) => void (t += ms), now: () => t }),
    ).rejects.toMatchObject({ code: 'expired_token' });
  });

  it('can be aborted', async () => {
    const ctl = new AbortController();
    ctl.abort();
    await expect(pollDeviceToken('https://s', grant, { fetch: fakeFetch(() => ({ status: 400 })), signal: ctl.signal })).rejects.toBeInstanceOf(ServerError);
  });
});

describe('directLogin', () => {
  it('sends credentials and maps mfa_required', async () => {
    const f = fakeFetch((url, init) => {
      expect(url).toBe('https://s/api/device/login');
      const b = JSON.parse(String(init?.body));
      if (!b.totp_code) return { status: 401, body: { error: 'mfa_required', error_description: 'two-factor code required' } };
      expect(b).toEqual({ username: 'anna', password: 'pw', totp_code: '123456', device_name: 'Phone', scope: 'data' });
      return { status: 200, body: { access_token: 'tok' } };
    });
    await expect(directLogin('https://s', { username: 'anna', password: 'pw', deviceName: 'Phone' }, f)).rejects.toMatchObject({ code: 'mfa_required', status: 401 });
    expect((await directLogin('https://s', { username: 'anna', password: 'pw', totpCode: '123456', deviceName: 'Phone' }, f)).token).toBe('tok');
  });

  it('maps server error codes', async () => {
    for (const [status, error, code] of [
      [401, 'invalid_credentials', 'invalid_credentials'],
      [429, 'throttled', 'throttled'],
      [403, 'user_disabled', 'user_disabled'],
      [500, 'internal', 'server_error'],
    ] as const) {
      const f = fakeFetch(() => ({ status, body: { error, message: 'x' } }));
      await expect(directLogin('https://s', { username: 'a', password: 'b', deviceName: 'c' }, f)).rejects.toMatchObject({ code });
    }
  });
});

describe('safeVerificationUrl', () => {
  it('allows https pages and resolves relative URIs', () => {
    expect(safeVerificationUrl('https://s.example', 'https://s.example/device?user_code=A')).toBe('https://s.example/device?user_code=A');
    expect(safeVerificationUrl('https://s.example/tux', 'device?user_code=A')).toBe('https://s.example/tux/device?user_code=A');
    expect(safeVerificationUrl('https://s.example', 'https://sso.example/device')).toBe('https://sso.example/device');
  });
  it('rejects other schemes', () => {
    expect(safeVerificationUrl('https://s.example', 'javascript:alert(1)')).toBeNull();
    expect(safeVerificationUrl('https://s.example', 'http://s.example/device')).toBeNull();
    expect(safeVerificationUrl('http://localhost:8080', 'http://localhost:8080/device')).toBe('http://localhost:8080/device');
  });
});
