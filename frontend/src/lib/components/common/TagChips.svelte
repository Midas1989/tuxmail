<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Coloured tag chips ("Kunde/Müller" shows the leaf, full path in the tooltip). -->
<script lang="ts">
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import TagIcon from './TagIcon.svelte';

  interface Props {
    ids: number[] | undefined;
    max?: number;
    size?: 'xs' | 'sm';
    onclick?: (tagId: number) => void;
  }
  let { ids, max = 3, size = 'sm', onclick }: Props = $props();
  const tags = $derived(app.tagsOf(ids));
  const shown = $derived(tags.slice(0, max));
</script>

{#if tags.length}
  <span class="chips {size}" title={t('organize.tags.label')}>
    {#each shown as tg (tg.id)}
      {#if onclick}
        <button class="chip" data-touch-compact style:--c={tg.color} title={tg.path} onclick={(e) => (e.stopPropagation(), onclick(tg.id))}>
          {#if tg.icon}<TagIcon icon={tg.icon} size={size === 'xs' ? 10 : 11} />{:else}<span class="dot"></span>{/if}{tg.name}
        </button>
      {:else}
        <span class="chip" style:--c={tg.color} title={tg.path}>
          {#if tg.icon}<TagIcon icon={tg.icon} size={size === 'xs' ? 10 : 11} />{:else}<span class="dot"></span>{/if}{tg.name}
        </span>
      {/if}
    {/each}
    {#if tags.length > max}<span class="more" title={tags.slice(max).map((x) => x.path).join(', ')}>+{tags.length - max}</span>{/if}
  </span>
{/if}

<style>
  .chips {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 4px;
    min-width: 0;
    vertical-align: middle;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    max-width: 140px;
    padding: 1px 7px 1px 6px;
    border: 1px solid color-mix(in srgb, var(--c) 35%, transparent);
    border-radius: 99px;
    background: color-mix(in srgb, var(--c) 13%, transparent);
    color: color-mix(in srgb, var(--c) 75%, var(--tm-fg));
    font: inherit;
    font-size: 11px;
    font-weight: 600;
    line-height: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  button.chip {
    cursor: pointer;
  }
  .xs .chip {
    font-size: 10px;
    line-height: 14px;
    padding: 0 6px 0 5px;
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 99px;
    background: var(--c);
    flex: none;
  }
  .more {
    font-size: 11px;
    color: var(--tm-muted);
    align-self: center;
  }
</style>
