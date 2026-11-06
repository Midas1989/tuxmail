<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Sessions of one account (browsers and connected devices): end a single one
  or all of them. The administrator's own current session cannot be ended
  here, and "all" is blocked for the own account (the UI never signs the
  administrator out).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import Modal from '../common/Modal.svelte';
  import { t, fmt } from '../../i18n/index.svelte';
  import type { AdminClient, AdminSession, AdminUser } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';

  interface Props {
    client: AdminClient;
    user: AdminUser;
    /** True for the administrator's own account. */
    own: boolean;
    onchanged?: () => void;
    onclose: () => void;
  }
  let { client, user, own, onchanged, onclose }: Props = $props();

  let sessions = $state<AdminSession[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state('');

  async function load() {
    loading = true;
    try {
      sessions = await client.listSessions(user.id);
      error = '';
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  async function revoke(s: AdminSession) {
    busy = true;
    try {
      await client.revokeSession(user.id, s.id);
      await load();
      onchanged?.();
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  async function revokeAll() {
    busy = true;
    try {
      await client.revokeSessions(user.id);
      await load();
      onchanged?.();
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  const label = (s: AdminSession) => s.deviceName || s.userAgent || t('admin.sessions.unknownDevice');
</script>

<Modal title={t('admin.sessions.title', { name: user.username })} {onclose} width="720px">
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if loading}
    <p class="muted" role="status">{t('admin.loading')}</p>
  {:else if !sessions.length}
    <p class="muted" role="status">{t('admin.sessions.empty')}</p>
  {:else}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (a scrollable region must be reachable by keyboard) -->
    <div class="scroll" role="region" aria-label={t('admin.sessions.title', { name: user.username })} tabindex="0">
      <table class="tbl">
        <caption class="sr-only">{t('admin.sessions.title', { name: user.username })}</caption>
        <thead>
          <tr>
            <th scope="col">{t('admin.sessions.device')}</th>
            <th scope="col">{t('admin.sessions.address')}</th>
            <th scope="col">{t('admin.sessions.signedIn')}</th>
            <th scope="col">{t('admin.sessions.lastSeen')}</th>
            <th scope="col"><span class="sr-only">{t('admin.table.actions')}</span></th>
          </tr>
        </thead>
        <tbody>
          {#each sessions as s (s.id)}
            <tr>
              <td>
                {label(s)}
                {#if s.current}<span class="badge">{t('admin.sessions.current')}</span>{/if}
                <div class="sub">{t(s.kind === 'device' ? 'admin.sessions.kindDevice' : 'admin.sessions.kindWeb')}{#if s.mfa}{' · '}{t('admin.sessions.mfa')}{/if}</div>
              </td>
              <td>{s.ip || '–'}</td>
              <td>{fmt().dateTime(s.createdAt)}</td>
              <td>{fmt().dateTime(s.lastSeenAt)}</td>
              <td class="end">
                <button class="btn" disabled={busy || s.current} onclick={() => revoke(s)} aria-label={t('admin.sessions.revokeOne', { device: label(s) })}>{t('admin.sessions.revoke')}</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  {#snippet footer()}
    {#if own}<span class="own">{t('admin.guard.selfSessions')}</span>{/if}
    <button class="btn" disabled={busy || own || !sessions.length} onclick={revokeAll} data-testid="revoke-all">{t('admin.sessions.revokeAll')}</button>
    <button class="btn btn-primary" onclick={onclose}>{t('common.action.close')}</button>
  {/snippet}
</Modal>

<style>
  .scroll {
    overflow-x: auto;
  }
  .tbl {
    width: 100%;
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
  .tbl th {
    font-size: 12px;
    font-weight: 650;
    color: var(--tm-muted);
  }
  .end {
    text-align: right;
    white-space: nowrap;
  }
  .sub,
  .muted,
  .own {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .own {
    margin-right: auto;
    align-self: center;
  }
  .badge {
    margin-left: 6px;
  }
  .error {
    margin: 0 0 10px;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
    font-size: 13px;
  }
</style>
