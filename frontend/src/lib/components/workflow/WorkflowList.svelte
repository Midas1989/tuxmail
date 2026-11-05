<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Workflow list (Settings -> Workflows and the project tab): toggle, last run, run log, dry run, editor. -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash';
  import History from '@lucide/svelte/icons/history';
  import FlaskConical from '@lucide/svelte/icons/flask-conical';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import CircleX from '@lucide/svelte/icons/circle-x';
  import CircleDashed from '@lucide/svelte/icons/circle-dashed';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Workflow from '@lucide/svelte/icons/workflow';
  import X from '@lucide/svelte/icons/x';
  import SquareFunction from '@lucide/svelte/icons/square-function';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { Workflow as Wf, WorkflowDryRunResult, WorkflowEditorMode, WorkflowRun, WorkflowSchema, WorkflowTemplate } from '../../types';
  import { workflowToRule } from '../../workflow/lang';
  import { ApiError } from '../../api/api';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import WorkflowEditor from './WorkflowEditor.svelte';
  import DryRunPanel from './DryRunPanel.svelte';

  interface Props {
    projectId?: number;
  }
  let { projectId }: Props = $props();

  let schema = $state<WorkflowSchema | null>(null);
  let list = $state<Wf[]>([]);
  let templates = $state<WorkflowTemplate[]>([]);
  let editing = $state<{ w: Wf; mode: WorkflowEditorMode } | null>(null);
  let runsFor = $state<Wf | null>(null);
  let runs = $state<WorkflowRun[]>([]);
  let dryFor = $state<{ id: string; result: WorkflowDryRunResult } | null>(null);
  let confirmEnable = $state<Wf | null>(null);
  let confirmDelete = $state<Wf | null>(null);
  let tplOpen = $state(false);

  async function load() {
    const [s, l, tp] = await Promise.all([app.api.describeWorkflows(), app.api.listWorkflows(projectId != null ? { projectId } : {}), app.api.listWorkflowTemplates()]);
    schema = s;
    list = l;
    templates = tp;
  }
  $effect(() => {
    void projectId;
    void load();
  });

  function blank(): Wf {
    return { id: '', name: '', enabled: false, trigger: { event: 'mail.received' }, actions: [{ type: 'tag', params: { tags: [] } }], projectId };
  }

  function fromTemplate(tp: WorkflowTemplate) {
    tplOpen = false;
    const w: Wf = JSON.parse(JSON.stringify(tp.workflow)) as Wf;
    w.id = '';
    w.templateId = tp.id;
    w.enabled = false;
    w.projectId = projectId;
    const p = projectId != null ? app.project(projectId) : undefined;
    if (p) w.actions = w.actions.map((a) => (a.type === 'add_to_project' ? { ...a, params: { project: p.name } } : a));
    editing = { w, mode: 'visual' };
  }

  async function toggle(w: Wf, confirmed = false) {
    if (!w.enabled && w.externalEffects && !confirmed) {
      confirmEnable = w;
      return;
    }
    try {
      const saved = await app.api.setWorkflowEnabled(w.id, !w.enabled, confirmed);
      list = list.map((x) => (x.id === saved.id ? { ...x, enabled: saved.enabled } : x));
    } catch (err) {
      if (err instanceof ApiError && err.code === 'confirmation_required' && !confirmed) {
        confirmEnable = w;
        return;
      }
      throw err;
    }
  }

  async function openRuns(w: Wf) {
    runsFor = w;
    runs = await app.api.listWorkflowRuns(w.id, { limit: 20 });
  }

  async function dryRun(w: Wf) {
    dryFor = { id: w.id, result: await app.api.dryRunWorkflow(w, { limit: 8 }) };
  }

  function summary(w: Wf): string {
    return schema ? workflowToRule(w, schema, app.workflowContext).src : '';
  }

  function onsaved(w: Wf) {
    const i = list.findIndex((x) => x.id === w.id);
    list = i >= 0 ? list.map((x) => (x.id === w.id ? w : x)) : [...list, w];
    app.toast(t('workflow.saved', { name: w.name }));
  }
</script>

