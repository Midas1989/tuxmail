<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Chats & Messenger: chat folders and focus mode. The editors are
  the dialogs of the chat view (ChatDialogs, mounted by the settings screen).
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash';
  import Settings2 from '@lucide/svelte/icons/settings-2';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    part: 'folders' | 'focus';
  }
  let { part }: Props = $props();

  const folders = $derived(app.chatFolders.filter((f) => f.searchFolderId == null));
</script>

{#if part === 'folders'}
  <section class="card" id="set-chatfolders" aria-labelledby="set-chatfolders-h">
    <div class="card-head">
      <h2 id="set-chatfolders-h">{t('chat.folders.label')}</h2>
      <button class="btn" onclick={() => (app.chatDialog = { kind: 'folder', folder: null })}><Plus size={14} /> {t('settings.pages.chats.newFolder')}</button>
    </div>
    <p class="intro">{t('settings.pages.chats.foldersIntro')}</p>
    {#if folders.length}
      <ul class="rows">
        {#each folders as f (f.id)}
          <li>
            <span class="nm">{#if f.emoji}<span aria-hidden="true">{f.emoji}</span>{/if} {f.name}</span>
            <button class="icon-btn" aria-label={t('settings.pages.chats.editFolder', { name: f.name })} title={t('settings.pages.chats.editFolder', { name: f.name })} onclick={() => (app.chatDialog = { kind: 'folder', folder: f })}><Pencil size={15} /></button>
            <button class="icon-btn" aria-label={t('chat.folders.delete')} title={t('chat.folders.delete')} onclick={() => app.deleteChatFolder(f.id)}><Trash size={15} /></button>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="empty">{t('settings.pages.chats.foldersEmpty')}</p>
    {/if}
  </section>
{:else}
  <section class="card" id="set-focus" aria-labelledby="set-focus-h">
    <h2 id="set-focus-h">{t('chat.focus.title')}</h2>
    <p class="intro">{t('chat.focus.intro')}</p>
    <div class="actions">
      <label class="check">
        <input type="checkbox" checked={app.focus.enabled} onchange={() => app.toggleFocus()} />
        {t('chat.focus.enabled')}
      </label>
      <button class="btn" onclick={() => (app.chatDialog = { kind: 'focus' })}><Settings2 size={14} /> {t('settings.pages.chats.focusConfigure')}</button>
    </div>
  </section>
{/if}

<style>
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .card-head h2 {
    margin: 0;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .rows li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 0;
    border-top: 1px solid var(--tm-line);
  }
  .nm {
    flex: 1;
    min-width: 0;
    font-weight: 550;
  }
  .empty {
    margin: 0;
    font-size: 13px;
    color: var(--tm-faint);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .check {
    display: inline-flex;
    gap: 8px;
    align-items: center;
    font-size: 13.5px;
  }
</style>
