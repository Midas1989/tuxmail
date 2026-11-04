// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import en from '$locales/en.json';
import de from '$locales/de.json';
import { flatten } from '../i18n/core';
import type { MessageKey } from '../i18n/index.svelte';
import {
  ALL_ENV,
  LEGACY_SETTINGS,
  SETTINGS_CATEGORIES,
  navKey,
  normalizeLocation,
  normalizeText,
  pageDescKey,
  pageTitleKey,
  parseSettingsHash,
  resolveLegacy,
  searchSettings,
  sectionKey,
  settingsHash,
  visibleCategories,
  type SettingsEnv,
} from './catalog';
import { decideBack, parseDeepLink } from '../native/navigation';

const EN = flatten(en);
const DE = flatten(de);
const tEn = (k: MessageKey) => EN[k] ?? k;
const tDe = (k: MessageKey) => DE[k] ?? k;
const WEB: SettingsEnv = { desktop: false, native: false, team: false, vault: false };

describe('settings catalog', () => {
  it('has nav labels, page titles, descriptions and section titles in both languages', () => {
    for (const c of SETTINGS_CATEGORIES) {
      for (const k of [navKey(c.id), pageTitleKey(c.id), pageDescKey(c.id), ...c.sections.map((s) => sectionKey(c.id, s.id))]) {
        expect(EN[k], k).toBeTruthy();
        expect(DE[k], k).toBeTruthy();
      }
    }
  });

  it('only indexes existing i18n keys', () => {
    for (const c of SETTINGS_CATEGORIES) for (const s of c.sections) for (const k of s.keywords) expect(EN[k], `${c.id}/${s.id}: ${k}`).toBeTruthy();
  });

  it('uses short single-word-ish navigation labels (no wrapping in a 248px column)', () => {
    for (const c of SETTINGS_CATEGORIES) {
      expect(EN[navKey(c.id)].length, c.id).toBeLessThanOrEqual(20);
      expect(DE[navKey(c.id)].length, c.id).toBeLessThanOrEqual(20);
      expect(DE[navKey(c.id)]).not.toMatch(/­/);
    }
  });

  it('section ids are unique within a category', () => {
    for (const c of SETTINGS_CATEGORIES) expect(new Set(c.sections.map((s) => s.id)).size).toBe(c.sections.length);
  });

  it('hides categories and sections that do not apply', () => {
    const web = visibleCategories(WEB);
    expect(web.map((c) => c.id)).not.toContain('team');
    expect(web.find((c) => c.id === 'general')!.sections.map((s) => s.id)).toEqual(['language', 'appearance', 'shortcuts']);
    expect(web.find((c) => c.id === 'security')!.sections.map((s) => s.id)).not.toContain('vault');
    const all = visibleCategories(ALL_ENV);
    expect(all.map((c) => c.id)).toContain('team');
    expect(all.find((c) => c.id === 'general')!.sections.map((s) => s.id)).toContain('window');
  });
});

