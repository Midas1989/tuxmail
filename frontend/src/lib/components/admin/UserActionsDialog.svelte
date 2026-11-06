<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  All actions on one account, each with its consequence in words. Actions the
  administrator must not take (own account, last administrator) stay visible
  but disabled, with the reason written below the button, so nothing silently
  disappears. "Unlock" exists only while the account is throttled.
-->
<script lang="ts">
  import Pencil from '@lucide/svelte/icons/pencil';
  import Power from '@lucide/svelte/icons/power';
  import PowerOff from '@lucide/svelte/icons/power-off';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import ShieldOff from '@lucide/svelte/icons/shield-off';
  import LockOpen from '@lucide/svelte/icons/lock-open';
  import Monitor from '@lucide/svelte/icons/monitor';
  import Trash from '@lucide/svelte/icons/trash';
  import Modal from '../common/Modal.svelte';
  import { t, fmt } from '../../i18n/index.svelte';
  import type { AdminUser } from '../../api/admin';
  import { needsUnlock, type GuardReason, type UserAction, type UserGuards } from '../../admin/users';

  interface Props {
    user: AdminUser;
    guards: UserGuards;
    onaction: (a: UserAction) => void;
    onclose: () => void;
  }
  let { user, guards, onaction, onclose }: Props = $props();

  interface Row {
    id: UserAction;
    icon: typeof Pencil;
    label: string;
    desc: string;
    reason: GuardReason | null;
    danger?: boolean;
  }

  const rows = $derived<Row[]>([
    { id: 'edit', icon: Pencil, label: t('admin.actions.edit'), desc: t('admin.actions.editDesc'), reason: null },
    ...(needsUnlock(user)
      ? [{ id: 'unlock', icon: LockOpen, label: t('admin.users.unlock'), desc: unlockDesc(), reason: null } satisfies Row]
      : []),
    user.disabled
      ? { id: 'toggleDisabled', icon: Power, label: t('admin.actions.enable'), desc: t('admin.actions.enableDesc'), reason: guards.disable }
      : { id: 'toggleDisabled', icon: PowerOff, label: t('admin.actions.disable'), desc: t('admin.actions.disableDesc'), reason: guards.disable },
    { id: 'resetPassword', icon: KeyRound, label: t('admin.users.resetPassword'), desc: t('admin.actions.resetPasswordDesc'), reason: guards.resetPassword },
    { id: 'reset2fa', icon: ShieldOff, label: t('admin.users.reset2fa'), desc: user.totpEnabled ? t('admin.actions.reset2faDesc') : t('admin.actions.reset2faOff'), reason: guards.reset2FA },
    { id: 'sessions', icon: Monitor, label: t('admin.actions.sessions'), desc: t('admin.actions.sessionsDesc'), reason: null },
    { id: 'delete', icon: Trash, label: t('admin.actions.delete'), desc: t('admin.actions.deleteDesc'), reason: guards.delete, danger: true },
  ]);

  function unlockDesc(): string {
    if (user.locked && user.lockedUntil) return t('admin.actions.unlockLockedDesc', { until: fmt().time(user.lockedUntil) });
    return t('admin.actions.unlockPressureDesc');
  }

  function reasonText(r: GuardReason, id: UserAction): string {
    if (r === 'lastAdmin') return t('admin.guard.lastAdmin');
    return id === 'resetPassword' || id === 'sessions' ? t('admin.guard.selfSessions') : t('admin.guard.self');
  }
</script>

<Modal title={user.displayName ? `${user.displayName} (${user.username})` : user.username} {onclose} width="560px">
  <ul class="actions" aria-label={t('admin.actions.label', { name: user.username })}>
    {#each rows as r (r.id)}
      {@const Icon = r.icon}
      <li class:danger={r.danger}>
        <button class="btn act" class:danger-btn={r.danger} disabled={!!r.reason} aria-describedby="act-{r.id}" onclick={() => onaction(r.id)} data-action={r.id} data-autofocus={r.id === 'edit' ? true : undefined}>
          <Icon size={15} /> {r.label}
        </button>
        <p class="desc" id="act-{r.id}">{r.reason ? reasonText(r.reason, r.id) : r.desc}</p>
      </li>
    {/each}
  </ul>
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.close')}</button>
  {/snippet}
</Modal>

<style>
  .actions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  li {
    display: grid;
    grid-template-columns: 180px 1fr;
    gap: 4px 14px;
    align-items: center;
  }
  .act {
    justify-content: flex-start;
    width: 100%;
  }
  .desc {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .danger-btn:not(:disabled) {
    color: var(--tm-danger);
    border-color: color-mix(in srgb, var(--tm-danger) 45%, var(--tm-line));
  }
  @media (max-width: 520px) {
    li {
      grid-template-columns: 1fr;
    }
  }
</style>
