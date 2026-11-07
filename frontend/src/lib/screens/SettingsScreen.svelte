<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings: category navigation with search on the left, the page of the
  selected category with its grouped sections on the right
  (#/settings/<category>/<section>, see settings/catalog.ts). Phones show
  the category list first and each page full screen with a back button.
  The sections are the existing settings components, moved, not rewritten.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import { MediaQuery } from 'svelte/reactivity';
  import CircleUser from '@lucide/svelte/icons/circle-user';
  import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
  import AtSign from '@lucide/svelte/icons/at-sign';
  import Mail from '@lucide/svelte/icons/mail';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import CalendarDays from '@lucide/svelte/icons/calendar-days';
  import Cloud from '@lucide/svelte/icons/cloud';
  import Workflow from '@lucide/svelte/icons/workflow';
  import Bell from '@lucide/svelte/icons/bell';
  import Sparkles from '@lucide/svelte/icons/sparkles';
  import Shield from '@lucide/svelte/icons/shield';
  import Database from '@lucide/svelte/icons/database';
  import Users from '@lucide/svelte/icons/users';
  import UserCog from '@lucide/svelte/icons/user-cog';
  import Info from '@lucide/svelte/icons/info';
  import Search from '@lucide/svelte/icons/search';
  import X from '@lucide/svelte/icons/x';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import en from '$locales/en.json';
  import de from '$locales/de.json';
  import { admin, app, security, team, connection } from '../state';
  import { t, type MessageKey } from '../i18n/index.svelte';
  import { flatten, type Dict } from '../i18n/core';
  import { isNativeShell } from '../platform';
  import { desktopWindow } from '../native/window.svelte';
  import { BACK_EVENT } from '../native/navigation';
  import {
    groupKey,
    navKey,
    normalizeLocation,
    pageDescKey,
    pageTitleKey,
    resolveLegacy,
    searchSettings,
    sectionDomId,
    sectionKey,
    settingsHash,
    visibleCategories,
    type SettingsCategoryId,
    type SettingsEnv,
    type SettingsHit,
    type Translate,
  } from '../settings/catalog';
  // Sections
  import ProfileSettings from '../components/settings/ProfileSettings.svelte';
  import RegionSettings from '../components/settings/RegionSettings.svelte';
  import AppearanceSettings from '../components/settings/AppearanceSettings.svelte';
  import WindowSettings from '../components/settings/WindowSettings.svelte';
  import ShortcutSettings from '../components/settings/ShortcutSettings.svelte';
  import AccountList from '../components/settings/AccountList.svelte';
  import SignatureSettings from '../components/settings/SignatureSettings.svelte';
  import CloudAccountsSettings from '../components/settings/CloudAccountsSettings.svelte';
  import FolderSettings from '../components/settings/FolderSettings.svelte';
  import ServerConnectionCard from '../components/settings/ServerConnectionCard.svelte';
  import SieveSettings from '../components/settings/SieveSettings.svelte';
  import ComfortSettingsCard from '../components/comfort/ComfortSettingsCard.svelte';
  import RemoteContentSettings from '../components/settings/RemoteContentSettings.svelte';
  import SnippetSettings from '../components/settings/SnippetSettings.svelte';
  import VacationSettings from '../components/settings/VacationSettings.svelte';
  import MessengerSettings from '../components/settings/MessengerSettings.svelte';
  import ChatSettings from '../components/settings/ChatSettings.svelte';
  import ChatFeatureSettings from '../components/settings/ChatFeatureSettings.svelte';
  import ChatDialogs from '../components/chat/ChatDialogs.svelte';
  import PimSettings from '../components/settings/PimSettings.svelte';
  import CloudStorageSettings from '../components/settings/CloudStorageSettings.svelte';
  import WorkflowList from '../components/workflow/WorkflowList.svelte';
  import TagSettings from '../components/settings/TagSettings.svelte';
  import NotificationSettings from '../components/settings/NotificationSettings.svelte';
  import AiSettingsCard from '../components/settings/AiSettingsCard.svelte';
  import SmimeSettingsCard from '../components/comfort/SmimeSettingsCard.svelte';
  import PgpSettingsCard from '../components/comfort/PgpSettingsCard.svelte';
  import SecuritySettings from '../components/settings/SecuritySettings.svelte';
  import ImportExportSettings from '../components/settings/ImportExportSettings.svelte';
  import BackupSettings from '../components/settings/BackupSettings.svelte';
  import TeamSettings from '../components/team/TeamSettings.svelte';
  import AdminSettings from '../components/admin/AdminSettings.svelte';
  import AboutSettings from '../components/settings/AboutSettings.svelte';
  import DiagnosticsSettings from '../components/settings/DiagnosticsSettings.svelte';

  const ICONS: Record<SettingsCategoryId, typeof Mail> = {
    profile: CircleUser,
    general: SlidersHorizontal,
    accounts: AtSign,
    mail: Mail,
    chats: MessageCircle,
    pim: CalendarDays,
    cloud: Cloud,
    automation: Workflow,
    notifications: Bell,
    ai: Sparkles,
    security: Shield,
    data: Database,
    team: Users,
    admin: UserCog,
    about: Info,
  };

  const phone = new MediaQuery('(max-width: 639px), (pointer: coarse) and (max-height: 500px)');

  // ----- which categories apply ------------------------------------------------------------
  let teamAdmin = $state(false);
  $effect(() => {
    const c = team.client;
    if (!c) return;
    c.adminList().then(
      () => (teamAdmin = true),
      () => (teamAdmin = false),
    );
  });
  // Administration: role from /api/auth/me (web client only).
  $effect(() => {
    void admin.init();
  });
  const env = $derived<SettingsEnv>({
    desktop: !!desktopWindow.info,
    native: isNativeShell(),
    team: !!team.client && (teamAdmin || team.mailboxes.some((m) => m.role === 'manage')),
    vault: !!app.vault?.portable,
    admin: admin.isAdmin,
    web: security.available,
  });
  const categories = $derived(visibleCategories(env));

  // ----- location ---------------------------------------------------------------------------
  const loc = $derived(normalizeLocation(app.settingsLoc, env));
  /** Desktop shows "Allgemein" for #/settings; phones show the category list. */
  const pageId = $derived<SettingsCategoryId | null>(loc.category ?? (phone.current ? null : 'general'));
  const sectionId = $derived(loc.section);
  const page = $derived(categories.find((c) => c.id === pageId) ?? null);

  let navEl = $state<HTMLElement>();
  let paneEl = $state<HTMLElement>();
  let titleEl = $state<HTMLElement>();
  let searchEl = $state<HTMLInputElement>();
  let focusNext: 'page' | 'section' | 'nav' | null = null;
  let lastCategory: SettingsCategoryId | null = null;
  let flash = $state<string | null>(null);
  let flashTimer: ReturnType<typeof setTimeout> | undefined;

  function go(category: SettingsCategoryId | null, section: string | null = null, focus: 'page' | 'section' | 'nav' = 'page') {
    if (category === null) lastCategory = pageId;
    focusNext = focus;
    const same = (category ?? (phone.current ? null : 'general')) === pageId && section === sectionId;
    app.openSettings(category, section);
    if (same) void tick().then(() => settle(pageId, sectionId));
  }

  // Scroll, highlight and focus after the page or section changed.
  $effect(() => {
    const c = pageId;
    const s = sectionId;
    void tick().then(() => settle(c, s));
  });

  function settle(c: SettingsCategoryId | null, s: string | null) {
    const focus = focusNext;
    focusNext = null;
    if (c && s) {
      const id = sectionDomId(c, s);
      const el = document.getElementById(id);
      el?.scrollIntoView({ block: 'start' });
      // Sections that load their data asynchronously grow; keep the target in view.
      setTimeout(() => document.getElementById(id)?.scrollIntoView({ block: 'start' }), 250);
      flash = id;
      clearTimeout(flashTimer);
      flashTimer = setTimeout(() => (flash = null), 1800);
      if (focus) el?.focus({ preventScroll: true });
    } else {
      paneEl?.scrollTo?.({ top: 0 });
      if (focus === 'page') titleEl?.focus({ preventScroll: true });
    }
    if (focus === 'nav') navEl?.querySelector<HTMLElement>(`[data-cat="${lastCategory}"]`)?.focus();
  }

  // Old anchors (app.settingsAnchor = 'set-tags') still work.
  $effect(() => {
    const id = app.settingsAnchor;
    if (!id) return;
    app.settingsAnchor = null;
    const l = resolveLegacy(id);
    if (l) go(l.category, l.section, 'section');
  });

  // Phones: the Android back button returns from a page to the category list.
  $effect(() => {
    const onBack = (e: Event) => {
      if (phone.current && app.settingsLoc.category) {
        e.preventDefault();
        go(null, null, 'nav');
      }
    };
    window.addEventListener(BACK_EVENT, onBack);
    return () => window.removeEventListener(BACK_EVENT, onBack);
  });

  // ----- search -----------------------------------------------------------------------------
  let query = $state('');
  let dicts: Dict[] | null = null;
  function translators(): Translate[] {
    dicts ??= [flatten(en), flatten(de)];
    return [(k) => t(k), ...dicts.map((d) => (k: MessageKey) => d[k] ?? '')];
  }
  const hits = $derived(query.trim() ? searchSettings(query, env, translators()) : []);

  function openHit(h: SettingsHit) {
    query = '';
    go(h.category, h.section, 'section');
  }

  function onSearchKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      navEl?.querySelector<HTMLElement>('.nav-item')?.focus();
    } else if (e.key === 'Enter' && hits[0]) {
      e.preventDefault();
      openHit(hits[0]);
    } else if (e.key === 'Escape' && query) {
      e.preventDefault();
      e.stopPropagation();
      query = '';
    }
  }

  /** ↑/↓/Home/End move between the entries of the list; Enter opens (native link/button). */
  function onListKey(e: KeyboardEvent) {
    const items = [...(navEl?.querySelectorAll<HTMLElement>('.nav-item') ?? [])];
    const i = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (e.key === 'ArrowDown') next = Math.min(items.length - 1, i + 1);
    else if (e.key === 'ArrowUp') next = i - 1;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = items.length - 1;
    else return;
    e.preventDefault();
    if (next < 0) searchEl?.focus();
    else items[next]?.focus();
  }

  // Offline (desktop app in server mode) the settings live on the unreachable
  // server: visible, but switched off. These work anyway: they belong to this device.
  const WORKS_OFFLINE = new Set(['accounts/server', 'general/appearance', 'general/window', 'general/shortcuts', 'general/app']);
