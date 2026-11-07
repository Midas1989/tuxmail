<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Projects: list (status, colour, due date, progress) and a dashboard with
  Overview, Timeline (all linked mails/chats/events/tasks/files), Communication,
  Events & tasks, Contacts, Files, Workflows and Rules (auto-assignment query).
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash';
  import Mail from '@lucide/svelte/icons/mail';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import CalendarDays from '@lucide/svelte/icons/calendar-days';
  import ListTodo from '@lucide/svelte/icons/list-todo';
  import FileIcon from '@lucide/svelte/icons/file';
  import Cloud from '@lucide/svelte/icons/cloud';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import FolderKanban from '@lucide/svelte/icons/folder-kanban';
  import FolderSearch from '@lucide/svelte/icons/folder-search';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Wand from '@lucide/svelte/icons/wand-sparkles';
  import Zap from '@lucide/svelte/icons/zap';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import { app } from '../state';
  import { fmt, formatSize, t } from '../i18n/index.svelte';
  import { parse } from '../search/query';
  import type { Project, ProjectStatus, ProjectTimelineEntry, TimelineKind } from '../types';
  import Avatar from '../components/common/Avatar.svelte';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';
  import TagChips from '../components/common/TagChips.svelte';
  import ConfirmDialog from '../components/common/ConfirmDialog.svelte';
  import QueryBar from '../components/search/QueryBar.svelte';
  import ProjectDialog from '../components/projects/ProjectDialog.svelte';
  import WorkflowList from '../components/workflow/WorkflowList.svelte';
  import { safeHref } from '../safeurl';

  type Tab = 'overview' | 'timeline' | 'communication' | 'schedule' | 'contacts' | 'files' | 'workflows' | 'rules';
  const TABS: Tab[] = ['overview', 'timeline', 'communication', 'schedule', 'contacts', 'files', 'workflows', 'rules'];
  const KIND_ICON = { mail: Mail, chat: MessageCircle, event: CalendarDays, task: ListTodo, file: FileIcon, note: Pencil };

  let statusFilter = $state<'open' | 'all'>('open');
  let tab = $state<Tab>('overview');
  let timeline = $state<ProjectTimelineEntry[]>([]);
  let kindFilter = $state<TimelineKind | 'all'>('all');
  let editing = $state<Project | null>(null);
  let creating = $state(false);
  let confirmDelete = $state(false);
  let ruleQuery = $state('');
  let ruleEnabled = $state(false);
  let notes = $state('');

  const OPEN: ProjectStatus[] = ['planned', 'active', 'paused'];
  const projects = $derived(app.projects.filter((p) => statusFilter === 'all' || OPEN.includes(p.status)));
  const project = $derived(app.selectedProjectId != null ? app.project(app.selectedProjectId) : undefined);

  $effect(() => {
    if (app.selectedProjectId == null && projects.length && typeof window !== 'undefined' && window.innerWidth >= 900) app.selectedProjectId = projects[0].id;
  });

  let loadedFor = -1;
  $effect(() => {
    const p = project;
    void app.projects;
    if (!p) return;
    void app.api.projectTimeline(p.id).then((x) => (timeline = x));
    if (loadedFor !== p.id) {
      loadedFor = p.id;
      ruleQuery = p.rules.query;
      ruleEnabled = p.rules.enabled;
      notes = p.notes;
    }
  });

  const filtered = $derived(timeline.filter((e) => kindFilter === 'all' || e.kind === kindFilter));
  const byDay = $derived.by(() => {
    const out: { day: string; date: string; items: ProjectTimelineEntry[] }[] = [];
    for (const e of filtered) {
      const k = fmt().dayKey(e.date);
      const last = out[out.length - 1];
      if (last?.day === k) last.items.push(e);
      else out.push({ day: k, date: e.date, items: [e] });
    }
    return out;
  });
  const comms = $derived(timeline.filter((e) => e.kind === 'mail' || e.kind === 'chat'));
  const events = $derived(timeline.filter((e) => e.kind === 'event').sort((a, b) => a.date.localeCompare(b.date)));
  const tasks = $derived(timeline.filter((e) => e.kind === 'task').sort((a, b) => Number(a.done) - Number(b.done) || (a.due ?? '').localeCompare(b.due ?? '')));
  const files = $derived(timeline.filter((e) => e.kind === 'file'));
  const contacts = $derived.by(() => {
    if (!project) return [];
    const ids = new Set<number>([...(project.customerPersonId ? [project.customerPersonId] : []), ...project.memberPersonIds]);
    for (const e of comms) if (e.personId && !app.person(e.personId)?.isMe) ids.add(e.personId);
    return [...ids].map((id) => ({ person: app.person(id), count: comms.filter((e) => e.personId === id).length, member: project.memberPersonIds.includes(id), customer: project.customerPersonId === id })).filter((x) => !!x.person);
  });
  const nowIso = $derived(new Date(app.now).toISOString());
  const parsedRule = $derived(ruleQuery.trim() ? parse(ruleQuery) : null);

  function openEntry(e: ProjectTimelineEntry) {
    if (e.kind === 'mail' && e.messageId != null) void app.revealMessage(e.messageId);
    else if (e.kind === 'chat' && e.conversationId != null) void app.revealItem(e.conversationId, e.itemId);
  }

  async function setStatus(p: Project, status: ProjectStatus) {
    await app.api.saveProject({ ...JSON.parse(JSON.stringify(p)), status });
    await app.loadProjects();
  }

  async function saveNotes(p: Project) {
    if (notes === p.notes) return;
    await app.api.saveProject({ ...JSON.parse(JSON.stringify(p)), notes });
    await app.loadProjects();
  }

  async function saveRules(p: Project) {
    await app.api.saveProject({ ...JSON.parse(JSON.stringify(p)), rules: { enabled: ruleEnabled, query: ruleQuery } });
    await app.loadProjects();
    app.toast(t('projects.rules.saved'));
  }

  async function applyRules(p: Project) {
    await saveRules(p);
    const r = await app.api.applyProjectRules(p.id);
    await app.loadProjects();
    app.toast(t('projects.rules.applied', { count: r.added }));
  }

  async function toggleTask(e: ProjectTimelineEntry) {
    await app.api.setTaskDone(e.item.id, !e.done);
    await app.loadProjects();
  }

  async function remove(p: Project) {
    confirmDelete = false;
    await app.api.deleteProject(p.id);
    app.selectedProjectId = null;
    await app.loadProjects();
  }

  const money = (n: number, cur: string) => fmt().number(n, { style: 'currency', currency: cur, maximumFractionDigits: 0 });
  const isOverdue = (iso?: string) => !!iso && iso.slice(0, 10) < nowIso.slice(0, 10);
