<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Messenger-style chat list: search, filter pills, archived row, pinned on top. -->
<script lang="ts">
  import Search from '@lucide/svelte/icons/search';
  import X from '@lucide/svelte/icons/x';
  import SquarePen from '@lucide/svelte/icons/square-pen';
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import Archive from '@lucide/svelte/icons/archive';
  import ArchiveRestore from '@lucide/svelte/icons/archive-arrow-up';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Pin from '@lucide/svelte/icons/pin';
  import PinOff from '@lucide/svelte/icons/pin-off';
  import BellOff from '@lucide/svelte/icons/bell-off';
  import Bell from '@lucide/svelte/icons/bell';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import MailOpen from '@lucide/svelte/icons/mail-open';
  import Mail from '@lucide/svelte/icons/mail';
  import Star from '@lucide/svelte/icons/star';
  import Trash from '@lucide/svelte/icons/trash';
  import Check from '@lucide/svelte/icons/check';
  import CheckCheck from '@lucide/svelte/icons/check-check';
  import Clock from '@lucide/svelte/icons/clock';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import { app, prefs } from '../../state';
  import { formatListDate, t } from '../../i18n/index.svelte';
  import type { ChatFilter, Conversation } from '../../types';
  import { longpress, swipe } from '../../actions';
  import ChatAvatar from './ChatAvatar.svelte';
  import ContextMenu, { type MenuItem } from '../common/ContextMenu.svelte';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';

  const FILTERS: ChatFilter[] = ['all', 'unread', 'mail', 'whatsapp', 'signal', 'telegram', 'groups', 'favorites'];

  let query = $state(app.chatQuery);
  let timer: ReturnType<typeof setTimeout> | null = null;
  function onSearch() {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => app.setChatQuery(query.trim()), 200);
  }

  let menu = $state<{ x: number; y: number; conv: Conversation } | null>(null);
  let confirmDelete = $state<Conversation | null>(null);
  let listEl = $state<HTMLDivElement>();

  $effect(() => {
    const id = app.selectedConversationId;
    if (id == null || !listEl) return;
    const el = listEl.querySelector<HTMLElement>(`#chat-${id}`);
    if (!el) return;
    const top = el.offsetTop;
    if (top < listEl.scrollTop) listEl.scrollTop = top;
    else if (top + el.offsetHeight > listEl.scrollTop + listEl.clientHeight) listEl.scrollTop = top + el.offsetHeight - listEl.clientHeight;
  });

  function avatarSeed(c: Conversation): string {
    if (c.kind === 'chat' && c.isGroup) return c.title;
    return app.person(c.participantIds[0])?.handles[0]?.value ?? c.title;
  }

  function preview(c: Conversation): string {
    if (c.snippet) return c.snippet;
    if (c.lastMedia === 'photo') return '📷 ' + t('chat.media.photo');
    if (c.lastMedia === 'voice') return '🎤 ' + t('chat.media.voice');
    if (c.lastMedia === 'file') return '📎 ' + (c.lastMediaName ?? t('chat.media.file'));
    return '';
  }

  function menuItems(c: Conversation): MenuItem[] {
    return [
      { id: 'pin', label: c.pinned ? t('chat.action.unpin') : t('chat.action.pin'), icon: c.pinned ? PinOff : Pin, run: () => app.updateConversation(c.id, { pinned: !c.pinned }) },
      { id: 'mute', label: c.muted ? t('chat.action.unmute') : t('chat.action.mute'), icon: c.muted ? Bell : BellOff, run: () => app.updateConversation(c.id, { muted: !c.muted }) },
      {
        id: 'archive',
        label: c.archived ? t('chat.action.unarchive') : t('chat.action.archive'),
        icon: c.archived ? ArchiveRestore : Archive,
        run: () => app.updateConversation(c.id, { archived: !c.archived }, c.archived ? 'chat.toast.unarchived' : 'chat.toast.archived'),
      },
      {
        id: 'unread',
        label: c.unreadCount > 0 || c.markedUnread ? t('chat.action.markRead') : t('chat.action.markUnread'),
        icon: c.unreadCount > 0 || c.markedUnread ? MailOpen : Mail,
        run: () => (c.unreadCount > 0 || c.markedUnread ? app.api.markConversationRead(c.id).then(() => app.loadConversations(true)) : app.updateConversation(c.id, { markedUnread: true })),
      },
      { id: 'fav', label: c.favorite ? t('chat.action.unfavorite') : t('chat.action.favorite'), icon: Star, run: () => app.updateConversation(c.id, { favorite: !c.favorite }) },
      { id: 'delete', label: t('chat.action.delete'), icon: Trash, danger: true, run: () => void (confirmDelete = c) },
    ];
  }

  function openMenu(c: Conversation, x: number, y: number) {
    menu = { x, y, conv: c };
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      app.moveSelection(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      app.moveSelection(-1);
    } else if ((e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) && app.conversation) {
      e.preventDefault();
      const el = listEl?.querySelector<HTMLElement>(`#chat-${app.conversation.id}`);
      const r = el?.getBoundingClientRect();
      openMenu(app.conversation, (r?.left ?? 0) + 60, (r?.top ?? 0) + 30);
    }
  }
