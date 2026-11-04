<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Portable desktop mode (KONZEPT 15.2): account passwords live in a vault
  protected by a master password. Shown while app.vault.locked; on first use
  the master password is created (with confirmation).
-->
<script lang="ts">
  import LockKeyhole from '@lucide/svelte/icons/lock-keyhole';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app } from '../../state';
  import { ApiError } from '../../api/api';
  import { t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';

  /** Minimum length of a new master password. */
  const MIN_LENGTH = 8;

  const creating = $derived(!app.vault?.initialized);
  let password = $state('');
  let confirm = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    if (creating) {
      if (password.length < MIN_LENGTH) {
        error = t('vault.errShort', { count: MIN_LENGTH });
        return;
      }
      if (password !== confirm) {
        error = t('vault.errMismatch');
        return;
      }
    } else if (!password) {
      error = t('vault.errEmpty');
      return;
    }
    busy = true;
    try {
      await app.unlockVault(password);
      password = confirm = '';
    } catch (err) {
      error = err instanceof ApiError && err.code === 'invalid_password' ? t('vault.errWrong') : err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<!-- The vault cannot be dismissed: without it no account can connect. -->
<Modal title={creating ? t('vault.createTitle') : t('vault.unlockTitle')} onclose={() => {}} closable={false} width="440px">
  <form class="vault" onsubmit={submit}>
    <div class="ic"><LockKeyhole size={34} aria-hidden="true" /></div>
    <p class="lead">{creating ? t('vault.createLead') : t('vault.unlockLead')}</p>
    <label>
      <span class="label">{t('vault.password')}</span>
      <input class="input" type="password" bind:value={password} autocomplete={creating ? 'new-password' : 'current-password'} data-autofocus />
    </label>
    {#if creating}
      <label>
        <span class="label">{t('vault.confirm')}</span>
        <input class="input" type="password" bind:value={confirm} autocomplete="new-password" />
      </label>
      <p class="hint">{t('vault.createHint')}</p>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button type="submit" class="btn btn-primary" disabled={busy}>
      {#if busy}<LoaderCircle size={15} class="spin" />{/if}
      {creating ? t('vault.create') : t('vault.unlock')}
    </button>
  </form>
</Modal>

<style>
  .vault {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .ic {
    display: flex;
    justify-content: center;
    color: var(--tm-accent-text);
  }
  .lead {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13.5px;
    text-align: center;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .hint {
    margin: -4px 0 0;
    font-size: 12px;
    color: var(--tm-faint);
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
