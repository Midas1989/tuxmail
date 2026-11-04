<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings → Allgemein → Fenster (desktop only): "Systemtitelleiste verwenden". -->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import { desktopWindow } from '../../native/window.svelte';

  let restartHint = $state(false);

  async function toggleTitleBar(e: Event) {
    const native = (e.currentTarget as HTMLInputElement).checked;
    await desktopWindow.setNativeTitleBar(native);
    restartHint = true;
  }
</script>

{#if desktopWindow.info}
  <section class="card" id="set-window" aria-labelledby="set-window-h">
    <h2 id="set-window-h">{t('settings.window.title')}</h2>
    <label class="check">
      <input type="checkbox" checked={desktopWindow.info.nativeTitleBar} onchange={toggleTitleBar} />
      <span>
        {t('settings.window.nativeTitleBar')}
        <small class="muted">{t('settings.window.nativeTitleBarHint')}</small>
      </span>
    </label>
    {#if restartHint}<p class="hint" role="status">{t('settings.window.restart')}</p>{/if}
  </section>
{/if}

<style>
  .check {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    font-size: 13px;
  }
  .check small {
    display: block;
  }
  .muted {
    color: var(--tm-muted);
    font-size: 12.5px;
  }
  .hint {
    font-size: 12.5px;
    color: var(--tm-warn);
  }
</style>
