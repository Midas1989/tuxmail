<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Administration -> Invites: single-use registration links (server setting
  auth.registration: invite). The link contains a secret that the server
  returns only when the invite is created, so it is shown once in a dialog
  with a copy button; the list shows state and lets administrators revoke.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import MailPlus from '@lucide/svelte/icons/mail-plus';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import SecretDialog from './SecretDialog.svelte';
  import { t, fmt } from '../../i18n/index.svelte';
  import { ADMIN_ROLES, type AdminInvite, type AdminRole } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';
  import { inviteStatus, INVITE_LIFETIMES_HOURS } from '../../admin/invites';
  import type { AdminPanelProps } from '../../admin/panel';

  let { client, config, notify }: AdminPanelProps = $props();

  let invites = $state<AdminInvite[]>([]);
  let names = $state<Record<number, string>>({});
  let loading = $state(true);
  let loadError = $state('');
  let role = $state<AdminRole>('user');
  let email = $state('');
  let hours = $state(24 * 7);
  let busy = $state(false);
  let formError = $state('');
  let link = $state<string | null>(null);
  let revoking = $state<AdminInvite | null>(null);

  // Invites only work when the server is configured for them; unknown (null) is treated as possible.
  const enabled = $derived(!config || config.registration === 'invite');

  async function load() {
    loading = true;
    try {
      invites = await client.listInvites();
      loadError = '';
      const users = await client.listUsers().catch(() => []);
      names = Object.fromEntries(users.map((u) => [u.id, u.username]));
    } catch (err) {
      loadError = adminErrorMessage(err, t);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !enabled) return;
    busy = true;
    formError = '';
    try {
      const r = await client.createInvite({ role, email: email.trim() || undefined, expiresInHours: hours });
      link = r.url;
      email = '';
      notify(t('admin.toast.inviteCreated'));
      await load();
    } catch (err) {
      formError = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  async function revoke(inv: AdminInvite) {
    revoking = null;
    try {
      await client.deleteInvite(inv.id);
      notify(t('admin.toast.inviteRevoked'));
    } catch (err) {
      notify(adminErrorMessage(err, t), 'error');
    }
    await load();
  }

  const who = (id?: number) => (id ? (names[id] ?? `#${id}`) : '–');
</script>

<div class="invites">
  {#if !enabled}
    <p class="notice" role="status" data-testid="invites-off">{t('admin.invites.disabled')}</p>
  {/if}

  <form class="create" onsubmit={create} aria-label={t('admin.invites.create')}>
    <div class="f">
      <label class="label" for="inv-role">{t('admin.form.role')}</label>
      <select id="inv-role" class="input" bind:value={role} disabled={!enabled}>
        {#each ADMIN_ROLES as r (r)}<option value={r}>{t(`admin.role.${r}`)}</option>{/each}
      </select>
    </div>
    <div class="f wide">
      <label class="label" for="inv-email">{t('admin.invites.email')}</label>
      <input id="inv-email" class="input" type="email" bind:value={email} placeholder={t('admin.invites.emailPlaceholder')} autocomplete="off" disabled={!enabled} />
    </div>
    <div class="f">
      <label class="label" for="inv-exp">{t('admin.invites.expiry')}</label>
      <select id="inv-exp" class="input" bind:value={hours} disabled={!enabled}>
        {#each INVITE_LIFETIMES_HOURS as h (h)}<option value={h}>{t('admin.invites.days', { count: h / 24 })}</option>{/each}
      </select>
    </div>
    <button class="btn btn-primary" disabled={busy || !enabled} data-testid="invite-create"><MailPlus size={15} /> {t('admin.invites.create')}</button>
  </form>
  <p class="hint">{email.trim() ? t('admin.invites.emailFixedHint') : t('admin.invites.emailOpenHint')}</p>
  {#if formError}<p class="error" role="alert">{formError}</p>{/if}

  <div class="head">
    <h3>{t('admin.invites.listTitle')}</h3>
    <button class="icon-btn" onclick={() => void load()} aria-label={t('admin.refresh')} title={t('admin.refresh')} disabled={loading}><RefreshCw size={16} /></button>
  </div>
  {#if loadError}
    <p class="error" role="alert">{t('admin.loadFailed', { error: loadError })}</p>
  {:else if loading && !invites.length}
    <p class="muted" role="status">{t('admin.loading')}</p>
  {:else if !invites.length}
    <p class="muted" role="status">{t('admin.invites.empty')}</p>
  {:else}
    <p class="muted">{t('admin.invites.linkOnce')}</p>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (a scrollable region must be reachable by keyboard) -->
    <div class="scroll" role="region" aria-label={t('admin.invites.listTitle')} tabindex="0">
      <table class="tbl" data-testid="invite-table">
        <caption class="sr-only">{t('admin.invites.listTitle')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('admin.table.email')}</th>
            <th scope="col">{t('admin.table.role')}</th>
            <th scope="col">{t('admin.invites.created')}</th>
            <th scope="col">{t('admin.invites.expires')}</th>
            <th scope="col">{t('admin.table.status')}</th>
            <th scope="col"><span class="sr-only">{t('admin.table.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each invites as inv (inv.id)}
            {@const st = inviteStatus(inv)}
            <tr>
              <td>{inv.email || t('admin.invites.anyEmail')}</td>
              <td>{t(`admin.role.${inv.role}`)}</td>
              <td>{fmt().dateTime(inv.createdAt)}<span class="sub">{who(inv.createdBy)}</span></td>
              <td>{fmt().dateTime(inv.expiresAt)}</td>
              <td>
                <span class="state state-{st}">{t(`admin.invites.status.${st}`)}</span>
                {#if st === 'used'}<span class="sub">{who(inv.usedBy)}{inv.usedAt ? ' · ' + fmt().dateTime(inv.usedAt) : ''}</span>{/if}
              </td>
              <td class="end">
                <button class="btn" onclick={() => (revoking = inv)} aria-label={t(st === 'open' ? 'admin.invites.revokeFor' : 'admin.invites.removeFor', { email: inv.email || String(inv.id) })}>
                  {st === 'open' ? t('admin.invites.revoke') : t('admin.invites.remove')}
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

{#if link}
  <SecretDialog title={t('admin.invites.linkTitle')} intro={t('admin.invites.linkIntro')} label={t('admin.invites.linkLabel')} value={link} onclose={() => (link = null)} />
{/if}
{#if revoking}
  {@const inv = revoking}
  <ConfirmDialog
    title={t('admin.invites.revoke')}
    message={t('admin.invites.revokeConfirm', { email: inv.email || t('admin.invites.anyEmail') })}
    confirmLabel={t('admin.invites.revoke')}
    danger
    onconfirm={() => void revoke(inv)}
    oncancel={() => (revoking = null)}
  />
{/if}

<style>
  .invites {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .create {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 10px;
  }
  .f {
    min-width: 130px;
  }
  .f.wide {
    flex: 1 1 220px;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 6px;
  }
  h3 {
    margin: 0;
    font-size: 14px;
    font-weight: 650;
  }
  .hint,
  .muted {
    margin: 0;
    font-size: 12.5px;
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
  .scroll {
    overflow-x: auto;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .tbl {
    width: 100%;
    min-width: 640px;
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
  .tbl tbody tr:last-child td {
    border-bottom: none;
  }
  .sub {
    display: block;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .end {
    text-align: right;
    white-space: nowrap;
  }
  .state {
    display: inline-block;
    padding: 1px 8px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 600;
    background: var(--tm-surface-3);
  }
  .state-open {
    background: color-mix(in srgb, var(--tm-success) 18%, transparent);
  }
</style>
