// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import en from '$locales/en.json';
import de from '$locales/de.json';
import { flatten } from '../i18n/core';
import type { MessageKey } from '../i18n/index.svelte';
import { adminErrorMessage } from '../admin/errors';
import { ApiError } from './api';
import { MockTransport } from './mock';
import { ADMIN_ERROR_CODES, HttpAdminClient, MockAdminClient, createAdminClient, normalizeAdminUser } from './admin';

const EN = flatten(en);
const DE = flatten(de);
const tr = (k: MessageKey, p?: Record<string, string | number>) => (EN[k] ?? k).replace(/\{(\w+)\}/g, (_, n) => String(p?.[n] ?? `{${n}}`));

function http(responses: Array<{ status?: number; body?: unknown }>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchImpl = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const r = responses.shift() ?? { status: 204 };
    const status = r.status ?? 200;
    return new Response(status === 204 ? null : JSON.stringify(r.body ?? {}), { status, headers: { 'Content-Type': 'application/json' } });
  }) as unknown as typeof fetch;
  return { client: new HttpAdminClient({ fetchImpl }), calls };
}

describe('createAdminClient (hidden outside the web client)', () => {
  it('exists for the web client (http) and the demo (mock) only', () => {
    expect(createAdminClient({ kind: 'http' } as never, {}, false)).toBeInstanceOf(HttpAdminClient);
    expect(createAdminClient(new MockTransport(), {}, false)).toBeInstanceOf(MockAdminClient);
  });
  it('is null in the desktop shell (wails) and in the native app', () => {
    expect(createAdminClient({ kind: 'wails' } as never, {}, false)).toBeNull();
    expect(createAdminClient({ kind: 'http' } as never, {}, true)).toBeNull();
  });
});

describe('HttpAdminClient', () => {
  it('reads the role from /api/auth/me', async () => {
    const { client, calls } = http([{ body: { user: { id: 7, username: 'root', role: 'admin' }, mfaSetupRequired: true } }]);
    expect(await client.me()).toEqual({ id: 7, username: 'root', role: 'admin', mfaSetupRequired: true });
    expect(calls[0].url).toBe('/api/auth/me');
  });

  it('lists users with the lock state and fills defaults for older servers', async () => {
    const { client } = http([
      { body: { users: [{ id: 1, username: 'a', locked: true, lockedUntil: '2026-11-06T12:00:00Z', underPressure: true }, { id: 2, username: 'b' }] } },
    ]);
    const [a, b] = await client.listUsers();
    expect(a).toMatchObject({ locked: true, lockedUntil: '2026-11-06T12:00:00Z', underPressure: true });
    expect(b).toMatchObject({ locked: false, underPressure: false, disabled: false });
  });

  it('sends the documented requests', async () => {
    const { client, calls } = http([{ status: 204 }, { status: 204 }, { status: 204 }, { status: 204 }, { status: 204 }]);
    await client.unlock(5);
    await client.deleteUser(5, true);
    await client.deleteUser(6, false);
    await client.revokeSession(5, 9);
    await client.removeMember(2, 5);
    expect(calls.map((c) => `${c.init.method} ${c.url}`)).toEqual([
      'POST /api/admin/users/5/unlock',
      'DELETE /api/admin/users/5?keepData=true',
      'DELETE /api/admin/users/6',
      'DELETE /api/admin/users/5/sessions/9',
      'DELETE /api/admin/groups/2/members/5',
    ]);
  });

  it('returns a generated password once from create and reset', async () => {
    const { client } = http([{ status: 201, body: { user: { id: 3, username: 'n' }, password: 'generated-pass-123' } }, { body: { password: 'another-one' } }]);
    expect((await client.createUser({ username: 'n', email: '', displayName: '', role: 'user' })).password).toBe('generated-pass-123');
    expect(await client.resetPassword(3)).toBe('another-one');
  });

  it('builds the audit query (paging, prefix filters, failures only)', async () => {
    const { client, calls } = http([{ body: { entries: [{ id: 3 }] } }, { body: { entries: null } }]);
    expect(await client.auditLog({ limit: 50, before: 120, action: ' user. ', actor: 'ro', onlyFailed: true })).toHaveLength(1);
    expect(calls[0].url).toBe('/api/admin/audit?limit=50&before=120&action=user.&actor=ro&result=failed');
    expect(await client.auditLog()).toEqual([]);
    expect(calls[1].url).toBe('/api/admin/audit');
  });

  it('creates an invite and returns the link', async () => {
    const { client, calls } = http([{ status: 201, body: { invite: { id: 1, role: 'user' }, url: 'https://m.example/register?invite=abc', token: 'abc' } }]);
    const r = await client.createInvite({ role: 'user', email: ' a@b.de ', expiresInHours: 72 });
    expect(r.url).toBe('https://m.example/register?invite=abc');
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ role: 'user', email: 'a@b.de', expiresInHours: 72 });
  });

  it('turns server errors into ApiError with the server code', async () => {
    const { client } = http([{ status: 409, body: { error: 'last_admin', message: 'cannot remove the last administrator' } }]);
    await expect(client.updateUser(1, { role: 'user' })).rejects.toMatchObject({ status: 409, code: 'last_admin' });
  });
});

