<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Shown instead of "Dieser Ordner ist leer" while the list's folder (or, in
  unified views, an account in its first sync) is still being synchronised:
  skeleton rows and "Wird synchronisiert … (450 von 2036)".
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import type { AccountSyncStatus } from '../../types';
  import { t } from '../../i18n/index.svelte';

  let { status }: { status: AccountSyncStatus } = $props();
</script>

<div class="lss" role="status" aria-live="polite" data-testid="list-syncing">
  <div class="msg">
    <LoaderCircle size={22} class="spin" aria-hidden="true" />
    <p class="e1">{t('sync.listSyncing')}</p>
    <p class="e2">
      {status.total ? t('sync.listSyncingHint', { done: Math.min(status.done ?? 0, status.total), total: status.total }) : t('sync.listSyncingHintNoCount')}
    </p>
  </div>
  {#each [0, 1, 2, 3, 4, 5] as i (i)}
    <div class="sk" aria-hidden="true" style:opacity={1 - i * 0.14}>
      <span class="av"></span>
      <span class="lines"><span class="l a"></span><span class="l b"></span><span class="l c"></span></span>
    </div>
  {/each}
</div>

<style>
  .lss {
    padding: 8px 0;
  }
  .msg {
    text-align: center;
    padding: 18px 16px 12px;
    color: var(--tm-muted);
  }
  .e1 {
    margin: 8px 0 2px;
    color: var(--tm-fg);
    font-weight: 600;
  }
  .e2 {
    margin: 0;
    font-size: 12.5px;
  }
  .sk {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 10px 14px;
  }
  .av {
    width: 34px;
    height: 34px;
    border-radius: 50%;
    flex: none;
  }
  .lines {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .l {
    height: 9px;
    border-radius: 4px;
  }
  .a {
    width: 45%;
  }
  .b {
    width: 80%;
  }
  .c {
    width: 60%;
  }
  .av,
  .l {
    background: linear-gradient(90deg, var(--tm-surface-2) 0%, var(--tm-line) 50%, var(--tm-surface-2) 100%);
    background-size: 200% 100%;
    animation: tm-shimmer 1.4s ease-in-out infinite;
  }
  .msg :global(.spin) {
    animation: tm-spin 0.9s linear infinite;
  }
  @keyframes tm-shimmer {
    from {
      background-position: 100% 0;
    }
    to {
      background-position: -100% 0;
    }
  }
  @keyframes tm-spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .av,
    .l {
      animation: none;
    }
  }
</style>
