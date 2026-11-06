<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Asks for the passphrase of an OpenPGP key. It goes to the server in this one request and stays in no browser storage. -->
<script lang="ts">
  import Modal from '../common/Modal.svelte';
  import { comfort } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { ApiError } from '../../api/api';

  interface Props {
    id: number;
    email?: string;
    onclose: () => void;
    ondone: () => void;
  }
  let { id, email = '', onclose, ondone }: Props = $props();

  let passphrase = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    error = null;
    busy = true;
    try {
      await comfort.api.unlockPgpKey(id, passphrase);
      passphrase = '';
      ondone();
    } catch (err) {
      passphrase = '';
      if (err instanceof ApiError && err.code === 'pgp_wrong_passphrase') error = t('comfort.pgp.err.pgp_wrong_passphrase');
      else if (err instanceof ApiError && err.code === 'pgp_unlock_throttled') error = t('comfort.pgp.err.pgp_unlock_throttled');
      else error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('comfort.pgp.unlockTitle')} {onclose} width="420px">
  <form onsubmit={submit}>
    <p class="hint">{t('comfort.pgp.unlockIntro', { email })}</p>
    <!-- svelte-ignore a11y_autofocus -->
    <input class="input" type="password" autocomplete="off" autofocus bind:value={passphrase} placeholder={t('comfort.pgp.passphrase')} aria-label={t('comfort.pgp.passphrase')} />
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="acts">
      <button type="button" class="btn btn-ghost" onclick={onclose}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={busy || !passphrase}>{t('comfort.pgp.unlock')}</button>
    </div>
  </form>
</Modal>

<style>
  .hint {
    font-size: 13px;
    color: var(--tm-muted);
    margin-top: 0;
  }
  .input {
    width: 100%;
  }
  .error {
    color: #dc2626;
    font-size: 13px;
  }
  .acts {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
  }
</style>