describe('error messages', () => {
  it('translates every known code in both languages', () => {
    for (const code of ADMIN_ERROR_CODES) {
      const key = `admin.errors.${code}`;
      expect(EN[key], key).toBeTruthy();
      expect(DE[key], key).toBeTruthy();
    }
    expect(EN['admin.errors.unknown']).toBeTruthy();
  });

  it('shows known codes in the UI language, with the password policy, and unknown ones as the server wrote them', () => {
    expect(adminErrorMessage(new ApiError('x', 409, 'last_admin'), tr)).toBe(EN['admin.errors.last_admin']);
    expect(adminErrorMessage(new ApiError('x', 400, 'weak_password'), tr, 14)).toContain('14');
    expect(adminErrorMessage(new ApiError('boom', 500, 'internal'), tr)).toBe('boom');
    expect(adminErrorMessage('???', tr)).toBe(EN['admin.errors.unknown']);
  });
});

describe('MockAdminClient (demo server)', () => {
  const mk = () => new MockAdminClient({ latencyMs: 0 });

  it('reports locked and attacked accounts and unlock clears both', async () => {
    const c = mk();
    const find = async (n: string) => (await c.listUsers()).find((u) => u.username === n)!;
    expect(await find('ben')).toMatchObject({ locked: true });
    expect(await find('carla')).toMatchObject({ underPressure: true });
    for (const n of ['ben', 'carla']) await c.unlock((await find(n)).id);
    expect(await find('ben')).toMatchObject({ locked: false, underPressure: false });
    expect(await find('carla')).toMatchObject({ locked: false, underPressure: false });
  });

  it('keeps the last-administrator rule of the server', async () => {
    const c = mk();
    const admins = (await c.listUsers()).filter((u) => u.role === 'admin');
    expect(admins.length).toBe(2);
    await c.updateUser(admins[1].id, { role: 'user' });
    await expect(c.updateUser(admins[0].id, { role: 'user' })).rejects.toMatchObject({ code: 'last_admin' });
    await expect(c.updateUser(admins[0].id, { disabled: true })).rejects.toMatchObject({ code: 'last_admin' });
    await expect(c.deleteUser(admins[0].id, false)).rejects.toMatchObject({ code: 'last_admin' });
  });

  it('enforces the password policy and unique names, and generates a password when none is given', async () => {
    const c = mk();
    await expect(c.createUser({ username: 'neu', email: '', displayName: '', role: 'user', password: 'short' })).rejects.toMatchObject({ code: 'weak_password' });
    await expect(c.createUser({ username: 'anna', email: '', displayName: '', role: 'user' })).rejects.toMatchObject({ code: 'exists' });
    await expect(c.createUser({ username: 'bad name', email: '', displayName: '', role: 'user' })).rejects.toMatchObject({ code: 'invalid_input' });
    const r = await c.createUser({ username: 'neu', email: 'neu@example.de', displayName: 'Neu', role: 'readonly' });
    expect(r.password).toHaveLength(20);
    expect(r.user).toMatchObject({ username: 'neu', role: 'readonly', locked: false });
    // The password is not part of the list.
    expect(JSON.stringify(await c.listUsers())).not.toContain(r.password!);
  });

  it('is forbidden for non-administrators', async () => {
    const c = new MockAdminClient({ latencyMs: 0, role: 'user' });
    expect((await c.me()).role).toBe('user');
    await expect(c.listUsers()).rejects.toMatchObject({ status: 403, code: 'forbidden' });
  });

  it('filters and pages the audit log like the server', async () => {
    const c = mk();
    await c.createGroup('Neu', '');
    const failed = await c.auditLog({ onlyFailed: true });
    expect(failed.length).toBeGreaterThan(0);
    expect(failed.every((e) => !e.success)).toBe(true);
    const groupOnly = await c.auditLog({ action: 'GROUP.' });
    expect(groupOnly.map((e) => e.action)).toEqual(['group.create']);
    const first = await c.auditLog({ limit: 1 });
    const next = await c.auditLog({ limit: 1, before: first[0].id });
    expect(next[0].id).toBeLessThan(first[0].id);
  });
});

describe('normalizeAdminUser', () => {
  it('defaults the lock fields', () => {
    expect(normalizeAdminUser({ id: 1, username: 'x' })).toMatchObject({ locked: false, underPressure: false });
  });
});
