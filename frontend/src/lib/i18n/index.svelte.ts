// SPDX-License-Identifier: AGPL-3.0-or-later
import { translate, type Params } from './core';
import { de, type MessageKey } from './de';
import { en } from './en';

export type Locale = 'de' | 'en';
export const LOCALES: readonly Locale[] = ['de', 'en'];
export const dictionaries: Record<Locale, Record<MessageKey, string>> = { de, en };

const state = $state<{ locale: Locale }>({ locale: 'de' });

export const i18n = {
  get locale(): Locale {
    return state.locale;
  },
  set locale(l: Locale) {
    state.locale = l;
    if (typeof document !== 'undefined') document.documentElement.lang = l;
  },
};

/** Reactive translation (re-evaluated in templates when the locale changes). */
export function t(key: MessageKey, params?: Params): string {
  return translate(dictionaries[state.locale], de, state.locale, key, params);
}

function intlLocale(): string {
  return state.locale === 'de' ? 'de-DE' : 'en-GB';
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** Compact date for lists: time today, weekday this week, else date. */
export function formatListDate(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const loc = intlLocale();
  if (sameDay(d, now)) return d.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' });
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(d, yesterday)) return t('date.yesterday');
  const diffDays = (now.getTime() - d.getTime()) / 86400_000;
  if (diffDays < 7) return d.toLocaleDateString(loc, { weekday: 'short' });
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString(loc, { day: '2-digit', month: '2-digit' });
  return d.toLocaleDateString(loc, { day: '2-digit', month: '2-digit', year: '2-digit' });
}

export function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString(intlLocale(), { hour: '2-digit', minute: '2-digit' });
}

export function formatFullDate(iso: string): string {
  return new Date(iso).toLocaleString(intlLocale(), { weekday: 'short', day: '2-digit', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' });
}

export function formatDay(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (sameDay(d, now)) return t('date.today');
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (sameDay(d, y)) return t('date.yesterday');
  return d.toLocaleDateString(intlLocale(), { weekday: 'long', day: 'numeric', month: 'long' });
}

export function formatSize(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toLocaleString(intlLocale(), { maximumFractionDigits: i === 0 ? 0 : 1 })} ${units[i]}`;
}

export type { MessageKey };
