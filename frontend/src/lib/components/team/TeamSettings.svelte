<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Team mailboxes (server mode): administrators create shared
     mailboxes with their account; administrators and managers grant users
     and groups the roles read, reply, assign and manage (KONZEPT 8.14, 15.6). -->
<script lang="ts">
  import { isNativeShell } from '../../platform';
  import BrowserOnlyHint from '../common/BrowserOnlyHint.svelte';
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import Users from '@lucide/svelte/icons/users';
  import ChartColumn from '@lucide/svelte/icons/chart-column';
  import { onMount } from 'svelte';
  import { app, team } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { TeamAdminEntry, TeamDirectory, TeamGrant, TeamRole, TeamStats } from '../../api/team';
  import type { NewAccountInput } from '../../types';

  const roles: TeamRole[] = ['read', 'reply', 'assign', 'manage'];
  let isAdmin = $state(false);
  let entries = $state<TeamAdminEntry[]>([]);
  let grants = $state<Record<number, TeamGrant[]>>({});
  let dir = $state<TeamDirectory>({ users: [], groups: [] });
  let stats = $state<Record<number, TeamStats>>({});
  let creating = $state(false);
  let busy = $state(false);
  let form = $state({ name: '', address: '', description: '', sharedDrafts: true, withAccount: true, password: '', imapHost: '', imapPort: 993, smtpHost: '', smtpPort: 465 });
  let add = $state<Record<number, { who: string; role: TeamRole }>>({});

  const managed = $derived(
    isAdmin
      ? entries.map((e) => ({ id: e.mailbox.id, name: e.mailbox.name, address: e.mailbox.address }))
      : team.mailboxes.filter((m) => m.role === 'manage').map((m) => ({ id: m.id, name: m.name, address: m.address })),
  );

  async function load() {
    const c = team.client;
    if (!c) return;
    try {
      entries = await c.adminList();
      isAdmin = true;
    } catch {
      isAdmin = false;
    }
    try {
      dir = await c.directory();
    } catch {
      dir = { users: [], groups: [] };
    }
    for (const m of managed) {
      grants[m.id] = await c.grants(m.id).catch(() => []);
      add[m.id] ??= { who: '', role: 'reply' };
    }
  }
  onMount(() => void load());

  async function create() {
    const c = team.client;
    if (!c || !form.name.trim()) return;
    busy = true;
    try {
      let account: NewAccountInput | undefined;
      if (form.withAccount && form.address) {
        account = {
          kind: 'imap', displayName: form.name, email: form.address, name: form.name, password: form.password,
          incoming: { host: form.imapHost, port: form.imapPort, tls: 'tls', auth: 'password', username: form.address },
          outgoing: { host: form.smtpHost, port: form.smtpPort, tls: 'tls', auth: 'password', username: form.address },
        };
      }
      await c.createMailbox({ name: form.name.trim(), address: form.address.trim(), description: form.description, sharedDrafts: form.sharedDrafts }, account);
      creating = false;
      form = { ...form, name: '', address: '', description: '', password: '' };
      await team.loadMailboxes();
      await load();
      app.toast(t('team.admin.created'));
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    } finally {
      busy = false;
    }
  }

  async function patch(id: number, p: { sharedDrafts?: boolean; disabled?: boolean }) {
    await team.client?.updateMailbox(id, p);
    await load();
  }

  async function remove(id: number, name: string) {
    if (!confirm(t('team.admin.deleteConfirm', { name }))) return;
    await team.client?.deleteMailbox(id);
    await team.loadMailboxes();
    await load();
  }

  async function setRole(id: number, g: TeamGrant, role: TeamRole | '') {
    const c = team.client;
    if (!c) return;
    grants[id] = await c.setGrant(id, g.userId ? { userId: g.userId } : { groupId: g.groupId }, role);
  }

  async function addGrant(id: number) {
    const c = team.client;
    const a = add[id];
    if (!c || !a?.who) return;
    const [kind, raw] = a.who.split(':');
    const n = Number(raw);
    grants[id] = await c.setGrant(id, kind === 'g' ? { groupId: n } : { userId: n }, a.role);
    add[id] = { who: '', role: a.role };
  }

  async function showStats(id: number) {
    const s = await team.client?.stats(id);
    if (s) stats[id] = s;
  }

  function minutes(sec: number): string {
    return sec < 3600 ? t('team.stats.minutes', { n: Math.round(sec / 60) }) : t('team.stats.hours', { n: (sec / 3600).toFixed(1) });
  }

  const native = isNativeShell();
