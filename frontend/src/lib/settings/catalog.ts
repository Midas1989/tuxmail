// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Information architecture of the settings screen: categories (left
// navigation), their sections (right pane), deep links
// #/settings/<category>/<section>, the redirects of old anchors and links
// (#/settings, set-tags, set-diag …) and the settings search, which indexes
// section titles plus key setting labels in every built-in language.
//
// Pure module (no runes) so routing and search are unit-testable.

import type { MessageKey } from '../i18n/index.svelte';

export type SettingsCategoryId = 'general' | 'accounts' | 'mail' | 'chats' | 'pim' | 'cloud' | 'automation' | 'notifications' | 'ai' | 'security' | 'data' | 'team' | 'about';

/** Where a category or section applies; anything else is hidden, not disabled. */
export type SettingsAvailability = 'desktop' | 'native' | 'team' | 'vault';

export interface SettingsEnv {
  /** Wails desktop shell (window settings). */
  desktop: boolean;
  /** Capacitor app (app lock, push, server profile in #/native-settings). */
  native: boolean;
  /** Server mode with team mailboxes the user administers or manages. */
  team: boolean;
  /** Portable desktop with a master-password vault. */
  vault: boolean;
}

export const ALL_ENV: SettingsEnv = { desktop: true, native: true, team: true, vault: true };

export interface SettingsSection {
  id: string;
  /** Sub-heading the section is listed under (e.g. "Lesen", "Erweitert"). */
  group?: 'reading' | 'compose' | 'absence' | 'advanced';
  only?: SettingsAvailability;
  /** Labels of settings inside the section (existing i18n keys) for the search. */
  keywords: MessageKey[];
}

export interface SettingsCategory {
  id: SettingsCategoryId;
  only?: SettingsAvailability;
  sections: SettingsSection[];
}

const sec = (id: string, keywords: MessageKey[], extra: Partial<SettingsSection> = {}): SettingsSection => ({ id, keywords, ...extra });

