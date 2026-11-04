<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Hybrid view, middle pane: conversations (threads) with at least one
  message in the selected folder / tag / search folder. The right pane shows
  the whole conversation as bubbles with the folder's messages highlighted.
-->
<script lang="ts">
  import { comfort } from '../../state';
  import CategoryTabs from '../comfort/CategoryTabs.svelte';
  import TeamBadge from '../team/TeamBadge.svelte';
  import Inbox from '@lucide/svelte/icons/inbox';
  import Paperclip from '@lucide/svelte/icons/paperclip';
  import { app } from '../../state';
  import { formatListDate, t } from '../../i18n/index.svelte';
  import ChatAvatar from '../chat/ChatAvatar.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import type { Conversation } from '../../types';
  import ListSyncState from './ListSyncState.svelte';

  interface Props {
    title: string;
    subtitle: string;
  }
  let { title, subtitle }: Props = $props();

  // Counterparts of the conversation; "Du" only when there is no one else
  // (own notes / mails to oneself). Persons created by a running sync may
  // not be loaded yet: app.personName reloads them.
  function names(c: Conversation): string {
    if (!c.participantIds.length) return t('chat.you');
    const ns = c.participantIds.map((id) => app.personName(id)?.split(' ')[0]).filter(Boolean);
    return ns.length ? ns.join(', ') : '…';
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      app.moveSelection(e.key === 'ArrowDown' ? 1 : -1);
    }
  }
</script>

<section class="hl" aria-label={t('mail.hybrid.listLabel')}>
  <header class="head">
    <h1>{title}</h1>
    <span class="sub">{subtitle} · {t('mail.hybrid.count', { count: app.conversations.length })}</span>
  </header>
  <CategoryTabs />
  <div class="scroll" role="listbox" tabindex="0" aria-label={title} {onkeydown} data-list="conversations" aria-activedescendant={app.selectedConversationId != null ? `hc-${app.selectedConversationId}` : undefined}>
    {#if !app.conversations.length && !app.search && app.listSync}
      <ListSyncState status={app.listSync} />
    {:else if !app.conversations.length && !app.conversationsLoading}
      <div class="empty">
        <Inbox size={34} strokeWidth={1.4} aria-hidden="true" />
        <p>{t('mail.list.empty')}</p>
      </div>
    {/if}
    {#each comfort.filterConversations(app.conversations) as c (c.id)}
      {@const sel = c.id === app.selectedConversationId}
      <div id="hc-{c.id}" class="row" class:sel class:unread={c.unreadCount > 0} role="option" aria-selected={sel} tabindex="-1" onclick={() => app.selectConversation(c.id)} onkeydown={() => {}}>
        <ChatAvatar name={app.convAvatar(c).name} seed={app.convAvatar(c).seed} size={38} group={app.convAvatar(c).group} url={c.avatarUrl} />
        <div class="body">
          <div class="l1">
            <span class="subject">{c.title}</span>
            <time datetime={c.lastActivity}>{formatListDate(c.lastActivity)}</time>
          </div>
          <div class="l2">
            <span class="who">{names(c)}</span>
            {#each c.channels.filter((ch) => ch !== 'email') as ch (ch)}<ChannelIcon channel={ch} size={12} />{/each}
            <TeamBadge conversation={c} />
          </div>
          <div class="l3">
            <span class="snip">{c.lastIsOwn ? t('chat.you') + ': ' : c.lastSenderName ? c.lastSenderName.split(' ')[0] + ': ' : ''}{c.snippet}</span>
            {#if c.lastMedia === 'file'}<Paperclip size={12} aria-hidden="true" />{/if}
            {#if c.unreadCount > 0}<span class="badge">{c.unreadCount}</span>{/if}
          </div>
        </div>
      </div>
    {/each}
  </div>
</section>

<style>
  .hl {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--tm-surface);
  }
  .head {
    padding: 12px var(--tm-pad-x) 10px;
    border-bottom: 1px solid var(--tm-line);
  }
  h1 {
    margin: 0;
    font-size: 17px;
    font-weight: 650;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .scroll {
    position: relative;
    flex: 1;
    overflow-y: auto;
    outline: none;
  }
  /* Off-screen rows are not laid out or painted (cheap virtualization of
     long conversation lists; keyed rows are only re-rendered on change). */
  .row {
    content-visibility: auto;
    contain-intrinsic-size: auto 76px;
    display: flex;
    gap: 10px;
    padding: 9px var(--tm-pad-x);
    border-bottom: 1px solid var(--tm-line);
    cursor: pointer;
  }
  .row:hover {
    background: var(--tm-surface-2);
  }
  .row.sel {
    background: var(--tm-accent-soft);
  }
  .body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .l1,
  .l2,
  .l3 {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .subject {
    flex: 1;
    min-width: 0;
    font-size: 13.5px;
    font-weight: 550;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .unread .subject {
    font-weight: 700;
  }
  time {
    font-size: 11.5px;
    color: var(--tm-muted);
    flex: none;
  }
  .who {
    font-size: 12.5px;
    color: var(--tm-fg);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .snip {
    flex: 1;
    min-width: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .badge {
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 99px;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    font-size: 11px;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 60px 20px;
    color: var(--tm-faint);
  }
</style>
