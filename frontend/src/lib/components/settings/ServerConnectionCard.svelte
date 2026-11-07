<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Einstellungen > Konten > TuxMail-Server: the lasting connection of the desktop
  app. Status, last contact, change of mode, "sync now", disconnect. Token and
  key stay in the core: nothing here ever sees them.
-->
<script lang="ts">
  import Server from '@lucide/svelte/icons/server';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Unplug from '@lucide/svelte/icons/unplug';
  import ArrowLeftRight from '@lucide/svelte/icons/arrow-left-right';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import { connection as defaultConnection, type ConnectionState } from '../../state/connection.svelte';
  import { t, fmt, formatSize, type MessageKey } from '../../i18n/index.svelte';
  import type { ClientMode, SettingsLogEntry } from '../../types';
  import { openExternal } from '../../platform';
  import Modal from '../common/Modal.svelte';
  import ServerModeChoice from './ServerModeChoice.svelte';
  import OutboxList from '../common/OutboxList.svelte';

  interface Props {
    /** Tests only. */
    conn?: ConnectionState;
  }
  let { conn = defaultConnection }: Props = $props();

  const s = $derived(conn.status);
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function run(fn: () => Promise<void>) {
    busy = true;
    error = null;
    try {
      await fn();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  const attn = $derived(s.attention);

  const when = (iso?: string) => (iso ? fmt().dateTime(iso) : t('connection.never'));

  const STATE: Record<string, { label: MessageKey; cls: string }> = {
    online: { label: 'connection.state.online', cls: 'ok' },
    offline: { label: 'connection.state.offline', cls: 'warn' },
    expired: { label: 'connection.state.expired', cls: 'fail' },
    locked: { label: 'connection.state.locked', cls: 'warn' },
  };

  // ----- change the mode ------------------------------------------------------------------------
  let modeOpen = $state(false);
  let newMode = $state<ClientMode>('thin');

  function openMode() {
    newMode = s.mode === 'thin' ? 'local' : 'thin';
    error = null;
    modeOpen = true;
  }

  function applyMode() {
    if (newMode === s.mode) {
      modeOpen = false;
    } else if (newMode === 'thin') {
      void run(() => conn.switchToServer());
    } else {
      // Asks what to set up on this device, and fetches the passwords sealed to this device.
      modeOpen = false;
      conn.wizard = { purpose: 'switchLocal', serverUrl: s.serverUrl! };
    }
  }

  // ----- disconnect -----------------------------------------------------------------------------
  let leaveOpen = $state(false);
  let deleteCache = $state(true);
  let discardOutbox = $state(false);
  let notRevoked = $state(false);

  function openLeave() {
    deleteCache = true;
    discardOutbox = false;
    error = null;
    leaveOpen = true;
  }

  const mustDecideOutbox = $derived(s.outbox.length > 0);

  function leave() {
    void run(async () => {
      const r = await conn.disconnect({ deleteCache, discardOutbox });
      if (r.revoked) {
        conn.reloadApp();
      } else {
        leaveOpen = false;
        notRevoked = true;
      }
    });
  }

  function logLine(e: SettingsLogEntry): string {
    return t(`connection.log.${e.action}`, { name: e.name || t(`connection.log.kind.${e.kind}`) });
  }
</script>

<section class="card" id="set-server" aria-labelledby="set-server-h" data-testid="server-card">
  <h2 id="set-server-h">{t('onboarding.connect.cardTitle')}</h2>
  {#if !s.connected}
    <p class="intro">{t('onboarding.connect.cardText')}</p>
    <button class="btn btn-primary" onclick={() => (conn.wizard = { purpose: 'connect' })}><Server size={15} /> {t('onboarding.connect.title')}</button>
  {:else}
    {#if conn.expired}
      <div class="notice fail" role="alert">
        <p><b>{t('connection.expiredTitle')}</b> {t('connection.expiredText')}</p>
        <button class="btn btn-primary" onclick={() => conn.reauth()}><KeyRound size={15} /> {t('connection.reauth')}</button>
      </div>
    {/if}
    <p class="who">
      {t('connection.connectedAs', { server: s.serverName ?? '', user: s.displayName || s.username || '' })}
      <span class="badge {STATE[s.state]?.cls}" data-testid="server-state">{t(STATE[s.state]?.label ?? 'connection.state.offline')}</span>
    </p>
    <dl>
      <dt>{t('connection.modeLabel')}</dt>
      <dd>{s.mode === 'thin' ? t('onboarding.connect.modeThin') : t('onboarding.connect.modeLocal')}</dd>
      <dt>{t('connection.device')}</dt>
      <dd>{s.deviceName}</dd>
      <dt>{t('connection.lastConnection')}</dt>
      <dd>{when(s.lastConnectedAt)}</dd>
      {#if s.mode === 'local'}
        <dt>{t('connection.lastSettingsSync')}</dt>
        <dd data-testid="last-settings-sync">{when(s.lastSettingsSyncAt)}</dd>
      {:else}
        <dt>{t('connection.lastDataSync')}</dt>
        <dd>{when(s.lastDataSyncAt)}</dd>
        <dt>{t('connection.cache')}</dt>
        <dd>{formatSize(s.cacheBytes)}</dd>
      {/if}
    </dl>
    {#if s.mode === 'thin'}
      <p class="admin-note" data-testid="admin-note">
        {t('connection.adminBrowser')}
        <button class="linkish" onclick={() => openExternal(s.serverUrl ?? '')}>{t('connection.openInBrowser')}</button>
      </p>
    {/if}
    <div class="actions">
      <button class="btn" disabled={busy || conn.expired} onclick={() => void run(() => conn.syncNow())}><RefreshCw size={15} /> {t('connection.syncNow')}</button>
      <button class="btn" disabled={busy || conn.expired || s.state !== 'online'} title={s.state !== 'online' ? t('connection.notAvailableOffline') : undefined} onclick={openMode}><ArrowLeftRight size={15} /> {t('connection.changeMode')}</button>
      <button class="btn btn-ghost danger" disabled={busy} onclick={openLeave}><Unplug size={15} /> {t('connection.disconnect')}</button>
    </div>
    {#if error && !leaveOpen && !modeOpen}<p class="error" role="alert">{error}</p>{/if}

    {#if s.outbox.length}
      <h3>{t('connection.outbox.title')}</h3>
      <OutboxList {conn} {busy} run={(fn) => void run(fn)} />
    {/if}

    {#if attn?.pendingWorkflows.length}
      <h3 data-testid="pending-title">{t('connection.rules.waiting', { count: attn.pendingWorkflows.length })}</h3>
      <p class="sub">{t('connection.rules.why')}</p>
      <ul class="outbox" data-testid="pending-rules">
        {#each attn.pendingWorkflows as w (w.id)}
          <li>
            <span class="what">
              <b>{w.name} · {w.isNew ? t('connection.rules.isNew') : t('connection.rules.changed')}</b>
              {#if w.before}<small>{t('connection.rules.beforeLabel')}</small><code data-testid="rule-before">{w.before.join('\n')}</code>{/if}
              <small>{t('connection.rules.afterLabel')}</small><code data-testid="rule-after">{w.after.join('\n')}</code>
              {#if w.wasEnabled}<small class="bad">{t('connection.rules.wasOn')}</small>{/if}
              {#if w.external}<small class="bad">{t('connection.rules.external')}</small>{/if}
            </span>
            <button class="btn btn-ghost" disabled={busy} onclick={() => void run(() => conn.workflowApprove(w.id, w.hash))}>{t('connection.rules.approve')}</button>
            <button class="btn btn-ghost" disabled={busy} onclick={() => void run(() => conn.workflowDismiss(w.id))}>{t('connection.rules.keepOff')}</button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if attn?.conflicts.length}
      <h3>{t('connection.rules.conflictTitle')}</h3>
      <ul class="outbox" data-testid="rule-conflicts">
        {#each attn.conflicts as w (w.id)}
          <li>
            <span class="what">
              <b>{w.name}</b>
              <small>{t('connection.rules.hereLabel')}</small><code>{w.local.join('\n')}</code>
              <small>{t('connection.rules.onServerLabel')}</small><code>{w.server.join('\n')}</code>
            </span>
            <button class="btn btn-ghost" disabled={busy} onclick={() => void run(() => conn.workflowResolve(w.id, true))}>{t('connection.rules.keepMine')}</button>
            <button class="btn btn-ghost" disabled={busy} onclick={() => void run(() => conn.workflowResolve(w.id, false))}>{t('connection.rules.takeServer')}</button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if attn?.deletes.some((d) => !d.decided)}
      <h3>{t('connection.deletes.title')}</h3>
      <ul class="outbox" data-testid="mass-deletes">
        {#each attn.deletes.filter((d) => !d.decided) as d (d.kind)}
          <li>
            <span class="what">
              <b>{t('connection.deletes.what', { count: d.names.length, kind: t(`connection.log.kind.${d.kind}`) })}</b>
              <small>{d.names.join(', ')}</small>
            </span>
            <button class="btn btn-ghost danger" disabled={busy} onclick={() => void run(() => conn.settingsDeletes(d.kind, true))}>{t('connection.deletes.approve')}</button>
            <button class="btn btn-ghost" disabled={busy} onclick={() => void run(() => conn.settingsDeletes(d.kind, false))}>{t('connection.deletes.keep')}</button>
          </li>
        {/each}
      </ul>
    {/if}

    {#if s.mode === 'local' && s.settingsLog.length}
      <details class="log">
        <summary>{t('connection.log.title')}</summary>
        <ul>
          {#each [...s.settingsLog].reverse() as e, i (i)}
            <li><time>{fmt().dateTime(e.at)}</time> {logLine(e)}{#if e.conflict}&nbsp;<em>{t('connection.log.conflict')}</em>{/if}</li>
          {/each}
        </ul>
      </details>
    {/if}
  {/if}
</section>

{#if modeOpen}
  <Modal title={t('connection.changeMode')} onclose={() => (modeOpen = false)} width="620px">
    <ServerModeChoice bind:mode={newMode} />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#snippet footer()}
      <button class="btn" onclick={() => (modeOpen = false)}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={busy || newMode === s.mode} onclick={applyMode}>{t('connection.applyMode')}</button>
    {/snippet}
  </Modal>
{/if}

{#if leaveOpen}
  <Modal title={t('connection.leave.title', { server: s.serverName ?? '' })} onclose={() => (leaveOpen = false)} width="560px">
    <div class="leave" data-testid="leave-dialog">
      <p>{t('connection.leave.what')}</p>
      <ul>
        <li>{t('connection.leave.device')}</li>
        <li>{t('connection.leave.server')}</li>
        <li>{t('connection.leave.local')}</li>
      </ul>
      {#if s.mode === 'thin'}
        <label class="check"><input type="checkbox" bind:checked={deleteCache} /> {t('connection.leave.deleteCache', { size: formatSize(s.cacheBytes) })}</label>
      {/if}
      {#if mustDecideOutbox}
        <p class="warn" role="alert">{t('connection.leave.outbox', { count: s.outbox.length })}</p>
        <label class="check"><input type="checkbox" bind:checked={discardOutbox} /> {t('connection.leave.discardOutbox')}</label>
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
    </div>
    {#snippet footer()}
      <button class="btn" onclick={() => (leaveOpen = false)}>{t('common.action.cancel')}</button>
      <button class="btn btn-danger" disabled={busy || (mustDecideOutbox && !discardOutbox)} onclick={leave}>{t('connection.disconnect')}</button>
    {/snippet}
  </Modal>
{/if}

{#if notRevoked}
  <Modal title={t('connection.leave.doneTitle')} onclose={() => conn.reloadApp()} width="520px">
    <p class="warn" role="alert">{t('connection.leave.notRevoked')}</p>
    {#snippet footer()}
      <button class="btn btn-primary" onclick={() => conn.reloadApp()}>{t('common.action.close')}</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .who {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    margin: 0 0 10px;
    font-weight: 600;
  }
  .badge {
    background: transparent;
    padding: 1px 10px;
    border-radius: 99px;
    font-size: 12px;
    font-weight: 600;
    border: 1px solid currentColor;
  }
  .badge.ok {
    color: var(--tm-success);
  }
  .badge.warn {
    color: var(--tm-warn);
  }
  .badge.fail {
    color: var(--tm-danger);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 16px;
    margin: 0 0 12px;
    font-size: 13.5px;
  }
  dt {
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  /* Narrow window: label above value, so a date or device name is not torn apart. */
  @media (max-width: 560px) {
    dl {
      grid-template-columns: 1fr;
      gap: 0;
    }
    dd {
      margin-bottom: 8px;
    }
  }
  .admin-note {
    margin: 0 0 12px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .linkish {
    background: none;
    border: 0;
    padding: 0;
    color: var(--tm-accent-text);
    text-decoration: underline;
    cursor: pointer;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .danger {
    color: var(--tm-danger);
  }
  .btn-danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
    color: #fff;
  }
  .notice {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    justify-content: space-between;
    padding: 10px 12px;
    margin: 0 0 12px;
    border-radius: 10px;
  }
  .notice p {
    margin: 0;
    flex: 1 1 260px;
  }
  .notice.fail {
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    border: 1px solid var(--tm-danger);
  }
  .error {
    color: var(--tm-danger);
    font-size: 13px;
    margin: 8px 0 0;
  }
  h3 {
    margin: 16px 0 6px;
    font-size: 13.5px;
  }
  .sub {
    margin: 0 0 6px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .outbox {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .outbox li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
  }
  .what {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    font-size: 13px;
  }
  .what small {
    color: var(--tm-muted);
    overflow-wrap: anywhere;
  }
  .what small.bad {
    color: var(--tm-danger);
  }
  .what code {
    font-family: var(--tm-mono);
    font-size: 12px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    margin-bottom: 4px;
  }
  .log {
    margin-top: 12px;
    font-size: 12.5px;
  }
  .log ul {
    list-style: none;
    padding: 0;
    margin: 6px 0 0;
    max-height: 220px;
    overflow: auto;
  }
  .log time {
    color: var(--tm-muted);
    margin-right: 6px;
  }
  .leave p {
    margin: 0 0 8px;
  }
  .leave ul {
    list-style: disc;
    margin: 0 0 10px;
    padding-left: 18px;
    font-size: 13.5px;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    font-size: 13.5px;
    margin-bottom: 6px;
  }
  .warn {
    margin: 0 0 8px;
    padding: 8px 10px;
    border-radius: 10px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    border: 1px solid var(--tm-warn);
  }
</style>
