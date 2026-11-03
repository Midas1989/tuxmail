<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Tag picker, "Add to project", search folder editor and new project (app.organizeDialog). -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Search from '@lucide/svelte/icons/search';
  import Check from '@lucide/svelte/icons/check';
  import Trash from '@lucide/svelte/icons/trash';
  import FolderKanban from '@lucide/svelte/icons/folder-kanban';
  import Modal from '../common/Modal.svelte';
  import TagIcon, { TAG_COLORS } from '../common/TagIcon.svelte';
  import QueryBar from '../search/QueryBar.svelte';
  import ProjectDialog from '../projects/ProjectDialog.svelte';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { parse } from '../../search/query';
  import type { ItemRef, SearchFolderInput } from '../../types';

  const dlg = $derived(app.organizeDialog);
  const close = () => (app.organizeDialog = null);

  // ----- tag picker -----------------------------------------------------------
  let query = $state('');
  let checked = $state<number[]>([]);
  let newColor = $state(TAG_COLORS[0]);
  let active = $state(0);
  let lastDlg: unknown = null;
  $effect(() => {
    if (dlg !== lastDlg) {
      lastDlg = dlg;
      query = '';
      active = 0;
      if (dlg?.kind === 'tags') checked = [...dlg.current];
      if (dlg?.kind === 'searchFolder') initSearchFolder();
    }
  });
  const depth = (path: string) => path.split('/').length - 1;
  const tagList = $derived(app.tags.filter((x) => !query.trim() || x.tag.path.toLowerCase().includes(query.trim().toLowerCase())));
  const exact = $derived(app.tags.some((x) => x.tag.path.toLowerCase() === query.trim().toLowerCase()));

  function toggle(id: number) {
    checked = checked.includes(id) ? checked.filter((x) => x !== id) : [...checked, id];
  }

  async function createTag() {
    const path = query.trim();
    if (!path) return;
    const tag = await app.createTag(path, newColor);
    checked = [...checked, tag.id];
    query = '';
  }

  async function applyTags(items: ItemRef[], current: number[]) {
    const add = checked.filter((x) => !current.includes(x));
    const remove = current.filter((x) => !checked.includes(x));
    close();
    await app.applyTags(items, add, remove);
  }

  function onTagKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const n = tagList.length + (query.trim() && !exact ? 1 : 0);
      if (n) active = (active + (e.key === 'ArrowDown' ? 1 : -1) + n) % n;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) {
        if (dlg?.kind === 'tags') void applyTags(dlg.items, dlg.current);
        return;
      }
      const x = tagList[active];
      if (x) toggle(x.tag.id);
      else void createTag();
    }
  }

  // ----- project picker ---------------------------------------------------------
  const projectList = $derived(app.projects.filter((p) => p.status !== 'archived' && (!query.trim() || (p.name + ' ' + p.key).toLowerCase().includes(query.trim().toLowerCase()))));

  async function pickProject(id: number, items: ItemRef[]) {
    close();
    await app.addToProject(id, items);
  }

  // ----- search folder ------------------------------------------------------------
  let sf = $state<SearchFolderInput>({ name: '', query: '', useAsChatFolder: false });
  let location = $state('global');
  let sfError = $state('');
  let confirmDelete = $state(false);
  function initSearchFolder() {
    if (dlg?.kind !== 'searchFolder') return;
    const f = dlg.folder;
    sf = f ? { id: f.id, name: f.name, query: f.query, accountId: f.accountId, projectId: f.projectId, useAsChatFolder: f.useAsChatFolder, icon: f.icon, color: f.color } : { name: '', query: dlg.query ?? '', useAsChatFolder: false };
    location = f?.accountId ? 'a' + f.accountId : f?.projectId ? 'p' + f.projectId : dlg.projectId ? 'p' + dlg.projectId : 'global';
    sfError = '';
    confirmDelete = false;
  }
  const parsed = $derived(sf.query.trim() ? parse(sf.query) : null);

  async function saveSearchFolder(e: SubmitEvent) {
    e.preventDefault();
    if (!sf.name.trim()) {
      sfError = t('organize.searchFolder.nameRequired');
      return;
    }
    if (parsed && !parsed.ok) {
      sfError = parsed.error.message;
      return;
    }
    const input: SearchFolderInput = { ...($state.snapshot(sf) as SearchFolderInput), accountId: location.startsWith('a') ? Number(location.slice(1)) : undefined, projectId: location.startsWith('p') ? Number(location.slice(1)) : undefined };
    try {
      const saved = await app.saveSearchFolder(input);
      close();
      if (!input.id) await app.select({ kind: 'searchFolder', searchFolderId: saved.id });
    } catch (err) {
      sfError = err instanceof Error ? err.message : String(err);
    }
  }
