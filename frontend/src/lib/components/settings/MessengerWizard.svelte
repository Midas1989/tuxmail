<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Messenger account wizard (KONZEPT 11, 18.1): choose service -> risk notice
  (WhatsApp) -> pairing method -> QR code / pairing code / login code with
  live updates (ApiEvent messenger.pairing) -> done.
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Channel, MessengerPairing, MessengerPairingMethod, MessengerPlugin } from '../../types';
  import Modal from '../common/Modal.svelte';
  import QrCode from '../common/QrCode.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  interface Props {
    onclose: () => void;
    /** Preselect a plugin (from the Messenger settings). */
    plugin?: string;
  }
  let { onclose, plugin: initial }: Props = $props();

  type Step = 'choose' | 'risk' | 'method' | 'pairing';
  let step = $state<Step>('choose');
  let plugins = $state<MessengerPlugin[]>([]);
  let selected = $state<MessengerPlugin | null>(null);
  let method = $state<MessengerPairingMethod>('qr');
  let accepted = $state(false);
  let error = $state<string | null>(null);
  let busy = $state(false);

  let displayName = $state('');
  let phone = $state('');
  let homeserver = $state('');
  let username = $state('');
  let password = $state('');
  let network = $state('whatsapp');
  let bridgeAccountId = $state(0);
  let answer = $state('');

  let pairing = $state<MessengerPairing | null>(null);
  let now = $state(Date.now());

  const KNOWN: Channel[] = ['whatsapp', 'signal', 'telegram', 'matrix'];
  const channelOf = (p: MessengerPlugin): Channel => (KNOWN.includes(p.channel as Channel) ? (p.channel as Channel) : 'matrix');
  const matrixAccounts = $derived(app.accounts.filter((a) => a.kind === 'matrix' || a.kind === 'matrix_bridge'));
  const seconds = $derived(pairing?.expiresAt ? Math.max(0, Math.round((Date.parse(pairing.expiresAt) - now) / 1000)) : 0);
  const done = $derived(pairing?.state === 'success');
  const failed = $derived(pairing?.state === 'error' || pairing?.state === 'cancelled');
  const needsPhone = $derived(method === 'phone');
  const needsLogin = $derived(method === 'password');
  const scanHint = $derived.by(() => {
    if (!selected) return '';
    if (method === 'bridge') return t('messenger.wizard.scanHint.bridge', { network: t(`common.channel.${network}` as 'common.channel.whatsapp') });
    const ch = selected.channel;
    return ch === 'whatsapp' || ch === 'signal' || ch === 'telegram' ? t(`messenger.wizard.scanHint.${ch}`) : '';
  });

  $effect(() => {
    void app.api.listMessengerPlugins().then((ps) => {
      plugins = ps;
      if (initial) {
        const p = ps.find((x) => x.name === initial);
        if (p) choose(p);
      }
    });
    const off = app.api.subscribe((e) => {
      if (e.type === 'messenger.pairing' && pairing && e.pairing.id === pairing.id) pairing = e.pairing;
    });
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => {
      off();
      clearInterval(tick);
    };
  });

  // Polling fallback when no event transport is connected.
  $effect(() => {
    const id = pairing?.id;
    if (!id || done || failed) return;
    const iv = setInterval(() => {
      void app.api
        .getMessengerPairing(id)
        .then((p) => {
          if (pairing?.id === p.id && p.state !== pairing.state) pairing = p;
        })
        .catch(() => {});
    }, 2500);
    return () => clearInterval(iv);
  });

  function choose(p: MessengerPlugin) {
    selected = p;
    error = null;
    method = p.pairingMethods[0] ?? 'qr';
    if (!p.enabled) {
      step = 'risk';
      accepted = !p.needsRiskAck || !!p.riskAcceptedAt;
    } else {
      step = 'method';
    }
    if (p.name === 'matrix') homeserver = p.settings.default_homeserver ?? '';
    if (matrixAccounts.length) bridgeAccountId = matrixAccounts[0].id;
  }

  async function enable() {
    if (!selected) return;
    busy = true;
    error = null;
    try {
      selected = await app.api.updateMessengerPlugin(selected.name, { enabled: true, acceptRisk: selected.needsRiskAck ? accepted : undefined });
      step = 'method';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function start() {
    if (!selected) return;
    busy = true;
    error = null;
    try {
      pairing = await app.api.startMessengerPairing({
        plugin: selected.name,
        method,
        displayName: displayName || undefined,
        phone: needsPhone ? phone : undefined,
        homeserver: selected.name === 'matrix' ? homeserver : undefined,
        username: needsLogin ? username : undefined,
        password: needsLogin ? password : undefined,
        network: method === 'bridge' ? network : undefined,
        bridgeAccountId: method === 'bridge' ? bridgeAccountId : undefined,
      });
      step = 'pairing';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function submit() {
    if (!pairing?.field) return;
    const field = pairing.field;
    busy = true;
    try {
      await app.api.submitMessengerPairingInput(pairing.id, field, answer);
      answer = '';
      pairing = { ...pairing, state: 'waiting', field: undefined };
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function cancel() {
    if (pairing && !done && !failed) await app.api.cancelMessengerPairing(pairing.id).catch(() => {});
    onclose();
  }

  function retry() {
    pairing = null;
    step = 'method';
  }

  function inputLabel(field: string): string {
    const known = ['code', 'password', 'phone', 'homeserver', 'username', 'token', 'confirm'];
    return known.includes(field) ? t(`messenger.wizard.input.${field}` as 'messenger.wizard.input.code') : field;
  }
</script>

<Modal title={t('messenger.wizard.title')} onclose={cancel} width="600px">
  {#if step === 'choose'}
    <p class="lead">{t('messenger.wizard.chooseService')}</p>
    <div class="services">
      {#each plugins as p (p.name)}
        <button class="service" onclick={() => choose(p)} disabled={!p.installed} data-plugin={p.name}>
          <ChannelIcon channel={channelOf(p)} size={22} badge />
          <span class="sname">{p.displayName}</span>
          <span class="risk risk-{p.risk}">{t(`messenger.risk.${p.risk}`)}</span>
          {#if p.official}<span class="tag"><ShieldCheck size={12} /> {t('messenger.official')}</span>{/if}
          {#if !p.installed}<span class="sub">{t('messenger.state.missing')}</span>{/if}
        </button>
      {/each}
    </div>
    <p class="note">{t('messenger.wizard.sameChats')}</p>
  {:else if step === 'risk' && selected}
    <div class="risk-box" class:warn={selected.needsRiskAck}>
      {#if selected.needsRiskAck}<TriangleAlert size={20} />{/if}
      <div>
        <h3>{t('messenger.riskTitle', { name: selected.displayName })}</h3>
        {#if selected.needsRiskAck}
          <p>{selected.name === 'whatsapp' ? t('messenger.riskText.whatsapp') : t('messenger.riskText.generic')}</p>
          <label class="chk"><input type="checkbox" bind:checked={accepted} data-testid="risk-accept" /> {t('messenger.riskAccept', { name: selected.displayName })}</label>
        {:else}
          <p>{t('messenger.wizard.disabledNote')}</p>
        {/if}
      </div>
    </div>
  {:else if step === 'method' && selected}
    <div class="head-line"><ChannelIcon channel={channelOf(selected)} size={18} label /></div>
    {#if selected.status.updateHint}<p class="alert">{selected.name === 'telegram' ? t('messenger.wizard.telegramCredentials') : selected.status.updateHint}</p>{/if}
    <div class="seg" role="radiogroup" aria-label={t('messenger.wizard.title')}>
      {#each selected.pairingMethods as m (m)}
        <button role="radio" aria-checked={method === m} onclick={() => (method = m)}>{t(`messenger.wizard.method.${m}`)}</button>
      {/each}
    </div>
    <div class="form">
      {#if needsPhone}
        <label class="field"><span class="label">{t('messenger.wizard.phone')}</span><input class="input" type="tel" bind:value={phone} autocomplete="tel" data-autofocus /></label>
      {/if}
      {#if selected.name === 'matrix' && method !== 'bridge'}
        <label class="field"><span class="label">{t('messenger.wizard.homeserver')}</span><input class="input" bind:value={homeserver} placeholder={'matrix.org'} spellcheck="false" /></label>
      {/if}
      {#if needsLogin}
        <label class="field"><span class="label">{t('messenger.wizard.username')}</span><input class="input" bind:value={username} autocomplete="username" /></label>
        <label class="field"><span class="label">{t('messenger.wizard.password')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" /></label>
      {/if}
      {#if method === 'bridge'}
        {#if matrixAccounts.length === 0}
          <p class="alert">{t('messenger.wizard.noBridgeAccount')}</p>
        {:else}
          <label class="field"><span class="label">{t('messenger.wizard.bridgeAccount')}</span>
            <select class="input" bind:value={bridgeAccountId}>
              {#each matrixAccounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
            </select>
          </label>
          <label class="field"><span class="label">{t('messenger.wizard.network')}</span>
            <select class="input" bind:value={network}>
              {#each ['whatsapp', 'signal', 'telegram'] as n (n)}<option value={n}>{t(`common.channel.${n}` as 'common.channel.whatsapp')}</option>{/each}
            </select>
          </label>
        {/if}
      {:else}
        <label class="field"><span class="label">{t('messenger.wizard.displayName')}</span><input class="input" bind:value={displayName} /></label>
      {/if}
    </div>
    {#if selected.name === 'signal'}<p class="note">{t('messenger.wizard.signalHistory')}</p>{/if}
    {#if selected.name !== 'matrix'}<p class="note">{t('messenger.wizard.sameChats')}</p>{/if}
  {:else if step === 'pairing' && selected && pairing}
    <div class="pairing" data-state={pairing.state} aria-live="polite">
      {#if pairing.state === 'qr' && pairing.qr}
        <QrCode data={pairing.qr} size={248} label={t('messenger.wizard.qrLabel')} />
        <div class="explain">
          <h3>{t('messenger.wizard.scan', { name: method === 'bridge' ? t(`common.channel.${network}` as 'common.channel.whatsapp') : selected.displayName })}</h3>
          <p class="steps">{scanHint}</p>
          {#if seconds > 0}<p class="sub">{t('messenger.wizard.expires', { seconds })}</p>{/if}
        </div>
      {:else if pairing.state === 'code' && pairing.code}
        <div class="code" aria-label={t('messenger.wizard.code')}>{pairing.code}</div>
        <div class="explain">
          <h3>{t('messenger.wizard.code')}</h3>
          <p class="steps">{t('messenger.wizard.codeHint')}</p>
          {#if seconds > 0}<p class="sub">{t('messenger.wizard.expires', { seconds })}</p>{/if}
        </div>
      {:else if pairing.state === 'input' && pairing.field}
        <form class="ask" onsubmit={(e) => (e.preventDefault(), submit())}>
          <label class="field">
            <span class="label">{inputLabel(pairing.field)}</span>
            <input class="input" type={pairing.field === 'password' ? 'password' : 'text'} bind:value={answer} data-autofocus inputmode={pairing.field === 'code' ? 'numeric' : undefined} />
          </label>
          {#if pairing.message}<p class="sub">{pairing.message}</p>{/if}
          <button class="btn btn-primary" type="submit" disabled={busy || !answer}>{t('messenger.wizard.submit')}</button>
        </form>
      {:else if done}
        <div class="result ok"><CircleCheck size={36} /><p>{t('messenger.wizard.success', { name: selected.displayName })}</p></div>
      {:else if pairing.state === 'error'}
        <div class="result bad"><TriangleAlert size={32} /><p>{t('messenger.wizard.error', { message: pairing.message ?? '' })}</p></div>
      {:else if pairing.state === 'cancelled'}
        <div class="result bad"><p>{t('messenger.wizard.cancelled')}</p></div>
      {:else}
        <div class="result"><LoaderCircle size={30} class="spin" /><p>{t('messenger.wizard.waiting', { name: selected.displayName })}</p>{#if pairing.message}<p class="sub">{pairing.message}</p>{/if}</div>
      {/if}
    </div>
  {/if}
  {#if error}<p class="alert" role="alert">{error}</p>{/if}

  {#snippet footer()}
    {#if step === 'risk'}
      <button class="btn" onclick={() => (step = 'choose')}>{t('messenger.wizard.back')}</button>
      <button class="btn btn-primary" disabled={busy || !accepted} onclick={enable}>{t('messenger.riskConfirm', { name: selected?.displayName ?? '' })}</button>
    {:else if step === 'method'}
      <button class="btn" onclick={() => (step = 'choose')}>{t('messenger.wizard.back')}</button>
      <button class="btn btn-primary" disabled={busy || (needsPhone && !phone) || (needsLogin && (!username || !password)) || (method === 'bridge' && !bridgeAccountId)} onclick={start}>{t('messenger.wizard.start')}</button>
    {:else if step === 'pairing'}
      {#if done}
        <button class="btn btn-primary" onclick={onclose}>{t('messenger.wizard.done')}</button>
      {:else if failed}
        <button class="btn" onclick={onclose}>{t('messenger.wizard.cancel')}</button>
        <button class="btn btn-primary" onclick={retry}>{t('messenger.wizard.retry')}</button>
      {:else}
        <button class="btn" onclick={cancel}>{t('messenger.wizard.cancel')}</button>
      {/if}
    {:else}
      <button class="btn" onclick={onclose}>{t('messenger.wizard.cancel')}</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .lead {
    margin: 0 0 12px;
    font-size: 14px;
  }
  .services {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .service {
    display: grid;
    grid-template-columns: auto 1fr;
    grid-template-rows: auto auto;
    gap: 4px 10px;
    align-items: center;
    text-align: left;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg, 12px);
    background: var(--tm-surface);
    color: inherit;
    cursor: pointer;
  }
  .service:hover:not(:disabled) {
    border-color: var(--tm-accent);
  }
  .service:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .service :global(.ch) {
    grid-row: span 3;
  }
  .sname {
    font-weight: 650;
    font-size: 14px;
  }
  .risk,
  .tag,
  .sub {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .tag {
    display: inline-flex;
    gap: 4px;
    align-items: center;
    color: var(--tm-success);
  }
  .risk-medium,
  .risk-high {
    color: var(--tm-warning, #b7791f);
    font-weight: 600;
  }
  .note {
    margin: 12px 0 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .risk-box {
    display: flex;
    gap: 12px;
    padding: 14px 16px;
    border-radius: 12px;
    border: 1px solid var(--tm-line);
  }
  .risk-box.warn {
    border-color: var(--tm-warning, #b7791f);
    background: color-mix(in srgb, var(--tm-warning, #b7791f) 9%, transparent);
    color: inherit;
  }
  .risk-box h3,
  .explain h3 {
    margin: 0 0 6px;
    font-size: 15px;
  }
  .risk-box p {
    margin: 0 0 10px;
    font-size: 13.5px;
    line-height: 1.45;
  }
  .chk {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    font-size: 13.5px;
    font-weight: 600;
  }
  .chk input {
    margin-top: 2px;
    accent-color: var(--tm-accent);
  }
  .head-line {
    margin-bottom: 10px;
  }
  .seg {
    display: inline-flex;
    flex-wrap: wrap;
    border: 1px solid var(--tm-line);
    border-radius: 999px;
    padding: 2px;
    margin-bottom: 12px;
  }
  .seg button {
    border: 0;
    background: none;
    color: inherit;
    padding: 6px 12px;
    border-radius: 999px;
    font-size: 13px;
    cursor: pointer;
  }
  .seg button[aria-checked='true'] {
    background: var(--tm-accent);
    color: white;
  }
  .form {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 14px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .label {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .pairing {
    display: flex;
    gap: 22px;
    align-items: center;
    min-height: 260px;
  }
  .explain {
    flex: 1;
  }
  .steps {
    font-size: 13.5px;
    line-height: 1.5;
    margin: 0 0 8px;
  }
  .code {
    font-family: ui-monospace, monospace;
    font-size: 34px;
    font-weight: 700;
    letter-spacing: 0.12em;
    padding: 18px 22px;
    border-radius: 12px;
    border: 1px dashed var(--tm-line);
  }
  .ask {
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: 100%;
    max-width: 340px;
    margin: 0 auto;
  }
  .result {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: center;
    font-size: 14px;
  }
  .result.ok {
    color: var(--tm-success);
  }
  .result.bad {
    color: var(--tm-danger, #c53030);
  }
  .alert {
    margin: 10px 0 0;
    padding: 8px 12px;
    border-radius: 8px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-danger, #c53030) 10%, transparent);
  }
  :global(.spin) {
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (max-width: 560px) {
    .services,
    .form {
      grid-template-columns: 1fr;
    }
    .pairing {
      flex-direction: column;
    }
  }
</style>
