<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Mail from '@lucide/svelte/icons/mail';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import Calendar from '@lucide/svelte/icons/calendar';
  import Contact from '@lucide/svelte/icons/contact';
  import Cloud from '@lucide/svelte/icons/cloud';
  import Settings from '@lucide/svelte/icons/settings';
  import Search from '@lucide/svelte/icons/search';
  import FolderKanban from '@lucide/svelte/icons/folder-kanban';
  import AccountMenu from './AccountMenu.svelte';
  import { app, prefs } from '../state';
  import { t, i18n } from '../i18n/index.svelte';

  // Labels are always a single line - never hyphenated or wrapped. Settings
  // is an icon-only item at the bottom (common pattern), with tooltip and
  // aria-label. If a translation is too long for the rail, the label is
  // hidden for that item instead of being broken up (icon + tooltip remain).
  let rail = $state<HTMLElement>();
  $effect(() => {
    void i18n.locale;
    void items;
    if (!rail) return;
    for (const el of rail.querySelectorAll<HTMLElement>('.lbl')) {
      el.hidden = false;
      if (el.scrollWidth > el.clientWidth + 1) el.hidden = true;
    }
  });

  type Item = { id: 'mail' | 'chats' | 'search' | 'projects' | 'calendar' | 'contacts' | 'files'; icon: typeof Mail; label: string; badge?: number };

  const items: Item[] = $derived([
    { id: 'mail', icon: Mail, label: t('nav.mail'), badge: app.unifiedUnread },
    { id: 'chats', icon: MessagesSquare, label: t('nav.chats') },
    { id: 'search', icon: Search, label: t('nav.search') },
    { id: 'projects', icon: FolderKanban, label: t('nav.projects') },
    { id: 'calendar', icon: Calendar, label: t('nav.calendar') },
    { id: 'contacts', icon: Contact, label: t('nav.contacts') },
    { id: 'files', icon: Cloud, label: t('nav.files') },
  ]);

  function isActive(id: Item['id']): boolean {
    if (id === 'mail') return app.route === 'mail' && prefs.viewMode !== 'chat';
    if (id === 'chats') return app.route === 'mail' && prefs.viewMode === 'chat';
    return app.route === id;
  }

  async function go(id: Item['id']) {
    if (id === 'mail' || id === 'chats') {
      app.navigate('mail');
      if (id === 'chats') await app.setViewMode('chat');
      else if (prefs.viewMode === 'chat') await app.setViewMode('classic');
    } else {
      app.navigate(id);
    }
  }
</script>

<nav class="rail" aria-label={t('nav.label')} bind:this={rail} lang={i18n.locale}>
  <div class="logo"><img src="./icons/icon-64.png" alt="TuxMail" width="34" height="34" /></div>
  <ul>
    {#each items as item (item.id)}
      {@const Icon = item.icon}
      <li>
        <button class="item" aria-current={isActive(item.id) ? 'page' : undefined} onclick={() => go(item.id)} title={item.label}>
          <Icon size={21} strokeWidth={1.8} aria-hidden="true" />
          <span class="lbl">{item.label}</span>
          {#if item.badge}<span class="dot" aria-label={t('mail.folder.unreadCount', { count: item.badge })}>{item.badge > 99 ? '99+' : item.badge}</span>{/if}
        </button>
      </li>
    {/each}
  </ul>
  <div class="spacer"></div>
  <button class="item settings" aria-current={app.route === 'settings' ? 'page' : undefined} onclick={() => app.navigate('settings')} title={t('nav.settings')} aria-label={t('nav.settings')}>
    <Settings size={22} strokeWidth={1.8} aria-hidden="true" />
  </button>
  <AccountMenu />
</nav>

<style>
  .rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    width: 72px;
    flex: none;
    padding: 10px 0;
    background: var(--tm-rail);
  }
  .logo img {
    display: block;
    filter: drop-shadow(0 1px 2px rgb(0 0 0 / 0.35));
  }
  .logo {
    height: 40px;
    display: flex;
    align-items: center;
    margin-bottom: 8px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .spacer {
    flex: 1;
  }
  .item {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    width: 64px;
    min-height: 50px;
    padding: 4px 2px;
    border: none;
    border-radius: 12px;
    background: transparent;
    color: var(--tm-rail-fg);
    transition:
      background var(--tm-dur),
      color var(--tm-dur);
  }
  .item:hover {
    background: rgb(255 255 255 / 0.08);
    color: var(--tm-rail-active);
  }
  .item[aria-current='page'] {
    background: rgb(255 255 255 / 0.12);
    color: var(--tm-rail-active);
  }
  .item[aria-current='page']::before {
    content: '';
    position: absolute;
    left: -6px;
    top: 12px;
    bottom: 12px;
    width: 3px;
    border-radius: 3px;
    background: var(--tm-brand);
  }
  .lbl {
    font-size: 10.5px;
    font-weight: 500;
    line-height: 1.2;
    max-width: 60px;
    overflow: hidden;
    text-align: center;
    white-space: nowrap;
    letter-spacing: 0.01em;
  }
  .item.settings {
    min-height: 44px;
  }
  .dot {
    position: absolute;
    top: 3px;
    right: 4px;
    min-width: 17px;
    height: 17px;
    padding: 0 4px;
    border-radius: 999px;
    background: var(--tm-brand);
    color: var(--tm-brand-navy);
    font-size: 10px;
    font-weight: 700;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 2px solid var(--tm-rail);
  }

  /* Phones: bottom navigation bar. */
  @media (max-width: 639px) {
    .rail {
      position: fixed;
      left: 0;
      right: 0;
      bottom: 0;
      z-index: 30;
      flex-direction: row;
      width: auto;
      height: 60px;
      padding: 4px 8px calc(4px + env(safe-area-inset-bottom));
      border-right: none;
      border-top: 1px solid var(--tm-line);
      justify-content: space-around;
    }
    .logo,
    .spacer {
      display: none;
    }
    ul {
      flex-direction: row;
      flex: 8;
      min-width: 0;
      justify-content: space-around;
      gap: 0;
    }
    li {
      flex: 1 1 0;
      min-width: 0;
      display: flex;
      justify-content: center;
    }
    /* Eight destinations share the width; labels stay on one line (hidden if too long). */
    .item {
      width: 100%;
      max-width: 64px;
      min-height: 50px;
      padding: 3px 0;
      gap: 1px;
    }
    .item.settings {
      flex: 1 1 0;
      min-width: 0;
    }
    .lbl {
      font-size: 9.5px;
      max-width: 100%;
    }
    .item[aria-current='page']::before {
      display: none;
    }
  }
</style>
