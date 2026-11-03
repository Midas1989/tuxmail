<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Paperclip from '@lucide/svelte/icons/paperclip';
  import Star from '@lucide/svelte/icons/star';
  import Reply from '@lucide/svelte/icons/reply';
  import Forward from '@lucide/svelte/icons/forward';
  import Archive from '@lucide/svelte/icons/archive';
  import Trash from '@lucide/svelte/icons/trash';
  import MailOpen from '@lucide/svelte/icons/mail-open';
  import Mail from '@lucide/svelte/icons/mail';
  import Inbox from '@lucide/svelte/icons/inbox';
  import { app, prefs } from '../../state';
  import { FLAG_ANSWERED, FLAG_FLAGGED, FLAG_FORWARDED, FLAG_SEEN, type MessageSummary } from '../../types';
  import { formatListDate, t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';

  const OVERSCAN = 6;

  let viewport = $state<HTMLDivElement>();
  let scrollTop = $state(0);
  let height = $state(600);
  let rowH = $state(68);

  // Row height follows the density token.
  $effect(() => {
    void prefs.density;
    if (!viewport) return;
    const v = parseFloat(getComputedStyle(viewport).getPropertyValue('--tm-row-h'));
    if (v > 0) rowH = v;
  });

  const showRecipients = $derived.by(() => {
    const s = app.sel;
    if (s.kind !== 'folder') return false;
    const r = app.folder(s.folderId)?.role;
    return r === 'sent' || r === 'drafts';
  });

  const total = $derived(app.messages.length);
  const start = $derived(Math.max(0, Math.floor(scrollTop / rowH) - OVERSCAN));
  const end = $derived(Math.min(total, Math.ceil((scrollTop + height) / rowH) + OVERSCAN));
  const visible = $derived(app.messages.slice(start, end).map((m, i) => ({ m, index: start + i })));

  // Scroll the selected row into view when requested (keyboard nav, view switch).
  $effect(() => {
    void app.listScrollTick;
    const id = app.selectedMessageId;
    if (!viewport || id == null) return;
    const idx = app.messages.findIndex((m) => m.id === id);
    if (idx < 0) return;
    const top = idx * rowH;
    if (top < viewport.scrollTop) viewport.scrollTop = top;
    else if (top + rowH > viewport.scrollTop + height) viewport.scrollTop = top + rowH - height + (height > rowH * 3 ? rowH : 0);
  });

  function onscroll() {
    if (!viewport) return;
    scrollTop = viewport.scrollTop;
    if (scrollTop + height > total * rowH - rowH * 10) void app.loadMoreMessages();
  }

  function who(m: MessageSummary): string {
    if (showRecipients) return t('mail.list.toPrefix', { names: m.to.map((a) => a.name || a.email).join(', ') });
    const f = m.from[0];
    return f?.name || f?.email || t('mail.list.unknownSender');
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      app.moveSelection(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      app.moveSelection(-1);
    } else if (e.key === 'Home' && app.messages.length) {
      e.preventDefault();
      void app.selectMessage(app.messages[0].id, { scroll: true });
    } else if (e.key === 'End' && app.messages.length) {
      e.preventDefault();
      void app.selectMessage(app.messages[app.messages.length - 1].id, { scroll: true });
    }
  }

  const scopeLabel = $derived.by(() => {
    const s = app.sel;
    if (s.kind === 'unified') return s.role === 'inbox' ? t('mail.folder.unifiedInbox') : s.role === 'flagged' ? t('mail.folder.flagged') : t('mail.folder.unread');
    if (s.kind === 'folder') {
      const f = app.folder(s.folderId);
      return f ? (f.role ? t(`mail.folder.role.${f.role}` as 'mail.folder.role.inbox') : f.name) : '';
    }
    return '';
  });
  const scopeAccount = $derived(app.sel.kind === 'folder' ? app.account(app.sel.accountId)?.displayName : t('mail.sidebar.allAccounts'));
</script>

<section class="list" aria-label={t('mail.list.label')}>
  <header class="list-head">
    <div class="ttl">
      <h1>{scopeLabel}</h1>
      <span class="sub">{scopeAccount} · {t('mail.list.count', { count: app.messagesTotal })}</span>
    </div>
  </header>
  <div
    class="viewport"
    bind:this={viewport}
    bind:clientHeight={height}
    {onscroll}
    role="listbox"
    tabindex="0"
    aria-label={scopeLabel}
    aria-activedescendant={app.selectedMessageId != null ? `msg-${app.selectedMessageId}` : undefined}
    {onkeydown}
    data-list="messages"
  >
    {#if total === 0 && !app.messagesLoading}
      <div class="empty">
        <Inbox size={36} strokeWidth={1.4} aria-hidden="true" />
        <p class="e1">{app.search ? t('mail.list.noResults') : t('mail.list.empty')}</p>
        <p class="e2">{app.search ? t('mail.list.noResultsHint') : t('mail.list.emptyHint')}</p>
      </div>
    {/if}
    <div class="spacer" style:height="{total * rowH}px">
      {#each visible as { m, index } (m.id)}
        {@const unread = !m.flags.includes(FLAG_SEEN)}
        {@const flagged = m.flags.includes(FLAG_FLAGGED)}
        {@const selected = m.id === app.selectedMessageId}
        {@const acc = app.account(m.accountId)}
        <div
          id="msg-{m.id}"
          class="row"
          class:unread
          class:selected
          role="option"
          aria-selected={selected}
          aria-posinset={index + 1}
          aria-setsize={total}
          style:transform="translateY({index * rowH}px)"
          style:height="{rowH}px"
          onclick={() => app.selectMessage(m.id)}
          onkeydown={() => {}}
          tabindex="-1"
        >
          <span class="acc-bar" style:background={acc?.color} title={acc?.displayName}></span>
          <span class="unread-dot" aria-hidden="true"></span>
          {#if prefs.density !== 'compact'}
            <Avatar name={showRecipients ? (m.to[0]?.name ?? m.to[0]?.email ?? '?') : who(m)} seed={showRecipients ? m.to[0]?.email : m.from[0]?.email} size={34} />
          {/if}
          <div class="body">
            <div class="l1">
              <span class="from">{who(m)}</span>
              <span class="icons">
                {#if m.flags.includes(FLAG_ANSWERED)}<Reply size={13} aria-label={t('mail.list.answered')} />{/if}
                {#if m.flags.includes(FLAG_FORWARDED)}<Forward size={13} aria-label={t('mail.list.forwarded')} />{/if}
                {#if m.hasAttachments}<Paperclip size={13} aria-label={t('mail.list.hasAttachments')} />{/if}
                {#if flagged}<Star size={13} class="star" aria-label={t('mail.list.flagged')} />{/if}
              </span>
              <time class="date" datetime={m.date}>{formatListDate(m.date)}</time>
            </div>
            <div class="l2">
              <span class="subject">{m.subject || t('mail.list.noSubject')}</span>
              {#if prefs.density === 'compact'}<span class="preview-inline">— {m.preview}</span>{/if}
            </div>
            {#if prefs.density !== 'compact'}
              <div class="l3">{m.preview}</div>
            {/if}
          </div>
          <div class="hover-actions">
            <button class="icon-btn" title={t('common.action.archive')} aria-label={t('common.action.archive')} onclick={(e) => (e.stopPropagation(), app.moveToRole(m.id, 'archive'))}><Archive size={15} /></button>
            <button class="icon-btn" title={t('common.action.delete')} aria-label={t('common.action.delete')} onclick={(e) => (e.stopPropagation(), app.moveToRole(m.id, 'trash'))}><Trash size={15} /></button>
            <button class="icon-btn" title={unread ? t('common.action.markRead') : t('common.action.markUnread')} aria-label={unread ? t('common.action.markRead') : t('common.action.markUnread')} onclick={(e) => (e.stopPropagation(), app.toggleRead(m.id))}>
              {#if unread}<MailOpen size={15} />{:else}<Mail size={15} />{/if}
            </button>
            <button class="icon-btn" title={t('common.action.flag')} aria-label={t('common.action.flag')} aria-pressed={flagged} onclick={(e) => (e.stopPropagation(), app.toggleFlag(m.id))}><Star size={15} /></button>
          </div>
        </div>
      {/each}
    </div>
  </div>
</section>

<style>
  .list {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--tm-surface);
  }
  .list-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px var(--tm-pad-x) 10px;
    border-bottom: 1px solid var(--tm-line);
  }
  h1 {
    margin: 0;
    font-size: 17px;
    font-weight: 650;
    letter-spacing: -0.01em;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .viewport {
    position: relative;
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    outline: none;
  }
  .viewport:focus-visible .row.selected {
    box-shadow: inset 0 0 0 2px var(--tm-focus);
  }
  .spacer {
    position: relative;
  }
  .row {
    position: absolute;
    left: 0;
    right: 0;
    top: 0;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: var(--tm-pad-y) var(--tm-pad-x) var(--tm-pad-y) calc(var(--tm-pad-x) + 4px);
    border-bottom: 1px solid var(--tm-line);
    cursor: default;
    box-sizing: border-box;
    transition: background var(--tm-dur);
  }
  .row:hover {
    background: var(--tm-surface-2);
  }
  .row.selected {
    background: var(--tm-accent-soft);
  }
  .acc-bar {
    position: absolute;
    left: 0;
    top: 8px;
    bottom: 8px;
    width: 3px;
    border-radius: 0 3px 3px 0;
    opacity: 0.85;
  }
  .unread-dot {
    position: absolute;
    left: 8px;
    top: calc(var(--tm-pad-y) + 6px);
    width: 7px;
    height: 7px;
    border-radius: 99px;
    background: transparent;
  }
  .row.unread .unread-dot {
    background: var(--tm-accent);
  }
  .body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .l1,
  .l2 {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .from {
    flex: 1;
    min-width: 0;
    font-size: 13.5px;
    color: var(--tm-fg);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row.unread .from,
  .row.unread .subject {
    font-weight: 650;
  }
  .icons {
    display: inline-flex;
    gap: 4px;
    color: var(--tm-faint);
  }
  .icons :global(.star) {
    color: #f59e0b;
    fill: #f59e0b;
  }
  .date {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .row.unread .date {
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .subject {
    font-size: 13px;
    color: var(--tm-fg);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: none;
    max-width: 100%;
  }
  .preview-inline {
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
    font-size: 12.5px;
  }
  .l3 {
    font-size: 12.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .hover-actions {
    position: absolute;
    right: 8px;
    bottom: 6px;
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: 8px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow);
    opacity: 0;
    pointer-events: none;
    transition: opacity var(--tm-dur);
  }
  .hover-actions .icon-btn {
    width: 28px;
    height: 26px;
  }
  .row:hover .hover-actions {
    opacity: 1;
    pointer-events: auto;
  }
  @media (hover: none) {
    .hover-actions {
      display: none;
    }
  }
  .empty {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--tm-faint);
    text-align: center;
    padding: 24px;
  }
  .e1 {
    margin: 10px 0 2px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .e2 {
    margin: 0;
    font-size: 12.5px;
  }
</style>
