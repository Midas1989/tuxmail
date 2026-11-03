// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Per-device UI preferences (theme, density, language). Stored in
// localStorage as a convenience only; the authoritative account settings
// live in the core.

import { i18n, LOCALES, type Locale } from '../i18n/index.svelte';
import { negotiateLocale } from '../i18n/core';
import type { ConversationGrouping } from '../types';

export type Theme = 'system' | 'light' | 'dark';
export type Density = 'compact' | 'normal' | 'comfortable';

interface Stored {
  theme: Theme;
  density: Density;
  locale: Locale;
  grouping: ConversationGrouping;
  viewMode: 'classic' | 'chat';
}

const KEY = 'tuxmail.prefs.v1';

function load(): Partial<Stored> {
  try {
    const raw = localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Partial<Stored>) : {};
  } catch {
    return {};
  }
}

function initialLocale(): Locale {
  const langs = typeof navigator !== 'undefined' ? navigator.languages ?? [navigator.language] : [];
  // German is the default; English only when the browser clearly prefers it.
  return negotiateLocale(langs.slice(0, 1), LOCALES, 'de');
}

class Prefs {
  theme = $state<Theme>('system');
  density = $state<Density>('normal');
  locale = $state<Locale>('de');
  grouping = $state<ConversationGrouping>('person');
  viewMode = $state<'classic' | 'chat'>('classic');
  systemDark = $state(false);

  constructor() {
    const s = load();
    this.theme = s.theme ?? 'system';
    this.density = s.density ?? 'normal';
    this.locale = s.locale ?? initialLocale();
    this.grouping = s.grouping ?? 'person';
    this.viewMode = s.viewMode ?? 'classic';
    i18n.locale = this.locale;
    if (typeof matchMedia !== 'undefined') {
      const mq = matchMedia('(prefers-color-scheme: dark)');
      this.systemDark = mq.matches;
      mq.addEventListener?.('change', (e) => (this.systemDark = e.matches));
    }
  }

  get effectiveTheme(): 'light' | 'dark' {
    return this.theme === 'system' ? (this.systemDark ? 'dark' : 'light') : this.theme;
  }

  setLocale(l: Locale) {
    this.locale = l;
    i18n.locale = l;
    this.save();
  }

  save() {
    try {
      const s: Stored = { theme: this.theme, density: this.density, locale: this.locale, grouping: this.grouping, viewMode: this.viewMode };
      localStorage.setItem(KEY, JSON.stringify(s));
    } catch {
      /* storage unavailable (private mode, blocked) */
    }
  }

  /** Apply theme/density to <html>. Called from an effect in App.svelte. */
  apply(root: HTMLElement) {
    root.dataset.theme = this.effectiveTheme;
    root.dataset.density = this.density;
    root.style.colorScheme = this.effectiveTheme;
    // Keep the PWA status bar in sync with an explicit theme choice.
    const bg = getComputedStyle(root).getPropertyValue('--tm-surface').trim();
    if (bg) for (const m of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) m.content = bg;
  }
}

export const prefs = new Prefs();
