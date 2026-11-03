<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Floating context menu (hover button, right click or long press).
  role=menu with arrow-key navigation; Esc / outside click closes and
  focus returns to the opener.
-->
<script lang="ts" module>
  import type { Component } from 'svelte';

  export interface MenuItem {
    id: string;
    label: string;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    icon?: Component<any>;
    danger?: boolean;
    disabled?: boolean;
    run: () => void | Promise<void>;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { fade } from 'svelte/transition';

  interface Props {
    /** Anchor point in viewport coordinates. */
    x: number;
    y: number;
    items: MenuItem[];
    label: string;
    onclose: () => void;
    /** Optional content above the items (e.g. quick reactions). */
    header?: Snippet;
    width?: number;
  }
  let { x, y, items, label, onclose, header, width = 230 }: Props = $props();

  let menu = $state<HTMLDivElement>();
  const opener = typeof document !== 'undefined' ? (document.activeElement as HTMLElement | null) : null;
  let pos = $state({ left: 0, top: 0 });

  $effect(() => {
    if (!menu) return;
    const r = menu.getBoundingClientRect();
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    pos = {
      left: Math.max(8, Math.min(x, vw - r.width - 8)),
      top: y + r.height > vh - 8 ? Math.max(8, y - r.height) : y,
    };
    (menu.querySelector<HTMLElement>('[data-first], [role="menuitem"]:not([disabled])') ?? menu).focus();
    return () => opener?.focus?.();
  });

  function close() {
    onclose();
  }

  function onkeydown(e: KeyboardEvent) {
    if (!menu) return;
    const list = [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled]), [data-menu-extra]')];
    const idx = list.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      list[(idx + 1) % list.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      list[(idx - 1 + list.length) % list.length]?.focus();
    } else if (e.key === 'Tab') {
      e.preventDefault();
      close();
    }
  }

  async function run(item: MenuItem) {
    close();
    await item.run();
  }
</script>

<div class="backdrop" role="presentation" onmousedown={close} oncontextmenu={(e) => (e.preventDefault(), close())}></div>
<div
  class="menu"
  bind:this={menu}
  role="menu"
  tabindex="-1"
  aria-label={label}
  style:left="{pos.left}px"
  style:top="{pos.top}px"
  style:width="{width}px"
  {onkeydown}
  transition:fade={{ duration: 90 }}
>
  {#if header}<div class="header">{@render header()}</div>{/if}
  {#each items as item (item.id)}
    {@const Icon = item.icon}
    <button role="menuitem" class="item" class:danger={item.danger} disabled={item.disabled} onclick={() => run(item)}>
      {#if Icon}<Icon size={16} />{/if}
      <span>{item.label}</span>
    </button>
  {/each}
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 70;
  }
  .menu {
    position: fixed;
    z-index: 71;
    padding: 6px;
    border-radius: 12px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
    outline: none;
  }
  .header {
    padding: 2px 2px 6px;
    margin-bottom: 4px;
    border-bottom: 1px solid var(--tm-line);
  }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    height: 34px;
    padding: 0 10px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13.5px;
    text-align: left;
  }
  .item :global(svg) {
    color: var(--tm-muted);
    flex: none;
  }
  .item:hover,
  .item:focus {
    background: var(--tm-surface-2);
    outline: none;
  }
  .item.danger,
  .item.danger :global(svg) {
    color: var(--tm-danger);
  }
  .item:disabled {
    opacity: 0.45;
  }
</style>
