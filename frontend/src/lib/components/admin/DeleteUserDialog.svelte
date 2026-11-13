<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Delete an account for good. The administrator has to type the user name, and
  chooses whether the user's data directory (mail store, files) stays on the
  server ("keep data") - the account itself is removed either way.
-->
<script lang="ts">
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Modal from '../common/Modal.svelte';
  import StepUpForm from '../security/StepUpForm.svelte';
  import type { Reauth } from '../../api/security';
  import { t } from '../../i18n/index.svelte';
  import type { AdminClient, AdminMe, AdminUser } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';

  interface Props {
    client: AdminClient;
    /** The signed-in administrator (which proofs a step-up asks for). */
    me?: AdminMe;
    user: AdminUser;
    ondeleted: (user: AdminUser, keptData: boolean) => void;
    onclose: () => void;
  }
  let { client, me, user, ondeleted, onclose }: Props = $props();

  let typed = $state('');
  let keepData = $state(false);
  let busy = $state(false);
  let error = $state('');
  const confirmed = $derived(typed.trim() === user.username);

  /** Deleting an administrator needs a fresh proof (M-2). */
  let askProof = $state(false);

  function run(e: SubmitEvent) {
    e.preventDefault();
    if (!confirmed || busy) return;
    if (user.role === 'admin') {
      error = '';
      askProof = true;
      return;
    }
    void remove();
  }

  async function remove(reauth?: Reauth) {
    busy = true;
    error = '';
    try {
      await client.deleteUser(user.id, keepData, reauth);
      ondeleted(user, keepData);
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('admin.delete.title', { name: user.username })} {onclose} width="480px">
  <form id="admin-delete-form" onsubmit={run} class="form">
    <p class="warn"><TriangleAlert size={16} aria-hidden="true" /> {t('admin.delete.warning', { name: user.username })}</p>
    <label class="check"><input type="checkbox" bind:checked={keepData} /> {t('admin.delete.keepData')}</label>
    <p class="hint">{keepData ? t('admin.delete.keepDataOn') : t('admin.delete.keepDataOff')}</p>
    <div>
      <label class="label" for="del-confirm">{t('admin.delete.typeName', { name: user.username })}</label>
      <input id="del-confirm" class="input" bind:value={typed} autocomplete="off" autocapitalize="none" spellcheck="false" data-autofocus data-testid="delete-confirm" />
    </div>
    {#if error && !askProof}<p class="error" role="alert">{error}</p>{/if}
  </form>
  {#if askProof}
    <StepUpForm hasPassword={me?.hasPassword ?? true} hasSecondFactor={me?.totpEnabled ?? false} submitLabel={t('admin.delete.confirm')} danger {busy} {error} onsubmit={(r) => void remove(r)} oncancel={() => ((askProof = false), (error = ''))} />
  {/if}
  {#snippet footer()}
    <button class="btn" type="button" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary danger" type="submit" form="admin-delete-form" disabled={!confirmed || busy || askProof} data-testid="delete-submit">{t('admin.delete.confirm')}</button>
  {/snippet}
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .warn {
    display: flex;
    gap: 8px;
    margin: 0;
    font-size: 14px;
  }
  .warn :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--tm-danger);
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
  }
  .hint {
    margin: -6px 0 0 24px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
    color: #fff;
  }
  .error {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
    font-size: 13px;
  }
</style>
