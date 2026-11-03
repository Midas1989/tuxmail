<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Messenger conversation pane: header, in-chat search, timeline, composer. -->
<script lang="ts">
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Search from '@lucide/svelte/icons/search';
  import Info from '@lucide/svelte/icons/info';
  import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
  import ChevronUp from '@lucide/svelte/icons/chevron-up';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import ArrowDown from '@lucide/svelte/icons/arrow-down';
  import X from '@lucide/svelte/icons/x';
  import Reply from '@lucide/svelte/icons/reply';
  import Forward from '@lucide/svelte/icons/forward';
  import Copy from '@lucide/svelte/icons/copy';
  import ListTodo from '@lucide/svelte/icons/list-todo';
  import CalendarPlus from '@lucide/svelte/icons/calendar-plus';
  import Mail from '@lucide/svelte/icons/mail';
  import Trash from '@lucide/svelte/icons/trash';
  import Plus from '@lucide/svelte/icons/plus';
  import Pin from '@lucide/svelte/icons/pin';
  import PinOff from '@lucide/svelte/icons/pin-off';
  import Bell from '@lucide/svelte/icons/bell';
  import BellOff from '@lucide/svelte/icons/bell-off';
  import Archive from '@lucide/svelte/icons/archive';
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import { app } from '../../state';
  import { buildTimeline } from '../../conversation';
  import type { Attachment, ConversationItem } from '../../types';
  import { fmt, formatDay, t } from '../../i18n/index.svelte';
  import ChatAvatar from './ChatAvatar.svelte';
  import MessageBubble from './MessageBubble.svelte';
  import MessengerComposer from './MessengerComposer.svelte';
  import EmojiPicker, { QUICK_REACTIONS } from './EmojiPicker.svelte';
  import ContextMenu, { type MenuItem } from '../common/ContextMenu.svelte';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';

  let scroller = $state<HTMLDivElement>();
  let atBottom = $state(true);
  let newCount = $state(0);
  let lastLen = 0;
  let lastConv: number | null = null;

  let searchOpen = $state(false);
  let searchQuery = $state('');
  let hitIndex = $state(0);
  let searchInput = $state<HTMLInputElement>();

  let bubbleMenu = $state<{ x: number; y: number; item: ConversationItem } | null>(null);
  let headerMenu = $state<{ x: number; y: number } | null>(null);
  let reactPicker = $state<ConversationItem | null>(null);
  let confirmDeleteItem = $state<ConversationItem | null>(null);
  let confirmDeleteChat = $state(false);
  let lightbox = $state<{ images: Attachment[]; index: number } | null>(null);

  const conv = $derived(app.conversation);
  const isGroupChat = $derived(!!conv && conv.isGroup);
  const timeline = $derived(buildTimeline(app.items, { dayKey: (iso) => fmt().dayKey(iso), unreadFromId: app.unreadFromId }));

  const hits = $derived.by(() => {
    const q = searchQuery.trim().toLowerCase();
    if (!q) return [] as string[];
    return app.items.filter((i) => (i.body + ' ' + (i.card?.title ?? '')).toLowerCase().includes(q)).map((i) => i.id);
  });
  const activeHitId = $derived(hits.length ? hits[Math.min(hitIndex, hits.length - 1)] : null);

  const subtitle = $derived.by(() => {
    if (!conv) return '';
    if (conv.isGroup) {
      return conv.participantIds
        .map((id) => app.person(id)?.displayName.split(' ')[0])
        .filter(Boolean)
        .concat(t('chat.you'))
        .join(', ');
    }
    const p = app.person(conv.participantIds[0]);
    const kinds = new Set(p?.handles.map((h) => h.kind) ?? []);
    const names: string[] = [];
    if (kinds.has('email')) names.push(t('common.channel.email'));
    for (const ch of ['whatsapp', 'signal', 'telegram'] as const) if (kinds.has(ch)) names.push(t(`common.channel.${ch}`));
    if (p?.lastSeen && names.length > 1) return t('chat.lastSeen', { when: fmt().relative(p.lastSeen) });
    return names.join(' · ');
  });

  function scrollToBottom(smooth = false) {
    const el = scroller;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
    newCount = 0;
  }

  function scrollToItem(id: string, block: 'center' | 'start' = 'center') {
    const el = scroller;
    const target = el?.querySelector<HTMLElement>(`#item-${CSS.escape(id)}`);
    if (!el || !target) return false;
    const row = target.closest<HTMLElement>('.row') ?? target;
    const top = row.offsetTop - (block === 'center' ? el.clientHeight / 2 - row.offsetHeight / 2 : 60);
    el.scrollTo({ top: Math.max(0, top) });
    return true;
  }

  // Explicit scroll requests (open chat, own message, view switch).
  $effect(() => {
    void app.timelineScrollTick;
    const target = app.timelineScrollTarget;
    requestAnimationFrame(() => {
      if (target && scrollToItem(target, app.unreadFromId === target ? 'start' : 'center')) {
        const bubble = scroller?.querySelector<HTMLElement>(`#item-${CSS.escape(target)}`);
        if (target === app.focusedItemId && !(document.activeElement as HTMLElement | null)?.closest('.composer')) bubble?.focus({ preventScroll: true });
      } else scrollToBottom();
      lastLen = app.items.length;
    });
  });

  // Incoming messages: follow only when already at the bottom.
  $effect(() => {
    const len = app.items.length;
    const id = app.selectedConversationId;
    if (id !== lastConv) {
      lastConv = id;
      lastLen = len;
      newCount = 0;
      searchOpen = false;
      searchQuery = '';
      return;
    }
    if (len > lastLen) {
      const added = app.items.slice(lastLen).filter((i) => !i.isOwn).length;
      if (atBottom) requestAnimationFrame(() => scrollToBottom(true));
      else newCount += added;
    }
    lastLen = len;
  });

  function onscroll() {
    const el = scroller;
    if (!el) return;
    atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    if (atBottom) newCount = 0;
  }

  // In-chat search navigation.
  $effect(() => {
    if (activeHitId) scrollToItem(activeHitId);
  });
  function stepHit(d: 1 | -1) {
    if (!hits.length) return;
    hitIndex = (hitIndex + d + hits.length) % hits.length;
  }
  function openSearch() {
    searchOpen = true;
    queueMicrotask(() => searchInput?.focus());
  }

  function jumpTo(itemId: string) {
    if (scrollToItem(itemId)) {
      app.focusItem(itemId);
      scroller?.querySelector<HTMLElement>(`#item-${CSS.escape(itemId)}`)?.focus({ preventScroll: true });
    }
  }

  function bubbleItems(item: ConversationItem): MenuItem[] {
    const list: MenuItem[] = [
      { id: 'reply', label: t('chat.menu.reply'), icon: Reply, run: () => void (app.replyingTo = item) },
      { id: 'forward', label: t('chat.menu.forward'), icon: Forward, run: () => void (app.forwarding = item) },
      { id: 'copy', label: t('chat.menu.copy'), icon: Copy, run: () => copy(item) },
      { id: 'task', label: t('chat.menu.task'), icon: ListTodo, run: () => app.createFromItem(item, 'task') },
      { id: 'event', label: t('chat.menu.event'), icon: CalendarPlus, run: () => app.createFromItem(item, 'event') },
    ];
    if (item.messageId != null) list.push({ id: 'original', label: t('chat.menu.original'), icon: Mail, run: () => app.showOriginal(item) });
    list.push({ id: 'delete', label: t('chat.menu.delete'), icon: Trash, danger: true, run: () => void (confirmDeleteItem = item) });
    return list;
  }

  async function copy(item: ConversationItem) {
    try {
      await navigator.clipboard.writeText(item.body || item.card?.excerpt || '');
      app.toast(t('chat.copied'));
    } catch {
      app.toast(t('chat.copyFailed'), undefined, 'error');
    }
  }

  const headerItems = $derived.by((): MenuItem[] => {
    if (!conv) return [];
    return [
      { id: 'search', label: t('chat.searchInChat'), icon: Search, run: openSearch },
      { id: 'pin', label: conv.pinned ? t('chat.action.unpin') : t('chat.action.pin'), icon: conv.pinned ? PinOff : Pin, run: () => app.updateConversation(conv.id, { pinned: !conv.pinned }) },
      { id: 'mute', label: conv.muted ? t('chat.action.unmute') : t('chat.action.mute'), icon: conv.muted ? Bell : BellOff, run: () => app.updateConversation(conv.id, { muted: !conv.muted }) },
      { id: 'archive', label: conv.archived ? t('chat.action.unarchive') : t('chat.action.archive'), icon: Archive, run: () => app.updateConversation(conv.id, { archived: !conv.archived }, conv.archived ? 'chat.toast.unarchived' : 'chat.toast.archived') },
      { id: 'classic', label: t('chat.openInClassic'), icon: Rows3, run: () => app.openInClassic(app.items.find((i) => i.id === app.focusedItemId) ?? null) },
      { id: 'delete', label: t('chat.action.delete'), icon: Trash, danger: true, run: () => void (confirmDeleteChat = true) },
    ];
  });

  function onKeydownLightbox(e: KeyboardEvent) {
    if (!lightbox) return;
    if (e.key === 'Escape') lightbox = null;
    else if (e.key === 'ArrowRight') lightbox.index = (lightbox.index + 1) % lightbox.images.length;
    else if (e.key === 'ArrowLeft') lightbox.index = (lightbox.index - 1 + lightbox.images.length) % lightbox.images.length;
  }
