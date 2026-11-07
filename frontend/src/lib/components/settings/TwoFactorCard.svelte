<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> Security: two-factor authentication (TOTP) of the web client.
  Shows the status and offers: enable (QR code -> first code -> recovery codes,
  shown once), new recovery codes and disable (both re-authenticate with the
  password, if the account has one, and a second factor, as the server demands).
  Rendered by SecuritySettings only where the account API exists (server mode
  in the browser).
-->
<script lang="ts">
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import ShieldOff from '@lucide/svelte/icons/shield-off';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { onMount } from 'svelte';
  import { app, security } from '../../state';
  import type { Api } from '../../api/api';
  import type { AuthMe, Reauth } from '../../api/security';
  import { t } from '../../i18n/index.svelte';
  import { lockUntilOf, securityErrorMessage } from '../../security';
  import TotpEnrollment from '../security/TotpEnrollment.svelte';
  import RecoveryCodes from '../security/RecoveryCodes.svelte';
  import StepUpForm from '../security/StepUpForm.svelte';

  interface Props {
    /** Defaults to the application API. */
    api?: Api;
  }
  let { api: apiProp }: Props = $props();
  const api = $derived(apiProp ?? app.api);

  type View = 'status' | 'disable' | 'regenerate' | 'codes';
  let view = $state<View>('status');
  let me = $state<AuthMe | null>(null);
  let loadError = $state<string | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let lockUntil = $state<number | null>(null);
  let codes = $state<string[]>([]);
  let stored = $state(false);

  /** Warn below this number of unused recovery codes. */
  const LOW = 3;

  const enabled = $derived(!!me?.user.totpEnabled);
  const left = $derived(me?.recoveryCodesLeft ?? 0);

  async function load() {
    loadError = null;
    try {
      me = await api.getAuthMe();
    } catch (err) {
      loadError = securityErrorMessage(err, t);
    }
  }

  onMount(() => void load());

  function show(v: View) {
    error = null;
    lockUntil = null;
    view = v;
  }

  async function enrolled() {
    view = 'status';
    await load();
    void security.refresh();
    app.toast(t('security.twofactor.enabledToast'));
  }

  async function disable(reauth: Reauth) {
    busy = true;
    error = null;
    try {
      await api.totpDisable(reauth);
      view = 'status';
      await load();
      void security.refresh(); // an administrator is asked to set it up again right away
      app.toast(t('security.twofactor.disabledToast'));
    } catch (err) {
      error = securityErrorMessage(err, t);
      lockUntil = lockUntilOf(err);
    } finally {
      busy = false;
    }
  }

  async function regenerate(reauth: Reauth) {
    busy = true;
    error = null;
    try {
      codes = await api.regenerateRecoveryCodes(reauth);
      stored = false;
      view = 'codes';
      void load();
    } catch (err) {
      error = securityErrorMessage(err, t);
      lockUntil = lockUntilOf(err);
    } finally {
      busy = false;
    }
  }

  function codesDone() {
    codes = [];
    stored = false;
    view = 'status';
    app.toast(t('security.twofactor.codesToast'));
  }
</script>

<section class="card" id="set-twofactor" aria-labelledby="set-twofactor-h">
  <h2 id="set-twofactor-h">{t('security.twofactor.title')}</h2>
  <p class="intro">{t('security.twofactor.intro')}</p>

  {#if loadError}
    <p class="error" role="alert">{loadError}</p>
    <button class="btn" onclick={() => void load()}>{t('security.setup.retry')}</button>
  {:else if !me}
    <p class="muted"><LoaderCircle size={15} class="spin" /> {t('security.twofactor.loading')}</p>
  {:else}
    <p class="state" class:on={enabled} data-testid="twofactor-state">
      {#if enabled}<ShieldCheck size={14} />{:else}<ShieldOff size={14} />{/if}
      {enabled ? t('security.twofactor.on') : t('security.twofactor.off')}
    </p>

    {#if view === 'status'}
      {#if enabled}
        <p class="codes-left" class:low={left < LOW} data-testid="codes-left">
          {left === 0 ? t('security.twofactor.noCodesLeft') : t('security.twofactor.codesLeft', { count: left })}
          {#if left < LOW}<span> {t('security.twofactor.codesLow')}</span>{/if}
        </p>
        <div class="row">
          <button class="btn" onclick={() => show('regenerate')}><KeyRound size={14} /> {t('security.twofactor.regenerate')}</button>
          <button class="btn" onclick={() => show('disable')}><ShieldOff size={14} /> {t('security.twofactor.disable')}</button>
        </div>
      {:else}
        {#if me.mfaSetupRequired}<p class="required" role="status">{t('security.twofactor.required')}</p>{/if}
        <div class="panel">
          <TotpEnrollment {api} hasPassword={me.user.hasPassword} onfinished={() => void enrolled()} />
        </div>
      {/if}
    {:else if view === 'regenerate'}
      <div class="panel">
        <h3>{t('security.twofactor.regenerateTitle')}</h3>
        <p class="muted">{t('security.twofactor.regenerateText')}</p>
        <StepUpForm hasPassword={me.user.hasPassword} submitLabel={t('security.twofactor.regenerateSubmit')} {busy} {error} {lockUntil} onsubmit={(r) => void regenerate(r)} oncancel={() => show('status')} />
      </div>
    {:else if view === 'disable'}
      <div class="panel">
        <h3>{t('security.twofactor.disableTitle')}</h3>
        <p class="muted">{t('security.twofactor.disableText')}</p>
        <StepUpForm hasPassword={me.user.hasPassword} submitLabel={t('security.twofactor.disableSubmit')} danger {busy} {error} {lockUntil} onsubmit={(r) => void disable(r)} oncancel={() => show('status')} />
      </div>
    {:else if view === 'codes'}
      <div class="panel">
        <h3>{t('security.twofactor.newCodesTitle')}</h3>
        <p class="muted">{t('security.twofactor.newCodesText')}</p>
        <RecoveryCodes {codes} bind:confirmed={stored} />
        <div class="row">
          <button class="btn btn-primary" disabled={!stored} onclick={codesDone}>{t('security.setup.finish')}</button>
        </div>
      </div>
    {/if}
  {/if}
</section>

<style>
  .state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 10px;
    padding: 3px 10px;
    border-radius: 99px;
    font-size: 12.5px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .state.on {
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  .muted {
    margin: 0 0 10px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .codes-left {
    margin: 0 0 12px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .codes-left.low {
    color: var(--tm-warn);
  }
  .required {
    margin: 0 0 12px;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-warn) 12%, var(--tm-surface));
    font-size: 13px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .panel {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px 16px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2);
    max-width: 560px;
  }
  .panel h3 {
    margin: 0;
    font-size: 14.5px;
    font-weight: 650;
  }
  .panel .muted {
    margin: 0;
  }
  .error {
    margin: 0 0 10px;
    color: var(--tm-danger);
    font-size: 13px;
  }
  :global(.spin) {
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
