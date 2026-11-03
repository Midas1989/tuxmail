<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { fade, fly } from 'svelte/transition';
  import Search from '@lucide/svelte/icons/search';
  import Menu from '@lucide/svelte/icons/menu';
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import Command from '@lucide/svelte/icons/command';
  import Sun from '@lucide/svelte/icons/sun';
  import Moon from '@lucide/svelte/icons/moon';
  import WifiOff from '@lucide/svelte/icons/wifi-off';
  import X from '@lucide/svelte/icons/x';
  import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
  import { app, prefs, search } from '../state';
  import { t } from '../i18n/index.svelte';
  import Sidebar from '../components/Sidebar.svelte';
  import MessageList from '../components/mail/MessageList.svelte';
  import ReadingPane from '../components/mail/ReadingPane.svelte';
  import ConversationList from '../components/chat/ConversationList.svelte';
  import ChatTimeline from '../components/chat/ChatTimeline.svelte';

  let searchText = $state(app.search);
  let timer: ReturnType<typeof setTimeout> | null = null;
  function onSearchInput() {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => app.setSearch(searchText.trim()), 250);
  }
  function openAdvanced() {
    search.text = searchText.trim();
    search.mode = 'simple';
    app.navigate('search');
    if (search.text) void search.run();
  }
  function clearSearch() {
    searchText = '';
    void app.setSearch('');
  }

  const hasSelection = $derived(prefs.viewMode === 'classic' ? app.selectedMessageId != null : app.selectedConversationId != null);

  function toggleTheme() {
    prefs.theme = prefs.effectiveTheme === 'dark' ? 'light' : 'dark';
    prefs.save();
  }
</script>