describe('settings routing', () => {
  it('parses deep links', () => {
    expect(parseSettingsHash('#/settings')).toEqual({ category: null, section: null });
    expect(parseSettingsHash('#/settings/')).toEqual({ category: null, section: null });
    expect(parseSettingsHash('#/settings/mail')).toEqual({ category: 'mail', section: null });
    expect(parseSettingsHash('#/settings/mail/remote')).toEqual({ category: 'mail', section: 'remote' });
    expect(parseSettingsHash('#/settings/accounts/sieve?x=1')).toEqual({ category: 'accounts', section: 'sieve' });
    expect(parseSettingsHash('#/settings/mail/nope')).toEqual({ category: 'mail', section: null });
    expect(parseSettingsHash('#/settings/unknown')).toEqual({ category: null, section: null });
    expect(parseSettingsHash('#/mail')).toEqual({ category: null, section: null });
  });

  it('redirects old anchors and aliases', () => {
    expect(parseSettingsHash('#/settings/set-tags')).toEqual({ category: 'automation', section: 'tags' });
    expect(parseSettingsHash('#/settings/diagnostics')).toEqual({ category: 'about', section: 'diagnostics' });
    expect(parseSettingsHash('#/settings/accounts/set-sieve')).toEqual({ category: 'accounts', section: 'sieve' });
    expect(resolveLegacy('set-diag')).toEqual({ category: 'about', section: 'diagnostics' });
    expect(resolveLegacy('set-folders')).toEqual({ category: 'accounts', section: 'folders' });
    expect(resolveLegacy('set-signin')).toEqual({ category: 'security', section: 'signin' });
    expect(resolveLegacy('set-ai')).toEqual({ category: 'ai', section: 'assistant' });
    expect(resolveLegacy('workflows')).toEqual({ category: 'automation', section: 'workflows' });
    expect(resolveLegacy('nothing')).toBeNull();
  });

  it('every legacy target exists', () => {
    for (const [k, [c, s]] of Object.entries(LEGACY_SETTINGS)) {
      const cat = SETTINGS_CATEGORIES.find((x) => x.id === c);
      expect(cat, k).toBeTruthy();
      if (s) expect(cat!.sections.some((x) => x.id === s), k).toBe(true);
    }
  });

  it('round-trips hashes', () => {
    for (const c of SETTINGS_CATEGORIES) {
      expect(parseSettingsHash(settingsHash({ category: c.id }))).toEqual({ category: c.id, section: null });
      for (const s of c.sections) expect(parseSettingsHash(settingsHash({ category: c.id, section: s.id }))).toEqual({ category: c.id, section: s.id });
    }
    expect(settingsHash()).toBe('#/settings');
  });

  it('normalises hidden categories to the overview', () => {
    expect(normalizeLocation({ category: 'team', section: 'mailboxes' }, WEB)).toEqual({ category: null, section: null });
    expect(normalizeLocation({ category: 'general', section: 'window' }, WEB)).toEqual({ category: 'general', section: null });
    expect(normalizeLocation({ category: 'general', section: 'window' }, ALL_ENV)).toEqual({ category: 'general', section: 'window' });
  });

  it('keeps settings sub-routes in native deep links; native settings and back still work', () => {
    expect(parseDeepLink('./#/settings/mail/remote')).toEqual({ hash: '#/settings/mail/remote' });
    expect(parseDeepLink('./#/settings')).toEqual({ hash: '#/settings' });
    expect(parseDeepLink('./#/native-settings')).toEqual({ hash: '#/native-settings' });
    expect(parseDeepLink('./#/settings/<script>')).toEqual({ hash: '#/settings' });
    expect(decideBack({ handledByUi: true, dialogOpen: false, replying: false, infoOpen: false, sidebarOpen: false, detailOpen: false, route: 'settings' })).toBe('handled');
  });
});

describe('settings search', () => {
  const tr = [tDe, tEn];

  it('normalises umlauts, case and soft hyphens', () => {
    expect(normalizeText('Größe Ä­b')).toBe('grosse ab');
  });

  it('finds section titles in German and English', () => {
    expect(searchSettings('signatur', WEB, tr)[0]).toMatchObject({ category: 'accounts', section: 'signatures' });
    expect(searchSettings('signature', WEB, tr)[0]).toMatchObject({ category: 'accounts', section: 'signatures' });
    expect(searchSettings('Abwesenheit', WEB, tr)[0]).toMatchObject({ category: 'mail', section: 'vacation' });
    expect(searchSettings('out of office', WEB, tr)[0]).toMatchObject({ category: 'mail', section: 'vacation' });
    expect(searchSettings('sieve', WEB, tr)[0]).toMatchObject({ category: 'accounts', section: 'sieve' });
  });

  it('finds key setting labels inside sections', () => {
    const hit = searchSettings('zeitzone', WEB, tr)[0];
    expect(hit).toMatchObject({ category: 'general', section: 'language' });
    expect(hit.match).toBe('settings.region.timeZone');
    expect(searchSettings('ruhezeit', WEB, tr)[0]).toMatchObject({ category: 'notifications' });
    expect(searchSettings('spam', WEB, tr)[0]).toMatchObject({ category: 'mail', section: 'reading' });
    expect(searchSettings('Dunkel', WEB, tr)[0]).toMatchObject({ category: 'general', section: 'appearance' });
  });

  it('requires every word and skips hidden sections', () => {
    expect(searchSettings('signatur xyzzy', WEB, tr)).toEqual([]);
    expect(searchSettings('', WEB, tr)).toEqual([]);
    expect(searchSettings('Titelleiste', WEB, tr)).toEqual([]);
    expect(searchSettings('Titelleiste', ALL_ENV, tr)[0]).toMatchObject({ category: 'general', section: 'window' });
    expect(searchSettings('team', WEB, tr).some((h) => h.category === 'team')).toBe(false);
  });
});