</script>

{#if team.client && (isAdmin || managed.length)}
  <section class="card wide" id="set-team" aria-labelledby="set-team-h">
    <div class="head">
      <h2 id="set-team-h"><Users size={16} /> {t('team.admin.title')}</h2>
      {#if isAdmin}<button class="btn" onclick={() => (creating = !creating)}><Plus size={14} /> {t('team.admin.create')}</button>{/if}
    </div>
    <p class="hint">{t('team.admin.intro')}</p>

    {#if creating}
      <form class="create" onsubmit={(e) => (e.preventDefault(), void create())}>
        <label>{t('team.admin.name')}<input class="input" bind:value={form.name} required placeholder={t('team.admin.namePh')} /></label>
        <label>{t('team.admin.address')}<input class="input" type="email" bind:value={form.address} placeholder={t('team.admin.addressPh')} /></label>
        <label class="full">{t('team.admin.description')}<input class="input" bind:value={form.description} /></label>
        <label class="check"><input type="checkbox" bind:checked={form.sharedDrafts} /> {t('team.admin.sharedDrafts')}</label>
        <label class="check"><input type="checkbox" bind:checked={form.withAccount} /> {t('team.admin.withAccount')}</label>
        {#if form.withAccount}
          <label>{t('team.admin.password')}<input class="input" type="password" bind:value={form.password} autocomplete="new-password" /></label>
          <span></span>
          <label>{t('team.admin.imap')}<input class="input" bind:value={form.imapHost} placeholder={t('team.admin.imapPh')} /></label>
          <label>{t('team.admin.port')}<input class="input" type="number" bind:value={form.imapPort} /></label>
          <label>{t('team.admin.smtp')}<input class="input" bind:value={form.smtpHost} placeholder={t('team.admin.smtpPh')} /></label>
          <label>{t('team.admin.port')}<input class="input" type="number" bind:value={form.smtpPort} /></label>
        {/if}
        <div class="full actions">
          <button type="button" class="btn btn-ghost" onclick={() => (creating = false)}>{t('common.action.cancel')}</button>
          <button class="btn btn-primary" disabled={busy || !form.name.trim()}>{t('team.admin.create')}</button>
        </div>
      </form>
    {/if}

    <ul class="boxes">
      {#each managed as m (m.id)}
        {@const e = entries.find((x) => x.mailbox.id === m.id)}
        <li>
          <div class="box-head">
            <div class="who">
              <strong>{m.name}</strong>
              <span class="addr">{[m.address, e ? t('team.admin.accounts', { count: e.accounts.length }) : '', e?.mailbox.disabled ? t('team.admin.disabled') : ''].filter(Boolean).join(' · ')}</span>
            </div>
            <button class="icon-btn" title={t('team.stats.title')} aria-label={t('team.stats.title')} onclick={() => showStats(m.id)}><ChartColumn size={16} /></button>
            {#if e && isAdmin}
              <label class="check small"><input type="checkbox" checked={e.mailbox.sharedDrafts} onchange={(ev) => patch(m.id, { sharedDrafts: (ev.currentTarget as HTMLInputElement).checked })} /> {t('team.admin.sharedDraftsShort')}</label>
              <label class="check small"><input type="checkbox" checked={!e.mailbox.disabled} onchange={(ev) => patch(m.id, { disabled: !(ev.currentTarget as HTMLInputElement).checked })} /> {t('team.admin.active')}</label>
              <button class="icon-btn" aria-label={t('team.admin.delete', { name: m.name })} onclick={() => remove(m.id, m.name)}><Trash size={15} /></button>
            {/if}
          </div>
          {#if stats[m.id]}
            {@const s = stats[m.id]}
            <dl class="stats">
              <div><dt>{t('team.stats.total')}</dt><dd>{s.total}</dd></div>
              <div><dt>{t('team.status.open')}</dt><dd>{s.open}</dd></div>
              <div><dt>{t('team.status.in_progress')}</dt><dd>{s.inProgress}</dd></div>
              <div><dt>{t('team.status.waiting')}</dt><dd>{s.waiting}</dd></div>
              <div><dt>{t('team.status.done')}</dt><dd>{s.done}</dd></div>
              <div><dt>{t('team.unassigned')}</dt><dd>{s.unassigned}</dd></div>
              <div><dt>{t('team.stats.median')}</dt><dd>{s.responded ? minutes(s.medianFirstResponseSeconds) : '–'}</dd></div>
            </dl>
          {/if}
          {#if native}<BrowserOnlyHint what={t('errors.teamGrantsTitle')} />{:else}
          <table class="grants">
            <thead><tr><th>{t('team.admin.member')}</th><th>{t('team.admin.role')}</th><th></th></tr></thead>
            <tbody>
              {#each grants[m.id] ?? [] as g (`${g.userId ?? 0}-${g.groupId ?? 0}`)}
                <tr>
                  <td>{#if g.groupId}<span class="grp">{t('team.admin.group')}</span>{/if} {g.name}</td>
                  <td>
                    <select class="input sel" value={g.role} onchange={(ev) => setRole(m.id, g, (ev.currentTarget as HTMLSelectElement).value as TeamRole)} aria-label={t('team.admin.role')}>
                      {#each roles as r (r)}<option value={r}>{t(`team.role.${r}`)}</option>{/each}
                    </select>
                  </td>
                  <td><button class="icon-btn" aria-label={t('team.admin.removeMember', { name: g.name })} onclick={() => setRole(m.id, g, '')}><Trash size={14} /></button></td>
                </tr>
              {/each}
              {#if add[m.id]}
                <tr class="add">
                  <td>
                    <select class="input sel" bind:value={add[m.id].who} aria-label={t('team.admin.member')}>
                      <option value="">{t('team.admin.pick')}</option>
                      <optgroup label={t('team.admin.groups')}>{#each dir.groups as g (g.id)}<option value={`g:${g.id}`}>{g.name}</option>{/each}</optgroup>
                      <optgroup label={t('team.admin.users')}>{#each dir.users as u (u.id)}<option value={`u:${u.id}`}>{u.displayName || u.name} ({u.name})</option>{/each}</optgroup>
                    </select>
                  </td>
                  <td>
                    <select class="input sel" bind:value={add[m.id].role} aria-label={t('team.admin.role')}>
                      {#each roles as r (r)}<option value={r}>{t(`team.role.${r}`)}</option>{/each}
                    </select>
                  </td>
                  <td><button class="btn" disabled={!add[m.id].who} onclick={() => addGrant(m.id)}><Plus size={14} /> {t('team.admin.add')}</button></td>
                </tr>
              {/if}
            </tbody>
          </table>
          {/if}
        </li>
      {/each}
    </ul>
    <p class="hint">{t('team.admin.rolesHint')}</p>
  </section>
{/if}

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .wide {
    grid-column: 1 / -1;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .hint {
    margin: 6px 0 12px;
    color: var(--tm-muted);
    font-size: 13px;
  }
  .create {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px 12px;
    margin-bottom: 14px;
    padding: 12px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2);
  }
  .create label {
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .create .full {
    grid-column: 1 / -1;
  }
  .check {
    flex-direction: row !important;
    align-items: center;
    gap: 6px !important;
    color: var(--tm-fg) !important;
    font-size: 13px !important;
  }
  .check.small {
    display: flex;
    font-size: 12px !important;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .boxes {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 12px;
  }
  .boxes > li {
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .box-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .who {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .addr {
    color: var(--tm-muted);
    font-size: 12px;
  }
  .grants {
    width: 100%;
    margin-top: 8px;
    border-collapse: collapse;
    font-size: 13px;
  }
  .grants th {
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    color: var(--tm-faint);
    text-transform: uppercase;
    padding: 4px 6px;
  }
  .grants td {
    padding: 3px 6px;
    border-top: 1px solid var(--tm-line);
  }
  .sel {
    height: 30px;
    font-size: 13px;
  }
  .grp {
    padding: 0 5px;
    border-radius: 4px;
    background: var(--tm-surface-3);
    font-size: 11px;
  }
  .stats {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 10px 0 4px;
  }
  .stats div {
    padding: 6px 10px;
    border-radius: 8px;
    background: var(--tm-surface-2);
  }
  .stats dt {
    font-size: 11px;
    color: var(--tm-muted);
  }
  .stats dd {
    margin: 0;
    font-size: 16px;
    font-weight: 650;
  }
</style>
