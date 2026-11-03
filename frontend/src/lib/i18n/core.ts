// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Framework-free translation core.
//
// Language files: locales/<code>.json at the repository root (nested keys by
// area, plus a `meta` block). They are flattened to dotted keys here.
//
// Interpolation: {name}.
// Plurals: pass params.count; the translator looks for `<key>_<category>`
// where category is the Intl.PluralRules category of the active locale
// (zero, one, two, few, many, other) and falls back to `<key>` itself, which
// holds the general ("other") form. Example:
//   "count": "{count} messages", "count_one": "{count} message"
// Fallback chain: selected locale -> en (reference) -> key.

export type Dict = Record<string, string>;
export type Params = Record<string, string | number>;

export interface LocaleMeta {
  code: string;
  name: string;
  nativeName: string;
  direction: 'ltr' | 'rtl';
}

/** Flatten nested JSON into dotted keys (the `meta` block is skipped). */
export function flatten(obj: Record<string, unknown>, prefix = ''): Dict {
  const out: Dict = {};
  for (const [k, v] of Object.entries(obj)) {
    if (!prefix && k === 'meta') continue;
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === 'string') out[key] = v;
    else if (v && typeof v === 'object') Object.assign(out, flatten(v as Record<string, unknown>, key));
  }
  return out;
}

export function placeholders(s: string): string[] {
  return [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
}

export function interpolate(template: string, params?: Params): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (k in params ? String(params[k]) : m));
}

export type MissingHandler = (key: string, locale: string) => void;

export function translate(dict: Dict, fallback: Dict, locale: string, key: string, params?: Params, onMissing?: MissingHandler): string {
  let k = key;
  if (params && typeof params.count === 'number') {
    let cat = 'other';
    try {
      cat = new Intl.PluralRules(locale).select(params.count);
    } catch {
      /* unknown locale */
    }
    const candidate = `${key}_${cat}`;
    if (candidate in dict || (!(key in dict) && candidate in fallback)) k = candidate;
  }
  let template = dict[k];
  if (template === undefined) {
    onMissing?.(k, locale);
    template = fallback[k] ?? fallback[key] ?? key;
  }
  return interpolate(template, params);
}

export function createTranslator(dict: Dict, fallback: Dict, locale: string) {
  return (key: string, params?: Params) => translate(dict, fallback, locale, key, params);
}

/** Pick the best supported locale from navigator.languages-like input. */
export function negotiateLocale<L extends string>(requested: readonly string[], supported: readonly L[], fallback: L): L {
  for (const r of requested) {
    const lower = r.toLowerCase();
    const exact = supported.find((s) => s.toLowerCase() === lower);
    if (exact) return exact;
    const base = lower.split('-')[0];
    const hit = supported.find((s) => s.toLowerCase() === base);
    if (hit) return hit;
  }
  return fallback;
}

/** Deep-merge `over` onto `base` (for server-delivered override files). */
export function mergeDeep(base: Record<string, unknown>, over: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(over)) {
    const b = out[k];
    out[k] = v && typeof v === 'object' && !Array.isArray(v) && b && typeof b === 'object' ? mergeDeep(b as Record<string, unknown>, v as Record<string, unknown>) : v;
  }
  return out;
}