</script>

{#if dlg?.kind === 'tags'}
  {@const d = dlg}
  <Modal title={t('organize.tags.title', { count: d.items.length })} onclose={close} width="420px">
    <div class="picker">
      <div class="search">
        <Search size={15} aria-hidden="true" />
        <input
          bind:value={query}
          oninput={() => (active = 0)}
          onkeydown={onTagKey}
          placeholder={t('organize.tags.searchOrCreate')}
          aria-label={t('organize.tags.searchOrCreate')}
          data-autofocus
          role="combobox"
          aria-expanded="true"
          aria-controls="tag-options"
          aria-activedescendant={'tag-opt-' + active}
        />
      </div>
      <ul class="options" id="tag-options" role="listbox" aria-multiselectable="true" aria-label={t('organize.tags.label')}>
        {#each tagList as x, i (x.tag.id)}
          <li id={'tag-opt-' + i} role="option" aria-selected={checked.includes(x.tag.id)} class:active={i === active}>
            <button class="opt" style:padding-left="{8 + (query ? 0 : depth(x.tag.path) * 16)}px" onclick={() => toggle(x.tag.id)}>
              <span class="box" class:on={checked.includes(x.tag.id)} style:--c={x.tag.color}>{#if checked.includes(x.tag.id)}<Check size={12} />{/if}</span>
              <TagIcon icon={x.tag.icon} color={x.tag.color} />
              <span class="name">{query ? x.tag.path : x.tag.name}</span>
              <span class="cnt">{x.total}</span>
            </button>
          </li>
        {/each}
        {#if query.trim() && !exact}
          <li id={'tag-opt-' + tagList.length} role="option" aria-selected="false" class:active={active === tagList.length}>
            <div class="create">
              <button class="opt" onclick={createTag}><Plus size={14} /> {t('organize.tags.create', { name: query.trim() })}</button>
              <span class="swatches" role="radiogroup" aria-label={t('organize.tags.color')}>
                {#each TAG_COLORS.slice(0, 7) as c (c)}<button class="sw" role="radio" aria-checked={newColor === c} aria-label={c} style:background={c} onclick={() => (newColor = c)}></button>{/each}
              </span>
            </div>
          </li>
        {/if}
      </ul>
      <p class="hint">{t('organize.tags.hint')}</p>
    </div>
    {#snippet footer()}
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" onclick={() => applyTags(d.items, d.current)}>{t('organize.tags.apply')}</button>
    {/snippet}
  </Modal>
{:else if dlg?.kind === 'project'}
  {@const d = dlg}
  <Modal title={t('organize.project.title', { count: d.items.length })} onclose={close} width="420px">
    <div class="picker">
      <div class="search">
        <Search size={15} aria-hidden="true" />
        <input bind:value={query} placeholder={t('organize.project.search')} aria-label={t('organize.project.search')} data-autofocus />
      </div>
      <ul class="options" aria-label={t('nav.projects')}>
        {#each projectList as p (p.id)}
          <li>
            <button class="opt proj" onclick={() => pickProject(p.id, d.items)}>
              <span class="pdot" style:background={p.color}></span>
              <span class="name">{p.name}</span>
              <span class="key">{p.key}</span>
              <span class="st st-{p.status}">{t(`projects.status.${p.status}`)}</span>
            </button>
          </li>
        {/each}
      </ul>
    </div>
    {#snippet footer()}
      <button class="btn btn-ghost" onclick={() => (app.organizeDialog = { kind: 'newProject' })}><FolderKanban size={15} /> {t('projects.new')}</button>
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
    {/snippet}
  </Modal>
{:else if dlg?.kind === 'searchFolder'}
  <Modal title={sf.id ? t('organize.searchFolder.editTitle') : t('organize.searchFolder.newTitle')} onclose={close} width="640px">
    <form id="sf-form" class="sf" onsubmit={saveSearchFolder}>
      <label class="field">
        <span class="label">{t('organize.searchFolder.name')}</span>
        <input class="input" bind:value={sf.name} data-autofocus required />
      </label>
      <div class="field">
        <span class="label">{t('organize.searchFolder.query')}</span>
        <QueryBar bind:value={sf.query} data={app.suggestData} error={parsed && !parsed.ok ? parsed.error : null} onsubmit={() => {}} placeholder={t('search.expertPlaceholder')} />
      </div>
      <label class="field">
        <span class="label">{t('organize.searchFolder.location')}</span>
        <select class="input" bind:value={location}>
          <option value="global">{t('organize.searchFolder.global')}</option>
          <optgroup label={t('organize.searchFolder.inAccount')}>
            {#each app.mailAccounts as a (a.id)}<option value={'a' + a.id}>{a.displayName}</option>{/each}
          </optgroup>
          <optgroup label={t('nav.projects')}>
            {#each app.projects as p (p.id)}<option value={'p' + p.id}>{p.name}</option>{/each}
          </optgroup>
        </select>
      </label>
      <label class="chk"><input type="checkbox" bind:checked={sf.useAsChatFolder} /> {t('organize.searchFolder.asChatFolder')}</label>
      <p class="hint">{t('organize.searchFolder.readOnlyHint')}</p>
      {#if sfError}<p class="err" role="alert">{sfError}</p>{/if}
    </form>
    {#snippet footer()}
      {#if sf.id}
        {#if confirmDelete}
          <button
            class="btn danger"
            onclick={async () => {
              const id = sf.id!;
              close();
              await app.deleteSearchFolder(id);
            }}><Trash size={14} /> {t('organize.searchFolder.confirmDelete')}</button
          >
        {:else}
          <button class="btn btn-ghost" onclick={() => (confirmDelete = true)}><Trash size={14} /> {t('common.action.delete')}</button>
        {/if}
      {/if}
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" type="submit" form="sf-form">{t('common.action.save')}</button>
    {/snippet}
  </Modal>
{:else if dlg?.kind === 'newProject'}
  <ProjectDialog onclose={close} onsaved={(p) => app.openProject(p.id)} />
{/if}

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 10px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .search input {
    flex: 1;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
  }
  .options {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 320px;
    overflow-y: auto;
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 13.5px;
    text-align: left;
    cursor: pointer;
  }
  li.active > .opt,
  li.active .create,
  .opt:hover {
    background: var(--tm-surface-2);
  }
  .box {
    width: 16px;
    height: 16px;
    border-radius: 4px;
    border: 1.5px solid var(--tm-line-strong);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    color: #fff;
  }
  .box.on {
    background: var(--c);
    border-color: var(--c);
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cnt,
  .key {
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .create {
    display: flex;
    align-items: center;
    gap: 8px;
    border-radius: 8px;
    padding-right: 8px;
  }
  .create .opt {
    flex: 1;
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .swatches {
    display: inline-flex;
    gap: 4px;
  }
  .sw {
    width: 16px;
    height: 16px;
    border-radius: 99px;
    border: 2px solid transparent;
    cursor: pointer;
  }
  .sw[aria-checked='true'] {
    border-color: var(--tm-fg);
  }
  .hint {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .pdot {
    width: 10px;
    height: 10px;
    border-radius: 3px;
    flex: none;
  }
  .st {
    font-size: 10.5px;
    font-weight: 600;
    padding: 1px 6px;
    border-radius: 99px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .st-active {
    background: color-mix(in srgb, var(--tm-success) 15%, transparent);
    color: var(--tm-success);
  }
  .sf {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .chk {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .chk input {
    accent-color: var(--tm-accent);
  }
  .err {
    margin: 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }
  .danger {
    color: var(--tm-danger);
  }
</style>
