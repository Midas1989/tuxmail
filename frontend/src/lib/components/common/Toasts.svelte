<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { fly } from 'svelte/transition';
  import X from '@lucide/svelte/icons/x';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';

  // While the user types (keyboard open), plain notes do not cover the field; undo/error toasts still show.
  let typing = $state(false);
  $effect(() => {
    const update = () => {
      const el = document.activeElement as HTMLElement | null;
      typing = !!el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
    };
    const later = () => setTimeout(update, 0);
    document.addEventListener('focusin', update);
    document.addEventListener('focusout', later);
    return () => {
      document.removeEventListener('focusin', update);
      document.removeEventListener('focusout', later);
    };
  });
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each app.toasts as toast (toast.id)}
    <div class="toast" class:error={toast.kind === 'error'} class:quiet={typing && !toast.action && toast.kind !== 'error'} transition:fly={{ y: 12, duration: 160 }}>
      <span>{toast.text}</span>
      {#if toast.action}
        <button
          class="act"
          onclick={() => {
            toast.action?.();
            app.dismissToast(toast.id);
          }}>{toast.actionLabel}</button
        >
      {/if}
      <button class="close" aria-label={t('common.action.close')} onclick={() => app.dismissToast(toast.id)}><X size={14} /></button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    left: 50%;
    bottom: calc(20px + var(--tm-safe-bottom));
    transform: translateX(-50%);
    z-index: 60;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: center;
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 8px 8px 14px;
    border-radius: 10px;
    background: #1f2430;
    color: #f2f4f7;
    font-size: 13px;
    box-shadow: var(--tm-shadow-lg);
  }
  .toast.error {
    background: #7f1d1d;
  }
  .act {
    border: none;
    background: transparent;
    color: #93c5fd;
    font-weight: 600;
    font-size: 13px;
    padding: 4px 6px;
    border-radius: 6px;
  }
  .act:hover {
    background: rgb(255 255 255 / 0.08);
  }
  .close {
    display: inline-flex;
    border: none;
    background: transparent;
    color: #aab2c0;
    padding: 4px;
    border-radius: 6px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .toasts {
      bottom: 72px;
    }
  }

  /* Phones: toasts sit at the top so they never cover the message field, the bottom bar or the keyboard.
     They stack as one column of slim rows of the same width, never on top of each other. */
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .toasts {
      bottom: auto;
      top: calc(12px + var(--tm-safe-top));
      width: calc(100vw - 24px);
      align-items: stretch;
      gap: 6px;
    }
    .toast {
      padding: 4px 4px 4px 12px;
      min-height: 44px;
    }
    .toast span {
      flex: 1;
      min-width: 0;
      /* One slim line; errors may take two. */
      overflow: hidden;
      display: -webkit-box;
      -webkit-box-orient: vertical;
      -webkit-line-clamp: 1;
      line-clamp: 1;
    }
    .toast.error span {
      -webkit-line-clamp: 2;
      line-clamp: 2;
    }
    /* Still read out by screen readers, just not drawn over the field. */
    .toast.quiet {
      position: absolute;
      width: 1px;
      height: 1px;
      min-height: 0;
      padding: 0;
      overflow: hidden;
      clip-path: inset(50%);
      white-space: nowrap;
    }
    /* An open sheet, dialog or the composer owns the top (title, sender): toasts go to the bottom,
       above its action row, instead of covering its head. */
    :global(body:has([aria-modal='true'], .composer)) .toasts {
      top: auto;
      bottom: calc(84px + var(--tm-safe-bottom));
    }
  }
</style>