</script>

<section class="clist" aria-label={t('chat.listLabel')}>
  <header class="head">
    {#if app.showArchived}
      <button class="icon-btn" aria-label={t('common.action.back')} onclick={() => app.toggleArchivedView(false)}><ArrowLeft size={19} /></button>
      <h1>{t('chat.archived')}</h1>
    {:else}
      <h1>{t('nav.chats')}</h1>
      <span class="grow"></span>
      <button class="icon-btn" title={t('chat.newChat')} aria-label={t('chat.newChat')} onclick={() => app.openCompose()}><SquarePen size={18} /></button>
      <button class="icon-btn" title={t('mail.view.classic') + ' (Ctrl+Shift+V)'} aria-label={t('mail.view.classic')} onclick={() => app.setViewMode('classic')}><Rows3 size={18} /></button>
    {/if}
  </header>

  <div class="search" role="search">
    <Search size={16} aria-hidden="true" />
    <input type="search" bind:value={query} oninput={onSearch} placeholder={t('chat.searchPlaceholder')} aria-label={t('chat.searchPlaceholder')} data-search />
    {#if query}<button class="clear" aria-label={t('search.bar.clear')} onclick={() => ((query = ''), app.setChatQuery(''))}><X size={14} /></button>{/if}
  </div>

  {#if !app.showArchived}
    <div class="pills" role="radiogroup" aria-label={t('chat.filterLabel')}>
      {#each FILTERS as f (f)}
        <button class="pill" role="radio" aria-checked={app.chatFilter === f} onclick={() => app.setChatFilter(f)}>{t(`chat.filter.${f}`)}</button>
      {/each}
    </div>
  {/if}

  <div
    class="items"
    role="listbox"
    tabindex="0"
    aria-label={t('chat.listLabel')}
    bind:this={listEl}
    {onkeydown}
    aria-activedescendant={app.selectedConversationId != null ? `chat-${app.selectedConversationId}` : undefined}
    data-list="conversations"
  >
    {#if !app.showArchived && app.archivedCount > 0 && app.chatFilter === 'all' && !app.chatQuery}
      <button class="archived-row" onclick={() => app.toggleArchivedView(true)}>
        <span class="arch-ic"><Archive size={18} /></span>
        <span class="arch-label">{t('chat.archived')}</span>
        <span class="arch-count">{app.archivedCount}</span>
      </button>
    {/if}
    {#each app.conversations as c (c.id)}
      {@const selected = c.id === app.selectedConversationId}
      {@const unread = c.unreadCount > 0 || c.markedUnread}
      <div
        id="chat-{c.id}"
        class="item"
        class:selected
        class:unread
        role="option"
        aria-selected={selected}
        tabindex="-1"
        onclick={() => app.selectConversation(c.id)}
        onkeydown={() => {}}
        use:longpress={(x, y) => openMenu(c, x, y)}
        use:swipe={{ left: () => app.updateConversation(c.id, { archived: !c.archived }, c.archived ? 'chat.toast.unarchived' : 'chat.toast.archived'), right: () => app.updateConversation(c.id, { markedUnread: !unread }) }}
      >
        <ChatAvatar name={c.title} seed={avatarSeed(c)} size={48} group={c.isGroup && c.kind === 'chat'} url={c.avatarUrl} channel={c.lastChannel ?? c.channels[0]} />
        <div class="body">
          <div class="l1">
            <span class="title">{c.title}</span>
            <time class="time" datetime={c.lastActivity}>{formatListDate(c.lastActivity)}</time>
          </div>
          <div class="l2">
            <span class="snippet">
              {#if c.lastIsOwn}
                <span class="tick tick-{c.lastStatus}" aria-label={t(`chat.status.${c.lastStatus ?? 'sent'}`)}>
                  {#if c.lastStatus === 'pending'}<Clock size={13} />{:else if c.lastStatus === 'delivered' || c.lastStatus === 'read'}<CheckCheck size={14} />{:else}<Check size={14} />{/if}
                </span>
                <span class="who">{t('chat.you')}:</span>
              {:else if c.lastSenderName && c.isGroup}
                <span class="who">{c.lastSenderName}:</span>
              {/if}
              {preview(c)}
            </span>
            <span class="marks">
              {#if c.muted}<BellOff size={14} class="mark" aria-label={t('chat.muted')} />{/if}
              {#if c.pinned}<Pin size={14} class="mark" aria-label={t('chat.pinned')} />{/if}
              {#if c.unreadCount > 0}
                <span class="badge" class:muted={c.muted} aria-label={t('mail.folder.unreadCount', { count: c.unreadCount })}>{c.unreadCount}</span>
              {:else if c.markedUnread}
                <span class="badge dot" aria-label={t('chat.markedUnread')}></span>
              {/if}
            </span>
          </div>
        </div>
        <button class="more" aria-label={t('chat.chatMenu', { name: c.title })} onclick={(e) => (e.stopPropagation(), openMenu(c, e.clientX, e.clientY))}>
          <ChevronDown size={17} />
        </button>
      </div>
    {:else}
      {#if !app.conversationsLoading}
        <div class="empty">
          <MessagesSquare size={36} strokeWidth={1.4} aria-hidden="true" />
          <p>{app.chatQuery ? t('chat.noResults') : t('chat.empty')}</p>
        </div>
      {/if}
    {/each}
  </div>
  {#if prefs.grouping === 'thread'}
    <p class="grouping-hint">{t('chat.groupedByTopic')}</p>
  {/if}
</section>

{#if menu}
  <ContextMenu x={menu.x} y={menu.y} items={menuItems(menu.conv)} label={t('chat.chatMenu', { name: menu.conv.title })} onclose={() => (menu = null)} />
{/if}
{#if confirmDelete}
  {@const c = confirmDelete}
  <ConfirmDialog
    title={t('chat.deleteTitle', { name: c.title })}
    message={c.channels.includes('email') ? t('chat.deleteMessageMail') : t('chat.deleteMessage')}
    confirmLabel={t('chat.action.delete')}
    danger
    onconfirm={() => {
      confirmDelete = null;
      void app.deleteConversation(c.id);
    }}
    oncancel={() => (confirmDelete = null)}
  />
{/if}

<style>
  .clist {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--tm-surface);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 12px 12px 6px 16px;
    min-height: 52px;
    box-sizing: border-box;
  }
  h1 {
    margin: 0;
    font-size: 20px;
    font-weight: 700;
    letter-spacing: -0.02em;
  }
  .grow {
    flex: 1;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    margin: 4px 12px 8px;
    padding: 0 8px 0 12px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    border: 1px solid transparent;
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
  .pills {
    display: flex;
    gap: 6px;
    padding: 0 12px 8px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .pills::-webkit-scrollbar {
    display: none;
  }
  .pill {
    flex: none;
    height: 28px;
    padding: 0 12px;
    border-radius: 99px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-muted);
    font: inherit;
    font-size: 12.5px;
    font-weight: 500;
    transition:
      background var(--tm-dur),
      color var(--tm-dur);
  }
  .pill:hover {
    background: var(--tm-surface-2);
  }
  .pill[aria-checked='true'] {
    background: var(--tm-accent-soft);
    border-color: transparent;
    color: var(--tm-accent-text);
    font-weight: 650;
  }
  .items {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    outline: none;
    border-top: 1px solid var(--tm-line);
  }
  .items:focus-visible .item.selected {
    box-shadow: inset 3px 0 0 var(--tm-focus);
  }
  .archived-row {
    display: flex;
    align-items: center;
    gap: 14px;
    width: 100%;
    padding: 10px 16px;
    border: none;
    border-bottom: 1px solid var(--tm-line);
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
  }
  .archived-row:hover {
    background: var(--tm-surface-2);
  }
  .arch-ic {
    width: 48px;
    display: inline-flex;
    justify-content: center;
    color: var(--tm-accent-text);
  }
  .arch-label {
    flex: 1;
    font-weight: 600;
  }
  .arch-count {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .item {
    position: relative;
    display: flex;
    gap: 12px;
    align-items: center;
    padding: 9px 12px 9px 16px;
    cursor: default;
    background: var(--tm-surface);
    transition:
      background var(--tm-dur),
      transform 120ms ease-out;
    touch-action: pan-y;
  }
  .item::after {
    content: '';
    position: absolute;
    left: 76px;
    right: 0;
    bottom: 0;
    height: 1px;
    background: var(--tm-line);
  }
  .item:hover {
    background: var(--tm-surface-2);
  }
  .item.selected {
    background: var(--tm-accent-soft);
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .l1,
  .l2 {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .title {
    flex: 1;
    min-width: 0;
    font-size: 15px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .time {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .item.unread .time {
    color: var(--tm-brand);
    font-weight: 650;
  }
  .l2 {
    margin-top: 2px;
  }
  .snippet {
    flex: 1;
    min-width: 0;
    font-size: 13.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .item.unread .snippet {
    color: var(--tm-fg);
  }
  .who {
    font-weight: 500;
    color: var(--tm-fg);
    margin-right: 2px;
  }
  .tick {
    display: inline-flex;
    vertical-align: -2px;
    margin-right: 3px;
    color: var(--tm-faint);
  }
  .tick-read {
    color: #3b9be6;
  }
  .marks {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex: none;
  }
  .marks :global(.mark) {
    color: var(--tm-faint);
  }
  .badge {
    min-width: 20px;
    height: 20px;
    padding: 0 6px;
    border-radius: 99px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11.5px;
    font-weight: 700;
    background: var(--tm-brand);
    color: var(--tm-brand-navy);
  }
  .badge.muted {
    background: var(--tm-faint);
    color: var(--tm-surface);
  }
  .badge.dot {
    min-width: 12px;
    width: 12px;
    height: 12px;
    padding: 0;
  }
  .more {
    position: absolute;
    right: 10px;
    top: 10px;
    display: inline-flex;
    padding: 2px;
    border: none;
    border-radius: 6px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    opacity: 0;
    transition: opacity var(--tm-dur);
  }
  .item:hover .more,
  .more:focus-visible {
    opacity: 1;
  }
  .item:hover .time {
    visibility: hidden;
  }
  @media (hover: none) {
    .more {
      display: none;
    }
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 48px 16px;
    color: var(--tm-faint);
    text-align: center;
  }
  .grouping-hint {
    margin: 0;
    padding: 6px 16px;
    font-size: 11.5px;
    color: var(--tm-faint);
    border-top: 1px solid var(--tm-line);
  }
</style>