</script>

<section class="projects" class:has-selection={!!project} aria-labelledby="proj-title">
  <aside class="list" aria-label={t('nav.projects')}>
    <header class="lhead">
      <h1 id="proj-title">{t('nav.projects')}</h1>
      <button class="btn btn-primary" onclick={() => (creating = true)}><Plus size={15} /> {t('projects.new')}</button>
    </header>
    <div class="seg filt" role="radiogroup" aria-label={t('projects.filter')}>
      <button role="radio" aria-checked={statusFilter === 'open'} onclick={() => (statusFilter = 'open')}>{t('projects.filterOpen')}</button>
      <button role="radio" aria-checked={statusFilter === 'all'} onclick={() => (statusFilter = 'all')}>{t('projects.filterAll')}</button>
    </div>
    <ul class="plist">
      {#each projects as p (p.id)}
        <li>
          <button class="pcard" aria-current={app.selectedProjectId === p.id ? 'true' : undefined} style:--c={p.color} onclick={() => ((app.selectedProjectId = p.id), (tab = 'overview'))}>
            <span class="pc-1">
              <span class="pname">{p.name}</span>
              <span class="st st-{p.status}">{t(`projects.status.${p.status}`)}</span>
            </span>
            <span class="pc-2">
              <span class="key">{p.key}</span>
              {#if p.customer}<span>· {p.customer}</span>{/if}
              {#if p.dueDate}<span class="due" class:over={isOverdue(p.dueDate) && p.status !== 'done'}>· {t('projects.dueShort', { date: fmt().dateShort(p.dueDate) })}</span>{/if}
            </span>
            <span class="progress" title={Math.round(p.progress * 100) + '%'}><span style:width="{p.progress * 100}%"></span></span>
            <span class="pc-3">
              <span><Mail size={11} /> {p.counts.mail + p.counts.chat}</span>
              <span><ListTodo size={11} /> {p.counts.task}</span>
              <span><CalendarDays size={11} /> {p.counts.event}</span>
              <span><FileIcon size={11} /> {p.counts.file}</span>
              <span class="pct">{Math.round(p.progress * 100)} %</span>
            </span>
          </button>
        </li>
      {/each}
    </ul>
  </aside>

  <div class="dash">
    {#if project}
      {@const p = project}
      <header class="dhead" style:--c={p.color}>
        <button class="icon-btn back" aria-label={t('common.action.back')} onclick={() => (app.selectedProjectId = null)}><ArrowLeft size={18} /></button>
        <span class="pico"><FolderKanban size={20} /></span>
        <div class="dtitle">
          <h2>{p.name}</h2>
          <span class="sub">{[p.key, p.customer, p.dueDate ? t('projects.due', { date: fmt().date(p.dueDate) }) : ''].filter(Boolean).join(' · ')}</span>
        </div>
        <select class="input status-sel" value={p.status} onchange={(e) => setStatus(p, (e.currentTarget as HTMLSelectElement).value as ProjectStatus)} aria-label={t('projects.field.status')}>
          {#each ['planned', 'active', 'paused', 'done', 'archived'] as s (s)}<option value={s}>{t(`projects.status.${s as ProjectStatus}`)}</option>{/each}
        </select>
        <button class="icon-btn" aria-label={t('common.action.edit')} title={t('common.action.edit')} onclick={() => (editing = p)}><Pencil size={16} /></button>
        <button class="icon-btn" aria-label={t('common.action.delete')} title={t('common.action.delete')} onclick={() => (confirmDelete = true)}><Trash size={16} /></button>
      </header>
      <div class="tabs" role="tablist" aria-label={t('projects.tabs')}>
        {#each TABS as tb (tb)}
          <button role="tab" aria-selected={tab === tb} onclick={() => (tab = tb)}>
            {t(`projects.tab.${tb}`)}
            {#if tb === 'timeline'}<span class="tcount">{timeline.length}</span>{/if}
            {#if tb === 'communication'}<span class="tcount">{comms.length}</span>{/if}
            {#if tb === 'schedule'}<span class="tcount">{tasks.filter((x) => !x.done).length}</span>{/if}
          </button>
        {/each}
      </div>

      <div class="tabbody" role="tabpanel">
        {#if tab === 'overview'}
          <div class="ov">
            <div class="col">
              <section class="box">
                <h3>{t('projects.field.description')}</h3>
                <p class="desc">{p.description || '–'}</p>
                {#if p.tagIds.length}<TagChips ids={p.tagIds} max={8} />{/if}
              </section>
              <section class="box">
                <h3>{t('projects.facts')}</h3>
                <dl class="facts">
                  <dt>{t('projects.field.key')}</dt>
                  <dd><code>{p.key}</code></dd>
                  {#if p.customer}<dt>{t('projects.field.customer')}</dt><dd>{p.customer}</dd>{/if}
                  {#if p.startDate}<dt>{t('projects.field.start')}</dt><dd>{fmt().date(p.startDate)}</dd>{/if}
                  {#if p.dueDate}<dt>{t('projects.field.due')}</dt><dd class:over={isOverdue(p.dueDate) && p.status !== 'done'}>{fmt().date(p.dueDate)}</dd>{/if}
                  {#each p.customFields as f (f.key)}
                    <dt>{f.label}</dt>
                    <dd>{#if f.type === 'url'}<a href={safeHref(f.value)} target="_blank" rel="noopener noreferrer">{f.value}</a>{:else if f.type === 'date' && f.value}{fmt().date(f.value)}{:else}{f.value || '–'}{/if}</dd>
                  {/each}
                </dl>
              </section>
              <section class="box">
                <h3>{t('projects.notes')}</h3>
                <textarea class="input" rows="4" bind:value={notes} onblur={() => saveNotes(p)} aria-label={t('projects.notes')}></textarea>
              </section>
            </div>
            <div class="col">
              <section class="box tiles">
                <div class="tile"><strong>{Math.round(p.progress * 100)} %</strong><span>{t('projects.progress')}</span><span class="progress big"><span style:width="{p.progress * 100}%" style:background={p.color}></span></span></div>
                <div class="tile"><strong>{p.counts.mail + p.counts.chat}</strong><span>{t('projects.tab.communication')}</span></div>
                <div class="tile"><strong>{tasks.filter((x) => !x.done).length}/{p.counts.task}</strong><span>{t('projects.openTasks')}</span></div>
                <div class="tile"><strong>{events.filter((e) => e.date >= nowIso).length}</strong><span>{t('projects.upcoming')}</span></div>
              </section>
              {#if p.budget}
                {@const b = p.budget}
                <section class="box">
                  <h3>{t('projects.budget')}</h3>
                  <div class="budget">
                    <span>{money(b.spent ?? 0, b.currency)} <small>/ {money(b.amount, b.currency)}</small></span>
                    <span class="progress big"><span style:width="{Math.min(100, ((b.spent ?? 0) / b.amount) * 100)}%"></span></span>
                    <small>{t('projects.budgetLeft', { amount: money(b.amount - (b.spent ?? 0), b.currency) })}</small>
                  </div>
                </section>
              {/if}
              <section class="box">
                <h3>{t('projects.members')}</h3>
                <ul class="members">
                  {#each p.memberPersonIds as id (id)}
                    {@const per = app.person(id)}
                    {#if per}<li><Avatar name={per.displayName} seed={per.handles[0]?.value} size={28} /> {per.displayName}</li>{/if}
                  {/each}
                </ul>
              </section>
              {#if p.cloudFolder}
                <section class="box">
                  <h3>{t('projects.cloudFolder')}</h3>
                  <a class="cloud" href={safeHref(p.cloudFolder.url) ?? '#'} target="_blank" rel="noopener noreferrer"><Cloud size={16} /> <code>{p.cloudFolder.path}</code> <ExternalLink size={13} /></a>
                </section>
              {/if}
              {#if app.searchFolders.some((s) => s.projectId === p.id)}
                <section class="box">
                  <h3>{t('organize.searchFolder.section')}</h3>
                  {#each app.searchFolders.filter((s) => s.projectId === p.id) as sf (sf.id)}
                    <button class="linkish sfl" onclick={() => app.select({ kind: 'searchFolder', searchFolderId: sf.id })}><FolderSearch size={14} /> {sf.name} <small>{sf.total}</small></button>
                  {/each}
                </section>
              {/if}
            </div>
          </div>
        {:else if tab === 'timeline' || tab === 'communication'}
          {#if tab === 'timeline'}
            <div class="kinds" role="radiogroup" aria-label={t('projects.kindFilter')}>
              {#each ['all', 'mail', 'chat', 'event', 'task', 'file'] as k (k)}
                <button class="pill" role="radio" aria-checked={kindFilter === k} onclick={() => (kindFilter = k as TimelineKind | 'all')}>{t(`projects.kind.${k as TimelineKind | 'all'}`)}</button>
              {/each}
            </div>
          {/if}
          <ol class="tl">
            {#each tab === 'timeline' ? byDay : [{ day: 'c', date: '', items: comms }] as g (g.day)}
              {#if tab === 'timeline'}<li class="tl-day"><span>{fmt().day(g.date)}</span></li>{/if}
              {#each g.items as e (e.item.kind + e.item.id)}
                {@const Icon = KIND_ICON[e.kind]}
                <li class="tl-item k-{e.kind}" class:future={e.date > nowIso}>
                  <span class="tl-ic"><Icon size={14} /></span>
                  <button class="tl-card" onclick={() => openEntry(e)} disabled={e.kind !== 'mail' && e.kind !== 'chat'}>
                    <span class="tl-1">
                      {#if e.channel && e.channel !== 'email'}<ChannelIcon channel={e.channel} size={12} />{/if}
                      <span class="tl-title">{e.title}</span>
                      {#if e.kind === 'task'}<span class="badge-s" class:done={e.done}>{e.done ? t('projects.task.done') : t('projects.task.open')}</span>{/if}
                      {#if e.source !== 'manual'}<span class="src" title={t(`projects.source.${e.source}`)}>{#if e.source === 'rule'}<Wand size={11} />{:else}<Zap size={11} />{/if} {t(`projects.source.${e.source}`)}</span>{/if}
                      <time datetime={e.date}>{tab === 'timeline' ? fmt().time(e.date) : fmt().listDate(e.date)}</time>
                    </span>
                    {#if e.snippet || e.actorName || e.location || e.size}
                      <span class="tl-2">
                        {#if e.actorName}<strong>{e.actorName}</strong>{/if}
                        {#if e.snippet}<span>{e.snippet}</span>{/if}
                        {#if e.location}<span><MapPin size={11} /> {e.location}</span>{/if}
                        {#if e.size}<span>{formatSize(e.size)}</span>{/if}
                      </span>
                    {/if}
                  </button>
                </li>
              {/each}
            {/each}
          </ol>
        {:else if tab === 'schedule'}
          <div class="two">
            <section class="box">
              <h3>{t('projects.tasks')}</h3>
              <ul class="tasks">
                {#each tasks as e (e.item.id)}
                  <li class:done={e.done}>
                    <label><input type="checkbox" checked={e.done} onchange={() => toggleTask(e)} /> {e.title}</label>
                    {#if e.due}<span class="due" class:over={!e.done && isOverdue(e.due)}>{fmt().dateShort(e.due)}</span>{/if}
                  </li>
                {/each}
              </ul>
            </section>
            <section class="box">
              <h3>{t('projects.events')}</h3>
              <ul class="events">
                {#each events as e (e.item.id)}
                  <li class:past={e.date < nowIso}>
                    <span class="ev-date"><strong>{fmt().pattern(e.date, 'DD')}</strong><small>{fmt().pattern(e.date, 'MMM')}</small></span>
                    <span class="ev-txt"><strong>{e.title}</strong><small>{fmt().time(e.date)}{#if e.end} – {fmt().time(e.end)}{/if}{#if e.location} · {e.location}{/if}</small></span>
                  </li>
                {/each}
              </ul>
            </section>
          </div>
        {:else if tab === 'contacts'}
          <ul class="contacts">
            {#each contacts as c (c.person!.id)}
              {@const per = c.person!}
              <li class="box">
                <Avatar name={per.displayName} seed={per.handles[0]?.value} size={40} />
                <div class="ct">
                  <strong>{per.displayName}</strong>
                  <small>{per.handles.find((h) => h.kind === 'email')?.value ?? per.handles[0]?.value}</small>
                  <span class="roles">
                    {#if c.customer}<span class="role">{t('projects.contact.customer')}</span>{/if}
                    {#if c.member}<span class="role">{t('projects.contact.member')}</span>{/if}
                    {#if c.count}<span class="role muted">{t('projects.contact.messages', { count: c.count })}</span>{/if}
                  </span>
                </div>
              </li>
            {/each}
          </ul>
        {:else if tab === 'files'}
          {#if p.cloudFolder}
            <a class="cloud box" href={safeHref(p.cloudFolder.url) ?? '#'} target="_blank" rel="noopener noreferrer"><Cloud size={16} /> {t('projects.openCloud')} <code>{p.cloudFolder.path}</code> <ExternalLink size={13} /></a>
          {/if}
          <table class="files">
            <thead><tr><th>{t('projects.file.name')}</th><th>{t('projects.file.size')}</th><th>{t('projects.file.date')}</th><th>{t('projects.file.by')}</th></tr></thead>
            <tbody>
              {#each files as f (f.item.id)}
                <tr><td><FileIcon size={14} /> {f.title}</td><td>{f.size ? formatSize(f.size) : ''}</td><td>{fmt().date(f.date)}</td><td>{f.actorName ?? ''}</td></tr>
              {/each}
            </tbody>
          </table>
        {:else if tab === 'workflows'}
          <p class="hint">{t('projects.workflowsIntro')}</p>
          <WorkflowList projectId={p.id} />
        {:else if tab === 'rules'}
          <section class="box rules">
            <h3>{t('projects.rules.title')}</h3>
            <p class="hint">{t('projects.rules.intro')}</p>
            <label class="chk"><input type="checkbox" bind:checked={ruleEnabled} /> {t('projects.rules.enabled')}</label>
            <QueryBar bind:value={ruleQuery} data={app.suggestData} error={parsedRule && !parsedRule.ok ? parsedRule.error : null} onsubmit={() => saveRules(p)} placeholder={'project:' + p.key + ' OR from:@kunde.de'} />
            <div class="row-btns">
              <button class="btn" onclick={() => saveRules(p)} disabled={!!parsedRule && !parsedRule.ok}>{t('common.action.save')}</button>
              <button class="btn btn-primary" onclick={() => applyRules(p)} disabled={!ruleQuery.trim() || (!!parsedRule && !parsedRule.ok)}><Wand size={14} /> {t('projects.rules.applyNow')}</button>
              <button class="btn btn-ghost" onclick={() => (app.organizeDialog = { kind: 'searchFolder', query: ruleQuery || 'project:' + p.key, projectId: p.id })}><FolderSearch size={14} /> {t('organize.searchFolder.saveAs')}</button>
            </div>
          </section>
        {/if}
      </div>
    {:else}
      <div class="empty">
        <FolderKanban size={48} strokeWidth={1.2} aria-hidden="true" />
        <p>{t('projects.empty')}</p>
      </div>
    {/if}
  </div>
</section>

{#if creating}
  <ProjectDialog onclose={() => (creating = false)} onsaved={(p) => (app.selectedProjectId = p.id)} />
{/if}
{#if editing}
  <ProjectDialog project={editing} onclose={() => (editing = null)} />
{/if}
{#if confirmDelete && project}
  {@const p = project}
  <ConfirmDialog title={t('projects.deleteTitle', { name: p.name })} message={t('projects.deleteText')} confirmLabel={t('common.action.delete')} danger onconfirm={() => remove(p)} oncancel={() => (confirmDelete = false)} />
{/if}

<style>
  .projects {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(280px, 340px) 1fr;
    grid-template-rows: minmax(0, 1fr);
    background: var(--tm-bg);
  }
  .list {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-right: 1px solid var(--tm-line);
    background: var(--tm-surface);
  }
  .lhead {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 14px 8px;
  }
  h1 {
    margin: 0;
    font-size: 18px;
    font-weight: 700;
  }
  .filt {
    margin: 0 14px 8px;
    align-self: flex-start;
  }
  .plist {
    position: relative;
    list-style: none;
    margin: 0;
    padding: 0 10px 14px;
    overflow-y: auto;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .pcard {
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 100%;
    padding: 10px 12px 9px 14px;
    border: 1px solid var(--tm-line);
    border-left: 4px solid var(--c);
    border-radius: 10px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .pcard:hover {
    background: var(--tm-surface-2);
  }
  .pcard[aria-current='true'] {
    background: color-mix(in srgb, var(--c) 9%, var(--tm-surface));
    border-color: color-mix(in srgb, var(--c) 50%, var(--tm-line));
  }
  .pc-1 {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .pname {
    flex: 1;
    min-width: 0;
    font-weight: 650;
    font-size: 13.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .pc-2 {
    display: flex;
    gap: 4px;
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
  }
  .key {
    font-family: var(--tm-mono);
    font-size: 11.5px;
  }
  .due.over,
  dd.over {
    color: var(--tm-danger);
    font-weight: 600;
  }
  .pc-3 {
    display: flex;
    gap: 10px;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .pc-3 span {
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .pct {
    margin-left: auto;
    color: var(--tm-muted);
    font-weight: 600;
  }
  .progress {
    display: block;
    height: 4px;
    border-radius: 2px;
    background: var(--tm-line);
    overflow: hidden;
  }
  .progress span {
    display: block;
    height: 100%;
    background: var(--c, var(--tm-accent));
  }
  .progress.big {
    height: 7px;
    border-radius: 4px;
  }
  .st {
    font-size: 10.5px;
    font-weight: 650;
    padding: 1px 7px;
    border-radius: 99px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    flex: none;
  }
  .st-active {
    background: color-mix(in srgb, var(--tm-success) 15%, transparent);
    color: var(--tm-success);
  }
  .st-planned {
    background: color-mix(in srgb, var(--tm-accent) 14%, transparent);
    color: var(--tm-accent-text);
  }
  .st-paused {
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    color: var(--tm-warn);
  }
  .dash {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .dhead {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
  }
  .back {
    display: none;
  }
  .pico {
    width: 38px;
    height: 38px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--c) 16%, transparent);
    color: var(--c);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }
  .dtitle {
    flex: 1;
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: 18px;
    font-weight: 700;
  }
  .sub {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .status-sel {
    width: auto;
  }
  .tabs {
    display: flex;
    gap: 2px;
    padding: 0 16px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tabs button {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 10px 10px 9px;
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--tm-muted);
    font: inherit;
    font-size: 13px;
    font-weight: 550;
    white-space: nowrap;
    cursor: pointer;
  }
  .tabs button[aria-selected='true'] {
    color: var(--tm-fg);
    border-bottom-color: var(--tm-brand);
  }
  .tcount {
    font-size: 11px;
    padding: 0 6px;
    border-radius: 99px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .tabbody {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 16px 20px 28px;
  }
  .ov,
  .two {
    display: grid;
    grid-template-columns: 1.3fr 1fr;
    gap: 14px;
    align-items: start;
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .box {
    padding: 12px 14px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: 12px;
  }
  h3 {
    margin: 0 0 8px;
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-muted);
  }
  .desc {
    margin: 0 0 8px;
    font-size: 14px;
    line-height: 1.5;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 5px 14px;
    margin: 0;
    font-size: 13px;
  }
  .facts dt {
    color: var(--tm-muted);
  }
  .facts dd {
    margin: 0;
  }
  textarea {
    width: 100%;
    resize: vertical;
    font: inherit;
    box-sizing: border-box;
  }
  .tiles {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .tile strong {
    font-size: 22px;
    font-weight: 700;
  }
  .tile span {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .budget {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 15px;
    font-weight: 650;
  }
  .budget small {
    font-weight: 400;
    color: var(--tm-muted);
    font-size: 12px;
  }
  .budget .progress span {
    background: var(--tm-brand);
  }
  .members {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13.5px;
  }
  .members li {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .cloud {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--tm-accent-text);
    text-decoration: none;
  }
  a.cloud.box {
    display: flex;
    margin-bottom: 12px;
  }
  .linkish {
    border: none;
    background: transparent;
    color: var(--tm-accent-text);
    font: inherit;
    cursor: pointer;
    padding: 2px 0;
  }
  .sfl {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
  .sfl small {
    color: var(--tm-faint);
  }
  .kinds {
    display: flex;
    gap: 6px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  .pill {
    height: 28px;
    padding: 0 12px;
    border-radius: 99px;
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    color: var(--tm-muted);
    font: inherit;
    font-size: 12.5px;
    cursor: pointer;
  }
  .pill[aria-checked='true'] {
    background: var(--tm-accent-soft);
    border-color: transparent;
    color: var(--tm-accent-text);
    font-weight: 650;
  }
  .tl {
    list-style: none;
    margin: 0;
    padding: 0 0 0 4px;
    position: relative;
  }
  .tl::before {
    content: '';
    position: absolute;
    left: 17px;
    top: 0;
    bottom: 0;
    border-left: 2px solid var(--tm-line);
  }
  .tl-day {
    position: relative;
    margin: 14px 0 6px 40px;
    font-size: 11.5px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-muted);
  }
  .tl-day:first-child {
    margin-top: 0;
  }
  .tl-item {
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    margin: 0 0 6px;
  }
  .tl-ic {
    position: relative;
    z-index: 1;
    width: 28px;
    height: 28px;
    border-radius: 99px;
    background: var(--tm-surface);
    border: 2px solid var(--tm-line-strong);
    color: var(--tm-muted);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }
  .k-mail .tl-ic {
    border-color: var(--tm-mail);
    color: var(--tm-mail);
  }
  .k-chat .tl-ic {
    border-color: var(--tm-whatsapp);
    color: var(--tm-whatsapp);
  }
  .k-event .tl-ic {
    border-color: #7c3aed;
    color: #7c3aed;
  }
  .k-task .tl-ic {
    border-color: var(--tm-success);
    color: var(--tm-success);
  }
  .k-file .tl-ic {
    border-color: var(--tm-brand);
    color: var(--tm-brand);
  }
  .tl-card {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 7px 10px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .tl-card:disabled {
    cursor: default;
  }
  .tl-card:not(:disabled):hover {
    border-color: var(--tm-accent);
  }
  .future .tl-card {
    border-style: dashed;
  }
  .tl-1 {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .tl-title {
    flex: 1;
    min-width: 0;
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tl-1 time {
    font-size: 11.5px;
    color: var(--tm-muted);
    flex: none;
  }
  .tl-2 {
    display: flex;
    gap: 6px;
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tl-2 span {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tl-2 strong {
    color: var(--tm-fg);
    font-weight: 600;
  }
  .badge-s {
    font-size: 10.5px;
    font-weight: 650;
    padding: 0 6px;
    border-radius: 99px;
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    color: var(--tm-warn);
  }
  .badge-s.done {
    background: color-mix(in srgb, var(--tm-success) 15%, transparent);
    color: var(--tm-success);
  }
  .src {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: 10.5px;
    color: var(--tm-accent-text);
  }
  .tasks,
  .events,
  .contacts {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .tasks li {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .tasks label {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .tasks input {
    accent-color: var(--tm-success);
  }
  .tasks li.done label {
    text-decoration: line-through;
    color: var(--tm-muted);
  }
  .tasks .due {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .events li {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .events li.past {
    opacity: 0.55;
  }
  .ev-date {
    width: 42px;
    height: 42px;
    border-radius: 10px;
    background: color-mix(in srgb, #7c3aed 12%, transparent);
    color: #7c3aed;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    flex: none;
    line-height: 1.1;
  }
  .ev-date small {
    font-size: 10px;
    text-transform: uppercase;
  }
  .ev-txt {
    display: flex;
    flex-direction: column;
    font-size: 13px;
  }
  .ev-txt small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  .contacts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 10px;
  }
  .contacts li {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .ct {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .ct small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  .roles {
    display: flex;
    gap: 4px;
    margin-top: 3px;
  }
  .role {
    font-size: 10.5px;
    font-weight: 650;
    padding: 0 6px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  .role.muted {
    background: var(--tm-surface-2);
    color: var(--tm-muted);
  }
  .files {
    width: 100%;
    border-collapse: collapse;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    overflow: hidden;
    font-size: 13px;
  }
  .files th,
  .files td {
    padding: 8px 12px;
    text-align: left;
    border-bottom: 1px solid var(--tm-line);
  }
  .files th {
    font-size: 11.5px;
    color: var(--tm-muted);
    font-weight: 650;
    background: var(--tm-surface-2);
  }
  .files td :global(svg) {
    vertical-align: -2px;
    color: var(--tm-muted);
  }
  .rules {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-width: 860px;
  }
  .hint {
    margin: 0 0 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .rules .hint {
    margin: 0;
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
  .row-btns {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .empty {
    margin: auto;
    text-align: center;
    color: var(--tm-faint);
  }
  @media (max-width: 1100px) {
    .ov,
    .two {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 899px) {
    .projects {
      grid-template-columns: 1fr;
    }
    .projects.has-selection .list,
    .projects:not(.has-selection) .dash {
      display: none;
    }
    .back {
      display: inline-flex;
    }
  }
  /* Phones: icon, title and the status selector share the first lines, the tabs scroll. */
  @media (max-width: 639px) {
    .dhead {
      flex-wrap: wrap;
      padding: 10px 12px;
      gap: 8px;
    }
    .dtitle {
      flex: 1 1 calc(100% - 140px);
    }
    .status-sel {
      flex: 1 1 auto;
      min-width: 0;
    }
    .tabs button {
      flex: none;
    }
  }
</style>
