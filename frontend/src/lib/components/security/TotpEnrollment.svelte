<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Set up two-factor authentication (TOTP) in three steps:
    1. scan the QR code (otpauth URL) or type the secret,
    2. confirm with the first 6-digit code,
    3. recovery codes, shown once; the finish button needs the confirmation
       "I have stored them".
  Used by the card in Settings -> Security (TwoFactorCard) and by the
  blocking dialog for administrators (MfaSetupDialog). `onfinished` is called
  only after step 3 was confirmed; the parent reloads its state.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import type { Api } from '../../api/api';
  import type { TotpEnrollment } from '../../api/security';
  import { t } from '../../i18n/index.svelte';
  import { formatSecret, sanitizeTotpCode, securityErrorMessage } from '../../security';
  import QrCode from '../common/QrCode.svelte';
  import RecoveryCodes from './RecoveryCodes.svelte';

  interface Props {
    api: Api;
    onfinished: () => void;
    /** Leave before the codes are shown (hidden once TOTP is active: from then on only "finish" is possible). */
    oncancel?: () => void;
  }
  let { api, onfinished, oncancel }: Props = $props();

  type Step = 'loading' | 'scan' | 'verify' | 'codes';
  let step = $state<Step>('loading');
  let enrollment = $state<TotpEnrollment | null>(null);
  let code = $state('');
  let codes = $state<string[]>([]);
  let stored = $state(false);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let secretCopied = $state(false);
  let codeInput = $state<HTMLInputElement>();

  const stepNumber = $derived(step === 'loading' || step === 'scan' ? 1 : step === 'verify' ? 2 : 3);

  async function begin() {
    error = null;
    step = 'loading';
    try {
      enrollment = await api.totpSetup();
      step = 'scan';
    } catch (err) {
      error = securityErrorMessage(err, t);
    }
  }

  async function verify(e: SubmitEvent) {
    e.preventDefault();
    if (code.length !== 6 || busy) return;
    busy = true;
    error = null;
    try {
      codes = await api.totpConfirm(code);
      code = '';
      step = 'codes';
    } catch (err) {
      error = securityErrorMessage(err, t);
      code = '';
      codeInput?.focus();
    } finally {
      busy = false;
    }
  }

  async function copySecret() {
    try {
      await navigator.clipboard.writeText(enrollment?.secret ?? '');
      secretCopied = true;
      setTimeout(() => (secretCopied = false), 3000);
    } catch {
      /* the key is shown as text and can be selected */
    }
  }

  $effect(() => {
    if (step === 'verify') codeInput?.focus();
  });

  onMount(() => void begin());

  // The recovery codes are shown only once: warn before the page is left.
  function onbeforeunload(e: BeforeUnloadEvent) {
    if (step === 'codes' && !stored) e.preventDefault();
  }
</script>

<svelte:window {onbeforeunload} />

<div class="enroll" data-step={step}>
  <p class="steps" aria-live="polite">{t('security.setup.step', { current: stepNumber, total: 3 })}</p>

  {#if step === 'loading'}
    {#if error}
      <p class="error" role="alert">{error}</p>
      <div class="row">
        <button type="button" class="btn" onclick={() => void begin()}>{t('security.setup.retry')}</button>
        {#if oncancel}<button type="button" class="btn btn-ghost" onclick={oncancel}>{t('common.action.cancel')}</button>{/if}
      </div>
    {:else}
      <p class="muted"><LoaderCircle size={15} class="spin" /> {t('security.setup.preparing')}</p>
    {/if}
  {:else if step === 'scan' && enrollment}
    <h3>{t('security.setup.scanTitle')}</h3>
    <p class="muted">{t('security.setup.scanText')}</p>
    <div class="qr"><QrCode data={enrollment.otpauthUrl} size={188} label={t('security.setup.qrLabel')} /></div>
    <p class="muted">{t('security.setup.manual')}</p>
    <div class="secret">
      <code data-testid="totp-secret">{formatSecret(enrollment.secret)}</code>
      <button type="button" class="btn" onclick={() => void copySecret()}>
        {#if secretCopied}<Check size={14} />{:else}<Copy size={14} />{/if}
        {secretCopied ? t('security.codes.copied') : t('security.setup.copyKey')}
      </button>
    </div>
    <div class="row">
      <button type="button" class="btn btn-primary" data-autofocus onclick={() => (step = 'verify')}>{t('security.setup.next')}</button>
      {#if oncancel}<button type="button" class="btn btn-ghost" onclick={oncancel}>{t('common.action.cancel')}</button>{/if}
    </div>
  {:else if step === 'verify'}
    <h3>{t('security.setup.verifyTitle')}</h3>
    <p class="muted">{t('security.setup.verifyText')}</p>
    <form onsubmit={verify}>
      <label>
        <span class="label">{t('security.setup.codeLabel')}</span>
        <input
          class="input code"
          bind:this={codeInput}
          value={code}
          oninput={(e) => {
            code = sanitizeTotpCode(e.currentTarget.value);
            e.currentTarget.value = code;
          }}
          inputmode="numeric"
          autocomplete="one-time-code"
          maxlength="7"
          placeholder="123456"
          aria-invalid={error ? 'true' : undefined}
        />
      </label>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row">
        <button type="submit" class="btn btn-primary" disabled={code.length !== 6 || busy}>
          {#if busy}<LoaderCircle size={14} class="spin" />{/if}
          {t('security.setup.verify')}
        </button>
        <button type="button" class="btn btn-ghost" onclick={() => ((step = 'scan'), (error = null))} disabled={busy}>{t('common.action.back')}</button>
      </div>
    </form>
  {:else if step === 'codes'}
    <h3>{t('security.setup.codesTitle')}</h3>
    <p class="muted">{t('security.setup.codesText')}</p>
    <RecoveryCodes {codes} bind:confirmed={stored} />
    <div class="row">
      <button type="button" class="btn btn-primary" disabled={!stored} onclick={onfinished}>{t('security.setup.finish')}</button>
    </div>
  {/if}
</div>

<style>
  .enroll {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .steps {
    margin: 0;
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    color: var(--tm-faint);
  }
  h3 {
    margin: 0;
    font-size: 14.5px;
    font-weight: 650;
  }
  .muted {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .qr {
    display: flex;
    justify-content: center;
    padding: 4px 0;
  }
  .secret {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .secret code {
    flex: 1 1 auto;
    min-width: 0;
    padding: 6px 10px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-sm);
    background: var(--tm-surface-2);
    font-family: var(--tm-mono);
    font-size: 13.5px;
    letter-spacing: 0.06em;
    overflow-wrap: anywhere;
    user-select: all;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .input.code {
    max-width: 180px;
    font-family: var(--tm-mono);
    font-size: 18px;
    letter-spacing: 0.25em;
    text-align: center;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .error {
    margin: 0;
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
