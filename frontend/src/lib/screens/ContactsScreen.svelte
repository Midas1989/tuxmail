<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Mail from '@lucide/svelte/icons/mail';
  import Phone from '@lucide/svelte/icons/phone';
  import UserPlus from '@lucide/svelte/icons/user-plus';
  import { app } from '../state';
  import { t } from '../i18n/index.svelte';
  import type { HandleKind, Person } from '../types';
  import Avatar from '../components/common/Avatar.svelte';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';
  import ScreenShell from './ScreenShell.svelte';

  let filter = $state('');
  const people = $derived(
    app.persons
      .filter((p) => !p.isMe)
      .filter((p) => (p.displayName + ' ' + p.handles.map((h) => h.value).join(' ')).toLowerCase().includes(filter.toLowerCase()))
      .sort((a, b) => a.displayName.localeCompare(b.displayName)),
  );

  async function openConversation(p: Person) {
    app.navigate('mail');
    await app.setViewMode('chat');
    if (app.sel.kind !== 'unified') await app.select({ kind: 'unified', role: 'inbox' });
    await app.setGrouping('person');
    await app.loadConversations();
    await app.selectConversation(1000 + p.id);
  }

  function channelOf(kind: HandleKind): 'whatsapp' | 'signal' | 'telegram' | null {
    return kind === 'whatsapp' || kind === 'signal' || kind === 'telegram' ? kind : null;
  }
</script>

<ScreenShell title={t('nav.contacts')} subtitle={t('contacts.placeholder')}>
  {#snippet actions()}
    <input class="input filter" placeholder={t('contacts.filter')} bind:value={filter} aria-label={t('contacts.filter')} />
    <button class="btn" disabled title={t('common.soon')}><UserPlus size={15} /> {t('contacts.add')}</button>
  {/snippet}
  <ul class="grid">
    {#each people as p (p.id)}
      <li class="card">
        <Avatar name={p.displayName} seed={p.handles[0]?.value} size={44} />
        <div class="info">
          <button class="name" onclick={() => openConversation(p)}>{p.displayName}</button>
          <ul class="handles">
            {#each p.handles as h (h.kind + h.value)}
              {@const ch = channelOf(h.kind)}
              <li>
                {#if ch}<ChannelIcon channel={ch} size={13} />{:else if h.kind === 'email'}<Mail size={13} />{:else}<Phone size={13} />{/if}
                <span>{h.value}</span>
              </li>
            {/each}
          </ul>
        </div>
      </li>
    {/each}
  </ul>
</ScreenShell>

<style>
  .filter {
    width: 220px;
  }
  .grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
  }
  .card {
    display: flex;
    gap: 12px;
    padding: 14px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .info {
    min-width: 0;
  }
  .name {
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    font-weight: 600;
    color: var(--tm-fg);
    text-align: left;
  }
  .name:hover {
    color: var(--tm-accent-text);
    text-decoration: underline;
  }
  .handles {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .handles li {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .handles span {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  @media (max-width: 639px) {
    .filter {
      width: 140px;
    }
  }
</style>
