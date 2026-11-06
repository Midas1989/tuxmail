// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';
import type { Api } from '../api/api';
import { HttpTransport } from '../api/http';
import type { AuthMe } from '../api/security';
import { MockTransport } from '../api/mock';
import { notifyMfaSetupRequired, onMfaSetupRequired } from '../mfaGate';
import { platformHttpOptions } from '../session';
import { SecurityState } from './security.svelte';

function me(over: Partial<AuthMe> = {}): AuthMe {
  return { user: { id: 1, username: 'root', email: '', displayName: 'Root', role: 'admin', hasPassword: true, totpEnabled: false, oidc: false }, mfaSetupRequired: false, recoveryCodesLeft: 0, ...over };
}

function fakeApi(getAuthMe: () => Promise<AuthMe>, kind: Api['kind'] = 'http'): Api {
  return { kind, getAuthMe: vi.fn(getAuthMe) } as unknown as Api;
}

const settle = () => new Promise((r) => setTimeout(r, 0));

describe('forced two-factor set-up trigger', () => {
  it('/api/auth/me reporting mfaSetupRequired shows the dialog', async () => {
    const api = fakeApi(async () => me({ mfaSetupRequired: true }));
    const s = new SecurityState(api, { web: true, subscribe: () => () => {} });
    expect(s.mfaSetupRequired).toBe(false);
    s.start();
    await settle();
    expect(api.getAuthMe).toHaveBeenCalledTimes(1);
    expect(s.mfaSetupRequired).toBe(true);
    expect(s.me?.user.username).toBe('root');
  });

  it('mfaSetupRequired false (or a normal user) keeps the app usable', async () => {
    const s = new SecurityState(fakeApi(async () => me({ mfaSetupRequired: false })), { web: true, subscribe: () => () => {} });
    s.start();
    await settle();
    expect(s.mfaSetupRequired).toBe(false);
  });

  it('a 403 mfa_setup_required from any request (transport hook) shows the dialog', async () => {
    const s = new SecurityState(fakeApi(async () => me()), { web: true });
    const stop = s.start();
    await settle();
    expect(s.mfaSetupRequired).toBe(false);
    notifyMfaSetupRequired();
    expect(s.mfaSetupRequired).toBe(true);
    stop();
  });

  it('end to end: the default web transport options turn a 403 mfa_setup_required into the dialog', async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ error: 'mfa_setup_required', message: 'set up two-factor authentication first' }), { status: 403, headers: { 'Content-Type': 'application/json' } }));
    const transport = new HttpTransport({ ...platformHttpOptions(), fetchImpl: fetchImpl as unknown as typeof fetch });
    const s = new SecurityState(fakeApi(async () => me()), { web: true });
    const stop = s.start();
    await settle();
    await expect(transport.listAccounts()).rejects.toMatchObject({ code: 'mfa_setup_required' });
    expect(s.mfaSetupRequired).toBe(true);
    stop();
  });

  it('stopping unsubscribes from the gate', async () => {
    const s = new SecurityState(fakeApi(async () => me()), { web: true });
    s.start()();
    notifyMfaSetupRequired();
    expect(s.mfaSetupRequired).toBe(false);
  });

  it('a failing /api/auth/me changes nothing (401 is handled by the transport)', async () => {
    const s = new SecurityState(fakeApi(async () => Promise.reject(new Error('offline'))), { web: true, subscribe: () => () => {} });
    s.start();
    await settle();
    expect(s.mfaSetupRequired).toBe(false);
    expect(s.me).toBeNull();
  });

  it('desktop shell, demo and native app: nothing is asked, nothing is shown', async () => {
    const api = fakeApi(async () => me({ mfaSetupRequired: true }), 'wails');
    const s = new SecurityState(api, { subscribe: onMfaSetupRequired });
    expect(s.available).toBe(false);
    s.start();
    s.require();
    await settle();
    expect(api.getAuthMe).not.toHaveBeenCalled();
    expect(s.mfaSetupRequired).toBe(false);
    expect(new SecurityState(new MockTransport(), {}).available).toBe(false);
    expect(new SecurityState(fakeApi(async () => me()), {}).available).toBe(true);
  });

  it('finished() closes the dialog and reloads the application state', async () => {
    const reload = vi.fn();
    const s = new SecurityState(fakeApi(async () => me({ mfaSetupRequired: true })), { web: true, subscribe: () => () => {}, reload });
    s.start();
    await settle();
    s.finished();
    expect(s.mfaSetupRequired).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('refresh() after the administrator turned TOTP on hides the dialog; after turning it off it appears again', async () => {
    let required = true;
    const s = new SecurityState(fakeApi(async () => me({ mfaSetupRequired: required })), { web: true, subscribe: () => () => {} });
    await s.refresh();
    expect(s.mfaSetupRequired).toBe(true);
    required = false;
    await s.refresh();
    expect(s.mfaSetupRequired).toBe(false);
    required = true;
    await s.refresh();
    expect(s.mfaSetupRequired).toBe(true);
  });
});
