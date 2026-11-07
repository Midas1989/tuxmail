<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Short notice while the desktop app cannot use its TuxMail server: what still
  works, how many mails wait, and "sign in again" when the device was rejected.
-->
<script lang="ts">
  import CloudOff from '@lucide/svelte/icons/cloud-off';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import { connection } from '../../state/connection.svelte';
  import { t } from '../../i18n/index.svelte';

  const text = $derived.by(() => {
    const local = connection.status.mode === 'local';
    switch (connection.status.state) {
      case 'expired':
        return t('connection.bar.expired');
      case 'locked':
        return t('connection.bar.locked');
      default:
        return local ? t('connection.bar.offlineLocal') : t('connection.bar.offline');
    }
  });
</script>

{#if connection.unreachable}
  <div class="bar" class:danger={connection.expired} role={connection.expired ? 'alert' : 'status'} data-testid="connection-bar">
    {#if connection.expired}<KeyRound size={15} aria-hidden="true" />{:else}<CloudOff size={15} aria-hidden="true" />{/if}
    <span class="text">{text}</span>
    {#if connection.waitingMails > 0}
      <span class="count">{t('connection.bar.waiting', { count: connection.waitingMails })}</span>
    {/if}
    {#if connection.expired && connection.status.serverUrl}
      <button class="btn btn-ghost" onclick={() => (connection.wizard = { purpose: 'reauth', serverUrl: connection.status.serverUrl! })}>{t('connection.reauth')}</button>
    {/if}
  </div>
{/if}

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    padding: 0 12px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-warn) 18%, var(--tm-surface));
    color: var(--tm-fg);
    border-bottom: 1px solid var(--tm-line);
  }
  .bar.danger {
    background: color-mix(in srgb, var(--tm-danger) 16%, var(--tm-surface));
  }
  .text {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    text-overflow: ellipsis;
  }
  .count {
    font-weight: 600;
    white-space: nowrap;
  }
  .btn {
    height: 26px;
    white-space: nowrap;
  }
</style>
