<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import FolderInput from '@lucide/svelte/icons/folder-input';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Folder } from '../../types';

  interface Props {
    messageId: number;
    accountId: number;
    currentFolderIds: number[];
  }
  let { messageId, accountId, currentFolderIds }: Props = $props();

  let open = $state(false);
  let filter = $state('');
  let menu = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();

  function depth(f: Folder, all: Folder[]): number {
    let d = 0;
    let p = f.parentId;
    while (p) {
      d++;
      p = all.find((x) => x.id === p)?.parentId;
    }
    return d;
  }

  const folders = $derived((app.folders[accountId] ?? []).filter((f) => !currentFolderIds.includes(f.id) && f.name.toLowerCase().includes(filter.toLowerCase())));

  function label(f: Folder): string {
    return f.role ? t(`mail.folder.role.${f.role}` as 'mail.folder.role.inbox') : f.name;
  }

  async function pick(f: Folder) {
    open = false;
    trigger?.focus();
    await app.moveTo(messageId, f.id);
  }

  function onkeydown(e: KeyboardEvent) {
    if (!menu) return;
    const items = [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]')];
    const idx = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.stopPropagation();
      open = false;
      trigger?.focus();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      items[Math.min(items.length - 1, idx + 1)]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (idx <= 0) menu.querySelector<HTMLInputElement>('input')?.focus();
      else items[idx - 1]?.focus();
    }
  }

  $effect(() => {
    if (open) menu?.querySelector<HTMLInputElement>('input')?.focus();
  });
</script>

<div class="wrap">
  <button bind:this={trigger} class="icon-btn" aria-haspopup="menu" aria-expanded={open} title={t('common.action.move')} aria-label={t('common.action.move')} onclick={() => (open = !open)}>
    <FolderInput size={17} />
  </button>
  {#if open}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="backdrop" role="presentation" onclick={() => (open = false)}></div>
    <div class="menu" role="menu" tabindex="-1" bind:this={menu} {onkeydown} aria-label={t('common.action.move')}>
      <input class="input" placeholder={t('mail.move.filter')} bind:value={filter} aria-label={t('mail.move.filter')} />
      <div class="items">
        {#each folders as f (f.id)}
          {@const all = app.folders[accountId] ?? []}
          <button role="menuitem" class="item" style:padding-left="{10 + depth(f, all) * 14}px" onclick={() => pick(f)}>{label(f)}</button>
        {:else}
          <div class="none">{t('mail.move.none')}</div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .wrap {
    position: relative;
  }
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 19;
  }
  .menu {
    position: absolute;
    right: 0;
    top: 36px;
    z-index: 20;
    width: 240px;
    padding: 6px;
    border-radius: 10px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .items {
    max-height: 280px;
    overflow: auto;
    margin-top: 6px;
  }
  .item {
    display: block;
    width: 100%;
    text-align: left;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    height: 30px;
    border-radius: 6px;
    font: inherit;
    font-size: 13px;
  }
  .item:hover,
  .item:focus {
    background: var(--tm-accent-soft);
    outline: none;
  }
  .none {
    padding: 8px;
    color: var(--tm-faint);
    font-size: 12.5px;
  }
</style>
