// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/api';
import { MockAdminClient, type AdminClient, type AdminMe } from '../api/admin';
import { ALL_ENV, parseSettingsHash, visibleCategories, normalizeLocation, searchSettings, type SettingsEnv } from '../settings/catalog';
import { AdminState } from './admin.svelte';

const WEB: SettingsEnv = { desktop: false, native: false, team: false, vault: false };

function clientWith(me: () => Promise<AdminMe>): AdminClient {
  return Object.assign(new MockAdminClient({ latencyMs: 0 }), { me });
}

describe('AdminState (role gating)', () => {
  it('shows the administration to an administrator (role from /api/auth/me)', async () => {
    const admin = new AdminState(new MockAdminClient({ latencyMs: 0 }));
    expect(admin.isAdmin).toBe(false); // nothing known yet
    await admin.init();
    expect(admin.isAdmin).toBe(true);
    expect(admin.config?.passwordMinLength).toBeGreaterThan(0);
  });

  it.each(['user', 'readonly'] as const)('hides it from the role %s and does not load the server config', async (role) => {
    const admin = new AdminState(new MockAdminClient({ latencyMs: 0, role }));
    await admin.init();
    expect(admin.me?.role).toBe(role);
    expect(admin.isAdmin).toBe(false);
    expect(admin.config).toBeNull();
  });

  it('hides it where there is no client (desktop shell, native app)', async () => {
    const admin = new AdminState(null);
    await admin.init();
    expect(admin.isAdmin).toBe(false);
    expect(admin.me).toBeNull();
  });

  it('hides it when the role cannot be read (signed out, server down)', async () => {
    const admin = new AdminState(clientWith(() => Promise.reject(new ApiError('down', 0, 'network'))));
    await admin.init();
    expect(admin.isAdmin).toBe(false);
  });

  it('asks the server only once', async () => {
    const me = vi.fn(async () => ({ id: 1, username: 'a', role: 'admin' as const, mfaSetupRequired: false }));
    const admin = new AdminState(clientWith(me));
    await Promise.all([admin.init(), admin.init()]);
    await admin.init();
    expect(me).toHaveBeenCalledTimes(1);
  });
});

describe('settings page "Administration"', () => {
  it('is part of the navigation only for administrators', () => {
    expect(visibleCategories({ ...WEB, admin: true }).map((c) => c.id)).toContain('admin');
    expect(visibleCategories({ ...WEB, admin: false }).map((c) => c.id)).not.toContain('admin');
    expect(visibleCategories(WEB).map((c) => c.id)).not.toContain('admin');
    expect(visibleCategories(ALL_ENV).map((c) => c.id)).toContain('admin');
  });

  it('a deep link to it falls back to the overview for everyone else', () => {
    const loc = parseSettingsHash('#/settings/admin/manage');
    expect(loc).toEqual({ category: 'admin', section: 'manage' });
    expect(normalizeLocation(loc, { ...WEB, admin: true })).toEqual(loc);
    expect(normalizeLocation(loc, WEB)).toEqual({ category: null, section: null });
  });

  it('is found by the settings search only when visible', () => {
    const tr = [(k: string) => k];
    // keys resolve to themselves here; search on the section/page keys
    const hit = (env: SettingsEnv) => searchSettings('admin', env, tr as never).some((h) => h.category === 'admin');
    expect(hit({ ...WEB, admin: true })).toBe(true);
    expect(hit(WEB)).toBe(false);
  });
});
