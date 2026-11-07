<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2026 Midas Wollinger and TuxMail contributors -->
<!--
  App settings > Offline: what the Android app keeps on the device for reading
  without a network, what waits for the connection, and the rules (size,
  headers-only for older mails, network, background synchronisation).
  Stored per server profile (native/offline/bridge.ts).
-->
<script lang="ts">
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Trash from '@lucide/svelte/icons/trash-2';
  import { connection } from '../state/connection.svelte';
  import { fmt, formatSize, t } from '../i18n/index.svelte';
  import OutboxList from '../components/common/OutboxList.svelte';
  import { ATTACHMENT_CHOICES_MB, INTERVAL_CHOICES, SIZE_CHOICES_MB, parseDays, type OfflineSettings } from './offline/rules';
  import type { OfflineController } from './status';

  interface Props {
    ctl: OfflineController;
  }
  let { ctl }: Props = $props();

  const MiB = 1024 * 1024;
  const load = () => ctl.settings();
  const first = load();
  let s = $state<OfflineSettings>(first);
  let days = $state(first.headersOnlyOlderThanDays ? String(first.headersOnlyOlderThanDays) : '');
  let busy = $state(false);
  let cleared = $state(false);
  let error = $state<string | null>(null);
  const st = $derived(connection.status);
  let foreign = $state(load2());
  function load2() {
    return ctl.foreign();
  }
  let lockOn = $state(true);
  $effect(() => {
    void ctl.lockEnabled().then((v) => (lockOn = v));
  });
  let confirmForeign = $state<string | null>(null);
  function discardForeign(name: string) {
    if (confirmForeign !== name) {
      confirmForeign = name;
      return;
    }
    void run(async () => {
      await ctl.discardForeign(name);
      foreign = ctl.foreign();
      confirmForeign = null;
    });
  }

  async function run(fn: () => Promise<void>) {
    busy = true;
    error = null;
    try {
      await fn();
      await connection.refresh();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  function save(next: OfflineSettings) {
    s = next;
    void run(() => ctl.save(next));
  }

  function saveDays() {
    const d = parseDays(days);
    days = d ? String(d) : '';
    if (d !== s.headersOnlyOlderThanDays) save({ ...s, headersOnlyOlderThanDays: d });
  }

  const stateLabel = $derived(st.state === 'online' ? t('connection.state.online') : st.state === 'expired' ? t('connection.state.expired') : t('connection.state.offline'));
  const sizeChoices = $derived(SIZE_CHOICES_MB.includes(s.maxBytes / MiB) ? SIZE_CHOICES_MB : [...SIZE_CHOICES_MB, Math.round(s.maxBytes / MiB)].sort((a, b) => a - b));
  const intervalLabel = (m: number) => (m === 0 ? t('native.offline.intervalOff') : m < 60 ? t('native.offline.intervalMinutes', { count: m }) : t('native.offline.intervalHours', { count: m / 60 }));
</script>

<section data-testid="offline-settings" aria-labelledby="ns-offline">
  <h2 id="ns-offline">{t('native.offline.title')}</h2>
  <p class="muted">{t('native.offline.intro')}</p>
  {#if !lockOn}<p class="hint" data-testid="lock-recommended">{t('native.offline.lockRecommended')}</p>{/if}

  {#each foreign as f (f.name)}
    <div class="foreign" role="alert" data-testid="foreign-waiting">
      <p>{t('native.offline.foreignTitle', { count: f.waiting.length })}</p>
      <ul>
        {#each f.waiting as w (w.id)}<li>{w.kind === 'send' ? w.title || t('connection.outbox.noSubject') : t('connection.outbox.mark') + (w.title ? ': ' + w.title : '')}{#if w.recipients}<small> · {w.recipients}</small>{/if}</li>{/each}
      </ul>
      <p class="hint">{confirmForeign === f.name ? t('native.offline.foreignConfirm') : t('native.offline.foreignHint')}</p>
      <button class="btn danger" disabled={busy} onclick={() => discardForeign(f.name)}>{t('native.offline.foreignDiscard')}</button>
    </div>
  {/each}

  <dl>
    <dt>{t('native.offline.state')}</dt>
    <dd><span class="state" class:ok={st.state === 'online'} class:warn={st.state === 'offline'} class:fail={st.state === 'expired'} data-testid="offline-state">{stateLabel}</span></dd>
    <dt>{t('connection.lastDataSync')}</dt>
    <dd data-testid="offline-last-sync">{st.lastDataSyncAt ? fmt().dateTime(st.lastDataSyncAt) : t('connection.never')}</dd>
    <dt>{t('native.offline.used')}</dt>
    <dd>{t('native.offline.usedOf', { used: formatSize(st.cacheBytes), max: formatSize(s.maxBytes) })}</dd>
    <dt>{t('native.offline.waiting')}</dt>
    <dd data-testid="offline-waiting">{connection.waitingMails}</dd>
  </dl>
  {#if st.state === 'expired'}<p class="warn" role="alert">{t('connection.expiredText')}</p>{/if}
  <button class="btn" disabled={busy || st.state === 'expired'} title={st.state === 'expired' ? t('connection.bar.expired') : undefined} onclick={() => void run(() => ctl.syncNow())}><RefreshCw size={16} />{t('connection.syncNow')}</button>

  {#if st.outbox.length}
    <h3>{t('connection.outbox.title')}</h3>
    <OutboxList conn={connection} {busy} run={(fn) => void run(fn)} />
  {/if}

  <label class="label" for="ns-days">{t('native.offline.headersOnly')}</label>
  <div class="row">
    <input id="ns-days" class="input days" type="number" inputmode="numeric" min="0" max="36500" placeholder={t('native.offline.headersOnlyAll')} bind:value={days} onchange={saveDays} aria-describedby="ns-days-hint" />
    <span>{t('native.offline.days')}</span>
  </div>
  <p class="hint" id="ns-days-hint">{t('native.offline.headersOnlyHint')}</p>

  <label class="label" for="ns-size">{t('native.offline.maxSize')}</label>
  <select id="ns-size" class="input" value={String(Math.round(s.maxBytes / MiB))} onchange={(e) => save({ ...s, maxBytes: Number((e.currentTarget as HTMLSelectElement).value) * MiB })}>
    {#each sizeChoices as mb (mb)}<option value={String(mb)}>{formatSize(mb * MiB)}</option>{/each}
  </select>

  <label class="label" for="ns-att">{t('native.offline.attachments')}</label>
  <select id="ns-att" class="input" value={String(Math.round(s.attachmentMaxBytes / MiB))} onchange={(e) => save({ ...s, attachmentMaxBytes: Number((e.currentTarget as HTMLSelectElement).value) * MiB })}>
    {#each ATTACHMENT_CHOICES_MB as mb (mb)}<option value={String(mb)}>{mb === 0 ? t('native.offline.attachmentsOpen') : t('native.offline.attachmentsUpTo', { size: formatSize(mb * MiB) })}</option>{/each}
  </select>

  <fieldset>
    <legend class="label">{t('native.offline.network')}</legend>
    <label class="radio"><input type="radio" name="ns-net" checked={s.network === 'any'} onchange={() => save({ ...s, network: 'any' })} /> {t('native.offline.networkAny')}</label>
    <label class="radio"><input type="radio" name="ns-net" checked={s.network === 'wifi'} onchange={() => save({ ...s, network: 'wifi' })} /> {t('native.offline.networkWifi')}</label>
  </fieldset>

  <label class="label" for="ns-interval">{t('native.offline.interval')}</label>
  <select id="ns-interval" class="input" value={String(s.intervalMinutes)} onchange={(e) => save({ ...s, intervalMinutes: Number((e.currentTarget as HTMLSelectElement).value) })} aria-describedby="ns-interval-hint">
    {#each INTERVAL_CHOICES as m (m)}<option value={String(m)}>{intervalLabel(m)}</option>{/each}
  </select>
  <p class="hint" id="ns-interval-hint">{t('native.offline.intervalHint')}</p>

  <button class="btn" disabled={busy} onclick={() => void run(async () => {
      await ctl.clear();
      cleared = true;
    })}><Trash size={16} />{t('native.offline.clear')}</button>
  <p class="hint">{cleared ? t('native.offline.cleared') : t('native.offline.clearHint')}</p>
  {#if error}<p class="warn" role="alert">{error}</p>{/if}
</section>

<style>
  section {
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 640px;
  }
  h2 {
    margin: 0 0 8px;
    font-size: 14px;
  }
  .foreign {
    border: 1px solid var(--tm-danger);
    border-radius: 10px;
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 14px;
  }
  .foreign p,
  .foreign ul {
    margin: 0;
  }
  .foreign ul {
    padding-left: 18px;
    overflow-wrap: anywhere;
  }
  .danger {
    color: var(--tm-danger);
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 12px;
    margin: 0;
    font-size: 14px;
  }
  dt {
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  h3 {
    margin: 8px 0 0;
    font-size: 14px;
  }
  .state {
    display: inline-block;
    padding: 1px 10px;
    border-radius: 999px;
    font-size: 13px;
    border: 1px solid var(--tm-line);
  }
  .state.ok {
    color: var(--tm-success);
  }
  .state.warn {
    color: var(--tm-warn);
  }
  .state.fail {
    color: var(--tm-danger);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .days {
    width: 9rem;
    font-size: 16px;
  }
  .label {
    font-weight: 600;
    font-size: 14px;
    margin-top: 6px;
  }
  .hint {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .muted {
    margin: 0;
    color: var(--tm-muted);
  }
  .warn {
    margin: 0;
    color: var(--tm-danger);
  }
  fieldset {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .radio {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 44px;
    font-size: 14px;
  }
  .radio input {
    width: 20px;
    height: 20px;
  }
  .input {
    height: 44px;
    font-size: 16px;
  }
  .btn {
    height: 44px;
    justify-content: center;
    font-size: 14px;
  }
</style>
