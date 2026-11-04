<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Add a shared/delegated Microsoft 365 or Exchange mailbox (KONZEPT 7.3a). -->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Account, SendMode } from '../../types';
  import Modal from '../common/Modal.svelte';

  interface Props {
    accounts: Account[];
    onclose: () => void;
    onadded: (acc: Account) => void;
  }
  let { accounts, onclose, onadded }: Props = $props();

  let accountId = $state(0);
  $effect(() => {
    if (!accountId && accounts[0]) accountId = accounts[0].id;
  });
  let address = $state('');
  let displayName = $state('');
  let sendMode = $state<SendMode | ''>('send_as');
  let busy = $state(false);
  let error = $state<string | null>(null);
  const modes: (SendMode | '')[] = ['send_as', 'send_on_behalf', ''];

  async function submit() {
    error = null;
    busy = true;
    try {
      const acc = await app.api.addSharedMailbox({ accountId, address: address.trim(), sendMode, displayName: displayName.trim() || undefined });
      onadded(acc);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('cloud.shared.add')} {onclose} width="480px">
  {#if accounts.length === 0}
    <p class="hint">{t('cloud.shared.noAccounts')}</p>
  {:else}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <p class="hint">{t('cloud.shared.intro')}</p>
      <label>
        <span class="label">{t('cloud.shared.account')}</span>
        <select class="input" bind:value={accountId}>
          {#each accounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
        </select>
      </label>
      <label><span class="label">{t('cloud.shared.address')}</span><input class="input" type="email" bind:value={address} required data-autofocus /></label>
      <label><span class="label">{t('cloud.shared.name')}</span><input class="input" bind:value={displayName} /></label>
      <fieldset>
        <legend>{t('cloud.shared.sendMode')}</legend>
        {#each modes as m (m)}
          <label class="radio">
            <input type="radio" name="sendmode" value={m} bind:group={sendMode} />
            <span>
              {m ? t(`cloud.sendMode.${m}`) : t('cloud.shared.none')}
              {#if m}<small>{t(`cloud.sendMode.hint.${m}`)}</small>{/if}
            </span>
          </label>
        {/each}
      </fieldset>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={onclose}>{t('common.action.cancel')}</button>
        <button type="submit" class="btn btn-primary" disabled={busy || !address}>
          {#if busy}<LoaderCircle size={15} class="spin" />{/if}
          {t('cloud.shared.add')}
        </button>
      </div>
    </form>
  {/if}
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-faint);
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 8px 12px;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .radio {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    font-size: 13.5px;
  }
  .radio small {
    display: block;
    color: var(--tm-faint);
    font-size: 12px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
