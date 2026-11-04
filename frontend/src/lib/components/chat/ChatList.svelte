<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Messenger-style chat list: search, filter pills, archived row, pinned on top. -->
<script lang="ts">
  import { comfort } from '../../state';
  import CategoryTabs from '../comfort/CategoryTabs.svelte';
  import TeamSection from '../team/TeamSection.svelte';
  import TeamBadge from '../team/TeamBadge.svelte';
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
  import Target from '@lucide/svelte/icons/target';
  import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
  import AlarmClock from '@lucide/svelte/icons/alarm-clock';
  import Plus from '@lucide/svelte/icons/plus';
  import FolderPlus from '@lucide/svelte/icons/folder-plus';
  import Hourglass from '@lucide/svelte/icons/hourglass';
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
  let headerMenu = $state<{ x: number; y: number } | null>(null);
  let tabMenu = $state<{ x: number; y: number; id: string } | null>(null);
  let dropTarget = $state<string | null>(null);

  const headerItems = $derived<MenuItem[]>([
    { id: 'starred', label: t('chat.starred.title'), icon: Star, run: () => void (app.chatDialog = { kind: 'starred' }) },
    { id: 'scheduled', label: t('chat.schedule.overview'), icon: Clock, run: () => void (app.chatDialog = { kind: 'scheduled' }) },
    { id: 'focus', label: t('chat.focus.title'), icon: Target, run: () => void (app.chatDialog = { kind: 'focus' }) },
    { id: 'folder', label: t('chat.folders.new'), icon: FolderPlus, run: () => void (app.chatDialog = { kind: 'folder', folder: null }) },
  ]);

  function ondragstart(e: DragEvent, c: Conversation) {
    e.dataTransfer?.setData('application/x-tuxmail-chat', String(c.id));
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copy';
  }
  function ondrop(e: DragEvent, folderId: string) {
    e.preventDefault();
    dropTarget = null;
    const id = Number(e.dataTransfer?.getData('application/x-tuxmail-chat'));
    if (id) void app.addToFolder(id, folderId);
  }
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
      { id: 'snooze', label: t('chat.snooze.menu'), icon: AlarmClock, run: () => void (app.chatDialog = { kind: 'snooze', conversation: c }) },
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
      <button class="icon-btn" class:focus-on={app.focusActive} title={t('chat.focus.toggle')} aria-label={t('chat.focus.toggle')} aria-pressed={app.focus.enabled} onclick={() => app.toggleFocus()}><Target size={18} /></button>
      <button class="icon-btn" title={t('chat.newChat')} aria-label={t('chat.newChat')} onclick={() => app.openCompose()}><SquarePen size={18} /></button>
      <button class="icon-btn" title={t('mail.view.classic') + ' (Ctrl+Shift+V)'} aria-label={t('mail.view.classic')} onclick={() => app.setViewMode('classic')}><Rows3 size={18} /></button>
      <button class="icon-btn" title={t('chat.moreActions')} aria-label={t('chat.moreActions')} aria-haspopup="menu" onclick={(e) => (headerMenu = { x: e.clientX - 220, y: e.clientY + 12 })}><EllipsisVertical size={18} /></button>
    {/if}
  </header>
  <TeamSection variant="strip" />

  <div class="search" role="search">
    <Search size={16} aria-hidden="true" />
    <input type="search" bind:value={query} oninput={onSearch} placeholder={t('chat.searchPlaceholder')} aria-label={t('chat.searchPlaceholder')} data-search />
    {#if query}<button class="clear" aria-label={t('search.bar.clear')} onclick={() => ((query = ''), app.setChatQuery(''))}><X size={14} /></button>{/if}
  </div>

  {#if !app.showArchived}
    <div class="tabs" role="tablist" aria-label={t('chat.folders.label')}>
      <button role="tab" class="tab" aria-selected={app.activeFolderId === null} onclick={() => app.setActiveFolder(null)}>{t('chat.folders.all')}</button>
      {#each app.chatFolders as f (f.id)}
        <button
          role="tab"
          class="tab"
          class:drop={dropTarget === f.id}
          aria-selected={app.activeFolderId === f.id}
          onclick={() => app.setActiveFolder(f.id)}
          ondblclick={() => (f.searchFolderId ? (app.organizeDialog = { kind: 'searchFolder', folder: app.searchFolder(f.searchFolderId) }) : (app.chatDialog = { kind: 'folder', folder: f }))}
          oncontextmenu={(e) => {
            e.preventDefault();
            tabMenu = { x: e.clientX, y: e.clientY, id: f.id };
          }}
          ondragover={(e) => {
            if (f.searchFolderId) return; // search folders are read-only
            e.preventDefault();
            dropTarget = f.id;
          }}
          ondragleave={() => (dropTarget = null)}
          ondrop={(e) => ondrop(e, f.id)}
        >
          {#if f.emoji}<span aria-hidden="true">{f.emoji}</span>{/if}
          {f.name}
          {#if f.unreadCount}<span class="tab-count" aria-label={t('mail.folder.unreadCount', { count: f.unreadCount })}>{f.unreadCount}</span>{/if}
        </button>
      {/each}
      <button class="tab add" aria-label={t('chat.folders.new')} title={t('chat.folders.new')} onclick={() => (app.chatDialog = { kind: 'folder', folder: null })}><Plus size={14} /></button>
    </div>
    {#if app.focusActive}
      <div class="focus-banner" role="status"><Target size={14} /> {t('chat.focus.active')}</div>
    {/if}
    <div class="pills" role="radiogroup" aria-label={t('chat.filterLabel')}>
      {#each FILTERS as f (f)}
        <button class="pill" role="radio" aria-checked={app.chatFilter === f} onclick={() => app.setChatFilter(f)}>{t(`chat.filter.${f}`)}</button>
      {/each}
    </div>
    <CategoryTabs variant="pills" />
    {#if app.tags.length}
      <div class="pills tag-pills" role="group" aria-label={t('organize.tags.filter')}>
        {#each app.tags.filter((x) => x.total > 0) as x (x.tag.id)}
          <button class="pill tag-pill" style:--c={x.tag.color} aria-pressed={app.chatTagId === x.tag.id} onclick={() => app.setChatTag(app.chatTagId === x.tag.id ? null : x.tag.id)} title={x.tag.path}>
            <span class="tdot"></span>{x.tag.name}
          </button>
        {/each}
      </div>
    {/if}
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
    {#each comfort.filterConversations(app.conversations) as c (c.id)}
      {@render chatRow(c)}
    {:else}
      {#if !app.conversationsLoading && !(app.focusActive && app.waitingCount)}
        <div class="empty">
          <MessagesSquare size={36} strokeWidth={1.4} aria-hidden="true" />
          <p>{app.chatQuery ? t('chat.noResults') : t('chat.empty')}</p>
        </div>
      {/if}
    {/each}
    {#if app.focusActive && app.waitingCount > 0 && !app.showArchived}
      <button class="waiting-row" aria-expanded={app.waitingOpen} onclick={() => (app.waitingOpen = !app.waitingOpen)}>
        <span class="arch-ic"><Hourglass size={18} /></span>
        <span class="arch-label">{t('chat.focus.waiting', { count: app.waitingCount })}</span>
        <ChevronDown size={16} class={app.waitingOpen ? 'rot' : ''} />
      </button>
      {#if app.waitingOpen}
        <div class="waiting-list">
          {#each app.waitingConversations as c (c.id)}{@render chatRow(c)}{/each}
        </div>
      {/if}
    {/if}
  </div>
  {#if prefs.grouping === 'thread'}
    <p class="grouping-hint">{t('chat.groupedByTopic')}</p>
  {/if}
</section>

{#snippet chatRow(c: Conversation)}
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
        draggable="true"
        ondragstart={(e) => ondragstart(e, c)}
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
              {#if c.draft && c.id !== app.selectedConversationId}
                <span class="draft">{t('chat.draft.label')}:</span> {c.draft}
              {:else if c.lastIsOwn}
                <span class="tick tick-{c.lastStatus}" aria-label={t(`chat.status.${c.lastStatus ?? 'sent'}`)}>
                  {#if c.lastStatus === 'pending'}<Clock size={13} />{:else if c.lastStatus === 'delivered' || c.lastStatus === 'read'}<CheckCheck size={14} />{:else}<Check size={14} />{/if}
                </span>
                <span class="who">{t('chat.you')}:</span>
              {:else if c.lastSenderName && c.isGroup}
                <span class="who">{c.lastSenderName}:</span>
              {/if}
              {#if !(c.draft && c.id !== app.selectedConversationId)}{preview(c)}{/if}
            </span>
            <span class="marks">
              <TeamBadge conversation={c} />
              {#if c.returnedFromSnooze}<span class="back" title={t('chat.snooze.back')}><AlarmClock size={12} /> {t('chat.snooze.backShort')}</span>{/if}
              {#if c.scheduledCount}<Clock size={13} class="mark sched" aria-label={t('chat.schedule.count', { count: c.scheduledCount })} />{/if}
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
{/snippet}

{#if menu}
  <ContextMenu x={menu.x} y={menu.y} items={menuItems(menu.conv)} label={t('chat.chatMenu', { name: menu.conv.title })} onclose={() => (menu = null)} />
{/if}
{#if headerMenu}
  <ContextMenu x={headerMenu.x} y={headerMenu.y} items={headerItems} label={t('chat.moreActions')} onclose={() => (headerMenu = null)} width={250} />
{/if}
{#if tabMenu}
  {@const f = app.chatFolders.find((x) => x.id === tabMenu!.id)}
  {#if f}
    <ContextMenu
      x={tabMenu.x}
      y={tabMenu.y}
      label={f.name}
      onclose={() => (tabMenu = null)}
      items={f.searchFolderId
        ? [{ id: 'edit', label: t('organize.searchFolder.editTitle'), icon: FolderPlus, run: () => void (app.organizeDialog = { kind: 'searchFolder', folder: app.searchFolder(f.searchFolderId!) }) }]
        : [
            { id: 'edit', label: t('chat.folders.edit'), icon: FolderPlus, run: () => void (app.chatDialog = { kind: 'folder', folder: f }) },
            { id: 'del', label: t('chat.folders.delete'), icon: Trash, danger: true, run: () => app.deleteChatFolder(f.id) },
          ]}
    />
  {/if}
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
  .tabs {
    display: flex;
    gap: 2px;
    padding: 0 10px;
    overflow-x: auto;
    scrollbar-width: none;
    border-bottom: 1px solid var(--tm-line);
    margin-bottom: 8px;
  }
  .tabs::-webkit-scrollbar {
    display: none;
  }
  .tab {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 34px;
    padding: 0 10px;
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--tm-muted);
    font: inherit;
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
  }
  .tab[aria-selected='true'] {
    color: var(--tm-accent-text);
    border-bottom-color: var(--tm-brand);
  }
  .tab.drop {
    background: var(--tm-accent-soft);
    border-radius: 8px 8px 0 0;
  }
  .tab.add {
    color: var(--tm-faint);
  }
  .tab-count {
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 99px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
    font-size: 11px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .tab[aria-selected='true'] .tab-count {
    background: var(--tm-brand);
    color: var(--tm-brand-navy);
  }
  .focus-banner {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 12px 8px;
    padding: 5px 10px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-brand) 14%, var(--tm-surface));
    color: var(--tm-fg);
    font-size: 12.5px;
    font-weight: 600;
  }
  .focus-on {
    color: var(--tm-brand) !important;
    background: color-mix(in srgb, var(--tm-brand) 14%, transparent) !important;
  }
  .draft {
    color: var(--tm-danger);
    font-weight: 600;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    padding: 0 6px;
    height: 18px;
    border-radius: 99px;
    background: color-mix(in srgb, var(--tm-brand) 18%, transparent);
    color: #b45309;
    font-size: 10.5px;
    font-weight: 700;
  }
  .waiting-row {
    display: flex;
    align-items: center;
    gap: 14px;
    width: 100%;
    padding: 12px 16px;
    border: none;
    border-top: 1px solid var(--tm-line);
    background: var(--tm-surface-2);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
  }
  .waiting-row :global(.rot) {
    transform: rotate(180deg);
  }
  .waiting-list {
    opacity: 0.85;
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
  .tag-pills {
    margin-top: -2px;
  }
  .tag-pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 24px;
    padding: 0 9px;
    font-size: 12px;
  }
  .tag-pill[aria-pressed='true'] {
    background: color-mix(in srgb, var(--c) 16%, transparent);
    border-color: var(--c);
    color: color-mix(in srgb, var(--c) 70%, var(--tm-fg));
    font-weight: 650;
  }
  .tdot {
    width: 7px;
    height: 7px;
    border-radius: 99px;
    background: var(--c);
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
