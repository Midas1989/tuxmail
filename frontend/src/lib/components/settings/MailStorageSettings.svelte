<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings › Mail › Speicher & offline: keep older mails with header and preview only (mail.headersOnlyOlderThanDays, this device only). -->
<script lang="ts">
  import { app, connection } from '../../state';
  import { formatNumber, formatSize, t } from '../../i18n/index.svelte';
  import type { MailStorageEstimate, MailStorageSettings } from '../../types';

  const MAX_DAYS = 36500;
  type Mode = 'default' | 'all' | 'days';
  const modes: Mode[] = ['default', 'all', 'days'];

  let current = $state<MailStorageSettings | null>(null);
  let mode = $state<Mode>('default');
  let days = $state(90);
  let saving = $state(false);
  let message = $state<string | null>(null);
  let error = $state<string | null>(null);
  /** Rückfrage: what the new value would download. */
  let confirm = $state<MailStorageEstimate | null>(null);

  function modeOf(n: number): Mode {
    return n < 0 ? 'default' : n === 0 ? 'all' : 'days';
  }

  async function load() {
    try {
      const s = await app.api.getMailStorageSettings();
      current = s;
      mode = modeOf(s.headersOnlyOlderThanDays);
      if (s.headersOnlyOlderThanDays > 0) days = s.headersOnlyOlderThanDays;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
  void load();

  const daysValid = $derived(Number.isInteger(days) && days >= 1 && days <= MAX_DAYS);
  const wanted = $derived(mode === 'default' ? -1 : mode === 'all' ? 0 : days);
  const dirty = $derived(!!current && wanted !== current.headersOnlyOlderThanDays);

  /** Mails of the last days stored completely under a value (-1: as before = 30, 0: all). */
  const windowDays = (n: number) => (n === 0 ? Infinity : n < 0 ? 30 : n);

  async function save(confirmed = false) {
    if (mode === 'days' && !daysValid) {
      error = t('settings.storage.invalid');
      return;
    }
    saving = true;
    error = null;
    message = null;
    try {
      // "All" or a larger count downloads more: ask first, with an estimate.
      if (!confirmed && current && windowDays(wanted) > windowDays(current.headersOnlyOlderThanDays)) {
        const est = await app.api.estimateMailStorage(wanted);
        if (est.messages > 0) {
          confirm = est;
          return;
        }
      }
      confirm = null;
      current = await app.api.setMailStorageSettings({ headersOnlyOlderThanDays: wanted, downloadPaused: current?.downloadPaused ?? false });
      message = t('settings.storage.saved');
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function togglePause() {
    if (!current) return;
    try {
      current = await app.api.setMailStorageSettings({ headersOnlyOlderThanDays: current.headersOnlyOlderThanDays, downloadPaused: !current.downloadPaused });
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  // Progress of a running download: refreshed every few seconds.
  $effect(() => {
    if (!current?.pending || current.downloadPaused) return;
    const timer = setInterval(() => {
      void app.api.getMailStorageSettings().then(
        (s) => (current = s),
        () => undefined,
      );
    }, 5000);
    return () => clearInterval(timer);
  });
</script>

<section class="card" id="set-storage" aria-labelledby="set-storage-h">
  <h2 id="set-storage-h">{t('settings.storage.title')}</h2>
  <p class="intro" id="set-storage-intro">{t('settings.storage.intro')}</p>
  {#if current && current.available}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <fieldset class="modes" aria-describedby="set-storage-intro">
        <legend class="sr">{t('settings.storage.modeLabel')}</legend>
        {#each modes as m (m)}
          <label class="check">
            <input type="radio" name="storage-mode" value={m} bind:group={mode} data-testid="storage-mode-{m}" />
            <span>{t(`settings.storage.mode.${m}`)}</span>
            {#if m === 'days'}
              <input
                class="input days"
                type="number"
                min="1"
                max={MAX_DAYS}
                step="1"
                bind:value={days}
                disabled={mode !== 'days'}
                aria-label={t('settings.storage.daysLabel')}
                aria-invalid={mode === 'days' && !daysValid}
                aria-describedby="set-storage-state"
                data-testid="storage-days"
              />
              <span>{t('settings.storage.days')}</span>
            {/if}
          </label>
        {/each}
      </fieldset>
      <div class="row">
        <p class="state" id="set-storage-state" role="status">
          {#if mode === 'days' && daysValid}{t('settings.storage.on', { days: formatNumber(days) })}{:else if mode === 'all'}{t('settings.storage.all')}{:else if mode === 'default'}{t('settings.storage.off')}{/if}
          {#if message}<span class="ok">{message}</span>{/if}
        </p>
        <button class="btn btn-primary" type="submit" disabled={!dirty || saving || (mode === 'days' && !daysValid)}>{t('common.action.save')}</button>
      </div>
      {#if confirm}
        <div class="confirm" role="alertdialog" aria-labelledby="set-storage-confirm" data-testid="storage-confirm">
          <p id="set-storage-confirm">{t('settings.storage.confirm', { count: formatNumber(confirm.messages), size: formatSize(confirm.bytes) })}</p>
          <div class="confirm-actions">
            <button class="btn" type="button" onclick={() => (confirm = null)}>{t('settings.storage.confirmNo')}</button>
            <button class="btn btn-primary" type="button" disabled={saving} onclick={() => void save(true)}>{t('settings.storage.confirmYes')}</button>
          </div>
        </div>
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </form>
    {#if current.pending}
      <div class="progress" role="status" data-testid="storage-progress">
        <span>
          {t(current.downloadPaused ? 'settings.storage.progressPaused' : 'settings.storage.progress', { count: formatNumber(current.pending), size: formatSize(current.pendingBytes ?? 0) })}
        </span>
        <button class="btn" type="button" onclick={togglePause}>{current.downloadPaused ? t('settings.storage.resume') : t('settings.storage.pause')}</button>
      </div>
    {/if}
    <ul class="help">
      <li>{t('settings.storage.release')}</li>
      <li>{t('settings.storage.exceptions')}</li>
      <li>{t('settings.storage.opening')}</li>
      <li>{t('settings.storage.search')}</li>
      <li>{t('settings.storage.providers')}</li>
      {#if connection.serverMode}<li>{t('settings.storage.thin')}</li>{/if}
      <li>{t('settings.storage.perAccount')}</li>
    </ul>
    {#if !connection.serverMode && current.headersOnly + current.complete > 0}
      <p class="stats">{t('settings.storage.stats', { headersOnly: formatNumber(current.headersOnly), complete: formatNumber(current.complete) })}</p>
    {/if}
  {:else if error}
    <p class="error" role="alert">{error}</p>
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  h2 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 650;
  }
  .intro {
    margin: 0 0 12px;
    font-size: 13px;
    line-height: 1.5;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .modes {
    border: 0;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .check {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    min-height: 32px;
  }
  .row .state {
    flex: 1;
  }
  .days {
    width: 6.5em;
  }
  .row .btn {
    margin-left: auto;
  }
  .state {
    margin: 8px 0 0;
    font-size: 12.5px;
    color: var(--tm-muted);
    min-height: 1.4em;
  }
  .ok {
    margin-left: 8px;
    color: var(--tm-success, #15803d);
  }
  .confirm,
  .progress {
    margin: 10px 0 0;
    padding: 10px 12px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--tm-warn, #f59e0b) 12%, var(--tm-surface));
    font-size: 13px;
  }
  .confirm p {
    margin: 0 0 8px;
  }
  .confirm-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .progress {
    display: flex;
    align-items: center;
    gap: 10px;
    background: color-mix(in srgb, var(--tm-accent) 8%, var(--tm-surface));
  }
  .progress span {
    flex: 1;
  }
  .help {
    margin: 12px 0 0;
    padding-left: 18px;
    font-size: 12.5px;
    line-height: 1.5;
    color: var(--tm-muted);
  }
  .help li + li {
    margin-top: 4px;
  }
  .stats {
    margin: 10px 0 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .error {
    color: var(--tm-danger, #b42318);
    font-size: 13px;
  }
</style>
