<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> remote images: senders and domains whose images load automatically (via the image proxy). -->
<script lang="ts">
  import Trash from '@lucide/svelte/icons/trash';
  import Plus from '@lucide/svelte/icons/plus';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { RemoteAllowEntry } from '../../types';

  let entries = $state<RemoteAllowEntry[]>([]);
  let value = $state('');
  let error = $state<string | null>(null);

  async function load() {
    try {
      entries = await app.api.listRemoteAllow();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
  void load();

  async function add() {
    const v = value.trim();
    if (!v) return;
    error = null;
    try {
      await app.api.allowRemoteContent(v.includes('@') && !v.startsWith('@') ? 'sender' : 'domain', v);
      value = '';
      await load();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function remove(e: RemoteAllowEntry) {
    await app.api.removeRemoteAllow(e.kind, e.value);
    await load();
  }
</script>

<section class="card" id="set-remote" aria-labelledby="set-remote-h">
  <h2 id="set-remote-h">{t('settings.remote.title')}</h2>
  <p class="hint">{t('settings.remote.intro')}</p>
  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      void add();
    }}
  >
    <input class="input" bind:value placeholder={t('settings.remote.placeholder')} aria-label={t('settings.remote.placeholder')} />
    <button class="btn" type="submit"><Plus size={14} /> {t('settings.remote.add')}</button>
  </form>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if entries.length}
    <ul>
      {#each entries as e (e.kind + e.value)}
        <li>
          <span class="kind">{t(`settings.remote.kind.${e.kind}`)}</span>
          <span class="val">{e.value}</span>
          <button class="icon-btn" aria-label={t('settings.remote.remove', { value: e.value })} onclick={() => remove(e)}><Trash size={14} /></button>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="hint">{t('settings.remote.empty')}</p>
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  h2 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 650;
  }
  .hint {
    margin: 4px 0 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .row {
    display: flex;
    gap: 6px;
    margin-bottom: 8px;
  }
  .row .input {
    flex: 1;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    border-top: 1px solid var(--tm-line);
  }
  .kind {
    font-size: 11px;
    padding: 1px 8px;
    border-radius: 99px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .val {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .error {
    color: var(--tm-danger, #b42318);
    font-size: 13px;
  }
</style>
