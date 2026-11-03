<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  KONZEPT 12.2: there is no fixed default - the client asks on every send
  with a cloud link how the share password should be delivered.
-->
<script lang="ts" module>
  export interface MessengerTarget {
    accountId: number;
    channel: 'whatsapp' | 'signal' | 'telegram';
    label: string;
  }
</script>

<script lang="ts">
  import KeyRound from '@lucide/svelte/icons/key-round';
  import type { PasswordDelivery, PasswordDeliveryMode } from '../../types';
  import { t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  interface Props {
    files: string[];
    recipientName: string;
    messengerTargets: MessengerTarget[];
    onconfirm: (d: PasswordDelivery) => void;
    oncancel: () => void;
  }
  let { files, recipientName, messengerTargets, onconfirm, oncancel }: Props = $props();

  let mode = $state<PasswordDeliveryMode | null>(null);
  let messengerAccountId = $state<number | null>(null);
  let remember = $state(false);

  const fileLabel = $derived(files.length === 1 ? files[0] : t('sendcheck.password.nFiles', { count: files.length }));

  function confirm() {
    if (!mode) return;
    onconfirm({ mode, messengerAccountId: mode === 'messenger' ? (messengerAccountId ?? undefined) : undefined, rememberForRecipient: remember });
  }
</script>

<Modal title={t('sendcheck.password.title')} onclose={oncancel} width="480px">
  <div class="intro">
    <span class="ic"><KeyRound size={18} /></span>
    <p>{t('sendcheck.password.intro', { files: fileLabel })}</p>
  </div>
  <fieldset>
    <legend class="sr-only">{t('sendcheck.password.title')}</legend>
    <label class="opt">
      <input type="radio" name="pw" value="inline" bind:group={mode} data-autofocus />
      <span><b>{t('sendcheck.password.inline')}</b><small>{t('sendcheck.password.inlineHint')}</small></span>
    </label>
    <label class="opt">
      <input type="radio" name="pw" value="separate_mail" bind:group={mode} />
      <span><b>{t('sendcheck.password.separate')}</b><small>{t('sendcheck.password.separateHint')}</small></span>
    </label>
    {#if messengerTargets.length}
      {#each messengerTargets as m (m.accountId)}
        <label class="opt">
          <input
            type="radio"
            name="pw"
            value="messenger"
            checked={mode === 'messenger' && messengerAccountId === m.accountId}
            onchange={() => {
              mode = 'messenger';
              messengerAccountId = m.accountId;
            }}
          />
          <span>
            <b class="row"><ChannelIcon channel={m.channel} size={14} /> {t('sendcheck.password.messenger', { channel: m.label, name: recipientName })}</b>
            <small>{t('sendcheck.password.messengerHint')}</small>
          </span>
        </label>
      {/each}
    {:else}
      <div class="opt disabled" aria-disabled="true">
        <input type="radio" disabled />
        <span><b>{t('sendcheck.password.messengerGeneric')}</b><small>{t('sendcheck.password.messengerUnknown', { name: recipientName })}</small></span>
      </div>
    {/if}
    <label class="opt">
      <input type="radio" name="pw" value="none" bind:group={mode} />
      <span><b>{t('sendcheck.password.none')}</b><small>{t('sendcheck.password.noneHint')}</small></span>
    </label>
  </fieldset>
  <label class="remember">
    <input type="checkbox" bind:checked={remember} />
    {t('sendcheck.password.remember', { name: recipientName })}
  </label>

  {#snippet footer()}
    <button class="btn" onclick={oncancel}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" disabled={!mode} onclick={confirm}>{t('sendcheck.password.sendNow')}</button>
  {/snippet}
</Modal>

<style>
  .intro {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    margin-bottom: 12px;
  }
  .intro p {
    margin: 0;
    font-size: 14px;
  }
  .ic {
    flex: none;
    display: inline-flex;
    padding: 8px;
    border-radius: 10px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  fieldset {
    border: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .opt {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    cursor: pointer;
    transition:
      border-color var(--tm-dur),
      background var(--tm-dur);
  }
  .opt:hover {
    background: var(--tm-surface-2);
  }
  .opt:has(input:checked) {
    border-color: var(--tm-accent);
    background: var(--tm-accent-soft);
  }
  .opt.disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .opt input {
    margin-top: 3px;
    accent-color: var(--tm-accent);
  }
  .opt span {
    display: flex;
    flex-direction: column;
  }
  .opt b {
    font-weight: 600;
    font-size: 13.5px;
  }
  .opt b.row {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
  .opt small {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .remember {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    font-size: 13px;
  }
  .remember input {
    accent-color: var(--tm-accent);
  }
</style>
