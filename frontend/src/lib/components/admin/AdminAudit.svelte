<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Administration -> Audit log (read only): who did what, newest first.
  Filter by action prefix ("user.", "login"), by actor and by failures; older
  entries are loaded page by page (GET /api/admin/audit?before=<id>).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import { t, fmt } from '../../i18n/index.svelte';
  import type { AdminAuditEntry, AuditQuery } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';
  import { AUDIT_ACTION_PREFIXES, AUDIT_PAGE_SIZE } from '../../admin/audit';
  import type { AdminPanelProps } from '../../admin/panel';

  let { client }: AdminPanelProps = $props();

  let entries = $state<AdminAuditEntry[]>([]);
  let action = $state('');
  let actor = $state('');
  let onlyFailed = $state(false);
  /** The filter the shown list was loaded with (the inputs may have changed since). */
  let applied = $state<AuditQuery>({});
  let loading = $state(true);
  let more = $state(false);
  let error = $state('');

  async function load(append: boolean) {
    loading = true;
    try {
      const q: AuditQuery = append
        ? { ...applied, before: entries[entries.length - 1]?.id }
        : { action: action.trim(), actor: actor.trim(), onlyFailed };
      const page = await client.auditLog({ ...q, limit: AUDIT_PAGE_SIZE });
      if (!append) applied = { action: q.action, actor: q.actor, onlyFailed: q.onlyFailed };
      entries = append ? [...entries, ...page] : page;
      more = page.length === AUDIT_PAGE_SIZE;
      error = '';
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      loading = false;
    }
  }
  onMount(() => void load(false));
</script>

<div class="audit">
  <form class="filters" onsubmit={(e) => (e.preventDefault(), void load(false))} role="search" aria-label={t('admin.audit.filters')}>
    <div class="f">
      <label class="label" for="aud-action">{t('admin.audit.action')}</label>
      <input id="aud-action" class="input" list="aud-actions" bind:value={action} placeholder={t('admin.audit.actionPlaceholder')} autocomplete="off" autocapitalize="none" spellcheck="false" />
      <datalist id="aud-actions">
        {#each AUDIT_ACTION_PREFIXES as p (p)}<option value={p}></option>{/each}
      </datalist>
    </div>
    <div class="f">
      <label class="label" for="aud-actor">{t('admin.audit.actor')}</label>
      <input id="aud-actor" class="input" bind:value={actor} autocomplete="off" autocapitalize="none" spellcheck="false" />
    </div>
    <label class="check"><input type="checkbox" bind:checked={onlyFailed} /> {t('admin.audit.onlyFailed')}</label>
    <button class="btn btn-primary" disabled={loading} data-testid="audit-apply">{t('admin.audit.apply')}</button>
    <button class="icon-btn" type="button" onclick={() => void load(false)} aria-label={t('admin.refresh')} title={t('admin.refresh')} disabled={loading}><RefreshCw size={16} /></button>
  </form>

  {#if error}
    <p class="error" role="alert">{t('admin.loadFailed', { error })}</p>
  {:else if loading && !entries.length}
    <p class="muted" role="status">{t('admin.loading')}</p>
  {:else if !entries.length}
    <p class="muted" role="status">{t('admin.audit.empty')}</p>
  {:else}
    <p class="muted" role="status" aria-live="polite">{t('admin.audit.count', { count: entries.length })}</p>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (a scrollable region must be reachable by keyboard) -->
    <div class="scroll" role="region" aria-label={t('admin.audit.title')} tabindex="0">
      <table class="tbl" data-testid="audit-table">
        <caption class="sr-only">{t('admin.audit.title')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('admin.audit.time')}</th>
            <th scope="col">{t('admin.audit.actor')}</th>
            <th scope="col">{t('admin.audit.action')}</th>
            <th scope="col">{t('admin.audit.target')}</th>
            <th scope="col">{t('admin.audit.result')}</th>
            <th scope="col">{t('admin.audit.address')}</th>
            <th scope="col">{t('admin.audit.details')}</th>
          </tr>
        </thead>
        <tbody>
          {#each entries as e (e.id)}
            <tr class:fail={!e.success}>
              <td class="nowrap">{fmt().dateTime(e.at)}</td>
              <td>{e.actor || '–'}</td>
              <td class="mono">{e.action}</td>
              <td class="wrap">{e.target || '–'}</td>
              <td><span class="res" class:bad={!e.success}>{e.success ? t('admin.audit.ok') : t('admin.audit.failed')}</span></td>
              <td class="mono">{e.ip || '–'}</td>
              <td class="wrap">{e.details || '–'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if more}
      <div><button class="btn" disabled={loading} onclick={() => void load(true)} data-testid="audit-more">{t('admin.audit.more')}</button></div>
    {/if}
  {/if}
</div>

<style>
  .audit {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 10px;
  }
  .f {
    min-width: 150px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 34px;
    font-size: 13.5px;
  }
  .muted {
    margin: 0;
    font-size: 13px;
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
  .scroll {
    overflow-x: auto;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .tbl {
    width: 100%;
    min-width: 860px;
    border-collapse: collapse;
    font-size: 12.5px;
  }
  .tbl th,
  .tbl td {
    padding: 6px 10px;
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
  .nowrap {
    white-space: nowrap;
  }
  .wrap {
    overflow-wrap: anywhere;
  }
  .mono {
    font-family: var(--tm-mono);
    font-size: 12px;
  }
  .res.bad {
    color: var(--tm-danger);
    font-weight: 600;
  }
</style>