<div class="wl">
  <div class="bar">
    <button class="btn btn-primary" onclick={() => (editing = { w: blank(), mode: 'visual' })}><Plus size={15} /> {t('workflow.new')}</button>
    <button class="btn" onclick={() => (editing = { w: blank(), mode: 'rule' })}><SquareFunction size={15} /> {t('workflow.newExpert')}</button>
    <div class="tpl">
      <button class="btn" aria-expanded={tplOpen} onclick={() => (tplOpen = !tplOpen)}><Workflow size={15} /> {t('workflow.fromTemplate')}</button>
      {#if tplOpen}
        <ul class="tpl-list" aria-label={t('workflow.fromTemplate')}>
          {#each templates as tp (tp.id)}
            <li>
              <button onclick={() => fromTemplate(tp)}>
                <strong>{tp.name}</strong>
                <small>{tp.description}</small>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>

  {#if !list.length}
    <p class="empty">{t('workflow.empty')}</p>
  {/if}
  <ul class="list">
    {#each list as w (w.id)}
      <li class:off={!w.enabled}>
        <div class="main">
          <label class="switch" title={w.enabled ? t('workflow.enabled') : t('workflow.disabled')}>
            <input type="checkbox" checked={w.enabled} onchange={(e) => ((e.currentTarget as HTMLInputElement).checked = w.enabled, toggle(w))} aria-label={t('workflow.toggle', { name: w.name })} />
            <span class="track" aria-hidden="true"><span></span></span>
          </label>
          <div class="txt">
            <span class="name">
              {w.name}
              {#if w.externalEffects}<span class="ext" title={t('workflow.externalHint')}><TriangleAlert size={11} /> {t('workflow.external')}</span>{/if}
              {#if w.projectId != null && projectId == null}{@const p = app.project(w.projectId)}{#if p}<span class="proj" style:--c={p.color}>{p.name}</span>{/if}{/if}
            </span>
            <code class="rule" title={summary(w)}>{summary(w)}</code>
            {#if w.author}<span class="author">{t('workflow.author', { name: w.author })}</span>{/if}
          </div>
          <span class="last" title={w.lastRun?.message ?? ''}>
            {#if w.lastRun}
              {#if w.lastRun.status === 'ok'}<CircleCheck size={14} class="ok" />{:else if w.lastRun.status === 'failed'}<CircleX size={14} class="bad" />{:else}<CircleDashed size={14} class="skip" />{/if}
              {fmt().relative(w.lastRun.startedAt)}
            {:else}
              {t('workflow.neverRun')}
            {/if}
          </span>
          <div class="acts">
            <button class="icon-btn" title={t('workflow.dryRun')} aria-label={t('workflow.dryRun')} onclick={() => dryRun(w)}><FlaskConical size={15} /></button>
            <button class="icon-btn" title={t('workflow.runLog')} aria-label={t('workflow.runLog')} onclick={() => openRuns(w)}><History size={15} /></button>
            <button class="icon-btn" title={t('common.action.edit')} aria-label={t('common.action.edit')} onclick={() => (editing = { w, mode: 'visual' })}><Pencil size={15} /></button>
            <button class="icon-btn" title={t('common.action.delete')} aria-label={t('common.action.delete')} onclick={() => (confirmDelete = w)}><Trash size={15} /></button>
          </div>
        </div>
        {#if dryFor?.id === w.id}
          <DryRunPanel result={dryFor.result} onclose={() => (dryFor = null)} />
        {/if}
      </li>
    {/each}
  </ul>
</div>

{#if runsFor}
  {@const w = runsFor}
  <div class="drawer-bg" role="presentation" onclick={() => (runsFor = null)}></div>
  <aside class="drawer" aria-label={t('workflow.runLogFor', { name: w.name })}>
    <header>
      <History size={16} aria-hidden="true" />
      <strong>{t('workflow.runLogFor', { name: w.name })}</strong>
      <button class="icon-btn" aria-label={t('common.action.close')} onclick={() => (runsFor = null)}><X size={16} /></button>
    </header>
    {#if !runs.length}<p class="empty">{t('workflow.noRuns')}</p>{/if}
    <ol class="runs">
      {#each runs as r (r.id)}
        <li class="run st-{r.status}">
          <div class="r-head">
            {#if r.status === 'ok'}<CircleCheck size={14} class="ok" />{:else if r.status === 'failed'}<CircleX size={14} class="bad" />{:else}<CircleDashed size={14} class="skip" />{/if}
            <span class="r-status">{t(`workflow.status.${r.status}`)}</span>
            <time datetime={r.startedAt}>{fmt().dateTime(r.startedAt)}</time>
          </div>
          {#if r.item}<div class="r-item">{r.item.title}</div>{/if}
          {#if r.message}<div class="r-msg">{r.message}</div>{/if}
          {#if r.steps.length}
            <ul class="steps">
              {#each r.steps as s, i (i)}
                <li class="st-{s.status}"><span class="act">{t(`workflow.action.${s.action}` as 'workflow.action.move')}</span> <span class="sum">{s.summary}</span> <span class="ms">{t('workflow.durationMs', { ms: s.durationMs })}</span></li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ol>
  </aside>
{/if}

{#if editing && schema}
  <WorkflowEditor workflow={editing.w} {schema} initialMode={editing.mode} onclose={() => (editing = null)} {onsaved} />
{/if}
{#if confirmEnable}
  {@const w = confirmEnable}
  <ConfirmDialog
    title={t('workflow.confirmExternal.title')}
    message={t('workflow.confirmExternal.text')}
    confirmLabel={t('workflow.confirmExternal.ok')}
    onconfirm={() => {
      confirmEnable = null;
      void toggle(w, true);
    }}
    oncancel={() => (confirmEnable = null)}
  />
{/if}
{#if confirmDelete}
  {@const w = confirmDelete}
  <ConfirmDialog
    title={t('workflow.deleteTitle', { name: w.name })}
    message={t('workflow.deleteText')}
    confirmLabel={t('common.action.delete')}
    danger
    onconfirm={async () => {
      confirmDelete = null;
      await app.api.deleteWorkflow(w.id);
      list = list.filter((x) => x.id !== w.id);
    }}
    oncancel={() => (confirmDelete = null)}
  />
{/if}

<style>
  .wl {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .bar {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .tpl {
    position: relative;
  }
  .tpl-list {
    position: absolute;
    z-index: 15;
    top: calc(100% + 4px);
    left: 0;
    width: 340px;
    max-height: 360px;
    overflow-y: auto;
    margin: 0;
    padding: 4px;
    list-style: none;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line-strong);
    border-radius: 10px;
    box-shadow: var(--tm-shadow-lg);
  }
  .tpl-list button {
    display: flex;
    flex-direction: column;
    gap: 2px;
    width: 100%;
    padding: 7px 9px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .tpl-list button:hover {
    background: var(--tm-surface-2);
  }
  .tpl-list strong {
    font-size: 13px;
  }
  .tpl-list small {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    overflow: hidden;
  }
  .list > li {
    padding: 8px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .list > li + li {
    border-top: 1px solid var(--tm-line);
  }
  .main {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }
  .off .txt {
    opacity: 0.6;
  }
  .txt {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .name {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  .author {
    font-size: 11px;
    color: var(--tm-muted);
  }
  .rule {
    font-family: var(--tm-mono);
    font-size: 11.5px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ext {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    font-size: 10.5px;
    font-weight: 700;
    color: var(--tm-warn);
  }
  .proj {
    font-size: 10.5px;
    font-weight: 600;
    padding: 0 6px;
    border-radius: 5px;
    border: 1px solid color-mix(in srgb, var(--c) 40%, transparent);
    color: color-mix(in srgb, var(--c) 70%, var(--tm-fg));
  }
  .last {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
  }
  :global(.wl .ok),
  :global(.drawer .ok) {
    color: var(--tm-success);
  }
  :global(.wl .bad),
  :global(.drawer .bad) {
    color: var(--tm-danger);
  }
  :global(.wl .skip),
  :global(.drawer .skip) {
    color: var(--tm-faint);
  }
  .acts {
    display: flex;
  }
  .switch {
    display: inline-flex;
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .track {
    width: 34px;
    height: 20px;
    border-radius: 99px;
    background: var(--tm-line-strong);
    position: relative;
    transition: background var(--tm-dur);
  }
  .track span {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 99px;
    background: #fff;
    transition: transform var(--tm-dur);
  }
  .switch input:checked + .track {
    background: var(--tm-success);
  }
  .switch input:checked + .track span {
    transform: translateX(14px);
  }
  .switch input:focus-visible + .track {
    outline: 2px solid var(--tm-focus);
    outline-offset: 2px;
  }
  .empty {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .drawer-bg {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: rgb(10 14 22 / 0.3);
  }
  .drawer {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    z-index: 61;
    width: min(440px, 100vw);
    background: var(--tm-surface);
    box-shadow: var(--tm-shadow-lg);
    display: flex;
    flex-direction: column;
  }
  .drawer header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--tm-line);
  }
  .drawer header strong {
    flex: 1;
    font-size: 14px;
  }
  .runs {
    list-style: none;
    margin: 0;
    padding: 8px 14px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .run {
    border: 1px solid var(--tm-line);
    border-left: 4px solid var(--tm-success);
    border-radius: 10px;
    padding: 8px 10px;
    font-size: 12.5px;
  }
  .run.st-failed {
    border-left-color: var(--tm-danger);
  }
  .run.st-skipped {
    border-left-color: var(--tm-line-strong);
  }
  .r-head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .r-status {
    font-weight: 650;
  }
  .r-head time {
    margin-left: auto;
    color: var(--tm-muted);
    font-size: 11.5px;
  }
  .r-item {
    margin-top: 2px;
    color: var(--tm-fg);
  }
  .r-msg {
    margin-top: 2px;
    color: var(--tm-danger);
  }
  .st-skipped .r-msg {
    color: var(--tm-muted);
  }
  .steps {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .steps .act {
    font-weight: 600;
  }
  .steps .sum {
    color: var(--tm-muted);
    font-family: var(--tm-mono);
    font-size: 11px;
  }
  .steps .ms {
    color: var(--tm-faint);
    font-size: 11px;
  }
  .steps li.st-failed .act {
    color: var(--tm-danger);
  }
  .steps li.st-skipped {
    opacity: 0.55;
  }
</style>
