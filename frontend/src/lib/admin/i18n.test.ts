// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Every message key the administration screens use exists in both languages
// (the generic key-parity test only compares the two files with each other).

import { describe, expect, it } from 'vitest';
import en from '$locales/en.json';
import de from '$locales/de.json';
import { flatten } from '../i18n/core';
import { ADMIN_ROLES } from '../api/admin';
import { ADMIN_TABS } from './tabs';

const EN = flatten(en);
const DE = flatten(de);

const sources = import.meta.glob(['/src/lib/components/admin/*.svelte', '/src/lib/admin/*.ts', '/src/lib/api/admin.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

describe('administration messages', () => {
  it('uses only keys that exist in English and German', () => {
    const used = new Set<string>();
    for (const [path, src] of Object.entries(sources)) {
      if (path.endsWith('.test.ts')) continue;
      for (const m of src.matchAll(/['"`](admin\.[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)['"`]/g)) used.add(m[1]);
    }
    expect(used.size).toBeGreaterThan(100);
    const missing = [...used].filter((k) => !(k in EN) || !(k in DE));
    expect(missing).toEqual([]);
  });

  it('covers the keys that are built dynamically', () => {
    const dynamic = [
      ...ADMIN_ROLES.flatMap((r) => [`admin.role.${r}`, `admin.roleHint.${r}`]),
      ...ADMIN_TABS.map((t) => `admin.tabs.${t}`),
      ...['active', 'disabled', 'locked'].map((s) => `admin.status.${s}`),
      ...['open', 'used', 'expired'].map((s) => `admin.invites.status.${s}`),
    ];
    for (const k of dynamic) {
      expect(EN[k], k).toBeTruthy();
      expect(DE[k], k).toBeTruthy();
    }
  });

  it('has singular forms next to the plural keys that take a count', () => {
    for (const k of ['admin.users.count', 'admin.users.lockedNotice', 'admin.groups.memberCount', 'admin.audit.count', 'admin.invites.days']) {
      expect(EN[k], k).toBeTruthy();
      expect(EN[`${k}_one`], `${k}_one`).toBeTruthy();
      expect(DE[`${k}_one`], `${k}_one (de)`).toBeTruthy();
    }
  });

  it('names the settings page in the navigation', () => {
    expect(EN['settings.nav.admin']).toBeTruthy();
    expect(DE['settings.nav.admin']).toBeTruthy();
    expect(EN['settings.pages.admin.sections.manage']).toBeTruthy();
    expect(DE['settings.pages.admin.sections.manage']).toBeTruthy();
  });
});
