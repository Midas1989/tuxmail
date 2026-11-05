<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2026 Midas Wollinger and TuxMail contributors -->
<!--
  First start of the Android app: "Server verbinden" (KONZEPT 15.8/15.9).
  1. server address -> GET /healthz + /api/auth/config
  2. sign in via device flow (Custom Tab on verification_uri_complete, polling
     /api/device/token) or directly with username/password/TOTP
     (/api/device/login).
  The device token goes to secure storage (ProfileStore -> Android Keystore).
-->
<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Server from '@lucide/svelte/icons/server';
  import Trash from '@lucide/svelte/icons/trash-2';
  import penguinUrl from '$branding/tuxmail-penguin.png';
  import { t, type MessageKey } from '../i18n/index.svelte';
  import type { ProfileStore, ServerProfile } from './profiles';
  import {
    ServerError,
    directLogin,
    pollDeviceToken,
    safeVerificationUrl,
    startDeviceAuthorization,
    validateServer,
    type DeviceGrant,
    type ServerInfo,
    type TokenResult,
  } from './server';

  interface Props {
    store: ProfileStore;
    profiles: ServerProfile[];
    deviceName: string;
    /** Server URL to prefill (re-login after 401, tuxmail://connect link). */
    prefill?: string;
    /** Show the "session ended" notice for this server. */
    expired?: boolean;
    fromLink?: boolean;
    openUrl: (url: string) => Promise<void>;
    closeBrowser?: () => Promise<void>;
    ondone: (profile: ServerProfile) => void;
  }
  let { store, profiles, deviceName, prefill = '', expired = false, fromLink = false, openUrl, closeBrowser, ondone }: Props = $props();

  type Step = 'url' | 'choose' | 'device' | 'password';
  // Initial values only: later prop changes do not reset the form.
  const initial = untrack(() => ({ prefill, profiles }));
  let step = $state<Step>('url');
  let urlInput = $state(initial.prefill.replace(/^https:\/\//, ''));
  let saved = $state<ServerProfile[]>(initial.profiles);
  let info = $state<ServerInfo | null>(null);
  let grant = $state<DeviceGrant | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let username = $state('');
  let password = $state('');
  let totp = $state('');
  let needTotp = $state(false);
  let abort: AbortController | null = null;

  onDestroy(() => abort?.abort());

  function message(err: unknown): string {
    if (err instanceof ServerError) {
      const key = `native.error.${err.code}` as MessageKey;
      return t(key, { message: err.message });
    }
    return t('native.error.server_error', { message: err instanceof Error ? err.message : String(err) });
  }

  async function check(e?: Event) {
    e?.preventDefault();
    error = null;
    busy = true;
    abort?.abort();
    abort = new AbortController();
    try {
      info = await validateServer(urlInput, fetch, abort.signal);
      step = 'choose';
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }

  async function finish(res: TokenResult) {
    if (!info) return;
    const profile = await store.upsert(
      {
        serverUrl: info.serverUrl,
        username: res.user?.username ?? (username || undefined),
        displayName: res.user?.displayName,
        sessionId: res.sessionId,
        serverVersion: info.version,
      },
      res.token,
    );
    await closeBrowser?.().catch(() => {});
    ondone(profile);
  }

  async function startDevice() {
    if (!info) return;
    error = null;
    busy = true;
    abort?.abort();
    abort = new AbortController();
    try {
      grant = await startDeviceAuthorization(info.serverUrl, deviceName, fetch, abort.signal);
      step = 'device';
      busy = false;
      await openPage();
      const res = await pollDeviceToken(info.serverUrl, grant, { signal: abort.signal });
      await finish(res);
    } catch (err) {
      if (!(err instanceof ServerError && err.code === 'aborted')) error = message(err);
      busy = false;
    }
  }

  async function openPage() {
    if (!info || !grant) return;
    const url = safeVerificationUrl(info.serverUrl, grant.verificationUriComplete);
    if (url) await openUrl(url).catch(() => {});
  }

  async function submitPassword(e: Event) {
    e.preventDefault();
    if (!info) return;
    error = null;
    busy = true;
    try {
      const res = await directLogin(info.serverUrl, { username, password, totpCode: needTotp ? totp : undefined, deviceName });
      await finish(res);
    } catch (err) {
      if (err instanceof ServerError && err.code === 'mfa_required' && !needTotp) {
        needTotp = true;
      } else {
        error = message(err);
      }
    } finally {
      busy = false;
    }
  }

  function back() {
    abort?.abort();
    error = null;
    grant = null;
    needTotp = false;
    totp = '';
    step = step === 'device' || step === 'password' ? 'choose' : 'url';
  }

  async function useProfile(p: ServerProfile) {
    urlInput = p.serverUrl.replace(/^https:\/\//, '');
    username = p.username ?? '';
    if (!p.needsLogin && (await store.token(p.id))) {
      await store.activate(p);
      ondone(p);
      return;
    }
    await check();
  }

  async function removeProfile(p: ServerProfile) {
    await store.remove(p.id);
    saved = saved.filter((x) => x.id !== p.id);
  }
</script>

<div class="connect">
  <div class="card">
    <img class="brand" src={penguinUrl} alt="" width="96" height="122" />
    <h1>{t('native.connect.title')}</h1>

    {#if expired && step === 'url'}
      <p class="notice" role="status">{t('native.connect.expired', { server: initial.prefill })}</p>
    {:else if fromLink && step === 'url'}
      <p class="notice" role="status">{t('native.connect.linkPrefilled')}</p>
    {/if}

    {#if step === 'url'}
      <form class="form" onsubmit={check}>
        <p class="lead">{t('native.connect.lead')}</p>
        <label class="label" for="server-url">{t('onboarding.connect.serverUrl')}</label>
        <div class="url">
          <span class="scheme" aria-hidden="true">https://</span>
          <input
            id="server-url"
            class="input"
            type="url"
            inputmode="url"
            autocapitalize="off"
            autocomplete="url"
            spellcheck="false"
            placeholder={t('native.connect.placeholder')}
            bind:value={urlInput}
            required
          />
        </div>
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <button class="btn btn-primary wide" type="submit" disabled={busy || !urlInput.trim()}>
          {#if busy}<LoaderCircle size={16} class="spin" />{t('native.connect.checking')}{:else}{t('native.connect.continue')}{/if}
        </button>
      </form>

      {#if saved.length}
        <section class="profiles" aria-label={t('native.connect.profiles')}>
          <h2>{t('native.connect.profiles')}</h2>
          <ul>
            {#each saved as p (p.id)}
              <li>
                <button class="btn btn-ghost grow" onclick={() => void useProfile(p)}>
                  <Server size={16} />
                  <span class="truncate">{t('native.connect.useProfile', { server: p.label })}</span>
                </button>
                <button class="icon-btn" aria-label={t('native.connect.removeProfile', { server: p.label })} title={t('native.connect.removeProfile', { server: p.label })} onclick={() => void removeProfile(p)}>
                  <Trash size={16} />
                </button>
              </li>
            {/each}
          </ul>
        </section>
      {/if}
    {:else if step === 'choose' && info}
      <div class="form">
        <p class="ok">{t('native.connect.serverFound', { version: info.version, server: info.serverUrl.replace(/^https:\/\//, '') }).replace(/\s{2,}/g, ' ')}</p>
        <button class="btn btn-primary wide" onclick={() => void startDevice()} disabled={busy}>
          {#if busy}<LoaderCircle size={16} class="spin" />{:else}<ExternalLink size={16} />{/if}
          {t('native.connect.signInBrowser')}
        </button>
        <p class="hint">{t('native.connect.browserHint')}</p>
        <button class="btn wide" onclick={() => (step = 'password')} disabled={busy}><KeyRound size={16} />{t('onboarding.connect.withPassword')}</button>
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <button class="btn btn-ghost wide" onclick={back}>{t('native.connect.changeServer')}</button>
      </div>
    {:else if step === 'device' && grant && info}
      <div class="form">
        <p class="lead">{t('onboarding.connect.deviceLead', { server: info.serverUrl.replace(/^https:\/\//, '') })}</p>
        <output class="code" aria-label={t('onboarding.connect.userCode')}>{grant.userCode}</output>
        <p class="waiting"><LoaderCircle size={16} class="spin" />{t('onboarding.connect.waiting')}</p>
        <button class="btn btn-primary wide" onclick={() => void openPage()}><ExternalLink size={16} />{t('native.connect.openPage')}</button>
        <p class="hint">{t('onboarding.connect.orVisit')} <span class="mono">{grant.verificationUri}</span></p>
        {#if error}
          <p class="error" role="alert">{error}</p>
          <button class="btn wide" onclick={() => void startDevice()}>{t('onboarding.connect.restart')}</button>
        {/if}
        <button class="btn btn-ghost wide" onclick={back}>{t('common.action.back')}</button>
      </div>
    {:else if step === 'password' && info}
      <form class="form" onsubmit={submitPassword}>
        <p class="lead">{t('onboarding.connect.loginLead', { server: info.serverUrl.replace(/^https:\/\//, '') })}</p>
        <label class="label" for="login-user">{t('onboarding.connect.username')}</label>
        <input id="login-user" class="input" autocomplete="username" autocapitalize="off" spellcheck="false" bind:value={username} required />
        <label class="label" for="login-pass">{t('onboarding.account.password')}</label>
        <input id="login-pass" class="input" type="password" autocomplete="current-password" bind:value={password} required />
        {#if needTotp}
          <p class="hint">{t('native.connect.totpHint')}</p>
          <label class="label" for="login-totp">{t('onboarding.connect.totp')}</label>
          <!-- svelte-ignore a11y_autofocus -->
          <input id="login-totp" class="input mono" inputmode="numeric" autocomplete="one-time-code" maxlength="12" bind:value={totp} required autofocus />
        {/if}
        {#if error}<p class="error" role="alert">{error}</p>{/if}
        <button class="btn btn-primary wide" type="submit" disabled={busy || !username || !password}>
          {#if busy}<LoaderCircle size={16} class="spin" />{/if}{t('onboarding.connect.signIn')}
        </button>
        <button class="btn btn-ghost wide" type="button" onclick={back}>{t('common.action.back')}</button>
      </form>
    {/if}
  </div>
</div>

<style>
  .connect {
    position: fixed;
    inset: 0;
    overflow-y: auto;
    background: var(--tm-bg);
    padding: calc(24px + env(safe-area-inset-top)) 16px calc(24px + env(safe-area-inset-bottom));
    display: flex;
    justify-content: center;
    align-items: flex-start;
  }
  .card {
    width: 100%;
    max-width: 420px;
    margin: auto 0;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .brand {
    display: block;
    width: 96px;
    height: auto;
    margin: 0 auto;
  }
  h1 {
    margin: 0;
    text-align: center;
    font-size: 22px;
    color: var(--tm-brand-navy);
  }
  :global([data-theme='dark']) h1 {
    color: var(--tm-fg);
  }
  h2 {
    margin: 8px 0 4px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .lead,
  .hint {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13.5px;
  }
  .hint {
    font-size: 12.5px;
  }
  .notice {
    margin: 0;
    padding: 10px 12px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-brand) 16%, var(--tm-surface));
    font-size: 13px;
  }
  .ok {
    margin: 0;
    color: var(--tm-success);
    font-size: 13.5px;
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .url {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .scheme {
    color: var(--tm-faint);
    font-family: var(--tm-mono);
    font-size: 13px;
  }
  .input {
    height: 44px;
    font-size: 16px; /* no zoom on focus */
  }
  .wide {
    width: 100%;
    height: 44px;
    justify-content: center;
    font-size: 14px;
  }
  .code {
    align-self: center;
    font-family: var(--tm-mono);
    font-size: 32px;
    font-weight: 700;
    letter-spacing: 0.12em;
    user-select: all;
  }
  .waiting {
    margin: 0;
    display: flex;
    gap: 8px;
    align-items: center;
    justify-content: center;
    color: var(--tm-muted);
  }
  .mono {
    font-family: var(--tm-mono);
    word-break: break-all;
  }
  .profiles ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .profiles li {
    display: flex;
    gap: 4px;
    align-items: center;
  }
  .grow {
    flex: 1;
    min-width: 0;
    height: 44px;
    justify-content: flex-start;
  }
  .truncate {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .icon-btn {
    width: 44px;
    height: 44px;
  }
  :global(.spin) {
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
