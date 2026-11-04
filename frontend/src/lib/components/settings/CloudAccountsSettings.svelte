<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Anmeldedienste (own OAuth client IDs for Microsoft/Google),
  shared mailboxes (Microsoft 365 / Exchange) and the send mode of
  identities (Send-As / Send-on-Behalf), KONZEPT 7.3–7.4, 7.3a.
-->
<script lang="ts">
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Users from '@lucide/svelte/icons/users';
  import CalendarSync from '@lucide/svelte/icons/calendar-sync';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Account, IdentityDelegation, OAuthClientInput, OAuthProviderInfo, SendMode } from '../../types';
  import SharedMailboxDialog from './SharedMailboxDialog.svelte';

  let providers = $state<OAuthProviderInfo[]>([]);
  let delegations = $state<IdentityDelegation[]>([]);
  let editing = $state<OAuthClientInput | null>(null);
  let status = $state<string | null>(null);
  let error = $state<string | null>(null);
  let sharedOpen = $state(false);

  const cloudAccounts = $derived(app.accounts.filter((a) => (a.kind === 'graph' || a.kind === 'ews') && !a.extra?.mailbox));
  const pimAccounts = $derived(app.accounts.filter((a) => a.kind === 'graph' || a.kind === 'ews' || a.kind === 'gmail'));
  const mailIdentities = $derived(app.identities.filter((i) => app.accounts.some((a) => a.id === i.accountId && (a.kind === 'graph' || a.kind === 'ews'))));

  async function load() {
    try {
      [providers, delegations] = await Promise.all([app.api.listOAuthProviders(), app.api.listIdentityDelegations()]);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
  $effect(() => {
    void load();
  });

  function flash(msg: string) {
    status = msg;
    setTimeout(() => (status = null), 2500);
  }

  function edit(p: OAuthProviderInfo) {
    editing = p.userClient ? { ...p.userClient, clientSecret: '' } : { provider: p.id, clientId: '', tenant: '' };
  }

  async function saveClient() {
    if (!editing) return;
    error = null;
    try {
      await app.api.saveOAuthClient({ ...editing, clientSecret: editing.clientSecret || undefined });
      editing = null;
      await load();
      flash(t('cloud.services.saved'));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function removeClient(provider: string) {
    await app.api.deleteOAuthClient(provider);
    editing = null;
    await load();
  }

  function modeOf(identityId: number): SendMode | '' {
    return delegations.find((d) => d.identityId === identityId)?.mode ?? '';
  }

  async function setMode(identityId: number, mode: string) {
    error = null;
    try {
      await app.api.setIdentityDelegation({ identityId, mode });
      delegations = await app.api.listIdentityDelegations();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function added(acc: Account) {
    sharedOpen = false;
    const [accounts, identities] = await Promise.all([app.api.listAccounts(), app.api.listIdentities()]);
    app.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
    app.identities = identities;
    delegations = await app.api.listIdentityDelegations();
    flash(t('cloud.shared.added', { address: acc.extra?.mailbox ?? acc.displayName }));
  }

  async function syncPim(accountId: number) {
    error = null;
    try {
      const r = await app.api.syncCloudPim(accountId);
      flash(t('cloud.pim.done', { events: r.events, contacts: r.contacts, tasks: r.tasks }));
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
</script>

<section class="card" id="set-signin" aria-labelledby="set-signin-h">
  <div class="head">
    <h2 id="set-signin-h"><KeyRound size={16} /> {t('cloud.services.title')}</h2>
    {#if status}<span class="saved" role="status">{status}</span>{/if}
  </div>
  <p class="hint">{t('cloud.services.intro')}</p>
  <ul class="list">
    {#each providers as p (p.id)}
      <li>
        <div class="row">
          <strong>{p.displayName}</strong>
          <span class="badge" class:on={p.configured}>{p.configured ? t('cloud.services.configured') : t('cloud.services.notConfigured')}</span>
          {#if p.source}<span class="sub">{t(`cloud.services.source.${p.source}`)}</span>{/if}
          {#if p.allowUserClients && editing?.provider !== p.id}
            <button class="btn btn-ghost small" onclick={() => edit(p)}>{t('cloud.services.edit')}</button>
          {/if}
        </div>
        {#if p.clientId}<code class="sub">{p.clientId}</code>{/if}
        {#if p.redirectUri}<p class="sub">{t('cloud.services.redirect', { uri: p.redirectUri })}</p>{/if}
        {#if editing && editing.provider === p.id}
          <form
            class="form"
            onsubmit={(e) => {
              e.preventDefault();
              void saveClient();
            }}
          >
            <label><span class="label">{t('cloud.services.clientId')}</span><input class="input" bind:value={editing.clientId} required data-autofocus /></label>
            {#if p.id === 'google'}
              <label><span class="label">{t('cloud.services.clientSecret')}</span><input class="input" type="password" bind:value={editing.clientSecret} autocomplete="off" /></label>
              {#if editing.hasSecret}<p class="sub">{t('cloud.services.secretStored')}</p>{/if}
            {:else}
              <label><span class="label">{t('cloud.services.tenant')}</span><input class="input" bind:value={editing.tenant} /></label>
            {/if}
            <div class="row-actions">
              {#if p.userClient}<button type="button" class="btn btn-ghost" onclick={() => removeClient(p.id)}>{t('cloud.services.remove')}</button>{/if}
              <button type="button" class="btn btn-ghost" onclick={() => (editing = null)}>{t('common.action.cancel')}</button>
              <button type="submit" class="btn btn-primary">{t('cloud.services.save')}</button>
            </div>
          </form>
        {/if}
        {#if !p.allowUserClients}<p class="sub">{t('cloud.services.adminOnly')}</p>{/if}
      </li>
    {/each}
  </ul>

  <h3><Users size={15} /> {t('cloud.shared.title')}</h3>
  <p class="hint">{t('cloud.shared.intro')}</p>
  <div class="row-actions start">
    <button class="btn" onclick={() => (sharedOpen = true)} disabled={cloudAccounts.length === 0}>{t('cloud.shared.add')}</button>
    {#if cloudAccounts.length === 0}<span class="sub">{t('cloud.shared.noAccounts')}</span>{/if}
  </div>

  {#if mailIdentities.length > 0}
    <h3>{t('cloud.identities.title')}</h3>
    <p class="hint">{t('cloud.identities.intro')}</p>
    <ul class="list">
      {#each mailIdentities as i (i.id)}
        <li class="row">
          <span class="ident">{i.name} &lt;{i.email}&gt;</span>
          {#if modeOf(i.id)}<span class="badge on">{t(`cloud.sendMode.${modeOf(i.id) as SendMode}`)}</span>{/if}
          <select class="input small" aria-label={t('cloud.identities.mode', { email: i.email })} value={modeOf(i.id)} onchange={(e) => setMode(i.id, e.currentTarget.value)}>
            <option value="">{t('cloud.sendMode.own')}</option>
            <option value="send_as">{t('cloud.sendMode.send_as')}</option>
            <option value="send_on_behalf">{t('cloud.sendMode.send_on_behalf')}</option>
          </select>
        </li>
      {/each}
    </ul>
  {/if}

  {#if pimAccounts.length > 0}
    <ul class="list">
      {#each pimAccounts as a (a.id)}
        <li class="row">
          <span class="ident">{a.displayName}</span>
          <button class="btn btn-ghost small" onclick={() => syncPim(a.id)}><CalendarSync size={14} /> {t('cloud.pim.sync')}</button>
        </li>
      {/each}
    </ul>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

{#if sharedOpen}
  <SharedMailboxDialog accounts={cloudAccounts} onclose={() => (sharedOpen = false)} onadded={added} />
{/if}

<style>
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2,
  h3 {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  h3 {
    font-size: 14px;
    margin: 16px 0 4px;
  }
  .hint {
    margin: 0 0 8px;
    font-size: 12.5px;
    color: var(--tm-faint);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .ident {
    flex: 1;
    font-size: 13.5px;
  }
  .sub {
    font-size: 12px;
    color: var(--tm-faint);
    margin: 2px 0 0;
    word-break: break-all;
  }
  .badge {
    font-size: 11.5px;
    padding: 1px 8px;
    border-radius: 999px;
    background: var(--tm-line);
  }
  .badge.on {
    background: color-mix(in srgb, var(--tm-success) 18%, var(--tm-surface));
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 8px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    align-items: center;
  }
  .row-actions.start {
    justify-content: flex-start;
  }
  .small {
    font-size: 12.5px;
  }
  .saved {
    font-size: 12.5px;
    color: var(--tm-success);
  }
  .error {
    color: var(--tm-danger);
    font-size: 13px;
  }
</style>
