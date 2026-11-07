<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  "Mit TuxMail-Server verbinden": desktop auto-setup against a TuxMail server.
  Device flow (RFC 8628): POST /api/device/authorize -> user confirms in the
  browser -> poll POST /api/device/token. Alternative: direct login with
  username, password and TOTP. The core keeps all tokens; the UI only sees
  the provisioning profile and decides the mode and what to set up.
  Also used for "sign in again" (purpose reauth) and for changing to the mode
  "Auf diesem Gerät" (purpose switchLocal). It always talks to the local core.
-->
<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Copy from '@lucide/svelte/icons/copy';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import Server from '@lucide/svelte/icons/server';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import { app } from '../../state';
  import { connection, type ConnectionWizard } from '../../state/connection.svelte';
  import { t } from '../../i18n/index.svelte';
  import { normalizeServerUrl, openExternal } from '../../platform';
  import type { ClientMode, DeviceAuthorization, ProvisioningProfile } from '../../types';
  import Modal from '../common/Modal.svelte';
  import ServerModeChoice from './ServerModeChoice.svelte';
  import logoUrl from '$branding/tuxmail-logo.png';

  interface Props {
    purpose?: ConnectionWizard;
    onclose: () => void;
  }
  let { purpose = { purpose: 'connect' }, onclose }: Props = $props();
  // Fixed for the life of the wizard: it is opened anew for another purpose.
  // svelte-ignore state_referenced_locally
  const start = purpose;
  const reauth = start.purpose === 'reauth';
  const switching = start.purpose === 'switchLocal';
  // The sign-in belongs to this device, also while the interface uses the server.
  const core = connection.available ? connection.localCore() : app.api;

  type Step = 'url' | 'device' | 'login' | 'mode' | 'profile' | 'importing' | 'done';
  let step = $state<Step>('url');
  let urlInput = $state('https://');
  let serverUrl = $state(start.purpose === 'connect' ? '' : start.serverUrl);
  let error = $state<string | null>(null);
  let busy = $state(false);

  // device flow
  let auth = $state<DeviceAuthorization | null>(null);
  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let expiresAt = 0;
  let cancelled = false;

  // login
  let username = $state('');
  let password = $state('');
  let totp = $state('');

  // profile
  let profile = $state<ProvisioningProfile | null>(null);
  let selAccounts = $state<number[]>([]);
  let selIdentities = $state<number[]>([]);
  let selSignatures = $state<number[]>([]);
  let selWorkflows = $state<string[]>([]);
  let mode = $state<ClientMode>(switching ? 'local' : 'thin');
  const pop3 = $derived(!!profile?.accounts.some((a) => a.kind === 'pop3'));
  let finished = false;

  onDestroy(() => {
    cancelled = true;
    if (pollTimer) clearTimeout(pollTimer);
    // Left in the middle: the new device entry at the server must not stay behind.
    if (!finished && !switching && serverUrl && connection.available) void connection.abortConnect(serverUrl).catch(() => {});
  });

  onMount(() => {
    if (reauth) void startDevice();
    if (switching) {
      busy = true;
      connection.serverProfile().then(
        (p) => {
          busy = false;
          gotProfile(p);
        },
        (err) => {
          busy = false;
          error = err instanceof Error ? err.message : String(err);
        },
      );
    }
  });

  function validUrl(): boolean {
    const u = normalizeServerUrl(urlInput);
    if (!u) {
      error = t('onboarding.connect.errUrl');
      return false;
    }
    serverUrl = u;
    error = null;
    return true;
  }

  async function startDevice() {
    if (!reauth && !validUrl()) return;
    busy = true;
    try {
      auth = await core.deviceAuthorize(serverUrl);
      expiresAt = Date.now() + auth.expiresIn * 1000;
      step = 'device';
      schedulePoll(auth.interval);
    } catch (err) {
      error = t('onboarding.connect.errReach', { error: err instanceof Error ? err.message : String(err) });
    } finally {
      busy = false;
    }
  }

  function schedulePoll(seconds: number) {
    if (pollTimer) clearTimeout(pollTimer);
    pollTimer = setTimeout(() => void poll(seconds), seconds * 1000);
  }

  async function poll(interval: number) {
    if (cancelled || !auth || step !== 'device') return;
    if (Date.now() > expiresAt) {
      error = t('onboarding.connect.expired');
      return;
    }
    try {
      const r = await core.deviceToken(serverUrl, auth.deviceCode);
      if (cancelled || step !== 'device') return;
      switch (r.status) {
        case 'approved':
          if (r.profile) gotProfile(r.profile);
          return;
        case 'pending':
          schedulePoll(interval);
          return;
        case 'slow_down':
          schedulePoll(interval + 5);
          return;
        case 'denied':
          error = t('onboarding.connect.denied');
          return;
        case 'expired':
          error = t('onboarding.connect.expired');
          return;
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      schedulePoll(interval * 2);
    }
  }

  async function login() {
    if (!serverUrl && !validUrl()) return;
    busy = true;
    error = null;
    try {
      const r = await core.serverLogin(serverUrl, { username, password, totp: totp || undefined });
      if (r.status === 'approved' && r.profile) gotProfile(r.profile);
      else error = r.error ?? t('onboarding.connect.denied');
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  function toLogin() {
    if (!reauth && !validUrl()) return;
    if (pollTimer) clearTimeout(pollTimer);
    error = null;
    step = 'login';
  }

  function gotProfile(p: ProvisioningProfile) {
    if (pollTimer) clearTimeout(pollTimer);
    profile = p;
    selAccounts = p.accounts.map((a) => a.id);
    selIdentities = p.identities.map((i) => i.id);
    selSignatures = p.signatures.map((s) => s.id);
    selWorkflows = p.workflows.map((w) => w.id);
    error = null;
    if (reauth) {
      void finishReauth();
      return;
    }
    step = switching ? 'profile' : 'mode';
  }

  async function finishReauth() {
    step = 'importing';
    try {
      await connection.reauthComplete(serverUrl);
      finished = true;
      await connection.refresh();
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      step = 'login';
    }
  }

  function chooseMode() {
    error = null;
    if (mode === 'thin') void doImport();
    else step = 'profile';
  }

  async function doImport() {
    if (!profile) return;
    step = 'importing';
    error = null;
    try {
      await core.importProvisioning({
        serverUrl,
        mode,
        // "Über den Server": nothing is copied to this device.
        accountIds: mode === 'thin' ? [] : selAccounts,
        identityIds: selIdentities.filter((id) => selAccounts.includes(profile!.identities.find((i) => i.id === id)?.accountId ?? -1)),
        signatureIds: selSignatures,
        workflowIds: selWorkflows,
      });
      finished = true;
      void connection.refresh();
      step = 'done';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      step = mode === 'thin' ? 'mode' : 'profile';
    }
  }

  /** The interface picks its way to the data when it starts. */
  function closeDone() {
    if (connection.available) connection.reloadApp();
    else onclose();
  }

  async function copyCode() {
    if (!auth) return;
    try {
      await navigator.clipboard.writeText(auth.userCode);
      app.toast(t('onboarding.connect.codeCopied'));
    } catch {
      /* clipboard blocked */
    }
  }
</script>

<Modal title={reauth ? t('connection.reauth') : switching ? t('connection.changeMode') : t('onboarding.connect.title')} {onclose} width="620px">
  {#if switching && busy}
    <p class="waiting" role="status"><LoaderCircle size={15} class="spin" /> {t('connection.connecting')}</p>
  {:else if step === 'url' && !reauth && !switching}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void startDevice();
      }}
    >
      <img class="brand" src={logoUrl} alt="TuxMail" width="120" height="120" />
      <p class="lead">{t('onboarding.connect.lead')}</p>
      <label>
        <span class="label">{t('onboarding.connect.serverUrl')}</span>
        <input class="input" type="text" inputmode="url" autocapitalize="off" spellcheck="false" bind:value={urlInput} placeholder="https://mail.example.de" data-autofocus />
      </label>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={toLogin}><KeyRound size={15} /> {t('onboarding.connect.withPassword')}</button>
        <button type="submit" class="btn btn-primary" disabled={busy}>
          {#if busy}<LoaderCircle size={15} class="spin" />{/if}
          {t('onboarding.account.next')}
        </button>
      </div>
    </form>
  {:else if step === 'device' && auth}
    <div class="device">
      <p class="lead">{t('onboarding.connect.deviceLead', { server: serverUrl })}</p>
      <div class="code" aria-label={t('onboarding.connect.userCode')}>{auth.userCode}</div>
      <div class="device-actions">
        <button class="btn btn-primary" onclick={() => openExternal(auth!.verificationUriComplete)} data-autofocus>
          <ExternalLink size={15} /> {t('onboarding.connect.confirmInBrowser')}
        </button>
        <button class="btn" onclick={copyCode}><Copy size={15} /> {t('onboarding.connect.copyCode')}</button>
      </div>
      <p class="hint">{t('onboarding.connect.orVisit')} <code>{auth.verificationUri}</code></p>
      {#if error}
        <p class="error" role="alert">{error}</p>
        <button class="btn" onclick={startDevice}>{t('onboarding.connect.restart')}</button>
      {:else}
        <p class="waiting" role="status"><LoaderCircle size={15} class="spin" /> {t('onboarding.connect.waiting')}</p>
      {/if}
      <div class="row-actions">
        <button class="btn btn-ghost" onclick={toLogin}><KeyRound size={15} /> {t('onboarding.connect.withPassword')}</button>
      </div>
    </div>
  {:else if step === 'login'}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void login();
      }}
    >
      <p class="lead">{t('onboarding.connect.loginLead', { server: serverUrl })}</p>
      <label><span class="label">{t('onboarding.connect.username')}</span><input class="input" bind:value={username} autocomplete="username" data-autofocus /></label>
      <label><span class="label">{t('onboarding.account.password')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" /></label>
      <label>
        <span class="label">{t('onboarding.connect.totp')}</span>
        <input class="input totp" bind:value={totp} inputmode="numeric" autocomplete="one-time-code" maxlength="6" pattern="[0-9]*" placeholder="123456" />
      </label>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        {#if !reauth}<button type="button" class="btn btn-ghost" onclick={() => ((step = 'url'), (error = null))}>{t('common.action.back')}</button>{/if}
        <button type="submit" class="btn btn-primary" disabled={busy}>
          {#if busy}<LoaderCircle size={15} class="spin" />{/if}
          {t('onboarding.connect.signIn')}
        </button>
      </div>
    </form>
  {:else if step === 'mode' && profile}
    <div class="profile">
      <p class="ok"><CircleCheck size={18} /> {t('onboarding.connect.connected', { name: profile.server.name, user: profile.user.email || profile.user.name })}</p>
      <ServerModeChoice bind:mode {pop3} />
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
        <button class="btn btn-primary" onclick={chooseMode} data-autofocus>{mode === 'thin' ? t('connection.connectNow') : t('onboarding.account.next')}</button>
      </div>
    </div>
  {:else if step === 'importing' && (mode === 'thin' || reauth)}
    <p class="waiting" role="status"><LoaderCircle size={15} class="spin" /> {t('connection.connecting')}</p>
  {:else if (step === 'profile' || step === 'importing') && profile}
    <div class="profile">
      <p class="ok"><CircleCheck size={18} /> {t('onboarding.connect.connected', { name: profile.server.name, user: profile.user.email })}</p>
      <p class="lead">{t('onboarding.connect.chooseImport')}</p>

      <fieldset>
        <legend>{t('settings.accounts')}</legend>
        {#each profile.accounts as a (a.id)}
          <label class="check"><input type="checkbox" value={a.id} bind:group={selAccounts} /> <span class="dot" style:background={a.color}></span> {a.displayName} <small>{a.extra?.address} · {t(`settings.protocol.${a.kind}` as 'settings.protocol.imap')}</small></label>
        {/each}
      </fieldset>
      <fieldset>
        <legend>{t('onboarding.connect.identities')}</legend>
        {#each profile.identities as i (i.id)}
          <label class="check" class:disabled={!selAccounts.includes(i.accountId)}>
            <input type="checkbox" value={i.id} bind:group={selIdentities} disabled={!selAccounts.includes(i.accountId)} />
            {i.name} <small>&lt;{i.email}&gt;</small>
          </label>
        {/each}
      </fieldset>
      <fieldset>
        <legend>{t('onboarding.connect.signatures')}</legend>
        {#each profile.signatures as s (s.id)}
          <label class="check"><input type="checkbox" value={s.id} bind:group={selSignatures} /> {s.name}</label>
        {/each}
      </fieldset>
      <fieldset>
        <legend>{t('onboarding.connect.workflows')}</legend>
        {#each profile.workflows as w (w.id)}
          <label class="check"><input type="checkbox" value={w.id} bind:group={selWorkflows} /> {w.name} {#if w.description}<small>{w.description}</small>{/if}</label>
        {/each}
      </fieldset>

      {#if pop3}<p class="warn" role="alert" data-testid="pop3-warning">{t('connection.mode.pop3')}</p>{/if}
      <p class="hint">{t('connection.passwordsNote')}</p>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
        <button class="btn btn-primary" onclick={doImport} disabled={step === 'importing'}>
          {#if step === 'importing'}<LoaderCircle size={15} class="spin" />{:else}<Server size={15} />{/if}
          {t('onboarding.connect.import')}
        </button>
      </div>
    </div>
  {:else if switching && error}
    <p class="error" role="alert">{error}</p>
    <div class="row-actions"><button class="btn" onclick={onclose}>{t('common.action.close')}</button></div>
  {:else if step === 'done'}
    <div class="done">
      <CircleCheck size={40} />
      <p>{mode === 'thin' ? t('onboarding.connect.doneThin') : t('onboarding.connect.doneLocal')}</p>
      <button class="btn btn-primary" onclick={closeDone} data-autofocus>{t('common.action.close')}</button>
    </div>
  {/if}
</Modal>

<style>
  .brand {
    display: block;
    width: 120px;
    height: 120px;
    margin: 0 auto 4px;
    border-radius: 18px;
  }
  .form,
  .device,
  .profile {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .lead {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13.5px;
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .code {
    align-self: center;
    font-family: var(--tm-mono);
    font-size: 34px;
    font-weight: 700;
    letter-spacing: 0.12em;
    padding: 12px 24px;
    border-radius: 14px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    user-select: all;
  }
  .device-actions {
    display: flex;
    justify-content: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .device .hint {
    text-align: center;
  }
  .waiting {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    margin: 0;
    color: var(--tm-muted);
    font-size: 13px;
  }
  .totp {
    width: 140px;
    font-family: var(--tm-mono);
    letter-spacing: 0.2em;
  }
  .ok {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--tm-success);
    font-weight: 600;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 8px 12px 10px;
    margin: 0;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 3px 0;
    font-size: 13.5px;
    flex-wrap: wrap;
  }
  .check small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  .check.disabled {
    opacity: 0.5;
  }
  .check input {
    accent-color: var(--tm-accent);
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 9px;
  }
  .warn {
    margin: 0;
    padding: 8px 10px;
    border-radius: 10px;
    font-size: 12.5px;
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    border: 1px solid var(--tm-warn);
  }
  .done {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 16px;
    color: var(--tm-success);
    text-align: center;
  }
  .done p {
    color: var(--tm-fg);
    margin: 0;
  }
</style>
