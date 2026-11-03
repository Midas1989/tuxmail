// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it, vi } from 'vitest';
import en from '$locales/en.json';
import de from '$locales/de.json';
import { createTranslator, flatten, interpolate, mergeDeep, negotiateLocale, placeholders, translate } from './core';

const EN = flatten(en);
const DE = flatten(de);

describe('language files (locales/*.json)', () => {
  it('English is the reference and German has exactly the same keys', () => {
    const missing = Object.keys(EN).filter((k) => !(k in DE));
    const extra = Object.keys(DE).filter((k) => !(k in EN));
    expect(missing).toEqual([]);
    expect(extra).toEqual([]);
    expect(en.meta.reference).toBe(true);
  });

  it('uses the same {placeholders} per key', () => {
    for (const k of Object.keys(EN)) expect(placeholders(DE[k]), k).toEqual(placeholders(EN[k]));
  });

  it('has a meta block and no empty strings', () => {
    for (const file of [en, de]) {
      expect(file.meta).toMatchObject({ code: expect.any(String), name: expect.any(String), nativeName: expect.any(String), direction: 'ltr' });
    }
    for (const [k, v] of [...Object.entries(EN), ...Object.entries(DE)]) expect(v.trim(), k).not.toBe('');
  });

  it('keeps the required top-level areas', () => {
    const areas = Object.keys(en).filter((k) => k !== 'meta');
    for (const a of ['common', 'nav', 'mail', 'chat', 'calendar', 'contacts', 'files', 'settings', 'search', 'compose', 'workflow', 'errors', 'notifications', 'sendcheck', 'onboarding']) {
      expect(areas).toContain(a);
    }
  });

  it('plural variants only use Intl.PluralRules categories', () => {
    const cats = new Set(['zero', 'one', 'two', 'few', 'many', 'other']);
    for (const k of Object.keys(EN)) {
      const m = /_([a-z]+)$/.exec(k);
      if (m && cats.has(m[1])) expect(k.replace(/_[a-z]+$/, '') in EN, `${k} needs a base key`).toBe(true);
    }
  });
});

describe('translator', () => {
  it('interpolates placeholders and leaves unknown ones', () => {
    expect(interpolate('Hello {name}!', { name: 'Anna' })).toBe('Hello Anna!');
    expect(interpolate('{a} {b}', { a: 1 })).toBe('1 {b}');
  });

  it('selects plural forms via Intl.PluralRules (key_one / key)', () => {
    const tEn = createTranslator(EN, EN, 'en');
    expect(tEn('mail.list.count', { count: 1 })).toBe('1 message');
    expect(tEn('mail.list.count', { count: 0 })).toBe('0 messages');
    const tDe = createTranslator(DE, EN, 'de');
    expect(tDe('mail.list.count', { count: 1 })).toBe('1 Nachricht');
    expect(tDe('mail.list.count', { count: 7 })).toBe('7 Nachrichten');
  });

  it('falls back selected -> en -> key and reports missing keys', () => {
    const missing = vi.fn();
    expect(translate({}, EN, 'de', 'nav.mail', undefined, missing)).toBe('Mail');
    expect(missing).toHaveBeenCalledWith('nav.mail', 'de');
    expect(translate({}, {}, 'de', 'does.not.exist')).toBe('does.not.exist');
  });

  it('negotiates the locale with English as default', () => {
    expect(negotiateLocale(['de-AT', 'en'], ['en', 'de'], 'en')).toBe('de');
    expect(negotiateLocale(['fr-FR', 'it'], ['en', 'de'], 'en')).toBe('en');
    expect(negotiateLocale([], ['en', 'de'], 'en')).toBe('en');
  });

  it('merges server-delivered overrides on top of built-ins', () => {
    const merged = flatten(mergeDeep(en as unknown as Record<string, unknown>, { nav: { mail: 'Inbox' } }));
    expect(merged['nav.mail']).toBe('Inbox');
    expect(merged['nav.chats']).toBe(EN['nav.chats']);
  });
});
