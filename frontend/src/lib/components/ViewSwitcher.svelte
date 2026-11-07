<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Klassisch / Hybrid / Chat / Terminal. Always reachable: a segmented control with
  labels on wide screens, icon-only below 1280 px; variant "menu" (chat
  list header, phones) is one button with the current view's icon that
  opens a menu. Ctrl+Shift+V cycles the views everywhere (keyboard.ts).
-->
<script lang="ts">
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import MessagesSquare from '@lucide/svelte/icons/messages-square';
  import MessageSquareText from '@lucide/svelte/icons/message-square-text';
  import SquareTerminal from '@lucide/svelte/icons/square-terminal';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import Check from '@lucide/svelte/icons/check';
  import { app, prefs } from '../state';
  import type { ViewMode } from '../state/app.svelte';
  import { t } from '../i18n/index.svelte';
  import { phone } from '../phone.svelte';

  let { variant = 'auto' }: { variant?: 'auto' | 'menu' } = $props();

  // The terminal view is a desktop keyboard tool; phones do not offer it.
  const ALL_MODES: { id: ViewMode; icon: typeof Rows3 }[] = [
    { id: 'classic', icon: Rows3 },
    { id: 'hybrid', icon: MessageSquareText },
    { id: 'chat', icon: MessagesSquare },
    { id: 'terminal', icon: SquareTerminal },
  ];
  const MODES = $derived(phone.current ? ALL_MODES.filter((m) => m.id !== 'terminal') : ALL_MODES);
  const current = $derived(MODES.find((m) => m.id === prefs.viewMode) ?? MODES[0]);
  let open = $state(false);
  let btn = $state<HTMLButtonElement>();

  function pick(id: ViewMode) {
    open = false;
    void app.setViewMode(id);
    btn?.focus();
  }
  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      e.stopPropagation();
      open = false;
      btn?.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="vs {variant}" data-view-switcher {onkeydown}>
  <div class="seg full" role="radiogroup" aria-label={t('mail.view.label')} title="Ctrl+Shift+V">
    {#each MODES as m (m.id)}
      {@const Icon = m.icon}
      <button role="radio" aria-checked={prefs.viewMode === m.id} onclick={() => app.setViewMode(m.id)} title={t(`mail.view.${m.id}Hint`) + ' (Ctrl+Shift+V)'} aria-label={t(`mail.view.${m.id}`)}>
        <Icon size={15} aria-hidden="true" /><span class="txt">{t(`mail.view.${m.id}`)}</span>
      </button>
    {/each}
  </div>

  <button
    bind:this={btn}
    class="icon-btn menu-btn"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label={t('mail.view.label') + ': ' + t(`mail.view.${current.id}`)}
    title={t('mail.view.label') + ' (Ctrl+Shift+V)'}
    onclick={() => (open = !open)}
  >
    <current.icon size={17} aria-hidden="true" /><ChevronDown size={12} aria-hidden="true" />
  </button>
  {#if open}
    <div class="backdrop" role="presentation" onclick={() => (open = false)}></div>
    <div class="dd" role="menu" aria-label={t('mail.view.label')}>
      {#each MODES as m (m.id)}
        {@const Icon = m.icon}
        <button role="menuitemradio" aria-checked={prefs.viewMode === m.id} onclick={() => pick(m.id)}>
          <Icon size={16} aria-hidden="true" />
          <span class="mt"><span>{t(`mail.view.${m.id}`)}</span><small>{t(`mail.view.${m.id}Hint`)}</small></span>
          {#if prefs.viewMode === m.id}<Check size={15} aria-hidden="true" />{/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .vs {
    position: relative;
    display: inline-flex;
    flex: none;
  }
  .menu-btn {
    display: none;
    gap: 2px;
    width: auto;
    padding: 0 6px;
  }
  .vs.menu .full {
    display: none;
  }
  .vs.menu .menu-btn {
    display: inline-flex;
  }
  .full button {
    padding: 0 9px;
  }
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 60;
  }
  .dd {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    z-index: 61;
    min-width: 230px;
    padding: 4px;
    border-radius: 10px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .dd button {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 8px 10px;
    border: none;
    border-radius: 7px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
  }
  .dd button:hover,
  .dd button:focus-visible {
    background: var(--tm-surface-2);
  }
  .mt {
    flex: 1;
    display: flex;
    flex-direction: column;
  }
  .mt small {
    color: var(--tm-muted);
    font-size: 11.5px;
  }
  @media (max-width: 1279px) {
    .txt {
      display: none;
    }
    .full button {
      padding: 0 8px;
    }
  }
  /* Phones: one button with a menu. */
  @media (max-width: 479px) {
    .full {
      display: none;
    }
    .menu-btn {
      display: inline-flex;
    }
  }
</style>
