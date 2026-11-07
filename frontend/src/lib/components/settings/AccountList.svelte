<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Konten: list of all accounts (opens AccountEditDialog with
  identities, server, folders and advanced options) and the wizards for
  new mail and messenger accounts.
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { messengerChannel, type Account } from '../../types';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import AccountWizard from './AccountWizard.svelte';
  import MessengerWizard from './MessengerWizard.svelte';
  import AccountEditDialog from './AccountEditDialog.svelte';

  /** Account whose edit dialog is open ("Konto bearbeiten"). */
  let editAccountId = $state<number | null>(null);
  let wizardOpen = $state(false);
  let messengerWizardOpen = $state(false);

  function server(a: Account): string {
    if (messengerChannel(a.kind)) return a.extra?.address ?? '';
    const i = a.incoming;
    return i ? `${i.host}:${i.port} · ${t(`settings.tls.${i.tls}`)}` : '';
  }
</script>

<section class="card" id="set-accounts" aria-labelledby="set-accounts-h">
  <div class="card-head">
    <h2 id="set-accounts-h">{t('settings.accounts')}</h2>
    <span class="acc-actions">
      <button class="btn" onclick={() => (messengerWizardOpen = true)}><MessageCircle size={15} /> {t('messenger.add')}</button>
      <button class="btn btn-primary" onclick={() => (wizardOpen = true)}><Plus size={15} /> {t('settings.addAccount')}</button>
    </span>
  </div>
  <ul class="accounts">
    {#each app.accounts as a (a.id)}
      <li>
        <button class="acc-row" aria-label={t('settings.accountEdit.open', { name: a.displayName })} onclick={() => (editAccountId = a.id)}>
          <span class="dot" style:background={a.color}></span>
          <div class="acc">
            <span class="name">
              {a.displayName}
              {#if messengerChannel(a.kind)}<ChannelIcon channel={messengerChannel(a.kind)!} size={13} />{/if}
            </span>
            <span class="sub">{a.extra?.address} · {server(a)}</span>
          </div>
          <span class="kind">{t(`settings.protocol.${a.kind}` as 'settings.protocol.imap')}</span>
          <span class="state" class:on={a.enabled}>{a.enabled ? t('settings.active') : t('settings.inactive')}</span>
          <ChevronRight size={15} class="chev" />
        </button>
      </li>
    {/each}
  </ul>
</section>

{#if wizardOpen}
  <AccountWizard onclose={() => (wizardOpen = false)} />
{/if}
{#if messengerWizardOpen}
  <MessengerWizard onclose={() => (messengerWizardOpen = false)} />
{/if}
{#if editAccountId !== null}
  <AccountEditDialog accountId={editAccountId} onclose={() => (editAccountId = null)} />
{/if}

<style>
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 8px;
  }
  .card-head h2 {
    margin: 0;
  }
  .acc-actions {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .accounts {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .accounts li {
    border-top: 1px solid var(--tm-line);
  }
  .acc-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 10px 4px;
    background: none;
    border: none;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: 8px;
  }
  .acc-row:hover,
  .acc-row:focus-visible {
    background: var(--tm-surface-2, var(--tm-surface-3));
  }
  .acc-row :global(.chev) {
    color: var(--tm-faint);
    flex: none;
  }
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 99px;
    flex: none;
  }
  .acc {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .name {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-weight: 600;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .kind {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .state {
    font-size: 11px;
    padding: 1px 8px;
    border-radius: 99px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
    white-space: nowrap;
  }
  .state.on {
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .kind {
      display: none;
    }
  }
</style>
