<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Inbox from '@lucide/svelte/icons/inbox';
  import Send from '@lucide/svelte/icons/send';
  import FilePen from '@lucide/svelte/icons/file-pen';
  import Archive from '@lucide/svelte/icons/archive';
  import Trash from '@lucide/svelte/icons/trash';
  import Ban from '@lucide/svelte/icons/ban';
  import FolderIcon from '@lucide/svelte/icons/folder';
  import Star from '@lucide/svelte/icons/star';
  import MailOpen from '@lucide/svelte/icons/mail-open';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import SquarePen from '@lucide/svelte/icons/square-pen';
  import FolderSearch from '@lucide/svelte/icons/folder-search';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Plus from '@lucide/svelte/icons/plus';
  import TagIcon from './common/TagIcon.svelte';
  import type { SearchFolder } from '../types';
  import { app } from '../state';
  import type { SidebarSel } from '../state';
  import { messengerChannel, type Account, type Folder, type FolderRole } from '../types';
  import { t } from '../i18n/index.svelte';
  import ChannelIcon from './common/ChannelIcon.svelte';

  let collapsed = $state<Record<string, boolean>>({});

  const roleIcon: Record<string, typeof Inbox> = {
    inbox: Inbox,
    sent: Send,
    drafts: FilePen,
    archive: Archive,
    trash: Trash,
    junk: Ban,
  };

  interface Node {
    folder: Folder;
    depth: number;
    hasChildren: boolean;
  }

  function flatten(folders: Folder[]): Node[] {
    const out: Node[] = [];
    const walk = (parentId: number | undefined, depth: number) => {
      for (const f of folders.filter((x) => (x.parentId ?? undefined) === parentId)) {
        const hasChildren = folders.some((x) => x.parentId === f.id);
        out.push({ folder: f, depth, hasChildren });
        if (hasChildren && !collapsed['f' + f.id]) walk(f.id, depth + 1);
      }
    };
    walk(undefined, 0);
    return out;
  }

  function isSel(s: SidebarSel): boolean {
    const c = app.sel;
    if (s.kind !== c.kind) return false;
    if (s.kind === 'unified' && c.kind === 'unified') return s.role === c.role;
    if (s.kind === 'folder' && c.kind === 'folder') return s.folderId === c.folderId;
    if (s.kind === 'account' && c.kind === 'account') return s.accountId === c.accountId;
    if (s.kind === 'tag' && c.kind === 'tag') return s.tagId === c.tagId;
    if (s.kind === 'searchFolder' && c.kind === 'searchFolder') return s.searchFolderId === c.searchFolderId;
    return false;
  }

  // Tags as a tree (paths "Kunde/Müller").
  const tagNodes = $derived.by(() => {
    const out: { id: number; depth: number; hasChildren: boolean; key: string }[] = [];
    const list = app.tags;
    const walk = (parentId: number | undefined, depth: number) => {
      for (const x of list.filter((y) => (y.tag.parentId ?? undefined) === parentId)) {
        const hasChildren = list.some((y) => y.tag.parentId === x.tag.id);
        out.push({ id: x.tag.id, depth, hasChildren, key: 't' + x.tag.id });
        if (hasChildren && !collapsed['t' + x.tag.id]) walk(x.tag.id, depth + 1);
      }
    };
    walk(undefined, 0);
    return out;
  });

  const globalSearchFolders = $derived(app.searchFolders.filter((f) => !f.accountId && !f.projectId));
  const activeProjects = $derived(app.projects.filter((p) => p.status === 'active' || p.status === 'planned' || p.status === 'paused'));

  // Hybrid view: drop a bubble (mail) onto a folder to move it.
  const DRAG_TYPE = 'application/x-tuxmail-message';
  let dropTarget = $state<number | null>(null);
  function onDragOver(e: DragEvent, folderId: number) {
    if (!e.dataTransfer?.types.includes(DRAG_TYPE)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    dropTarget = folderId;
  }
  function onDrop(e: DragEvent, folderId: number) {
    const id = Number(e.dataTransfer?.getData(DRAG_TYPE));
    dropTarget = null;
    if (!id) return;
    e.preventDefault();
    void app.moveItemToFolder(id, folderId);
  }

  function editSearchFolder(e: Event, f: SearchFolder) {
    e.stopPropagation();
    app.organizeDialog = { kind: 'searchFolder', folder: f };
  }

  function folderLabel(f: Folder): string {
    const role = f.role as FolderRole;
    return role ? t(`mail.folder.role.${role}` as `mail.folder.role.inbox`) : f.name;
  }

  function accountAddress(a: Account): string {
    return a.extra?.address ?? '';
  }

  const unifiedUnreadOnly = $derived(app.allFolders.filter((f) => f.role !== 'junk' && f.role !== 'trash').reduce((n, f) => n + f.unread, 0));

  // Roving keyboard navigation inside the tree (Up/Down/Left/Right/Home/End).
  let tree = $state<HTMLElement>();
  function onTreeKey(e: KeyboardEvent) {
    if (!tree) return;
    const items = [...tree.querySelectorAll<HTMLElement>('[role="treeitem"]')];
    const idx = items.indexOf(document.activeElement as HTMLElement);
    if (idx === -1) return;
    const focus = (i: number) => items[Math.max(0, Math.min(items.length - 1, i))]?.focus();
    const cur = items[idx];
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        focus(idx + 1);
        break;
      case 'ArrowUp':
        e.preventDefault();
        focus(idx - 1);
        break;
      case 'Home':
        e.preventDefault();
        focus(0);
        break;
      case 'End':
        e.preventDefault();
        focus(items.length - 1);
        break;
      case 'ArrowRight':
      case 'ArrowLeft': {
        const key = cur.dataset.key;
        if (key && cur.getAttribute('aria-expanded') !== null) {
          e.preventDefault();
          collapsed[key] = e.key === 'ArrowLeft';
        }
        break;
      }
    }
  }
