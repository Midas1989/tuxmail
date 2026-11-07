// SPDX-License-Identifier: AGPL-3.0-or-later
//
// UI preferences. Theme, density and the last view are per device
// (localStorage, a convenience only). Language and the "Language & region"
// settings are user preferences synced with the core via
// Api.getPreferences/setPreferences; localStorage only caches them so the
// first paint is already in the right language.

import { i18n } from '../i18n/index.svelte';
import { DEFAULT_REGION } from '../format';
import { isWails } from '../platform';
import type { Api } from '../api/api';
import type { ConversationGrouping, RegionSettings, UserPreferences } from '../types';

export type Theme = 'system' | 'light' | 'dark';
export type Density = 'compact' | 'normal' | 'comfortable';
export type ViewMode = 'classic' | 'hybrid' | 'chat' | 'terminal';

interface Stored {
  theme: Theme;
  density: Density;
  /** 'system' or a language code. */
  locale: string;
  region: RegionSettings;
  grouping: ConversationGrouping;
  viewMode: ViewMode;
  folderViewModes?: Record<string, ViewMode>;
  hybridOnlyFolder?: boolean;
  chatEnterSends: boolean;
  chatSignatures: 'collapsed' | 'shown';
}

const KEY = 'tuxmail.prefs.v2';

function load(): Partial<Stored> {
  try {
    const raw = localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Partial<Stored>) : {};
  } catch {
    return {};
  }
}

class Prefs {
  theme = $state<Theme>('system');
  density = $state<Density>('normal');
  /** Stored choice: 'system' (follow navigator.languages) or a code. */
  locale = $state<string>('system');
  region = $state<RegionSettings>({ ...DEFAULT_REGION });
  grouping = $state<ConversationGrouping>('person');
  viewMode = $state<ViewMode>('classic');
  /** Per-folder default view (key: SidebarSel key such as "f:17", "u:inbox", "t:3"). */
  folderViewModes = $state<Record<string, ViewMode>>({});
  /** Hybrid view: show only the messages of the selected folder. */
  hybridOnlyFolder = $state(false);
  /** Messenger composer: Enter sends (Shift+Enter = newline); off = Ctrl+Enter sends. */
  chatEnterSends = $state(true);
  /** Signatures in the chat view: collapsed (default, chevron per bubble) or shown. */
  chatSignatures = $state<'collapsed' | 'shown'>('collapsed');
  systemDark = $state(false);
  private api: Api | null = null;
  private syncTimer: ReturnType<typeof setTimeout> | null = null;

  constructor() {
    const s = load();
    this.theme = s.theme ?? 'system';
    this.density = s.density ?? 'normal';
    this.locale = s.locale ?? 'system';
    this.region = { ...DEFAULT_REGION, ...(s.region ?? {}) };
    this.grouping = s.grouping ?? 'person';
    this.viewMode = s.viewMode ?? 'classic';
    this.folderViewModes = s.folderViewModes ?? {};
    this.hybridOnlyFolder = s.hybridOnlyFolder ?? false;
    this.chatEnterSends = s.chatEnterSends ?? true;
    this.chatSignatures = s.chatSignatures ?? 'collapsed';
    this.applyI18n();
    if (typeof matchMedia !== 'undefined') {
      const mq = matchMedia('(prefers-color-scheme: dark)');
      this.systemDark = mq.matches;
      mq.addEventListener?.('change', (e) => (this.systemDark = e.matches));
    }
  }

  get effectiveTheme(): 'light' | 'dark' {
    return this.theme === 'system' ? (this.systemDark ? 'dark' : 'light') : this.theme;
  }

  private applyI18n() {
    i18n.locale = i18n.resolve(this.locale);
    i18n.region = this.region;
  }

  /** Load the synced preferences and any extra languages from the core. */
  async connect(api: Api): Promise<void> {
    this.api = api;
    try {
      const extra = await api.listLocales();
      for (const l of extra) {
        try {
          i18n.register(l.code, await api.getLocale(l.code), l);
        } catch {
          /* ignore broken language files */
        }
      }
    } catch {
      /* core without language endpoint */
    }
    try {
      const p = await api.getPreferences();
      this.locale = p.locale || 'system';
      this.region = { ...DEFAULT_REGION, ...p.region };
      if (isWails() && p.view) this.applyView(p.view);
      this.applyI18n();
      this.save(false);
    } catch {
      /* keep local values */
    }
  }

  /** View settings from the settings synchronisation of the desktop app. */
  private applyView(v: NonNullable<UserPreferences['view']>) {
    if (v.density) this.density = v.density as Density;
    if (v.viewMode) this.viewMode = v.viewMode as ViewMode;
    if (v.grouping) this.grouping = v.grouping as ConversationGrouping;
    if (v.hybridOnlyFolder !== undefined) this.hybridOnlyFolder = v.hybridOnlyFolder;
    if (v.chatEnterSends !== undefined) this.chatEnterSends = v.chatEnterSends;
    if (v.chatSignatures) this.chatSignatures = v.chatSignatures as 'collapsed' | 'shown';
  }

  setLocale(code: string) {
    this.locale = code;
    this.applyI18n();
    this.save();
  }

  setRegion(patch: Partial<RegionSettings>) {
    this.region = { ...this.region, ...patch };
    this.applyI18n();
    this.save();
  }

  save(sync = true) {
    try {
      const s: Stored = {
        theme: this.theme,
        density: this.density,
        locale: this.locale,
        region: $state.snapshot(this.region) as RegionSettings,
        grouping: this.grouping,
        viewMode: this.viewMode,
        folderViewModes: $state.snapshot(this.folderViewModes),
        hybridOnlyFolder: this.hybridOnlyFolder,
        chatEnterSends: this.chatEnterSends,
        chatSignatures: this.chatSignatures,
      };
      localStorage.setItem(KEY, JSON.stringify(s));
    } catch {
      /* storage unavailable (private mode, blocked) */
    }
    if (sync && this.api) {
      if (this.syncTimer) clearTimeout(this.syncTimer);
      const api = this.api;
      const payload: UserPreferences = { locale: this.locale, region: $state.snapshot(this.region) as RegionSettings };
      if (isWails()) {
        payload.view = { density: this.density, viewMode: this.viewMode, grouping: this.grouping, hybridOnlyFolder: this.hybridOnlyFolder, chatEnterSends: this.chatEnterSends, chatSignatures: this.chatSignatures };
      }
      this.syncTimer = setTimeout(() => void api.setPreferences(payload).catch(() => {}), 400);
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
