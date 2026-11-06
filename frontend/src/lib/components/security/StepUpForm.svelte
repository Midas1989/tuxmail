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
    submitLabel: string;
    danger?: boolean;
    busy?: boolean;
    error?: string | null;
    onsubmit: (reauth: Reauth) => void;
    oncancel: () => void;
  }
  let { hasPassword, submitLabel, danger = false, busy = false, error = null, onsubmit, oncancel }: Props = $props();

  let form = $state<HTMLFormElement>();
  let password = $state('');
  let secondFactor = $state('');
  const complete = $derived(reauthComplete({ hasPassword, password, secondFactor }));

  onMount(() => form?.querySelector('input')?.focus());

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!complete || busy) return;
    onsubmit(buildReauth({ hasPassword, password, secondFactor }));
  }
</script>

<form class="stepup" bind:this={form} onsubmit={submit}>
  <p class="muted">{hasPassword ? t('security.stepup.introPassword') : t('security.stepup.intro')}</p>
  {#if hasPassword}
    <label>
      <span class="label">{t('security.stepup.password')}</span>
      <input class="input" type="password" bind:value={password} autocomplete="current-password" />
    </label>
  {/if}
  <label>
    <span class="label">{t('security.stepup.code')}</span>
    <input class="input" bind:value={secondFactor} autocomplete="one-time-code" autocapitalize="off" spellcheck="false" />
    <span class="hint">{t('security.stepup.codeHint')}</span>
  </label>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  <div class="row">
    <button type="submit" class="btn" class:danger class:btn-primary={!danger} disabled={!complete || busy}>
      {#if busy}<LoaderCircle size={14} class="spin" />{/if}
      {submitLabel}
    </button>
    <button type="button" class="btn btn-ghost" onclick={oncancel} disabled={busy}>{t('common.action.cancel')}</button>
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