</script>

{#snippet searchFolderRow(sf: SearchFolder, depth: number)}
  {@const s = { kind: 'searchFolder', searchFolderId: sf.id } as SidebarSel}
  <div class="sf-wrap">
    <button role="treeitem" aria-selected={isSel(s)} tabindex={isSel(s) ? 0 : -1} class="row sf" style:--depth={depth} onclick={() => app.select(s)} oncontextmenu={(e) => (e.preventDefault(), editSearchFolder(e, sf))} title={sf.query}>
      <span class="twisty"></span>
      <FolderSearch size={16} aria-hidden="true" />
      <span class="name">{sf.name}</span>
      {#if sf.unread > 0}<span class="count">{sf.unread}</span>{:else if sf.total > 0}<span class="count muted">{sf.total}</span>{/if}
    </button>
    <button class="sf-edit" aria-label={t('organize.searchFolder.editNamed', { name: sf.name })} onclick={(e) => editSearchFolder(e, sf)}><Pencil size={12} /></button>
  </div>
{/snippet}

<aside class="sidebar" aria-label={t('mail.sidebar.label')}>
  <div class="head">
    <button class="btn btn-primary compose" onclick={() => app.openCompose()}>
      <SquarePen size={16} aria-hidden="true" />
      {t('common.action.compose')}
    </button>
  </div>

  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <ul class="tree" role="tree" aria-label={t('mail.sidebar.folders')} bind:this={tree} onkeydown={onTreeKey}>
    <li role="none" class="section">
      <span class="section-title">{t('mail.sidebar.allAccounts')}</span>
    </li>
    {#each [{ role: 'inbox', icon: Inbox, label: t('mail.folder.unifiedInbox'), count: app.unifiedUnread }, { role: 'unread', icon: MailOpen, label: t('mail.folder.unread'), count: unifiedUnreadOnly }, { role: 'flagged', icon: Star, label: t('mail.folder.flagged'), count: 0 }] as u (u.role)}
      {@const s = { kind: 'unified', role: u.role } as SidebarSel}
      {@const Icon = u.icon}
      <li role="none">
        <button role="treeitem" aria-selected={isSel(s)} class="row" style:--depth={0} onclick={() => app.select(s)} tabindex={isSel(s) ? 0 : -1}>
          <span class="twisty"></span>
          <Icon size={16} aria-hidden="true" />
          <span class="name">{u.label}</span>
          {#if u.count > 0}<span class="count">{u.count}</span>{/if}
        </button>
      </li>
    {/each}

    {#each app.mailAccounts as acc (acc.id)}
      {@const key = 'a' + acc.id}
      <li role="none" class="section">
        <button
          role="treeitem"
          aria-selected="false"
          aria-expanded={!collapsed[key]}
          data-key={key}
          tabindex="-1"
          class="account"
          onclick={() => (collapsed[key] = !collapsed[key])}
        >
          <ChevronRight size={14} class="chev {collapsed[key] ? '' : 'open'}" aria-hidden="true" />
          <span class="acc-dot" style:background={acc.color}></span>
          <span class="acc-text">
            <span class="acc-name">{acc.displayName}</span>
            <span class="acc-addr">{accountAddress(acc)}</span>
          </span>
          <span class="kind">{acc.kind.toUpperCase()}</span>
        </button>
        {#if !collapsed[key]}
          <ul role="group">
            {#each flatten(app.folders[acc.id] ?? []) as node (node.folder.id)}
              {@const f = node.folder}
              {@const s = { kind: 'folder', accountId: acc.id, folderId: f.id } as SidebarSel}
              {@const Icon = roleIcon[f.role ?? ''] ?? FolderIcon}
              {@const fkey = 'f' + f.id}
              <li role="none">
                <button
                  role="treeitem"
                  aria-selected={isSel(s)}
                  aria-expanded={node.hasChildren ? !collapsed[fkey] : undefined}
                  data-key={node.hasChildren ? fkey : undefined}
                  tabindex={isSel(s) ? 0 : -1}
                  class="row"
                  class:drop={dropTarget === f.id}
                  style:--depth={node.depth}
                  onclick={() => app.select(s)}
                  ondragover={(e) => onDragOver(e, f.id)}
                  ondragleave={() => (dropTarget = dropTarget === f.id ? null : dropTarget)}
                  ondrop={(e) => onDrop(e, f.id)}
                >
                  {#if node.hasChildren}
                    <span
                      class="twisty"
                      role="presentation"
                      onclick={(e) => {
                        e.stopPropagation();
                        collapsed[fkey] = !collapsed[fkey];
                      }}
                    >
                      <ChevronRight size={13} class="chev {collapsed[fkey] ? '' : 'open'}" />
                    </span>
                  {:else}
                    <span class="twisty"></span>
                  {/if}
                  <Icon size={16} aria-hidden="true" />
                  <span class="name">{folderLabel(f)}</span>
                  {#if f.unread > 0 && f.role !== 'sent' && f.role !== 'drafts'}
                    <span class="count" class:muted={f.role === 'junk' || f.role === 'trash'}>{f.unread}</span>
                  {:else if f.role === 'drafts' && f.total > 0}
                    <span class="count muted">{f.total}</span>
                  {/if}
                </button>
              </li>
            {/each}
            {#each app.searchFolders.filter((x) => x.accountId === acc.id) as sf (sf.id)}
              <li role="none">{@render searchFolderRow(sf, 0)}</li>
            {/each}
          </ul>
        {/if}
      </li>
    {/each}

    <li role="none" class="section">
      <span class="section-title with-action">{t('organize.searchFolder.section')}
        <button class="sec-btn" aria-label={t('organize.searchFolder.newTitle')} title={t('organize.searchFolder.newTitle')} onclick={() => (app.organizeDialog = { kind: 'searchFolder' })}><Plus size={13} /></button>
      </span>
    </li>
    {#each globalSearchFolders as sf (sf.id)}
      <li role="none">{@render searchFolderRow(sf, 0)}</li>
    {/each}

    {#if activeProjects.length}
      <li role="none" class="section">
        <span class="section-title with-action">{t('nav.projects')}
          <button class="sec-btn" aria-label={t('projects.new')} title={t('projects.new')} onclick={() => (app.organizeDialog = { kind: 'newProject' })}><Plus size={13} /></button>
        </span>
      </li>
      {#each activeProjects as p (p.id)}
        <li role="none">
          <button role="treeitem" aria-selected="false" tabindex="-1" class="row" style:--depth={0} onclick={() => app.openProject(p.id)}>
            <span class="twisty"></span>
            <span class="pdot" style:background={p.color}></span>
            <span class="name">{p.name}</span>
            <span class="pbar" title={Math.round(p.progress * 100) + '%'}><span style:width="{p.progress * 100}%" style:background={p.color}></span></span>
          </button>
        </li>
        {#each app.searchFolders.filter((x) => x.projectId === p.id) as sf (sf.id)}
          <li role="none">{@render searchFolderRow(sf, 1)}</li>
        {/each}
      {/each}
    {/if}

    {#if tagNodes.length}
      <li role="none" class="section">
        <span class="section-title with-action">{t('organize.tags.section')}
          <button class="sec-btn" aria-label={t('organize.tags.manage')} title={t('organize.tags.manage')} onclick={() => ((app.settingsAnchor = 'set-tags'), app.navigate('settings'))}><Pencil size={12} /></button>
        </span>
      </li>
      {#each tagNodes as node (node.id)}
        {@const x = app.tags.find((y) => y.tag.id === node.id)!}
        {@const s = { kind: 'tag', tagId: node.id } as SidebarSel}
        <li role="none">
          <button
            role="treeitem"
            aria-selected={isSel(s)}
            aria-expanded={node.hasChildren ? !collapsed[node.key] : undefined}
            data-key={node.hasChildren ? node.key : undefined}
            tabindex={isSel(s) ? 0 : -1}
            class="row"
            style:--depth={node.depth}
            onclick={() => app.select(s)}
          >
            {#if node.hasChildren}
              <span
                class="twisty"
                role="presentation"
                onclick={(e) => {
                  e.stopPropagation();
                  collapsed[node.key] = !collapsed[node.key];
                }}
              >
                <ChevronRight size={13} class="chev {collapsed[node.key] ? '' : 'open'}" />
              </span>
            {:else}
              <span class="twisty"></span>
            {/if}
            <TagIcon icon={x.tag.icon} size={15} color={x.tag.color} />
            <span class="name">{x.tag.name}</span>
            {#if x.unread > 0}<span class="count">{x.unread}</span>{:else if x.total > 0}<span class="count muted">{x.total}</span>{/if}
          </button>
        </li>
      {/each}
    {/if}

    {#if app.messengerAccounts.length}
      <li role="none" class="section">
        <span class="section-title">{t('mail.sidebar.messengers')}</span>
      </li>
      {#each app.messengerAccounts as acc (acc.id)}
        {@const s = { kind: 'account', accountId: acc.id } as SidebarSel}
        <li role="none">
          <button role="treeitem" aria-selected={isSel(s)} tabindex={isSel(s) ? 0 : -1} class="row" style:--depth={0} onclick={() => app.select(s)}>
            <span class="twisty"></span>
            <ChannelIcon channel={messengerChannel(acc.kind) ?? 'whatsapp'} size={16} />
            <span class="name">{acc.displayName}</span>
            <span class="addr">{acc.extra?.address}</span>
          </button>
        </li>
      {/each}
    {/if}
  </ul>
</aside>

<style>
  .sidebar {
    display: flex;
    flex-direction: column;
    width: 264px;
    flex: none;
    background: var(--tm-sidebar);
    border-right: 1px solid var(--tm-line);
    min-height: 0;
  }
  .head {
    padding: 12px 12px 8px;
  }
  .compose {
    width: 100%;
    justify-content: center;
    height: 38px;
    border-radius: 10px;
    font-size: 14px;
  }
  .tree {
    position: relative;
    list-style: none;
    margin: 0;
    padding: 4px 8px 16px;
    overflow-y: auto;
    flex: 1;
  }
  .tree ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .section {
    margin-top: 10px;
  }
  .section-title {
    display: block;
    padding: 6px 8px 4px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--tm-faint);
  }
  .row,
  .account {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    text-align: left;
    border-radius: 8px;
    font: inherit;
    transition: background var(--tm-dur);
  }
  .row {
    height: var(--tm-tree-h);
    padding: 0 8px 0 calc(4px + var(--depth) * 16px);
    font-size: 13.5px;
    color: var(--tm-muted);
  }
  .row :global(svg) {
    flex: none;
  }
  .row:hover,
  .account:hover {
    background: color-mix(in srgb, var(--tm-fg) 6%, transparent);
  }
  .row[aria-selected='true'] {
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .twisty {
    width: 14px;
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  :global(.chev) {
    transition: transform var(--tm-dur);
  }
  :global(.chev.open) {
    transform: rotate(90deg);
  }
  .name {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--tm-fg);
  }
  .row[aria-selected='true'] .name {
    color: inherit;
  }
  .row.drop {
    background: var(--tm-accent-soft);
    box-shadow: inset 0 0 0 2px var(--tm-accent);
  }
  .with-action {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .sec-btn {
    display: inline-flex;
    border: none;
    background: transparent;
    color: var(--tm-faint);
    padding: 2px;
    border-radius: 5px;
    cursor: pointer;
  }
  .sec-btn:hover {
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--tm-fg) 8%, transparent);
  }
  .sf-wrap {
    position: relative;
  }
  .sf-edit {
    position: absolute;
    right: 4px;
    top: 50%;
    transform: translateY(-50%);
    display: none;
    border: none;
    background: var(--tm-sidebar);
    color: var(--tm-muted);
    padding: 3px;
    border-radius: 5px;
    cursor: pointer;
  }
  .sf-wrap:hover .sf-edit,
  .sf-edit:focus-visible {
    display: inline-flex;
  }
  .pdot {
    width: 10px;
    height: 10px;
    border-radius: 3px;
    flex: none;
    margin: 0 3px;
  }
  .pbar {
    width: 34px;
    height: 4px;
    border-radius: 2px;
    background: var(--tm-line);
    overflow: hidden;
    flex: none;
  }
  .pbar span {
    display: block;
    height: 100%;
  }
  .addr {
    font-size: 11px;
    color: var(--tm-faint);
  }
  .count {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--tm-accent-text);
  }
  .count.muted {
    color: var(--tm-faint);
    font-weight: 500;
  }
  .account {
    padding: 6px 8px 6px 4px;
    margin-bottom: 2px;
  }
  .acc-dot {
    width: 8px;
    height: 8px;
    border-radius: 99px;
    flex: none;
  }
  .acc-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
    line-height: 1.25;
  }
  .acc-name {
    font-size: 13px;
    font-weight: 600;
  }
  .acc-addr {
    font-size: 11.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .kind {
    font-size: 9.5px;
    font-weight: 700;
    letter-spacing: 0.04em;
    color: var(--tm-faint);
    border: 1px solid var(--tm-line-strong);
    border-radius: 4px;
    padding: 0 4px;
  }
</style>