</script>

<svelte:window onkeydown={onKeydownLightbox} />

<section class="conv" aria-label={t('chat.timelineLabel')}>
  {#if conv}
    <header class="head">
      <button class="icon-btn back" aria-label={t('common.action.back')} onclick={() => app.selectConversation(null)}><ArrowLeft size={19} /></button>
      <button class="who" onclick={() => (app.infoOpen = !app.infoOpen)} aria-label={t('chat.info.toggle')} aria-expanded={app.infoOpen}>
        <ChatAvatar name={conv.title} seed={conv.kind === 'chat' && conv.isGroup ? conv.title : (app.person(conv.participantIds[0])?.handles[0]?.value ?? conv.title)} size={40} group={conv.isGroup && conv.kind === 'chat'} url={conv.avatarUrl} />
        <span class="names">
          <span class="title">{conv.title}</span>
          <span class="sub">{subtitle}</span>
        </span>
      </button>
      <button class="icon-btn" aria-label={t('chat.searchInChat')} title={t('chat.searchInChat')} aria-pressed={searchOpen} onclick={() => (searchOpen ? (searchOpen = false) : openSearch())}><Search size={18} /></button>
      <button class="icon-btn" aria-label={t('chat.info.toggle')} title={t('chat.info.toggle')} aria-pressed={app.infoOpen} onclick={() => (app.infoOpen = !app.infoOpen)}><Info size={18} /></button>
      <button class="icon-btn" aria-label={t('chat.moreActions')} title={t('chat.moreActions')} aria-haspopup="menu" onclick={(e) => (headerMenu = { x: e.clientX - 220, y: e.clientY + 12 })}><EllipsisVertical size={18} /></button>
    </header>

    {#if searchOpen}
      <div class="chat-search" role="search">
        <Search size={15} aria-hidden="true" />
        <input
          bind:this={searchInput}
          bind:value={searchQuery}
          oninput={() => (hitIndex = Math.max(0, hits.length - 1))}
          onkeydown={(e) => {
            if (e.key === 'Enter') stepHit(e.shiftKey ? 1 : -1);
            if (e.key === 'Escape') {
              e.stopPropagation();
              searchOpen = false;
              searchQuery = '';
            }
          }}
          placeholder={t('chat.searchInChat')}
          aria-label={t('chat.searchInChat')}
        />
        <span class="hits" aria-live="polite">{searchQuery ? t('chat.searchHits', { current: hits.length ? Math.min(hitIndex, hits.length - 1) + 1 : 0, total: hits.length }) : ''}</span>
        <button class="icon-btn" aria-label={t('chat.prevHit')} onclick={() => stepHit(-1)} disabled={!hits.length}><ChevronUp size={16} /></button>
        <button class="icon-btn" aria-label={t('chat.nextHit')} onclick={() => stepHit(1)} disabled={!hits.length}><ChevronDown size={16} /></button>
        <button
          class="icon-btn"
          aria-label={t('common.action.close')}
          onclick={() => {
            searchOpen = false;
            searchQuery = '';
          }}><X size={16} /></button
        >
      </div>
    {/if}

    <div class="scroll" bind:this={scroller} {onscroll} role="log" aria-live="polite" aria-relevant="additions" tabindex="-1">
      <div class="inner">
        {#each timeline as entry (entry.key)}
          {#if entry.type === 'day'}
            <div class="day" role="separator"><span>{formatDay(entry.date)}</span></div>
          {:else if entry.type === 'unread'}
            <div class="unread-marker" role="separator"><span>{t('chat.unreadMarker', { count: entry.count })}</span></div>
          {:else if entry.type === 'subject'}
            <div class="system" role="note"><span>{entry.changed ? t('chat.subjectChanged', { subject: entry.subject }) : t('chat.subject', { subject: entry.subject })}</span></div>
          {:else}
            <MessageBubble
              item={entry.item}
              groupStart={entry.groupStart}
              groupEnd={entry.groupEnd}
              channelChanged={entry.channelChanged}
              showReplyQuote={entry.showReplyQuote}
              {isGroupChat}
              focused={entry.item.id === app.focusedItemId}
              query={searchOpen ? searchQuery : ''}
              activeHit={entry.item.id === activeHitId}
              onmenu={(item, x, y) => (bubbleMenu = { item, x, y })}
              onreact={(item, emoji) => app.react(item, emoji)}
              onjump={jumpTo}
              onoriginal={(item) => app.showOriginal(item)}
              onimage={(item, index) => (lightbox = { images: item.attachments.filter((a) => a.previewUrl), index })}
              onfocusitem={(id) => app.focusItem(id)}
            />
          {/if}
        {/each}
      </div>
    </div>

    {#if !atBottom}
      <button class="fab" onclick={() => scrollToBottom(true)} aria-label={newCount ? t('chat.newMessages', { count: newCount }) : t('chat.scrollToBottom')}>
        <ArrowDown size={18} />
        {#if newCount}<span class="fab-badge">{newCount}</span>{/if}
      </button>
    {/if}

    <MessengerComposer />
  {:else}
    <div class="empty">
      <MessagesSquare size={52} strokeWidth={1.1} aria-hidden="true" />
      <p class="e1">{t('chat.noSelection')}</p>
      <p class="e2">{t('chat.noSelectionHint')}</p>
    </div>
  {/if}
</section>

{#if bubbleMenu}
  {@const it = bubbleMenu.item}
  <ContextMenu x={bubbleMenu.x} y={bubbleMenu.y} items={bubbleItems(it)} label={t('chat.messageMenu')} onclose={() => (bubbleMenu = null)} width={250}>
    {#snippet header()}
      <div class="quick" role="group" aria-label={t('chat.menu.react')}>
        {#each QUICK_REACTIONS as e (e)}
          <button
            class="qr"
            class:mine={it.reactions?.some((r) => r.mine && r.emoji === e)}
            data-menu-extra
            aria-label={t('chat.reactWith', { emoji: e })}
            onclick={() => {
              bubbleMenu = null;
              void app.react(it, e);
            }}>{e}</button
          >
        {/each}
        <button
          class="qr more"
          data-menu-extra
          aria-label={t('chat.moreReactions')}
          onclick={() => {
            bubbleMenu = null;
            reactPicker = it;
          }}><Plus size={16} /></button
        >
      </div>
    {/snippet}
  </ContextMenu>
{/if}
{#if headerMenu}
  <ContextMenu x={headerMenu.x} y={headerMenu.y} items={headerItems} label={t('chat.moreActions')} onclose={() => (headerMenu = null)} />
{/if}
{#if reactPicker}
  {@const it = reactPicker}
  <div class="react-picker">
    <EmojiPicker
      onpick={(e) => {
        reactPicker = null;
        void app.react(it, e);
      }}
      onclose={() => (reactPicker = null)}
    />
  </div>
{/if}
{#if confirmDeleteItem}
  {@const it = confirmDeleteItem}
  <ConfirmDialog
    title={t('chat.deleteMessageTitle')}
    message={it.channel === 'email' ? t('chat.deleteMailConfirm') : t('chat.deleteMessageConfirm')}
    confirmLabel={t('chat.menu.delete')}
    danger
    onconfirm={() => {
      confirmDeleteItem = null;
      void app.deleteItem(it);
    }}
    oncancel={() => (confirmDeleteItem = null)}
  />
{/if}
{#if confirmDeleteChat && conv}
  {@const c = conv}
  <ConfirmDialog
    title={t('chat.deleteTitle', { name: c.title })}
    message={c.channels.includes('email') ? t('chat.deleteMessageMail') : t('chat.deleteMessage')}
    confirmLabel={t('chat.action.delete')}
    danger
    onconfirm={() => {
      confirmDeleteChat = false;
      void app.deleteConversation(c.id);
    }}
    oncancel={() => (confirmDeleteChat = false)}
  />
{/if}
{#if lightbox}
  {@const img = lightbox.images[lightbox.index]}
  <div class="lightbox" role="dialog" aria-modal="true" aria-label={img.filename}>
    <button class="lb-close icon-btn" aria-label={t('common.action.close')} onclick={() => (lightbox = null)}><X size={22} /></button>
    <img src={img.previewUrl} alt={img.filename} />
    <span class="lb-name">{img.filename} · {lightbox.index + 1}/{lightbox.images.length}</span>
  </div>
{/if}

<style>
  .conv {
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background-color: var(--tm-chat-bg);
    background-image: radial-gradient(color-mix(in srgb, var(--tm-fg) 5%, transparent) 1px, transparent 1px);
    background-size: 22px 22px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 6px 10px 6px 12px;
    min-height: 58px;
    box-sizing: border-box;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
  }
  .back {
    display: none;
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 4px 6px;
    border: none;
    border-radius: 10px;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
  }
  .who:hover {
    background: var(--tm-surface-2);
  }
  .names {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .title {
    font-size: 15.5px;
    font-weight: 650;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    font-size: 12.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chat-search {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px 6px 14px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
    color: var(--tm-muted);
  }
  .chat-search input {
    flex: 1;
    min-width: 0;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
  }
  .hits {
    font-size: 12px;
    white-space: nowrap;
  }
  .scroll {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    outline: none;
  }
  .inner {
    max-width: 900px;
    margin: 0 auto;
    padding: 10px 4% 18px;
  }
  .day,
  .system,
  .unread-marker {
    display: flex;
    justify-content: center;
    margin: 12px 0 6px;
  }
  .day {
    position: sticky;
    top: 6px;
    z-index: 2;
  }
  .day span {
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-muted);
    background: var(--tm-surface);
    padding: 3px 12px;
    border-radius: 8px;
    box-shadow: var(--tm-shadow);
  }
  .system span {
    font-size: 11.5px;
    color: var(--tm-muted);
    background: color-mix(in srgb, var(--tm-surface) 75%, transparent);
    padding: 2px 10px;
    border-radius: 8px;
    max-width: 80%;
    text-align: center;
  }
  .unread-marker {
    margin: 14px -4%;
  }
  .unread-marker span {
    width: 100%;
    text-align: center;
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-accent-text);
    background: color-mix(in srgb, var(--tm-surface) 85%, transparent);
    padding: 4px 0;
  }
  .fab {
    position: absolute;
    right: 18px;
    bottom: 92px;
    z-index: 3;
    width: 42px;
    height: 42px;
    border-radius: 99px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-muted);
    box-shadow: var(--tm-shadow-lg);
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .fab-badge {
    position: absolute;
    top: -6px;
    right: -4px;
    min-width: 20px;
    height: 20px;
    padding: 0 5px;
    border-radius: 99px;
    background: var(--tm-brand);
    color: var(--tm-brand-navy);
    font-size: 11px;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .quick {
    display: flex;
    justify-content: space-between;
    gap: 2px;
  }
  .qr {
    width: 32px;
    height: 32px;
    border: none;
    border-radius: 99px;
    background: transparent;
    font-size: 19px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: transform 100ms;
  }
  .qr:hover,
  .qr:focus-visible {
    background: var(--tm-surface-2);
    transform: scale(1.15);
    outline: none;
  }
  .qr.mine {
    background: color-mix(in srgb, var(--tm-brand) 22%, transparent);
  }
  .qr.more {
    color: var(--tm-muted);
  }
  .react-picker {
    position: fixed;
    left: 50%;
    bottom: 120px;
    z-index: 72;
    transform: translateX(-50%);
    width: 320px;
    height: 0;
  }
  .lightbox {
    position: fixed;
    inset: 0;
    z-index: 80;
    background: rgb(5 8 14 / 0.92);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 10px;
  }
  .lightbox img {
    max-width: 92vw;
    max-height: 82vh;
    border-radius: 6px;
  }
  .lb-close {
    position: absolute;
    top: 14px;
    right: 14px;
    color: #fff;
  }
  .lb-name {
    color: #d7dbe3;
    font-size: 13px;
  }
  .empty {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--tm-faint);
    text-align: center;
    padding: 24px;
  }
  .e1 {
    margin: 14px 0 4px;
    font-size: 16px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .e2 {
    margin: 0;
    font-size: 13px;
  }
  @media (max-width: 899px) {
    .back {
      display: inline-flex;
    }
    .inner {
      padding: 8px 10px 14px;
    }
    .unread-marker {
      margin: 14px -10px;
    }
  }
</style>
