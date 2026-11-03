// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { createTranslator, interpolate, negotiateLocale, translate } from './core';
import { de } from './de';
import { en } from './en';

describe('i18n core', () => {
  it('interpolates placeholders and leaves unknown ones', () => {
    expect(interpolate('Hallo {name}!', { name: 'Anna' })).toBe('Hallo Anna!');
    expect(interpolate('{a} {b}', { a: 1 })).toBe('1 {b}');
  });

  it('selects plural forms via Intl.PluralRules', () => {
    const tDe = createTranslator(de, de, 'de');
    expect(tDe('list.count', { count: 1 })).toBe('1 Nachricht');
    expect(tDe('list.count', { count: 5 })).toBe('5 Nachrichten');
    const tEn = createTranslator(en, de, 'en');
    expect(tEn('list.count', { count: 1 })).toBe('1 message');
    expect(tEn('list.count', { count: 0 })).toBe('0 messages');
  });

  it('falls back to German, then to the key', () => {
    expect(translate({}, de, 'en', 'nav.mail')).toBe('Mail');
    expect(translate({}, {}, 'de', 'does.not.exist')).toBe('does.not.exist');
  });

  it('negotiates the locale', () => {
    expect(negotiateLocale(['en-US', 'de'], ['de', 'en'], 'de')).toBe('en');
    expect(negotiateLocale(['fr-FR'], ['de', 'en'], 'de')).toBe('de');
    expect(negotiateLocale([], ['de', 'en'], 'de')).toBe('de');
  });
});

describe('dictionaries', () => {
  it('English covers every German key', () => {
    const missing = Object.keys(de).filter((k) => !(k in en));
    expect(missing).toEqual([]);
    const extra = Object.keys(en).filter((k) => !(k in de));
    expect(extra).toEqual([]);
  });

  it('placeholders match between languages', () => {
    const ph = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',');
    for (const k of Object.keys(de) as (keyof typeof de)[]) {
      expect(ph(en[k]), k).toBe(ph(de[k]));
    }
  });

  it('has no empty strings', () => {
    for (const [k, v] of Object.entries(de)) expect(v.trim(), k).not.toBe('');
    for (const [k, v] of Object.entries(en)) expect(v.trim(), k).not.toBe('');
  });
});