export const SETTINGS_CATEGORIES: readonly SettingsCategory[] = [
  {
    id: 'general',
    sections: [
      sec('language', ['settings.language', 'settings.region.title', 'settings.region.timeZone', 'settings.region.dateFormat', 'settings.region.timeFormat', 'settings.region.numberFormat', 'settings.region.weekStart', 'settings.region.secondaryTz', 'settings.region.relative']),
      sec('appearance', ['settings.appearance', 'settings.theme', 'settings.themes.dark', 'settings.themes.light', 'settings.density', 'settings.defaultView', 'mail.view.chat', 'mail.view.hybrid', 'mail.view.classic']),
      sec('window', ['settings.window.title', 'settings.window.nativeTitleBar'], { only: 'desktop' }),
      sec('shortcuts', ['settings.shortcuts', 'settings.shortcut.palette', 'settings.shortcut.compose', 'settings.shortcut.search']),
      sec('app', ['native.settings.title', 'native.settings.push', 'native.settings.server'], { only: 'native' }),
    ],
  },
  {
    id: 'accounts',
    sections: [
      sec('list', ['settings.accounts', 'settings.addAccount', 'messenger.add', 'settings.identities.add', 'settings.identities.sendAs']),
      sec('signatures', ['settings.signatures.title', 'settings.signatures.add', 'settings.signatures.default', 'settings.signatures.reply']),
      sec('shared', ['cloud.shared.title', 'cloud.shared.add', 'cloud.identities.title', 'cloud.shared.sendMode']),
      sec('folders', ['settings.folders.title', 'settings.folders.behaviour', 'settings.folders.saveSent', 'settings.folders.trash', 'settings.folders.archive', 'settings.folders.identities']),
      sec('server', ['onboarding.connect.cardTitle', 'onboarding.connect.title', 'onboarding.connect.modeThin', 'onboarding.connect.modeLocal']),
      sec('sieve', ['settings.sieve.title', 'settings.sieve.scripts', 'settings.sieve.syncWorkflows', 'settings.sieve.advanced'], { group: 'advanced' }),
    ],
  },
  {
    id: 'mail',
    sections: [
      sec('reading', ['comfort.settings.spam', 'comfort.settings.spamEnabled', 'comfort.settings.moveToJunk', 'comfort.settings.categories', 'comfort.settings.newsletter', 'comfort.subscriptions.open'], { group: 'reading' }),
      sec('remote', ['settings.remote.title', 'settings.remote.add'], { group: 'reading' }),
      sec('compose', ['comfort.settings.spell', 'comfort.settings.userDictionary', 'comfort.settings.meeting', 'comfort.settings.jitsiUrl', 'comfort.settings.mergePdf', 'comfort.settings.pdfName'], { group: 'compose' }),
      sec('snippets', ['chat.snippets.title'], { group: 'compose' }),
      sec('vacation', ['settings.vacation.title', 'settings.vacation.enabled', 'settings.vacation.forwardTo'], { group: 'absence' }),
    ],
  },
  {
    id: 'chats',
    sections: [
      sec('messenger', ['messenger.title', 'messenger.add', 'messenger.riskTitle', 'messenger.experimental', 'messenger.official']),
      sec('chat', ['settings.chat.title', 'settings.chat.enterSends', 'settings.chat.signatureDisplay', 'settings.chat.signatures']),
      sec('folders', ['chat.folders.label', 'chat.folders.new']),
      sec('focus', ['chat.focus.title', 'chat.focus.vip', 'chat.focus.schedule']),
    ],
  },
  {
    id: 'pim',
    sections: [
      sec('calendars', ['calendar.calendars', 'calendar.addCalendar', 'calendar.subscribe', 'calendar.dav.title', 'calendar.birthday', 'calendar.refreshEvery']),
      sec('contacts', ['contacts.books', 'contacts.newBook']),
      sec('cloudsync', ['cloud.pim.sync']),
    ],
  },
  {
    id: 'cloud',
    sections: [
      sec('storage', ['files.providers', 'files.addAccount', 'cloud.provider.nextcloud', 'cloud.provider.opencloud', 'cloud.provider.webdav', 'cloud.provider.vaultwarden', 'files.removeAccount']),
      sec('shares', ['files.myShares', 'files.share']),
    ],
  },
  {
    id: 'automation',
    sections: [
      sec('workflows', ['settings.workflows.title', 'settings.workflows.intro']),
      sec('tags', ['settings.tags.title', 'settings.tags.new', 'settings.tags.merge', 'organize.tags.manage']),
    ],
  },
  {
    id: 'notifications',
    sections: [sec('notifications', ['notifications.title', 'notifications.enable', 'notifications.quietHours', 'notifications.privacy', 'notifications.channels', 'notifications.accounts', 'notifications.test'])],
  },
  {
    id: 'ai',
    sections: [sec('assistant', ['settings.ai.title', 'settings.ai.provider', 'settings.ai.model', 'settings.ai.apiKey', 'settings.ai.accounts', 'settings.ai.mask'])],
  },
  {
    id: 'security',
    sections: [
      sec('smime', ['comfort.smime.title', 'comfort.smime.importNow', 'comfort.smime.signByDefault', 'comfort.smime.encryptAuto', 'comfort.smime.preferPgp']),
      sec('signin', ['cloud.services.title', 'cloud.services.clientId', 'cloud.services.tenant']),
      sec('vault', ['vault.password', 'vault.unlockTitle'], { only: 'vault' }),
      sec('applock', ['native.settings.appLock', 'native.settings.lockAfter'], { only: 'native' }),
      sec('privacy', ['settings.remote.title', 'notifications.privacy', 'settings.ai.mask', 'settings.passkeys.title']),
    ],
  },
  {
    id: 'data',
    sections: [
      sec('import', ['importExport.title', 'importExport.import', 'importExport.export', 'importExport.tbAccounts']),
      sec('backup', ['backup.title', 'backup.tabRestore', 'backup.schedules', 'backup.encryption']),
    ],
  },
  {
    id: 'team',
    only: 'team',
    sections: [sec('mailboxes', ['team.admin.title', 'team.admin.create', 'team.admin.role', 'team.admin.member'])],
  },
  {
    id: 'about',
    sections: [
      sec('about', ['settings.about', 'settings.diag.version']),
      sec('diagnostics', ['settings.diag.title', 'settings.diag.export', 'settings.diag.logFile', 'settings.diag.dataDir', 'settings.diag.lastError']),
      sec('licenses', []),
    ],
  },
];

const available = (only: SettingsAvailability | undefined, env: SettingsEnv) => !only || env[only];

