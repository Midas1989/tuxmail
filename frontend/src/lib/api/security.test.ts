// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { HttpTransport } from './http';
import { MockTransport } from './mock';
import { MOCK_PASSWORD, MOCK_TOTP_CODE } from './mockSecurity';
import { WailsTransport } from './wails';

interface Sent {
  method: string;
  url: string;
  body?: unknown;
}

function http(answer: (url: string, init: RequestInit) => Response, opts: { onMfaSetupRequired?: () => void } = {}) {
  const sent: Sent[] = [];
  const fetchImpl = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ method: init?.method ?? 'GET', url: String(url), body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined });
    return answer(String(url), init ?? {});
  });
  return { sent, api: new HttpTransport({ fetchImpl: fetchImpl as unknown as typeof fetch, ...opts }) };
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

describe('HttpTransport account security routes', () => {
  it('maps every method to its server route and unwraps the answers', async () => {
    const { api, sent } = http((url, init) => {
      if (url === '/api/auth/me') return json(200, { user: { id: 1, totpEnabled: false }, mfaSetupRequired: true, recoveryCodesLeft: 0 });
      if (url === '/api/auth/config') return json(200, { passwordMinLength: 14, product: 'tuxmail' });
      if (url === '/api/auth/totp/setup') return json(200, { secret: 'ABCDEF', otpauthUrl: 'otpauth://totp/x?secret=ABCDEF' });
      if (url === '/api/auth/totp/confirm') return json(200, { recoveryCodes: ['aaaaa-bbbbb', 'ccccc-ddddd'] });
      if (url === '/api/auth/recovery-codes') return json(200, { recoveryCodes: ['eeeee-fffff'] });
      if (url === '/api/auth/sessions' && init.method === 'GET') return json(200, { sessions: [{ id: 7, current: true }] });
      return new Response(null, { status: 204 });
    });
    expect((await api.getAuthMe()).mfaSetupRequired).toBe(true);
    expect((await api.getAuthConfig()).passwordMinLength).toBe(14);
    expect(await api.totpSetup()).toEqual({ secret: 'ABCDEF', otpauthUrl: 'otpauth://totp/x?secret=ABCDEF' });
    expect(await api.totpConfirm('123456')).toEqual(['aaaaa-bbbbb', 'ccccc-ddddd']);
    await api.totpDisable({ password: 'pw', code: '654321' });
    expect(await api.regenerateRecoveryCodes({ recoveryCode: 'zzzzz-yyyyy' })).toEqual(['eeeee-fffff']);
    await api.changePassword({ currentPassword: 'old', newPassword: 'new-password-1' });
    expect(await api.listAuthSessions()).toEqual([{ id: 7, current: true }]);
    await api.revokeAuthSession(9);
    await api.revokeOtherSessions();
    expect(sent).toEqual([
      { method: 'GET', url: '/api/auth/me' },
      { method: 'GET', url: '/api/auth/config' },
      { method: 'POST', url: '/api/auth/totp/setup' },
      { method: 'POST', url: '/api/auth/totp/confirm', body: { code: '123456' } },
      { method: 'POST', url: '/api/auth/totp/disable', body: { password: 'pw', code: '654321' } },
      { method: 'POST', url: '/api/auth/recovery-codes', body: { recoveryCode: 'zzzzz-yyyyy' } },
      { method: 'POST', url: '/api/auth/password', body: { currentPassword: 'old', newPassword: 'new-password-1' } },
      { method: 'GET', url: '/api/auth/sessions' },
      { method: 'DELETE', url: '/api/auth/sessions/9' },
      { method: 'POST', url: '/api/auth/sessions/revoke-others' },
    ]);
  });

  it('403 mfa_setup_required raises the hook (and still rejects); other 403s do not', async () => {
    const hook = vi.fn();
    const { api } = http(() => json(403, { error: 'mfa_setup_required', message: 'set up two-factor authentication first' }), { onMfaSetupRequired: hook });
    await expect(api.listAccounts()).rejects.toMatchObject({ status: 403, code: 'mfa_setup_required' });
    expect(hook).toHaveBeenCalledTimes(1);

    const other = http(() => json(403, { error: 'reauth_required', message: 'x' }), { onMfaSetupRequired: hook });
    await expect(other.api.totpDisable({})).rejects.toMatchObject({ status: 403, code: 'reauth_required' });
    const forbidden = http(() => json(403, { error: 'forbidden', message: 'administrator role required' }), { onMfaSetupRequired: hook });
    await expect(forbidden.api.listAccounts()).rejects.toMatchObject({ code: 'forbidden' });
    expect(hook).toHaveBeenCalledTimes(1);
  });

  it('429 carries Retry-After as ApiError.retryAfter', async () => {
    const { api } = http(() => json(429, { error: 'throttled', message: 'too many' }, { 'Retry-After': '240' }));
    const err = await api.totpDisable({ code: '000000' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 429, code: 'throttled', retryAfter: 240 });
  });

  it('other errors have no retryAfter', async () => {
    const { api } = http(() => json(400, { error: 'invalid_code', message: 'bad' }, { 'Retry-After': '5' }));
    const err = (await api.totpConfirm('1').catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe('invalid_code');
    expect(err.retryAfter).toBeUndefined();
  });
});

describe('MockTransport account security', () => {
  const mock = (security = {}) => new MockTransport({ latencyMs: 0, security });

  it('enrolment: confirm needs the right code, returns 10 recovery codes once', async () => {
    const api = mock();
    expect((await api.getAuthMe()).user.totpEnabled).toBe(false);
    await expect(api.totpConfirm(MOCK_TOTP_CODE)).rejects.toMatchObject({ code: 'invalid_code' }); // no setup yet
    const enr = await api.totpSetup();
    expect(enr.otpauthUrl.startsWith('otpauth://totp/')).toBe(true);
    await expect(api.totpConfirm('000000')).rejects.toMatchObject({ status: 400, code: 'invalid_code' });
    const codes = await api.totpConfirm(MOCK_TOTP_CODE);
    expect(codes).toHaveLength(10);
    expect(codes.every((c) => /^[a-z2-9]{5}-[a-z2-9]{5}$/.test(c))).toBe(true);
    const me = await api.getAuthMe();
    expect(me.user.totpEnabled).toBe(true);
    expect(me.recoveryCodesLeft).toBe(10);
    await expect(api.totpSetup()).rejects.toMatchObject({ code: 'exists' });
  });

  it('administrator without TOTP: mfaSetupRequired until the set-up is done', async () => {
    const api = mock({ admin: true });
    expect((await api.getAuthMe()).mfaSetupRequired).toBe(true);
    await api.totpSetup();
    await api.totpConfirm(MOCK_TOTP_CODE);
    expect((await api.getAuthMe()).mfaSetupRequired).toBe(false);
  });

  it('disable and new recovery codes need the password and a second factor', async () => {
    const api = mock({ totpEnabled: true });
    await expect(api.regenerateRecoveryCodes({ password: 'wrong', code: MOCK_TOTP_CODE })).rejects.toMatchObject({ status: 401, code: 'invalid_credentials' });
    await expect(api.regenerateRecoveryCodes({ password: MOCK_PASSWORD, code: '111111' })).rejects.toMatchObject({ code: 'invalid_code' });
    const fresh = await api.regenerateRecoveryCodes({ password: MOCK_PASSWORD, code: MOCK_TOTP_CODE });
    expect(fresh).toHaveLength(10);
    // a recovery code works as second factor and is used up
    await api.totpDisable({ password: MOCK_PASSWORD, recoveryCode: fresh[0].toUpperCase() });
    const me = await api.getAuthMe();
    expect(me.user.totpEnabled).toBe(false);
    expect(me.recoveryCodesLeft).toBe(0);
    await expect(api.regenerateRecoveryCodes({ password: MOCK_PASSWORD, code: MOCK_TOTP_CODE })).rejects.toMatchObject({ status: 409, code: 'totp_not_enabled' });
  });

  it('account without local password re-authenticates with the code only', async () => {
    const api = mock({ totpEnabled: true, hasPassword: false });
    expect((await api.getAuthMe()).user.hasPassword).toBe(false);
    await api.totpDisable({ code: MOCK_TOTP_CODE });
    expect((await api.getAuthMe()).user.totpEnabled).toBe(false);
  });

  it('repeated wrong answers lock the step-up (429 throttled with Retry-After)', async () => {
    const api = mock({ totpEnabled: true });
    for (let i = 0; i < 5; i++) await expect(api.totpDisable({ password: MOCK_PASSWORD, code: '000000' })).rejects.toMatchObject({ code: 'invalid_code' });
    const err = (await api.totpDisable({ password: MOCK_PASSWORD, code: '000000' }).catch((e: unknown) => e)) as ApiError;
    expect(err).toMatchObject({ status: 429, code: 'throttled' });
    expect(err.retryAfter).toBeGreaterThan(0);
    // even the right code is refused while locked
    await expect(api.totpDisable({ password: MOCK_PASSWORD, code: MOCK_TOTP_CODE })).rejects.toMatchObject({ code: 'throttled' });
  });

  it('password change: current password, policy, ends the other sessions', async () => {
    const api = mock();
    expect(await api.listAuthSessions()).toHaveLength(3);
    await expect(api.changePassword({ currentPassword: 'nope', newPassword: 'a-long-new-password' })).rejects.toMatchObject({ code: 'invalid_credentials' });
    await expect(api.changePassword({ currentPassword: MOCK_PASSWORD, newPassword: 'short' })).rejects.toMatchObject({ code: 'weak_password' });
    await api.changePassword({ currentPassword: MOCK_PASSWORD, newPassword: 'a-long-new-password' });
    const left = await api.listAuthSessions();
    expect(left).toHaveLength(1);
    expect(left[0].current).toBe(true);
    await api.changePassword({ currentPassword: 'a-long-new-password', newPassword: 'another-long-password' });
  });

  it('first password of an account without one needs the second factor', async () => {
    const api = mock({ hasPassword: false });
    await expect(api.changePassword({ newPassword: 'a-long-new-password' })).rejects.toMatchObject({ code: 'reauth_required' });
    await api.totpSetup();
    await api.totpConfirm(MOCK_TOTP_CODE);
    await expect(api.changePassword({ newPassword: 'a-long-new-password', totpCode: '000000' })).rejects.toMatchObject({ code: 'invalid_code' });
    await api.changePassword({ newPassword: 'a-long-new-password', totpCode: MOCK_TOTP_CODE });
    expect((await api.getAuthMe()).user.hasPassword).toBe(true);
  });

  it('sessions: revoke one, revoke the others, unknown id is 404', async () => {
    const api = mock();
    await api.revokeAuthSession(2);
    expect((await api.listAuthSessions()).map((s) => s.id)).toEqual([1, 3]);
    await expect(api.revokeAuthSession(99)).rejects.toMatchObject({ status: 404 });
    await api.revokeOtherSessions();
    expect((await api.listAuthSessions()).map((s) => s.id)).toEqual([1]);
  });

  it('config exposes the password policy', async () => {
    expect((await mock().getAuthConfig()).passwordMinLength).toBeGreaterThan(0);
  });
});

describe('WailsTransport (desktop shell has no server account)', () => {
  it('rejects with not_supported', async () => {
    const api = new WailsTransport({} as never);
    await expect(api.getAuthMe()).rejects.toMatchObject({ code: 'not_supported' });
    await expect(api.totpSetup()).rejects.toMatchObject({ code: 'not_supported' });
    await expect(api.listAuthSessions()).rejects.toMatchObject({ code: 'not_supported' });
  });
});
