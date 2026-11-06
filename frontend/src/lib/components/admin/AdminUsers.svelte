<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Administration -> Users: table with search and filters, create / edit /
  disable / reset password / reset 2FA / unlock / sessions / delete.
  The rules for who may do what live in lib/admin/users.ts (userGuards).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import Search from '@lucide/svelte/icons/search';
  import UserPlus from '@lucide/svelte/icons/user-plus';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Pencil from '@lucide/svelte/icons/pencil';
  import LockOpen from '@lucide/svelte/icons/lock-open';
  import Settings2 from '@lucide/svelte/icons/settings-2';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import SecretDialog from './SecretDialog.svelte';
  import UserFormDialog from './UserFormDialog.svelte';
  import UserActionsDialog from './UserActionsDialog.svelte';
  import UserSessionsDialog from './UserSessionsDialog.svelte';
  import DeleteUserDialog from './DeleteUserDialog.svelte';
  import { t, fmt } from '../../i18n/index.svelte';
  import { ADMIN_ROLES, type AdminUser } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';
  import type { AdminPanelProps } from '../../admin/panel';
  import { EMPTY_FILTER, filterUsers, isFiltered, needsUnlock, userGuards, userStatus, type UserAction, type UserFilter } from '../../admin/users';

  let { client, me, config, notify }: AdminPanelProps = $props();

  let users = $state<AdminUser[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let filter = $state<UserFilter>({ ...EMPTY_FILTER });
  let busyId = $state<number | null>(null);

  type Dlg =
    | { kind: 'create' }
    | { kind: 'edit'; id: number }
    | { kind: 'actions'; id: number }
    | { kind: 'sessions'; id: number }
    | { kind: 'delete'; id: number }
    | { kind: 'confirm'; id: number; what: 'disable' | 'resetPassword' | 'reset2fa' }
    /** A password the server generated; shown once, then dropped with the dialog. */
    | { kind: 'secret'; title: string; intro: string; value: string };
  let dlg = $state<Dlg | null>(null);

  const minLength = $derived(config?.passwordMinLength ?? 0);
  const shown = $derived(filterUsers(users, filter));
  const target = $derived.by(() => {
    const d = dlg;
    return d && 'id' in d ? (users.find((u) => u.id === d.id) ?? null) : null;
  });
  const lockedCount = $derived(users.filter(needsUnlock).length);

  async function load() {
    loading = true;
    try {
      users = await client.listUsers();
      loadError = '';
    } catch (err) {
      loadError = adminErrorMessage(err, t, minLength);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  const guardsOf = (u: AdminUser) => userGuards(u, users, me.id);

  /** Runs one admin call for a user: error toast on failure, reload on success. */
  async function run(u: AdminUser, call: () => Promise<void>, okText: string) {
    busyId = u.id;
    try {
      await call();
      notify(okText);
      await load();
    } catch (err) {
      notify(adminErrorMessage(err, t, minLength), 'error');
      await load();
    } finally {
      busyId = null;
    }
  }

  async function unlock(u: AdminUser) {
    dlg = null;
    await run(u, () => client.unlock(u.id), t('admin.toast.unlocked', { name: u.username }));
  }

  function onAction(u: AdminUser, a: UserAction) {
    switch (a) {
      case 'edit':
        dlg = { kind: 'edit', id: u.id };
        break;
      case 'unlock':
        void unlock(u);
        break;
      case 'toggleDisabled':
        if (u.disabled) {
          dlg = null;
          void run(u, async () => void (await client.updateUser(u.id, { disabled: false })), t('admin.toast.enabled', { name: u.username }));
        } else dlg = { kind: 'confirm', id: u.id, what: 'disable' };
        break;
      case 'resetPassword':
        dlg = { kind: 'confirm', id: u.id, what: 'resetPassword' };
        break;
      case 'reset2fa':
        dlg = { kind: 'confirm', id: u.id, what: 'reset2fa' };
        break;
      case 'sessions':
        dlg = { kind: 'sessions', id: u.id };
        break;
      case 'delete':
        dlg = { kind: 'delete', id: u.id };
        break;
    }
  }

  async function confirmed(u: AdminUser, what: 'disable' | 'resetPassword' | 'reset2fa') {
    dlg = null;
    if (what === 'disable') await run(u, async () => void (await client.updateUser(u.id, { disabled: true })), t('admin.toast.disabled', { name: u.username }));
    else if (what === 'reset2fa') await run(u, () => client.reset2FA(u.id), t('admin.toast.reset2fa', { name: u.username }));
    else {
      busyId = u.id;
      try {
        const password = await client.resetPassword(u.id);
        dlg = { kind: 'secret', title: t('admin.secret.resetTitle'), intro: t('admin.secret.resetIntro', { name: u.username }), value: password };
        await load();
      } catch (err) {
        notify(adminErrorMessage(err, t, minLength), 'error');
      } finally {
        busyId = null;
      }
    }
  }

  function saved(r: { user: AdminUser; password?: string; created: boolean }) {
    dlg = r.password ? { kind: 'secret', title: t('admin.secret.createdTitle'), intro: t('admin.secret.createdIntro', { name: r.user.username }), value: r.password } : null;
    notify(r.created ? t('admin.toast.created', { name: r.user.username }) : t('admin.toast.saved', { name: r.user.username }));
    void load();
  }

  const confirmText = (what: 'disable' | 'resetPassword' | 'reset2fa', u: AdminUser) =>
    t(what === 'disable' ? 'admin.confirm.disable' : what === 'resetPassword' ? 'admin.confirm.resetPassword' : 'admin.confirm.reset2fa', { name: u.username });
  const confirmTitle = (what: 'disable' | 'resetPassword' | 'reset2fa') =>
    t(what === 'disable' ? 'admin.actions.disable' : what === 'resetPassword' ? 'admin.users.resetPassword' : 'admin.users.reset2fa');
</script>

<div class="users">
  <div class="toolbar" role="search" aria-label={t('admin.users.filters')}>
    <div class="search">
      <Search size={15} class="s-icon" aria-hidden="true" />
      <input class="input" type="search" bind:value={filter.query} placeholder={t('admin.users.searchPlaceholder')} aria-label={t('admin.users.search')} data-testid="user-search" />
    </div>
    <select class="input sel" bind:value={filter.role} aria-label={t('admin.users.filterRole')}>
      <option value="">{t('admin.users.allRoles')}</option>
      {#each ADMIN_ROLES as r (r)}<option value={r}>{t(`admin.role.${r}`)}</option>{/each}
    </select>
    <select class="input sel" bind:value={filter.status} aria-label={t('admin.users.filterStatus')} data-testid="user-status-filter">
      <option value="">{t('admin.users.allStatus')}</option>
      <option value="active">{t('admin.status.active')}</option>
      <option value="disabled">{t('admin.status.disabled')}</option>
      <option value="locked">{t('admin.status.locked')}</option>
    </select>
    <select class="input sel" bind:value={filter.totp} aria-label={t('admin.users.filterTotp')}>
      <option value="">{t('admin.users.allTotp')}</option>
      <option value="on">{t('admin.users.totpOn')}</option>
      <option value="off">{t('admin.users.totpOff')}</option>
    </select>
    <span class="grow"></span>
    <button class="icon-btn" onclick={() => void load()} aria-label={t('admin.refresh')} title={t('admin.refresh')} disabled={loading}><RefreshCw size={16} /></button>
    <button class="btn btn-primary" onclick={() => (dlg = { kind: 'create' })} data-testid="user-create"><UserPlus size={15} /> {t('admin.users.create')}</button>
  </div>

  {#if lockedCount}
    <p class="notice" role="status">
      {t('admin.users.lockedNotice', { count: lockedCount })}
      {#if filter.status !== 'locked'}<button class="link" onclick={() => (filter.status = 'locked')}>{t('admin.users.showLocked')}</button>{/if}
    </p>
  {/if}

  {#if loadError}
    <p class="error" role="alert">{t('admin.loadFailed', { error: loadError })} <button class="link" onclick={() => void load()}>{t('errors.retry')}</button></p>
  {:else if loading && !users.length}
    <p class="muted" role="status">{t('admin.loading')}</p>
  {:else}
    <p class="count" role="status" aria-live="polite">
      {isFiltered(filter) ? t('admin.users.countFiltered', { shown: shown.length, total: users.length }) : t('admin.users.count', { count: users.length })}
      {#if isFiltered(filter)}<button class="link" onclick={() => (filter = { ...EMPTY_FILTER })}>{t('admin.users.resetFilters')}</button>{/if}
    </p>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (a scrollable region must be reachable by keyboard) -->
    <div class="scroll" role="region" aria-label={t('admin.users.title')} tabindex="0">
      <table class="tbl" data-testid="user-table">
        <caption class="sr-only">{t('admin.users.title')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('admin.table.user')}</th>
            <th scope="col" class="opt">{t('admin.table.email')}</th>
            <th scope="col">{t('admin.table.role')}</th>
            <th scope="col">{t('admin.table.status')}</th>
            <th scope="col" class="opt">{t('admin.table.twoFactor')}</th>
            <th scope="col" class="opt">{t('admin.table.lastLogin')}</th>
            <th scope="col"><span class="sr-only">{t('admin.table.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each shown as u (u.id)}
            {@const st = userStatus(u)}
            <tr data-user={u.username} class:dim={u.disabled}>
              <th scope="row" class="who">
                <span class="uname">{u.username}</span>
                {#if u.id === me.id}<span class="badge badge-muted">{t('admin.users.you')}</span>{/if}
                {#if u.displayName}<span class="sub">{u.displayName}</span>{/if}
              </th>
              <td class="email opt">{u.email || '–'}</td>
              <td>{t(`admin.role.${u.role}`)}</td>
              <td>
                <span class="state state-{st}" data-status={st}>{t(`admin.status.${st}`)}</span>
                {#if u.locked}
                  <span class="sub" data-testid="lock-info">{u.lockedUntil ? t('admin.status.lockedUntil', { time: fmt().time(u.lockedUntil) }) : t('admin.status.lockedNetwork')}</span>
                {/if}
                {#if u.underPressure}
                  <span class="sub" data-testid="pressure-info">{t('admin.status.underPressure')}</span>
                {/if}
              </td>
              <td class="opt">{u.totpEnabled ? t('admin.users.totpOn') : t('admin.users.totpOff')}</td>
              <td class="opt">{u.lastLoginAt ? fmt().dateTime(u.lastLoginAt) : t('admin.users.never')}</td>
              <td class="end">
                {#if needsUnlock(u)}
                  <button class="btn" disabled={busyId === u.id} onclick={() => void unlock(u)} aria-label={t('admin.users.unlockFor', { name: u.username })} data-action="unlock">
                    <LockOpen size={14} /> <span class="lbl">{t('admin.users.unlock')}</span>
                  </button>
                {/if}
                <button class="icon-btn" onclick={() => (dlg = { kind: 'edit', id: u.id })} aria-label={t('admin.users.editFor', { name: u.username })} title={t('admin.actions.edit')} data-action="edit"><Pencil size={15} /></button>
                <button class="icon-btn" onclick={() => (dlg = { kind: 'actions', id: u.id })} aria-label={t('admin.users.actionsFor', { name: u.username })} title={t('admin.users.actions')} data-action="manage"><Settings2 size={16} /></button>
              </td>
            </tr>
          {:else}
            <tr><td colspan="7" class="empty">{isFiltered(filter) ? t('admin.users.noMatch') : t('admin.users.empty')}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

{#if dlg?.kind === 'create'}
  <UserFormDialog {client} user={null} minPasswordLength={minLength} onsaved={saved} onclose={() => (dlg = null)} />
{:else if dlg?.kind === 'edit' && target}
  <UserFormDialog {client} user={target} minPasswordLength={minLength} roleLocked={guardsOf(target).role} onsaved={saved} onclose={() => (dlg = null)} />
{:else if dlg?.kind === 'actions' && target}
  <UserActionsDialog user={target} guards={guardsOf(target)} onaction={(a) => onAction(target, a)} onclose={() => (dlg = null)} />
{:else if dlg?.kind === 'sessions' && target}
  <UserSessionsDialog {client} user={target} own={target.id === me.id} onclose={() => (dlg = null)} />
{:else if dlg?.kind === 'delete' && target}
  <DeleteUserDialog
    {client}
    user={target}
    ondeleted={(u, kept) => {
      dlg = null;
      notify(kept ? t('admin.toast.deletedKept', { name: u.username }) : t('admin.toast.deleted', { name: u.username }));
      void load();
    }}
    onclose={() => (dlg = null)}
  />
{:else if dlg?.kind === 'confirm' && target}
  {@const what = dlg.what}
  <ConfirmDialog title={confirmTitle(what)} message={confirmText(what, target)} confirmLabel={confirmTitle(what)} danger={what !== 'resetPassword'} onconfirm={() => void confirmed(target, what)} oncancel={() => (dlg = null)} />
{:else if dlg?.kind === 'secret'}
  <SecretDialog title={dlg.title} intro={dlg.intro} label={t('admin.secret.password')} value={dlg.value} onclose={() => (dlg = null)} />
{/if}

<style>
  .users {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .search {
    position: relative;
    flex: 1 1 200px;
    min-width: 160px;
  }
  .search :global(.s-icon) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--tm-faint);
    pointer-events: none;
  }
  .search .input {
    padding-left: 32px;
  }
  .sel {
    width: auto;
    flex: none;
  }
  .grow {
    flex: 1;
  }
  .count,
  .muted {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .notice {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-warn) 14%, transparent);
    font-size: 13px;
  }
  .error {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
    font-size: 13px;
  }
  .link {
    border: none;
    background: none;
    padding: 0 2px;
    color: var(--tm-accent-text);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
  }
  .scroll {
    overflow-x: auto;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .tbl {
    width: 100%;
    min-width: 760px;
    border-collapse: collapse;
    font-size: 13px;
  }
  .tbl th,
  .tbl td {
    padding: 8px 10px;
    text-align: left;
    vertical-align: top;
    border-bottom: 1px solid var(--tm-line);
  }
  .tbl thead th {
    font-size: 12px;
    font-weight: 650;
    color: var(--tm-muted);
    background: var(--tm-surface-2);
  }
  .tbl tbody tr:last-child th,
  .tbl tbody tr:last-child td {
    border-bottom: none;
  }
  .who {
    font-weight: 400;
  }
  .uname {
    font-weight: 600;
  }
  .sub {
    display: block;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .email {
    overflow-wrap: anywhere;
  }
  .dim {
    color: var(--tm-muted);
  }
  .end {
    text-align: right;
    white-space: nowrap;
  }
  .empty {
    padding: 20px 10px;
    text-align: center;
    color: var(--tm-muted);
  }
  .state {
    display: inline-block;
    padding: 1px 8px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 600;
    background: var(--tm-surface-3);
  }
  .state-active {
    background: color-mix(in srgb, var(--tm-success) 18%, transparent);
  }
  .state-locked {
    background: color-mix(in srgb, var(--tm-warn) 22%, transparent);
  }
  .state-disabled {
    color: var(--tm-muted);
  }
  /* Phones: e-mail, 2FA and last sign-in move to the edit dialog's reach; the row keeps what the actions need. */
  @media (max-width: 639px) {
    .tbl {
      min-width: 0;
    }
    .opt,
    .lbl {
      display: none;
    }
  }
</style>