export function visibleCategories(env: SettingsEnv): SettingsCategory[] {
  return SETTINGS_CATEGORIES.filter((c) => available(c.only, env)).map((c) => ({ ...c, sections: c.sections.filter((s) => available(s.only, env)) }));
}

export function findCategory(id: string | null | undefined, env: SettingsEnv = ALL_ENV): SettingsCategory | undefined {
  return visibleCategories(env).find((c) => c.id === id);
}

// ---------------------------------------------------------------------------
// i18n keys of the IA (settings.nav.* / settings.pages.*)
// ---------------------------------------------------------------------------

export const navKey = (c: SettingsCategoryId) => `settings.nav.${c}` as MessageKey;
export const pageTitleKey = (c: SettingsCategoryId) => `settings.pages.${c}.title` as MessageKey;
export const pageDescKey = (c: SettingsCategoryId) => `settings.pages.${c}.desc` as MessageKey;
export const sectionKey = (c: SettingsCategoryId, s: string) => `settings.pages.${c}.sections.${s}` as MessageKey;
export const groupKey = (g: NonNullable<SettingsSection['group']>) => `settings.pages.groups.${g}` as MessageKey;

/** DOM id of a section wrapper (scroll target / highlight). */
export const sectionDomId = (c: SettingsCategoryId, s: string) => `settings-${c}-${s}`;

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

export interface SettingsLocation {
  category: SettingsCategoryId | null;
  section: string | null;
}

export const SETTINGS_HOME: SettingsLocation = { category: null, section: null };

/**
 * Old anchors of the single-page settings (app.settingsAnchor, ids such as
 * "set-tags") and friendly aliases, mapped to the new category/section.
 */
export const LEGACY_SETTINGS: Readonly<Record<string, [SettingsCategoryId, string | null]>> = {
  'set-accounts': ['accounts', 'list'],
  'set-folders': ['accounts', 'folders'],
  'set-server': ['accounts', 'server'],
  'set-signatures': ['accounts', 'signatures'],
  'set-sieve': ['accounts', 'sieve'],
  'set-team': ['team', 'mailboxes'],
  'set-messenger': ['chats', 'messenger'],
  'set-chat': ['chats', 'chat'],
  'set-notify': ['notifications', 'notifications'],
  'set-region': ['general', 'language'],
  'set-look': ['general', 'appearance'],
  'set-keys': ['general', 'shortcuts'],
  'set-snippets': ['mail', 'snippets'],
  'set-comfort': ['mail', 'reading'],
  'set-vacation': ['mail', 'vacation'],
  'set-remote': ['mail', 'remote'],
  'set-ai': ['ai', 'assistant'],
  'set-signin': ['security', 'signin'],
  'set-smime': ['security', 'smime'],
  'set-tags': ['automation', 'tags'],
  'set-workflows': ['automation', 'workflows'],
  'set-import-export': ['data', 'import'],
  'set-backup': ['data', 'backup'],
  'set-diag': ['about', 'diagnostics'],
  'set-about': ['about', 'about'],
  // aliases
  diagnostics: ['about', 'diagnostics'],
  diag: ['about', 'diagnostics'],
  info: ['about', null],
  tags: ['automation', 'tags'],
  workflows: ['automation', 'workflows'],
  sieve: ['accounts', 'sieve'],
  signatures: ['accounts', 'signatures'],
  folders: ['accounts', 'folders'],
  messenger: ['chats', 'messenger'],
  vacation: ['mail', 'vacation'],
  backup: ['data', 'backup'],
  import: ['data', 'import'],
  region: ['general', 'language'],
  language: ['general', 'language'],
  appearance: ['general', 'appearance'],
  native: ['general', 'app'],
  calendar: ['pim', 'calendars'],
  contacts: ['pim', 'contacts'],
  files: ['cloud', 'storage'],
  privacy: ['security', 'privacy'],
};

function isCategory(id: string): id is SettingsCategoryId {
  return SETTINGS_CATEGORIES.some((c) => c.id === id);
}

function sectionOf(c: SettingsCategoryId, s: string | null | undefined): string | null {
  if (!s) return null;
  return SETTINGS_CATEGORIES.find((x) => x.id === c)?.sections.some((x) => x.id === s) ? s : null;
}

