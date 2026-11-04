<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Account edit dialog, tab "Identitäten": sender addresses and aliases of
  one account (name, address, reply-to, Bcc to self, own SMTP account,
  signatures, chat short signature, Send-As/On-Behalf for Microsoft).
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import Star from '@lucide/svelte/icons/star';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { AccountKind, Identity, IdentityDelegation, SendMode } from '../../types';

  interface Props {
    accountId: number;
    accountKind: AccountKind;
  }
  let { accountId, accountKind }: Props = $props();

  const identities = $derived(app.identities.filter((i) => i.accountId === accountId));
  const smtpAccounts = $derived(app.mailAccounts.filter((a) => a.outgoing?.host && a.id !== accountId));
  const delegationKinds: AccountKind[] = ['graph', 'ews'];
  const canDelegate = $derived(delegationKinds.includes(accountKind));

  let editing = $state<Identity | null>(null);
  let delegationMode = $state<SendMode | ''>('');
  let delegationMailbox = $state('');
  let delegations = $state<IdentityDelegation[]>([]);
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    if (canDelegate) void app.api.listIdentityDelegations().then((d) => (delegations = d)).catch(() => undefined);
  });

  function blank(): Identity {
    return { id: 0, accountId, name: identities[0]?.name ?? '', email: '', bccSelf: false, isDefault: false, chatSignatureMode: 'full' };
  }

  function edit(i: Identity) {
    editing = { ...i, chatSignatureMode: i.chatSignatureMode ?? 'full' };
    const d = delegations.find((x) => x.identityId === i.id);
    delegationMode = d?.mode ?? '';
    delegationMailbox = d?.mailbox ?? '';
    error = null;
  }

  async function reload() {
    app.identities = await app.api.listIdentities();
    if (canDelegate) delegations = await app.api.listIdentityDelegations().catch(() => delegations);
  }

  async function act(f: () => Promise<unknown>, toast?: string) {
    busy = true;
    error = null;
    try {
      await f();
      await reload();
      if (toast) app.toast(toast);
      return true;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      return false;
    } finally {
      busy = false;
    }
  }

  async function save() {
    const i = editing;
    if (!i) return;
    const clean: Identity = {
      ...i,
      smtpAccountId: Number(i.smtpAccountId) || undefined,
      defaultSignatureId: Number(i.defaultSignatureId) || undefined,
      replySignatureId: Number(i.replySignatureId) || undefined,
      chatSignatureId: Number(i.chatSignatureId) || undefined,
    };
    const ok = await act(async () => {
      const saved = clean.id ? await app.api.updateIdentity(clean) : await app.api.createIdentity(clean);
      if (canDelegate) {
        const cur = delegations.find((d) => d.identityId === saved.id);
        if ((cur?.mode ?? '') !== delegationMode || (cur?.mailbox ?? '') !== delegationMailbox) {
          await app.api.setIdentityDelegation({ identityId: saved.id, mode: delegationMode, mailbox: delegationMailbox || undefined });
        }
      }
    }, t('settings.identities.saved'));
    if (ok) editing = null;
  }

  const sigName = (id?: number) => app.signatures.find((s) => s.id === id)?.name;
</script>

