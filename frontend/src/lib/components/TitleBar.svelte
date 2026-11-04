<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Custom title bar of the frameless desktop window (cmd/tuxmail desktop,
  Windows default; Linux with "Systemtitelleiste verwenden" off): app icon
  and title, the sync indicator, a drag region (--wails-draggable: drag;
  double-click maximises) and minimise / maximise / close on the right with
  Windows-style hover states (red close). Follows the light/dark theme; on
  Windows 11 the Mica backdrop shows through (html.tm-backdrop).
-->
<script lang="ts">
  import { desktopWindow } from '../native/window.svelte';
  import { t } from '../i18n/index.svelte';
  import SyncIndicator from './SyncIndicator.svelte';

  const maximised = $derived(desktopWindow.maximised);

  function ondblclick(e: MouseEvent) {
    if ((e.target as HTMLElement).closest('button')) return;
    desktopWindow.toggleMaximise();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<header class="titlebar" style="--wails-draggable: drag" {ondblclick} data-testid="titlebar">
  <img class="ic" src="./icons/icon-64.png" alt="" width="18" height="18" />
  <span class="title">TuxMail</span>
  <div class="center"><SyncIndicator /></div>
  <div class="buttons" style="--wails-draggable: no-drag">
    <button class="wb" onclick={() => desktopWindow.minimise()} title={t('window.minimise')} aria-label={t('window.minimise')}>
      <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M0 5.5h10" stroke="currentColor" stroke-width="1" /></svg>
    </button>
    <button class="wb" onclick={() => desktopWindow.toggleMaximise()} title={maximised ? t('window.restore') : t('window.maximise')} aria-label={maximised ? t('window.restore') : t('window.maximise')}>
      {#if maximised}
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M2.5 0.5h7v7M0.5 2.5h7v7h-7z" fill="none" stroke="currentColor" stroke-width="1" /></svg>
      {:else}
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><rect x="0.5" y="0.5" width="9" height="9" fill="none" stroke="currentColor" stroke-width="1" /></svg>
      {/if}
    </button>
    <button class="wb close" onclick={() => desktopWindow.close()} title={t('window.close')} aria-label={t('window.close')}>
      <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M0.5 0.5l9 9M9.5 0.5l-9 9" stroke="currentColor" stroke-width="1.1" /></svg>
    </button>
  </div>
</header>

<style>
  .titlebar {
    display: flex;
    align-items: center;
    height: 34px;
    flex: none;
    gap: 8px;
    padding-left: 12px;
    background: var(--tm-rail);
    color: var(--tm-rail-fg);
    user-select: none;
    -webkit-user-select: none;
    position: relative;
    z-index: 50;
  }
  :global(html.tm-backdrop) .titlebar {
    background: color-mix(in srgb, var(--tm-rail) 72%, transparent);
  }
  .ic {
    display: block;
    filter: drop-shadow(0 1px 1px rgb(0 0 0 / 0.3));
  }
  .title {
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.01em;
    color: var(--tm-rail-active);
  }
  .center {
    flex: 1;
    min-width: 0;
    display: flex;
    justify-content: center;
  }
  .center :global(.sync) {
    height: 24px;
    background: rgb(255 255 255 / 0.08);
    color: var(--tm-rail-fg);
  }
  .buttons {
    display: flex;
    align-self: stretch;
  }
  .wb {
    width: 46px;
    height: 100%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: none;
    border-radius: 0;
    background: transparent;
    color: var(--tm-rail-active);
    transition: background 0.1s;
  }
  .wb:hover {
    background: rgb(255 255 255 / 0.1);
  }
  .wb:active {
    background: rgb(255 255 255 / 0.16);
  }
  .wb.close:hover {
    background: #c42b1c;
    color: #fff;
  }
  .wb.close:active {
    background: #b22a1b;
    color: #fff;
  }
  .wb:focus-visible {
    outline: 2px solid var(--tm-accent);
    outline-offset: -2px;
  }
  /* Linux/macOS frameless: slightly rounder look, same buttons. */
  :global(html[data-platform='darwin']) .buttons {
    display: none;
  }
</style>
