<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { connection, needsOnline } from '../../state/connection.svelte';
  import { t as tOffline } from '../../i18n/index.svelte';
  const notOffline = () => tOffline('connection.notAvailableOffline');
  import Plus from '@lucide/svelte/icons/plus';
  import Flag from '@lucide/svelte/icons/flag';
  import Repeat from '@lucide/svelte/icons/repeat';
  import Trash from '@lucide/svelte/icons/trash-2';
  import CornerDownRight from '@lucide/svelte/icons/corner-down-right';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { PimCalendar, PimTask, RecurrenceRule, TaskFilter } from '../../types';
  import Modal from '../common/Modal.svelte';
  import RecurrenceEditor from './RecurrenceEditor.svelte';
  import { parseQuick } from '../../calendar/quickparse';
  import { dayKeyIn, zonedInstant } from '../../calendar/datemath';

  interface Props {
    calendars: PimCalendar[];
    tz: string;
    weekStart: number;
    reloadKey: number;
  }
  let { calendars, tz, weekStart, reloadKey }: Props = $props();

  const FILTERS: TaskFilter[] = ['open', 'today', 'overdue', 'upcoming', 'completed', 'all'];
  let filter = $state<TaskFilter>('open');
  let listId = $state(0);
  let search = $state('');
  let tasks = $state<PimTask[]>([]);
  let quick = $state('');
  let editing = $state<PimTask | 'new' | null>(null);

  const lists = $derived(calendars.filter((c) => c.components.includes('VTODO') && c.kind !== 'birthdays'));
  const defaultList = $derived(lists.find((c) => c.isDefault && !c.readOnly) ?? lists.find((c) => !c.readOnly));

  async function load() {
    tasks = await app.api.listTasks({ filter, calendarIds: listId ? [listId] : undefined, search: search || undefined });
  }
  $effect(() => {
    void reloadKey;
    void filter;
    void listId;
    void search;
    void load();
  });

  /** Parents first, their subtasks indented below. */
  const ordered = $derived.by(() => {
    const ids = new Set(tasks.map((x) => x.id));
    const top = tasks.filter((x) => !x.parentId || !ids.has(x.parentId));
    const out: { task: PimTask; depth: number }[] = [];
    const walk = (task: PimTask, depth: number) => {
      out.push({ task, depth });
      for (const c of tasks.filter((x) => x.parentId === task.id)) walk(c, depth + 1);
    };
    top.forEach((x) => walk(x, 0));
    return out;
  });

  async function toggle(task: PimTask) {
    await app.api.setTaskDone(task.id, !task.done);
    await load();
  }

  async function addQuick() {
    const text = quick.trim();
    if (!text) return;
    const r = parseQuick(text, dayKeyIn(new Date(), tz));
    const due = r.matched ? (r.startMin === null ? r.day : zonedInstant(r.day, r.startMin, tz).toISOString()) : undefined;
    await app.api.saveTask({ title: r.matched ? r.title || text : text, due, dueAllDay: r.matched && r.startMin === null, calendarId: listId || defaultList?.id });
    quick = '';
    await load();
  }

  function dueLabel(task: PimTask): string {
    if (!task.due) return '';
    return task.dueAllDay ? fmt().listDate(task.due + 'T12:00:00Z') : `${fmt().listDate(task.due)} ${fmt().time(task.due)}`;
  }

  // ---- edit dialog ----------------------------------------------------------
  let fTitle = $state('');
  let fNotes = $state('');
  let fDue = $state('');
  let fDueTime = $state('');
  let fPrio = $state(0);
  let fProgress = $state(0);
  let fList = $state(0);
  let fParent = $state(0);
  let fRec = $state<RecurrenceRule | null>(null);
  let fCats = $state('');

  function open(task: PimTask | 'new', parent?: PimTask) {
    editing = task;
    const x = task === 'new' ? null : task;
    fTitle = x?.title ?? '';
    fNotes = x?.description ?? '';
    fDue = x?.due ? (x.dueAllDay ? x.due : dayKeyIn(new Date(x.due), tz)) : '';
    fDueTime = x?.due && !x.dueAllDay ? fmt().pattern(x.due, 'HH:mm') : '';
    fPrio = x?.priority ?? 0;
    fProgress = x?.progress ?? 0;
    fList = x?.calendarId ?? parent?.calendarId ?? (listId || defaultList?.id || 0);
    fParent = x?.parentId ?? parent?.id ?? 0;
    fRec = x?.recurrence ?? null;
    fCats = x?.categories.join(', ') ?? '';
  }

  async function saveEdit() {
    if (!fTitle.trim()) return;
    const x = editing === 'new' ? null : editing;
    let due: string | undefined;
    if (fDue) {
      if (fDueTime) {
        const [h, m] = fDueTime.split(':').map(Number);
        due = zonedInstant(fDue, h * 60 + m, tz).toISOString();
      } else due = fDue;
    }
    await app.api.saveTask({
      id: x?.id,
      calendarId: fList,
      title: fTitle.trim(),
      description: fNotes || undefined,
      due,
      dueAllDay: !!fDue && !fDueTime,
      priority: fPrio,
      progress: fProgress,
      done: fProgress >= 100,
      parentId: fParent || undefined,
      recurrence: fRec ?? (x?.recurrence ? { freq: '' } : null),
      categories: fCats
        .split(',')
        .map((c) => c.trim())
        .filter(Boolean),
    });
    editing = null;
    await load();
  }

  async function remove() {
    if (editing && editing !== 'new') {
      await app.api.deleteTask(editing.id);
      editing = null;
      await load();
    }
  }

  const prioClass = (p: number) => (p === 0 ? '' : p <= 4 ? 'high' : p === 5 ? 'med' : 'low');
  const listColor = (task: PimTask) => task.color || calendars.find((c) => c.id === task.calendarId)?.color || '#64748b';
