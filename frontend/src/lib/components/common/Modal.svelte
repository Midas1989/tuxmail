<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  // Open modals, top-most last: Escape closes the top-most one even when the
  // focused element was removed (focus fell back to <body>).
  const stack: symbol[] = [];
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { fade, scale } from 'svelte/transition';
  import X from '@lucide/svelte/icons/x';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    title: string;
    onclose: () => void;
    width?: string;
    children: Snippet;
    footer?: Snippet;
    /** Hide the header (e.g. command palette). */
    bare?: boolean;
    align?: 'center' | 'top';
    labelledBy?: string;
    /** False hides the close button (dialogs that must be answered). */
    closable?: boolean;
  }
  let { title, onclose, width = '560px', children, footer, bare = false, align = 'center', closable = true }: Props = $props();

  let dialog = $state<HTMLDivElement>();
  const titleId = `dlg-${Math.random().toString(36).slice(2, 8)}`;
  const previouslyFocused = typeof document !== 'undefined' ? (document.activeElement as HTMLElement | null) : null;

  const me = Symbol('modal');
  $effect(() => {
    stack.push(me);
    return () => {
      const i = stack.indexOf(me);
      if (i >= 0) stack.splice(i, 1);
    };
  });

  function onwindowkeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape' || e.defaultPrevented || stack[stack.length - 1] !== me) return;
    const active = document.activeElement;
    if (active && active !== document.body && dialog?.contains(active)) return; // handled by onkeydown
    if (active && active !== document.body && active.closest('[role="dialog"]')) return;
    e.preventDefault();
    onclose();
  }

  $effect(() => {
    // Initial focus: first element marked autofocus, else first focusable.
    const el = dialog?.querySelector<HTMLElement>('[data-autofocus]') ?? focusables()[0];
    el?.focus();
    return () => previouslyFocused?.focus?.();
  });

  function focusables(): HTMLElement[] {
    if (!dialog) return [];
    return [
      ...dialog.querySelectorAll<HTMLElement>('a[href],button:not([disabled]),input:not([disabled]),select,textarea,[contenteditable="true"],[tabindex]:not([tabindex="-1"])'),
    ].filter((e) => e.offsetParent !== null || e === document.activeElement);
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      e.preventDefault();
      onclose();
      return;
    }
    if (e.key === 'Tab') {
      const f = focusables();
      if (f.length === 0) return;
      const first = f[0];
      const last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }
</script>

<svelte:window onkeydown={onwindowkeydown} />

<div class="backdrop" class:top={align === 'top'} transition:fade={{ duration: 120 }} role="presentation" onmousedown={(e) => e.target === e.currentTarget && onclose()}>
  <div
    bind:this={dialog}
    class="dialog"
    style:max-width={width}
    role="dialog"
    aria-modal="true"
    aria-labelledby={bare ? undefined : titleId}
    aria-label={bare ? title : undefined}
    tabindex="-1"
    {onkeydown}
    transition:scale={{ duration: 140, start: 0.97 }}
  >
    {#if !bare}
      <header>
        <h2 id={titleId}>{title}</h2>
        {#if closable}<button class="icon-btn" onclick={onclose} aria-label={t('common.action.close')}><X size={18} /></button>{/if}
      </header>
    {/if}
    <div class="content" class:bare>
      {@render children()}
    </div>
    {#if footer}
      <footer>{@render footer()}</footer>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: calc(16px + var(--tm-safe-top)) calc(16px + var(--tm-safe-right)) calc(16px + var(--tm-safe-bottom)) calc(16px + var(--tm-safe-left));
    background: rgb(10 14 22 / 0.45);
    backdrop-filter: blur(2px);
  }
  .backdrop.top {
    align-items: flex-start;
    padding-top: min(14vh, 120px);
  }
  .dialog {
    width: 100%;
    max-height: calc(100vh - 32px - var(--tm-safe-top) - var(--tm-safe-bottom));
    display: flex;
    flex-direction: column;
    background: var(--tm-surface);
    color: var(--tm-fg);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    box-shadow: var(--tm-shadow-lg);
    overflow: hidden;
  }
  .dialog:focus {
    outline: none;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 16px 10px 20px;
    border-bottom: 1px solid var(--tm-line);
  }
  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
  }
  .content {
    padding: 16px 20px;
    overflow: auto;
    flex: 1;
    min-height: 0;
  }
  .content.bare {
    padding: 0;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 16px;
    border-top: 1px solid var(--tm-line);
    background: var(--tm-surface-2);
  }

  /* Phones: dialogs are bottom sheets that use the full width and clear the system bars. */
  @media (max-width: 639px) {
    .backdrop:not(.top) {
      align-items: flex-end;
      padding: var(--tm-safe-top) 0 0;
    }
    .backdrop.top {
      padding-top: calc(8px + var(--tm-safe-top));
    }
    .backdrop:not(.top) .dialog {
      max-width: none !important;
      max-height: calc(100dvh - var(--tm-safe-top) - 8px);
      border-radius: 16px 16px 0 0;
      border-bottom: none;
    }
    .dialog:not(:has(footer)) .content {
      padding-bottom: var(--tm-safe-bottom);
    }
    footer {
      padding-bottom: calc(12px + var(--tm-safe-bottom));
      flex-wrap: wrap;
    }
  }
</style>
