<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Global sync indicator (title bar / top bar): spinner + "Synchronisiere
  <Konto> · <Ordner> 450 / 2036" with a thin progress bar while a sync runs,
  a warning with "Erneut versuchen" / "Details" (Settings → Info & Diagnose)
  after an error. Nothing is shown while everything is up to date.
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import RotateCw from '@lucide/svelte/icons/rotate-cw';
  import { app } from '../state';
  import { t } from '../i18n/index.svelte';

  interface Props {
    /** compact: icon only (narrow top bars); the text is in the tooltip. */
    compact?: boolean;
  }
  let { compact = false }: Props = $props();

  const active = $derived(app.syncActive[0]);
  const error = $derived(active ? undefined : app.syncErrors[0]);
  const label = $derived(active ? app.syncLabel(active) : '');
  const pct = $derived(active?.total ? Math.min(100, Math.round(((active.done ?? 0) / active.total) * 100)) : null);
  const errLabel = $derived(error ? t('sync.error', { account: app.account(error.accountId)?.displayName ?? '' }) : '');

  function details() {
    app.settingsAnchor = 'set-diag';
    app.navigate('settings');
  }
</script>

{#if active}
  <div class="sync" class:compact role="status" aria-live="polite" title={label} data-testid="sync-indicator">
    <LoaderCircle size={15} class="spin" aria-hidden="true" />
    <span class="txt">{label}</span>
    {#if pct != null}<span class="bar" aria-hidden="true"><span style:width="{pct}%"></span></span>{/if}
  </div>
{:else if error}
  <div class="sync err" class:compact role="alert" title={(error.error ?? '') || errLabel}>
    <TriangleAlert size={15} aria-hidden="true" />
    <span class="txt">{errLabel}</span>
    <button class="mini" onclick={() => void app.api.syncNow(error.accountId)} title={t('sync.retry')} aria-label={t('sync.retry')}><RotateCw size={13} /></button>
    <button class="link" onclick={details}>{t('sync.details')}</button>
  </div>
{/if}

<style>
  .sync {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    max-width: min(420px, 40vw);
    height: 28px;
    padding: 0 10px;
    border-radius: 999px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    font-size: 12px;
    overflow: hidden;
    flex: 0 1 auto;
  }
  .txt {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .compact {
    padding: 0 7px;
  }
  .compact .txt,
  .compact .link {
    display: none;
  }
  .err {
    color: var(--tm-danger, #c0392b);
    background: color-mix(in srgb, var(--tm-danger, #c0392b) 10%, transparent);
  }
  .bar {
    position: absolute;
    left: 10px;
    right: 10px;
    bottom: 3px;
    height: 2px;
    border-radius: 2px;
    background: color-mix(in srgb, var(--tm-accent) 18%, transparent);
  }
  .bar span {
    display: block;
    height: 100%;
    border-radius: 2px;
    background: var(--tm-accent);
    transition: width 0.25s;
  }
  .mini,
  .link {
    border: none;
    background: transparent;
    color: inherit;
    font: inherit;
    padding: 2px 4px;
    border-radius: 6px;
    cursor: pointer;
    display: inline-flex;
  }
  .link {
    text-decoration: underline;
  }
  .sync :global(.spin) {
    flex: none;
    animation: tm-spin 0.9s linear infinite;
  }
  @keyframes tm-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .sync :global(.spin) {
      animation-duration: 3s;
    }
  }
</style>
