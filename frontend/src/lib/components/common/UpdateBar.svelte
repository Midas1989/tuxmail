<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  "TuxMail was updated. Reload": the server serves another client than this tab
  runs (see state/updateWatch.svelte.ts). The page is only reloaded by the button,
  and not while a mail is being written.
-->
<script lang="ts">
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import { app } from '../../state';
  import { updateWatch } from '../../state/updateWatch.svelte';
  import { t } from '../../i18n/index.svelte';

  const writing = $derived(app.composer != null);
  let busy = $state(false);

  async function reload() {
    busy = true;
    await updateWatch.reload();
  }
</script>

{#if updateWatch.available}
  <div class="bar" role="status" data-testid="update-bar">
    <RefreshCw size={15} aria-hidden="true" />
    <span class="text">
      {t('connection.update.text')}
      {#if writing}<span class="hint">{t('connection.update.composing')}</span>{/if}
    </span>
    <button class="btn btn-primary" disabled={writing || busy} onclick={() => void reload()}>{t('connection.update.reload')}</button>
  </div>
{/if}

<style>
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 34px;
    padding: 4px 12px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-accent, #2563eb) 14%, var(--tm-surface));
    color: var(--tm-fg);
    border-bottom: 1px solid var(--tm-line);
  }
  .text {
    flex: 1;
    min-width: 0;
    line-height: 1.3;
  }
  .hint {
    color: var(--tm-muted);
    margin-left: 6px;
  }
  .btn {
    height: 26px;
    white-space: nowrap;
  }
</style>
