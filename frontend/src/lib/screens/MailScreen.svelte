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
  import ChatWorkspace from '../components/chat/ChatWorkspace.svelte';
  import HybridList from '../components/mail/HybridList.svelte';
  import Conversation from '../components/chat/Conversation.svelte';
  import OriginalMailDialog from '../components/chat/OriginalMailDialog.svelte';
  import ForwardDialog from '../components/chat/ForwardDialog.svelte';
  import ChatDialogs from '../components/chat/ChatDialogs.svelte';
  import MessageSquareText from '@lucide/svelte/icons/message-square-text';
  import Pin from '@lucide/svelte/icons/pin';
  import type { ViewMode } from '../state/app.svelte';

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

  const hybrid = $derived(prefs.viewMode === 'hybrid');
  const hasSelection = $derived(hybrid ? app.selectedConversationId != null : app.selectedMessageId != null);
  const MODES: { id: ViewMode; icon: typeof Rows3 }[] = [
    { id: 'classic', icon: Rows3 },
    { id: 'hybrid', icon: MessageSquareText },
    { id: 'chat', icon: MessagesSquare },
  ];
  const isDefault = $derived(app.folderDefaultView === prefs.viewMode);

  const scopeTitle = $derived.by(() => {
    const s = app.sel;
    if (s.kind === 'unified') return s.role === 'inbox' ? t('mail.folder.unifiedInbox') : s.role === 'flagged' ? t('mail.folder.flagged') : t('mail.folder.unread');
    if (s.kind === 'folder') return app.folderPath(s.folderId);
    if (s.kind === 'tag') return app.tag(s.tagId)?.path ?? '';
    if (s.kind === 'searchFolder') return app.searchFolder(s.searchFolderId)?.name ?? '';
    return '';
  });
  const scopeSub = $derived.by(() => {
    const s = app.sel;
    if (s.kind === 'folder') return app.account(s.accountId)?.displayName ?? '';
    if (s.kind === 'tag') return t('organize.tags.label');
    if (s.kind === 'searchFolder') return t('organize.searchFolder.label');
    return t('mail.sidebar.allAccounts');
  });

  function toggleTheme() {
    prefs.theme = prefs.effectiveTheme === 'dark' ? 'light' : 'dark';
    prefs.save();
  }
</script>

{#if prefs.viewMode === 'chat'}
<ChatWorkspace />
{:else}
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
          placeholder={t('search.bar.placeholderMail')}
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
        {#each MODES as m (m.id)}
          {@const Icon = m.icon}
          <button role="radio" aria-checked={prefs.viewMode === m.id} onclick={() => app.setViewMode(m.id)} title={t(`mail.view.${m.id}Hint`)}>
            <Icon size={15} aria-hidden="true" /><span class="hide-md">{t(`mail.view.${m.id}`)}</span>
          </button>
        {/each}
      </div>
      <button
        class="icon-btn hide-xs"
        aria-pressed={isDefault}
        aria-label={isDefault ? t('mail.view.unsetDefault') : t('mail.view.setDefault', { view: t(`mail.view.${prefs.viewMode}`) })}
        title={isDefault ? t('mail.view.unsetDefault') : t('mail.view.setDefault', { view: t(`mail.view.${prefs.viewMode}`) })}
        onclick={() => app.setFolderDefaultView(!isDefault)}><Pin size={15} /></button
      >
      <button class="icon-btn hide-xs" aria-label={t('common.palette.title')} title={t('common.palette.title') + ' (Ctrl+K)'} onclick={() => (app.paletteOpen = true)}><Command size={17} /></button>
      <button class="icon-btn" aria-label={t('settings.theme')} title={t('settings.theme')} onclick={toggleTheme}>
        {#if prefs.effectiveTheme === 'dark'}<Sun size={17} />{:else}<Moon size={17} />{/if}
      </button>
    </header>

    <div class="panes" class:has-selection={hasSelection} data-mode={prefs.viewMode}>
      {#if hybrid}
        <div class="pane-list"><HybridList title={scopeTitle} subtitle={scopeSub} /></div>
        <div class="pane-detail"><Conversation hybrid /></div>
      {:else}
        <div class="pane-list"><MessageList /></div>
        <div class="pane-detail"><ReadingPane /></div>
      {/if}
    </div>
  </div>
</div>
{#if hybrid}
  {#if app.originalMessage}
    <OriginalMailDialog message={app.originalMessage} />
  {/if}
  <ChatDialogs />
  {#if app.forwarding}
    <ForwardDialog item={app.forwarding} />
  {/if}
{/if}
{/if}


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
  .panes[data-mode='hybrid'] {
    grid-template-columns: minmax(280px, 340px) 1fr;
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
    .panes[data-mode='hybrid'] {
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
  @media (max-width: 1279px) {
    .hide-md {
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
