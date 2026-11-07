<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Tags: rename (path = hierarchy), colour, icon, merge, delete. -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import Merge from '@lucide/svelte/icons/merge';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import TagIcon, { TAG_COLORS, TAG_ICONS } from '../common/TagIcon.svelte';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import type { Tag } from '../../types';

  let newPath = $state('');
  let newColor = $state(TAG_COLORS[2]);
  let selected = $state<number[]>([]);
  let mergeTarget = $state<number | ''>('');
  let colorFor = $state<number | null>(null);
  let confirmDelete = $state<Tag | null>(null);
  let error = $state('');

  const depth = (p: string) => p.split('/').length - 1;

  async function save(tag: Tag, patch: Partial<Tag>) {
    error = '';
    try {
      await app.api.saveTag({ id: tag.id, path: patch.path ?? tag.path, color: patch.color ?? tag.color, icon: patch.icon === '' ? undefined : (patch.icon ?? tag.icon) });
      await app.loadTags();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!newPath.trim()) return;
    error = '';
    try {
      await app.createTag(newPath.trim(), newColor);
      newPath = '';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function merge() {
    if (mergeTarget === '') return;
    const target = mergeTarget;
    await app.api.mergeTags(selected.filter((x) => x !== target), target);
    selected = [];
    mergeTarget = '';
    await app.loadTags();
    app.toast(t('settings.tags.merged'));
  }

  async function remove(tag: Tag) {
    confirmDelete = null;
    await app.api.deleteTag(tag.id);
    selected = selected.filter((x) => x !== tag.id);
    await app.loadTags();
  }
</script>

<section class="card" id="set-tags" aria-labelledby="set-tags-h">
  <h2 id="set-tags-h">{t('settings.tags.title')}</h2>
  <p class="hint">{t('settings.tags.intro')}</p>
  <ul class="tags">
    {#each app.tags as x (x.tag.id)}
      {@const tg = x.tag}
      <li style:--d={depth(tg.path)}>
        <input type="checkbox" checked={selected.includes(tg.id)} aria-label={t('settings.tags.select', { name: tg.path })} onchange={(e) => (selected = (e.currentTarget as HTMLInputElement).checked ? [...selected, tg.id] : selected.filter((y) => y !== tg.id))} />
        <button class="swatch" style:background={tg.color} aria-label={t('organize.tags.color')} aria-expanded={colorFor === tg.id} onclick={() => (colorFor = colorFor === tg.id ? null : tg.id)}></button>
        <select class="icon-sel" value={tg.icon ?? ''} aria-label={t('settings.tags.icon')} onchange={(e) => save(tg, { icon: (e.currentTarget as HTMLSelectElement).value })}>
          <option value="">–</option>
          {#each Object.keys(TAG_ICONS) as ic (ic)}<option value={ic}>{ic}</option>{/each}
        </select>
        <span class="ic"><TagIcon icon={tg.icon} color={tg.color} /></span>
        <input
          class="path"
          value={tg.path}
          aria-label={t('settings.tags.path')}
          onchange={(e) => save(tg, { path: (e.currentTarget as HTMLInputElement).value })}
          onkeydown={(e) => e.key === 'Enter' && (e.currentTarget as HTMLInputElement).blur()}
        />
        <span class="cnt" title={t('settings.tags.counts', { direct: x.direct, total: x.total })}>{x.total}</span>
        <button class="icon-btn" aria-label={t('common.action.delete')} onclick={() => (confirmDelete = tg)}><Trash size={14} /></button>
        {#if colorFor === tg.id}
          <div class="colors" role="radiogroup" aria-label={t('organize.tags.color')}>
            {#each TAG_COLORS as c (c)}
              <button
                class="sw"
                role="radio"
                aria-checked={tg.color === c}
                aria-label={c}
                style:background={c}
                onclick={() => {
                  colorFor = null;
                  void save(tg, { color: c });
                }}
              ></button>
            {/each}
          </div>
        {/if}
      </li>
    {/each}
  </ul>
  {#if selected.length > 1}
    <div class="merge">
      <Merge size={15} aria-hidden="true" />
      <span>{t('settings.tags.mergeInto', { count: selected.length })}</span>
      <select class="input" bind:value={mergeTarget} aria-label={t('settings.tags.mergeTarget')}>
        <option value="">–</option>
        {#each selected as id (id)}<option value={id}>{app.tag(id)?.path}</option>{/each}
      </select>
      <button class="btn" disabled={mergeTarget === ''} onclick={merge}>{t('settings.tags.merge')}</button>
    </div>
  {/if}
  <form class="new" onsubmit={create}>
    <input class="input" bind:value={newPath} placeholder={t('settings.tags.newPlaceholder')} aria-label={t('settings.tags.new')} />
    <span class="colors inline" role="radiogroup" aria-label={t('organize.tags.color')}>
      {#each TAG_COLORS.slice(0, 6) as c (c)}<button type="button" class="sw" role="radio" aria-checked={newColor === c} aria-label={c} style:background={c} onclick={() => (newColor = c)}></button>{/each}
    </span>
    <button class="btn btn-primary" type="submit" disabled={!newPath.trim()}><Plus size={14} /> {t('settings.tags.add')}</button>
  </form>
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</section>

{#if confirmDelete}
  {@const tg = confirmDelete}
  <ConfirmDialog title={t('settings.tags.deleteTitle', { name: tg.path })} message={t('settings.tags.deleteText')} confirmLabel={t('common.action.delete')} danger onconfirm={() => remove(tg)} oncancel={() => (confirmDelete = null)} />
{/if}

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
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
  .tags {
    list-style: none;
    margin: 0 0 10px;
    padding: 0;
    max-height: 340px;
    overflow-y: auto;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    padding: 3px 0 3px calc(var(--d) * 18px);
    border-bottom: 1px solid var(--tm-line);
  }
  li input[type='checkbox'] {
    accent-color: var(--tm-accent);
  }
  .swatch {
    width: 18px;
    height: 18px;
    border-radius: 6px;
    border: none;
    cursor: pointer;
  }
  .icon-sel {
    order: 9;
    width: 92px;
    padding: 2px 4px;
    border: 1px solid var(--tm-line);
    border-radius: 6px;
    background: var(--tm-surface);
    color: var(--tm-muted);
    font: inherit;
    font-size: 12px;
  }
  .cnt,
  li .icon-btn {
    order: 10;
  }
  .colors {
    order: 11;
  }
  .ic {
    display: inline-flex;
  }
  .path {
    flex: 1;
    min-width: 120px;
    border: 1px solid transparent;
    border-radius: 6px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13.5px;
    padding: 3px 6px;
  }
  .path:hover,
  .path:focus {
    border-color: var(--tm-line-strong);
    outline: none;
  }
  .cnt {
    font-size: 11.5px;
    color: var(--tm-faint);
    min-width: 22px;
    text-align: right;
  }
  .colors {
    display: flex;
    gap: 5px;
    width: 100%;
    padding: 4px 0 4px 48px;
  }
  .colors.inline {
    order: 0;
    width: auto;
    padding: 0;
  }
  .sw {
    width: 18px;
    height: 18px;
    border-radius: 99px;
    border: 2px solid transparent;
    cursor: pointer;
  }
  .sw[aria-checked='true'] {
    border-color: var(--tm-fg);
  }
  .merge,
  .new {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 8px;
    font-size: 13px;
  }
  .new .input {
    flex: 1;
  }
  .err {
    margin: 6px 0 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }

  @media (max-width: 639px) {
    .new {
      flex-wrap: wrap;
    }
    .new :global(.input) {
      flex: 1 1 100%;
    }
  }
</style>