</script>

<div class="tv">
  <div class="bar">
    <div class="seg" role="radiogroup" aria-label={t('tasks.filterLabel')}>
      {#each FILTERS as f (f)}
        <button role="radio" aria-checked={filter === f} onclick={() => (filter = f)}>{tk(`tasks.filter.${f}`)}</button>
      {/each}
    </div>
    <select class="input lists" bind:value={listId} aria-label={t('tasks.list')}>
      <option value={0}>{t('tasks.allLists')}</option>
      {#each lists as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
    </select>
    <input class="input search" type="search" bind:value={search} placeholder={t('tasks.search')} aria-label={t('tasks.search')} />
    <button use:needsOnline={notOffline} class="btn btn-primary" onclick={() => open('new')}><Plus size={15} /> {t('tasks.new')}</button>
  </div>
  <form
    class="quick"
    onsubmit={(e) => {
      e.preventDefault();
      void addQuick();
    }}
  >
    <Plus size={16} />
    <input class="qi" bind:value={quick} placeholder={t('tasks.addPlaceholder')} aria-label={t('tasks.addPlaceholder')} />
  </form>
  {#if ordered.length === 0}
    <p class="empty">{t('tasks.empty')}</p>
  {:else}
    <ul class="list">
      {#each ordered as { task, depth } (task.id)}
        <li class:done={task.done} style:--depth={depth} style:--c={listColor(task)}>
          <input type="checkbox" checked={task.done} disabled={task.readOnly} aria-label={task.done ? t('tasks.reopen') : t('tasks.markDone')} onchange={() => toggle(task)} />
          <button class="body" onclick={() => open(task)}>
            <span class="tt">
              {#if depth}<CornerDownRight size={12} />{/if}
              {task.title}
              {#if task.recurrence}<Repeat size={12} />{/if}
            </span>
            <span class="meta">
              {#if task.due}<span class="due" class:overdue={task.overdue}>{task.overdue ? t('tasks.overdue') + ' · ' : ''}{dueLabel(task)}</span>{/if}
              {#if task.progress > 0 && !task.done}<span class="prog"><span style:width="{task.progress}%"></span></span>{task.progress} %{/if}
              {#each task.categories as c (c)}<span class="cat">{c}</span>{/each}
              <span class="list-dot"></span>{calendars.find((x) => x.id === task.calendarId)?.name ?? ''}
            </span>
          </button>
          {#if task.priority}<span class="prio {prioClass(task.priority)}" title={tk(`tasks.priorityLevel.${prioClass(task.priority)}`)}><Flag size={14} /></span>{/if}
          {#if !task.readOnly && depth === 0}
            <button class="icon-btn sm add" title={t('tasks.addSubtask')} aria-label={t('tasks.addSubtask')} onclick={() => open('new', task)}><Plus size={14} /></button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

{#if editing}
  <Modal title={editing === 'new' ? t('tasks.new') : t('tasks.edit')} onclose={() => (editing = null)} width="560px">
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void saveEdit();
      }}
    >
      <input class="input big" bind:value={fTitle} placeholder={t('tasks.titlePlaceholder')} aria-label={t('tasks.title')} data-autofocus />
      <div class="grid2">
        <label><span class="label">{t('tasks.dueLabel')}</span><input class="input" type="date" bind:value={fDue} /></label>
        <label><span class="label">{t('tasks.dueTime')}</span><input class="input" type="time" bind:value={fDueTime} disabled={!fDue} /></label>
        <label>
          <span class="label">{t('tasks.priority')}</span>
          <select class="input" bind:value={fPrio}>
            <option value={0}>{t('tasks.priorityLevel.none')}</option>
            <option value={1}>{t('tasks.priorityLevel.high')}</option>
            <option value={5}>{t('tasks.priorityLevel.med')}</option>
            <option value={9}>{t('tasks.priorityLevel.low')}</option>
          </select>
        </label>
        <label>
          <span class="label">{t('tasks.progress')} ({fProgress} %)</span>
          <input type="range" min="0" max="100" step="10" bind:value={fProgress} />
        </label>
        <label>
          <span class="label">{t('tasks.list')}</span>
          <select class="input" bind:value={fList}>
            {#each lists.filter((l) => !l.readOnly) as l (l.id)}<option value={l.id}>{l.name}</option>{/each}
          </select>
        </label>
        <label>
          <span class="label">{t('tasks.parent')}</span>
          <select class="input" bind:value={fParent}>
            <option value={0}>{t('tasks.noParent')}</option>
            {#each tasks.filter((x) => !x.parentId && (editing === 'new' || x.id !== (editing as PimTask | null)?.id)) as x (x.id)}<option value={x.id}>{x.title}</option>{/each}
          </select>
        </label>
      </div>
      {#if fDue}<RecurrenceEditor bind:value={fRec} start={fDue} {tz} {weekStart} />{/if}
      <label><span class="label">{t('tasks.categories')}</span><input class="input" bind:value={fCats} /></label>
      <label><span class="label">{t('tasks.notes')}</span><textarea class="input" rows="4" bind:value={fNotes}></textarea></label>
      <button type="submit" hidden aria-hidden="true"></button>
    </form>
    {#snippet footer()}
      {#if editing !== 'new'}<button class="btn btn-ghost danger" onclick={remove}><Trash size={15} /> {t('common.action.delete')}</button>{/if}
      <span class="sp"></span>
      <button class="btn" onclick={() => (editing = null)}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={!fTitle.trim() || connection.offline} onclick={saveEdit}>{t('common.action.save')}</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .tv {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
    overflow-y: auto;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }
  .lists {
    width: auto;
  }
  .search {
    flex: 1;
    min-width: 140px;
  }
  .quick {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 12px;
    height: 42px;
    background: var(--tm-surface);
    border: 1px dashed var(--tm-line-strong);
    border-radius: var(--tm-radius);
    color: var(--tm-muted);
  }
  .qi {
    flex: 1;
    border: none;
    outline: none;
    background: none;
    font: inherit;
    color: var(--tm-fg);
  }
  .empty {
    color: var(--tm-muted);
    text-align: center;
    margin-top: 40px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    overflow: hidden;
  }
  .list li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px 8px calc(12px + var(--depth) * 26px);
    border-left: 3px solid var(--c);
  }
  .list li + li {
    border-top: 1px solid var(--tm-line);
  }
  .list input[type='checkbox'] {
    width: 18px;
    height: 18px;
    accent-color: var(--tm-accent);
  }
  .body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    border: none;
    background: none;
    font: inherit;
    color: var(--tm-fg);
    text-align: left;
    cursor: pointer;
    padding: 0;
  }
  .tt {
    font-weight: 550;
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
  .done .tt {
    text-decoration: line-through;
    color: var(--tm-faint);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .due.overdue {
    color: var(--tm-danger);
    font-weight: 600;
  }
  .prog {
    width: 60px;
    height: 5px;
    border-radius: 9px;
    background: var(--tm-line);
    overflow: hidden;
    display: inline-block;
  }
  .prog span {
    display: block;
    height: 100%;
    background: var(--c);
  }
  .cat {
    padding: 0 6px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
  }
  .list-dot {
    width: 7px;
    height: 7px;
    border-radius: 99px;
    background: var(--c);
  }
  .prio.high {
    color: var(--tm-danger);
  }
  .prio.med {
    color: var(--tm-warn);
  }
  .prio.low {
    color: var(--tm-faint);
  }
  .sm {
    width: 26px;
    height: 26px;
  }
  .add {
    opacity: 0;
  }
  li:hover .add,
  .add:focus-visible {
    opacity: 1;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .big {
    font-size: 16px;
    font-weight: 600;
    height: 40px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .sp {
    flex: 1;
  }
  .danger {
    color: var(--tm-danger);
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .seg {
      overflow-x: auto;
      max-width: 100%;
    }
    .grid2 {
      grid-template-columns: 1fr;
    }
    .add {
      opacity: 1;
    }
  }
</style>
