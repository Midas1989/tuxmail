<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Administration -> Groups: named sets of users (used for shared mailbox
  permissions and provisioning). List, create, delete, add and remove members.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import X from '@lucide/svelte/icons/x';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import { t } from '../../i18n/index.svelte';
  import type { AdminGroup, AdminUser } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';
  import type { AdminPanelProps } from '../../admin/panel';

  let { client, notify }: AdminPanelProps = $props();

  let groups = $state<AdminGroup[]>([]);
  let users = $state<AdminUser[]>([]);
  let loading = $state(true);
  let loadError = $state('');
  let newName = $state('');
  let newDesc = $state('');
  let formError = $state('');
  let busy = $state(false);
  let deleting = $state<AdminGroup | null>(null);
  /** Chosen user per group for the "add member" select. */
  let pick = $state<Record<number, string>>({});

  async function load() {
    loading = true;
    try {
      [groups, users] = await Promise.all([client.listGroups(), client.listUsers()]);
      loadError = '';
    } catch (err) {
      loadError = adminErrorMessage(err, t);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load());

  const userName = (id: number) => users.find((u) => u.id === id)?.username ?? `#${id}`;
  const candidates = (g: AdminGroup) => users.filter((u) => !g.members.includes(u.id));

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !newName.trim()) return;
    busy = true;
    formError = '';
    try {
      const g = await client.createGroup(newName.trim(), newDesc.trim());
      newName = '';
      newDesc = '';
      notify(t('admin.toast.groupCreated', { name: g.name }));
      await load();
    } catch (err) {
      formError = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  async function change(call: () => Promise<void>) {
    try {
      await call();
    } catch (err) {
      notify(adminErrorMessage(err, t), 'error');
    }
    await load();
  }

  async function add(g: AdminGroup) {
    const uid = Number(pick[g.id]);
    if (!uid) return;
    pick[g.id] = '';
    await change(() => client.addMember(g.id, uid));
  }

  async function remove(g: AdminGroup) {
    deleting = null;
    await change(async () => {
      await client.deleteGroup(g.id);
      notify(t('admin.toast.groupDeleted', { name: g.name }));
    });
  }
</script>

<div class="groups">
  <form class="create" onsubmit={create} aria-label={t('admin.groups.create')}>
    <div class="f">
      <label class="label" for="grp-name">{t('admin.groups.name')}</label>
      <input id="grp-name" class="input" bind:value={newName} maxlength="100" autocomplete="off" required />
    </div>
    <div class="f wide">
      <label class="label" for="grp-desc">{t('admin.groups.description')}</label>
      <input id="grp-desc" class="input" bind:value={newDesc} autocomplete="off" />
    </div>
    <button class="btn btn-primary" disabled={busy || !newName.trim()} data-testid="group-create"><Plus size={15} /> {t('admin.groups.create')}</button>
  </form>
  {#if formError}<p class="error" role="alert">{formError}</p>{/if}

  <div class="head">
    <h3>{t('admin.groups.listTitle')}</h3>
    <button class="icon-btn" onclick={() => void load()} aria-label={t('admin.refresh')} title={t('admin.refresh')} disabled={loading}><RefreshCw size={16} /></button>
  </div>

  {#if loadError}
    <p class="error" role="alert">{t('admin.loadFailed', { error: loadError })}</p>
  {:else if loading && !groups.length}
    <p class="muted" role="status">{t('admin.loading')}</p>
  {:else if !groups.length}
    <p class="muted" role="status">{t('admin.groups.empty')}</p>
  {:else}
    <ul class="list" aria-label={t('admin.groups.listTitle')}>
      {#each groups as g (g.id)}
        <li class="grp" data-group={g.name}>
          <div class="top">
            <div>
              <strong>{g.name}</strong>
              <span class="muted">{t('admin.groups.memberCount', { count: g.members.length })}</span>
              {#if g.description}<div class="muted">{g.description}</div>{/if}
            </div>
            <button class="btn" onclick={() => (deleting = g)} aria-label={t('admin.groups.deleteFor', { name: g.name })}><Trash size={14} /> {t('common.action.delete')}</button>
          </div>
          {#if g.members.length}
            <ul class="chips" aria-label={t('admin.groups.members', { name: g.name })}>
              {#each g.members as uid (uid)}
                <li class="chip">
                  {userName(uid)}
                  <button class="x" onclick={() => change(() => client.removeMember(g.id, uid))} aria-label={t('admin.groups.removeMember', { user: userName(uid), group: g.name })}><X size={12} /></button>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="muted">{t('admin.groups.noMembers')}</p>
          {/if}
          <div class="addrow">
            <select class="input" bind:value={pick[g.id]} aria-label={t('admin.groups.addTo', { name: g.name })}>
              <option value="">{t('admin.groups.chooseUser')}</option>
              {#each candidates(g) as u (u.id)}<option value={String(u.id)}>{u.username}{u.displayName ? ` (${u.displayName})` : ''}</option>{/each}
            </select>
            <button class="btn" disabled={!pick[g.id]} onclick={() => void add(g)} aria-label={t('admin.groups.addMemberTo', { name: g.name })}>{t('admin.groups.add')}</button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

{#if deleting}
  {@const g = deleting}
  <ConfirmDialog title={t('admin.groups.delete')} message={t('admin.groups.deleteConfirm', { name: g.name })} confirmLabel={t('admin.groups.delete')} danger onconfirm={() => void remove(g)} oncancel={() => (deleting = null)} />
{/if}

<style>
  .groups {
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
    min-width: 160px;
  }
  .f.wide {
    flex: 1 1 240px;
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
  .muted {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
    font-size: 13px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .grp {
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .top {
    display: flex;
    justify-content: space-between;
    gap: 10px;
    align-items: flex-start;
  }
  .chips {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 4px 2px 10px;
    border-radius: 999px;
    background: var(--tm-surface-3);
    font-size: 13px;
  }
  .x {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--tm-muted);
    cursor: pointer;
  }
  .x:hover {
    background: var(--tm-line);
    color: var(--tm-fg);
  }
  .addrow {
    display: flex;
    gap: 8px;
  }
  .addrow .input {
    max-width: 280px;
  }
</style>