</script>

<section class="settings" class:paged={!!loc.category} aria-labelledby="settings-title">
  <aside class="nav-col">
    <div class="nav-head">
      <h1 id="settings-title">{t('nav.settings')}</h1>
      <div class="search" class:has-query={!!query} role="search">
        <Search size={15} class="s-icon" aria-hidden="true" />
        <input
          bind:this={searchEl}
          bind:value={query}
          class="input"
          type="search"
          placeholder={t('settings.nav.searchPlaceholder')}
          aria-label={t('settings.nav.search')}
          aria-controls="settings-nav-list"
          onkeydown={onSearchKey}
          data-testid="settings-search"
        />
        {#if query}
          <button class="clear" aria-label={t('settings.nav.clearSearch')} title={t('settings.nav.clearSearch')} onclick={() => ((query = ''), searchEl?.focus())}><X size={14} /></button>
        {/if}
      </div>
    </div>
    <nav bind:this={navEl} aria-label={t('settings.nav.label')}>
      {#if query.trim()}
        <p class="results-h" id="settings-results-h">{t('settings.nav.results')}</p>
        <ul class="nav-list" id="settings-nav-list" aria-labelledby="settings-results-h" role="list">
          {#each hits as h (h.category + '/' + h.section)}
            {@const Icon = ICONS[h.category]}
            <li>
              <a class="nav-item hit" href={settingsHash(h)} onkeydown={onListKey} onclick={(e) => (e.preventDefault(), openHit(h))}>
                <Icon size={16} aria-hidden="true" />
                <span class="hit-txt">
                  <span class="lbl">{t(sectionKey(h.category, h.section))}</span>
                  <span class="where">{t(navKey(h.category))}{#if h.match}{' · '}{t(h.match)}{/if}</span>
                </span>
              </a>
            </li>
          {:else}
            <li class="none" role="status">{t('settings.nav.noResults', { query: query.trim() })}</li>
          {/each}
        </ul>
      {:else}
        <ul class="nav-list" id="settings-nav-list" role="list">
          {#each categories as c (c.id)}
            {@const Icon = ICONS[c.id]}
            <li>
              <a class="nav-item" data-cat={c.id} href={settingsHash({ category: c.id })} onkeydown={onListKey} aria-current={pageId === c.id ? 'page' : undefined} onclick={(e) => (e.preventDefault(), go(c.id))}>
                <Icon size={17} aria-hidden="true" />
                <span class="lbl">{t(navKey(c.id))}</span>
                <ChevronRight size={16} class="chev" aria-hidden="true" />
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </nav>
  </aside>

  <div class="pane" bind:this={paneEl}>
    {#if page}
      <div class="pane-inner" data-testid="settings-page" data-category={page.id}>
        <header class="page-head">
          <button class="back btn-ghost" onclick={() => go(null, null, 'nav')}><ChevronLeft size={18} /> {t('settings.nav.back')}</button>
          <h2 class="page-title" tabindex="-1" bind:this={titleEl}>{t(pageTitleKey(page.id))}</h2>
          <p class="page-desc">{t(pageDescKey(page.id))}</p>
        </header>
        {#if connection.offline}<p class="offline-note" role="status">{t('connection.settingsOffline')}</p>{/if}
        <div class="sections">
          {#each page.sections as s, i (s.id)}
            {#if s.group && s.group !== page.sections[i - 1]?.group}
              <h3 class="group">{t(groupKey(s.group))}</h3>
            {/if}
            <div class="sec" id={sectionDomId(page.id, s.id)} data-section={s.id} tabindex="-1" class:solo={page.sections.length === 1} class:flash={flash === sectionDomId(page.id, s.id)}>
              <fieldset class="sec-lock" disabled={connection.offline && !WORKS_OFFLINE.has(`${page.id}/${s.id}`)}>
                {@render body(page.id, s.id)}
              </fieldset>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>
</section>

{#snippet body(c: SettingsCategoryId, s: string)}
  {@const k = `${c}/${s}`}
  {#if k === 'profile/profile'}<ProfileSettings part="profile" />
  {:else if k === 'profile/signin'}<ProfileSettings part="signin" />
  {:else if k === 'profile/session'}<ProfileSettings part="session" />
  {:else if k === 'general/language'}<RegionSettings />
  {:else if k === 'general/appearance'}<AppearanceSettings />
  {:else if k === 'general/window'}<WindowSettings />
  {:else if k === 'general/shortcuts'}<ShortcutSettings />
  {:else if k === 'general/app'}<SecuritySettings part="app" />
  {:else if k === 'accounts/list'}<AccountList />
  {:else if k === 'accounts/signatures'}<SignatureSettings />
  {:else if k === 'accounts/shared'}<CloudAccountsSettings part="mailboxes" />
  {:else if k === 'accounts/folders'}<FolderSettings />
  {:else if k === 'accounts/server'}<ServerConnectionCard />
  {:else if k === 'accounts/sieve'}<SieveSettings />
  {:else if k === 'mail/reading'}<ComfortSettingsCard part="reading" />
  {:else if k === 'mail/remote'}<RemoteContentSettings />
  {:else if k === 'mail/compose'}<ComfortSettingsCard part="compose" />
  {:else if k === 'mail/snippets'}<SnippetSettings />
  {:else if k === 'mail/vacation'}<VacationSettings />
  {:else if k === 'chats/messenger'}<MessengerSettings />
  {:else if k === 'chats/chat'}<ChatSettings />
  {:else if k === 'chats/folders'}<ChatFeatureSettings part="folders" />
  {:else if k === 'chats/focus'}<ChatFeatureSettings part="focus" />
  {:else if k === 'pim/calendars'}<PimSettings part="calendars" />
  {:else if k === 'pim/contacts'}<PimSettings part="contacts" />
  {:else if k === 'pim/cloudsync'}<CloudAccountsSettings part="pim" />
  {:else if k === 'cloud/storage'}<CloudStorageSettings part="storage" />
  {:else if k === 'cloud/shares'}<CloudStorageSettings part="shares" />
  {:else if k === 'automation/workflows'}
    <section class="card" id="set-workflows" aria-labelledby="set-workflows-h">
      <h2 id="set-workflows-h">{t('settings.workflows.title')}</h2>
      <p class="intro">{t('settings.workflows.intro')}</p>
      <WorkflowList />
    </section>
  {:else if k === 'automation/tags'}<TagSettings />
  {:else if k === 'notifications/notifications'}<NotificationSettings />
  {:else if k === 'ai/assistant'}<AiSettingsCard />
  {:else if k === 'security/pgp'}<PgpSettingsCard />
  {:else if k === 'security/smime'}<SmimeSettingsCard />
  {:else if k === 'security/signin'}<CloudAccountsSettings part="services" />
  {:else if k === 'security/vault'}<SecuritySettings part="vault" />
  {:else if k === 'security/applock'}<SecuritySettings part="applock" />
  {:else if k === 'security/privacy'}<SecuritySettings part="privacy" />
  {:else if k === 'data/import'}<ImportExportSettings />
  {:else if k === 'data/backup'}<BackupSettings />
  {:else if k === 'team/mailboxes'}<TeamSettings />
  {:else if k === 'admin/manage'}<AdminSettings />
  {:else if k === 'about/about'}<AboutSettings part="about" />
  {:else if k === 'about/diagnostics'}<DiagnosticsSettings />
  {:else if k === 'about/licenses'}<AboutSettings part="licenses" />
  {/if}
{/snippet}

<!-- Editors of chat folders and focus mode (Chats & Messenger). -->
<ChatDialogs />

<style>
  .sec-lock {
    display: contents;
    border: 0;
  }
  .offline-note {
    margin: 0 0 12px;
    padding: 8px 12px;
    border-radius: 10px;
    background: var(--tm-warning-soft, var(--tm-accent-soft));
    font-size: 13px;
  }
  .settings {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    background: var(--tm-bg);
  }

  /* ----- navigation ----- */
  .nav-col {
    width: 272px;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--tm-surface);
    border-right: 1px solid var(--tm-line);
  }
  .nav-head {
    padding: 18px 14px 10px;
  }
  h1 {
    margin: 0 4px 12px;
    font-size: 20px;
    font-weight: 650;
    letter-spacing: -0.015em;
  }
  .search {
    position: relative;
  }
  .search :global(.s-icon) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--tm-faint);
    pointer-events: none;
  }
  .search .input {
    width: 100%;
    padding-left: 32px;
    padding-right: 8px;
  }
  .search.has-query .input {
    padding-right: 30px;
  }
  .search .input::-webkit-search-cancel-button {
    display: none;
  }
  .clear {
    position: absolute;
    right: 6px;
    top: 50%;
    transform: translateY(-50%);
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border: none;
    border-radius: 6px;
    background: none;
    color: var(--tm-muted);
    cursor: pointer;
  }
  .clear:hover {
    background: var(--tm-surface-3);
  }
  nav {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 2px 8px 16px;
  }
  .nav-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 11px;
    min-height: 36px;
    padding: 0 10px;
    border-radius: 8px;
    color: var(--tm-fg);
    text-decoration: none;
    font-size: 13.5px;
    white-space: nowrap;
  }
  .nav-item :global(svg) {
    flex: none;
    color: var(--tm-muted);
  }
  .nav-item .lbl {
    white-space: nowrap;
    hyphens: none;
  }
  .nav-item :global(.chev) {
    display: none;
    margin-left: auto;
    color: var(--tm-faint);
  }
  .nav-item:hover {
    background: var(--tm-surface-2);
  }
  .nav-item:focus-visible {
    outline: 2px solid var(--tm-focus);
    outline-offset: -2px;
  }
  .nav-item[aria-current='page'] {
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .nav-item[aria-current='page'] :global(svg) {
    color: var(--tm-accent-text);
  }
  .results-h {
    margin: 6px 10px 4px;
    font-size: 11.5px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--tm-muted);
  }
  .hit {
    padding-top: 6px;
    padding-bottom: 6px;
    white-space: normal;
  }
  .hit-txt {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .where {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .none {
    padding: 10px;
    font-size: 13px;
    color: var(--tm-muted);
  }

  /* ----- page ----- */
  .pane {
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow: auto;
  }
  .pane-inner {
    max-width: 880px;
    padding: 26px 36px 56px;
  }
  .page-head {
    margin-bottom: 20px;
  }
  .back {
    display: none;
    align-items: center;
    gap: 2px;
    margin: 0 0 6px -8px;
    padding: 6px 8px;
    border: none;
    border-radius: 8px;
    background: none;
    color: var(--tm-accent-text);
    font: inherit;
    font-size: 14px;
    cursor: pointer;
  }
  .page-title {
    margin: 0;
    font-size: 22px;
    font-weight: 650;
    letter-spacing: -0.015em;
    outline: none;
  }
  .page-desc {
    margin: 4px 0 0;
    font-size: 13.5px;
    color: var(--tm-muted);
  }
  .sections {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .group {
    margin: 10px 2px -6px;
    font-size: 12px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-muted);
  }
  .group:first-child {
    margin-top: 0;
  }
  .sec {
    position: relative;
    scroll-margin-top: 16px;
    border-radius: var(--tm-radius-lg);
    outline: none;
  }
  .sec:empty {
    display: none;
  }
  .sec.flash {
    animation: flash 1.8s ease-out;
  }
  @keyframes flash {
    0%,
    45% {
      box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-focus) 75%, transparent);
    }
    100% {
      box-shadow: 0 0 0 3px transparent;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sec.flash {
      animation: none;
      box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-focus) 75%, transparent);
    }
  }

  /* One consistent look for the moved components: a single card per section. */
  .sec :global(section.card) {
    margin: 0;
    padding: 18px 22px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    box-shadow: none;
    max-width: none;
  }
  .sec :global(section.card > h2),
  .sec :global(section.card > div > h2) {
    font-size: 15px;
    font-weight: 650;
    letter-spacing: -0.005em;
  }
  .sec :global(section.card > h2) {
    margin: 0 0 6px;
  }
  /* A page with a single section already carries its title. */
  .sec.solo :global(section.card > h2),
  .sec.solo :global(section.card > div:first-child > h2) {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  .sec :global(.intro) {
    margin: 4px 0 14px;
    font-size: 13px;
    color: var(--tm-muted);
  }

  /* ----- phones: list → full-screen page ----- */
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .nav-col {
      width: 100%;
      border-right: none;
    }
    .settings.paged .nav-col {
      display: none;
    }
    .settings:not(.paged) .pane {
      display: none;
    }
    .nav-head {
      padding: 14px 16px 8px;
    }
    nav {
      padding: 2px 8px 80px;
    }
    .nav-item {
      min-height: 48px;
      font-size: 15px;
    }
    .nav-item :global(.chev) {
      display: block;
    }
    .nav-item :global(svg:not(.chev)) {
      min-width: 20px;
      min-height: 20px;
    }
    .nav-item[aria-current='page'] {
      background: none;
      color: var(--tm-fg);
      font-weight: 400;
    }
    .pane-inner {
      padding: 10px 16px 96px;
    }
    .back {
      display: inline-flex;
      min-height: 44px;
    }
    .back :global(svg) {
      min-width: 20px;
      min-height: 20px;
    }
    .page-title {
      font-size: 20px;
    }
    .sec :global(section.card) {
      padding: 14px 14px 16px;
      border-radius: var(--tm-radius);
    }
  }
</style>
