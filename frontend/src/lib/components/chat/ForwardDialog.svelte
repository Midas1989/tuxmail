<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Search from '@lucide/svelte/icons/search';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { ConversationItem } from '../../types';
  import Modal from '../common/Modal.svelte';
  import ChatAvatar from './ChatAvatar.svelte';

  interface Props {
    item: ConversationItem;
  }
  let { item }: Props = $props();
  let q = $state('');
  const targets = $derived(app.conversations.filter((c) => c.title.toLowerCase().includes(q.toLowerCase())).slice(0, 50));
</script>

<Modal title={t('chat.forwardTitle')} onclose={() => (app.forwarding = null)} width="440px">
  <p class="preview">“{item.body || item.card?.title || item.attachments[0]?.filename}”</p>
  <div class="search">
    <Search size={15} aria-hidden="true" />
    <input bind:value={q} placeholder={t('chat.searchPlaceholder')} aria-label={t('chat.searchPlaceholder')} data-autofocus />
  </div>
  <ul class="list">
    {#each targets as c (c.id)}
      <li>
        <button onclick={() => app.forwardTo(item, c.id)}>
          <ChatAvatar name={c.title} seed={c.title} size={36} group={c.isGroup && c.kind === 'chat'} channel={c.lastChannel} />
          <span>{c.title}</span>
        </button>
      </li>
    {/each}
  </ul>
</Modal>

<style>
  .preview {
    margin: 0 0 10px;
    font-size: 13px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 10px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .search input {
    flex: 1;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
  }
  .list {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    max-height: 50vh;
    overflow-y: auto;
  }
  .list button {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 6px 8px;
    border: none;
    border-radius: 10px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
  }
  .list button:hover,
  .list button:focus-visible {
    background: var(--tm-surface-2);
    outline: none;
  }
</style>
