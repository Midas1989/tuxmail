<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Mail from '@lucide/svelte/icons/mail';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import Calendar from '@lucide/svelte/icons/calendar';
  import Contact from '@lucide/svelte/icons/contact';
  import Cloud from '@lucide/svelte/icons/cloud';
  import Settings from '@lucide/svelte/icons/settings';
  import { app, prefs } from '../state';
  import { t } from '../i18n/index.svelte';

  type Item = { id: 'mail' | 'chats' | 'calendar' | 'contacts' | 'files'; icon: typeof Mail; label: string; badge?: number };

  const items: Item[] = $derived([
    { id: 'mail', icon: Mail, label: t('nav.mail'), badge: app.unifiedUnread },
    { id: 'chats', icon: MessagesSquare, label: t('nav.chats') },
    { id: 'calendar', icon: Calendar, label: t('nav.calendar') },
    { id: 'contacts', icon: Contact, label: t('nav.contacts') },
    { id: 'files', icon: Cloud, label: t('nav.files') },
  ]);

  function isActive(id: Item['id']): boolean {
    if (id === 'mail') return app.route === 'mail' && prefs.viewMode === 'classic';
    if (id === 'chats') return app.route === 'mail' && prefs.viewMode === 'chat';
    return app.route === id;
  }

  async function go(id: Item['id']) {
    if (id === 'mail' || id === 'chats') {
      app.navigate('mail');
      await app.setViewMode(id === 'mail' ? 'classic' : 'chat');
    } else {
      app.navigate(id);
    }
  }
</script>

<nav class="rail" aria-label={t('nav.label')}>
  <div class="logo" aria-hidden="true">
    <svg viewBox="0 0 64 64" width="26" height="26"
      ><rect width="64" height="64" rx="16" fill="var(--tm-accent)" /><path
        d="M15 22h34v21H15z M15 22l17 12 17-12"
        fill="none"
        stroke="#fff"
        stroke-width="4"
        stroke-linejoin="round"
      /></svg
    >
  </div>
  <ul>
    {#each items as item (item.id)}
      {@const Icon = item.icon}
      <li>
        <button class="item" aria-current={isActive(item.id) ? 'page' : undefined} onclick={() => go(item.id)} title={item.label}>
          <Icon size={21} strokeWidth={1.8} aria-hidden="true" />
          <span class="lbl">{item.label}</span>
          {#if item.badge}<span class="dot" aria-label={t('folder.unreadCount', { count: item.badge })}>{item.badge > 99 ? '99+' : item.badge}</span>{/if}
        </button>
      </li>
    {/each}
  </ul>
  <div class="spacer"></div>
  <button class="item settings" aria-current={app.route === 'settings' ? 'page' : undefined} onclick={() => app.navigate('settings')} title={t('nav.settings')}>
    <Settings size={21} strokeWidth={1.8} aria-hidden="true" />
    <span class="lbl">{t('nav.settings')}</span>
  </button>
</nav>

<style>
  .rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    width: 64px;
    flex: none;
    padding: 10px 0;
    background: var(--tm-rail);
    border-right: 1px solid var(--tm-line);
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
    width: 52px;
    height: 50px;
    border: none;
    border-radius: 12px;
    background: transparent;
    color: var(--tm-muted);
    transition:
      background var(--tm-dur),
      color var(--tm-dur);
  }
  .item:hover {
    background: color-mix(in srgb, var(--tm-fg) 7%, transparent);
    color: var(--tm-fg);
  }
  .item[aria-current='page'] {
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  .item[aria-current='page']::before {
    content: '';
    position: absolute;
    left: -6px;
    top: 12px;
    bottom: 12px;
    width: 3px;
    border-radius: 3px;
    background: var(--tm-accent);
  }
  .lbl {
    font-size: 10px;
    font-weight: 500;
    max-width: 50px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dot {
    position: absolute;
    top: 3px;
    right: 4px;
    min-width: 17px;
    height: 17px;
    padding: 0 4px;
    border-radius: 999px;
    background: var(--tm-accent);
    color: white;
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
      flex: 1;
      justify-content: space-around;
    }
    .item[aria-current='page']::before {
      display: none;
    }
  }
</style>