/** Resolve an old anchor / alias ("set-tags", "diagnostics") to a location. */
export function resolveLegacy(id: string | null | undefined): SettingsLocation | null {
  if (!id) return null;
  const key = id.toLowerCase().replace(/^#/, '');
  const hit = LEGACY_SETTINGS[key] ?? LEGACY_SETTINGS['set-' + key];
  return hit ? { category: hit[0], section: hit[1] } : null;
}

/**
 * #/settings                       → overview (phones: category list)
 * #/settings/mail                  → category
 * #/settings/mail/remote           → section
 * #/settings/set-tags, /tags, /diagnostics … → redirects
 * Anything unknown falls back to the overview.
 */
export function parseSettingsHash(hash: string): SettingsLocation {
  const path = hash.replace(/^#\/?/, '').split('?')[0];
  const segs = path.split('/').filter(Boolean).map((s) => decodeURIComponent(s));
  if (segs[0] !== 'settings') return { ...SETTINGS_HOME };
  const [, a, b] = segs;
  if (!a) return { ...SETTINGS_HOME };
  if (isCategory(a)) {
    return { category: a, section: sectionOf(a, b) ?? resolveSectionAlias(a, b) };
  }
  return resolveLegacy(a) ?? { ...SETTINGS_HOME };
}

// "#/settings/accounts/set-sieve" and similar mixes.
function resolveSectionAlias(c: SettingsCategoryId, b: string | undefined): string | null {
  const l = resolveLegacy(b);
  return l && l.category === c ? l.section : null;
}

export function settingsHash(loc: Partial<SettingsLocation> = {}): string {
  if (!loc.category) return '#/settings';
  return `#/settings/${loc.category}` + (loc.section ? `/${loc.section}` : '');
}

/** Normalise a requested location: unknown/hidden categories go to the overview, unknown sections are dropped. */
export function normalizeLocation(loc: SettingsLocation, env: SettingsEnv): SettingsLocation {
  const cat = findCategory(loc.category, env);
  if (!cat) return { ...SETTINGS_HOME };
  return { category: cat.id, section: cat.sections.some((s) => s.id === loc.section) ? loc.section : null };
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

export type Translate = (key: MessageKey) => string;

export interface SettingsHit {
  category: SettingsCategoryId;
  section: string;
  score: number;
  /** Label of the setting that matched when it is not the section title. */
  match?: MessageKey;
}

export function normalizeText(s: string): string {
  return s
    .toLowerCase()
    .normalize('NFD')
    .replace(/\p{M}/gu, '')
    .replace(/ß/g, 'ss')
    .replace(/[­]/g, '')
    .replace(/\{[^}]*\}/g, ' ')
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .trim();
}

/**
 * Searches section titles, page titles and key setting labels in all given
 * languages (current first). Every query word must occur; titles rank above
 * page names and setting labels.
 */
export function searchSettings(query: string, env: SettingsEnv, translators: Translate[], limit = 12): SettingsHit[] {
  const words = normalizeText(query).split(' ').filter(Boolean);
  if (!words.length) return [];
  const all = (key: MessageKey) => translators.map((tr) => normalizeText(tr(key))).join(' ');
  const hits: SettingsHit[] = [];
  for (const cat of visibleCategories(env)) {
    const catText = all(navKey(cat.id)) + ' ' + all(pageTitleKey(cat.id));
    for (const s of cat.sections) {
      const title = all(sectionKey(cat.id, s.id));
      const labels = s.keywords.map((k) => [k, all(k)] as const);
      const hay = [title, catText, ...labels.map((l) => l[1])].join(' ');
      if (!words.every((w) => hay.includes(w))) continue;
      let score = 0;
      let match: MessageKey | undefined;
      for (const w of words) {
        if (title.split(' ').some((x) => x.startsWith(w))) score += 10;
        else if (title.includes(w)) score += 6;
        else if (catText.includes(w)) score += 3;
        else {
          const l = labels.find((x) => x[1].includes(w));
          if (l) {
            score += l[1].split(' ').some((x) => x.startsWith(w)) ? 5 : 2;
            match ??= l[0];
          }
        }
      }
      hits.push({ category: cat.id, section: s.id, score, match });
    }
  }
  return hits.sort((a, b) => b.score - a.score).slice(0, limit);
}
