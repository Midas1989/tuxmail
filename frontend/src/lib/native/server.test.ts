// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it, vi } from 'vitest';
import { ServerError, directLogin, passkeyLogin, pollDeviceToken, secondFactor, safeVerificationUrl, startDeviceAuthorization, validateServer, type DeviceGrant } from './server';
import { rpIdMatchesServer } from './passkey';

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
    expect(info).toEqual({ serverUrl: 'https://mail.example.org', version: '1.2.0', oidcEnabled: true, registration: 'invite', maxUploadMB: 50, passkeyAppLogin: false });
    expect(f).toHaveBeenCalledTimes(2);
    expect(f.mock.calls[0][1]).toMatchObject({ credentials: 'omit' });
  });

  it('recognises a server that does not disclose its version (security.expose_version off)', async () => {
    const f = fakeFetch((url) => {
      if (url.endsWith('/healthz')) return { status: 200, body: { status: 'ok', product: 'tuxmail' } };
      if (url.endsWith('/api/auth/config')) return { status: 200, body: { product: 'tuxmail', oidcEnabled: false, registration: 'disabled', maxUploadMB: 50 } };
      return { status: 404 };
    });
    expect(await validateServer('mail.example.org', f)).toEqual({ serverUrl: 'https://mail.example.org', version: '', oidcEnabled: false, registration: 'disabled', maxUploadMB: 50, passkeyAppLogin: false });
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

describe('second factor field', () => {
  it('six digits are TOTP, anything else a recovery code', () => {
    expect(secondFactor('123456')).toEqual({ totpCode: '123456' });
    expect(secondFactor(' 123 456 ')).toEqual({ totpCode: '123456' });
    expect(secondFactor('abcd-efgh-1234')).toEqual({ recoveryCode: 'abcd-efgh-1234' });
    expect(secondFactor('1234567')).toEqual({ recoveryCode: '1234567' });
    expect(secondFactor('')).toEqual({});
    expect(secondFactor(undefined)).toEqual({});
  });

  it('directLogin sends a recovery code as recovery_code', async () => {
    const f = fakeFetch(() => ({ status: 200, body: { access_token: 't' } }));
    await directLogin('https://s', { username: 'a', password: 'p', ...secondFactor('wxyz-1234'), deviceName: 'X' }, f);
    const sent = JSON.parse(String(f.mock.calls[0][1].body));
    expect(sent).toMatchObject({ recovery_code: 'wxyz-1234' });
    expect(sent.totp_code).toBeUndefined();
  });
});

describe('passkeyLogin (Credential Manager)', () => {
  const options = { challenge: 'Y2hhbGxlbmdl', rpId: 'mail.example.org', userVerification: 'required', timeout: 300000 };
  const assertion = { id: 'cred', rawId: 'cred', type: 'public-key', response: { clientDataJSON: 'x', authenticatorData: 'y', signature: 'z' } };

  it('begin -> bridge -> login, token like the password login', async () => {
    const f = fakeFetch((url, init) => {
      if (url === 'https://mail.example.org/api/device/passkey/begin') return { status: 200, body: { challengeId: 'c1', publicKey: options } };
      if (url === 'https://mail.example.org/api/device/passkey/login') {
        const b = JSON.parse(String(init?.body));
        expect(b).toEqual({ challengeId: 'c1', credential: assertion, device_name: 'TuxMail Android (Pixel)', scope: 'data' });
        return { status: 200, body: { access_token: 'tok', expires_in: 60, session_id: 7, user: { username: 'anna' } } };
      }
      return { status: 404 };
    });
    const get = vi.fn(async (req: string, _serverUrl: string) => {
      expect(JSON.parse(req)).toEqual(options); // the options go to Credential Manager unchanged
      return JSON.stringify(assertion);
    });
    const res = await passkeyLogin('https://mail.example.org', { available: async () => true, get }, 'TuxMail Android (Pixel)', f);
    expect(get.mock.calls[0][1]).toBe('https://mail.example.org');
    expect(res).toMatchObject({ token: 'tok', sessionId: 7, user: { username: 'anna' } });
    expect(f.mock.calls.every((c: unknown[]) => (c[1] as RequestInit).credentials === 'omit')).toBe(true);
  });

  it('maps cancel, missing passkey and refusals', async () => {
    const { PasskeyError } = await import('./passkey');
    const begin = fakeFetch((url) => (url.endsWith('/begin') ? { status: 200, body: { challengeId: 'c', publicKey: options } } : { status: 401, body: { error: 'invalid_credentials', message: 'passkey sign-in failed' } }));
    const bridge = (err: unknown) => ({ available: async () => true, get: async () => Promise.reject(err) });
    await expect(passkeyLogin('https://mail.example.org', bridge(new PasskeyError('cancelled')), 'X', begin)).rejects.toMatchObject({ code: 'passkey_cancelled' });
    await expect(passkeyLogin('https://mail.example.org', bridge(new PasskeyError('no_credential')), 'X', begin)).rejects.toMatchObject({ code: 'passkey_unavailable' });
    await expect(passkeyLogin('https://mail.example.org', bridge(new Error('boom')), 'X', begin)).rejects.toMatchObject({ code: 'passkey_failed' });
    // The server refuses the assertion: never "wrong password".
    const ok = { available: async () => true, get: async () => JSON.stringify(assertion) };
    await expect(passkeyLogin('https://mail.example.org', ok, 'X', begin)).rejects.toMatchObject({ code: 'passkey_failed', status: 401 });
    // Throttled networks and switched-off passkeys keep their own codes.
    const throttled = fakeFetch(() => ({ status: 429, body: { error: 'throttled' } }));
    await expect(passkeyLogin('https://mail.example.org', ok, 'X', throttled)).rejects.toMatchObject({ code: 'throttled' });
    const off = fakeFetch(() => ({ status: 404, body: { error: 'passkeys_disabled', message: 'off' } }));
    await expect(passkeyLogin('https://mail.example.org', ok, 'X', off)).rejects.toMatchObject({ code: 'server_error' });
  });

  it('refuses a foreign rpId before Credential Manager is called (relay protection)', async () => {
    const get = vi.fn(async () => JSON.stringify(assertion));
    for (const rpId of ['evil.example', 'org', 'com', 'attacker-mail.example.org', undefined]) {
      const f = fakeFetch(() => ({ status: 200, body: { challengeId: 'c', publicKey: { ...options, rpId } } }));
      await expect(passkeyLogin('https://sub.mail.example.org', { available: async () => true, get }, 'X', f)).rejects.toMatchObject({ code: 'passkey_failed' });
    }
    expect(get).not.toHaveBeenCalled();
  });
});

describe('rpIdMatchesServer', () => {
  it('accepts the host itself and a parent registrable domain', () => {
    expect(rpIdMatchesServer('https://mail.example.org', 'mail.example.org')).toBe(true);
    expect(rpIdMatchesServer('https://Mail.Example.org:8443/x', 'MAIL.example.org')).toBe(true);
    expect(rpIdMatchesServer('https://mail.example.org', 'example.org')).toBe(true);
    expect(rpIdMatchesServer('http://localhost:8080', 'localhost')).toBe(true);
  });
  it('refuses foreign, lookalike and public-suffix-like rpIds', () => {
    for (const rp of ['evil.example', 'xample.org', 'ample.org', 'org', 'com', '.org', 'org.', 'mail.example.org.evil.example', '', undefined, 5]) {
      expect(rpIdMatchesServer('https://mail.example.org', rp)).toBe(false);
    }
    expect(rpIdMatchesServer('not a url', 'example.org')).toBe(false);
  });
});
