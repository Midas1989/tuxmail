<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> Messenger (KONZEPT 4.4, 11, 18.1): per plugin opt-in with risk
  acknowledgment, process status (running / crashed / restarts, version,
  update hint), restart, plugin settings and the linked accounts.
-->
<script lang="ts">
  import RotateCw from '@lucide/svelte/icons/rotate-cw';
  import Plus from '@lucide/svelte/icons/plus';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { Channel, MessengerAccountState, MessengerPlugin } from '../../types';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import Modal from '../common/Modal.svelte';
  import MessengerWizard from './MessengerWizard.svelte';
  import MessengerComponent from './MessengerComponent.svelte';

  let plugins = $state<MessengerPlugin[]>([]);
  let accountStates = $state<MessengerAccountState[]>([]);
  let edits = $state<Record<string, Record<string, string>>>({});
  let riskFor = $state<MessengerPlugin | null>(null);
  let accepted = $state(false);
  let wizardFor = $state<string | null | undefined>(undefined);
  let unlink = $state<number | null>(null);
  let error = $state<string | null>(null);
  let savedName = $state<string | null>(null);

  const KNOWN: Channel[] = ['whatsapp', 'signal', 'telegram', 'matrix'];
  const channelOf = (p: MessengerPlugin): Channel => (KNOWN.includes(p.channel as Channel) ? (p.channel as Channel) : 'matrix');

  async function load() {
    try {
      const [ps, as] = await Promise.all([app.api.listMessengerPlugins(), app.api.listMessengerAccounts()]);
      plugins = ps;
      accountStates = as;
      for (const p of ps) edits[p.name] ??= Object.fromEntries(p.settingKeys.map((k) => [k, p.settings[k] ?? '']));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  $effect(() => {
    void load();
    return app.api.subscribe((e) => {
      if (e.type === 'messenger.plugin' || e.type === 'account.updated') void load();
    });
  });

  function accountsOf(p: MessengerPlugin) {
    return accountStates
      .filter((s) => s.plugin === p.name)
      .map((s) => ({ s, acc: app.accounts.find((a) => a.id === s.accountId) }))
      .filter((x) => x.acc);
  }

  async function toggle(p: MessengerPlugin) {
    error = null;
    if (!p.enabled && p.needsRiskAck && !p.riskAcceptedAt) {
      riskFor = p;
      accepted = false;
      return;
    }
    try {
      await app.api.updateMessengerPlugin(p.name, { enabled: !p.enabled });
      await load();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function confirmRisk() {
    if (!riskFor) return;
    const name = riskFor.name;
    riskFor = null;
    try {
      await app.api.updateMessengerPlugin(name, { enabled: true, acceptRisk: true });
      await load();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function restart(p: MessengerPlugin) {
    try {
      await app.api.restartMessengerPlugin(p.name);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function saveSettings(p: MessengerPlugin) {
    await app.api.updateMessengerPlugin(p.name, { settings: { ...edits[p.name] } });
    savedName = p.name;
    setTimeout(() => (savedName = null), 2000);
    await load();
  }

  async function doUnlink() {
    if (unlink == null) return;
    const id = unlink;
    unlink = null;
    try {
      await app.api.removeMessengerAccount(id);
      await load();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  function stateLabel(state: string): string {
    const known = ['connected', 'connecting', 'disconnected', 'logged_out', 'error'];
    return known.includes(state) ? t(`messenger.accountState.${state}` as 'messenger.accountState.connected') : state;
  }
</script>

<section class="card wide-card" id="set-messenger" aria-labelledby="set-messenger-h">
  <div class="head">
    <h2 id="set-messenger-h">{t('messenger.title')}</h2>
    <button class="btn btn-primary" onclick={() => (wizardFor = null)}><Plus size={15} /> {t('messenger.add')}</button>
  </div>
  <p class="hint">{t('messenger.intro')}</p>
  {#if error}<p class="alert" role="alert">{error}</p>{/if}
  <ul class="plugins">
    {#each plugins as p (p.name)}
      <li class="plugin" data-plugin={p.name}>
        <div class="row">
          <ChannelIcon channel={channelOf(p)} size={20} badge />
          <div class="title">
            <span class="name">{p.displayName}</span>
            <span class="badges">
              <span class="risk risk-{p.risk}">{t(`messenger.risk.${p.risk}`)}</span>
              {#if p.official}<span class="ok"><ShieldCheck size={12} /> {t('messenger.official')}</span>{/if}
              {#if p.experimental}<span class="risk risk-high">{t('messenger.experimental')}</span>{/if}
            </span>
          </div>
          <span class="state st-{p.status.state}">{t(`messenger.state.${p.status.state}`)}</span>
          <label class="switch">
            <input type="checkbox" checked={p.enabled} disabled={!p.installed} onchange={() => toggle(p)} aria-label={t('messenger.enabled')} />
            <span>{t('messenger.enabled')}</span>
          </label>
          <button class="btn icon" onclick={() => restart(p)} disabled={!p.enabled} title={t('messenger.restart')} aria-label={t('messenger.restart')}><RotateCw size={15} /></button>
        </div>
        <div class="meta">
          {#if !p.installed}
            <span>{t('messenger.notInstalled', { file: `tuxmail-plugin-${p.name}` })}</span>
          {:else}
            {#if p.status.version}<span>{t('messenger.version', { version: p.status.version })}</span>{/if}
            {#if p.status.pid}<span>{t('messenger.pid', { pid: p.status.pid })}</span>{/if}
            <span>{t('messenger.restarts', { count: p.status.restarts })}</span>
            <span>{t('messenger.accounts', { count: p.accounts })}</span>
            {#if p.status.nextRetry && p.status.state === 'crashed'}<span>{t('messenger.nextRetry', { time: fmt().time(p.status.nextRetry) })}</span>{/if}
          {/if}
        </div>
        {#if p.installed && p.component}<MessengerComponent plugin={p} compact />{/if}
        {#if p.status.lastError && (p.status.state === 'crashed' || p.status.state === 'failed')}
          <p class="err"><TriangleAlert size={13} /> {t('messenger.lastError', { error: p.status.lastError })}</p>
        {/if}
        {#if p.status.updateHint}<p class="err">{t('messenger.updateHint', { hint: p.status.updateHint })}</p>{/if}
        {#if p.settingKeys.length && edits[p.name]}
          <details class="settings">
            <summary>{t('messenger.settingsTitle')}</summary>
            <div class="grid">
              {#each p.settingKeys as k (k)}
                <label class="field">
                  <span class="label">{t(`messenger.setting.${k}` as 'messenger.setting.api_id')}</span>
                  <input class="input" type={k === 'api_hash' ? 'password' : 'text'} bind:value={edits[p.name][k]} spellcheck="false" />
                </label>
              {/each}
            </div>
            <button class="btn" onclick={() => saveSettings(p)}>{t('messenger.save')}</button>
            {#if savedName === p.name}<span class="saved" role="status">{t('messenger.saved')}</span>{/if}
          </details>
        {/if}
        {#if accountsOf(p).length}
          <ul class="accounts">
            {#each accountsOf(p) as { s, acc } (s.accountId)}
              <li>
                <span class="dot" style:background={acc?.color}></span>
                <span class="acc">{acc?.displayName}</span>
                <span class="sub">{acc?.extra?.address}</span>
                <span class="astate as-{s.state}">{stateLabel(s.state)}</span>
                <button class="btn small" onclick={() => app.api.syncMessengerAccount(s.accountId)}>{t('messenger.sync')}</button>
                <button class="btn small danger" onclick={() => (unlink = s.accountId)}>{t('messenger.unlink')}</button>
              </li>
            {/each}
          </ul>
        {/if}
        {#if p.enabled}
          <button class="btn link" onclick={() => (wizardFor = p.name)}><Plus size={13} /> {t('messenger.add')}</button>
        {/if}
      </li>
    {/each}
  </ul>
</section>

{#if riskFor}
  <Modal title={t('messenger.riskTitle', { name: riskFor.displayName })} onclose={() => (riskFor = null)}>
    <div class="risk-text">
      <TriangleAlert size={20} />
      <p>{riskFor.name === 'whatsapp' ? t('messenger.riskText.whatsapp') : t('messenger.riskText.generic')}</p>
    </div>
    <label class="chk"><input type="checkbox" bind:checked={accepted} /> {t('messenger.riskAccept', { name: riskFor.displayName })}</label>
    {#snippet footer()}
      <button class="btn" onclick={() => (riskFor = null)}>{t('messenger.wizard.cancel')}</button>
      <button class="btn btn-primary" disabled={!accepted} onclick={confirmRisk}>{t('messenger.riskConfirm', { name: riskFor?.displayName ?? '' })}</button>
    {/snippet}
  </Modal>
{/if}

{#if unlink != null}
  <Modal title={t('messenger.unlink')} onclose={() => (unlink = null)}>
    <p>{t('messenger.unlinkConfirm', { name: app.accounts.find((a) => a.id === unlink)?.displayName ?? '' })}</p>
    {#snippet footer()}
      <button class="btn" onclick={() => (unlink = null)}>{t('messenger.wizard.cancel')}</button>
      <button class="btn btn-primary danger" onclick={doUnlink}>{t('messenger.unlink')}</button>
    {/snippet}
  </Modal>
{/if}

{#if wizardFor !== undefined}
  <MessengerWizard plugin={wizardFor ?? undefined} onclose={() => ((wizardFor = undefined), void load())} />
{/if}

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .hint {
    margin: 4px 0 12px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .plugins,
  .accounts {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .plugin {
    padding: 12px 0;
    border-top: 1px solid var(--tm-line);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .title {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .name {
    font-weight: 650;
    font-size: 14px;
  }
  .badges {
    display: flex;
    gap: 10px;
    font-size: 11.5px;
  }
  .risk {
    color: var(--tm-muted);
  }
  .risk-medium,
  .risk-high {
    color: var(--tm-warning, #b7791f);
    font-weight: 600;
  }
  .ok {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    color: var(--tm-success);
  }
  .state {
    font-size: 12px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--tm-surface-2, rgba(127, 127, 127, 0.12));
    color: var(--tm-muted);
  }
  .st-running {
    color: var(--tm-success);
  }
  .st-crashed,
  .st-failed {
    color: var(--tm-danger, #c53030);
  }
  .switch {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-size: 12.5px;
  }
  .switch input {
    accent-color: var(--tm-accent);
  }
  .icon {
    padding: 4px 8px;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    margin: 6px 0 0 34px;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 6px 0 0 34px;
    font-size: 12px;
    color: var(--tm-danger, #c53030);
  }
  .settings {
    margin: 8px 0 0 34px;
    font-size: 13px;
  }
  .settings summary {
    cursor: pointer;
    color: var(--tm-muted);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px 12px;
    margin: 8px 0;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .label {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .saved {
    margin-left: 8px;
    font-size: 12px;
    color: var(--tm-success);
  }
  .accounts {
    margin: 8px 0 0 34px;
  }
  .accounts li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 0;
    font-size: 13px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .acc {
    font-weight: 600;
  }
  .sub {
    flex: 1;
    color: var(--tm-muted);
    font-size: 12px;
  }
  .astate {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .as-connected {
    color: var(--tm-success);
  }
  .as-error,
  .as-logged_out {
    color: var(--tm-danger, #c53030);
  }
  .small {
    padding: 2px 8px;
    font-size: 12px;
  }
  .danger {
    color: var(--tm-danger, #c53030);
  }
  .btn-primary.danger {
    background: var(--tm-danger, #c53030);
    color: white;
  }
  .link {
    margin: 6px 0 0 30px;
    border: 0;
    background: none;
    color: var(--tm-accent);
    font-size: 12.5px;
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .alert {
    padding: 8px 12px;
    border-radius: 8px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-danger, #c53030) 10%, transparent);
  }
  .risk-text {
    display: flex;
    gap: 10px;
    color: var(--tm-warning, #b7791f);
  }
  .risk-text p {
    margin: 0 0 12px;
    color: var(--tm-text, inherit);
    font-size: 13.5px;
    line-height: 1.45;
  }
  .chk {
    display: flex;
    gap: 8px;
    font-weight: 600;
    font-size: 13.5px;
  }
  @media (max-width: 640px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .row {
      flex-wrap: wrap;
    }
  }

  /* Phones: header button, plugin rows and account rows wrap instead of running out of the card. */
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .head,
    .row,
    .accounts li {
      flex-wrap: wrap;
    }
    .accounts {
      margin-left: 0;
    }
    .meta {
      margin-left: 0;
    }
    .title {
      flex: 1 1 60%;
      min-width: 0;
    }
  }
</style>