<div class="ids">
  <p class="hint">{t('settings.identities.intro')}</p>
  <ul>
    {#each identities as i (i.id)}
      <li>
        <div class="who">
          <span class="name">{i.name || i.email}{#if i.isDefault}<span class="def">{t('settings.identities.default')}</span>{/if}</span>
          <span class="sub">
            {[i.email, i.replyTo ? t('settings.identities.replyToShort', { address: i.replyTo }) : '', sigName(i.defaultSignatureId) ?? ''].filter(Boolean).join(' · ')}
          </span>
        </div>
        {#if !i.isDefault}
          <button class="btn btn-ghost small" disabled={busy} title={t('settings.identities.makeDefault')} aria-label={t('settings.identities.makeDefault')} onclick={() => void act(() => app.api.setDefaultIdentity(i.id))}><Star size={13} /></button>
        {/if}
        <button class="btn btn-ghost small" disabled={busy} onclick={() => edit(i)}>{t('common.action.edit')}</button>
        <button class="btn btn-ghost small" disabled={busy || identities.length <= 1} aria-label={t('settings.identities.delete', { email: i.email })} onclick={() => void act(() => app.api.deleteIdentity(i.id), t('settings.identities.deleted'))}><Trash size={13} /></button>
      </li>
    {/each}
  </ul>
  {#if !editing}
    <button class="btn" onclick={() => ((editing = blank()), (delegationMode = ''), (delegationMailbox = ''))}><Plus size={14} /> {t('settings.identities.add')}</button>
  {:else}
    <form
      class="edit"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <h3>{editing.id ? t('settings.identities.edit') : t('settings.identities.add')}</h3>
      <div class="grid">
        <label><span class="label">{t('settings.identities.name')}</span><input class="input" bind:value={editing.name} data-autofocus /></label>
        <label><span class="label">{t('onboarding.account.email')}</span><input class="input" type="email" bind:value={editing.email} required /></label>
        <label><span class="label">{t('settings.identities.replyTo')}</span><input class="input" type="email" bind:value={editing.replyTo} /></label>
        <label>
          <span class="label">{t('settings.identities.smtp')}</span>
          <select class="input" bind:value={editing.smtpAccountId}>
            <option value={undefined}>{t('settings.identities.smtpAccount')}</option>
            {#each smtpAccounts as a (a.id)}<option value={a.id}>{a.displayName} · {a.outgoing?.host}</option>{/each}
          </select>
        </label>
        <label>
          <span class="label">{t('settings.signatures.default')}</span>
          <select class="input" bind:value={editing.defaultSignatureId}>
            <option value={undefined}>{t('settings.signatures.none')}</option>
            {#each app.signatures as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
          </select>
        </label>
        <label>
          <span class="label">{t('settings.signatures.reply')}</span>
          <select class="input" bind:value={editing.replySignatureId}>
            <option value={undefined}>{t('settings.signatures.sameAsDefault')}</option>
            {#each app.signatures as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
          </select>
        </label>
        <label>
          <span class="label">{t('settings.identities.chatSignature')}</span>
          <select class="input" bind:value={editing.chatSignatureMode}>
            {#each ['full', 'short', 'off'] as const as m (m)}<option value={m}>{t(`settings.identities.chatMode.${m}`)}</option>{/each}
          </select>
        </label>
        {#if editing.chatSignatureMode === 'short'}
          <label>
            <span class="label">{t('settings.identities.chatShort')}</span>
            <select class="input" bind:value={editing.chatSignatureId}>
              <option value={undefined}>{t('settings.signatures.none')}</option>
              {#each app.signatures as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
            </select>
          </label>
        {/if}
      </div>
      <label class="check"><input type="checkbox" bind:checked={editing.bccSelf} /> {t('settings.identities.bccSelf')}</label>
      <label class="check"><input type="checkbox" bind:checked={editing.isDefault} /> {t('settings.identities.makeDefault')}</label>
      {#if canDelegate}
        <div class="grid">
          <label>
            <span class="label">{t('settings.identities.delegation')}</span>
            <select class="input" bind:value={delegationMode}>
              <option value="">{t('settings.identities.delegationNone')}</option>
              <option value="send_as">{t('settings.identities.sendAs')}</option>
              <option value="send_on_behalf">{t('settings.identities.sendOnBehalf')}</option>
            </select>
          </label>
          {#if delegationMode}
            <label><span class="label">{t('settings.identities.mailbox')}</span><input class="input" type="email" bind:value={delegationMailbox} /></label>
          {/if}
        </div>
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={() => (editing = null)}>{t('common.action.cancel')}</button>
        <button type="submit" class="btn btn-primary" disabled={busy}>{t('common.action.save')}</button>
      </div>
    </form>
  {/if}
  {#if error && !editing}<p class="error" role="alert">{error}</p>{/if}
</div>

<style>
  .ids {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .ids > .btn {
    align-self: flex-start;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 0;
    border-top: 1px solid var(--tm-line);
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .name {
    font-weight: 600;
    display: inline-flex;
    gap: 8px;
    align-items: center;
  }
  .def {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 8px;
    border-radius: 99px;
    background: color-mix(in srgb, var(--tm-accent) 14%, var(--tm-surface));
    color: var(--tm-accent-text);
  }
  .sub {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .btn.small {
    height: 28px;
    font-size: 12px;
    padding: 0 8px;
  }
  .edit {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
  }
  h3 {
    margin: 0;
    font-size: 13px;
    font-weight: 650;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .grid .input {
    width: 100%;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  @media (max-width: 639px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
</style>
