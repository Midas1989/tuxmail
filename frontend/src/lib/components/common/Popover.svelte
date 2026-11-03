<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';

  interface Props {
    label: string;
    active?: boolean;
    icon?: Snippet;
    children: Snippet;
    align?: 'left' | 'right';
    width?: string;
    chip?: boolean;
  }
  let { label, active = false, icon, children, align = 'left', width = '280px', chip = true }: Props = $props();
  let open = $state(false);
  let trigger = $state<HTMLButtonElement>();
  const id = `pop-${Math.random().toString(36).slice(2, 7)}`;

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.stopPropagation();
      open = false;
      trigger?.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="pop" {onkeydown}>
  <button bind:this={trigger} class={chip ? 'fchip' : 'btn'} class:on={active} aria-haspopup="dialog" aria-expanded={open} aria-controls={id} onclick={() => (open = !open)}>
    {#if icon}{@render icon()}{/if}
    <span class="lbl">{label}</span>
    <ChevronDown size={13} aria-hidden="true" />
  </button>
  {#if open}
    <div class="backdrop" role="presentation" onclick={() => (open = false)}></div>
    <div class="panel" class:right={align === 'right'} {id} role="dialog" aria-label={label} style:width>
      {@render children()}
    </div>
  {/if}
</div>

<style>
  .pop {
    position: relative;
  }
  .lbl {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 24;
  }
  .panel {
    position: absolute;
    top: calc(100% + 6px);
    left: 0;
    z-index: 25;
    max-height: 60vh;
    overflow: auto;
    padding: 10px;
    border-radius: 12px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .panel.right {
    left: auto;
    right: 0;
  }
  :global(.fchip) {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 28px;
    padding: 0 10px;
    border-radius: 99px;
    border: 1px solid var(--tm-line-strong);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 12.5px;
    white-space: nowrap;
    transition:
      background var(--tm-dur),
      border-color var(--tm-dur);
  }
  :global(.fchip:hover) {
    background: var(--tm-surface-2);
  }
  :global(.fchip.on),
  :global(.fchip[aria-pressed='true']) {
    background: var(--tm-accent-soft);
    border-color: color-mix(in srgb, var(--tm-accent) 45%, transparent);
    color: var(--tm-accent-text);
    font-weight: 600;
  }
</style>
