<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Quick replies ("/" snippets). -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Snippet } from '../../types';

  async function save(sn: Snippet) {
    await app.api.saveSnippet(sn);
    app.snippets = await app.api.listSnippets();
  }
  async function add() {
    await save({ id: '', trigger: 'neu', title: t('settings.snippets.newTitle'), text: '' });
  }
  async function remove(id: string) {
    await app.api.deleteSnippet(id);
    app.snippets = await app.api.listSnippets();
  }
</script>

<section class="card" aria-labelledby="set-snippets">
  <div class="head">
    <h2 id="set-snippets">{t('chat.snippets.title')}</h2>
    <button class="btn" onclick={add}><Plus size={14} /> {t('settings.snippets.add')}</button>
  </div>
  <p class="hint">{t('settings.snippets.hint')}</p>
  <ul>
    {#each app.snippets as sn (sn.id)}
      <li>
        <div class="row">
          <span class="slash">/</span>
          <input class="input trig" value={sn.trigger} aria-label={t('settings.snippets.trigger')} onchange={(e) => save({ ...sn, trigger: (e.currentTarget as HTMLInputElement).value })} />
          <input class="input" value={sn.title} aria-label={t('settings.snippets.titleLabel')} onchange={(e) => save({ ...sn, title: (e.currentTarget as HTMLInputElement).value })} />
          <button class="icon-btn" aria-label={t('settings.snippets.delete', { name: sn.trigger })} onclick={() => remove(sn.id)}><Trash size={15} /></button>
        </div>
        <textarea class="input text" rows="2" value={sn.text} aria-label={t('settings.snippets.text')} onchange={(e) => save({ ...sn, text: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
      </li>
    {/each}
  </ul>
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .hint {
    margin: 4px 0 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .slash {
    font-family: var(--tm-mono);
    font-weight: 700;
    color: var(--tm-accent-text);
  }
  .trig {
    width: 120px;
    font-family: var(--tm-mono);
  }
  .text {
    margin-top: 4px;
    height: auto;
    padding: 6px 10px;
    resize: vertical;
  }
</style>
