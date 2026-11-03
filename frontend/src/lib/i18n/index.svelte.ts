// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Reactive i18n for the UI. Language files live in /locales (repo root),
// imported through the `$locales` Vite alias. English is the reference and
// the fallback: selected -> en -> key. Extra or override languages can be
// delivered by the core at runtime (Api.listLocales/getLocale) and are merged
// on top of the built-in files.

import en from '$locales/en.json';
import de from '$locales/de.json';
import { flatten, mergeDeep, negotiateLocale, translate, type Dict, type LocaleMeta, type Params } from './core';
import { DEFAULT_REGION, Formatter } from '../format';
import type { RegionSettings } from '../types';

type Leaves<T, P extends string = ''> = {
  [K in keyof T & string]: T[K] extends string ? `${P}${K}` : Leaves<T[K], `${P}${K}.`>;
}[keyof T & string];

/** Every key of the reference file (locales/en.json), e.g. "mail.list.count". */
export type MessageKey = Leaves<Omit<typeof en, 'meta'>>;

export const REFERENCE_LOCALE = 'en';

const builtinRaw: Record<string, Record<string, unknown>> = { en, de };

function metaOf(raw: Record<string, unknown>): LocaleMeta {
  return raw.meta as LocaleMeta;
}

const state = $state<{
  locale: string;
  dicts: Record<string, Dict>;
  metas: Record<string, LocaleMeta>;
  region: RegionSettings;
}>({
  locale: REFERENCE_LOCALE,
  dicts: Object.fromEntries(Object.entries(builtinRaw).map(([c, r]) => [c, flatten(r)])),
  metas: Object.fromEntries(Object.entries(builtinRaw).map(([c, r]) => [c, metaOf(r)])),
  region: { ...DEFAULT_REGION },
});

const reportedMissing = new Set<string>();
function onMissing(key: string, locale: string) {
  if (!import.meta.env.DEV || locale === REFERENCE_LOCALE) return;
  const id = `${locale}:${key}`;
  if (reportedMissing.has(id)) return;
  reportedMissing.add(id);
  console.warn(`[i18n] missing key "${key}" in locale "${locale}"`);
}

export const i18n = {
  get locale(): string {
    return state.locale;
  },
  set locale(code: string) {
    state.locale = state.dicts[code] ? code : REFERENCE_LOCALE;
    if (typeof document !== 'undefined') {
      document.documentElement.lang = state.locale;
      document.documentElement.dir = state.metas[state.locale]?.direction ?? 'ltr';
    }
  },
  get region(): RegionSettings {
    return state.region;
  },
  set region(r: RegionSettings) {
    state.region = { ...DEFAULT_REGION, ...r };
  },
  /** Available languages (built-in + delivered by the core). */
  get locales(): LocaleMeta[] {
    return Object.values(state.metas).sort((a, b) => a.nativeName.localeCompare(b.nativeName));
  },
  /** Merge a language file (nested JSON) on top of the built-in one. */
  register(code: string, raw: Record<string, unknown>, meta?: LocaleMeta) {
    const merged = mergeDeep(builtinRaw[code] ?? {}, raw);
    state.dicts[code] = flatten(merged);
    state.metas[code] = meta ?? (merged.meta as LocaleMeta) ?? { code, name: code, nativeName: code, direction: 'ltr' };
  },
  /** Resolve "system" or a stored code against navigator.languages. */
  resolve(pref: string, requested: readonly string[] = typeof navigator !== 'undefined' ? (navigator.languages ?? [navigator.language]) : []): string {
    const codes = Object.keys(state.dicts);
    if (pref && pref !== 'system' && codes.includes(pref)) return pref;
    return negotiateLocale(requested, codes, REFERENCE_LOCALE);
  },
};

/** Reactive translation (re-evaluated in templates when the locale changes). */
export function t(key: MessageKey, params?: Params): string {
  return translate(state.dicts[state.locale] ?? {}, state.dicts[REFERENCE_LOCALE], state.locale, key, params, onMissing);
}

// ---------------------------------------------------------------------------
// Formatting: one Formatter per (locale, region settings), see ../format.ts
// ---------------------------------------------------------------------------

let cache: { key: string; f: Formatter } | null = null;

export function fmt(): Formatter {
  const key = state.locale + '|' + JSON.stringify(state.region);
  if (!cache || cache.key !== key) cache = { key, f: new Formatter(state.locale, state.region) };
  return cache.f;
}

export const formatListDate = (iso: string) => fmt().listDate(iso);
export const formatTime = (iso: string) => fmt().time(iso);
export const formatFullDate = (iso: string) => fmt().full(iso);
export const formatDay = (iso: string) => fmt().day(iso);
export const formatSize = (bytes: number) => fmt().size(bytes);
export const formatNumber = (n: number) => fmt().number(n);
