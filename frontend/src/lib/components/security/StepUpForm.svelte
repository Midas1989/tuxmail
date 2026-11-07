<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Re-authentication before a sensitive change (turn two-factor authentication
  off, create new recovery codes): the server demands the password (accounts
  that have one) AND a second factor (authenticator code or recovery code).
  Wrong answers are counted per user by the server; after a few the step-up is
  locked (429 throttled) for a while.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import type { Reauth } from '../../api/security';
  import { t } from '../../i18n/index.svelte';
  import { buildReauth, reauthComplete } from '../../security';

  interface Props {
    hasPassword: boolean;
    /** False: the account has no second factor to ask for (backup key of an administrator without one). */
    hasSecondFactor?: boolean;
    submitLabel: string;
    danger?: boolean;
    busy?: boolean;
    error?: string | null;
    /** End of a server lock (ms since epoch, from the 429 Retry-After): the field stays, the button waits and frees itself. */
    lockUntil?: number | null;
    /** Accounts without a password prove themselves by a recent sign-in: offered with an error so the form never ends without a control. */
    resignin?: () => void;
    onsubmit: (reauth: Reauth) => void;
    oncancel?: () => void;
  }
  let { hasPassword, hasSecondFactor = true, submitLabel, danger = false, busy = false, error = null, lockUntil = null, resignin, onsubmit, oncancel }: Props = $props();

  let now = $state(Date.now());
  const remaining = $derived(lockUntil ? Math.max(0, lockUntil - now) : 0);
  const locked = $derived(remaining > 0);
  $effect(() => {
    if (!lockUntil) return;
    now = Date.now();
    const id = setInterval(() => {
      now = Date.now();
      if (now >= lockUntil) clearInterval(id);
    }, 250);
    return () => clearInterval(id);
  });
  const remainingText = $derived(
    remaining >= 60_000 ? t('security.stepup.lockedMinutes', { minutes: Math.ceil(remaining / 60_000) }) : t('security.stepup.lockedSeconds', { seconds: Math.max(1, Math.ceil(remaining / 1000)) }),
  );

  let form = $state<HTMLFormElement>();
  let password = $state('');
  let secondFactor = $state('');
  const complete = $derived(hasSecondFactor ? reauthComplete({ hasPassword, password, secondFactor }) : !hasPassword || password.length > 0);

  onMount(() => form?.querySelector('input')?.focus());

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!complete || busy || locked) return;
    onsubmit(buildReauth({ hasPassword, password, secondFactor }));
  }
</script>

<form class="stepup" bind:this={form} onsubmit={submit}>
  <p class="muted">
    {#if !hasSecondFactor}{hasPassword ? t('security.stepup.introPasswordOnly') : t('security.stepup.introRecentLogin')}
    {:else}{hasPassword ? t('security.stepup.introPassword') : t('security.stepup.intro')}{/if}
  </p>
  {#if hasPassword}
    <label>
      <span class="label">{t('security.stepup.password')}</span>
      <input class="input" type="password" bind:value={password} autocomplete="current-password" />
    </label>
  {/if}
  {#if hasSecondFactor}
    <label>
      <span class="label">{t('security.stepup.code')}</span>
      <input class="input" bind:value={secondFactor} autocomplete="one-time-code" autocapitalize="off" spellcheck="false" />
      <span class="hint">{t('security.stepup.codeHint')}</span>
    </label>
  {/if}
  {#if locked}<p class="error" role="alert" data-testid="stepup-locked">{remainingText}</p>
  {:else if error && !lockUntil}<p class="error" role="alert">{error}</p>{/if}
  <div class="row">
    <button type="submit" class="btn" class:danger class:btn-primary={!danger} disabled={!complete || busy || locked}>
      {#if busy}<LoaderCircle size={14} class="spin" />{/if}
      {submitLabel}
    </button>
    {#if resignin && !hasPassword && error}<button type="button" class="btn" onclick={resignin}>{t('security.stepup.resignin')}</button>{/if}
    {#if oncancel}<button type="button" class="btn btn-ghost" onclick={oncancel} disabled={busy}>{t('common.action.cancel')}</button>{/if}
  </div>
</form>

<style>
  .stepup {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-width: 420px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .muted {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .hint {
    font-size: 12px;
    color: var(--tm-faint);
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
  .btn.danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
    color: #fff;
  }
  .btn.danger:hover:not(:disabled) {
    background: color-mix(in srgb, var(--tm-danger) 88%, black);
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
