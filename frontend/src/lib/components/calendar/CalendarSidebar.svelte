<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Rss from '@lucide/svelte/icons/rss';
  import Server from '@lucide/svelte/icons/server';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Star from '@lucide/svelte/icons/star';
  import Trash from '@lucide/svelte/icons/trash-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Cake from '@lucide/svelte/icons/cake';
  import Lock from '@lucide/svelte/icons/lock';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { PimCalendar } from '../../types';
  import { isPlainHttpUrl } from '../../insecure';
  import Modal from '../common/Modal.svelte';

  interface Props {
    calendars: PimCalendar[];
    onchanged: () => void;
  }
  let { calendars, onchanged }: Props = $props();

  const groups = $derived.by(() => {
    const m = new Map<string, PimCalendar[]>();
    for (const c of calendars) {
      const key = c.accountName ?? (c.kind === 'subscription' ? t('calendar.kind.subscription') : t('calendar.kind.local'));
      m.set(key, [...(m.get(key) ?? []), c]);
    }
    return [...m];
  });

  let dialog = $state<'local' | 'subscription' | 'dav' | null>(null);
  let edit = $state<PimCalendar | null>(null);
  let name = $state('');
  let color = $state('#0891b2');
  let url = $state('');
  let refresh = $state(360);
  let user = $state('');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let syncing = $state(false);

  function openNew(kind: 'local' | 'subscription' | 'dav') {
    dialog = kind;
    edit = null;
    name = '';
    color = ['#0891b2', '#16a34a', '#ea580c', '#db2777', '#ca8a04'][calendars.length % 5];
    url = '';
    refresh = 360;
    user = '';
    password = '';
    error = '';
  }

  function openEdit(c: PimCalendar) {
    edit = c;
    dialog = c.kind === 'subscription' ? 'subscription' : 'local';
    name = c.name;
    color = c.color;
    url = c.url ?? '';
    refresh = c.refreshMinutes ?? 360;
    error = '';
  }

  async function toggle(c: PimCalendar) {
    await app.api.saveCalendar({ id: c.id, name: c.name, visible: !c.visible });
    onchanged();
  }

  async function submit() {
    busy = true;
    error = '';
    try {
      if (dialog === 'dav') {
        const plain = isPlainHttpUrl(url);
        if (plain && !window.confirm(t('onboarding.account.insecureWarning'))) {
          busy = false;
          return;
        }
        await app.api.addDavAccount({ displayName: name, url, username: user, password, allowInsecure: plain ? true : undefined });
        app.toast(t('calendar.dav.added'));
      } else if (edit) {
        await app.api.saveCalendar({ id: edit.id, name, color, url: dialog === 'subscription' ? url : undefined, refreshMinutes: refresh });
      } else {
        await app.api.saveCalendar({ name, color, kind: dialog === 'subscription' ? 'subscription' : 'local', url: url || undefined, refreshMinutes: refresh });
      }
      dialog = null;
      onchanged();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function setDefault(c: PimCalendar) {
    await app.api.saveCalendar({ id: c.id, name: c.name, isDefault: true });
    onchanged();
  }

  async function remove() {
    if (!edit) return;
    await app.api.deleteCalendar(edit.id);
    dialog = null;
    onchanged();
  }

  async function sync() {
    syncing = true;
    try {
      await app.api.syncCalendars();
      onchanged();
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    } finally {
      syncing = false;
    }
  }
</script>

<div class="cl">
  <div class="hd">
    <span class="label">{t('calendar.calendars')}</span>
    <button class="icon-btn sm" title={t('calendar.sync')} aria-label={t('calendar.sync')} onclick={sync} class:spin={syncing}><RefreshCw size={14} /></button>
  </div>
  {#each groups as [group, list] (group)}
    <div class="grp">{group}</div>
    <ul>
      {#each list as c (c.id)}
        <li>
          <label class="row" title={c.error || c.url || ''}>
            <input type="checkbox" checked={c.visible} style:--c={c.color} onchange={() => toggle(c)} />
            <span class="nm">{c.name}</span>
            {#if c.kind === 'birthdays'}<Cake size={13} />{:else if c.readOnly}<Lock size={12} />{/if}
            {#if c.isDefault}<Star size={12} />{/if}
            {#if c.error}<span class="err" title={c.error}><TriangleAlert size={13} /></span>{/if}
          </label>
          {#if c.kind === 'local' || c.kind === 'subscription' || c.kind === 'caldav'}
            <button class="more" aria-label={t('calendar.editCalendar', { name: c.name })} onclick={() => openEdit(c)}>⋯</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/each}
  <div class="adds">
    <button class="btn btn-ghost" onclick={() => openNew('local')}><Plus size={14} /> {t('calendar.addCalendar')}</button>
    <button class="btn btn-ghost" onclick={() => openNew('subscription')}><Rss size={14} /> {t('calendar.subscribe')}</button>
    <button class="btn btn-ghost" onclick={() => openNew('dav')}><Server size={14} /> {t('calendar.dav.title')}</button>
  </div>
</div>

{#if dialog}
  <Modal title={dialog === 'dav' ? t('calendar.dav.title') : edit ? t('calendar.editCalendarTitle') : dialog === 'subscription' ? t('calendar.subscribe') : t('calendar.addCalendar')} onclose={() => (dialog = null)} width="460px">
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      {#if dialog === 'dav'}
        <p class="hint">{t('calendar.dav.hint')}</p>
        <label><span class="label">{t('calendar.dav.name')}</span><input class="input" bind:value={name} placeholder={t('calendar.dav.namePlaceholder')} data-autofocus /></label>
        <label><span class="label">{t('calendar.dav.url')}</span><input class="input" type="url" bind:value={url} placeholder="https://" required /></label>
        <label><span class="label">{t('calendar.dav.username')}</span><input class="input" bind:value={user} autocomplete="username" required /></label>
        <label><span class="label">{t('calendar.dav.password')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" /></label>
      {:else}
        <label><span class="label">{t('calendar.calendarName')}</span><input class="input" bind:value={name} data-autofocus required={dialog === 'local'} disabled={edit?.kind === 'caldav'} /></label>
        <label class="colr"><span class="label">{t('calendar.color')}</span><input type="color" bind:value={color} /></label>
        {#if dialog === 'subscription'}
          <label><span class="label">{t('calendar.subscribeUrl')}</span><input class="input" bind:value={url} placeholder={t('calendar.subscribePlaceholder')} required /></label>
          <label>
            <span class="label">{t('calendar.refreshEvery')}</span>
            <select class="input" bind:value={refresh}>
              {#each [60, 360, 720, 1440, 10080] as m (m)}<option value={m}>{m < 1440 ? t('calendar.hours', { n: m / 60 }) : t('calendar.days', { n: m / 1440 })}</option>{/each}
            </select>
          </label>
        {/if}
        {#if edit}
          <dl class="info">
            <dt>{t('calendar.kindLabel')}</dt>
            <dd>{tk(`calendar.kind.${edit.kind}`)}{edit.accountName ? ` · ${edit.accountName}` : ''}</dd>
            {#if edit.lastSync}<dt>{t('calendar.lastSync')}</dt><dd>{fmt().dateTime(edit.lastSync)}</dd>{/if}
            {#if edit.error}<dt>{t('calendar.syncError')}</dt><dd class="errtxt">{edit.error}</dd>{/if}
          </dl>
          {#if !edit.readOnly && !edit.isDefault}
            <button type="button" class="btn btn-ghost" onclick={() => edit && setDefault(edit)}><Star size={14} /> {t('calendar.setDefault')}</button>
          {/if}
        {/if}
      {/if}
      {#if error}<p class="errtxt" role="alert">{error}</p>{/if}
      <button type="submit" hidden aria-hidden="true"></button>
    </form>
    {#snippet footer()}
      {#if edit && (edit.kind === 'local' || edit.kind === 'subscription')}
        <button class="btn btn-ghost danger" onclick={remove}><Trash size={14} /> {t('common.action.delete')}</button>
      {/if}
      <span class="sp"></span>
      <button class="btn" onclick={() => (dialog = null)}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={busy} onclick={submit}>{dialog === 'dav' ? t('calendar.dav.connect') : t('common.action.save')}</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .cl {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .hd {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 6px;
  }
  .hd .label {
    margin: 0;
  }
  .sm {
    width: 26px;
    height: 26px;
  }
  .spin :global(svg) {
    animation: spin 1s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .grp {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--tm-faint);
    margin: 8px 0 2px 4px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    border-radius: 6px;
  }
  li:hover {
    background: var(--tm-accent-soft);
  }
  .row {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    font-size: 13px;
    cursor: pointer;
    color: var(--tm-muted);
  }
  .row input {
    accent-color: var(--c);
    width: 15px;
    height: 15px;
  }
  .nm {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--tm-fg);
  }
  .err {
    color: var(--tm-warn);
    display: inline-flex;
  }
  .more {
    border: none;
    background: none;
    color: var(--tm-muted);
    width: 26px;
    height: 26px;
    border-radius: 6px;
    cursor: pointer;
    opacity: 0;
  }
  li:hover .more,
  .more:focus-visible {
    opacity: 1;
  }
  .adds {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    margin-top: 8px;
  }
  .adds .btn {
    height: 28px;
    font-size: 12.5px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .colr input {
    width: 54px;
    height: 32px;
    border: none;
    background: none;
    padding: 0;
  }
  .hint {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .info {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 12px;
    font-size: 12.5px;
    margin: 0;
  }
  .info dt {
    color: var(--tm-muted);
  }
  .info dd {
    margin: 0;
    word-break: break-word;
  }
  .errtxt {
    color: var(--tm-danger);
    font-size: 13px;
    margin: 0;
  }
  .sp {
    flex: 1;
  }
  .danger {
    color: var(--tm-danger);
  }
  @media (max-width: 639px) {
    .more {
      opacity: 1;
    }
  }
</style>
