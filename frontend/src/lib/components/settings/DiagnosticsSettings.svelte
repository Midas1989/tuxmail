<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Info & Diagnose: version, mode, data directory and log file,
  per-account sync status (state, folder, progress, last error) and the
  diagnostics export (zip with logs and the redacted configuration). The
  desktop window setting lives in WindowSettings.svelte (Allgemein).
-->
<script lang="ts">
  import Download from '@lucide/svelte/icons/download';
  import RotateCw from '@lucide/svelte/icons/rotate-cw';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import { app } from '../../state';
  import { t, formatFullDate } from '../../i18n/index.svelte';
  import type { DiagnosticsInfo } from '../../types';

  let info = $state<DiagnosticsInfo | null>(null);
  let busy = $state(false);

  async function load() {
    try {
      info = await app.api.diagnostics();
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }
  $effect(() => {
    void load();
  });
  // Live: the sync status of the page follows sync.status events.
  const live = $derived(app.syncStatus);

  async function exportZip() {
    busy = true;
    try {
      const z = await app.api.exportDiagnostics();
      if (z.savedPath) {
        app.toast(t('settings.diag.exported', { file: z.savedPath }));
      } else {
        const url = URL.createObjectURL(z.blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = z.filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 30_000);
        app.toast(t('settings.diag.exported', { file: z.filename }));
      }
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    } finally {
      busy = false;
    }
  }
</script>

<section class="card wide-card" id="set-diag" aria-labelledby="set-diag-h">
  <div class="card-head">
    <h2 id="set-diag-h">{t('settings.pages.about.sections.diagnostics')}</h2>
    <button class="icon-btn" title={t('settings.diag.refresh')} aria-label={t('settings.diag.refresh')} onclick={load}><RefreshCw size={15} /></button>
  </div>
  {#if info}
    <dl class="kv">
      <dt>{t('settings.diag.version')}</dt>
      <dd><code>{info.version}</code></dd>
      <dt>{t('settings.diag.mode')}</dt>
      <dd>{info.mode === 'desktop' ? t('settings.diag.modeDesktop') : t('settings.diag.modeServer')}{info.portable ? ` (${t('settings.diag.portable')})` : ''}</dd>
      {#if info.dataDir}
        <dt>{t('settings.diag.dataDir')}</dt>
        <dd><code class="path">{info.dataDir}</code></dd>
      {/if}
      {#if info.logFile}
        <dt>{t('settings.diag.logFile')}</dt>
        <dd><code class="path">{info.logFile}</code></dd>
      {/if}
      <dt>{t('settings.diag.system')}</dt>
      <dd><code>{info.os} · {info.goVersion}</code></dd>
    </dl>

    <h3>{t('settings.diag.accounts')}</h3>
    {#if !info.accounts.length}
      <p class="muted">{t('settings.diag.noAccounts')}</p>
    {/if}
    <div class="accs">
      {#each info.accounts as acc (acc.id)}
        {@const st = live[acc.id] ?? acc.status}
        <div class="acc" data-testid="diag-account">
          <div class="acc-h">
            <strong>{acc.name}</strong>
            <span class="kind">{acc.kind.toUpperCase()}</span>
            {#if st}<span class="state s-{st.state}">{t(`settings.diag.states.${st.state}`)}</span>{/if}
            <span class="grow"></span>
            <button class="btn" onclick={() => void app.api.syncNow(acc.id)}><RotateCw size={13} /> {t('settings.diag.syncNow')}</button>
          </div>
          <dl class="kv small">
            {#if acc.incoming}<dt>{t('settings.diag.incoming')}</dt><dd><code>{acc.incoming}</code></dd>{/if}
            {#if acc.outgoing}<dt>{t('settings.diag.outgoing')}</dt><dd><code>{acc.outgoing}</code></dd>{/if}
            <dt>{t('settings.diag.folder')}</dt>
            <dd>{t('settings.diag.folders', { count: acc.folders, messages: acc.messages })}</dd>
            {#if st?.folder}
              <dt>{t('settings.diag.progress')}</dt>
              <dd>{app.syncLabel(st)}</dd>
            {/if}
            <dt>{t('settings.diag.lastSync')}</dt>
            <dd>{st?.lastSync ? formatFullDate(st.lastSync) : t('settings.diag.never')}</dd>
            {#if st?.error}
              <dt>{t('settings.diag.lastError')}</dt>
              <dd class="err">{st.error}{st.errorAt ? ` (${formatFullDate(st.errorAt)})` : ''}</dd>
            {/if}
          </dl>
        </div>
      {/each}
    </div>
  {/if}

  <div class="export">
    <button class="btn btn-primary" disabled={busy} onclick={exportZip} data-testid="diag-export"><Download size={15} /> {t('settings.diag.export')}</button>
    <p class="muted">{t('settings.diag.exportHint')}</p>
  </div>

</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .wide-card {
    grid-column: 1 / -1;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 8px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  h3 {
    margin: 16px 0 8px;
    font-size: 13px;
    font-weight: 650;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 14px;
    margin: 0;
    font-size: 13px;
  }
  .kv dt {
    color: var(--tm-muted);
  }
  .kv dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .kv.small {
    font-size: 12.5px;
  }
  .path {
    user-select: all;
  }
  .accs {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 10px;
  }
  .acc {
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .acc-h {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 6px;
    flex-wrap: wrap;
  }
  .grow {
    flex: 1;
  }
  .kind {
    font-size: 10.5px;
    color: var(--tm-muted);
    border: 1px solid var(--tm-line);
    padding: 0 5px;
    border-radius: 4px;
  }
  .state {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--tm-surface-2);
  }
  .s-idle {
    color: var(--tm-ok, #15803d);
  }
  .s-syncing,
  .s-connecting {
    color: var(--tm-accent);
  }
  .s-error {
    color: var(--tm-danger);
  }
  .err {
    color: var(--tm-danger);
  }
  .muted {
    color: var(--tm-muted);
    font-size: 12.5px;
  }
  .export {
    margin-top: 14px;
    display: flex;
    gap: 12px;
    align-items: center;
    flex-wrap: wrap;
  }
  .export p {
    margin: 0;
    flex: 1;
    min-width: 220px;
  }
</style>