<div class="mail">
  <div class="sidebar-inline"><Sidebar /></div>
  {#if app.sidebarOpen}
    <div class="drawer-backdrop" transition:fade={{ duration: 120 }} onclick={() => (app.sidebarOpen = false)} role="presentation"></div>
    <div class="drawer" transition:fly={{ x: -280, duration: 180 }}><Sidebar /></div>
  {/if}

  <div class="main">
    <header class="topbar">
      <button class="icon-btn menu" aria-label={t('mail.sidebar.open')} aria-expanded={app.sidebarOpen} onclick={() => (app.sidebarOpen = !app.sidebarOpen)}><Menu size={19} /></button>
      <div class="search" role="search">
        <Search size={16} aria-hidden="true" />
        <input
          type="search"
          bind:value={searchText}
          oninput={onSearchInput}
          onkeydown={(e) => {
            if (e.key === 'Escape') {
              clearSearch();
              (e.currentTarget as HTMLInputElement).blur();
            }
          }}
          placeholder={prefs.viewMode === 'classic' ? t('search.bar.placeholderMail') : t('search.bar.placeholderChat')}
          aria-label={t('search.bar.label')}
          data-search
        />
        <button class="clear" aria-label={t('search.bar.advanced')} title={t('search.bar.advanced')} onclick={openAdvanced}><SlidersHorizontal size={14} /></button>
        {#if searchText}
          <button class="clear" aria-label={t('search.bar.clear')} onclick={clearSearch}><X size={14} /></button>
        {:else}
          <button class="kbd palette-hint" onclick={() => (app.paletteOpen = true)} title={t('common.palette.title')}>Ctrl K</button>
        {/if}
      </div>
      <div class="grow"></div>
      {#if app.connection !== 'online'}
        <span class="conn" role="status"><WifiOff size={15} /> {t(`common.conn.${app.connection}`)}</span>
      {/if}
      <div class="seg view-toggle" role="radiogroup" aria-label={t('mail.view.label')} title="Ctrl+Shift+V">
        <button role="radio" aria-checked={prefs.viewMode === 'classic'} onclick={() => app.setViewMode('classic')}>
          <Rows3 size={15} aria-hidden="true" /><span class="hide-xs">{t('mail.view.classic')}</span>
        </button>
        <button role="radio" aria-checked={prefs.viewMode === 'chat'} onclick={() => app.setViewMode('chat')}>
          <MessagesSquare size={15} aria-hidden="true" /><span class="hide-xs">{t('mail.view.chat')}</span>
        </button>
      </div>
      <button class="icon-btn hide-xs" aria-label={t('common.palette.title')} title={t('common.palette.title') + ' (Ctrl+K)'} onclick={() => (app.paletteOpen = true)}><Command size={17} /></button>
      <button class="icon-btn" aria-label={t('settings.theme')} title={t('settings.theme')} onclick={toggleTheme}>
        {#if prefs.effectiveTheme === 'dark'}<Sun size={17} />{:else}<Moon size={17} />{/if}
      </button>
    </header>

    <div class="panes" class:has-selection={hasSelection} data-mode={prefs.viewMode}>
      {#if prefs.viewMode === 'classic'}
        <div class="pane-list"><MessageList /></div>
        <div class="pane-detail"><ReadingPane /></div>
      {:else}
        <div class="pane-list"><ConversationList /></div>
        <div class="pane-detail"><ChatTimeline /></div>
      {/if}
    </div>
  </div>
</div>

<style>
  .mail {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }
  .sidebar-inline {
    display: flex;
  }
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .topbar {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 54px;
    padding: 0 12px;
    border-bottom: 1px solid var(--tm-line);
    background: var(--tm-surface);
    flex: none;
  }
  .menu {
    display: none;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 1 520px;
    min-width: 0;
    height: 36px;
    padding: 0 6px 0 12px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    border: 1px solid transparent;
    color: var(--tm-muted);
    transition:
      border-color var(--tm-dur),
      background var(--tm-dur);
  }
  .search:focus-within {
    border-color: var(--tm-accent);
    background: var(--tm-surface);
  }
  .search input {
    flex: 1;
    min-width: 0;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
  }
  .search input::-webkit-search-cancel-button {
    display: none;
  }
  .clear {
    display: inline-flex;
    border: none;
    background: transparent;
    color: var(--tm-muted);
    padding: 4px;
    border-radius: 6px;
  }
  .palette-hint {
    cursor: pointer;
  }
  .grow {
    flex: 1;
  }
  .conn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--tm-warn);
  }
  .panes {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(320px, 400px) 1fr;
    grid-template-rows: minmax(0, 1fr);
  }
  .panes[data-mode='chat'] {
    grid-template-columns: minmax(300px, 360px) 1fr;
  }
  .pane-list,
  .pane-detail {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .pane-list > :global(*),
  .pane-detail > :global(*) {
    flex: 1;
  }
  .pane-list {
    border-right: 1px solid var(--tm-line);
  }
  .drawer-backdrop {
    position: fixed;
    inset: 0;
    z-index: 40;
    background: rgb(10 14 22 / 0.4);
  }
  .drawer {
    position: fixed;
    top: 0;
    bottom: 0;
    left: 0;
    z-index: 41;
    display: flex;
    box-shadow: var(--tm-shadow-lg);
  }

  @media (max-width: 1023px) {
    .sidebar-inline {
      display: none;
    }
    .menu {
      display: inline-flex;
    }
  }
  @media (max-width: 899px) {
    .panes,
    .panes[data-mode='chat'] {
      grid-template-columns: 1fr;
    }
    .pane-list {
      border-right: none;
    }
    .panes.has-selection .pane-list,
    .panes:not(.has-selection) .pane-detail {
      display: none;
    }
  }
  @media (max-width: 639px) {
    .hide-xs {
      display: none;
    }
    .topbar {
      padding: 0 8px;
      gap: 4px;
    }
    .palette-hint {
      display: none;
    }
    .search {
      flex: 1 1 auto;
      padding: 0 4px 0 10px;
    }
    .grow {
      display: none;
    }
  }
</style>
