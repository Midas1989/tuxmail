// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Framework-free translation core. Dictionaries are flat key -> string maps.
// Placeholders use {name}. Plurals: a key "x" may have variants "x#one" and
// "x#other", selected via Intl.PluralRules when params.count is given.

export type Dict = Record<string, string>;
export type Params = Record<string, string | number>;

export function interpolate(template: string, params?: Params): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in params ? String(params[k]) : m));
}

export function translate(dict: Dict, fallback: Dict, locale: string, key: string, params?: Params): string {
  let k = key;
  if (params && typeof params.count === 'number') {
    const form = new Intl.PluralRules(locale).select(params.count);
    const candidate = `${key}#${form}`;
    if (candidate in dict || candidate in fallback) k = candidate;
    else if (`${key}#other` in dict || `${key}#other` in fallback) k = `${key}#other`;
  }
  const template = dict[k] ?? fallback[k] ?? key;
  return interpolate(template, params);
}

export function createTranslator(dict: Dict, fallback: Dict, locale: string) {
  return (key: string, params?: Params) => translate(dict, fallback, locale, key, params);
}

/** Pick the best supported locale from navigator.languages-like input. */
export function negotiateLocale<L extends string>(requested: readonly string[], supported: readonly L[], fallback: L): L {
  for (const r of requested) {
    const base = r.toLowerCase().split('-')[0];
    const hit = supported.find((s) => s === base);
    if (hit) return hit;
  }
  return fallback;
}
