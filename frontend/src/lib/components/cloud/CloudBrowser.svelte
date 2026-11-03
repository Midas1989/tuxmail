<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Breadcrumb file browser of one cloud account (KONZEPT 12.1): used by the
  Files screen, the composer's "Attach from cloud" picker and the "Save to
  cloud" folder picker.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import FolderIcon from '@lucide/svelte/icons/folder';
  import FileIcon from '@lucide/svelte/icons/file';
  import FileText from '@lucide/svelte/icons/file-text';
  import ImageIcon from '@lucide/svelte/icons/image';
  import HardDrive from '@lucide/svelte/icons/hard-drive';
  import House from '@lucide/svelte/icons/house';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app } from '../../state';
  import { breadcrumbs } from '../../state/cloud.svelte';
  import { formatListDate, formatSize, t } from '../../i18n/index.svelte';
  import type { CloudEntry } from '../../types';

  interface Props {
    accountId: number;
    path?: string;
    /** files: open folders, click files; pick: checkbox per file; folders: only folders are shown. */
    mode?: 'files' | 'pick' | 'folders';
    selected?: CloudEntry[];
    /** Changing it reloads the listing. */
    reloadKey?: number;
    onfile?: (e: CloudEntry) => void;
    actions?: Snippet<[CloudEntry]>;
    compact?: boolean;
  }
  let { accountId, path = $bindable('/'), mode = 'files', selected = $bindable([]), reloadKey = 0, onfile, actions, compact = false }: Props = $props();

  let entries = $state<CloudEntry[]>([]);
  let loading = $state(false);
  let error = $state<string | null>(null);
  /** Display names of path segments (OpenCloud space ids -> names). */
  let names = $state<Record<string, string>>({});

  $effect(() => {
    const p = path;
    const acc = accountId;
    void reloadKey;
    void load(acc, p);
  });

  async function load(acc: number, p: string) {
    loading = true;
    error = null;
    try {
      const l = await app.api.listCloudFiles(acc, p);
      if (acc !== accountId || p !== path) return;
      entries = mode === 'folders' ? l.entries.filter((e) => e.dir) : l.entries;
      for (const e of l.entries) if (e.dir && e.name) names[e.path] = e.name;
    } catch (err) {
      entries = [];
      error = t('files.loadFailed', { error: err instanceof Error ? err.message : String(err) });
    } finally {
      loading = false;
    }
  }

  function open(e: CloudEntry) {
    if (e.dir) {
      path = e.path;
      return;
    }
    if (mode === 'pick') toggle(e);
    else onfile?.(e);
  }

  function toggle(e: CloudEntry) {
    selected = selected.some((s) => s.path === e.path) ? selected.filter((s) => s.path !== e.path) : [...selected, e];
  }

  function iconFor(e: CloudEntry) {
    if (e.dir) return e.contentType?.startsWith('space/') ? HardDrive : FolderIcon;
    if (e.contentType?.startsWith('image/')) return ImageIcon;
    if (e.contentType?.includes('pdf') || e.contentType?.includes('text') || e.contentType?.includes('document')) return FileText;
    return FileIcon;
  }

  const crumbs = $derived(breadcrumbs(path));
</script>

<div class="browser" class:compact>
  <nav class="crumbs" aria-label={t('files.breadcrumb')}>
    {#each crumbs as c, i (c.path)}
      {#if i > 0}<ChevronRight size={14} class="sep" aria-hidden="true" />{/if}
      <button class="crumb" class:current={i === crumbs.length - 1} onclick={() => (path = c.path)} aria-current={i === crumbs.length - 1 ? 'location' : undefined}>
        {#if i === 0}<House size={14} /><span class="sr-only">{t('files.root')}</span>{:else}{names[c.path] ?? c.name}{/if}
      </button>
    {/each}
    {#if loading}<LoaderCircle size={14} class="spin" aria-label={t('files.loading')} />{/if}
  </nav>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {:else if !loading && entries.length === 0}
    <p class="empty">{mode === 'folders' ? t('files.noSubfolders') : t('files.empty')}</p>
  {:else}
    <table class="list">
      <thead>
        <tr>
          {#if mode === 'pick'}<th class="chk"><span class="sr-only">{t('files.col.select')}</span></th>{/if}
          <th>{t('files.col.name')}</th>
          {#if !compact}<th class="num">{t('files.col.size')}</th><th class="date">{t('files.col.modified')}</th>{/if}
          {#if actions}<th class="act"><span class="sr-only">{t('files.col.actions')}</span></th>{/if}
        </tr>
      </thead>
      <tbody>
        {#each entries as e (e.path)}
          {@const Icon = iconFor(e)}
          <tr class:dir={e.dir} class:sel={selected.some((s) => s.path === e.path)}>
            {#if mode === 'pick'}
              <td class="chk">
                {#if !e.dir}<input type="checkbox" checked={selected.some((s) => s.path === e.path)} onchange={() => toggle(e)} aria-label={t('files.selectFile', { name: e.name })} />{/if}
              </td>
            {/if}
            <td class="name">
              <button class="open" onclick={() => open(e)} title={e.name}>
                <span class="ic" class:folder={e.dir}><Icon size={17} /></span>
                <span class="label">{e.name}</span>
              </button>
            </td>
            {#if !compact}
              <td class="num">{e.dir ? '' : formatSize(e.size)}</td>
              <td class="date">{e.modified ? formatListDate(e.modified) : ''}</td>
            {/if}
            {#if actions}<td class="act">{@render actions(e)}</td>{/if}
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .browser {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .crumb {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 4px 8px;
    border-radius: var(--tm-radius-sm);
    color: inherit;
  }
  .crumb:hover {
    background: var(--tm-surface-2);
    color: var(--tm-fg);
  }
  .crumb.current {
    color: var(--tm-fg);
    font-weight: 600;
  }
  .crumbs :global(.sep) {
    color: var(--tm-faint);
  }
  .list {
    width: 100%;
    border-collapse: collapse;
    font-size: 13.5px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    overflow: hidden;
  }
  th {
    text-align: left;
    font-weight: 600;
    font-size: 12px;
    color: var(--tm-muted);
    padding: 8px 12px;
    border-bottom: 1px solid var(--tm-line);
    background: var(--tm-surface-2);
  }
  td {
    padding: 2px 12px;
    border-bottom: 1px solid var(--tm-line);
    height: 40px;
  }
  tr:last-child td {
    border-bottom: 0;
  }
  tr:hover td,
  tr.sel td {
    background: var(--tm-accent-soft);
  }
  .compact td {
    height: 34px;
  }
  .num,
  .date {
    white-space: nowrap;
    color: var(--tm-muted);
    width: 1%;
  }
  .num {
    text-align: right;
  }
  .chk,
  .act {
    width: 1%;
    white-space: nowrap;
  }
  .name {
    max-width: 0;
    width: 100%;
  }
  .open {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    min-width: 0;
    text-align: left;
    color: var(--tm-fg);
    padding: 6px 0;
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ic {
    display: inline-flex;
    color: var(--tm-muted);
    flex: none;
  }
  .ic.folder {
    color: var(--tm-brand);
  }
  .dir .label {
    font-weight: 550;
  }
  .empty,
  .error {
    margin: 0;
    padding: 28px 12px;
    text-align: center;
    color: var(--tm-muted);
    background: var(--tm-surface);
    border: 1px dashed var(--tm-line-strong);
    border-radius: var(--tm-radius);
    font-size: 13px;
  }
  .error {
    color: var(--tm-danger);
  }
  @media (max-width: 639px) {
    .date {
      display: none;
    }
  }
</style>
