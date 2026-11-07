<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Smart category tabs above the mail list (KONZEPT 8.8). -->
<script lang="ts">
  import { app, comfort } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { CATEGORIES } from '../../api/comfort';
  import type { CategoryFilter } from '../../state/comfort.svelte';

  interface Props {
    /** "pills" for the chat list. */
    variant?: 'tabs' | 'pills';
  }
  let { variant = 'tabs' }: Props = $props();

  const enabled = $derived(comfort.settings?.categories.enabled ?? true);
  const tabs = $derived<CategoryFilter[]>(['all', ...(comfort.settings?.categories.tabs?.length ? comfort.settings.categories.tabs : CATEGORIES)]);

  $effect(() => {
    void comfort.loadSettings();
    void comfort.loadCounts();
  });
  // Load the categories of the listed mails (classic list).
  $effect(() => {
    const ids = app.messages.map((m) => m.id);
    if (ids.length) void comfort.loadCategories(ids);
  });

  function unread(c: CategoryFilter): number {
    return c === 'all' ? 0 : (comfort.counts.find((x) => x.id === c)?.unread ?? 0);
  }
</script>

{#if enabled}
  <div class={variant} role="tablist" aria-label={t('comfort.category.tabs')}>
    {#each tabs as c (c)}
      <button role="tab" class="tab" aria-selected={comfort.category === c} onclick={() => comfort.setFilter(c)}>
        {t(`comfort.category.${c}`)}
        {#if unread(c)}<span class="n">{unread(c)}</span>{/if}
      </button>
    {/each}
  </div>
{/if}

<style>
  .tabs {
    display: flex;
    gap: 2px;
    overflow-x: auto;
    padding: 0 10px;
    border-bottom: 1px solid var(--tm-line);
    scrollbar-width: none;
  }
  .tabs .tab {
    border: none;
    background: none;
    padding: 8px 10px 7px;
    font: inherit;
    font-size: 12.5px;
    color: var(--tm-muted);
    border-bottom: 2px solid transparent;
    white-space: nowrap;
    flex: none;
    cursor: pointer;
  }
  .tabs .tab[aria-selected='true'] {
    color: var(--tm-accent-text);
    border-bottom-color: var(--tm-accent);
    font-weight: 600;
  }
  .pills {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    padding: 4px 12px 8px;
    scrollbar-width: none;
  }
  .pills .tab {
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    border-radius: 999px;
    padding: 3px 10px;
    font: inherit;
    font-size: 12px;
    white-space: nowrap;
    flex: none;
    cursor: pointer;
    color: var(--tm-fg);
  }
  .pills .tab[aria-selected='true'] {
    background: var(--tm-accent);
    border-color: var(--tm-accent);
    color: #fff;
  }
  .n {
    display: inline-block;
    min-width: 16px;
    margin-left: 4px;
    padding: 0 4px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-accent) 18%, transparent);
    font-size: 11px;
    text-align: center;
  }

  /* Phone held sideways: the rows need the height more than the category tabs. */
  @media (pointer: coarse) and (max-height: 500px) {
    .tabs {
      display: none;
    }
  }
</style>
