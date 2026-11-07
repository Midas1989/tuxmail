<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings › Mail › Speicher & offline: keep older mails with header and preview only (mail.headersOnlyOlderThanDays, this device only). -->
<script lang="ts">
  import { app, connection } from '../../state';
  import { formatNumber, t } from '../../i18n/index.svelte';
  import type { MailStorageSettings } from '../../types';

  const MAX_DAYS = 36500;

  let current = $state<MailStorageSettings | null>(null);
  let enabled = $state(false);
  let days = $state(90);
  let saving = $state(false);
  let message = $state<string | null>(null);
  let error = $state<string | null>(null);

  async function load() {
    try {
      const s = await app.api.getMailStorageSettings();
      current = s;
      enabled = s.headersOnlyOlderThanDays > 0;
      if (enabled) days = s.headersOnlyOlderThanDays;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
  void load();

  const daysValid = $derived(Number.isInteger(days) && days >= 1 && days <= MAX_DAYS);
  const wanted = $derived(enabled ? days : 0);
  const dirty = $derived(!!current && wanted !== current.headersOnlyOlderThanDays);

  async function save() {
    if (enabled && !daysValid) {
      error = t('settings.storage.invalid');
      return;
    }
    saving = true;
    error = null;
    message = null;
    try {
      current = await app.api.setMailStorageSettings({ headersOnlyOlderThanDays: wanted });
      message = t('settings.storage.saved');
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
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
      <div class="row">
        <label class="check">
          <input type="checkbox" bind:checked={enabled} aria-describedby="set-storage-intro" data-testid="storage-enabled" />
          {t('settings.storage.enable')}
        </label>
        <input
          class="input days"
          type="number"
          min="1"
          max={MAX_DAYS}
          step="1"
          bind:value={days}
          disabled={!enabled}
          aria-label={t('settings.storage.daysLabel')}
          aria-invalid={enabled && !daysValid}
          aria-describedby="set-storage-state"
          data-testid="storage-days"
        />
        <span>{t('settings.storage.days')}</span>
        <button class="btn btn-primary" type="submit" disabled={!dirty || saving || (enabled && !daysValid)}>{t('common.action.save')}</button>
      </div>
      <p class="state" id="set-storage-state" role="status">
        {#if enabled && daysValid}{t('settings.storage.on', { days: formatNumber(days) })}{:else if !enabled}{t('settings.storage.off')}{/if}
        {#if message}<span class="ok">{message}</span>{/if}
      </p>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </form>
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
  .check {
    display: inline-flex;
    align-items: center;
    gap: 8px;
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
