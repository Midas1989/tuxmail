<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Pin from '@lucide/svelte/icons/pin';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import { app, prefs } from '../../state';
  import { formatListDate, t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  let listEl = $state<HTMLDivElement>();

  // Keep the selected conversation visible (keyboard nav, view switch).
  $effect(() => {
    const id = app.selectedConversationId;
    if (id == null || !listEl) return;
    listEl.querySelector(`#conv-${id}`)?.scrollIntoView({ block: 'nearest' });
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      app.moveSelection(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      app.moveSelection(-1);
    }
  }

  const scopeLabel = $derived.by(() => {
    const s = app.sel;
    if (s.kind === 'account') return app.account(s.accountId)?.displayName ?? '';
    if (s.kind === 'folder') {
      const f = app.folder(s.folderId);
      return f ? (f.role ? t(`mail.folder.role.${f.role}` as 'mail.folder.role.inbox') : f.name) : '';
    }
    return t('chat.allConversations');
  });
</script>

<section class="clist" aria-label={t('chat.listLabel')}>
  <header class="head">
    <div class="ttl">
      <h1>{scopeLabel}</h1>
      <span class="sub">{t('chat.count', { count: app.conversations.length })}</span>
    </div>
    <div class="seg" role="radiogroup" aria-label={t('chat.grouping')}>
      <button role="radio" aria-checked={prefs.grouping === 'person'} onclick={() => app.setGrouping('person')}>{t('chat.byPerson')}</button>
      <button role="radio" aria-checked={prefs.grouping === 'thread'} onclick={() => app.setGrouping('thread')}>{t('chat.byThread')}</button>
    </div>
  </header>
  <div
    class="items"
    role="listbox"
    tabindex="0"
    aria-label={scopeLabel}
    bind:this={listEl}
    {onkeydown}
    aria-activedescendant={app.selectedConversationId != null ? `conv-${app.selectedConversationId}` : undefined}
    data-list="conversations"
  >
    {#each app.conversations as c (c.id)}
      {@const selected = c.id === app.selectedConversationId}
      <div id="conv-{c.id}" class="item" class:selected class:unread={c.unreadCount > 0} role="option" aria-selected={selected} tabindex="-1" onclick={() => app.selectConversation(c.id)} onkeydown={() => {}}>
        <Avatar name={c.title} seed={c.kind === 'chat' && c.isGroup ? c.title : (app.person(c.participantIds[0])?.handles[0]?.value ?? c.title)} size={42} group={c.isGroup && c.kind === 'chat'} />
        <div class="body">
          <div class="l1">
            <span class="title">{c.title}</span>
            <span class="chs">
              {#each c.channels as ch (ch)}<ChannelIcon channel={ch} size={13} />{/each}
            </span>
            <time class="time" datetime={c.lastActivity}>{formatListDate(c.lastActivity)}</time>
          </div>
          <div class="l2">
            <span class="snippet">
              {#if c.lastSenderName && (c.isGroup || c.kind === 'email_thread')}<span class="who">{c.lastSenderName}:</span>{/if}
              {c.snippet}
            </span>
            {#if c.pinned}<Pin size={13} class="pin" aria-label={t('chat.pinned')} />{/if}
            {#if c.unreadCount > 0}<span class="badge" aria-label={t('mail.folder.unreadCount', { count: c.unreadCount })}>{c.unreadCount}</span>{/if}
          </div>
        </div>
      </div>
    {:else}
      {#if !app.conversationsLoading}
        <div class="empty">
          <MessagesSquare size={36} strokeWidth={1.4} aria-hidden="true" />
          <p>{t('chat.empty')}</p>
        </div>
      {/if}
    {/each}
  </div>
</section>

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
    justify-content: space-between;
    gap: 8px;
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
  .items {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 6px;
    outline: none;
  }
  .items:focus-visible .item.selected {
    box-shadow: inset 0 0 0 2px var(--tm-focus);
  }
  .item {
    display: flex;
    gap: 11px;
    align-items: center;
    padding: var(--tm-pad-y) 10px;
    border-radius: 12px;
    cursor: default;
    transition: background var(--tm-dur);
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
    font-weight: 600;
    font-size: 14px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .chs {
    display: inline-flex;
    gap: 3px;
    flex: 1;
  }
  .time {
    font-size: 11.5px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .item.unread .time {
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .snippet {
    flex: 1;
    min-width: 0;
    font-size: 13px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .item.unread .snippet {
    color: var(--tm-fg);
  }
  .who {
    color: var(--tm-fg);
    font-weight: 500;
  }
  .item :global(.pin) {
    color: var(--tm-faint);
    flex: none;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 48px 16px;
    color: var(--tm-faint);
  }
</style>
