// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { normalizeAdminUser, type AdminUser } from '../api/admin';
import { activeAdmins, EMPTY_FILTER, filterUsers, isFiltered, needsUnlock, userGuards, userStatus } from './users';
import { inviteStatus } from './invites';

let nextId = 1;
const user = (username: string, extra: Partial<AdminUser> = {}): AdminUser =>
  normalizeAdminUser({ id: nextId++, username, email: `${username}@example.de`, displayName: '', ...extra });

describe('userStatus / needsUnlock', () => {
  it('disabled wins over locked; pressure counts as locked', () => {
    expect(userStatus(user('a'))).toBe('active');
    expect(userStatus(user('a', { locked: true }))).toBe('locked');
    expect(userStatus(user('a', { underPressure: true }))).toBe('locked');
    expect(userStatus(user('a', { disabled: true, locked: true }))).toBe('disabled');
  });

  it('offers unlock only while the account is throttled (also when it is disabled)', () => {
    expect(needsUnlock(user('a'))).toBe(false);
    expect(needsUnlock(user('a', { locked: true }))).toBe(true);
    expect(needsUnlock(user('a', { underPressure: true }))).toBe(true);
    expect(needsUnlock(user('a', { disabled: true, locked: true }))).toBe(true);
  });
});

describe('filterUsers', () => {
  const all = [
    user('anna', { displayName: 'Anna Müller', role: 'admin', totpEnabled: true }),
    user('ben', { displayName: 'Ben Keller', locked: true }),
    user('carla', { email: 'carla@firma.de', underPressure: true }),
    user('dieter', { disabled: true, role: 'readonly' }),
  ];
  const names = (f: Partial<typeof EMPTY_FILTER>) => filterUsers(all, { ...EMPTY_FILTER, ...f }).map((u) => u.username);

  it('returns everything without a filter', () => {
    expect(names({})).toEqual(['anna', 'ben', 'carla', 'dieter']);
    expect(isFiltered(EMPTY_FILTER)).toBe(false);
    expect(isFiltered({ ...EMPTY_FILTER, query: ' ' })).toBe(false);
    expect(isFiltered({ ...EMPTY_FILTER, role: 'user' })).toBe(true);
  });

  it('searches name, display name and e-mail, ignoring case and accents, all words must match', () => {
    expect(names({ query: 'MULLER' })).toEqual(['anna']);
    expect(names({ query: 'firma' })).toEqual(['carla']);
    expect(names({ query: 'ben kel' })).toEqual(['ben']);
    expect(names({ query: 'ben anna' })).toEqual([]);
  });

  it('filters by role, status and 2FA', () => {
    expect(names({ role: 'admin' })).toEqual(['anna']);
    expect(names({ status: 'disabled' })).toEqual(['dieter']);
    expect(names({ status: 'locked' })).toEqual(['ben', 'carla']);
    expect(names({ status: 'active' })).toEqual(['anna']);
    expect(names({ totp: 'on' })).toEqual(['anna']);
    expect(names({ totp: 'off' })).toEqual(['ben', 'carla', 'dieter']);
    expect(names({ role: 'user', status: 'locked' })).toEqual(['ben', 'carla']);
  });
});

describe('userGuards', () => {
  it('blocks self-lockout: role, disable, delete, password reset, session revoke, 2FA reset', () => {
    const me = user('root', { role: 'admin' });
    const other = user('boss', { role: 'admin' });
    const g = userGuards(me, [me, other], me.id);
    expect(g).toEqual({ role: 'self', disable: 'self', delete: 'self', resetPassword: 'self', revokeAll: 'self', reset2FA: 'self' });
  });

  it('blocks demoting, disabling and deleting the only enabled administrator', () => {
    const only = user('only', { role: 'admin' });
    const plain = user('plain');
    const g = userGuards(only, [only, plain], plain.id);
    expect(g.role).toBe('lastAdmin');
    expect(g.disable).toBe('lastAdmin');
    expect(g.delete).toBe('lastAdmin');
    expect(g.resetPassword).toBeNull();
    expect(g.reset2FA).toBeNull();
  });

  it('a disabled administrator does not count, so the other one is the last', () => {
    const a = user('a', { role: 'admin' });
    const b = user('b', { role: 'admin', disabled: true });
    expect(activeAdmins([a, b])).toBe(1);
    expect(userGuards(a, [a, b], 999).delete).toBe('lastAdmin');
    // The disabled one can be enabled and deleted at will.
    const gb = userGuards(b, [a, b], 999);
    expect(gb.disable).toBeNull();
    expect(gb.delete).toBeNull();
  });

  it('allows everything on others when there are two administrators', () => {
    const a = user('a', { role: 'admin' });
    const b = user('b', { role: 'admin' });
    expect(userGuards(b, [a, b], a.id)).toEqual({ role: null, disable: null, delete: null, resetPassword: null, revokeAll: null, reset2FA: null });
  });

  it('enabling a disabled user is never blocked', () => {
    const me = user('me', { role: 'admin', disabled: true });
    expect(userGuards(me, [me], me.id).disable).toBeNull();
  });
});

describe('inviteStatus', () => {
  const now = new Date('2026-11-06T12:00:00Z');
  it('is used, expired or open', () => {
    expect(inviteStatus({ expiresAt: '2026-11-07T00:00:00Z' }, now)).toBe('open');
    expect(inviteStatus({ expiresAt: '2026-11-06T11:00:00Z' }, now)).toBe('expired');
    expect(inviteStatus({ expiresAt: '2026-11-07T00:00:00Z', usedAt: '2026-11-06T10:00:00Z' }, now)).toBe('used');
  });
});
